package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	gosync "sync"
	"time"

	"github.com/cespare/xxhash/v2"
	"golang.org/x/sync/errgroup"

	"sift/internal/chunk"
	"sift/internal/db"
	"sift/internal/ignore"
	"sift/internal/index"
	"sift/internal/voyage"
)

// RefreshOptions controls refresh behavior.
type RefreshOptions struct {
	CollectionName string // Empty = all collections
	Full           bool   // Force full reindex
	DryRun         bool   // Show what would be indexed
	ChunkOpts      chunk.Options
	BatchSize      int // Embedding batch size (default 128)
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
}

// textExtensions defines which file types SIFT indexes.
var textExtensions = map[string]bool{
	".md":    true,
	".txt":   true,
	".jsonl": true,
	".json":  true,
	".yml":   true,
	".yaml":  true,
	".toml":  true,
	".csv":   true,
	".log":   true,
	".qmd":   true,
}

// Refresh performs an incremental index refresh.
// If voyageClient is nil, embedding is skipped (BM25-only mode).
func Refresh(ctx context.Context, database *db.DB, bleveIdx *index.BleveIndex, voyageClient *voyage.Client, opts RefreshOptions, w io.Writer) (*RefreshStats, error) {
	stats := &RefreshStats{}
	start := time.Now()

	if opts.BatchSize <= 0 {
		opts.BatchSize = 128
	}

	// Get collections to refresh.
	collections, err := getCollections(database, opts.CollectionName)
	if err != nil {
		return nil, err
	}

	if len(collections) == 0 {
		fmt.Fprintln(w, "No collections registered. Use: sift collections add <name> <path>")
		return stats, nil
	}

	for _, col := range collections {
		if err := refreshCollection(ctx, database, bleveIdx, voyageClient, col, opts, stats, w); err != nil {
			return stats, fmt.Errorf("refresh %q: %w", col.Name, err)
		}
	}

	stats.Duration = time.Since(start)
	return stats, nil
}

// RefreshFiles indexes specific files by path.
// Each file is resolved to its parent collection via DB lookup or path prefix match.
// If voyageClient is nil, embedding is skipped (BM25-only mode).
func RefreshFiles(ctx context.Context, database *db.DB, bleveIdx *index.BleveIndex, voyageClient *voyage.Client, paths []string, opts RefreshOptions, w io.Writer) (*RefreshStats, error) {
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

	// Resolve each path and group by collection.
	grouped := make(map[int64][]fsFile)
	colMap := make(map[int64]db.Collection)
	ignoreCache := make(map[int64][]string) // patterns per collection ID

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

		ext := strings.ToLower(filepath.Ext(absPath))
		if !textExtensions[ext] {
			fmt.Fprintf(w, "  Skipping %s: unsupported file type %q\n", p, ext)
			continue
		}

		col, err := resolveCollection(database, absPath, collections)
		if err != nil {
			fmt.Fprintf(w, "  Skipping %s: %v\n", p, err)
			continue
		}

		// Check .siftignore patterns (cached per collection).
		if _, ok := ignoreCache[col.ID]; !ok {
			patterns, loadErr := ignore.LoadPatterns(col.Path)
			if loadErr != nil {
				fmt.Fprintf(w, "  Warning: reading .siftignore: %v\n", loadErr)
			}
			ignoreCache[col.ID] = patterns
		}
		if ignore.ShouldIgnore(absPath, col.Path, ignoreCache[col.ID]) {
			fmt.Fprintf(w, "  Skipping %s: ignored by .siftignore\n", p)
			continue
		}

		existing, _ := database.GetFileByPath(absPath)
		if existing == nil {
			stats.FilesNew++
		} else {
			stats.FilesChanged++
		}

		grouped[col.ID] = append(grouped[col.ID], fsFile{
			path:      absPath,
			mtime:     info.ModTime().Unix(),
			sizeBytes: info.Size(),
		})
		colMap[col.ID] = *col
	}

	total := stats.FilesNew + stats.FilesChanged
	if total == 0 {
		fmt.Fprintln(w, "No valid files to refresh.")
		stats.Duration = time.Since(start)
		return stats, nil
	}

	fmt.Fprintf(w, "Refreshing %d files (%d new, %d changed)\n", total, stats.FilesNew, stats.FilesChanged)

	if opts.DryRun {
		for _, files := range grouped {
			for _, f := range files {
				fmt.Fprintf(w, "  + %s\n", f.path)
			}
		}
		stats.Duration = time.Since(start)
		return stats, nil
	}

	for colID, files := range grouped {
		col := colMap[colID]
		if err := indexFiles(ctx, database, bleveIdx, voyageClient, col, files, opts, stats, w); err != nil {
			return stats, fmt.Errorf("refresh files in %q: %w", col.Name, err)
		}
	}

	stats.Duration = time.Since(start)
	return stats, nil
}

