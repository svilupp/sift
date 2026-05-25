package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	gosync "sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"golang.org/x/sync/errgroup"

	"sift/internal/bm25"
	"sift/internal/chunk"
	"sift/internal/db"
	"sift/internal/fileutil"
	"sift/internal/ignore"
	"sift/internal/index"
	"sift/internal/voyage"
)

// RefreshOptions controls refresh behavior.
type RefreshOptions struct {
	CollectionName string // Empty = all collections
	Full           bool   // Force full reindex
	DryRun         bool   // Show what would be indexed
	NoIndex        bool   // Skip sift.toml (folder-index) maintenance
	IndexOnly      bool   // Skip chunk/embed entirely; only run sift.toml maintenance
	ChunkOpts      chunk.Options
	SectionOpts    chunk.SectionOptions // Section-based chunking options
	ChunkMode      string               // "row" or "section" (default: "row")
	BatchSize      int                  // Embedding batch size (default 128)

	// IndexHook, when non-nil, is invoked once per maintained folder
	// after Plan completes. It receives the folder absolute path, the
	// previous/next FolderIndex, and the plan. Used by the AI-generation
	// driver to enqueue per-folder jobs without coupling sync→aigen
	// directly. Errors from the hook are logged as warnings.
	IndexHook func(folder string, plan *index.MaintainPlan) error
}

// RefreshStats tracks what happened during a refresh.
type RefreshStats struct {
	FilesScanned   int
	FilesNew       int
	FilesChanged   int
	FilesDeleted   int
	ChunksTotal    int
	ChunksEmbedded int
	EmbedTokens    int
	EmbedErrors    int
	Duration       time.Duration

	// IndexFoldersScanned counts the folders considered for sift.toml
	// maintenance (zero when NoIndex is set or no collections were
	// scanned).
	IndexFoldersScanned int
	// IndexFoldersWritten counts how many sift.toml files Apply
	// actually wrote on this run.
	IndexFoldersWritten int
	// IndexFoldersCreated counts brand-new sift.toml files written.
	IndexFoldersCreated int
}

type fileUpdate struct {
	file        fsFile
	primary     db.Collection
	memberships []db.Collection
}

type membershipUpdate struct {
	fileID        int64
	primaryID     int64
	collectionIDs []int64
}

type collectionMatcher struct {
	collections []db.Collection
	ignoreByID  map[int64][]string
}

// Refresh performs an incremental index refresh.
// If voyageClient is nil, embedding is skipped (BM25-only mode).
func Refresh(ctx context.Context, database *db.DB, bleveIdx *bm25.BleveIndex, voyageClient *voyage.Client, opts RefreshOptions, w io.Writer) (*RefreshStats, error) {
	stats := &RefreshStats{}
	start := time.Now()

	if opts.BatchSize <= 0 {
		opts.BatchSize = 128
	}

	allCollections, err := database.ListCollections()
	if err != nil {
		return nil, err
	}
	if len(allCollections) == 0 {
		fmt.Fprintln(w, "No collections registered. Use: sift collections add <name> <path>")
		return stats, nil
	}

	scanCollections, err := selectScanCollections(allCollections, opts.CollectionName)
	if err != nil {
		return nil, err
	}

	matcher := newCollectionMatcher(allCollections, loadIgnorePatterns(allCollections, w))
	seenScanned := make(map[string]struct{})

	for _, col := range scanCollections {
		if err := refreshCollection(ctx, database, bleveIdx, voyageClient, col, matcher, opts, stats, seenScanned, w); err != nil {
			return stats, fmt.Errorf("refresh %q: %w", col.Name, err)
		}
	}

	stats.Duration = time.Since(start)
	return stats, nil
}

