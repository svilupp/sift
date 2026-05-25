package chunk

import (
	"fmt"
	"strings"
	"testing"
)

func TestFromSections_BasicSections(t *testing.T) {
	content := `## Introduction

This is the introduction section.
It has some content here.

## Methods

Here we describe the methods used.
Some more method details.

## Results

The results are presented here.
More result details follow.
`
	chunks := FromSections("test.md", []byte(content), DefaultSectionOptions())

	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}

	tests := []struct {
		idx          int
		sectionID    string
		heading      string
		level        int
		wantStartGte int
		wantEndGte   int
	}{
		{0, "introduction", "Introduction", 2, 1, 4},
		{1, "methods", "Methods", 2, 6, 8},
		{2, "results", "Results", 2, 10, 13},
	}

	for _, tt := range tests {
		t.Run(tt.heading, func(t *testing.T) {
			c := chunks[tt.idx]
			if c.SectionID != tt.sectionID {
				t.Errorf("SectionID = %q, want %q", c.SectionID, tt.sectionID)
			}
			if c.Heading != tt.heading {
				t.Errorf("Heading = %q, want %q", c.Heading, tt.heading)
			}
			if c.HeadingLevel != tt.level {
				t.Errorf("HeadingLevel = %d, want %d", c.HeadingLevel, tt.level)
			}
			if c.Order != tt.idx {
				t.Errorf("Order = %d, want %d", c.Order, tt.idx)
			}
			if c.StartLine < tt.wantStartGte {
				t.Errorf("StartLine = %d, want >= %d", c.StartLine, tt.wantStartGte)
			}
		})
	}
}

func TestFromSections_FrontmatterAndSections(t *testing.T) {
	content := `---
title: Test Document
---

## First Section

Content of the first section.

## Second Section

Content of the second section.
`
	chunks := FromSections("test.md", []byte(content), DefaultSectionOptions())

	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks (frontmatter + 2 sections), got %d", len(chunks))
	}

	t.Run("frontmatter", func(t *testing.T) {
		c := chunks[0]
		if !c.IsFrontmatter {
			t.Error("expected IsFrontmatter=true")
		}
		if c.SectionID != "frontmatter" {
			t.Errorf("SectionID = %q, want %q", c.SectionID, "frontmatter")
		}
		if c.Order != 0 {
			t.Errorf("Order = %d, want 0", c.Order)
		}
	})

	t.Run("first_section", func(t *testing.T) {
		c := chunks[1]
		if c.SectionID != "first-section" {
			t.Errorf("SectionID = %q, want %q", c.SectionID, "first-section")
		}
		if c.Order != 1 {
			t.Errorf("Order = %d, want 1", c.Order)
		}
	})

	t.Run("second_section", func(t *testing.T) {
		c := chunks[2]
		if c.SectionID != "second-section" {
			t.Errorf("SectionID = %q, want %q", c.SectionID, "second-section")
		}
		if c.Order != 2 {
			t.Errorf("Order = %d, want 2", c.Order)
		}
	})
}

func TestFromSections_NoHeadingsFallback(t *testing.T) {
	content := `This is a plain text file.
It has no headings at all.
Just some regular content.
`
	chunks := FromSections("plain.txt", []byte(content), DefaultSectionOptions())

	if len(chunks) != 1 {
		t.Fatalf("expected 1 chunk for no-heading file, got %d", len(chunks))
	}

	c := chunks[0]
	if c.SectionID != "" {
		t.Errorf("SectionID = %q, want empty", c.SectionID)
	}
	if c.HeadingLevel != 0 {
		t.Errorf("HeadingLevel = %d, want 0", c.HeadingLevel)
	}
	if c.CharCount == 0 {
		t.Error("expected non-zero CharCount")
	}
}

func TestFromSections_LargeSectionSplit(t *testing.T) {
	// Build a section with > 3000 chars.
	var b strings.Builder
	b.WriteString("## Big Section\n\n")
	for i := 0; i < 100; i++ {
		b.WriteString("This is a line of content that adds some characters to make it large enough. ")
		b.WriteString("Adding more text to be sure we exceed the threshold.\n")
	}
	content := b.String()

	opts := DefaultSectionOptions()
	opts.MaxSectionChars = 500 // Lower threshold to force splitting.
	chunks := FromSections("big.md", []byte(content), opts)

	if len(chunks) < 2 {
		t.Fatalf("expected section to be split into >= 2 chunks, got %d", len(chunks))
	}

	// First chunk should have the original section ID.
	if chunks[0].SectionID != "big-section" {
		t.Errorf("first chunk SectionID = %q, want %q", chunks[0].SectionID, "big-section")
	}

	// Subsequent chunks should have ::N suffix.
	if len(chunks) > 1 && !strings.HasPrefix(chunks[1].SectionID, "big-section::") {
		t.Errorf("second chunk SectionID = %q, want prefix %q", chunks[1].SectionID, "big-section::")
	}
}

