package anchor

import (
	"math"
	"testing"
)

func TestAggregateSections(t *testing.T) {
	idx := &CorpusIndex{
		Files: map[string]*FileSections{
			"doc.md": {
				Path: "doc.md",
				Sections: []Section{
					{ID: "intro", Heading: "Intro", Level: 1, StartLine: 1, EndLine: 10, FilePath: "doc.md"},
					{ID: "details", Heading: "Details", Level: 2, StartLine: 5, EndLine: 10, FilePath: "doc.md"},
				},
			},
		},
	}

	chunks := []ChunkResult{
		{FilePath: "doc.md", StartLine: 1, EndLine: 3, Score: 0.8},
		{FilePath: "doc.md", StartLine: 5, EndLine: 7, Score: 0.9},
		{FilePath: "doc.md", StartLine: 6, EndLine: 8, Score: 0.7},
	}

	results := AggregateSections(idx, chunks)

	// Chunk 1 (lines 1-3) maps to "intro" (deepest at line 1 is level 1).
	// Chunk 2 (lines 5-7) maps to "details" (deepest at line 5 is level 2).
	// Chunk 3 (lines 6-8) maps to "details" (deepest at line 6 is level 2).
	// So: intro score=0.8, details score=max(0.9, 0.7)=0.9.

	if len(results) != 2 {
		t.Fatalf("got %d results, want 2", len(results))
	}

	scoreByID := make(map[string]float64)
	for _, r := range results {
		scoreByID[r.Section.ID] = r.Score
	}

	if s, ok := scoreByID["intro"]; !ok || s != 0.8 {
		t.Errorf("intro score = %v, want 0.8", s)
	}
	if s, ok := scoreByID["details"]; !ok || s != 0.9 {
		t.Errorf("details score = %v, want 0.9", s)
	}
}

func TestAggregateSections_unknownFile(t *testing.T) {
	idx := &CorpusIndex{
		Files: map[string]*FileSections{},
	}

	chunks := []ChunkResult{
		{FilePath: "missing.md", StartLine: 1, EndLine: 3, Score: 0.5},
	}

	results := AggregateSections(idx, chunks)
	if len(results) != 0 {
		t.Errorf("got %d results, want 0 for unknown file", len(results))
	}
}

func TestExpandOneHop(t *testing.T) {
	idx := &CorpusIndex{
		Files: map[string]*FileSections{
			"a.md": {
				Path: "a.md",
				Sections: []Section{
					{ID: "intro", Heading: "Intro", Level: 1, StartLine: 1, EndLine: 10, FilePath: "a.md"},
				},
			},
			"b.md": {
				Path: "b.md",
				Sections: []Section{
					{ID: "details", Heading: "Details", Level: 1, StartLine: 1, EndLine: 20, FilePath: "b.md"},
				},
			},
		},
		Links: []Link{
			{
				SourcePath:    "a.md",
				SourceSection: "intro",
				TargetPath:    "b.md",
				TargetSection: "details",
				LinkType:      "markdown",
			},
		},
		Backlinks: []Link{
			{
				SourcePath:    "b.md",
				SourceSection: "details",
				TargetPath:    "a.md",
				TargetSection: "intro",
				LinkType:      "markdown",
			},
		},
	}

	initial := []SectionResult{
		{
			Section:  idx.Files["a.md"].Sections[0],
			Score:    1.0,
			FilePath: "a.md",
		},
	}

	expanded := ExpandOneHop(idx, initial, 0.5)

	// Should have: a.md:intro (1.0) and b.md:details (0.5).
	if len(expanded) != 2 {
		t.Fatalf("got %d results, want 2", len(expanded))
	}

	scoreByKey := make(map[string]float64)
	for _, sr := range expanded {
		key := sr.FilePath + "::" + sr.Section.ID
		scoreByKey[key] = sr.Score
	}

	if s := scoreByKey["a.md::intro"]; s != 1.0 {
		t.Errorf("a.md::intro score = %v, want 1.0", s)
	}
	if s := scoreByKey["b.md::details"]; s != 0.5 {
		t.Errorf("b.md::details score = %v, want 0.5", s)
	}
}

func TestExpandOneHop_dedup(t *testing.T) {
	idx := &CorpusIndex{
		Files: map[string]*FileSections{
			"a.md": {
				Path: "a.md",
				Sections: []Section{
					{ID: "intro", Level: 1, StartLine: 1, EndLine: 10, FilePath: "a.md"},
				},
			},
			"b.md": {
				Path: "b.md",
				Sections: []Section{
					{ID: "sec", Level: 1, StartLine: 1, EndLine: 10, FilePath: "b.md"},
				},
			},
		},
		Links:     []Link{},
		Backlinks: []Link{},
	}

	// b.md already in results with high score; expansion should not lower it.
	initial := []SectionResult{
		{Section: idx.Files["a.md"].Sections[0], Score: 1.0, FilePath: "a.md"},
		{Section: idx.Files["b.md"].Sections[0], Score: 0.9, FilePath: "b.md"},
	}

	expanded := ExpandOneHop(idx, initial, 0.5)
	scoreByKey := make(map[string]float64)
	for _, sr := range expanded {
		key := sr.FilePath + "::" + sr.Section.ID
		scoreByKey[key] = sr.Score
	}

	if s := scoreByKey["b.md::sec"]; s != 0.9 {
		t.Errorf("b.md::sec score = %v, want 0.9 (should not be lowered)", s)
	}
}

