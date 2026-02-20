package index

import (
	"os"
	"path/filepath"
	"testing"
)

const testAnalyzer = "standard"

// testDocs contains documents with distinct content for predictable BM25 scoring.
var testDocs = []struct {
	id      string
	content string
	path    string
}{
	{"chunk-a", "The quick brown fox jumps over the lazy dog", "/docs/animals.md"},
	{"chunk-b", "Go programming language is fast and efficient", "/docs/golang.md"},
	{"chunk-c", "Authentication flow uses JWT tokens for security", "/docs/auth-flow.md"},
	{"chunk-d", "Database queries should be optimized for performance", "/docs/database.md"},
	{"chunk-e", "The authentication system validates user credentials", "/docs/auth-system.md"},
}

// openTestIndex is a helper that creates a new BleveIndex in a temp directory.
func openTestIndex(t *testing.T) *BleveIndex {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.bleve")
	idx, err := OpenBleve(path, testAnalyzer)
	if err != nil {
		t.Fatalf("OpenBleve: %v", err)
	}
	t.Cleanup(func() {
		idx.Close()
	})
	return idx
}

// indexTestDocs indexes all testDocs into the given BleveIndex.
func indexTestDocs(t *testing.T, idx *BleveIndex) {
	t.Helper()
	for _, doc := range testDocs {
		if err := idx.Index(doc.id, doc.content, doc.path); err != nil {
			t.Fatalf("Index(%q): %v", doc.id, err)
		}
	}
}

func TestOpenCreateNew(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "new.bleve")

	idx, err := OpenBleve(path, testAnalyzer)
	if err != nil {
		t.Fatalf("OpenBleve: %v", err)
	}
	defer idx.Close()

	// The index directory should exist on disk.
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat index path: %v", err)
	}
	if !info.IsDir() {
		t.Fatal("expected index path to be a directory")
	}

	// A fresh index should have zero documents.
	count, err := idx.DocCount()
	if err != nil {
		t.Fatalf("DocCount: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected 0 docs in new index, got %d", count)
	}
}

func TestOpenExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "existing.bleve")

	// Create and populate an index, then close it.
	idx, err := OpenBleve(path, testAnalyzer)
	if err != nil {
		t.Fatalf("OpenBleve (create): %v", err)
	}
	if err := idx.Index("doc-1", "hello world", "/hello.md"); err != nil {
		t.Fatalf("Index: %v", err)
	}
	if err := idx.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Re-open the same path.
	idx2, err := OpenBleve(path, testAnalyzer)
	if err != nil {
		t.Fatalf("OpenBleve (reopen): %v", err)
	}
	defer idx2.Close()

	count, err := idx2.DocCount()
	if err != nil {
		t.Fatalf("DocCount: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected 1 doc after reopen, got %d", count)
	}
}

func TestIndexAndSearch(t *testing.T) {
	idx := openTestIndex(t)
	indexTestDocs(t, idx)

	results, err := idx.Search("authentication", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results for 'authentication', got %d", len(results))
	}

	// Collect the IDs returned.
	got := make(map[string]bool)
	for _, r := range results {
		got[r.ChunkID] = true
	}

	// chunk-c and chunk-e both mention "authentication".
	for _, want := range []string{"chunk-c", "chunk-e"} {
		if !got[want] {
			t.Errorf("expected %q in results, got IDs: %v", want, got)
		}
	}

	// Verify the path field is populated on at least the first result.
	if results[0].Path == "" {
		t.Error("expected non-empty Path on first result")
	}
}

func TestSearchScoreOrdering(t *testing.T) {
	idx := openTestIndex(t)
	indexTestDocs(t, idx)

	results, err := idx.Search("authentication", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(results) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(results))
	}

	// Verify scores are in descending order.
	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Errorf("results not sorted by score descending: result[%d].Score=%f > result[%d].Score=%f",
				i, results[i].Score, i-1, results[i-1].Score)
		}
	}
}

func TestDelete(t *testing.T) {
	idx := openTestIndex(t)

	// Index a single document.
	if err := idx.Index("del-1", "authentication tokens for login", "/del.md"); err != nil {
		t.Fatalf("Index: %v", err)
	}

	// Confirm it's searchable.
	results, err := idx.Search("authentication", 10)
	if err != nil {
		t.Fatalf("Search before delete: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result before delete, got %d", len(results))
	}

	// Delete and verify it's gone.
	if err := idx.Delete("del-1"); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	results, err = idx.Search("authentication", 10)
	if err != nil {
		t.Fatalf("Search after delete: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("expected 0 results after delete, got %d", len(results))
	}

	count, err := idx.DocCount()
	if err != nil {
		t.Fatalf("DocCount: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected DocCount 0 after delete, got %d", count)
	}
}

func TestDocCount(t *testing.T) {
	tests := []struct {
		name  string
		count int
	}{
		{"zero docs", 0},
		{"one doc", 1},
		{"all five docs", 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := openTestIndex(t)

			for i := 0; i < tt.count; i++ {
				doc := testDocs[i]
				if err := idx.Index(doc.id, doc.content, doc.path); err != nil {
					t.Fatalf("Index(%q): %v", doc.id, err)
				}
			}

			got, err := idx.DocCount()
			if err != nil {
				t.Fatalf("DocCount: %v", err)
			}
			if got != uint64(tt.count) {
				t.Fatalf("DocCount = %d, want %d", got, tt.count)
			}
		})
	}
}

func TestDestroyRemovesFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "destroy.bleve")

	idx, err := OpenBleve(path, testAnalyzer)
	if err != nil {
		t.Fatalf("OpenBleve: %v", err)
	}

	// Index a document so the directory has content.
	if err := idx.Index("doc-1", "some content", "/some.md"); err != nil {
		t.Fatalf("Index: %v", err)
	}

	// Verify the directory exists before destroy.
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("index path should exist before Destroy: %v", err)
	}

	// Destroy closes the index and removes the directory.
	if err := idx.Destroy(); err != nil {
		t.Fatalf("Destroy: %v", err)
	}

	// The index directory should no longer exist.
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected index path to be removed after Destroy, got err: %v", err)
	}
}
