package sync

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sift/internal/index"
)

// readFileSha256 returns the SHA-256 hex digest of the file at path.
// Missing files return an empty string. Used to check that subsequent
// refreshes do not touch unchanged sift.toml files.
func readFileSha256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read %s: %v", path, err)
	}
	sum := sha256.Sum256(data)
	return string(sum[:])
}

// TestRefreshMaintainsFolderIndex_AutoCreate verifies that refresh
// auto-creates a minimal sift.toml in folders that contain indexable
// files but lack one.
func TestRefreshMaintainsFolderIndex_AutoCreate(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	writeMD(t, colDir, "alpha.md", 25)
	writeMD(t, colDir, "beta.md", 15)

	addCollection(t, env.DB, "notes", colDir)

	var buf bytes.Buffer
	stats, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	tomlPath := filepath.Join(colDir, index.FilenameSiftToml)
	if _, err := os.Stat(tomlPath); err != nil {
		t.Fatalf("expected sift.toml at %s: %v", tomlPath, err)
	}

	idx, err := index.Load(tomlPath)
	if err != nil {
		t.Fatalf("load sift.toml: %v", err)
	}
	if idx.SchemaVersion != index.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", idx.SchemaVersion, index.SchemaVersion)
	}
	if got := len(idx.Files); got != 2 {
		t.Errorf("Files entries = %d, want 2", got)
	}
	if _, ok := idx.Files["alpha.md"]; !ok {
		t.Errorf("alpha.md missing from index files")
	}
	if _, ok := idx.Files["beta.md"]; !ok {
		t.Errorf("beta.md missing from index files")
	}
	if idx.Refresh.FileCount != 2 {
		t.Errorf("Refresh.FileCount = %d, want 2", idx.Refresh.FileCount)
	}
	if idx.Purpose != "" {
		t.Errorf("expected empty Purpose on auto-created index, got %q", idx.Purpose)
	}

	if stats.IndexFoldersWritten == 0 {
		t.Errorf("IndexFoldersWritten = 0, want >= 1")
	}
	if stats.IndexFoldersCreated == 0 {
		t.Errorf("IndexFoldersCreated = 0, want >= 1")
	}
}

// TestRefreshMaintainsFolderIndex_Idempotent verifies that re-running
// refresh on an unchanged tree does not modify sift.toml.
func TestRefreshMaintainsFolderIndex_Idempotent(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	writeMD(t, colDir, "alpha.md", 25)
	writeMD(t, colDir, "beta.md", 15)
	addCollection(t, env.DB, "notes", colDir)

	var buf bytes.Buffer
	if _, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}

	tomlPath := filepath.Join(colDir, index.FilenameSiftToml)
	first := readFileSha256(t, tomlPath)
	if first == "" {
		t.Fatalf("sift.toml not created")
	}

	// Second refresh on unchanged tree.
	stats, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("second Refresh: %v", err)
	}

	second := readFileSha256(t, tomlPath)
	if first != second {
		t.Errorf("sift.toml content changed across refreshes (idempotency violated)")
	}
	if stats.IndexFoldersWritten != 0 {
		t.Errorf("IndexFoldersWritten = %d, want 0 on idempotent re-run", stats.IndexFoldersWritten)
	}
	if stats.IndexFoldersCreated != 0 {
		t.Errorf("IndexFoldersCreated = %d, want 0 on idempotent re-run", stats.IndexFoldersCreated)
	}
}

