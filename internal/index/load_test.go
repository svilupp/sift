package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeIndexHelper saves a sift.toml in folder with given purpose and
// list of file entries. The on-disk files must already exist; we hash
// them so the resulting index is fresh.
func writeIndexHelper(tb testing.TB, folder, purpose string, files []string) {
	tb.Helper()
	idx := &FolderIndex{
		SchemaVersion: SchemaVersion,
		Purpose:       purpose,
		Files:         map[string]FileEntry{},
	}
	for _, name := range files {
		sig, err := SigOf(filepath.Join(folder, name))
		if err != nil {
			tb.Fatalf("sig %s: %v", name, err)
		}
		idx.Files[name] = FileEntry{
			ContentHash: sig.ContentHash,
			HeadHash:    sig.HeadHash,
			TailHash:    sig.TailHash,
			Words:       sig.Words,
			Summary:     fmt.Sprintf("summary of %s", name),
		}
	}
	idx.Refresh = RefreshStats{FileCount: len(files), WordCount: 100 * len(files)}
	if err := Save(filepath.Join(folder, FilenameSiftToml), idx); err != nil {
		tb.Fatalf("save: %v", err)
	}
}

func writeFileTH(tb testing.TB, path, body string) {
	tb.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		tb.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		tb.Fatalf("write: %v", err)
	}
}

func TestLoadTree_Basic(t *testing.T) {
	root := t.TempDir()
	writeFileTH(t, filepath.Join(root, "README.md"), "hi root")
	writeFileTH(t, filepath.Join(root, "docs", "guide.md"), "guide content")
	writeIndexHelper(t, root, "Root purpose.", []string{"README.md"})
	writeIndexHelper(t, filepath.Join(root, "docs"), "Docs purpose.", []string{"guide.md"})

	fm, err := LoadTree(context.Background(), root, "", DefaultLoadOptions())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(fm.Folders) != 2 {
		t.Fatalf("want 2 folders, got %d", len(fm.Folders))
	}
	if fm.Folders[0].Path != "." {
		t.Fatalf("first folder path = %q, want .", fm.Folders[0].Path)
	}
	if fm.Folders[1].Path != "docs" {
		t.Fatalf("second folder path = %q", fm.Folders[1].Path)
	}
	if !fm.Folders[0].HasIndex {
		t.Fatalf("root should have index")
	}
	if fm.Folders[0].Purpose != "Root purpose." {
		t.Fatalf("purpose = %q", fm.Folders[0].Purpose)
	}
}

func TestLoadTree_DepthLimits(t *testing.T) {
	root := t.TempDir()
	writeFileTH(t, filepath.Join(root, "a.md"), "a")
	writeFileTH(t, filepath.Join(root, "child", "b.md"), "b")
	writeFileTH(t, filepath.Join(root, "child", "deeper", "c.md"), "c")
	writeIndexHelper(t, root, "root", []string{"a.md"})
	writeIndexHelper(t, filepath.Join(root, "child"), "child", []string{"b.md"})
	writeIndexHelper(t, filepath.Join(root, "child", "deeper"), "deeper", []string{"c.md"})

	cases := []struct {
		depth int
		want  int
	}{
		{depth: 0, want: 1},  // only root
		{depth: 1, want: 2},  // root + child
		{depth: 2, want: 3},  // root + child + deeper
		{depth: -1, want: 3}, // unbounded
	}
	for _, tc := range cases {
		t.Run(fmt.Sprintf("depth=%d", tc.depth), func(t *testing.T) {
			fm, err := LoadTree(context.Background(), root, "", LoadOptions{Depth: tc.depth})
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			if len(fm.Folders) != tc.want {
				t.Fatalf("got %d folders, want %d", len(fm.Folders), tc.want)
			}
		})
	}
}

func TestLoadTree_MissingSiftTomlGraceful(t *testing.T) {
	root := t.TempDir()
	writeFileTH(t, filepath.Join(root, "x.md"), "x")
	// no sift.toml at all

	fm, err := LoadTree(context.Background(), root, "", DefaultLoadOptions())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(fm.Folders) != 1 {
		t.Fatalf("want 1 folder, got %d", len(fm.Folders))
	}
	if fm.Folders[0].HasIndex {
		t.Fatalf("expected HasIndex=false")
	}
	if fm.Folders[0].Purpose != "" {
		t.Fatalf("expected empty purpose, got %q", fm.Folders[0].Purpose)
	}
	if len(fm.Folders[0].Files) != 1 {
		t.Fatalf("expected 1 file (orphan-on-disk), got %d", len(fm.Folders[0].Files))
	}
	if fm.Folders[0].Files[0].Name != "x.md" {
		t.Fatalf("file name = %q", fm.Folders[0].Files[0].Name)
	}
	if !fm.Folders[0].Files[0].Exists {
		t.Fatalf("on-disk file should be marked Exists=true")
	}
}

func TestLoadTree_IgnoreFlagFiltersByDefault(t *testing.T) {
	root := t.TempDir()
	writeFileTH(t, filepath.Join(root, "keep.md"), "k")
	writeFileTH(t, filepath.Join(root, "skip.md"), "s")
	writeIndexHelper(t, root, "root", []string{"keep.md", "skip.md"})
	// Mark skip.md as ignored.
	idx, err := Load(filepath.Join(root, FilenameSiftToml))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	entry := idx.Files["skip.md"]
	entry.Ignore = true
	idx.Files["skip.md"] = entry
	if err := Save(filepath.Join(root, FilenameSiftToml), idx); err != nil {
		t.Fatalf("save: %v", err)
	}

	fm, err := LoadTree(context.Background(), root, "", DefaultLoadOptions())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := len(fm.Folders[0].Files); got != 1 {
		t.Fatalf("want 1 file (skip filtered), got %d", got)
	}

	fm2, err := LoadTree(context.Background(), root, "", LoadOptions{Depth: -1, IncludeIgnored: true})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := len(fm2.Folders[0].Files); got != 2 {
		t.Fatalf("want 2 files with --include-ignored, got %d", got)
	}
}

