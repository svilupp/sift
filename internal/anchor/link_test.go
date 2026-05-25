package anchor

import (
	"testing"
)

func TestExtractMarkdownLinks(t *testing.T) {
	tests := []struct {
		name       string
		filePath   string
		lines      []string
		wantCount  int
		checkFirst func(t *testing.T, l Link)
	}{
		{
			name:      "simple relative link",
			filePath:  "docs/README.md",
			lines:     []string{"See [architecture](./ARCHITECTURE.md) for details."},
			wantCount: 1,
			checkFirst: func(t *testing.T, l Link) {
				t.Helper()
				if l.TargetPath != "docs/ARCHITECTURE.md" {
					t.Errorf("TargetPath = %q, want %q", l.TargetPath, "docs/ARCHITECTURE.md")
				}
				if l.TargetSection != "" {
					t.Errorf("TargetSection = %q, want empty", l.TargetSection)
				}
				if l.LinkType != "markdown" {
					t.Errorf("LinkType = %q, want %q", l.LinkType, "markdown")
				}
			},
		},
		{
			name:      "link with fragment",
			filePath:  "docs/README.md",
			lines:     []string{"See [trust zones](./ARCHITECTURE.md#trust-zones) for details."},
			wantCount: 1,
			checkFirst: func(t *testing.T, l Link) {
				t.Helper()
				if l.TargetPath != "docs/ARCHITECTURE.md" {
					t.Errorf("TargetPath = %q, want %q", l.TargetPath, "docs/ARCHITECTURE.md")
				}
				if l.TargetSection != "trust-zones" {
					t.Errorf("TargetSection = %q, want %q", l.TargetSection, "trust-zones")
				}
			},
		},
		{
			name:      "local anchor resolves to same file",
			filePath:  "docs/README.md",
			lines:     []string{"Jump to [trust zones](#trust-zones)."},
			wantCount: 1,
			checkFirst: func(t *testing.T, l Link) {
				t.Helper()
				if l.TargetPath != "docs/README.md" {
					t.Errorf("TargetPath = %q, want %q", l.TargetPath, "docs/README.md")
				}
				if l.TargetSection != "trust-zones" {
					t.Errorf("TargetSection = %q, want %q", l.TargetSection, "trust-zones")
				}
				if l.SourceLine != 1 {
					t.Errorf("SourceLine = %d, want 1", l.SourceLine)
				}
			},
		},
		{
			name:      "collection-root path resolves from memory file",
			filePath:  "/vault/memory/knowledge/projects.md",
			lines:     []string{"See [context](docs/context/agentic-commerce.md) for details."},
			wantCount: 1,
			checkFirst: func(t *testing.T, l Link) {
				t.Helper()
				if l.TargetPath != "/vault/docs/context/agentic-commerce.md" {
					t.Errorf("TargetPath = %q, want %q", l.TargetPath, "/vault/docs/context/agentic-commerce.md")
				}
			},
		},
		{
			name:      "external link skipped",
			filePath:  "README.md",
			lines:     []string{"Visit [docs](https://example.com) for more."},
			wantCount: 0,
		},
		{
			name:      "http link skipped",
			filePath:  "README.md",
			lines:     []string{"Visit [docs](http://example.com) for more."},
			wantCount: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sections := ParseSections(tt.filePath, tt.lines)
			links := ExtractMarkdownLinks(tt.filePath, tt.lines, sections)
			if len(links) != tt.wantCount {
				t.Fatalf("got %d links, want %d", len(links), tt.wantCount)
			}
			if tt.checkFirst != nil && len(links) > 0 {
				tt.checkFirst(t, links[0])
			}
		})
	}
}

