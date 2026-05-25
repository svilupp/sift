package search

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// helper: create a temp collection root with an optional sift.toml at
// each provided folder path (relative). The returned root is absolute
// and cleaned. Sub-folders are created on demand. Each (folder ->
// content) pair writes that content to <folder>/sift.toml.
func newTestCollection(t *testing.T, files map[string]string, indexes map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write file %s: %v", rel, err)
		}
	}
	for rel, content := range indexes {
		full := filepath.Join(root, rel, "sift.toml")
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("mkdir for index %s: %v", rel, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("write index %s: %v", rel, err)
		}
	}
	return root
}

func TestDecorate_AttachesFolderAndFileMetadata(t *testing.T) {
	root := newTestCollection(t,
		map[string]string{
			"docs/alpha.md": "# Alpha\n\nSome content.\n",
		},
		map[string]string{
			"docs": `purpose = "Documentation root for the alpha module."
[files."alpha.md"]
summary = "Intro to alpha."
words = 42
`,
		},
	)

	results := []Result{
		{ChunkID: 1, FilePath: filepath.Join(root, "docs", "alpha.md")},
	}

	stats := &DecorateStats{}
	out := Decorate(results, DecorateOptions{
		CollectionRoot: root,
		Stats:          stats,
	})

	if out[0].IndexAnnotation == nil {
		t.Fatalf("expected annotation, got nil")
	}
	ann := out[0].IndexAnnotation
	if ann.FolderPurpose != "Documentation root for the alpha module." {
		t.Errorf("FolderPurpose = %q", ann.FolderPurpose)
	}
	if ann.FileSummary != "Intro to alpha." {
		t.Errorf("FileSummary = %q", ann.FileSummary)
	}
	if ann.FileWords != 42 {
		t.Errorf("FileWords = %d", ann.FileWords)
	}
	if ann.FolderPath != "docs" {
		t.Errorf("FolderPath = %q", ann.FolderPath)
	}
	if stats.Decorated != 1 {
		t.Errorf("expected Decorated=1, got %d", stats.Decorated)
	}
}

func TestDecorate_MissingSiftToml_NoError_NoAnnotation(t *testing.T) {
	root := newTestCollection(t,
		map[string]string{
			"docs/lonely.md": "no sift.toml in this tree\n",
		},
		nil,
	)

	results := []Result{
		{ChunkID: 1, FilePath: filepath.Join(root, "docs", "lonely.md")},
	}
	stats := &DecorateStats{}
	out := Decorate(results, DecorateOptions{
		CollectionRoot: root,
		Stats:          stats,
	})
	if out[0].IndexAnnotation != nil {
		t.Errorf("expected no annotation, got %+v", out[0].IndexAnnotation)
	}
	if stats.Decorated != 0 {
		t.Errorf("expected Decorated=0, got %d", stats.Decorated)
	}
	if stats.NotFound == 0 {
		t.Errorf("expected NotFound > 0, got %d", stats.NotFound)
	}
}

func TestDecorate_WalksUpToParentSiftToml(t *testing.T) {
	// sift.toml at root, file two folders deep.
	root := newTestCollection(t,
		map[string]string{
			"a/b/deep.md": "deep file\n",
		},
		map[string]string{
			".": `purpose = "Top-level collection notes."
`,
		},
	)
	results := []Result{
		{FilePath: filepath.Join(root, "a", "b", "deep.md")},
	}
	out := Decorate(results, DecorateOptions{CollectionRoot: root})
	if out[0].IndexAnnotation == nil {
		t.Fatalf("expected annotation walking up, got nil")
	}
	if out[0].IndexAnnotation.FolderPurpose != "Top-level collection notes." {
		t.Errorf("FolderPurpose = %q", out[0].IndexAnnotation.FolderPurpose)
	}
}

