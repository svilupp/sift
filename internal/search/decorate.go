// Package search — decorate.go
//
// Decorate enriches search results with per-folder/per-file metadata
// pulled from `sift.toml` files on disk. It is invoked AFTER scoring,
// dedup and adaptive top-K trimming and BEFORE output rendering so it
// only pays parse cost for results the user is about to see.
//
// This file is intentionally pure I/O on the local filesystem; no HTTP,
// no DB calls. Missing or unparseable `sift.toml` is non-fatal: the
// result is returned unchanged.
package search

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"sift/internal/index"
)

// IndexAnnotation is the per-result decoration produced by Decorate.
// All fields are best-effort: any combination may be empty when the
// folder has no `sift.toml`, the file is not listed under `[files]`,
// or the entry has no purpose/summary populated.
type IndexAnnotation struct {
	// FolderPath is the folder (relative to the collection root) whose
	// `sift.toml` provided this annotation. Empty when no `sift.toml`
	// was found.
	FolderPath string `json:"folder_path,omitempty"`

	// FolderPurpose is the `purpose` field of the folder's `sift.toml`.
	FolderPurpose string `json:"folder_purpose,omitempty"`

	// FolderUseWhen is the `use_when` cue list from `sift.toml`.
	FolderUseWhen []string `json:"folder_use_when,omitempty"`

	// FileSummary is the per-file `summary` under `[files."<rel>"]`.
	FileSummary string `json:"file_summary,omitempty"`

	// FileWords mirrors the per-file `words` count when present.
	FileWords int `json:"file_words,omitempty"`
}

// IsZero reports whether the annotation has nothing useful to display.
// Used by callers to decide between attaching/omitting the field.
func (a IndexAnnotation) IsZero() bool {
	return a.FolderPath == "" &&
		a.FolderPurpose == "" &&
		len(a.FolderUseWhen) == 0 &&
		a.FileSummary == "" &&
		a.FileWords == 0
}

// DecorateOptions controls Decorate behaviour.
type DecorateOptions struct {
	// CollectionRoot is the absolute path of the collection. The walk
	// upward for `sift.toml` MUST stop at this path so we never cross
	// collection boundaries. Required; an empty value disables the walk
	// (Decorate returns the input unchanged).
	CollectionRoot string

	// MaxCacheEntries caps the per-call LRU/FIFO cache. Defaults to 50
	// when zero.
	MaxCacheEntries int

	// Stats, when non-nil, records cache hits/misses and load errors.
	// Used by tests; production callers may leave nil.
	Stats *DecorateStats
}

// DecorateStats accumulates cache instrumentation across one Decorate
// call. Each Decorate call gets its own *DecorateStats — there is no
// shared global, so concurrent searches do not contend for this state.
type DecorateStats struct {
	CacheHits   int
	CacheMisses int
	NotFound    int // number of folders walked that had no sift.toml at all
	ParseErrors int // load errors that were not "not exist"
	Decorated   int // number of results that ended up with a non-zero annotation
}

// Decorate attaches a folder/file IndexAnnotation to each Result whose
// FilePath sits under opts.CollectionRoot and whose parent folder (or
// any ancestor up to the root) has a parseable `sift.toml`.
//
// Behaviour contract:
//   - Missing sift.toml → result unchanged, no error.
//   - Parse error → result unchanged, error counted in stats, no error returned.
//   - opts.CollectionRoot empty → results returned unchanged.
//   - Cache is per-call: a fresh map is built every invocation, so two
//     concurrent searches never share state.
func Decorate(results []Result, opts DecorateOptions) []Result {
	if len(results) == 0 {
		return results
	}
	root := opts.CollectionRoot
	if root == "" {
		return results
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return results
	}
	absRoot = filepath.Clean(absRoot)

	maxEntries := opts.MaxCacheEntries
	if maxEntries <= 0 {
		maxEntries = 50
	}
	cache := newFolderIndexCache(maxEntries)

	for i := range results {
		if results[i].FilePath == "" {
			continue
		}
		annotation := lookupAnnotation(results[i].FilePath, absRoot, cache, opts.Stats)
		if !annotation.IsZero() {
			results[i].IndexAnnotation = &annotation
			if opts.Stats != nil {
				opts.Stats.Decorated++
			}
		}
	}
	return results
}

