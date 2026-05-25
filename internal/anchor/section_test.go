package anchor

import (
	"testing"
)

func TestSlugify(t *testing.T) {
	t.Helper()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{"simple two words", "Trust Zones", "trust-zones"},
		{"version string", "API v1 Contract", "api-v1-contract"},
		{"multi word", "SLOs Per Workflow Type", "slos-per-workflow-type"},
		{"backticks and parens", "`mem index --list` (enhanced)", "mem-index-list-enhanced"},
		{"colon and mixed case", "Step 2: Choose The Webhook Secret", "step-2-choose-the-webhook-secret"},
		{"empty string", "", ""},
		{"only special chars", "!@#$%", ""},
		{"already slug", "hello-world", "hello-world"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Slugify(tt.in)
			if got != tt.want {
				t.Errorf("Slugify(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestParseSections(t *testing.T) {
	t.Run("three headings at different levels", func(t *testing.T) {
		lines := []string{
			"# Introduction",
			"Some text here.",
			"## Details",
			"More text.",
			"### Sub-details",
			"Even more text.",
		}

		sections := ParseSections("test.md", lines)
		if len(sections) != 3 {
			t.Fatalf("got %d sections, want 3", len(sections))
		}

		// H1 Introduction: lines 1-6 (contains everything since no same-or-higher heading)
		assertSection(t, sections[0], "introduction", 1, 1, 6)
		// H2 Details: lines 3-6
		assertSection(t, sections[1], "details", 2, 3, 6)
		// H3 Sub-details: lines 5-6
		assertSection(t, sections[2], "sub-details", 3, 5, 6)
	})

	t.Run("no headings", func(t *testing.T) {
		lines := []string{
			"Just some text.",
			"No headings here.",
		}

		sections := ParseSections("test.md", lines)
		if len(sections) != 0 {
			t.Fatalf("got %d sections, want 0", len(sections))
		}
	})

	t.Run("frontmatter before first heading", func(t *testing.T) {
		lines := []string{
			"---",
			"title: My Doc",
			"---",
			"",
			"# Main Heading",
			"Content.",
		}

		sections := ParseSections("test.md", lines)
		if len(sections) != 2 {
			t.Fatalf("got %d sections, want 2", len(sections))
		}

		// Level-0 section for content before heading.
		assertSection(t, sections[0], "", 0, 1, 4)
		// H1 section.
		assertSection(t, sections[1], "main-heading", 1, 5, 6)
	})

	t.Run("nested headings verify EndLine", func(t *testing.T) {
		lines := []string{
			"# Top",
			"text",
			"## Section A",
			"text a",
			"### Subsection A1",
			"text a1",
			"## Section B",
			"text b",
		}

		sections := ParseSections("test.md", lines)
		if len(sections) != 4 {
			t.Fatalf("got %d sections, want 4", len(sections))
		}

		// H1 Top: ends before nothing, so EOF=8
		assertSection(t, sections[0], "top", 1, 1, 8)
		// H2 Section A: ends before H2 Section B -> line 6
		assertSection(t, sections[1], "section-a", 2, 3, 6)
		// H3 Subsection A1: ends before H2 Section B -> line 6
		assertSection(t, sections[2], "subsection-a1", 3, 5, 6)
		// H2 Section B: ends at EOF -> line 8
		assertSection(t, sections[3], "section-b", 2, 7, 8)
	})
}

func TestSectionAtLine(t *testing.T) {
	sections := []Section{
		{ID: "intro", Level: 1, StartLine: 1, EndLine: 10},
		{ID: "details", Level: 2, StartLine: 3, EndLine: 8},
		{ID: "sub", Level: 3, StartLine: 5, EndLine: 7},
	}

	tests := []struct {
		name   string
		line   int
		wantID string
	}{
		{"line within deepest section", 6, "sub"},
		{"line in level-2 but not level-3", 4, "details"},
		{"line on heading returns that section", 5, "sub"},
		{"line at end of level-1 only", 9, "intro"},
		{"line outside all sections", 11, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SectionAtLine(sections, tt.line)
			if tt.wantID == "" {
				if got != nil {
					t.Errorf("SectionAtLine(%d) = %q, want nil", tt.line, got.ID)
				}
				return
			}
			if got == nil {
				t.Fatalf("SectionAtLine(%d) = nil, want %q", tt.line, tt.wantID)
			}
			if got.ID != tt.wantID {
				t.Errorf("SectionAtLine(%d) = %q, want %q", tt.line, got.ID, tt.wantID)
			}
		})
	}
}

func TestMapChunkToSection(t *testing.T) {
	sections := []Section{
		{ID: "intro", Level: 1, StartLine: 1, EndLine: 10},
		{ID: "details", Level: 2, StartLine: 3, EndLine: 8},
		{ID: "sub", Level: 3, StartLine: 5, EndLine: 7},
	}

	t.Run("chunk within single section", func(t *testing.T) {
		got := MapChunkToSection(sections, 5, 7)
		if got == nil || got.ID != "sub" {
			t.Errorf("got %v, want section 'sub'", got)
		}
	})

	t.Run("chunk spanning two sections uses midpoint", func(t *testing.T) {
		// Chunk lines 4-9, midpoint = 6, which falls in "sub" (5-7).
		got := MapChunkToSection(sections, 4, 9)
		if got == nil || got.ID != "sub" {
			t.Errorf("got %v, want section 'sub'", got)
		}
	})

	t.Run("chunk outside all sections", func(t *testing.T) {
		got := MapChunkToSection(sections, 20, 30)
		if got != nil {
			t.Errorf("got %v, want nil", got)
		}
	})
}

func TestMapChunkToSection_midpointOverlap(t *testing.T) {
	// Simulate two adjacent sections where chunk overlap causes the start line
	// to fall in the previous section. Midpoint should pick the correct one.
	sections := []Section{
		{ID: "parent", Level: 2, StartLine: 100, EndLine: 176, FilePath: "doc.md"},
		{ID: "contract-tests", Level: 3, StartLine: 130, EndLine: 142, FilePath: "doc.md"},
		{ID: "trust-zones", Level: 3, StartLine: 143, EndLine: 176, FilePath: "doc.md"},
	}

	// Chunk L135-169: start line 135 is inside "contract-tests" (130-142),
	// but midpoint (135+169)/2 = 152 is inside "trust-zones" (143-176).
	got := MapChunkToSection(sections, 135, 169)
	if got == nil {
		t.Fatal("MapChunkToSection returned nil, want 'trust-zones'")
	}
	if got.ID != "trust-zones" {
		t.Errorf("MapChunkToSection(135, 169) = %q, want %q", got.ID, "trust-zones")
	}
}

func assertSection(t *testing.T, s Section, wantID string, wantLevel, wantStart, wantEnd int) {
	t.Helper()
	if s.ID != wantID {
		t.Errorf("ID = %q, want %q", s.ID, wantID)
	}
	if s.Level != wantLevel {
		t.Errorf("Level = %d, want %d", s.Level, wantLevel)
	}
	if s.StartLine != wantStart {
		t.Errorf("StartLine = %d, want %d", s.StartLine, wantStart)
	}
	if s.EndLine != wantEnd {
		t.Errorf("EndLine = %d, want %d", s.EndLine, wantEnd)
	}
}