// TestRefreshMaintainsFolderIndex_OnlyChangedFolderUpdates verifies that
// when one file changes, only the sift.toml in that file's folder is
// updated; sibling folders' sift.toml files remain byte-identical.
func TestRefreshMaintainsFolderIndex_OnlyChangedFolderUpdates(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	subA := filepath.Join(colDir, "a")
	subB := filepath.Join(colDir, "b")
	if err := os.MkdirAll(subA, 0o755); err != nil {
		t.Fatalf("mkdir a: %v", err)
	}
	if err := os.MkdirAll(subB, 0o755); err != nil {
		t.Fatalf("mkdir b: %v", err)
	}
	pathA := writeMD(t, subA, "doc.md", 20)
	writeMD(t, subB, "doc.md", 20)

	addCollection(t, env.DB, "notes", colDir)

	var buf bytes.Buffer
	if _, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}

	tomlA := filepath.Join(subA, index.FilenameSiftToml)
	tomlB := filepath.Join(subB, index.FilenameSiftToml)
	beforeA := readFileSha256(t, tomlA)
	beforeB := readFileSha256(t, tomlB)
	if beforeA == "" || beforeB == "" {
		t.Fatalf("expected sift.toml in both folders")
	}

	// Mutate file in folder a only.
	if err := os.WriteFile(pathA, []byte("Completely new content for alpha\nLine two\nLine three\n"), 0o644); err != nil {
		t.Fatalf("rewrite pathA: %v", err)
	}

	if _, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf); err != nil {
		t.Fatalf("second Refresh: %v", err)
	}

	afterA := readFileSha256(t, tomlA)
	afterB := readFileSha256(t, tomlB)
	if beforeA == afterA {
		t.Errorf("expected sift.toml in folder a to change after file edit")
	}
	if beforeB != afterB {
		t.Errorf("sift.toml in folder b changed even though no file in b changed")
	}
}

// TestRefreshExcludesSiftToml_FromBM25 confirms that distinctive content
// added to a sift.toml is not searchable via the BM25 index. This
// regression-locks the chunker exclusion of sift.toml.
func TestRefreshExcludesSiftToml_FromBM25(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	writeMD(t, colDir, "alpha.md", 10)

	// Drop a distinctive marker into a sift.toml; refresh should not
	// chunk it into BM25.
	const sentinel = "ZZSIFTSENTINELZZ_distinctive_string_for_test_lookup"
	tomlContent := "schema_version = 1\npurpose = \"" + sentinel + "\"\n"
	if err := os.WriteFile(filepath.Join(colDir, index.FilenameSiftToml), []byte(tomlContent), 0o644); err != nil {
		t.Fatalf("write seed sift.toml: %v", err)
	}

	addCollection(t, env.DB, "notes", colDir)

	var buf bytes.Buffer
	if _, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	hits, err := env.Bleve.Search(sentinel, 10)
	if err != nil {
		t.Fatalf("Bleve.Search: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("sentinel string from sift.toml leaked into BM25: %d hits", len(hits))
	}
}

// TestRefreshNoIndexSkipsWrites verifies that NoIndex=true prevents
// sift.toml creation while allowing the chunk/index pipeline to run.
func TestRefreshNoIndexSkipsWrites(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	writeMD(t, colDir, "alpha.md", 25)
	addCollection(t, env.DB, "notes", colDir)

	var buf bytes.Buffer
	stats, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
		NoIndex:   true,
	}, &buf)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	tomlPath := filepath.Join(colDir, index.FilenameSiftToml)
	if _, err := os.Stat(tomlPath); !os.IsNotExist(err) {
		t.Fatalf("expected no sift.toml under --no-index; stat err = %v", err)
	}
	if stats.IndexFoldersWritten != 0 {
		t.Errorf("IndexFoldersWritten = %d, want 0", stats.IndexFoldersWritten)
	}
	if stats.IndexFoldersCreated != 0 {
		t.Errorf("IndexFoldersCreated = %d, want 0", stats.IndexFoldersCreated)
	}
	// File indexing must still have happened.
	if stats.ChunksTotal == 0 {
		t.Errorf("ChunksTotal = 0; expected file indexing to run under --no-index")
	}
	if stats.FilesNew != 1 {
		t.Errorf("FilesNew = %d, want 1", stats.FilesNew)
	}
}