func TestHeadingBoost_fullMatch(t *testing.T) {
	results := []SectionResult{
		{Section: Section{ID: "trust-zones", Heading: "Trust Zones"}, Score: 0.5, FilePath: "a.md"},
		{Section: Section{ID: "related-documents", Heading: "Related Documents"}, Score: 0.7, FilePath: "b.md"},
		{Section: Section{ID: "error-budgets", Heading: "Error Budgets"}, Score: 0.3, FilePath: "c.md"},
	}

	boosted := HeadingBoost("trust zones", results, 2.0)

	if len(boosted) != 3 {
		t.Fatalf("got %d results, want 3", len(boosted))
	}

	// "Trust Zones" should get 2.0x boost: 0.5 * 2.0 = 1.0, now ranks #1.
	if boosted[0].Section.ID != "trust-zones" {
		t.Errorf("rank 1 = %q, want 'trust-zones'", boosted[0].Section.ID)
	}
	if boosted[0].Score != 1.0 {
		t.Errorf("trust-zones score = %v, want 1.0", boosted[0].Score)
	}

	// "Related Documents" stays at 0.7 (no match), ranks #2.
	if boosted[1].Section.ID != "related-documents" {
		t.Errorf("rank 2 = %q, want 'related-documents'", boosted[1].Section.ID)
	}
	if boosted[1].Score != 0.7 {
		t.Errorf("related-documents score = %v, want 0.7", boosted[1].Score)
	}

	// "Error Budgets" stays at 0.3, ranks #3.
	if boosted[2].Section.ID != "error-budgets" {
		t.Errorf("rank 3 = %q, want 'error-budgets'", boosted[2].Section.ID)
	}
	if boosted[2].Score != 0.3 {
		t.Errorf("error-budgets score = %v, want 0.3", boosted[2].Score)
	}
}

func TestHeadingBoost_partialMatch(t *testing.T) {
	results := []SectionResult{
		{Section: Section{ID: "api-v1-contract", Heading: "API v1 Contract"}, Score: 0.5, FilePath: "a.md"},
	}

	// Query "api contract validation": 2 of 3 terms match ("api", "contract").
	boosted := HeadingBoost("api contract validation", results, 2.0)

	if len(boosted) != 1 {
		t.Fatalf("got %d results, want 1", len(boosted))
	}

	// matchRatio = 2/3, partial boost = 1 + (2.0 - 1.0) * (2/3) = 1.6667
	// score = 0.5 * 1.6667 ≈ 0.8333
	want := 0.5 * (1 + (2.0-1.0)*(2.0/3.0))
	if math.Abs(boosted[0].Score-want) > 1e-9 {
		t.Errorf("score = %v, want %v", boosted[0].Score, want)
	}
}

func TestExpandOneHop_decayValues(t *testing.T) {
	idx := &CorpusIndex{
		Files: map[string]*FileSections{
			"a.md": {
				Path: "a.md",
				Sections: []Section{
					{ID: "intro", Heading: "Intro", Level: 1, StartLine: 1, EndLine: 10, FilePath: "a.md"},
				},
			},
			"b.md": {
				Path: "b.md",
				Sections: []Section{
					{ID: "details", Heading: "Details", Level: 1, StartLine: 1, EndLine: 20, FilePath: "b.md"},
				},
			},
		},
		Links: []Link{
			{
				SourcePath:    "a.md",
				SourceSection: "intro",
				TargetPath:    "b.md",
				TargetSection: "details",
				LinkType:      "markdown",
			},
		},
		Backlinks: []Link{},
	}

	initial := []SectionResult{
		{Section: idx.Files["a.md"].Sections[0], Score: 1.0, FilePath: "a.md"},
	}

	// Test with decay 0.3.
	expanded03 := ExpandOneHop(idx, initial, 0.3)
	score03 := make(map[string]float64)
	for _, sr := range expanded03 {
		score03[sr.FilePath+"::"+sr.Section.ID] = sr.Score
	}
	if s := score03["b.md::details"]; s != 0.3 {
		t.Errorf("decay=0.3: b.md::details score = %v, want 0.3", s)
	}

	// Test with decay 0.7.
	expanded07 := ExpandOneHop(idx, initial, 0.7)
	score07 := make(map[string]float64)
	for _, sr := range expanded07 {
		score07[sr.FilePath+"::"+sr.Section.ID] = sr.Score
	}
	if s := score07["b.md::details"]; s != 0.7 {
		t.Errorf("decay=0.7: b.md::details score = %v, want 0.7", s)
	}
}
