package chunk

import (
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	tests := []struct {
		name      string
		lines     []string
		wantNil   bool
		wantRaw   string
		wantStart int
		wantEnd   int
		wantFmt   string
	}{
		{
			name:      "yaml frontmatter",
			lines:     []string{"---", "title: Hello", "tags: [a, b]", "---", "# Content"},
			wantRaw:   "title: Hello\ntags: [a, b]",
			wantStart: 1,
			wantEnd:   4,
			wantFmt:   "yaml",
		},
		{
			name:      "toml frontmatter",
			lines:     []string{"+++", "title = \"Hello\"", "+++", "Content"},
			wantRaw:   "title = \"Hello\"",
			wantStart: 1,
			wantEnd:   3,
			wantFmt:   "toml",
		},
		{
			name:      "toml in yaml delimiters",
			lines:     []string{"---", "title = \"Voice Agent\"", "version = \"3.17\"", "---", "Body"},
			wantRaw:   "title = \"Voice Agent\"\nversion = \"3.17\"",
			wantStart: 1,
			wantEnd:   4,
			wantFmt:   "yaml",
		},
		{
			name:      "multi-line yaml values",
			lines:     []string{"---", "name: quarto-documents", "description: >", "  Write Quarto documents.", "---", "Body"},
			wantRaw:   "name: quarto-documents\ndescription: >\n  Write Quarto documents.",
			wantStart: 1,
			wantEnd:   5,
			wantFmt:   "yaml",
		},
		{
			name:      "leading blank lines before opener",
			lines:     []string{"", "  ", "---", "title: Test", "---", "Body"},
			wantRaw:   "title: Test",
			wantStart: 3,
			wantEnd:   5,
			wantFmt:   "yaml",
		},
		{
			name:    "no frontmatter content starts immediately",
			lines:   []string{"# Hello World", "Some text"},
			wantNil: true,
		},
		{
			name:    "unmatched delimiter no closer",
			lines:   []string{"---", "title: Test", "Some content without closer"},
			wantNil: true,
		},
		{
			name:    "hr mid file not at top",
			lines:   []string{"Some content", "---", "More content", "---"},
			wantNil: true,
		},
		{
			name:      "empty frontmatter",
			lines:     []string{"---", "---", "Body"},
			wantRaw:   "",
			wantStart: 1,
			wantEnd:   2,
			wantFmt:   "yaml",
		},
		{
			name:    "mixed delimiters dash open plus close",
			lines:   []string{"---", "title: Test", "+++", "Body"},
			wantNil: true,
		},
		{
			name:    "mixed delimiters plus open dash close",
			lines:   []string{"+++", "title: Test", "---", "Body"},
			wantNil: true,
		},
		{
			name:      "frontmatter only no body",
			lines:     []string{"---", "title: Test", "---"},
			wantRaw:   "title: Test",
			wantStart: 1,
			wantEnd:   3,
			wantFmt:   "yaml",
		},
		{
			name:      "four dashes opener and closer",
			lines:     []string{"----", "title: Test", "----", "Body"},
			wantRaw:   "title: Test",
			wantStart: 1,
			wantEnd:   3,
			wantFmt:   "yaml",
		},
		{
			name:      "three dash opener four dash closer",
			lines:     []string{"---", "title: Test", "----", "Body"},
			wantRaw:   "title: Test",
			wantStart: 1,
			wantEnd:   3,
			wantFmt:   "yaml",
		},
		{
			name:      "trailing space on delimiter",
			lines:     []string{"---  ", "title: Test", "--- ", "Body"},
			wantRaw:   "title: Test",
			wantStart: 1,
			wantEnd:   3,
			wantFmt:   "yaml",
		},
		{
			name:    "nil lines",
			lines:   nil,
			wantNil: true,
		},
		{
			name:    "empty lines only",
			lines:   []string{"", "", ""},
			wantNil: true,
		},
		{
			name:    "tab and space lines before non-delimiter content",
			lines:   []string{"\t", "  ", "not a delimiter"},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fm := ParseFrontmatter(tt.lines)
			if tt.wantNil {
				if fm != nil {
					t.Errorf("expected nil, got %+v", fm)
				}
				return
			}
			if fm == nil {
				t.Fatal("expected non-nil Frontmatter, got nil")
			}
			if fm.Raw != tt.wantRaw {
				t.Errorf("Raw = %q, want %q", fm.Raw, tt.wantRaw)
			}
			if fm.StartLine != tt.wantStart {
				t.Errorf("StartLine = %d, want %d", fm.StartLine, tt.wantStart)
			}
			if fm.EndLine != tt.wantEnd {
				t.Errorf("EndLine = %d, want %d", fm.EndLine, tt.wantEnd)
			}
			if fm.Format != tt.wantFmt {
				t.Errorf("Format = %q, want %q", fm.Format, tt.wantFmt)
			}
		})
	}
}