// RefreshFiles indexes specific files by path.
// Each file is resolved to its parent collection via DB lookup or path prefix match.
// If voyageClient is nil, embedding is skipped (BM25-only mode).
func RefreshFiles(ctx context.Context, database *db.DB, bleveIdx *bm25.BleveIndex, voyageClient *voyage.Client, paths []string, opts RefreshOptions, w io.Writer) (*RefreshStats, error) {
	stats := &RefreshStats{}
	start := time.Now()

	if opts.BatchSize <= 0 {
		opts.BatchSize = 128
	}

	collections, err := database.ListCollections()
	if err != nil {
		return nil, fmt.Errorf("list collections: %w", err)
	}
	if len(collections) == 0 {
		return nil, fmt.Errorf("no collections registered; use: sift collections add <name> <path>")
	}

	matcher := newCollectionMatcher(collections, loadIgnorePatterns(collections, w))

	// Cache per-collection `ignore = true` folder discovery. We populate
	// lazily — most refreshes touch one or two collections, so paying the
	// walk cost only for affected collections is the right trade-off.
	ignoredCache := map[int64]map[string]struct{}{}
	ignoredFor := func(col db.Collection) map[string]struct{} {
		if v, ok := ignoredCache[col.ID]; ok {
			return v
		}
		set, err := discoverIgnoredFolders(col.Path)
		if err != nil {
			fmt.Fprintf(w, "  Warning: discover ignored folders for %s: %v\n", col.Name, err)
		}
		if set == nil {
			set = map[string]struct{}{}
		}
		ignoredCache[col.ID] = set
		return set
	}

	var updates []fileUpdate

	for _, p := range paths {
		absPath, err := filepath.Abs(p)
		if err != nil {
			fmt.Fprintf(w, "  Skipping %s: %v\n", p, err)
			continue
		}

		info, err := os.Stat(absPath)
		if err != nil {
			fmt.Fprintf(w, "  Skipping %s: %v\n", p, err)
			continue
		}
		if info.IsDir() {
			fmt.Fprintf(w, "  Skipping %s: is a directory\n", p)
			continue
		}

		if !fileutil.IsIndexableText(absPath) {
			ext := strings.ToLower(filepath.Ext(absPath))
			fmt.Fprintf(w, "  Skipping %s: unsupported file type %q\n", p, ext)
			continue
		}
		if isFolderIndexFile(absPath) {
			fmt.Fprintf(w, "  Skipping %s: sift.toml is metadata, not indexed content\n", p)
			continue
		}

		matches := matcher.match(absPath)
		if len(matches) == 0 {
			fmt.Fprintf(w, "  Skipping %s: not inside any registered collection\n", p)
			continue
		}

		// Honor ancestor `ignore = true` sift.toml inheritance — if any
		// parent folder (within any matching collection) is marked
		// ignored, the file is skipped. Without this, `sift refresh
		// path/to/file.md` would index files inside an ignore=true
		// subtree that a full `sift refresh` would otherwise prune.
		ignored := false
		for _, col := range matches {
			if hasIgnoredAncestor(absPath, ignoredFor(col)) {
				ignored = true
				break
			}
		}
		if ignored {
			fmt.Fprintf(w, "  Skipping %s: inside an ignore=true folder\n", p)
			continue
		}

		existing, _ := database.GetFileByPath(absPath)
		if existing == nil {
			stats.FilesNew++
		} else {
			stats.FilesChanged++
		}

		updates = append(updates, fileUpdate{
			file: fsFile{
				path:      absPath,
				mtime:     info.ModTime().Unix(),
				sizeBytes: info.Size(),
			},
			primary:     matches[0],
			memberships: matches,
		})
	}

	total := len(updates)
	if total == 0 {
		fmt.Fprintln(w, "No valid files to refresh.")
		stats.Duration = time.Since(start)
		return stats, nil
	}

	fmt.Fprintf(w, "Refreshing %d files (%d new, %d changed)\n", total, stats.FilesNew, stats.FilesChanged)

	if opts.DryRun {
		for _, update := range updates {
			fmt.Fprintf(w, "  + %s\n", update.file.path)
		}
		stats.Duration = time.Since(start)
		return stats, nil
	}

	if err := indexFiles(ctx, database, bleveIdx, voyageClient, updates, opts, stats, w); err != nil {
		return stats, fmt.Errorf("refresh files: %w", err)
	}

	// Maintain folder-index files for the parent folders of the changed
	// files. We re-scan each affected folder (cheap) so the plan reflects
	// every sibling, not just the targeted file. The matcher is threaded
	// through so siblings excluded by .siftignore are not silently
	// re-added to the parent folder's sift.toml.
	maintainTargetedFolders(ctx, updates, matcher, opts, stats, w)

	stats.Duration = time.Since(start)
	return stats, nil
}

func selectScanCollections(collections []db.Collection, name string) ([]db.Collection, error) {
	if name == "" {
		return collections, nil
	}

	for _, col := range collections {
		if col.Name == name {
			return []db.Collection{col}, nil
		}
	}
	return nil, fmt.Errorf("collection %q not found", name)
}

func loadIgnorePatterns(collections []db.Collection, w io.Writer) map[int64][]string {
	patterns := make(map[int64][]string, len(collections))
	for _, col := range collections {
		p, err := ignore.LoadPatterns(col.Path)
		if err != nil {
			fmt.Fprintf(w, "  Warning: reading .siftignore for %s: %v\n", col.Name, err)
			continue
		}
		patterns[col.ID] = p
	}
	return patterns
}

func newCollectionMatcher(collections []db.Collection, ignoreByID map[int64][]string) collectionMatcher {
	return collectionMatcher{
		collections: collections,
		ignoreByID:  ignoreByID,
	}
}

func (m collectionMatcher) match(absPath string) []db.Collection {
	matches := make([]db.Collection, 0, len(m.collections))
	for _, col := range m.collections {
		if !pathWithinCollection(absPath, col.Path) {
			continue
		}
		if ignore.ShouldIgnore(absPath, col.Path, m.ignoreByID[col.ID]) {
			continue
		}
		matches = append(matches, col)
	}

	sort.Slice(matches, func(i, j int) bool {
		if len(matches[i].Path) != len(matches[j].Path) {
			return len(matches[i].Path) > len(matches[j].Path)
		}
		if matches[i].Name != matches[j].Name {
			return matches[i].Name < matches[j].Name
		}
		return matches[i].ID < matches[j].ID
	})

	return matches
}

func pathWithinCollection(absPath string, root string) bool {
	absPath = filepath.Clean(absPath)
	root = filepath.Clean(root)
	if absPath == root {
		return true
	}
	return strings.HasPrefix(absPath, root+string(os.PathSeparator))
}

// isFolderIndexFile reports whether path refers to a sift.toml folder-index
// metadata file. The check is filename-only, so it works for absolute paths,
// relative paths, and paths produced by filepath.Walk/WalkDir alike.
func isFolderIndexFile(path string) bool {
	return filepath.Base(path) == "sift.toml"
}

func collectionIDs(matches []db.Collection) []int64 {
	ids := make([]int64, len(matches))
	for i, match := range matches {
		ids[i] = match.ID
	}
	return ids
}

func sameCollectionIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}

	ac := append([]int64(nil), a...)
	bc := append([]int64(nil), b...)
	sort.Slice(ac, func(i, j int) bool { return ac[i] < ac[j] })
	sort.Slice(bc, func(i, j int) bool { return bc[i] < bc[j] })

	for i := range ac {
		if ac[i] != bc[i] {
			return false
		}
	}
	return true
}

// resolveCollection returns the most specific matching collection for a path.
// Kept for tests and compatibility with older call sites.
func resolveCollection(_ *db.DB, absPath string, collections []db.Collection) (*db.Collection, error) {
	matches := newCollectionMatcher(collections, nil).match(absPath)
	if len(matches) == 0 {
		return nil, fmt.Errorf("not inside any registered collection")
	}
	return &matches[0], nil
}

// pendingEmbed tracks a chunk awaiting embedding.
type pendingEmbed struct {
	chunkID int64
	content string
	path    string
}

// processedFile holds the result of parallel file reading/chunking.
type processedFile struct {
	update fileUpdate
	hash   string
	chunks []chunk.SectionChunk
	title  string
}

// readAndProcessFile reads a file once and returns hash, chunks, and title.
func readAndProcessFile(path string, chunkOpts chunk.Options, sectionOpts chunk.SectionOptions, mode string) (hash string, chunks []chunk.SectionChunk, title string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, "", err
	}

	h := xxhash.Sum64(data)
	hash = fmt.Sprintf("%016x", h)

	lines := strings.Split(string(data), "\n")
	title = chunk.ExtractTitle(lines)

	if mode == "section" {
		chunks = chunk.FromSections(path, data, sectionOpts)
	} else {
		rowChunks := chunk.FromLines(lines, chunkOpts)
		chunks = chunk.WrapRowChunks(rowChunks)
	}
	return hash, chunks, title, nil
}

func refreshCollection(ctx context.Context, database *db.DB, bleveIdx *bm25.BleveIndex, voyageClient *voyage.Client, col db.Collection, matcher collectionMatcher, opts RefreshOptions, stats *RefreshStats, seenScanned map[string]struct{}, w io.Writer) error {
	fmt.Fprintf(w, "  Scanning: %s (%s)\n", col.Name, col.Path)

	// Discover folders flagged with `ignore = true` in their `sift.toml`.
	// Children of those folders inherit the ignore: skipped from chunking
	// AND from index maintenance.
	ignoredFolders, err := discoverIgnoredFolders(col.Path)
	if err != nil {
		fmt.Fprintf(w, "  Warning: scanning sift.toml ignore flags: %v\n", err)
	}

	// Walk filesystem to find text files.
	fsFiles, err := scanDirectory(col.Path, matcher.ignoreByID[col.ID], ignoredFolders)
	if err != nil {
		return fmt.Errorf("scan directory: %w", err)
	}
	for _, f := range fsFiles {
		if _, ok := seenScanned[f.path]; ok {
			continue
		}
		seenScanned[f.path] = struct{}{}
		stats.FilesScanned++
	}

	// Get existing files from DB.
	dbFiles, err := database.GetFilesByCollection(col.ID)
	if err != nil {
		return fmt.Errorf("get db files: %w", err)
	}
	dbFileMap := make(map[string]*db.FileRecord, len(dbFiles))
	for i := range dbFiles {
		dbFileMap[dbFiles[i].Path] = &dbFiles[i]
	}

	// Classify files.
	var toIndex []fileUpdate
	var membershipOnly []membershipUpdate
	var toDelete []int64
	localNew := 0
	localChanged := 0
	localDeleted := 0

	for _, f := range fsFiles {
		matches := matcher.match(f.path)
		if len(matches) == 0 {
			delete(dbFileMap, f.path)
			continue
		}

		existing := dbFileMap[f.path]
		if existing == nil {
			toIndex = append(toIndex, fileUpdate{
				file:        f,
				primary:     matches[0],
				memberships: matches,
			})
			stats.FilesNew++
			localNew++
			delete(dbFileMap, f.path)
			continue
		}

		existingMemberships, err := database.GetFileCollectionIDs(existing.ID)
		if err != nil {
			fmt.Fprintf(w, "  Warning: load memberships for %s: %v\n", f.path, err)
			delete(dbFileMap, f.path)
			continue
		}

		targetIDs := collectionIDs(matches)
		membershipChanged := !sameCollectionIDs(existingMemberships, targetIDs)
		primaryChanged := existing.CollectionID != matches[0].ID
		needsReindex := opts.Full || primaryChanged
		if !needsReindex && existing.Mtime != f.mtime {
			hash, hashErr := hashFile(f.path)
			if hashErr != nil {
				fmt.Fprintf(w, "  Warning: hash failed for %s: %v\n", f.path, hashErr)
				delete(dbFileMap, f.path)
				continue
			}
			needsReindex = hash != existing.FileHash
		}

		switch {
		case needsReindex:
			toIndex = append(toIndex, fileUpdate{
				file:        f,
				primary:     matches[0],
				memberships: matches,
			})
			stats.FilesChanged++
			localChanged++
		case membershipChanged:
			membershipOnly = append(membershipOnly, membershipUpdate{
				fileID:        existing.ID,
				primaryID:     matches[0].ID,
				collectionIDs: targetIDs,
			})
			stats.FilesChanged++
			localChanged++
		}

		delete(dbFileMap, f.path)
	}

	// Remaining dbFiles are no longer visible to this collection.
	for _, f := range dbFileMap {
		info, statErr := os.Stat(f.Path)
		if statErr != nil {
			if os.IsNotExist(statErr) {
				toDelete = append(toDelete, f.ID)
				stats.FilesDeleted++
				localDeleted++
			} else {
				fmt.Fprintf(w, "  Warning: stat failed for %s: %v\n", f.Path, statErr)
			}
			continue
		}

		matches := matcher.match(f.Path)
		if len(matches) == 0 {
			toDelete = append(toDelete, f.ID)
			stats.FilesDeleted++
			localDeleted++
			continue
		}

		existingMemberships, err := database.GetFileCollectionIDs(f.ID)
		if err != nil {
			fmt.Fprintf(w, "  Warning: load memberships for %s: %v\n", f.Path, err)
			continue
		}

		targetIDs := collectionIDs(matches)
		membershipChanged := !sameCollectionIDs(existingMemberships, targetIDs)
		primaryChanged := f.CollectionID != matches[0].ID
		if !membershipChanged && !primaryChanged {
			continue
		}

		update := fileUpdate{
			file: fsFile{
				path:      f.Path,
				mtime:     info.ModTime().Unix(),
				sizeBytes: info.Size(),
			},
			primary:     matches[0],
			memberships: matches,
		}

		if primaryChanged {
			toIndex = append(toIndex, update)
		} else {
			membershipOnly = append(membershipOnly, membershipUpdate{
				fileID:        f.ID,
				primaryID:     matches[0].ID,
				collectionIDs: targetIDs,
			})
		}
		stats.FilesChanged++
		localChanged++
	}

	changed := localNew + localChanged + localDeleted
	fmt.Fprintf(w, "  Changed: %d files (%d new, %d modified, %d deleted)\n",
		changed, localNew, localChanged, localDeleted)

	if opts.DryRun {
		for _, update := range toIndex {
			fmt.Fprintf(w, "    + %s\n", update.file.path)
		}
		for _, id := range toDelete {
			fmt.Fprintf(w, "    - file_id=%d\n", id)
		}
		return nil
	}

	// Delete removed files.
	for _, fileID := range toDelete {
		chunks, err := database.GetChunksByFile(fileID)
		if err != nil {
			return fmt.Errorf("get chunks for delete: %w", err)
		}
		for _, c := range chunks {
			if err := bleveIdx.Delete(strconv.FormatInt(c.ID, 10)); err != nil {
				fmt.Fprintf(w, "  Warning: bleve delete failed: %v\n", err)
			}
		}
		if err := database.DeleteFile(fileID); err != nil {
			return fmt.Errorf("delete file: %w", err)
		}
	}

	if err := applyMembershipUpdates(database, membershipOnly); err != nil {
		return fmt.Errorf("apply membership updates: %w", err)
	}

	if !opts.IndexOnly {
		if err := indexFiles(ctx, database, bleveIdx, voyageClient, toIndex, opts, stats, w); err != nil {
			return err
		}
	}

	// Maintain folder-index `sift.toml` files for every folder in the
	// collection (after all chunking/embedding writes have completed).
	maintainCollectionIndex(ctx, col.Path, fsFiles, ignoredFolders, matcher.ignoreByID[col.ID], opts, stats, w)
	return nil
}

