package index

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sift/internal/ignore"
)

// FolderMap is the read-side, tree-shaped view of a tree of `sift.toml`
// files. Phase-4 commands consume it; renderers and filters operate on it.
//
// Folders are sorted by Path (alphabetical, "." root first because "."
// sorts before any letter). The slice is flat — children are linked to
// parents by path prefix only. Tree-shape rendering walks the slice.
type FolderMap struct {
	// Root is the absolute path the load started from.
	Root string

	// Folders is the flat, ordered slice of folder nodes, sorted by Path.
	Folders []FolderNode

	// Errors holds non-fatal walk errors (per-folder read or parse
	// problems). The map still loads when present.
	Errors []string
}

// FolderNode describes one folder in a FolderMap. When the folder lacks
// a `sift.toml` the metadata fields are empty (Purpose == "", etc.) but
// the node still appears so renderers can show the structure.
type FolderNode struct {
	// Path is the folder's path relative to FolderMap.Root, with forward
	// slashes. The root folder uses ".".
	Path string

	// AbsPath is the absolute filesystem path.
	AbsPath string

	// Depth from the root (0 == root, 1 == immediate children, ...).
	Depth int

	// HasIndex is true when a sift.toml exists in this folder.
	HasIndex bool

	// ParseError is non-empty when sift.toml failed to parse.
	ParseError string

	// Ignore mirrors the folder's `ignore = true` flag.
	Ignore bool

	// Purpose / UseWhen / Refresh come straight from sift.toml when present.
	Purpose string
	UseWhen []string
	Refresh RefreshStats

	// LastModified is the most recent mtime among non-ignored files in
	// this folder (zero when there are no files or stat failed).
	LastModified time.Time

	// Files is the per-file slice in alphabetic order.
	Files []FileNode
}

// FileNode describes one entry in a folder's `[files."<rel>"]` table.
// Mtime/Bytes are filled live from disk if the file exists.
type FileNode struct {
	// Path is the file's path relative to FolderMap.Root, forward-slashed.
	Path string

	// Name is the relative key inside its FolderNode (e.g. "README.md").
	Name string

	// Kind is a short type label derived from the extension ("md", "txt",
	// "go", ...). Empty for unknown extensions.
	Kind string

	// Ignore mirrors the per-file `ignore = true` flag in sift.toml.
	Ignore bool

	// Summary / Words come from sift.toml.
	Summary string
	Words   int

	// ContentHash is exposed mostly so callers can detect drift. The
	// read view doesn't recompute it.
	ContentHash string

	// Bytes / Mtime come from os.Stat — zero when the file is missing
	// on disk (orphan).
	Bytes int64
	Mtime time.Time

	// Exists is true when the file is present on disk.
	Exists bool

	// Sections is populated by AttachSections (nil otherwise). Empty
	// slice means we tried but found no headings; nil means we did not
	// attempt extraction.
	Sections []SectionNode
}

// SectionNode is a markdown heading discovered live in a file.
type SectionNode struct {
	Heading   string `json:"heading"`
	Level     int    `json:"level"`
	StartLine int    `json:"start_line"`
	EndLine   int    `json:"end_line"`
}

// LoadOptions controls a LoadTree walk.
type LoadOptions struct {
	// Depth caps the descent. -1 (the default) means unbounded. 0 keeps
	// only the root folder. 1 includes the root and its direct children.
	Depth int

	// IncludeIgnored, when true, descends into folders that have
	// `ignore = true` in their `sift.toml` and returns child files even
	// when the per-file `ignore` flag is set.
	IncludeIgnored bool
}

// DefaultLoadOptions returns sensible defaults: unbounded depth,
// ignore-aware filtering.
func DefaultLoadOptions() LoadOptions {
	return LoadOptions{Depth: -1}
}