func TestFromSections_NoHeadingsLargeFile(t *testing.T) {
	// Build a file with no headings that exceeds MaxSectionChars.
	var b strings.Builder
	for i := 0; i < 200; i++ {
		b.WriteString("This is a line of plain text content without any markdown headings at all.\n")
	}
	content := b.String()

	opts := DefaultSectionOptions()
	opts.MaxSectionChars = 500
	chunks := FromSections("large-plain.txt", []byte(content), opts)

	if len(chunks) < 2 {
		t.Fatalf("expected large no-heading file to be split into >= 2 chunks, got %d", len(chunks))
	}

	// First chunk should have empty SectionID.
	if chunks[0].SectionID != "" {
		t.Errorf("first chunk SectionID = %q, want empty", chunks[0].SectionID)
	}

	// Subsequent chunks should have part-N IDs.
	if len(chunks) > 1 {
		if !strings.HasPrefix(chunks[1].SectionID, "part-") {
			t.Errorf("second chunk SectionID = %q, want prefix %q", chunks[1].SectionID, "part-")
		}
	}

	// All chunks should have HeadingLevel 0.
	for i, c := range chunks {
		if c.HeadingLevel != 0 {
			t.Errorf("chunk %d HeadingLevel = %d, want 0", i, c.HeadingLevel)
		}
		if c.CharCount == 0 {
			t.Errorf("chunk %d has zero CharCount", i)
		}
	}
}

func TestFromSections_SmallSection(t *testing.T) {
	content := `## Tiny

Hi.

## Normal

This section has more content.
Enough to be above the minimum.
More lines here for padding.
`
	chunks := FromSections("small.md", []byte(content), DefaultSectionOptions())

	// Both sections should produce chunks (small sections are kept as-is in v1).
	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	// Find the tiny section.
	found := false
	for _, c := range chunks {
		if c.SectionID == "tiny" {
			found = true
			if c.CharCount == 0 {
				t.Error("expected non-zero CharCount for small section")
			}
		}
	}
	if !found {
		t.Error("expected to find chunk with SectionID='tiny'")
	}
}

func TestFromSections_ChunkHash(t *testing.T) {
	content := `## Section One

Some content here.
`
	chunks := FromSections("hash.md", []byte(content), DefaultSectionOptions())

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}

	for i, c := range chunks {
		if c.ChunkHash == "" {
			t.Errorf("chunk %d has empty ChunkHash", i)
		}
		if len(c.ChunkHash) != 16 {
			t.Errorf("chunk %d ChunkHash length = %d, want 16", i, len(c.ChunkHash))
		}
	}
}

func TestFromSections_NoCrossSectionOverlap(t *testing.T) {
	content := `## Overview

Workflow orchestration for batch processing.

## Setup

Collection bootstrapping and sync steps.
`

	opts := DefaultSectionOptions()
	opts.OverlapLines = 2
	chunks := FromSections("cross-section.md", []byte(content), opts)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}

	if strings.Contains(chunks[1].Content, "Workflow orchestration") {
		t.Fatalf("setup chunk should not include overview content, got: %q", chunks[1].Content)
	}
	if !strings.Contains(chunks[1].Content, "Collection bootstrapping") {
		t.Fatalf("setup chunk missing own content: %q", chunks[1].Content)
	}
}

func TestWrapRowChunks(t *testing.T) {
	rowChunks := []Chunk{
		{Order: 0, StartLine: 1, EndLine: 25, Content: "line 1\nline 2", CharCount: 13},
		{Order: 1, StartLine: 26, EndLine: 50, Content: "line 26\nline 27", CharCount: 15},
	}

	wrapped := WrapRowChunks(rowChunks)

	if len(wrapped) != 2 {
		t.Fatalf("expected 2 wrapped chunks, got %d", len(wrapped))
	}

	for i, w := range wrapped {
		t.Run(string(rune('A'+i)), func(t *testing.T) {
			if w.SectionID != "" {
				t.Errorf("SectionID = %q, want empty", w.SectionID)
			}
			if w.HeadingLevel != 0 {
				t.Errorf("HeadingLevel = %d, want 0", w.HeadingLevel)
			}
			if w.ParentIdx != -1 {
				t.Errorf("ParentIdx = %d, want -1", w.ParentIdx)
			}
			if w.ChunkHash == "" {
				t.Error("expected non-empty ChunkHash")
			}
			if w.Order != rowChunks[i].Order {
				t.Errorf("Order = %d, want %d", w.Order, rowChunks[i].Order)
			}
			if w.CharCount != rowChunks[i].CharCount {
				t.Errorf("CharCount = %d, want %d", w.CharCount, rowChunks[i].CharCount)
			}
		})
	}
}