// indexFiles processes a list of files: read/chunk, DB write, Bleve index, embed.
// Shared pipeline used by both collection refresh and single-file refresh.
func indexFiles(ctx context.Context, database *db.DB, bleveIdx *bm25.BleveIndex, voyageClient *voyage.Client, toIndex []fileUpdate, opts RefreshOptions, stats *RefreshStats, w io.Writer) error {
	if len(toIndex) == 0 {
		return nil
	}

	fmt.Fprintf(w, "  Chunking: ")

	// Parallel read/hash/chunk.
	var mu gosync.Mutex
	processed := make([]processedFile, 0, len(toIndex))
	var processWarnings []string

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())

	for _, update := range toIndex {
		g.Go(func() error {
			if gctx.Err() != nil {
				return gctx.Err()
			}

			hash, chunks, title, err := readAndProcessFile(update.file.path, opts.ChunkOpts, opts.SectionOpts, opts.ChunkMode)
			if err != nil {
				mu.Lock()
				processWarnings = append(processWarnings, fmt.Sprintf("read failed for %s: %v", update.file.path, err))
				mu.Unlock()
				return nil // non-fatal
			}

			mu.Lock()
			processed = append(processed, processedFile{update: update, hash: hash, chunks: chunks, title: title})
			mu.Unlock()
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return fmt.Errorf("file processing: %w", err)
	}

	for _, warn := range processWarnings {
		fmt.Fprintf(w, "\n  Warning: %s", warn)
	}

	// Serial DB transactions + Bleve doc building.
	allBleveDocs := make(map[string]bm25.BleveDoc)
	var allPendingEmbeds []pendingEmbed
	var allOldBleveIDs []string

	for _, pf := range processed {
		relPath := strings.TrimPrefix(pf.update.file.path, pf.update.primary.Path)
		relPath = strings.TrimPrefix(relPath, "/")

		// Get old chunks for Bleve cleanup.
		existingFile, err := database.GetFileByPath(pf.update.file.path)
		if err != nil {
			fmt.Fprintf(w, "\n  Warning: get existing file %s: %v", pf.update.file.path, err)
			continue
		}
		if existingFile != nil {
			oldChunks, err := database.GetChunksByFile(existingFile.ID)
			if err != nil {
				fmt.Fprintf(w, "\n  Warning: get old chunks %s: %v", pf.update.file.path, err)
				continue
			}
			for _, c := range oldChunks {
				allOldBleveIDs = append(allOldBleveIDs, strconv.FormatInt(c.ID, 10))
			}
		}

		// DB transaction.
		var newChunkIDs []int64
		err = database.WithTx(func(tx *db.Tx) error {
			fileID, err := tx.UpsertFile(pf.update.file.path, pf.update.primary.ID, pf.hash, pf.update.file.mtime, pf.update.file.sizeBytes, pf.title)
			if err != nil {
				return fmt.Errorf("upsert file: %w", err)
			}
			if err := tx.ReplaceFileCollections(fileID, collectionIDs(pf.update.memberships)); err != nil {
				return fmt.Errorf("replace file collections: %w", err)
			}
			if err := tx.DeleteChunksByFile(fileID); err != nil {
				return fmt.Errorf("delete old chunks: %w", err)
			}
			if err := tx.DeleteLinksByFile(fileID); err != nil {
				return fmt.Errorf("delete old links: %w", err)
			}
			newChunkIDs = make([]int64, len(pf.chunks))
			for i, sc := range pf.chunks {
				var parentChunkID *int64
				if sc.ParentIdx >= 0 && sc.ParentIdx < i {
					parentChunkID = &newChunkIDs[sc.ParentIdx]
				}
				cid, err := tx.InsertChunkV2(fileID, sc.Order, sc.StartLine, sc.EndLine, sc.CharCount,
					sc.SectionID, sc.Heading, sc.HeadingLevel, sc.ChunkHash, parentChunkID)
				if err != nil {
					return fmt.Errorf("insert chunk: %w", err)
				}
				newChunkIDs[i] = cid
			}
			// Store links from section chunks.
			for _, sc := range pf.chunks {
				for _, link := range sc.Links {
					_ = tx.InsertLink(fileID, sc.SectionID, 0, link.TargetPath, link.TargetSection, link.LinkType, link.Raw)
				}
			}
			return tx.UpdateFileChunkCount(fileID, len(pf.chunks))
		})
		if err != nil {
			fmt.Fprintf(w, "\n  Warning: index failed for %s: %v", pf.update.file.path, err)
			continue
		}

		stats.ChunksTotal += len(newChunkIDs)

		// Build Bleve docs and pending embeds.
		for i, sc := range pf.chunks {
			provenanceContent := chunk.PrependProvenance(sc.Content, relPath, pf.title, pf.update.primary.Name)
			docID := strconv.FormatInt(newChunkIDs[i], 10)
			allBleveDocs[docID] = bm25.BleveDoc{
				Content: provenanceContent,
				Path:    pf.update.file.path,
			}
			if voyageClient != nil && strings.TrimSpace(sc.Content) != "" {
				allPendingEmbeds = append(allPendingEmbeds, pendingEmbed{
					chunkID: newChunkIDs[i],
					content: provenanceContent,
					path:    pf.update.file.path,
				})
			}
		}
	}

	fmt.Fprintf(w, "%d chunks\n", stats.ChunksTotal)

	// Apply Bleve deletes and adds in a single batch to minimize inconsistency windows.
	if err := bleveIdx.ApplyBatch(allOldBleveIDs, allBleveDocs); err != nil {
		return fmt.Errorf("bleve batch apply: %w", err)
	}

	// Parallel embedding.
	if voyageClient != nil && len(allPendingEmbeds) > 0 {
		fmt.Fprintf(w, "  Embedding: %d chunks", len(allPendingEmbeds))

		embedG, embedCtx := errgroup.WithContext(ctx)
		embedG.SetLimit(5)

		var embedMu gosync.Mutex

		const maxTokensPerBatch = 500_000 // conservative under Voyage's 600K limit

		i := 0
		for i < len(allPendingEmbeds) {
			estimatedTokens := 0
			end := i
			for end < len(allPendingEmbeds) && end-i < opts.BatchSize {
				// ~1 token per 4 chars + overhead
				chunkTokens := len(allPendingEmbeds[end].content)/4 + 50
				if estimatedTokens+chunkTokens > maxTokensPerBatch {
					break
				}
				estimatedTokens += chunkTokens
				end++
			}
			if end == i {
				// Single chunk exceeds limit — include it anyway (let API handle it)
				end = i + 1
			}
			batch := allPendingEmbeds[i:end]
			i = end

			embedG.Go(func() error {
				if embedCtx.Err() != nil {
					return nil
				}

				texts := make([]string, len(batch))
				for j, pe := range batch {
					texts[j] = pe.content
				}

				// Retry loop for the batch.
				const maxBatchRetries = 3
				var vectors [][]float32
				var usage voyage.Usage
				var embedErr error
				batchBackoff := 2 * time.Second

				embedStart := time.Now()
				for batchAttempt := 0; batchAttempt <= maxBatchRetries; batchAttempt++ {
					if batchAttempt > 0 {
						// Exponential backoff with jitter before retry.
						jitter := time.Duration(rand.Int64N(int64(batchBackoff) / 2))
						sleepDur := batchBackoff + jitter
						log.Printf("embed: batch retry %d/%d after %v (%d chunks)", batchAttempt, maxBatchRetries, sleepDur, len(batch))
						select {
						case <-embedCtx.Done():
						case <-time.After(sleepDur):
						}
						if embedCtx.Err() != nil {
							embedErr = embedCtx.Err()
							break
						}
						batchBackoff *= 2
					}

					vectors, usage, embedErr = voyageClient.Embed(embedCtx, texts, "document")
					if embedErr == nil {
						break
					}
				}
				latencyMs := time.Since(embedStart).Milliseconds()

				if embedErr != nil {
					embedMu.Lock()
					stats.EmbedErrors += len(batch)
					embedMu.Unlock()

					for _, pe := range batch {
						chunkInfo, _ := json.Marshal(map[string]int64{"chunk_id": pe.chunkID})
						_ = database.InsertDeadLetter("embed", pe.path, string(chunkInfo), embedErr.Error(), "")
					}
					return nil
				}

				var batchEmbedded int
				var batchErrors int
				var batchTokens int
				for j, pe := range batch {
					if j >= len(vectors) {
						break
					}
					var vecBytes []byte
					var dims int
					if bm25.IsBinaryDtype(voyageClient.EmbedDtype) {
						vecBytes = bm25.EncodeBinaryVector(vectors[j])
						dims = voyageClient.EmbedDimensions
					} else {
						vecBytes = bm25.EncodeVector(vectors[j])
						dims = len(vectors[j])
					}
					if err := database.UpsertEmbedding(pe.chunkID, vecBytes, voyageClient.EmbedModel, dims); err != nil {
						batchErrors++
						continue
					}
					batchEmbedded++
				}

				batchTokens = usage.TotalTokens

				_ = database.InsertAPIUsage("embed", 1, usage.TotalTokens, latencyMs)

				embedMu.Lock()
				stats.ChunksEmbedded += batchEmbedded
				stats.EmbedErrors += batchErrors
				stats.EmbedTokens += batchTokens
				embedMu.Unlock()

				return nil
			})
		}

		_ = embedG.Wait()

		fmt.Fprintf(w, " done (%d embedded, %d errors, %d tokens)\n",
			stats.ChunksEmbedded, stats.EmbedErrors, stats.EmbedTokens)
	}

	// Post-embed retry sweep: attempt to embed chunks that failed in the main pass.
	if stats.EmbedErrors > 0 && voyageClient != nil {
		retryEmbedFailures(ctx, database, voyageClient, stats, w)
	}

	return nil
}

func applyMembershipUpdates(database *db.DB, updates []membershipUpdate) error {
	for _, update := range updates {
		err := database.WithTx(func(tx *db.Tx) error {
			if err := tx.UpdateFilePrimaryCollection(update.fileID, update.primaryID); err != nil {
				return err
			}
			if err := tx.ReplaceFileCollections(update.fileID, update.collectionIDs); err != nil {
				return err
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

// retryEmbedFailures does a sequential retry sweep for chunks that have no embedding.
// Uses smaller batches and longer pauses to work around rate limits.
func retryEmbedFailures(ctx context.Context, database *db.DB, voyageClient *voyage.Client, stats *RefreshStats, w io.Writer) {
	// Find chunks without embeddings from the most recent indexing.
	rows, err := database.QueryChunksWithoutEmbeddings()
	if err != nil || len(rows) == 0 {
		return
	}

	// Filter out empty-content chunks that can never be embedded.
	var validRows []db.ChunkWithFile
	for _, r := range rows {
		if r.StartLine > 0 && r.EndLine >= r.StartLine {
			content := fileutil.ReadLines(r.FilePath, r.StartLine, r.EndLine, 0)
			if strings.TrimSpace(content) != "" {
				validRows = append(validRows, r)
			}
		}
	}
	if len(validRows) == 0 {
		return
	}

	fmt.Fprintf(w, "  Retry sweep: %d chunks without embeddings (%d skipped empty)\n", len(validRows), len(rows)-len(validRows))

	const retryBatchSize = 20
	backoff := 1 * time.Second
	retried := 0
	retryErrors := 0

	for i := 0; i < len(validRows); i += retryBatchSize {
		if ctx.Err() != nil {
			break
		}

		end := i + retryBatchSize
		if end > len(validRows) {
			end = len(validRows)
		}
		batch := validRows[i:end]

		// Pause between batches (not before first).
		if i > 0 {
			time.Sleep(backoff)
			backoff = min(backoff*2, 10*time.Second) // cap at 10s
		}

		// Read chunk content from disk and embed.
		texts := make([]string, len(batch))
		for j, cr := range batch {
			texts[j] = fileutil.ReadLines(cr.FilePath, cr.StartLine, cr.EndLine, 0)
		}

		vectors, _, err := voyageClient.Embed(ctx, texts, "document")
		if err != nil {
			retryErrors += len(batch)
			log.Printf("embed retry: batch failed: %v", err)
			continue
		}

		for j, cr := range batch {
			if j >= len(vectors) {
				break
			}
			var vecBytes []byte
			var dims int
			if bm25.IsBinaryDtype(voyageClient.EmbedDtype) {
				vecBytes = bm25.EncodeBinaryVector(vectors[j])
				dims = voyageClient.EmbedDimensions
			} else {
				vecBytes = bm25.EncodeVector(vectors[j])
				dims = len(vectors[j])
			}
			if err := database.UpsertEmbedding(cr.ChunkID, vecBytes, voyageClient.EmbedModel, dims); err != nil {
				retryErrors++
				continue
			}
			retried++
		}
	}

	// Update stats.
	stats.ChunksEmbedded += retried
	stats.EmbedErrors -= retried // reduce error count by recovered chunks

	fmt.Fprintf(w, "  Retry sweep: recovered %d, %d still failed\n", retried, retryErrors)
}

type fsFile struct {
	path      string
	mtime     int64
	sizeBytes int64
}

func scanDirectory(dir string, patterns []string, ignoredFolders map[string]struct{}) ([]fsFile, error) {
	var files []fsFile

	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == ".obsidian" || name == "node_modules" ||
				name == ".venv" || name == "venv" || name == "__pycache__" {
				return filepath.SkipDir
			}
			if ignore.ShouldIgnore(path, dir, patterns) {
				return filepath.SkipDir
			}
			if _, skip := ignoredFolders[filepath.Clean(path)]; skip {
				return filepath.SkipDir
			}
			return nil
		}

		if !fileutil.IsIndexableText(path) {
			return nil
		}

		if isFolderIndexFile(path) {
			// sift.toml is the folder-index metadata file; never chunk
			// or index it as content.
			return nil
		}

		if ignore.ShouldIgnore(path, dir, patterns) {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return nil // Skip files we can't stat.
		}

		files = append(files, fsFile{
			path:      path,
			mtime:     info.ModTime().Unix(),
			sizeBytes: info.Size(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%016x", xxhash.Sum64(data)), nil
}

// discoverIgnoredFolders walks `root` looking for `sift.toml` files with
// `ignore = true`. The returned set contains the absolute, cleaned paths
// of those folders. Folders whose ancestors are already in the set are
// not pruned here — callers should still match by prefix when honoring
// the inheritance rule.
func discoverIgnoredFolders(root string) (map[string]struct{}, error) {
	ignored := map[string]struct{}{}
	root = filepath.Clean(root)

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == ".obsidian" || name == "node_modules" ||
				name == ".venv" || name == "venv" || name == "__pycache__" {
				return filepath.SkipDir
			}
			// If this folder is already under a known ignored ancestor,
			// no need to look inside.
			if hasIgnoredAncestor(path, ignored) {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Base(path) != index.FilenameSiftToml {
			return nil
		}
		idx, loadErr := index.Load(path)
		if loadErr != nil {
			return nil
		}
		if idx != nil && idx.Ignore {
			ignored[filepath.Clean(filepath.Dir(path))] = struct{}{}
		}
		return nil
	})
	return ignored, walkErr
}

// hasIgnoredAncestor reports whether path lies inside any folder in the
// `ignored` set (excluding path itself).
func hasIgnoredAncestor(path string, ignored map[string]struct{}) bool {
	cleaned := filepath.Clean(path)
	for ig := range ignored {
		if cleaned == ig {
			continue
		}
		if strings.HasPrefix(cleaned, ig+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

// maintainCollectionIndex walks the collection root and runs
// `index.Plan(...)` (and `Apply` unless `--no-index` or `--dry-run`) for
// every folder that holds an indexed file or already has a `sift.toml`.
// Folders flagged `ignore = true` (and their descendants) are skipped,
// as are folders matched by the collection's `.siftignore` patterns.
func maintainCollectionIndex(ctx context.Context, root string, scanned []fsFile, ignoredFolders map[string]struct{}, ignorePatterns []string, opts RefreshOptions, stats *RefreshStats, w io.Writer) {
	root = filepath.Clean(root)

	// Group scanned files by parent folder (absolute path -> []rel).
	filesByFolder := map[string][]string{}
	for _, f := range scanned {
		dir := filepath.Clean(filepath.Dir(f.path))
		filesByFolder[dir] = append(filesByFolder[dir], filepath.Base(f.path))
	}

	// Walk the tree to discover every folder under `root`. We need this
	// even for folders without scanned files — they may still own a
	// `sift.toml` that needs deletion entries to be reconciled.
	folders := map[string][]string{} // folder -> direct child folder names
	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		name := d.Name()
		if path != root && (name == ".git" || name == ".obsidian" || name == "node_modules" ||
			name == ".venv" || name == "venv" || name == "__pycache__") {
			return filepath.SkipDir
		}
		cleaned := filepath.Clean(path)
		if hasIgnoredAncestor(cleaned, ignoredFolders) {
			return filepath.SkipDir
		}
		if _, isIgnored := ignoredFolders[cleaned]; isIgnored {
			// Self-ignored: skip descent and skip maintenance, but the
			// folder's own `sift.toml` stays untouched.
			return filepath.SkipDir
		}
		// Honor `.siftignore`: do not descend into matched folders, and
		// do not record them as tracked children of their parent.
		if path != root && len(ignorePatterns) > 0 && ignore.ShouldIgnore(cleaned, root, ignorePatterns) {
			return filepath.SkipDir
		}
		if _, ok := folders[cleaned]; !ok {
			folders[cleaned] = nil
		}
		// Record this folder under its parent's child list.
		parent := filepath.Clean(filepath.Dir(cleaned))
		if cleaned != root && parent != cleaned {
			folders[parent] = append(folders[parent], name)
		}
		return nil
	})
	if walkErr != nil {
		fmt.Fprintf(w, "  Warning: walk for index maintenance: %v\n", walkErr)
		return
	}

	// Stable ordering for deterministic output and tests.
	folderPaths := make([]string, 0, len(folders))
	for p := range folders {
		folderPaths = append(folderPaths, p)
	}
	sort.Strings(folderPaths)

	for _, folder := range folderPaths {
		if ctx.Err() != nil {
			return
		}
		stats.IndexFoldersScanned++
		if err := maintainOneFolder(folder, filesByFolder[folder], folders[folder], opts, stats, w); err != nil {
			fmt.Fprintf(w, "  Warning: maintain %s: %v\n", folder, err)
		}
	}
}

// maintainTargetedFolders runs the per-folder maintenance pass only for
// the folders containing the supplied updates (used by `sift refresh
// <files...>`). We rescan each touched folder to capture sibling files
// so the plan is correct even when a single file was passed in.
//
// The matcher is consulted so siblings hidden by a collection's
// `.siftignore` are excluded from the rewritten `sift.toml`. Without
// this, an ignored sibling would silently get re-added to the parent's
// folder index every time `sift refresh <file>` is run.
func maintainTargetedFolders(ctx context.Context, updates []fileUpdate, matcher collectionMatcher, opts RefreshOptions, stats *RefreshStats, w io.Writer) {
	if len(updates) == 0 {
		return
	}
	// Map dir -> primary collection (used to gate siblings via
	// .siftignore). When the same folder appears in multiple updates we
	// keep the first primary; ignore patterns are per-collection-root, so
	// sibling filtering is consistent.
	type folderCtx struct {
		path    string
		primary db.Collection
	}
	seen := map[string]struct{}{}
	var folders []folderCtx
	for _, u := range updates {
		dir := filepath.Clean(filepath.Dir(u.file.path))
		if _, ok := seen[dir]; ok {
			continue
		}
		seen[dir] = struct{}{}
		folders = append(folders, folderCtx{path: dir, primary: u.primary})
	}
	sort.Slice(folders, func(i, j int) bool { return folders[i].path < folders[j].path })

	for _, fctx := range folders {
		if ctx.Err() != nil {
			return
		}
		folder := fctx.path
		// Discover sibling files & child folders by listing the folder.
		entries, err := os.ReadDir(folder)
		if err != nil {
			fmt.Fprintf(w, "  Warning: list %s for index maintenance: %v\n", folder, err)
			continue
		}
		var fileNames []string
		var childNames []string
		for _, e := range entries {
			name := e.Name()
			abs := filepath.Join(folder, name)
			if e.IsDir() {
				if name == ".git" || name == ".obsidian" || name == "node_modules" ||
					name == ".venv" || name == "venv" || name == "__pycache__" {
					continue
				}
				// Honor .siftignore for child folder names too — if the
				// sibling directory is ignored by the collection, do not
				// include it as a tracked child folder.
				if ignore.ShouldIgnore(abs, fctx.primary.Path, matcher.ignoreByID[fctx.primary.ID]) {
					continue
				}
				childNames = append(childNames, name)
				continue
			}
			if !fileutil.IsIndexableText(name) {
				continue
			}
			if name == index.FilenameSiftToml {
				continue
			}
			// Skip siblings excluded by the collection's .siftignore so
			// they are not re-added to sift.toml on every refresh.
			if ignore.ShouldIgnore(abs, fctx.primary.Path, matcher.ignoreByID[fctx.primary.ID]) {
				continue
			}
			fileNames = append(fileNames, name)
		}
		stats.IndexFoldersScanned++
		if err := maintainOneFolder(folder, fileNames, childNames, opts, stats, w); err != nil {
			fmt.Fprintf(w, "  Warning: maintain %s: %v\n", folder, err)
		}
	}
}

// maintainOneFolder builds a PlanInput for a single folder, runs Plan,
// and Applies it (unless `--no-index` or `--dry-run` is set). It updates
// the IndexFolders* stats counters.
func maintainOneFolder(folder string, fileNames, childNames []string, opts RefreshOptions, stats *RefreshStats, w io.Writer) error {
	prev, _, err := index.LoadOrZero(folder)
	if err != nil {
		return fmt.Errorf("load sift.toml: %w", err)
	}
	// Honor self-ignore: a folder whose own sift.toml has ignore=true is
	// skipped entirely (no plan, no rewrite).
	if prev != nil && prev.Ignore {
		return nil
	}

	observed := make([]index.ObservedFile, 0, len(fileNames))
	for _, name := range fileNames {
		full := filepath.Join(folder, name)
		sig, err := index.SigOf(full)
		if err != nil {
			fmt.Fprintf(w, "  Warning: sigcompute %s: %v\n", full, err)
			continue
		}
		observed = append(observed, index.ObservedFile{RelPath: name, Sig: sig})
	}

	children := make([]index.ObservedChildFolder, 0, len(childNames))
	for _, name := range childNames {
		children = append(children, index.ObservedChildFolder{Name: name})
	}

	plan, err := index.Plan(index.PlanInput{
		FolderPath: folder,
		Prev:       prev,
		Files:      observed,
		Folders:    children,
	})
	if err != nil {
		return fmt.Errorf("plan: %w", err)
	}

	// Mechanical write is gated by plan.Changed: re-running on an
	// unchanged tree must produce zero writes (idempotency).
	if plan.Changed && !opts.NoIndex && !opts.DryRun {
		if err := index.Apply(plan); err != nil {
			return fmt.Errorf("apply: %w", err)
		}
		stats.IndexFoldersWritten++
		if plan.Created {
			stats.IndexFoldersCreated++
		}
	}

	// LLM-generation gate is independent of plan.Changed: a folder
	// whose mechanical state is up-to-date may still need its empty
	// purpose/use_when/summary fields backfilled (e.g. user ran a
	// mechanical-only pass first, then re-ran with --generate=missing).
	// The hook itself decides whether the folder needs work based on
	// the generation mode; calling it on a no-op folder is cheap.
	if opts.IndexHook != nil {
		if err := opts.IndexHook(folder, plan); err != nil {
			fmt.Fprintf(w, "  Warning: index hook for %s: %v\n", folder, err)
		}
	}
	return nil
}