// resolveCollection determines which collection a file belongs to.
// First checks the DB (file already indexed), then does longest path prefix match.
func resolveCollection(database *db.DB, absPath string, collections []db.Collection) (*db.Collection, error) {
	existing, err := database.GetFileByPath(absPath)
	if err != nil {
		return nil, fmt.Errorf("lookup file: %w", err)
	}
	if existing != nil {
		for i := range collections {
			if collections[i].ID == existing.CollectionID {
				return &collections[i], nil
			}
		}
	}

	var best *db.Collection
	bestLen := 0
	for i := range collections {
		colPath := collections[i].Path
		if !strings.HasSuffix(colPath, "/") {
			colPath += "/"
		}
		if strings.HasPrefix(absPath, colPath) && len(colPath) > bestLen {
			best = &collections[i]
			bestLen = len(colPath)
		}
	}
	if best != nil {
		return best, nil
	}

	return nil, fmt.Errorf("not inside any registered collection")
}

func getCollections(database *db.DB, name string) ([]db.Collection, error) {
	if name != "" {
		col, err := database.GetCollection(name)
		if err != nil {
			return nil, err
		}
		if col == nil {
			return nil, fmt.Errorf("collection %q not found", name)
		}
		return []db.Collection{*col}, nil
	}
	return database.ListCollections()
}

// pendingEmbed tracks a chunk awaiting embedding.
type pendingEmbed struct {
	chunkID int64
	content string
	path    string
}

// processedFile holds the result of parallel file reading/chunking.
type processedFile struct {
	f      fsFile
	hash   string
	chunks []chunk.Chunk
	title  string
}

// readAndProcessFile reads a file once and returns hash, chunks, and title.
func readAndProcessFile(path string, chunkOpts chunk.Options) (hash string, chunks []chunk.Chunk, title string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", nil, "", err
	}

	h := xxhash.Sum64(data)
	hash = fmt.Sprintf("%016x", h)

	lines := strings.Split(string(data), "\n")
	chunks = chunk.FromLines(lines, chunkOpts)
	title = chunk.ExtractTitle(lines)
	return hash, chunks, title, nil
}