func TestFromSections_LinksExtraction(t *testing.T) {
	content := `## Introduction

See [other doc](other.md) for details.

## References

Check [[WikiTarget]] and [another](ref.md#section).
`
	chunks := FromSections("links.md", []byte(content), DefaultSectionOptions())

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks, got %d", len(chunks))
	}

	// Find intro chunk and check it has a link.
	introFound := false
	refsFound := false
	for _, c := range chunks {
		if c.SectionID == "introduction" {
			introFound = true
			if len(c.Links) != 1 {
				t.Errorf("introduction links count = %d, want 1", len(c.Links))
			} else if c.Links[0].TargetPath == "" {
				t.Error("expected non-empty TargetPath")
			}
		}
		if c.SectionID == "references" {
			refsFound = true
			if len(c.Links) != 2 {
				t.Errorf("references links count = %d, want 2", len(c.Links))
			}
		}
	}
	if !introFound {
		t.Error("expected to find introduction chunk")
	}
	if !refsFound {
		t.Error("expected to find references chunk")
	}
}

func TestFromSections_ParentIdx(t *testing.T) {
	content := `# Top Level

Intro text.

## Sub Section

Sub content.

### Deep Section

Deep content.
`
	chunks := FromSections("hierarchy.md", []byte(content), DefaultSectionOptions())

	if len(chunks) != 3 {
		t.Fatalf("expected 3 chunks, got %d", len(chunks))
	}

	// H1 should have ParentIdx = -1
	t.Run("H1_parent", func(t *testing.T) {
		if chunks[0].ParentIdx != -1 {
			t.Errorf("H1 ParentIdx = %d, want -1", chunks[0].ParentIdx)
		}
		if chunks[0].HeadingLevel != 1 {
			t.Errorf("H1 HeadingLevel = %d, want 1", chunks[0].HeadingLevel)
		}
	})

	// H2 should have ParentIdx pointing to H1 (index 0)
	t.Run("H2_parent", func(t *testing.T) {
		if chunks[1].ParentIdx != 0 {
			t.Errorf("H2 ParentIdx = %d, want 0", chunks[1].ParentIdx)
		}
		if chunks[1].HeadingLevel != 2 {
			t.Errorf("H2 HeadingLevel = %d, want 2", chunks[1].HeadingLevel)
		}
	})

	// H3 should have ParentIdx pointing to H2 (index 1)
	t.Run("H3_parent", func(t *testing.T) {
		if chunks[2].ParentIdx != 1 {
			t.Errorf("H3 ParentIdx = %d, want 1", chunks[2].ParentIdx)
		}
		if chunks[2].HeadingLevel != 3 {
			t.Errorf("H3 HeadingLevel = %d, want 3", chunks[2].HeadingLevel)
		}
	})
}

func TestFromSections_PreambleContent(t *testing.T) {
	content := `Some preamble text before any heading.

## First Heading

Content after heading.
`
	chunks := FromSections("preamble.md", []byte(content), DefaultSectionOptions())

	if len(chunks) < 2 {
		t.Fatalf("expected at least 2 chunks (preamble + section), got %d", len(chunks))
	}

	// Preamble should have empty SectionID and level 0.
	t.Run("preamble", func(t *testing.T) {
		c := chunks[0]
		if c.SectionID != "" {
			t.Errorf("preamble SectionID = %q, want empty", c.SectionID)
		}
		if c.HeadingLevel != 0 {
			t.Errorf("preamble HeadingLevel = %d, want 0", c.HeadingLevel)
		}
	})
}

func TestFromSections_EmptyContent(t *testing.T) {
	chunks := FromSections("empty.md", []byte(""), DefaultSectionOptions())
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks for empty content, got %d", len(chunks))
	}
}

// --- Regression tests ---

