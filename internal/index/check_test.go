package index

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is a small helper that creates parent directories as needed.
func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// seedSiftToml writes a sift.toml at folder, hashing the named files
// against their on-disk content so we get a "fresh" baseline.
func seedSiftToml(t *testing.T, folder string, fileNames []string, summary string) {
	t.Helper()
	idx := &FolderIndex{
		SchemaVersion: SchemaVersion,
		Files:         map[string]FileEntry{},
		Folders:       map[string]ChildFolder{},
	}
	for _, name := range fileNames {
		sig, err := SigOf(filepath.Join(folder, name))
		if err != nil {
			t.Fatalf("sig %s: %v", name, err)
		}
		idx.Files[name] = FileEntry{
			ContentHash: sig.ContentHash,
			HeadHash:    sig.HeadHash,
			TailHash:    sig.TailHash,
			Words:       sig.Words,
			Summary:     summary,
		}
	}
	if err := Save(filepath.Join(folder, FilenameSiftToml), idx); err != nil {
		t.Fatalf("save sift.toml: %v", err)
	}
}

// findDefect returns the first defect with the given kind and path; nil
// if none. Path "" matches any path.
func findDefect(rep *CheckReport, kind, path string) *Defect {
	for i := range rep.Defects {
		d := &rep.Defects[i]
		if d.Kind != kind {
			continue
		}
		if path == "" || d.Path == path {
			return d
		}
	}
	return nil
}

func TestCheck_CleanTree(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "alpha.md"), "Alpha content here for testing freshness baseline.")
	writeFile(t, filepath.Join(root, "beta.md"), "Beta is also a tracked file with stable content.")
	seedSiftToml(t, root, []string{"alpha.md", "beta.md"}, "Stable summary.")

	rep, err := Check(context.Background(), root, CheckOptions{RequireSummary: true})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !rep.Clean() {
		t.Fatalf("expected clean report, got: %+v", rep.Summary)
	}
	if len(rep.Defects) != 0 {
		t.Fatalf("expected zero defects, got %d", len(rep.Defects))
	}
}