func TestExtractWikilinks(t *testing.T) {
	tests := []struct {
		name       string
		filePath   string
		lines      []string
		wantCount  int
		checkFirst func(t *testing.T, l Link)
	}{
		{
			name:      "simple wikilink",
			filePath:  "notes.md",
			lines:     []string{"See [[PLAN.2026-02-12]] for the plan."},
			wantCount: 1,
			checkFirst: func(t *testing.T, l Link) {
				t.Helper()
				if l.TargetPath != "PLAN.2026-02-12.md" {
					t.Errorf("TargetPath = %q, want %q", l.TargetPath, "PLAN.2026-02-12.md")
				}
				if l.TargetSection != "" {
					t.Errorf("TargetSection = %q, want empty", l.TargetSection)
				}
				if l.LinkType != "wikilink" {
					t.Errorf("LinkType = %q, want %q", l.LinkType, "wikilink")
				}
			},
		},
		{
			name:      "wikilink with fragment and display",
			filePath:  "summary.md",
			lines:     []string{"See [[PLAN.2026-02-12#Overview|14-22]] details."},
			wantCount: 1,
			checkFirst: func(t *testing.T, l Link) {
				t.Helper()
				if l.TargetPath != "PLAN.2026-02-12.md" {
					t.Errorf("TargetPath = %q, want %q", l.TargetPath, "PLAN.2026-02-12.md")
				}
				if l.TargetSection != "overview" {
					t.Errorf("TargetSection = %q, want %q", l.TargetSection, "overview")
				}
			},
		},
		{
			name:      "wikilink already has .md",
			filePath:  "notes.md",
			lines:     []string{"Link to [[README.md]]."},
			wantCount: 1,
			checkFirst: func(t *testing.T, l Link) {
				t.Helper()
				if l.TargetPath != "README.md" {
					t.Errorf("TargetPath = %q, want %q", l.TargetPath, "README.md")
				}
			},
		},
		{
			name:      "wikilink resolved relative to source directory",
			filePath:  "docs/notes.md",
			lines:     []string{"See [[PLAN]] for the plan."},
			wantCount: 1,
			checkFirst: func(t *testing.T, l Link) {
				t.Helper()
				if l.TargetPath != "docs/PLAN.md" {
					t.Errorf("TargetPath = %q, want %q", l.TargetPath, "docs/PLAN.md")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sections := ParseSections(tt.filePath, tt.lines)
			links := ExtractWikilinks(tt.filePath, tt.lines, sections)
			if len(links) != tt.wantCount {
				t.Fatalf("got %d links, want %d", len(links), tt.wantCount)
			}
			if tt.checkFirst != nil && len(links) > 0 {
				tt.checkFirst(t, links[0])
			}
		})
	}
}

func TestExtractPathReferences(t *testing.T) {
	filePath := "/vault/memory/templates/agentic-commerce/agent.md"
	lines := []string{
		"# Agent",
		"Read `mem read docs/agent-anatomy/INFRASTRUCTURE.md --lines 81-97` for ports.",
		"{{include ../docs/context/agentic-commerce.md}}",
		"- `docs/agent-anatomy/01-api-reference.md` — API endpoints and contracts",
		"- `memory/knowledge/work.md` — curated work facts",
		"`docs/ai-engineer/` is a directory, not a file link.",
	}

	sections := ParseSections(filePath, lines)
	links := ExtractPathReferences(filePath, lines, sections)
	if len(links) != 4 {
		t.Fatalf("got %d links, want 4", len(links))
	}

	want := []struct {
		targetPath string
		linkType   string
		raw        string
	}{
		{
			targetPath: "/vault/docs/agent-anatomy/INFRASTRUCTURE.md",
			linkType:   "read_command",
			raw:        "mem read docs/agent-anatomy/INFRASTRUCTURE.md",
		},
		{
			targetPath: "/vault/docs/context/agentic-commerce.md",
			linkType:   "include",
			raw:        "{{include ../docs/context/agentic-commerce.md}}",
		},
		{
			targetPath: "/vault/docs/agent-anatomy/01-api-reference.md",
			linkType:   "path_ref",
			raw:        "`docs/agent-anatomy/01-api-reference.md`",
		},
		{
			targetPath: "/vault/memory/knowledge/work.md",
			linkType:   "path_ref",
			raw:        "`memory/knowledge/work.md`",
		},
	}

	for i, link := range links {
		if link.TargetPath != want[i].targetPath {
			t.Errorf("link %d TargetPath = %q, want %q", i, link.TargetPath, want[i].targetPath)
		}
		if link.LinkType != want[i].linkType {
			t.Errorf("link %d LinkType = %q, want %q", i, link.LinkType, want[i].linkType)
		}
		if link.Raw != want[i].raw {
			t.Errorf("link %d Raw = %q, want %q", i, link.Raw, want[i].raw)
		}
		if link.SourceSection != "agent" {
			t.Errorf("link %d SourceSection = %q, want %q", i, link.SourceSection, "agent")
		}
	}
}

func TestExtractAttentionLinks(t *testing.T) {
	data := []byte(`{
		"document": "PLAN.md",
		"blocks": [
			{"lines": [10, 20], "title": "Overview", "why_look": "important"},
			{"lines": [30, 40], "title": "Details", "why_look": "context"}
		]
	}`)

	links, err := ExtractAttentionLinks("PLAN.attention.json", data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(links) != 2 {
		t.Fatalf("got %d links, want 2", len(links))
	}

	if links[0].TargetPath != "PLAN.md" {
		t.Errorf("TargetPath = %q, want %q", links[0].TargetPath, "PLAN.md")
	}
	if links[0].LinkType != "attention_lines" {
		t.Errorf("LinkType = %q, want %q", links[0].LinkType, "attention_lines")
	}
	if links[0].SourceLine != 0 {
		t.Errorf("SourceLine = %d, want 0", links[0].SourceLine)
	}
	if links[1].Raw != "lines 30-40: Details" {
		t.Errorf("Raw = %q, want %q", links[1].Raw, "lines 30-40: Details")
	}
}

func TestExtractAttentionLinks_invalid(t *testing.T) {
	_, err := ExtractAttentionLinks("bad.attention.json", []byte("not json"))
	if err == nil {
		t.Error("expected error for invalid JSON, got nil")
	}
}

func TestExtractFrontmatterSource(t *testing.T) {
	lines := []string{
		"---",
		"title: Summary",
		"source: [[PLAN.2026-02-12]]",
		"---",
		"# Content",
	}

	link := ExtractFrontmatterSource("summary.md", lines)
	if link == nil {
		t.Fatal("expected a link, got nil")
	}
	if link.TargetPath != "PLAN.2026-02-12.md" {
		t.Errorf("TargetPath = %q, want %q", link.TargetPath, "PLAN.2026-02-12.md")
	}
	if link.LinkType != "frontmatter_source" {
		t.Errorf("LinkType = %q, want %q", link.LinkType, "frontmatter_source")
	}
	if link.SourceLine != 3 {
		t.Errorf("SourceLine = %d, want 3", link.SourceLine)
	}
}

func TestExtractFrontmatterSource_none(t *testing.T) {
	lines := []string{
		"# No frontmatter",
		"Just content.",
	}

	link := ExtractFrontmatterSource("test.md", lines)
	if link != nil {
		t.Errorf("expected nil, got %+v", link)
	}
}

func TestBuildBacklinks(t *testing.T) {
	links := []Link{
		{
			SourcePath:    "a.md",
			SourceSection: "intro",
			TargetPath:    "b.md",
			TargetSection: "details",
			LinkType:      "markdown",
			Raw:           "[b](b.md#details)",
		},
	}

	backlinks := BuildBacklinks(links)
	if len(backlinks) != 1 {
		t.Fatalf("got %d backlinks, want 1", len(backlinks))
	}

	bl := backlinks[0]
	if bl.SourcePath != "b.md" {
		t.Errorf("SourcePath = %q, want %q", bl.SourcePath, "b.md")
	}
	if bl.SourceSection != "details" {
		t.Errorf("SourceSection = %q, want %q", bl.SourceSection, "details")
	}
	if bl.TargetPath != "a.md" {
		t.Errorf("TargetPath = %q, want %q", bl.TargetPath, "a.md")
	}
	if bl.TargetSection != "intro" {
		t.Errorf("TargetSection = %q, want %q", bl.TargetSection, "intro")
	}
}