// TestRefreshHonorsIgnoreFlag verifies that a folder marked
// `ignore = true` in its sift.toml is skipped from chunking and from
// index maintenance, and that descendants inherit the ignore.
func TestRefreshHonorsIgnoreFlag(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	// Tree:
	//   colDir/keep.md             (indexed; root sift.toml maintained)
	//   colDir/skipme/sift.toml    (ignore = true; folder skipped entirely)
	//   colDir/skipme/secret.md    (skipped — parent is ignore=true)
	//   colDir/skipme/nested/x.md  (skipped — inherits ignore)
	writeMD(t, colDir, "keep.md", 20)

	skipDir := filepath.Join(colDir, "skipme")
	if err := os.MkdirAll(filepath.Join(skipDir, "nested"), 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	writeMD(t, skipDir, "secret.md", 20)
	writeMD(t, filepath.Join(skipDir, "nested"), "x.md", 20)

	ignoreToml := "schema_version = 1\nignore = true\n"
	skipTomlPath := filepath.Join(skipDir, index.FilenameSiftToml)
	if err := os.WriteFile(skipTomlPath, []byte(ignoreToml), 0o644); err != nil {
		t.Fatalf("seed ignore sift.toml: %v", err)
	}
	beforeSkipToml := readFileSha256(t, skipTomlPath)

	addCollection(t, env.DB, "notes", colDir)

	var buf bytes.Buffer
	stats, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// Only keep.md should have been scanned.
	if stats.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1 (skipme tree should be skipped)", stats.FilesScanned)
	}
	if stats.FilesNew != 1 {
		t.Errorf("FilesNew = %d, want 1", stats.FilesNew)
	}

	// Nested ignored folder must not have a sift.toml created.
	nestedToml := filepath.Join(skipDir, "nested", index.FilenameSiftToml)
	if _, err := os.Stat(nestedToml); !os.IsNotExist(err) {
		t.Errorf("expected no sift.toml under ignored descendant; stat err = %v", err)
	}

	// The ignored folder's own sift.toml should be byte-identical (no rewrite).
	afterSkipToml := readFileSha256(t, skipTomlPath)
	if beforeSkipToml != afterSkipToml {
		t.Errorf("ignore=true folder's sift.toml was rewritten; expected untouched")
	}

	// Root folder should have its sift.toml maintained.
	rootToml := filepath.Join(colDir, index.FilenameSiftToml)
	if _, err := os.Stat(rootToml); err != nil {
		t.Errorf("expected root sift.toml to be maintained: %v", err)
	}
}

// TestRefreshFiles_SiftIgnoreSiblingNotAddedToFolderIndex verifies that
// when `sift refresh <file>` runs against a single file in a folder
// that also contains a sibling matched by the collection's
// `.siftignore`, the rewritten parent `sift.toml` does NOT include the
// ignored sibling. Without this gate, every targeted refresh would
// silently re-add the ignored sibling.
func TestRefreshFiles_SiftIgnoreSiblingNotAddedToFolderIndex(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	// Two siblings on disk. private.md is matched by .siftignore.
	tracked := writeMD(t, colDir, "tracked.md", 15)
	writeMD(t, colDir, "private.md", 15)
	if err := os.WriteFile(filepath.Join(colDir, ".siftignore"), []byte("private.md\n"), 0o644); err != nil {
		t.Fatalf("write .siftignore: %v", err)
	}

	addCollection(t, env.DB, "notes", colDir)

	var buf bytes.Buffer
	if _, err := RefreshFiles(context.Background(), env.DB, env.Bleve, nil, []string{tracked}, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf); err != nil {
		t.Fatalf("RefreshFiles: %v", err)
	}

	tomlPath := filepath.Join(colDir, index.FilenameSiftToml)
	idx, err := index.Load(tomlPath)
	if err != nil {
		t.Fatalf("load sift.toml: %v", err)
	}
	if _, ok := idx.Files["private.md"]; ok {
		t.Errorf("ignored sibling private.md was added to sift.toml; out=%q", buf.String())
	}
	if _, ok := idx.Files["tracked.md"]; !ok {
		t.Errorf("tracked.md missing from sift.toml; out=%q", buf.String())
	}
}

