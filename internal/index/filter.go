package index

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
)

// Filters describes the search-style query filters applied to a loaded
// FolderMap. Empty fields mean "do not filter on this dimension." All
// supplied filters are AND-combined.
type Filters struct {
	// PathGlob is a doublestar pattern matched against each file's path
	// relative to FolderMap.Root. A folder is kept when its own path
	// matches OR any of its files match.
	PathGlob string

	// Since keeps files whose mtime is at or after this instant. Folders
	// are kept when at least one descendant file passes.
	Since time.Time

	// Files is a list of explicit file paths (relative to root, or
	// suffix-matched against the basename). When non-empty,
	// PathGlob/Since act as additional ANDs but only the listed files
	// can ever appear.
	Files []string

	// IncludeIgnored controls whether files with Ignore == true survive.
	// By default they're stripped. Mirrors the LoadOptions flag so the
	// reader can post-filter without reloading.
	IncludeIgnored bool
}

// IsZero reports whether the filter is a no-op.
func (f Filters) IsZero() bool {
	return f.PathGlob == "" &&
		f.Since.IsZero() &&
		len(f.Files) == 0 &&
		!f.IncludeIgnored
}

// ApplyFilters returns a new FolderMap containing only the folders/files
// that pass the filter. The original is left untouched. Folders that end
// up empty after file filtering are pruned, EXCEPT when their own path
// matches the path glob (in which case they appear with empty Files so
// their parent chain is preserved). Parent chains are always preserved
// when a child survives.
func ApplyFilters(fm *FolderMap, f Filters) *FolderMap {
	if fm == nil {
		return nil
	}
	out := &FolderMap{
		Root:   fm.Root,
		Errors: append([]string(nil), fm.Errors...),
	}
	if f.IsZero() {
		out.Folders = append([]FolderNode(nil), fm.Folders...)
		return out
	}

	fileSet := make(map[string]struct{}, len(f.Files))
	for _, p := range f.Files {
		fileSet[filepath.ToSlash(p)] = struct{}{}
	}

	// First pass: per-folder, decide which files survive.
	keep := make([]bool, len(fm.Folders))
	folderMatches := make([]bool, len(fm.Folders))
	survivors := make([][]FileNode, len(fm.Folders))

	for i, folder := range fm.Folders {
		folderMatches[i] = folderPathMatches(folder.Path, f.PathGlob)
		var kept []FileNode
		for _, fl := range folder.Files {
			if !f.IncludeIgnored && fl.Ignore {
				continue
			}
			if !fileMatches(fl, f, fileSet) {
				continue
			}
			kept = append(kept, fl)
		}
		survivors[i] = kept
		// A folder is kept if any of its files passed, OR the folder
		// itself matches the path glob (and no file/since/files filter
		// kicks the folder out).
		if len(kept) > 0 {
			keep[i] = true
		} else if len(f.Files) == 0 && f.Since.IsZero() && folderMatches[i] {
			// Pure path-glob filter and the folder itself matched.
			keep[i] = true
		}
	}

	// Second pass: preserve parent chain of every kept folder.
	pathIdx := make(map[string]int, len(fm.Folders))
	for i, folder := range fm.Folders {
		pathIdx[folder.Path] = i
	}
	for i, folder := range fm.Folders {
		if !keep[i] {
			continue
		}
		// Walk up the parent chain and mark each ancestor.
		ancestor := folder.Path
		for ancestor != "" && ancestor != "." {
			parent := parentOf(ancestor)
			if pi, ok := pathIdx[parent]; ok {
				keep[pi] = true
			}
			if parent == "." || parent == "" {
				break
			}
			ancestor = parent
		}
	}

	// Emit kept folders in original order, copying the survivor file slice.
	for i, folder := range fm.Folders {
		if !keep[i] {
			continue
		}
		copyNode := folder
		copyNode.Files = survivors[i]
		out.Folders = append(out.Folders, copyNode)
	}
	return out
}

func folderPathMatches(path, glob string) bool {
	if glob == "" {
		return false
	}
	// Try matching the folder path directly.
	if matched, err := doublestar.Match(glob, path); err == nil && matched {
		return true
	}
	// And any prefix path inside it (e.g., "docs/**" vs "docs").
	if strings.HasPrefix(path+"/", strings.TrimSuffix(glob, "/**")+"/") {
		return true
	}
	return false
}

func fileMatches(fl FileNode, f Filters, fileSet map[string]struct{}) bool {
	if len(fileSet) > 0 {
		if _, ok := fileSet[fl.Path]; !ok {
			// Allow basename match too — convenience for `--file foo.md`.
			if _, ok := fileSet[fl.Name]; !ok {
				return false
			}
		}
	}
	if !f.Since.IsZero() {
		if fl.Mtime.IsZero() || fl.Mtime.Before(f.Since) {
			return false
		}
	}
	if f.PathGlob != "" {
		matched, err := doublestar.Match(f.PathGlob, fl.Path)
		if err != nil || !matched {
			// Try matching basename when pattern is bare (no slash).
			if !strings.Contains(f.PathGlob, "/") {
				if m2, err2 := doublestar.Match(f.PathGlob, fl.Name); err2 == nil && m2 {
					return true
				}
			}
			return false
		}
	}
	return true
}

func parentOf(path string) string {
	path = filepath.ToSlash(path)
	if path == "" || path == "." {
		return "."
	}
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return "."
	}
	return path[:idx]
}
