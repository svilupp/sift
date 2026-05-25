package fileutil

import (
	"path/filepath"
	"strings"
)

// textExtensions is the canonical allowlist of file types that sift
// indexes for content. Binary blobs and source-code files are excluded
// by default; the chunker, embedder, and `sift index check` orphan
// walker all consult this list via IsIndexableText.
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

// IsIndexableText reports whether name's extension matches the
// allowlist of text-like file types that sift indexes for content
// (markdown, plain text, structured config, code-data). Binary and
// source-code files are excluded by default.
//
// The check is case-insensitive on the extension. name may be a bare
// filename or any path; only filepath.Ext is consulted.
func IsIndexableText(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return textExtensions[ext]
}