func refreshCollection(ctx context.Context, database *db.DB, bleveIdx *index.BleveIndex, voyageClient *voyage.Client, col db.Collection, opts RefreshOptions, stats *RefreshStats, w io.Writer) error {
	fmt.Fprintf(w, "  Scanning: %s (%s)\n", col.Name, col.Path)

	// Load .siftignore patterns.
	ignorePatterns, err := ignore.LoadPatterns(col.Path)
	if err != nil {
		fmt.Fprintf(w, "  Warning: reading .siftignore: %v\n", err)
		ignorePatterns = nil
	}

	// Walk filesystem to find text files.
	fsFiles, err := scanDirectory(col.Path, ignorePatterns)
	if err != nil {
		return fmt.Errorf("scan directory: %w", err)
	}
	stats.FilesScanned += len(fsFiles)

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
	var toIndex []fsFile
	var toDelete []int64

	for _, f := range fsFiles {
		existing := dbFileMap[f.path]
		if existing == nil {
			toIndex = append(toIndex, f)
			stats.FilesNew++
		} else if opts.Full || existing.Mtime != f.mtime {
			// Check hash for changed files.
			hash, err := hashFile(f.path)
			if err != nil {
				fmt.Fprintf(w, "  Warning: hash failed for %s: %v\n", f.path, err)
				delete(dbFileMap, f.path) // prevent false deletion
				continue
			}
			if opts.Full || hash != existing.FileHash {
				toIndex = append(toIndex, f)
				stats.FilesChanged++
			}
			delete(dbFileMap, f.path)
		} else {
			delete(dbFileMap, f.path)
		}
	}

	// Remaining dbFiles are deleted from filesystem.
	for _, f := range dbFileMap {
		toDelete = append(toDelete, f.ID)
		stats.FilesDeleted++
	}

	changed := len(toIndex) + len(toDelete)
	fmt.Fprintf(w, "  Changed: %d files (%d new, %d modified, %d deleted)\n",
		changed, stats.FilesNew, stats.FilesChanged, stats.FilesDeleted)

	if opts.DryRun {
		for _, f := range toIndex {
			fmt.Fprintf(w, "    + %s\n", f.path)
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

	return indexFiles(ctx, database, bleveIdx, voyageClient, col, toIndex, opts, stats, w)
}

// indexFiles processes a list of files: read/chunk, DB write, Bleve index, embed.
// Shared pipeline used by both collection refresh and single-file refresh.
func indexFiles(ctx context.Context, database *db.DB, bleveIdx *index.BleveIndex, voyageClient *voyage.Client, col db.Collection, toIndex []fsFile, opts RefreshOptions, stats *RefreshStats, w io.Writer) error {
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

	for _, f := range toIndex {
		g.Go(func() error {
			if gctx.Err() != nil {
				return gctx.Err()
			}

			hash, chunks, title, err := readAndProcessFile(f.path, opts.ChunkOpts)
			if err != nil {
				mu.Lock()
				processWarnings = append(processWarnings, fmt.Sprintf("read failed for %s: %v", f.path, err))
				mu.Unlock()
				return nil // non-fatal
			}

			mu.Lock()
			processed = append(processed, processedFile{f: f, hash: hash, chunks: chunks, title: title})
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
	allBleveDocs := make(map[string]index.BleveDoc)
	var allPendingEmbeds []pendingEmbed
	var allOldBleveIDs []string

	for _, pf := range processed {
		relPath := strings.TrimPrefix(pf.f.path, col.Path)
		relPath = strings.TrimPrefix(relPath, "/")

		// Get old chunks for Bleve cleanup.
		existingFile, err := database.GetFileByPath(pf.f.path)
		if err != nil {
			fmt.Fprintf(w, "\n  Warning: get existing file %s: %v", pf.f.path, err)
			continue
		}
		if existingFile != nil {
			oldChunks, err := database.GetChunksByFile(existingFile.ID)
			if err != nil {
				fmt.Fprintf(w, "\n  Warning: get old chunks %s: %v", pf.f.path, err)
				continue
			}
			for _, c := range oldChunks {
				allOldBleveIDs = append(allOldBleveIDs, strconv.FormatInt(c.ID, 10))
			}
		}

		// DB transaction.
		var newChunkIDs []int64
		err = database.WithTx(func(tx *db.Tx) error {
			fileID, err := tx.UpsertFile(pf.f.path, col.ID, pf.hash, pf.f.mtime, pf.f.sizeBytes, pf.title)
			if err != nil {
				return fmt.Errorf("upsert file: %w", err)
			}
			if err := tx.DeleteChunksByFile(fileID); err != nil {
				return fmt.Errorf("delete old chunks: %w", err)
			}
			newChunkIDs = make([]int64, len(pf.chunks))
			for i, c := range pf.chunks {
				cid, err := tx.InsertChunk(fileID, c.Order, c.StartLine, c.EndLine, c.CharCount)
				if err != nil {
					return fmt.Errorf("insert chunk: %w", err)
				}
				newChunkIDs[i] = cid
			}
			return tx.UpdateFileChunkCount(fileID, len(pf.chunks))
		})
		if err != nil {
			fmt.Fprintf(w, "\n  Warning: index failed for %s: %v", pf.f.path, err)
			continue
		}

		stats.ChunksTotal += len(newChunkIDs)

		// Build Bleve docs and pending embeds.
		for i, c := range pf.chunks {
			provenanceContent := chunk.PrependProvenance(c.Content, relPath, pf.title, col.Name)
			docID := strconv.FormatInt(newChunkIDs[i], 10)
			allBleveDocs[docID] = index.BleveDoc{
				Content: provenanceContent,
				Path:    pf.f.path,
			}
			if voyageClient != nil {
				allPendingEmbeds = append(allPendingEmbeds, pendingEmbed{
					chunkID: newChunkIDs[i],
					content: provenanceContent,
					path:    pf.f.path,
				})
			}
		}
	}

	fmt.Fprintf(w, "%d chunks\n", stats.ChunksTotal)

	// Delete old Bleve docs, then batch-index new ones.
	for _, oldID := range allOldBleveIDs {
		if err := bleveIdx.Delete(oldID); err != nil {
			fmt.Fprintf(w, "  Warning: bleve delete chunk %s: %v\n", oldID, err)
		}
	}
	if len(allBleveDocs) > 0 {
		if err := bleveIdx.IndexBatch(allBleveDocs); err != nil {
			return fmt.Errorf("bleve batch index: %w", err)
		}
	}

	// Parallel embedding.
	if voyageClient != nil && len(allPendingEmbeds) > 0 {
		fmt.Fprintf(w, "  Embedding: %d chunks", len(allPendingEmbeds))

		embedG, embedCtx := errgroup.WithContext(ctx)
		embedG.SetLimit(10)

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

				embedStart := time.Now()
				vectors, usage, err := voyageClient.Embed(embedCtx, texts, "document")
				latencyMs := time.Since(embedStart).Milliseconds()

				if err != nil {
					embedMu.Lock()
					stats.EmbedErrors += len(batch)
					embedMu.Unlock()

					for _, pe := range batch {
						chunkInfo, _ := json.Marshal(map[string]int64{"chunk_id": pe.chunkID})
						_ = database.InsertDeadLetter("embed", pe.path, string(chunkInfo), err.Error(), "")
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
					if index.IsBinaryDtype(voyageClient.EmbedDtype) {
						vecBytes = index.EncodeBinaryVector(vectors[j])
						dims = voyageClient.EmbedDimensions
					} else {
						vecBytes = index.EncodeVector(vectors[j])
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

	return nil
}

type fsFile struct {
	path      string
	mtime     int64
	sizeBytes int64
}

func scanDirectory(dir string, patterns []string) ([]fsFile, error) {
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
			return nil
		}

		ext := strings.ToLower(filepath.Ext(path))
		if !textExtensions[ext] {
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