// LoadTree walks root and parses every `sift.toml`, returning a flat
// FolderMap ordered by path. Missing `sift.toml` files are tolerated:
// the folder still appears, with empty metadata.
//
// LoadTree never writes. It honors `.siftignore` rooted at root and the
// per-folder/file `ignore` flag (unless opts.IncludeIgnored is set).
//
// `subPath` is optional. When non-empty, walk starts at root/subPath
// instead of root, and FolderMap.Root reflects that subdirectory.
func LoadTree(ctx context.Context, root, subPath string, opts LoadOptions) (*FolderMap, error) {
	if root == "" {
		return nil, errors.New("load: empty root")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("load: resolve root: %w", err)
	}
	if subPath != "" {
		abs = filepath.Join(abs, subPath)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("load: stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("load: root %q is not a directory", abs)
	}

	patterns, ierr := ignore.LoadPatterns(abs)
	if ierr != nil {
		return nil, fmt.Errorf("load: read .siftignore: %w", ierr)
	}

	fm := &FolderMap{Root: abs}
	if err := loadWalk(ctx, abs, patterns, opts, fm); err != nil {
		return nil, err
	}

	sort.Slice(fm.Folders, func(i, j int) bool {
		return fm.Folders[i].Path < fm.Folders[j].Path
	})
	return fm, nil
}

// loadWalk performs the descent. Mirrors check.walkTree — DFS with a
// stack so per-folder `ignore = true` can prune subtrees before reading
// them.
func loadWalk(ctx context.Context, root string, patterns []string, opts LoadOptions, fm *FolderMap) error {
	type item struct {
		abs   string
		rel   string
		depth int
	}
	stack := []item{{abs: root, rel: ".", depth: 0}}

	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("load: %w", err)
		}
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		entries, err := os.ReadDir(cur.abs)
		if err != nil {
			fm.Errors = append(fm.Errors, fmt.Sprintf("read folder %s: %v", cur.rel, err))
			continue
		}

		var (
			subdirs   []os.DirEntry
			diskFiles = make(map[string]os.DirEntry)
			hasIndex  bool
		)
		for _, e := range entries {
			name := e.Name()
			if name == FilenameSiftToml {
				hasIndex = true
				continue
			}
			if name == ".siftignore" {
				continue
			}
			abs := filepath.Join(cur.abs, name)
			if patterns != nil && ignore.ShouldIgnore(abs, root, patterns) {
				continue
			}
			if e.IsDir() {
				if name == ".git" || name == ".sift" {
					continue
				}
				subdirs = append(subdirs, e)
				continue
			}
			if !e.Type().IsRegular() {
				continue
			}
			diskFiles[name] = e
		}

		node := FolderNode{
			Path:     cur.rel,
			AbsPath:  cur.abs,
			Depth:    cur.depth,
			HasIndex: hasIndex,
		}

		var folderIdx *FolderIndex
		if hasIndex {
			tomlPath := filepath.Join(cur.abs, FilenameSiftToml)
			parsed, perr := Load(tomlPath)
			if perr != nil {
				node.ParseError = perr.Error()
			} else {
				folderIdx = parsed
				node.Ignore = parsed.Ignore
				node.Purpose = parsed.Purpose
				if len(parsed.UseWhen) > 0 {
					node.UseWhen = append([]string(nil), parsed.UseWhen...)
				}
				node.Refresh = parsed.Refresh
			}
		}

		// If folder is ignored and we're not including ignored, skip
		// reading file entries and don't descend into children.
		if folderIdx != nil && folderIdx.Ignore && !opts.IncludeIgnored {
			fm.Folders = append(fm.Folders, node)
			continue
		}

		// Build file list. Union of sift.toml entries and on-disk files
		// (which keeps orphan-on-disk files visible). Dedup by name.
		seen := make(map[string]struct{})
		var files []FileNode

		// sift.toml entries first.
		if folderIdx != nil {
			tomlNames := make([]string, 0, len(folderIdx.Files))
			for name := range folderIdx.Files {
				tomlNames = append(tomlNames, name)
			}
			sort.Strings(tomlNames)
			for _, name := range tomlNames {
				entry := folderIdx.Files[name]
				// Always mark as seen so the on-disk pass below
				// doesn't resurrect an ignore=true entry as an
				// orphan.
				seen[name] = struct{}{}
				if entry.Ignore && !opts.IncludeIgnored {
					continue
				}
				files = append(files, makeFileNode(cur.rel, cur.abs, name, entry, diskFiles))
			}
		}

		// On-disk files not already covered.
		diskNames := make([]string, 0, len(diskFiles))
		for name := range diskFiles {
			if _, ok := seen[name]; ok {
				continue
			}
			diskNames = append(diskNames, name)
		}
		sort.Strings(diskNames)
		for _, name := range diskNames {
			files = append(files, makeFileNode(cur.rel, cur.abs, name, FileEntry{}, diskFiles))
		}

		// Compute LastModified from non-ignored files' mtimes.
		var newest time.Time
		for _, f := range files {
			if !f.Mtime.IsZero() && f.Mtime.After(newest) {
				newest = f.Mtime
			}
		}
		node.LastModified = newest
		node.Files = files

		fm.Folders = append(fm.Folders, node)

		// Descent.
		if opts.Depth >= 0 && cur.depth >= opts.Depth {
			continue
		}
		// Reverse-sort so DFS pops in alphabetical order — the final
		// FolderMap is sorted at the top level anyway, but DFS order
		// keeps children adjacent to parents while building.
		sort.Slice(subdirs, func(i, j int) bool {
			return subdirs[i].Name() > subdirs[j].Name()
		})
		for _, sd := range subdirs {
			child := relJoin(cur.rel, sd.Name())
			if folderIdx != nil && !opts.IncludeIgnored {
				if cf, ok := folderIdx.Folders[sd.Name()]; ok && cf.Ignore {
					continue
				}
			}
			stack = append(stack, item{
				abs:   filepath.Join(cur.abs, sd.Name()),
				rel:   child,
				depth: cur.depth + 1,
			})
		}
	}

	return nil
}