func TestFromSections_NoEmptyChunks(t *testing.T) {
	// Document with sections that could produce empty sub-chunks:
	// blank lines between headings, whitespace-only sections.
	var b strings.Builder
	b.WriteString("## Section A\n")
	b.WriteString("\n\n\n\n\n") // blank lines
	b.WriteString("Some actual content in section A.\n")
	b.WriteString("\n\n\n\n\n")
	b.WriteString("## Section B\n")
	b.WriteString("   \n   \n   \n   \n") // whitespace-only lines
	b.WriteString("Content for section B here.\n")
	b.WriteString("\n\n")
	b.WriteString("## Section C\n")
	b.WriteString("\n\n\n")
	b.WriteString("Final content in C.\n")

	opts := SectionOptions{
		MaxSectionChars: 100, // small to force sub-chunk splitting
		MinSectionChars: 0,
		OverlapLines:    2,
	}
	chunks := FromSections("empty-guard.md", []byte(b.String()), opts)

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 chunk")
	}

	for i, c := range chunks {
		if c.CharCount <= 0 {
			t.Errorf("chunk %d (SectionID=%q) has CharCount=%d, want > 0", i, c.SectionID, c.CharCount)
		}
		if strings.TrimSpace(c.Content) == "" {
			t.Errorf("chunk %d (SectionID=%q) has empty/whitespace-only Content", i, c.SectionID)
		}
	}
}

func TestFromSections_EmptyLinesSection(t *testing.T) {
	// A heading followed by only blank lines before the next heading.
	content := `## Has Content

Real content here.

## Empty Section



## Another Section

More real content.
`
	opts := DefaultSectionOptions()
	chunks := FromSections("blank-section.md", []byte(content), opts)

	for i, c := range chunks {
		if c.IsFrontmatter {
			continue
		}
		if strings.TrimSpace(c.Content) == "" {
			t.Errorf("chunk %d (SectionID=%q) is empty/whitespace-only — should not be produced", i, c.SectionID)
		}
		if c.CharCount <= 0 {
			t.Errorf("chunk %d (SectionID=%q) has CharCount=%d, want > 0", i, c.SectionID, c.CharCount)
		}
	}
}

func TestFromSections_SplitNeverProducesZeroCharChunks(t *testing.T) {
	// Large document with blocks of whitespace-only lines interspersed with content.
	var b strings.Builder
	for i := 0; i < 50; i++ {
		b.WriteString(fmt.Sprintf("## Section %d\n", i))
		// Alternate between content and whitespace blocks.
		if i%3 == 0 {
			b.WriteString("\n   \n  \n\n") // whitespace only
		}
		for j := 0; j < 5; j++ {
			b.WriteString(fmt.Sprintf("Line %d of section %d with some padding text to add characters.\n", j, i))
		}
		b.WriteString("\n\n")
	}

	opts := SectionOptions{
		MaxSectionChars: 50, // very small to force many sub-chunk splits
		MinSectionChars: 0,
		OverlapLines:    1,
	}
	chunks := FromSections("large-whitespace.md", []byte(b.String()), opts)

	if len(chunks) == 0 {
		t.Fatal("expected chunks from large document")
	}

	for i, c := range chunks {
		if c.CharCount <= 0 {
			t.Errorf("chunk %d (Order=%d, SectionID=%q) has CharCount=%d, want > 0",
				i, c.Order, c.SectionID, c.CharCount)
		}
	}
}

