package index

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// buildFilterFixture lays out:
//
//	root/
//	  a.md            (mtime: now)
//	  docs/
//	    guide.md      (mtime: 10 days ago)
//	    deep.md       (mtime: now)
//	  src/
//	    main.go       (mtime: now)
func buildFilterFixture(t *testing.T) (string, *FolderMap) {
	t.Helper()
	root := t.TempDir()

	writeFileTH(t, filepath.Join(root, "a.md"), "root file")
	writeFileTH(t, filepath.Join(root, "docs", "guide.md"), "guide")
	writeFileTH(t, filepath.Join(root, "docs", "deep.md"), "deep")
	writeFileTH(t, filepath.Join(root, "src", "main.go"), "package main")

	writeIndexHelper(t, root, "root", []string{"a.md"})
	writeIndexHelper(t, filepath.Join(root, "docs"), "docs", []string{"guide.md", "deep.md"})
	writeIndexHelper(t, filepath.Join(root, "src"), "src", []string{"main.go"})

	old := time.Now().Add(-10 * 24 * time.Hour)
	if err := os.Chtimes(filepath.Join(root, "docs", "guide.md"), old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	fm, err := LoadTree(context.Background(), root, "", DefaultLoadOptions())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return root, fm
}

func countFiles(fm *FolderMap) int {
	n := 0
	for _, f := range fm.Folders {
		n += len(f.Files)
	}
	return n
}

func TestFilter_Empty_NoOp(t *testing.T) {
	_, fm := buildFilterFixture(t)
	out := ApplyFilters(fm, Filters{})
	if len(out.Folders) != len(fm.Folders) {
		t.Fatalf("Apply with zero filter changed folder count")
	}
	if countFiles(out) != countFiles(fm) {
		t.Fatalf("Apply with zero filter changed file count")
	}
}

func TestFilter_PathGlob(t *testing.T) {
	_, fm := buildFilterFixture(t)
	out := ApplyFilters(fm, Filters{PathGlob: "docs/**"})
	for _, f := range out.Folders {
		for _, fl := range f.Files {
			if f.Path == "docs" {
				continue
			}
			if f.Path == "." {
				if len(f.Files) != 0 {
					t.Fatalf("root folder should have no files after docs/** filter, got %v", f.Files)
				}
				continue
			}
			t.Fatalf("unexpected folder %q with files %v", f.Path, fl)
		}
	}
	// Must include parent chain (root is preserved).
	hasRoot := false
	hasDocs := false
	for _, f := range out.Folders {
		if f.Path == "." {
			hasRoot = true
		}
		if f.Path == "docs" {
			hasDocs = true
		}
	}
	if !hasDocs {
		t.Fatalf("expected docs folder to survive")
	}
	if !hasRoot {
		t.Fatalf("expected root folder to be preserved as parent chain")
	}
}

func TestFilter_Since(t *testing.T) {
	_, fm := buildFilterFixture(t)
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	out := ApplyFilters(fm, Filters{Since: cutoff})
	// guide.md was 10 days old → must be filtered out.
	for _, f := range out.Folders {
		for _, fl := range f.Files {
			if fl.Name == "guide.md" {
				t.Fatalf("expected guide.md to be filtered by --since")
			}
		}
	}
	// deep.md (recent) should still be present.
	found := false
	for _, f := range out.Folders {
		for _, fl := range f.Files {
			if fl.Name == "deep.md" {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("expected deep.md to survive --since")
	}
}

func TestFilter_FileList(t *testing.T) {
	_, fm := buildFilterFixture(t)
	out := ApplyFilters(fm, Filters{Files: []string{"docs/guide.md", "main.go"}})

	got := map[string]bool{}
	for _, f := range out.Folders {
		for _, fl := range f.Files {
			got[fl.Path] = true
		}
	}
	if !got["docs/guide.md"] {
		t.Fatalf("expected docs/guide.md")
	}
	if !got["src/main.go"] {
		t.Fatalf("expected src/main.go (matched by basename)")
	}
	if got["a.md"] {
		t.Fatalf("did not expect a.md")
	}
	// Parent chain present: root must appear.
	hasRoot := false
	for _, f := range out.Folders {
		if f.Path == "." {
			hasRoot = true
		}
	}
	if !hasRoot {
		t.Fatalf("expected root in parent chain")
	}
}

func TestFilter_Combined_PathAndSince(t *testing.T) {
	_, fm := buildFilterFixture(t)
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	out := ApplyFilters(fm, Filters{PathGlob: "docs/**", Since: cutoff})

	count := countFiles(out)
	if count != 1 {
		t.Fatalf("want exactly deep.md (1 file), got %d", count)
	}
	for _, f := range out.Folders {
		for _, fl := range f.Files {
			if fl.Name != "deep.md" {
				t.Fatalf("unexpected surviving file %q", fl.Name)
			}
		}
	}
}

func TestFilter_Combined_AllThree(t *testing.T) {
	_, fm := buildFilterFixture(t)
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	out := ApplyFilters(fm, Filters{
		PathGlob: "docs/**",
		Since:    cutoff,
		Files:    []string{"docs/deep.md"},
	})
	if countFiles(out) != 1 {
		t.Fatalf("expected 1 file, got %d", countFiles(out))
	}
}

func TestFilter_EmptyResultIsClean(t *testing.T) {
	_, fm := buildFilterFixture(t)
	out := ApplyFilters(fm, Filters{Files: []string{"nope.md"}})
	if len(out.Folders) != 0 {
		t.Fatalf("expected empty folders, got %d", len(out.Folders))
	}
}

func TestFilter_IncludeIgnoredAffectsFileFilter(t *testing.T) {
	_, fm := buildFilterFixture(t)
	// Mark deep.md as ignored.
	for fi := range fm.Folders {
		for fj := range fm.Folders[fi].Files {
			if fm.Folders[fi].Files[fj].Name == "deep.md" {
				fm.Folders[fi].Files[fj].Ignore = true
			}
		}
	}
	withDefault := ApplyFilters(fm, Filters{PathGlob: "docs/**"})
	withInclude := ApplyFilters(fm, Filters{PathGlob: "docs/**", IncludeIgnored: true})

	hasDeep := func(m *FolderMap) bool {
		for _, f := range m.Folders {
			for _, fl := range f.Files {
				if fl.Name == "deep.md" {
					return true
				}
			}
		}
		return false
	}

	if hasDeep(withDefault) {
		t.Fatalf("expected deep.md filtered by default")
	}
	if !hasDeep(withInclude) {
		t.Fatalf("expected deep.md present with IncludeIgnored")
	}
}