func TestExtractTitleFromFrontmatter(t *testing.T) {
	tests := []struct {
		name string
		fm   *Frontmatter
		want string
	}{
		{
			name: "yaml title",
			fm:   &Frontmatter{Raw: "title: My Document\ntags: [a]", Format: "yaml"},
			want: "My Document",
		},
		{
			name: "yaml name",
			fm:   &Frontmatter{Raw: "name: audio-generation\ndescription: foo", Format: "yaml"},
			want: "audio-generation",
		},
		{
			name: "toml title quoted",
			fm:   &Frontmatter{Raw: "title = \"Voice Shopping Agent\"\nversion = \"3.17\"", Format: "toml"},
			want: "Voice Shopping Agent",
		},
		{
			name: "toml name quoted",
			fm:   &Frontmatter{Raw: "name = \"my-tool\"\nother = \"x\"", Format: "toml"},
			want: "my-tool",
		},
		{
			name: "single quoted value",
			fm:   &Frontmatter{Raw: "title: 'Single Quoted'", Format: "yaml"},
			want: "Single Quoted",
		},
		{
			name: "double quoted yaml value",
			fm:   &Frontmatter{Raw: "title: \"Double Quoted\"", Format: "yaml"},
			want: "Double Quoted",
		},
		{
			name: "title preferred over name",
			fm:   &Frontmatter{Raw: "title: First\nname: Second", Format: "yaml"},
			want: "First",
		},
		{
			name: "name used when no title",
			fm:   &Frontmatter{Raw: "description: something\nname: Fallback", Format: "yaml"},
			want: "Fallback",
		},
		{
			name: "multi-line indicator skipped",
			fm:   &Frontmatter{Raw: "title: >\n  Multi line title\nname: actual-name", Format: "yaml"},
			want: "actual-name",
		},
		{
			name: "pipe indicator skipped",
			fm:   &Frontmatter{Raw: "title: |\n  Multi line\nname: fallback", Format: "yaml"},
			want: "fallback",
		},
		{
			name: "nil frontmatter",
			fm:   nil,
			want: "",
		},
		{
			name: "empty raw",
			fm:   &Frontmatter{Raw: "", Format: "yaml"},
			want: "",
		},
		{
			name: "no title or name keys",
			fm:   &Frontmatter{Raw: "description: something\ntags: [a, b]", Format: "yaml"},
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractTitleFromFrontmatter(tt.fm)
			if got != tt.want {
				t.Errorf("ExtractTitleFromFrontmatter() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestDelimType(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{"---", "yaml"},
		{"----", "yaml"},
		{"------", "yaml"},
		{"--- ", "yaml"},
		{"---\t", "yaml"},
		{"+++", "toml"},
		{"++++", "toml"},
		{"+++ ", "toml"},
		{"--", ""},
		{"++", ""},
		{"-", ""},
		{"+", ""},
		{"", ""},
		{"---x", ""},
		{"+++x", ""},
		{"- - -", ""},
		{"+ + +", ""},
		{"abc", ""},
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := delimType(tt.line)
			if got != tt.want {
				t.Errorf("delimType(%q) = %q, want %q", tt.line, got, tt.want)
			}
		})
	}
}