// TestRefreshFiles_IgnoreTrueAncestorSkipsFile verifies that targeted
// `sift refresh path/to/file.md` honors the inherited `ignore = true`
// on an ancestor folder's `sift.toml`. The file must NOT be chunked
// (no rows in the chunks table) and the ancestor's `sift.toml` must
// remain byte-identical.
func TestRefreshFiles_IgnoreTrueAncestorSkipsFile(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	// private/ is marked ignore=true via its own sift.toml.
	privateDir := filepath.Join(colDir, "private")
	if err := os.MkdirAll(privateDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	secret := writeMD(t, privateDir, "secret.md", 15)

	privateToml := filepath.Join(privateDir, index.FilenameSiftToml)
	if err := os.WriteFile(privateToml, []byte("schema_version = 1\nignore = true\n"), 0o644); err != nil {
		t.Fatalf("write private sift.toml: %v", err)
	}
	beforeHash := readFileSha256(t, privateToml)

	addCollection(t, env.DB, "notes", colDir)

	var buf bytes.Buffer
	stats, err := RefreshFiles(context.Background(), env.DB, env.Bleve, nil, []string{secret}, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("RefreshFiles: %v", err)
	}

	if stats.FilesNew != 0 || stats.FilesChanged != 0 {
		t.Errorf("expected zero files indexed (FilesNew=%d FilesChanged=%d); out=%q",
			stats.FilesNew, stats.FilesChanged, buf.String())
	}

	// Bleve must contain no docs from the ignored file.
	if n := bleveDocCount(t, env.Bleve); n != 0 {
		t.Errorf("expected 0 Bleve docs, got %d; out=%q", n, buf.String())
	}

	// The ignored ancestor's sift.toml must be untouched.
	if got := readFileSha256(t, privateToml); got != beforeHash {
		t.Errorf("ignore=true ancestor sift.toml was rewritten; out=%q", buf.String())
	}

	// The output should mention skipping. (Soft check; not load-bearing.)
	if !strings.Contains(buf.String(), "ignore=true") {
		t.Logf("note: skip message did not mention ignore=true; got: %s", buf.String())
	}
}

// TestRefreshFullTree_SiftIgnoreSkipsFolderMaintenance verifies that
// the collection-wide refresh path honors `.siftignore` for folder
// index maintenance: it must NOT walk into ignored directories and
// must NOT create `sift.toml` files inside them. It also must NOT
// list the ignored child folder under the parent's tracked children.
//
// Previously only the targeted-refresh path consulted `.siftignore`;
// the full-tree walker independently descended into ignored subtrees
// and created/updated empty `sift.toml` skeletons there.
func TestRefreshFullTree_SiftIgnoreSkipsFolderMaintenance(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	writeMD(t, colDir, "tracked.md", 15)

	// scratch/ — directly ignored. Contains a nested file and a
	// nested subfolder that, without the ignore, would each get a
	// `sift.toml`.
	scratchSub := filepath.Join(colDir, "scratch", "deep")
	if err := os.MkdirAll(scratchSub, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeMD(t, filepath.Join(colDir, "scratch"), "notes.md", 10)
	writeMD(t, scratchSub, "more.md", 10)

	if err := os.WriteFile(filepath.Join(colDir, ".siftignore"), []byte("scratch/\n"), 0o644); err != nil {
		t.Fatalf("write .siftignore: %v", err)
	}

	addCollection(t, env.DB, "notes", colDir)

	var buf bytes.Buffer
	if _, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	// No sift.toml may be created anywhere under scratch/.
	for _, p := range []string{
		filepath.Join(colDir, "scratch", index.FilenameSiftToml),
		filepath.Join(scratchSub, index.FilenameSiftToml),
	} {
		if _, err := os.Stat(p); err == nil {
			t.Errorf("unexpected sift.toml created in ignored subtree: %s; out=%q", p, buf.String())
		}
	}

	// The root sift.toml must not list `scratch` as a tracked child.
	rootToml := filepath.Join(colDir, index.FilenameSiftToml)
	idx, err := index.Load(rootToml)
	if err != nil {
		t.Fatalf("load root sift.toml: %v", err)
	}
	if _, ok := idx.Folders["scratch"]; ok {
		t.Errorf("root sift.toml lists ignored child folder 'scratch'; out=%q", buf.String())
	}
}

// TestRefreshIndexHookFiresWhenPlanUnchanged verifies the bug fix: when
// a mechanical pass already populated sift.toml (no editorial fields)
// and the user re-runs refresh on an unchanged tree, the IndexHook must
// still be invoked so that --generate=missing can backfill empty
// summaries. Previously the hook was skipped whenever plan.Changed was
// false, leaving missing-mode silently inert.
func TestRefreshIndexHookFiresWhenPlanUnchanged(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	writeMD(t, colDir, "alpha.md", 25)
	addCollection(t, env.DB, "notes", colDir)

	// First pass: mechanical only. Populates sift.toml with hashes
	// and a file entry but leaves Purpose/Summary empty.
	var buf bytes.Buffer
	if _, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf); err != nil {
		t.Fatalf("first Refresh: %v", err)
	}

	tomlPath := filepath.Join(colDir, index.FilenameSiftToml)
	beforeHash := readFileSha256(t, tomlPath)
	idxBefore, err := index.Load(tomlPath)
	if err != nil {
		t.Fatalf("load sift.toml: %v", err)
	}
	if idxBefore.Purpose != "" {
		t.Fatalf("precondition: expected empty Purpose, got %q", idxBefore.Purpose)
	}

	// Second pass: tree is unchanged so plan.Changed will be false.
	// The hook MUST still be invoked so a generation mode can decide
	// whether to backfill empty editorial fields.
	var hookCalls int
	var sawUnchangedFolder bool
	hook := func(folder string, plan *index.MaintainPlan) error {
		hookCalls++
		if plan != nil && !plan.Changed {
			sawUnchangedFolder = true
		}
		return nil
	}

	stats, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
		IndexHook: hook,
	}, &buf)
	if err != nil {
		t.Fatalf("second Refresh: %v", err)
	}

	if hookCalls == 0 {
		t.Errorf("IndexHook was never invoked on unchanged tree; expected at least one call")
	}
	if !sawUnchangedFolder {
		t.Errorf("expected at least one hook call with plan.Changed == false")
	}

	// Mechanical idempotency must be preserved: no rewrites.
	if stats.IndexFoldersWritten != 0 {
		t.Errorf("IndexFoldersWritten = %d, want 0 on unchanged tree", stats.IndexFoldersWritten)
	}
	if got := readFileSha256(t, tomlPath); got != beforeHash {
		t.Errorf("sift.toml content changed despite plan.Changed == false (idempotency violated)")
	}
}

// TestRefreshIndexHookFiresOnPlanChanged verifies the original behavior
// is preserved: when the plan is genuinely changing the folder, the
// hook still fires.
func TestRefreshIndexHookFiresOnPlanChanged(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	writeMD(t, colDir, "alpha.md", 25)
	addCollection(t, env.DB, "notes", colDir)

	var hookCalls int
	var sawChangedFolder bool
	hook := func(folder string, plan *index.MaintainPlan) error {
		hookCalls++
		if plan != nil && plan.Changed {
			sawChangedFolder = true
		}
		return nil
	}

	var buf bytes.Buffer
	if _, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
		IndexHook: hook,
	}, &buf); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if hookCalls == 0 {
		t.Errorf("expected IndexHook to be invoked on initial refresh")
	}
	if !sawChangedFolder {
		t.Errorf("expected at least one hook call with plan.Changed == true")
	}
}