func TestDecorate_StopsAtCollectionRoot(t *testing.T) {
	// Create an outer dir with a sift.toml the walker MUST NOT touch.
	outer := t.TempDir()
	if err := os.WriteFile(filepath.Join(outer, "sift.toml"),
		[]byte("purpose = \"OUTSIDE COLLECTION — must not appear.\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	collection := filepath.Join(outer, "inside")
	if err := os.MkdirAll(filepath.Join(collection, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(collection, "docs", "f.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// No sift.toml inside the collection.

	results := []Result{
		{FilePath: filepath.Join(collection, "docs", "f.md")},
	}
	out := Decorate(results, DecorateOptions{CollectionRoot: collection})
	if out[0].IndexAnnotation != nil {
		t.Fatalf("walker crossed collection root, got %+v", out[0].IndexAnnotation)
	}
}

func TestDecorate_LRUCacheHitsForSiblings(t *testing.T) {
	root := newTestCollection(t,
		map[string]string{
			"docs/a.md": "a",
			"docs/b.md": "b",
			"docs/c.md": "c",
		},
		map[string]string{
			"docs": `purpose = "Docs."
[files."a.md"]
summary = "A."
[files."b.md"]
summary = "B."
[files."c.md"]
summary = "C."
`,
		},
	)
	results := []Result{
		{FilePath: filepath.Join(root, "docs", "a.md")},
		{FilePath: filepath.Join(root, "docs", "b.md")},
		{FilePath: filepath.Join(root, "docs", "c.md")},
	}
	stats := &DecorateStats{}
	out := Decorate(results, DecorateOptions{
		CollectionRoot: root,
		Stats:          stats,
	})
	for i, r := range out {
		if r.IndexAnnotation == nil || r.IndexAnnotation.FileSummary == "" {
			t.Fatalf("result %d missing annotation", i)
		}
	}
	// 3 sibling results + walk up from "docs" to root for each (1 per
	// folder, root walk hits "docs" first, then root). The "docs"
	// folder is loaded once; subsequent sibling lookups must be cache
	// hits. We expect at least 3 cache hits across the three results
	// (one per sibling for the "docs" folder, plus extra for the root
	// walk).
	if stats.CacheHits < 2 {
		t.Errorf("expected cache hits to instrument sibling reuse, got hits=%d misses=%d",
			stats.CacheHits, stats.CacheMisses)
	}
	if stats.CacheMisses == 0 {
		t.Errorf("expected at least one miss (the first load), got %d", stats.CacheMisses)
	}
}

func TestDecorate_CachePerCallIsolation(t *testing.T) {
	// Two concurrent Decorate calls must not share cache state. We
	// detect leakage by giving each its own DecorateStats and checking
	// that both report the FIRST load as a miss (i.e. no cross-call
	// hit pollution).
	root := newTestCollection(t,
		map[string]string{
			"x/file.md": "x",
		},
		map[string]string{
			"x": `purpose = "X folder."`,
		},
	)
	results := []Result{
		{FilePath: filepath.Join(root, "x", "file.md")},
	}

	var wg sync.WaitGroup
	stats := make([]*DecorateStats, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		st := &DecorateStats{}
		stats[i] = st
		go func() {
			defer wg.Done()
			cp := make([]Result, len(results))
			copy(cp, results)
			Decorate(cp, DecorateOptions{
				CollectionRoot: root,
				Stats:          st,
			})
		}()
	}
	wg.Wait()
	for i, st := range stats {
		if st.CacheMisses == 0 {
			t.Errorf("call %d had zero misses; cache leaked across calls (hits=%d)",
				i, st.CacheHits)
		}
	}
}

func TestDecorate_ParseErrorIsNonFatal(t *testing.T) {
	root := newTestCollection(t,
		map[string]string{
			"bad/file.md": "x",
		},
		map[string]string{
			"bad": "not = valid = toml = ===\n",
		},
	)
	results := []Result{
		{FilePath: filepath.Join(root, "bad", "file.md")},
	}
	stats := &DecorateStats{}
	out := Decorate(results, DecorateOptions{
		CollectionRoot: root,
		Stats:          stats,
	})
	if out[0].IndexAnnotation != nil {
		t.Errorf("expected no annotation on parse error, got %+v", out[0].IndexAnnotation)
	}
	if stats.ParseErrors == 0 {
		t.Errorf("expected ParseErrors > 0")
	}
}

func TestDecorate_EmptyResults_NoOp(t *testing.T) {
	out := Decorate(nil, DecorateOptions{CollectionRoot: "/tmp"})
	if len(out) != 0 {
		t.Errorf("expected empty, got %d", len(out))
	}
}

func TestDecorate_EmptyCollectionRoot_ReturnsUnchanged(t *testing.T) {
	in := []Result{{FilePath: "/somewhere/x.md"}}
	out := Decorate(in, DecorateOptions{CollectionRoot: ""})
	if len(out) != 1 || out[0].IndexAnnotation != nil {
		t.Errorf("expected unchanged, got %+v", out)
	}
}

func TestDecorate_FileOutsideCollectionRoot_NoAnnotation(t *testing.T) {
	root := newTestCollection(t,
		map[string]string{},
		map[string]string{
			".": `purpose = "Root."`,
		},
	)
	other := t.TempDir()
	if err := os.WriteFile(filepath.Join(other, "x.md"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	results := []Result{{FilePath: filepath.Join(other, "x.md")}}
	out := Decorate(results, DecorateOptions{CollectionRoot: root})
	if out[0].IndexAnnotation != nil {
		t.Errorf("expected no annotation for file outside root, got %+v", out[0].IndexAnnotation)
	}
}

func TestFolderIndexCache_FIFOEviction(t *testing.T) {
	c := newFolderIndexCache(2)
	c.put("a", folderIndexEntry{})
	c.put("b", folderIndexEntry{})
	c.put("c", folderIndexEntry{}) // evicts "a"

	if _, ok := c.get("a"); ok {
		t.Errorf("expected 'a' evicted")
	}
	if _, ok := c.get("b"); !ok {
		t.Errorf("expected 'b' present")
	}
	if _, ok := c.get("c"); !ok {
		t.Errorf("expected 'c' present")
	}
}

func TestIndexAnnotation_IsZero(t *testing.T) {
	if !(IndexAnnotation{}).IsZero() {
		t.Error("default should be zero")
	}
	if (IndexAnnotation{FolderPurpose: "x"}).IsZero() {
		t.Error("non-empty Purpose should not be zero")
	}
	if (IndexAnnotation{FolderUseWhen: []string{"x"}}).IsZero() {
		t.Error("non-empty UseWhen should not be zero")
	}
}
