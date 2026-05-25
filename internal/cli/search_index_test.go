package cli

import (
	"errors"
	"strings"
	"testing"

	"sift/internal/db"
	"sift/internal/search"
)

// fakeCollectionLookup is a tiny stub for collectionLookup used by
// decorateResultsWithIndex tests so we don't have to spin up a real DB.
type fakeCollectionLookup struct {
	byName map[string]*db.Collection
	byID   map[int64]*db.Collection
}

func (f *fakeCollectionLookup) GetCollection(name string) (*db.Collection, error) {
	if c, ok := f.byName[name]; ok {
		return c, nil
	}
	return nil, nil
}

func (f *fakeCollectionLookup) GetCollectionByID(id int64) (*db.Collection, error) {
	if c, ok := f.byID[id]; ok {
		return c, nil
	}
	return nil, nil
}

func TestFormatIndexSuffix_PrefersFolderPurpose(t *testing.T) {
	colorMode = 0 // disable ANSI for stable string match
	defer func() { colorMode = -1 }()

	ann := &search.IndexAnnotation{
		FolderPurpose: "Top-level docs root.",
		FileSummary:   "Should not appear when purpose is set.",
	}
	got := formatIndexSuffix(ann, true)
	if !strings.HasPrefix(got, "[folder:") {
		t.Errorf("expected [folder: ...], got %q", got)
	}
	if !strings.Contains(got, "Top-level docs root.") {
		t.Errorf("expected purpose text, got %q", got)
	}
	if strings.Contains(got, "Should not appear") {
		t.Errorf("file summary leaked into folder suffix: %q", got)
	}
}

func TestFormatIndexSuffix_FallsBackToFileSummary(t *testing.T) {
	colorMode = 0
	defer func() { colorMode = -1 }()

	ann := &search.IndexAnnotation{
		FileSummary: "One-line summary.",
	}
	got := formatIndexSuffix(ann, true)
	if !strings.HasPrefix(got, "[file:") {
		t.Errorf("expected [file: ...], got %q", got)
	}
}

func TestFormatIndexSuffix_EmptyReturnsEmpty(t *testing.T) {
	if got := formatIndexSuffix(nil, true); got != "" {
		t.Errorf("nil annotation should yield empty, got %q", got)
	}
	if got := formatIndexSuffix(&search.IndexAnnotation{}, true); got != "" {
		t.Errorf("empty annotation should yield empty, got %q", got)
	}
}

func TestFormatIndexSuffix_TruncatesLongPurpose(t *testing.T) {
	colorMode = 0
	defer func() { colorMode = -1 }()

	long := strings.Repeat("a", 200)
	ann := &search.IndexAnnotation{FolderPurpose: long}
	got := formatIndexSuffix(ann, true)
	if !strings.Contains(got, "...") {
		t.Errorf("expected truncation ellipsis, got %q", got)
	}
}

func TestFormatIndexSuffix_FirstSentenceOnly(t *testing.T) {
	colorMode = 0
	defer func() { colorMode = -1 }()

	ann := &search.IndexAnnotation{FolderPurpose: "First sentence. Second sentence should not appear."}
	got := formatIndexSuffix(ann, true)
	if !strings.Contains(got, "First sentence.") {
		t.Errorf("expected first sentence in %q", got)
	}
	if strings.Contains(got, "Second sentence") {
		t.Errorf("second sentence leaked: %q", got)
	}
}

func TestFormatIndexSuffix_StopsAtFirstNewline(t *testing.T) {
	colorMode = 0
	defer func() { colorMode = -1 }()

	ann := &search.IndexAnnotation{FolderPurpose: "Line one\nLine two"}
	got := formatIndexSuffix(ann, true)
	if strings.Contains(got, "Line two") {
		t.Errorf("multi-line leaked: %q", got)
	}
}

func TestDecorateResultsWithIndex_NilDB_NoOp(t *testing.T) {
	r := &search.SearchResult{Results: []search.Result{{FilePath: "/x"}}}
	decorateResultsWithIndex(r, "anything", nil)
	if r.Results[0].IndexAnnotation != nil {
		t.Errorf("nil DB should leave results alone")
	}
}

func TestDecorateResultsWithIndex_EmptyResult_NoOp(t *testing.T) {
	r := &search.SearchResult{}
	decorateResultsWithIndex(r, "x", &fakeCollectionLookup{})
	if r != nil && len(r.Results) != 0 {
		t.Errorf("expected no results to remain empty")
	}
}

func TestDecorateResultsWithIndex_UnknownCollection_NoOp(t *testing.T) {
	r := &search.SearchResult{Results: []search.Result{{FilePath: "/tmp/x.md"}}}
	stub := &fakeCollectionLookup{byName: map[string]*db.Collection{}}
	decorateResultsWithIndex(r, "missing", stub)
	if r.Results[0].IndexAnnotation != nil {
		t.Errorf("unknown collection should leave results alone")
	}
}

// compile-time guard: *db.DB must satisfy the collectionLookup contract
// the search command uses for decoration.
var _ collectionLookup = (*db.DB)(nil)

// ensure errors package is used so imports compile when the test file
// stays minimal (defensive).
var _ = errors.New