func TestLoadTree_IgnoredFolderNotDescended(t *testing.T) {
	root := t.TempDir()
	writeFileTH(t, filepath.Join(root, "a.md"), "a")
	writeFileTH(t, filepath.Join(root, "secret", "b.md"), "b")
	writeIndexHelper(t, root, "root", []string{"a.md"})
	writeIndexHelper(t, filepath.Join(root, "secret"), "secret", []string{"b.md"})

	// Mark secret as ignored on disk.
	idx, _ := Load(filepath.Join(root, "secret", FilenameSiftToml))
	idx.Ignore = true
	if err := Save(filepath.Join(root, "secret", FilenameSiftToml), idx); err != nil {
		t.Fatalf("save: %v", err)
	}

	fm, err := LoadTree(context.Background(), root, "", DefaultLoadOptions())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, f := range fm.Folders {
		if f.Path == "secret" && len(f.Files) > 0 {
			t.Fatalf("expected ignored folder to have no files, got %+v", f.Files)
		}
	}
}

func TestLoadTree_HonorsSiftIgnore(t *testing.T) {
	root := t.TempDir()
	writeFileTH(t, filepath.Join(root, "keep.md"), "k")
	writeFileTH(t, filepath.Join(root, "node_modules", "junk.md"), "j")
	writeFileTH(t, filepath.Join(root, ".siftignore"), "node_modules/\n")
	writeIndexHelper(t, root, "root", []string{"keep.md"})

	fm, err := LoadTree(context.Background(), root, "", DefaultLoadOptions())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	for _, f := range fm.Folders {
		if f.Path == "node_modules" {
			t.Fatalf("node_modules should be filtered by .siftignore")
		}
	}
}

func TestLoadTree_BadRoot(t *testing.T) {
	_, err := LoadTree(context.Background(), "/this/does/not/exist/zzz", "", DefaultLoadOptions())
	if err == nil {
		t.Fatalf("expected error for bad root")
	}
}

func TestKindFromName(t *testing.T) {
	cases := map[string]string{
		"README.md":   "md",
		"main.go":     "go",
		"a.py":        "py",
		"x":           "",
		"foo.unknown": "unknown",
		"a.MD":        "md",
	}
	for name, want := range cases {
		if got := kindFromName(name); got != want {
			t.Errorf("kindFromName(%q) = %q, want %q", name, got, want)
		}
	}
}

// BenchmarkLoadTree_100Folders synthesizes a 100-folder tree (10 top-level
// folders × ~10 sub-folders each) with one sift.toml per folder and 6
// real files, then times a single LoadTree pass. PHASE-4 target: <100ms.
func BenchmarkLoadTree_100Folders(b *testing.B) {
	root := b.TempDir()
	mkTree100(b, root)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		fm, err := LoadTree(context.Background(), root, "", DefaultLoadOptions())
		if err != nil {
			b.Fatalf("load: %v", err)
		}
		if len(fm.Folders) < 100 {
			b.Fatalf("want >=100 folders, got %d", len(fm.Folders))
		}
	}
}

// TestLoadTree_100FoldersUnder100ms is a Go-test-friendly smoke that
// fails the suite if a single LoadTree pass exceeds 250ms (allows for
// noisy CI). Headline target is <100ms — captured separately in the
// benchmark.
func TestLoadTree_100FoldersUnder250ms(t *testing.T) {
	if testing.Short() {
		t.Skip("short")
	}
	root := t.TempDir()
	mkTree100(t, root)

	start := time.Now()
	fm, err := LoadTree(context.Background(), root, "", DefaultLoadOptions())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	elapsed := time.Since(start)
	if len(fm.Folders) < 100 {
		t.Fatalf("want >=100 folders, got %d", len(fm.Folders))
	}
	if elapsed > 250*time.Millisecond {
		t.Fatalf("LoadTree took %v, want <250ms", elapsed)
	}
	t.Logf("100-folder LoadTree: %v (%d folders)", elapsed, len(fm.Folders))
}

// mkTree100 builds the 100-folder fixture used by both the benchmark and
// the smoke test. Total folders >= 100.
func mkTree100(tb testing.TB, root string) {
	tb.Helper()
	for i := 0; i < 10; i++ {
		top := filepath.Join(root, fmt.Sprintf("top%02d", i))
		var topFiles []string
		for k := 0; k < 6; k++ {
			name := fmt.Sprintf("file_%d.md", k)
			writeFileTH(tb, filepath.Join(top, name), fmt.Sprintf("content %d-%d for benchmarking", i, k))
			topFiles = append(topFiles, name)
		}
		writeIndexHelper(tb, top, fmt.Sprintf("Top folder %d.", i), topFiles)
		for j := 0; j < 10; j++ {
			sub := filepath.Join(top, fmt.Sprintf("sub%02d", j))
			var files []string
			for k := 0; k < 6; k++ {
				name := fmt.Sprintf("file_%d.md", k)
				writeFileTH(tb, filepath.Join(sub, name), fmt.Sprintf("content %d-%d-%d for benchmarking", i, j, k))
				files = append(files, name)
			}
			writeIndexHelper(tb, sub, fmt.Sprintf("Sub %d-%d.", i, j), files)
		}
	}
}