func TestCheck_MissingSummaryAllowedByDefault(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "x.md"), "local index content")
	seedSiftToml(t, root, []string{"x.md"}, "")

	rep, err := Check(context.Background(), root, CheckOptions{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rep.Summary.MissingSummary != 0 {
		t.Fatalf("default local check reported missing summary: %+v", rep.Defects)
	}
}

func TestCheck_MissingSiftToml(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "lonely.md"), "no metadata here")

	rep, err := Check(context.Background(), root, CheckOptions{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rep.Summary.Missing != 1 {
		t.Fatalf("Missing = %d, want 1", rep.Summary.Missing)
	}
	if d := findDefect(rep, DefectMissingSiftToml, FilenameSiftToml); d == nil {
		t.Fatalf("expected missing_sift_toml defect, got %+v", rep.Defects)
	}
}

func TestCheck_OrphanedEntry(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "real.md"), "real content")
	// seed with a phantom entry that does not exist on disk
	idx := &FolderIndex{
		SchemaVersion: SchemaVersion,
		Files: map[string]FileEntry{
			"real.md": {ContentHash: "0", HeadHash: "0", TailHash: "0", Words: 1, Summary: "x"},
			"phantom.md": {
				ContentHash: "deadbeef",
				HeadHash:    "deadbeef",
				TailHash:    "deadbeef",
				Words:       100,
				Summary:     "phantom",
			},
		},
	}
	// fix the real.md entry to match disk so it doesn't show stale
	sig, _ := SigOf(filepath.Join(root, "real.md"))
	idx.Files["real.md"] = FileEntry{
		ContentHash: sig.ContentHash, HeadHash: sig.HeadHash, TailHash: sig.TailHash,
		Words: sig.Words, Summary: "ok",
	}
	if err := Save(filepath.Join(root, FilenameSiftToml), idx); err != nil {
		t.Fatalf("save: %v", err)
	}

	rep, err := Check(context.Background(), root, CheckOptions{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rep.Summary.Orphaned != 1 {
		t.Fatalf("Orphaned = %d, want 1: %+v", rep.Summary.Orphaned, rep.Defects)
	}
	if d := findDefect(rep, DefectOrphaned, "phantom.md"); d == nil {
		t.Fatalf("expected phantom orphaned defect")
	}
}

func TestCheck_OrphanedFileOnDisk(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "tracked.md"), "tracked content here")
	seedSiftToml(t, root, []string{"tracked.md"}, "ok")
	// now drop a new untracked file
	writeFile(t, filepath.Join(root, "new.md"), "untracked")

	rep, err := Check(context.Background(), root, CheckOptions{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d := findDefect(rep, DefectOrphaned, "new.md"); d == nil {
		t.Fatalf("expected new.md orphaned defect, got %+v", rep.Defects)
	}
}

func TestCheck_StaleEntry(t *testing.T) {
	root := t.TempDir()
	body := "first version of the file with several words in head and tail to give signatures something to bite into. " +
		strings.Repeat("middle ", 20) + " end."
	writeFile(t, filepath.Join(root, "drift.md"), body)
	seedSiftToml(t, root, []string{"drift.md"}, "ok")

	// Mutate content significantly so head/tail hashes change.
	mutated := "completely different opening words now starting fresh with a new theme. " +
		strings.Repeat("body ", 40) + " brand-new ending phrase."
	writeFile(t, filepath.Join(root, "drift.md"), mutated)

	rep, err := Check(context.Background(), root, CheckOptions{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rep.Summary.Stale != 1 {
		t.Fatalf("Stale = %d, want 1: %+v", rep.Summary.Stale, rep.Defects)
	}
	if d := findDefect(rep, DefectStale, "drift.md"); d == nil {
		t.Fatalf("expected drift.md stale defect")
	}
}

func TestCheck_MissingSummary(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "x.md"), "some words for hashing baseline content here")
	// seed without summary
	sig, _ := SigOf(filepath.Join(root, "x.md"))
	idx := &FolderIndex{
		SchemaVersion: SchemaVersion,
		Files: map[string]FileEntry{
			"x.md": {ContentHash: sig.ContentHash, HeadHash: sig.HeadHash, TailHash: sig.TailHash, Words: sig.Words},
		},
	}
	if err := Save(filepath.Join(root, FilenameSiftToml), idx); err != nil {
		t.Fatalf("save: %v", err)
	}

	rep, err := Check(context.Background(), root, CheckOptions{RequireSummary: true})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rep.Summary.MissingSummary != 1 {
		t.Fatalf("MissingSummary = %d, want 1: %+v", rep.Summary.MissingSummary, rep.Defects)
	}
}

func TestCheck_ParseError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, FilenameSiftToml), "not = = valid toml [[[")
	writeFile(t, filepath.Join(root, "f.md"), "content")

	rep, err := Check(context.Background(), root, CheckOptions{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rep.Summary.ParseError != 1 {
		t.Fatalf("ParseError = %d, want 1: %+v", rep.Summary.ParseError, rep.Defects)
	}
}

func TestCheck_SchemaVersionTooHigh(t *testing.T) {
	// Parser rejects > SchemaVersion → it surfaces as parse_error, since
	// Load returns an error. That's the expected, observable behavior.
	root := t.TempDir()
	writeFile(t, filepath.Join(root, FilenameSiftToml), "schema_version = 99\n")
	writeFile(t, filepath.Join(root, "f.md"), "content")

	rep, err := Check(context.Background(), root, CheckOptions{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rep.Summary.ParseError != 1 {
		t.Fatalf("ParseError = %d, want 1: %+v", rep.Summary.ParseError, rep.Defects)
	}
}

func TestCheck_RespectsSiftIgnore(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, ".siftignore"), "skip-me/\n")
	writeFile(t, filepath.Join(root, "skip-me", "junk.md"), "ignored")
	writeFile(t, filepath.Join(root, "kept.md"), "kept content")
	seedSiftToml(t, root, []string{"kept.md"}, "ok")

	rep, err := Check(context.Background(), root, CheckOptions{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !rep.Clean() {
		t.Fatalf("expected clean (ignored folder skipped), got %+v", rep.Defects)
	}
	for _, fc := range rep.Folders {
		if strings.Contains(fc.Folder, "skip-me") {
			t.Fatalf("walker descended into ignored folder: %s", fc.Folder)
		}
	}
}

func TestCheck_PerFolderIgnoreFlag(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "private", "secret.md"), "shh")
	// child folder marks itself ignore=true
	idx := &FolderIndex{SchemaVersion: SchemaVersion, Ignore: true}
	if err := Save(filepath.Join(root, "private", FilenameSiftToml), idx); err != nil {
		t.Fatalf("save: %v", err)
	}
	// root has no other files, so no missing defect at root
	rootIdx := &FolderIndex{SchemaVersion: SchemaVersion}
	if err := Save(filepath.Join(root, FilenameSiftToml), rootIdx); err != nil {
		t.Fatalf("save root: %v", err)
	}

	rep, err := Check(context.Background(), root, CheckOptions{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !rep.Clean() {
		t.Fatalf("expected clean, got %+v", rep.Defects)
	}

	// With include-ignored: secret.md becomes orphaned (not listed).
	rep2, err := Check(context.Background(), root, CheckOptions{IncludeIgnored: true})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rep2.Summary.Orphaned == 0 {
		t.Fatalf("expected orphaned with include-ignored, got %+v", rep2.Defects)
	}
}

func TestCheck_ContextCancellation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.md"), "x")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Check(ctx, root, CheckOptions{})
	if err == nil {
		t.Fatalf("expected context cancellation error")
	}
}

func TestCheck_BadRoot(t *testing.T) {
	_, err := Check(context.Background(), "/nonexistent-path-xyz-very-unlikely", CheckOptions{})
	if err == nil {
		t.Fatalf("expected error for missing root")
	}
}

func TestRenderJSON_Stable(t *testing.T) {
	rep := &CheckReport{
		SchemaVersion: 1,
		Root:          "/tmp/x",
		Folders: []FolderCheck{
			{Folder: ".", HasIndex: true, Stale: []string{"a.md"}},
		},
		Defects: []Defect{
			{Kind: "stale", Folder: ".", Path: "a.md", Detail: "x"},
		},
		Summary: Summary{Folders: 1, Stale: 1},
	}
	var buf bytes.Buffer
	if err := RenderJSON(&buf, rep); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var round map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &round); err != nil {
		t.Fatalf("json unmarshal: %v", err)
	}
	if round["schema_version"] == nil || round["root"] == nil {
		t.Fatalf("missing top-level fields: %s", buf.String())
	}
}

func TestRenderHumanAndMarkdown_DoNotCrash(t *testing.T) {
	rep := &CheckReport{
		SchemaVersion: 1,
		Root:          "/tmp/x",
		Folders: []FolderCheck{
			{Folder: "sub", Missing: true},
			{Folder: ".", HasIndex: true, Stale: []string{"a.md"}, Orphaned: []string{"b.md"}},
		},
		Defects: []Defect{
			{Kind: DefectMissingSiftToml, Folder: "sub"},
			{Kind: DefectStale, Folder: ".", Path: "a.md"},
			{Kind: DefectOrphaned, Folder: ".", Path: "b.md"},
		},
		Summary: Summary{Folders: 2, Missing: 1, Stale: 1, Orphaned: 1},
	}
	var hb, mb bytes.Buffer
	if err := RenderHuman(&hb, rep, false); err != nil {
		t.Fatalf("RenderHuman: %v", err)
	}
	if !strings.Contains(hb.String(), "Summary:") {
		t.Fatalf("human render missing Summary: %s", hb.String())
	}
	if err := RenderMarkdown(&mb, rep); err != nil {
		t.Fatalf("RenderMarkdown: %v", err)
	}
	if !strings.Contains(mb.String(), "# sift index check") {
		t.Fatalf("markdown render missing header: %s", mb.String())
	}
}

func TestCheck_MultipleDefectsSameTree(t *testing.T) {
	root := t.TempDir()
	// folder A: clean
	writeFile(t, filepath.Join(root, "a", "ok.md"), "fine")
	seedSiftToml(t, root, []string{}, "")
	rootIdx := &FolderIndex{
		SchemaVersion: SchemaVersion,
		Folders:       map[string]ChildFolder{"a": {}, "b": {}, "c": {}},
	}
	if err := Save(filepath.Join(root, FilenameSiftToml), rootIdx); err != nil {
		t.Fatalf("save root: %v", err)
	}
	seedSiftToml(t, filepath.Join(root, "a"), []string{"ok.md"}, "ok summary")

	// folder B: missing sift.toml + has files
	writeFile(t, filepath.Join(root, "b", "stuff.md"), "stuff")

	// folder C: orphan entry + orphan file
	writeFile(t, filepath.Join(root, "c", "real.md"), "real")
	cIdx := &FolderIndex{
		SchemaVersion: SchemaVersion,
		Files: map[string]FileEntry{
			"phantom.md": {ContentHash: "zz", HeadHash: "zz", TailHash: "zz", Words: 1, Summary: "x"},
		},
	}
	if err := Save(filepath.Join(root, "c", FilenameSiftToml), cIdx); err != nil {
		t.Fatalf("save c: %v", err)
	}

	rep, err := Check(context.Background(), root, CheckOptions{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if rep.Summary.Missing < 1 {
		t.Fatalf("expected missing >=1, got %+v", rep.Summary)
	}
	if rep.Summary.Orphaned < 2 {
		t.Fatalf("expected orphaned >=2 (phantom + real), got %+v", rep.Summary)
	}
}

// TestCheck_OrphanFilter_DefaultSkipsNonTextFiles verifies that the
// orphan walker only considers text-like files (per fileutil.IsIndexableText)
// by default, so a mixed code+docs folder doesn't drown in orphan defects
// for .go/.py/.rs sources. With --all (CheckOptions.IncludeAll=true), the
// non-text files participate again.
func TestCheck_OrphanFilter_DefaultSkipsNonTextFiles(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "doc.md"), "# Doc\n\nReadable markdown content for testing the orphan walker.")
	writeFile(t, filepath.Join(root, "main.go"), "package main\n")

	// No sift.toml exists → folder is missing-index but the .go file
	// must NOT be counted as a candidate.
	rep, err := Check(context.Background(), root, CheckOptions{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d := findDefect(rep, DefectOrphaned, "main.go"); d != nil {
		t.Fatalf("expected no orphan defect for main.go without --all, got %+v", d)
	}

	// Now seed a sift.toml that mentions doc.md only. main.go is on disk
	// but should NOT be flagged as an orphan in the default mode.
	seedSiftToml(t, root, []string{"doc.md"}, "Stable summary.")
	rep, err = Check(context.Background(), root, CheckOptions{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d := findDefect(rep, DefectOrphaned, "main.go"); d != nil {
		t.Fatalf("expected no orphan defect for main.go without --all, got %+v", d)
	}

	// With IncludeAll=true the .go file is now an orphan-candidate.
	rep, err = Check(context.Background(), root, CheckOptions{IncludeAll: true})
	if err != nil {
		t.Fatalf("Check IncludeAll: %v", err)
	}
	if d := findDefect(rep, DefectOrphaned, "main.go"); d == nil {
		t.Fatalf("expected orphan defect for main.go with IncludeAll, got none. defects=%+v", rep.Defects)
	}
}

// TestCheck_OrphanFilter_ExplicitTomlEntryAlwaysChecked verifies that a
// file explicitly listed in sift.toml is checked for existence on disk
// regardless of its extension. (Without this, deleting `tool.go` from
// disk while leaving its entry in sift.toml would silently slip past.)
func TestCheck_OrphanFilter_ExplicitTomlEntryAlwaysChecked(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "doc.md"), "# Doc\n\nMarkdown body.")
	// sift.toml lists a .go file that is NOT on disk.
	idx := &FolderIndex{
		SchemaVersion: SchemaVersion,
		Files: map[string]FileEntry{
			"missing.go": {ContentHash: "aa", HeadHash: "aa", TailHash: "aa", Words: 1, Summary: "tracked but gone"},
			"doc.md": func() FileEntry {
				sig, err := SigOf(filepath.Join(root, "doc.md"))
				if err != nil {
					t.Fatalf("sig: %v", err)
				}
				return FileEntry{ContentHash: sig.ContentHash, HeadHash: sig.HeadHash, TailHash: sig.TailHash, Words: sig.Words, Summary: "ok"}
			}(),
		},
	}
	if err := Save(filepath.Join(root, FilenameSiftToml), idx); err != nil {
		t.Fatalf("save: %v", err)
	}

	rep, err := Check(context.Background(), root, CheckOptions{})
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if d := findDefect(rep, DefectOrphaned, "missing.go"); d == nil {
		t.Fatalf("expected orphan defect for explicit missing.go entry, got none. defects=%+v", rep.Defects)
	}
}
