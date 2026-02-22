package chunk

import (
	"strings"
	"testing"
)

func TestFormatProvenance(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		title      string
		collection string
		want       string
	}{
		{
			name:       "basic",
			path:       "docs/foo.md",
			title:      "My Title",
			collection: "vault",
			want:       `<source file="docs/foo.md" title="My Title" collection="vault" />`,
		},
		{
			name:       "empty title",
			path:       "docs/bar.md",
			title:      "",
			collection: "vault",
			want:       `<source file="docs/bar.md" title="" collection="vault" />`,
		},
		{
			name:       "special chars preserved",
			path:       "docs/path with spaces/file.md",
			title:      "Title & More",
			collection: "my-vault",
			want:       `<source file="docs/path with spaces/file.md" title="Title & More" collection="my-vault" />`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatProvenance(tt.path, tt.title, tt.collection)
			if got != tt.want {
				t.Errorf("FormatProvenance() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractTitle(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
		want  string
	}{
		{
			name:  "h1 heading",
			lines: []string{"# Foo"},
			want:  "Foo",
		},
		{
			name:  "h2 heading",
			lines: []string{"## Bar"},
			want:  "Bar",
		},
		{
			name:  "no heading",
			lines: []string{"Just text", "More text"},
			want:  "",
		},
		{
			name:  "empty slice",
			lines: []string{},
			want:  "",
		},
		{
			name:  "nil slice",
			lines: nil,
			want:  "",
		},
		{
			name:  "h1 before h2",
			lines: []string{"Some text", "# First", "## Second"},
			want:  "First",
		},
		{
			name:  "h2 before h1",
			lines: []string{"## Early", "# Later"},
			want:  "Early",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractTitle(tt.lines)
			if got != tt.want {
				t.Errorf("ExtractTitle() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPrependProvenance(t *testing.T) {
	content := "Hello world\nSecond line"
	result := PrependProvenance(content, "docs/foo.md", "My Title", "vault")

	tag := `<source file="docs/foo.md" title="My Title" collection="vault" />`
	if !strings.HasPrefix(result, tag) {
		t.Errorf("result should start with provenance tag, got: %q", result[:min(len(result), 80)])
	}

	parts := strings.SplitN(result, "\n", 2)
	if len(parts) != 2 {
		t.Fatalf("expected provenance + newline + content, got %d parts", len(parts))
	}
	if parts[1] != content {
		t.Errorf("content after provenance = %q, want %q", parts[1], content)
	}
}
