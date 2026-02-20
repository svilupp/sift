package ignore

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSiftignore(t *testing.T, dir, content string) {
	t.Helper()
	err := os.WriteFile(filepath.Join(dir, ".siftignore"), []byte(content), 0644)
	if err != nil {
		t.Fatalf("write .siftignore: %v", err)
	}
}

func TestLoadPatterns_MissingFile(t *testing.T) {
	dir := t.TempDir()
	patterns, err := LoadPatterns(dir)
	if err != nil {
		t.Fatalf("expected nil error, got %v", err)
	}
	if patterns != nil {
		t.Fatalf("expected nil patterns, got %v", patterns)
	}
}

func TestLoadPatterns_CommentsAndBlankLines(t *testing.T) {
	dir := t.TempDir()
	writeSiftignore(t, dir, `# This is a comment
*.json

# Another comment
*.summary.md

`)
	patterns, err := LoadPatterns(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(patterns) != 2 {
		t.Fatalf("expected 2 patterns, got %d: %v", len(patterns), patterns)
	}
	if patterns[0] != "*.json" {
		t.Errorf("patterns[0] = %q, want %q", patterns[0], "*.json")
	}
	if patterns[1] != "*.summary.md" {
		t.Errorf("patterns[1] = %q, want %q", patterns[1], "*.summary.md")
	}
}

func TestShouldIgnore(t *testing.T) {
	root := "/collections/vault"

	tests := []struct {
		name     string
		path     string
		patterns []string
		want     bool
	}{
		{
			name:     "match *.json",
			path:     "/collections/vault/data.json",
			patterns: []string{"*.json"},
			want:     true,
		},
		{
			name:     "match *.summary.md",
			path:     "/collections/vault/notes/topic.summary.md",
			patterns: []string{"*.summary.md"},
			want:     true,
		},
		{
			name:     "no match for unrelated file",
			path:     "/collections/vault/notes/readme.md",
			patterns: []string{"*.json", "*.summary.md"},
			want:     false,
		},
		{
			name:     "doublestar pattern matches nested files",
			path:     "/collections/vault/deep/nested/file.log",
			patterns: []string{"**/*.log"},
			want:     true,
		},
		{
			name:     "doublestar pattern matches top-level file",
			path:     "/collections/vault/file.log",
			patterns: []string{"**/*.log"},
			want:     true,
		},
		{
			name:     "directory pattern skips whole dir",
			path:     "/collections/vault/.obsidian",
			patterns: []string{".obsidian"},
			want:     true,
		},
		{
			name:     "directory pattern matches file inside dir",
			path:     "/collections/vault/.obsidian/config.json",
			patterns: []string{".obsidian"},
			want:     true,
		},
		{
			name:     "subdirectory pattern",
			path:     "/collections/vault/archive/old/file.md",
			patterns: []string{"archive"},
			want:     true,
		},
		{
			name:     "nil patterns never ignores",
			path:     "/collections/vault/anything.md",
			patterns: nil,
			want:     false,
		},
		{
			name:     "empty patterns never ignores",
			path:     "/collections/vault/anything.md",
			patterns: []string{},
			want:     false,
		},
		{
			name:     "nested glob with directory prefix",
			path:     "/collections/vault/logs/2024/jan.log",
			patterns: []string{"logs/**/*.log"},
			want:     true,
		},
		{
			name:     "specific file pattern",
			path:     "/collections/vault/TODO.md",
			patterns: []string{"TODO.md"},
			want:     true,
		},
		{
			name:     "specific file pattern no match",
			path:     "/collections/vault/README.md",
			patterns: []string{"TODO.md"},
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldIgnore(tt.path, root, tt.patterns)
			if got != tt.want {
				t.Errorf("ShouldIgnore(%q, %q, %v) = %v, want %v",
					tt.path, root, tt.patterns, got, tt.want)
			}
		})
	}
}

func TestShouldIgnore_Integration(t *testing.T) {
	dir := t.TempDir()
	writeSiftignore(t, dir, `# Ignore JSON data files
*.json
# Ignore summaries
*.summary.md
# Ignore archive directory
archive
# Ignore all logs
**/*.log
`)

	patterns, err := LoadPatterns(dir)
	if err != nil {
		t.Fatalf("load patterns: %v", err)
	}

	tests := []struct {
		relPath string
		want    bool
	}{
		{"data.json", true},
		{"nested/data.json", true},
		{"notes.md", false},
		{"topic.summary.md", true},
		{"archive/old.md", true},
		{"archive", true},
		{"app.log", true},
		{"logs/debug.log", true},
		{"readme.md", false},
	}

	for _, tt := range tests {
		absPath := filepath.Join(dir, tt.relPath)
		got := ShouldIgnore(absPath, dir, patterns)
		if got != tt.want {
			t.Errorf("ShouldIgnore(%q) = %v, want %v", tt.relPath, got, tt.want)
		}
	}
}