func makeFileNode(folderRel, folderAbs, name string, entry FileEntry, diskFiles map[string]os.DirEntry) FileNode {
	rel := name
	if folderRel != "." {
		rel = folderRel + "/" + name
	}
	fn := FileNode{
		Path:        rel,
		Name:        name,
		Kind:        kindFromName(name),
		Ignore:      entry.Ignore,
		Summary:     entry.Summary,
		Words:       entry.Words,
		ContentHash: entry.ContentHash,
	}
	if _, present := diskFiles[name]; present {
		abs := filepath.Join(folderAbs, name)
		if info, err := os.Stat(abs); err == nil {
			fn.Bytes = info.Size()
			fn.Mtime = info.ModTime()
			fn.Exists = true
		}
	}
	return fn
}

// kindFromName maps a filename to a short type label by extension.
// Returns "" for unknown extensions; renderers can decide what to do.
func kindFromName(name string) string {
	ext := strings.ToLower(filepath.Ext(name))
	if ext == "" {
		return ""
	}
	ext = strings.TrimPrefix(ext, ".")
	switch ext {
	case "md", "markdown", "mdx":
		return "md"
	case "txt", "text":
		return "txt"
	case "rst":
		return "rst"
	case "org":
		return "org"
	case "go":
		return "go"
	case "py":
		return "py"
	case "js", "ts", "jsx", "tsx":
		return ext
	case "json", "toml", "yaml", "yml", "html", "css":
		return ext
	}
	return ext
}

// IsTextKind reports whether the file's kind is one we attempt section
// extraction on. Stays conservative: markdown and a few plain-text
// flavors only.
func IsTextKind(kind string) bool {
	switch kind {
	case "md", "markdown", "mdx", "rst", "org", "txt":
		return true
	}
	return false
}