func TestFromSections_LargeDocumentConsistency(t *testing.T) {
	// Build a realistic 1000+ line markdown document with multiple heading levels.
	var b strings.Builder
	b.WriteString("---\ntitle: Large Test Document\nauthor: Test\n---\n\n")
	lineCount := 5 // frontmatter lines + blank

	for i := 0; i < 10; i++ {
		b.WriteString(fmt.Sprintf("# Chapter %d\n\n", i+1))
		lineCount += 2
		for j := 0; j < 5; j++ {
			b.WriteString(fmt.Sprintf("## Section %d.%d\n\n", i+1, j+1))
			lineCount += 2
			for k := 0; k < 3; k++ {
				b.WriteString(fmt.Sprintf("### Subsection %d.%d.%d\n\n", i+1, j+1, k+1))
				lineCount += 2
				for l := 0; l < 8; l++ {
					b.WriteString(fmt.Sprintf("This is line %d of subsection %d.%d.%d with enough text to be meaningful content.\n", l+1, i+1, j+1, k+1))
					lineCount++
				}
				b.WriteString("\n")
				lineCount++
			}
		}
	}

	opts := SectionOptions{
		MaxSectionChars: 500,
		MinSectionChars: 50,
		OverlapLines:    3,
	}
	chunks := FromSections("large-doc.md", []byte(b.String()), opts)

	if len(chunks) < 10 {
		t.Fatalf("expected many chunks from large document, got %d", len(chunks))
	}

	// Verify all chunks have non-empty Content.
	for i, c := range chunks {
		if strings.TrimSpace(c.Content) == "" {
			t.Errorf("chunk %d (Order=%d, SectionID=%q) has empty Content", i, c.Order, c.SectionID)
		}
	}

	// Verify all chunks have CharCount > 0.
	for i, c := range chunks {
		if c.CharCount <= 0 {
			t.Errorf("chunk %d (Order=%d) has CharCount=%d, want > 0", i, c.Order, c.CharCount)
		}
	}

	// Verify all chunks have non-empty ChunkHash.
	for i, c := range chunks {
		if c.ChunkHash == "" {
			t.Errorf("chunk %d (Order=%d) has empty ChunkHash", i, c.Order)
		}
	}

	// Verify StartLine < EndLine for all chunks.
	for i, c := range chunks {
		if c.StartLine >= c.EndLine {
			t.Errorf("chunk %d (Order=%d): StartLine=%d >= EndLine=%d",
				i, c.Order, c.StartLine, c.EndLine)
		}
	}

	// Verify Order values are sequential starting from 0.
	for i, c := range chunks {
		if c.Order != i {
			t.Errorf("chunk %d has Order=%d, want %d", i, c.Order, i)
		}
	}

	// Verify no gaps in line ranges (each chunk's StartLine <= previous chunk's EndLine + 1).
	// Skip frontmatter-to-body transition which may have a gap.
	for i := 1; i < len(chunks); i++ {
		prev := chunks[i-1]
		cur := chunks[i]
		// Allow gap after frontmatter chunk.
		if prev.IsFrontmatter {
			continue
		}
		if cur.StartLine > prev.EndLine+1 {
			t.Errorf("gap between chunk %d (EndLine=%d) and chunk %d (StartLine=%d)",
				i-1, prev.EndLine, i, cur.StartLine)
		}
	}

	// Verify no two consecutive chunks have the same SectionID unless they're sub-chunks (with :: suffix).
	for i := 1; i < len(chunks); i++ {
		prev := chunks[i-1]
		cur := chunks[i]
		if prev.SectionID == cur.SectionID && prev.SectionID != "" {
			// Same SectionID is only OK if they are sub-chunks (one has :: suffix of the other).
			if !strings.Contains(prev.SectionID, "::") && !strings.Contains(cur.SectionID, "::") {
				t.Errorf("consecutive chunks %d and %d have same SectionID=%q without sub-chunk suffix",
					i-1, i, cur.SectionID)
			}
		}
	}
}

func TestWrapRowChunks_SkipsEmpty(t *testing.T) {
	// WrapRowChunks wraps whatever it gets — verify hash is computed even for empty content.
	rowChunks := []Chunk{
		{Order: 0, StartLine: 1, EndLine: 5, Content: "", CharCount: 0},
		{Order: 1, StartLine: 6, EndLine: 10, Content: "   ", CharCount: 3},
		{Order: 2, StartLine: 11, EndLine: 20, Content: "real content here", CharCount: 17},
	}

	wrapped := WrapRowChunks(rowChunks)

	if len(wrapped) != 3 {
		t.Fatalf("expected 3 wrapped chunks, got %d", len(wrapped))
	}

	for i, w := range wrapped {
		if w.ChunkHash == "" {
			t.Errorf("chunk %d has empty ChunkHash even though WrapRowChunks should always hash", i)
		}
		if len(w.ChunkHash) != 16 {
			t.Errorf("chunk %d ChunkHash length = %d, want 16", i, len(w.ChunkHash))
		}
		if w.Order != rowChunks[i].Order {
			t.Errorf("chunk %d Order = %d, want %d", i, w.Order, rowChunks[i].Order)
		}
	}

	// Verify different content produces different hashes.
	if wrapped[0].ChunkHash == wrapped[2].ChunkHash {
		t.Error("empty and non-empty content should have different hashes")
	}

	// Verify empty and whitespace produce different hashes.
	if wrapped[0].ChunkHash == wrapped[1].ChunkHash {
		t.Error("empty string and whitespace should have different hashes")
	}
}