// lookupAnnotation walks up from the file's directory toward absRoot
// and returns the first non-empty annotation it can build. The folder
// purpose comes from the nearest `sift.toml`; the file summary comes
// from that same `sift.toml` if it has a matching `[files]` entry,
// otherwise from no entry (we do not search higher folders for the
// summary — `summary` is an exact per-file relation).
func lookupAnnotation(filePath, absRoot string, cache *folderIndexCache, stats *DecorateStats) IndexAnnotation {
	absFile, err := filepath.Abs(filePath)
	if err != nil {
		return IndexAnnotation{}
	}
	absFile = filepath.Clean(absFile)

	// File must live under the collection root; otherwise we walk into
	// arbitrary parent territory which the caller did not authorise.
	rel, err := filepath.Rel(absRoot, absFile)
	if err != nil || strings.HasPrefix(rel, "..") || rel == ".." {
		return IndexAnnotation{}
	}

	startDir := filepath.Dir(absFile)
	dir := startDir
	for {
		entry, ok := cache.get(dir)
		if !ok {
			entry = loadFolderEntry(dir, stats)
			cache.put(dir, entry)
		} else if stats != nil {
			stats.CacheHits++
		}

		if entry.idx != nil {
			ann := buildAnnotation(absFile, dir, absRoot, entry.idx)
			if !ann.IsZero() {
				return ann
			}
		}

		// Stop at collection root.
		if dir == absRoot {
			break
		}
		parent := filepath.Dir(dir)
		// Defensive: filepath.Dir("/") returns "/" — guard against an
		// infinite loop if absRoot somehow falls outside the file's
		// ancestry (already filtered above, but belt-and-braces).
		if parent == dir {
			break
		}
		dir = parent
	}
	return IndexAnnotation{}
}

// buildAnnotation projects a parsed FolderIndex into the result-side
// view. relFile is the file path relative to dir (the folder owning
// the index); we use that to look up the file under [files."<rel>"].
func buildAnnotation(absFile, dir, absRoot string, idx *index.FolderIndex) IndexAnnotation {
	ann := IndexAnnotation{
		FolderPurpose: strings.TrimSpace(idx.Purpose),
	}
	if len(idx.UseWhen) > 0 {
		ann.FolderUseWhen = append(ann.FolderUseWhen, idx.UseWhen...)
	}

	// FolderPath: relative to collection root, forward slashes, "." for root.
	if folderRel, err := filepath.Rel(absRoot, dir); err == nil {
		ann.FolderPath = filepath.ToSlash(folderRel)
	}

	// File entry lookup. `sift.toml` indexes files only by their
	// folder-relative path (no leading "./", forward slashes).
	if rel, err := filepath.Rel(dir, absFile); err == nil {
		key := filepath.ToSlash(rel)
		if fe, ok := idx.Files[key]; ok {
			ann.FileSummary = strings.TrimSpace(fe.Summary)
			ann.FileWords = fe.Words
		}
	}
	return ann
}

// folderIndexEntry is the cache value: either a parsed *FolderIndex,
// nil meaning "we already looked and there is no sift.toml here", or
// nil meaning "we looked and parsing failed". Either way the cache
// suppresses re-reads inside one Decorate call.
type folderIndexEntry struct {
	idx *index.FolderIndex
}

// folderIndexCache is a tiny FIFO cache keyed by absolute folder path.
// Eviction order is insertion order; we evict the oldest entry when
// len(entries) > capacity. This is intentionally simpler than a true
// LRU — for a per-query cache of 5–50 folders, FIFO is plenty.
type folderIndexCache struct {
	cap     int
	order   []string
	entries map[string]folderIndexEntry
}

func newFolderIndexCache(capacity int) *folderIndexCache {
	if capacity <= 0 {
		capacity = 50
	}
	return &folderIndexCache{
		cap:     capacity,
		order:   make([]string, 0, capacity),
		entries: make(map[string]folderIndexEntry, capacity),
	}
}

func (c *folderIndexCache) get(key string) (folderIndexEntry, bool) {
	v, ok := c.entries[key]
	return v, ok
}

func (c *folderIndexCache) put(key string, v folderIndexEntry) {
	if _, exists := c.entries[key]; exists {
		c.entries[key] = v
		return
	}
	if len(c.entries) >= c.cap {
		// Evict the oldest insertion.
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.entries, oldest)
	}
	c.order = append(c.order, key)
	c.entries[key] = v
}

// loadFolderEntry tries to parse `sift.toml` in dir. Missing file is
// not an error — we cache an empty entry so the next sibling hit is a
// pure map lookup. Parse errors are counted but treated like missing.
func loadFolderEntry(dir string, stats *DecorateStats) folderIndexEntry {
	path := filepath.Join(dir, index.FilenameSiftToml)
	idx, err := index.Load(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if stats != nil {
				stats.CacheMisses++
				stats.NotFound++
			}
			return folderIndexEntry{}
		}
		if stats != nil {
			stats.CacheMisses++
			stats.ParseErrors++
		}
		return folderIndexEntry{}
	}
	if stats != nil {
		stats.CacheMisses++
	}
	return folderIndexEntry{idx: idx}
}
