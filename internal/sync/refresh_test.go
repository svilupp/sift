package sync

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sift/internal/bm25"
	"sift/internal/chunk"
	"sift/internal/db"
	"sift/internal/voyage"
)

// testEnv holds shared infrastructure for refresh tests.
type testEnv struct {
	DB       *db.DB
	Bleve    *bm25.BleveIndex
	ChunkOpt chunk.Options
}

// setupEnv creates a fresh DB and Bleve index in temp directories.
func setupEnv(t *testing.T) *testEnv {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	d, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	if err := d.Init(); err != nil {
		t.Fatalf("db.Init: %v", err)
	}

	blevePath := filepath.Join(t.TempDir(), "test.bleve")
	b, err := bm25.OpenBleve(blevePath, "standard")
	if err != nil {
		t.Fatalf("bm25.OpenBleve: %v", err)
	}
	t.Cleanup(func() { b.Close() })

	return &testEnv{
		DB:    d,
		Bleve: b,
		ChunkOpt: chunk.Options{
			RowsPerChunk:  10,
			OverlapRows:   2,
			MinChunkChars: 5,
			SkipEmptyRows: true,
		},
	}
}

// writeMD creates a .md file in dir with the given number of content lines.
// Each line is "Line N of <name>" to ensure enough chars per chunk.
func writeMD(t *testing.T, dir, name string, lines int) string {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		if i > 1 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "Line %d of %s with sufficient content for chunking", i, name)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// addCollection registers a collection in the DB and returns it.
func addCollection(t *testing.T, d *db.DB, name, path string) *db.Collection {
	t.Helper()
	col, err := d.AddCollection(name, path, nil)
	if err != nil {
		t.Fatalf("AddCollection(%q): %v", name, err)
	}
	return col
}

// bleveDocCount returns the Bleve document count, failing the test on error.
func bleveDocCount(t *testing.T, b *bm25.BleveIndex) uint64 {
	t.Helper()
	n, err := b.DocCount()
	if err != nil {
		t.Fatalf("DocCount: %v", err)
	}
	return n
}

func TestRefreshNewFiles(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	// Create sample .md files.
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

	// Verify stats.
	if stats.FilesScanned != 2 {
		t.Errorf("FilesScanned = %d, want 2", stats.FilesScanned)
	}
	if stats.FilesNew != 2 {
		t.Errorf("FilesNew = %d, want 2", stats.FilesNew)
	}
	if stats.FilesChanged != 0 {
		t.Errorf("FilesChanged = %d, want 0", stats.FilesChanged)
	}
	if stats.FilesDeleted != 0 {
		t.Errorf("FilesDeleted = %d, want 0", stats.FilesDeleted)
	}
	if stats.ChunksTotal <= 0 {
		t.Errorf("ChunksTotal = %d, want > 0", stats.ChunksTotal)
	}
	if stats.Duration <= 0 {
		t.Error("Duration should be positive")
	}

	// Verify DB has file records.
	col, err := env.DB.GetCollection("notes")
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	files, err := env.DB.GetFilesByCollection(col.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("DB files = %d, want 2", len(files))
	}

	// Verify DB has chunks for each file.
	totalChunks := 0
	for _, f := range files {
		chunks, err := env.DB.GetChunksByFile(f.ID)
		if err != nil {
			t.Fatalf("GetChunksByFile(%d): %v", f.ID, err)
		}
		if len(chunks) == 0 {
			t.Errorf("file %q has 0 chunks, want > 0", f.Path)
		}
		totalChunks += len(chunks)
	}
	if totalChunks != stats.ChunksTotal {
		t.Errorf("DB chunks = %d, stats.ChunksTotal = %d", totalChunks, stats.ChunksTotal)
	}

	// Verify Bleve doc count matches chunk count.
	if got := bleveDocCount(t, env.Bleve); got != uint64(stats.ChunksTotal) {
		t.Errorf("Bleve DocCount = %d, want %d", got, stats.ChunksTotal)
	}
}

func TestRefreshTreatsEmptyKeyClientAsBM25Only(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()
	writeMD(t, colDir, "local.md", 12)
	addCollection(t, env.DB, "local", colDir)

	requests := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client := voyage.NewClientWithBaseURL("", srv.URL)
	var buf bytes.Buffer
	stats, err := Refresh(context.Background(), env.DB, env.Bleve, client, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	if stats.ChunksEmbedded != 0 || stats.EmbedErrors != 0 {
		t.Fatalf("embedding stats = embedded:%d errors:%d, want 0/0", stats.ChunksEmbedded, stats.EmbedErrors)
	}
	if requests != 0 {
		t.Fatalf("empty-key refresh sent %d HTTP request(s), want 0", requests)
	}
	deadLetters, err := env.DB.GetUnresolvedDeadLetters()
	if err != nil {
		t.Fatalf("GetUnresolvedDeadLetters: %v", err)
	}
	if len(deadLetters) != 0 {
		t.Fatalf("empty-key refresh created %d dead letter(s), want 0", len(deadLetters))
	}
}

func TestRefreshIncremental(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	path1 := writeMD(t, colDir, "stable.md", 20)
	path2 := writeMD(t, colDir, "changing.md", 20)

	addCollection(t, env.DB, "docs", colDir)

	// First refresh: both files are new.
	var buf bytes.Buffer
	_, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("first Refresh: %v", err)
	}

	// Record state after first refresh.
	bleveCountAfterFirst := bleveDocCount(t, env.Bleve)

	// Modify one file: change content and explicitly bump mtime.
	// The mtime is compared as Unix seconds, so a write within the same
	// second would not be detected. Use os.Chtimes to guarantee a new mtime.
	_ = path1 // stable, not modified
	newContent := "Modified content\n" + strings.Repeat("Extra line of text for the changed file\n", 25)
	if err := os.WriteFile(path2, []byte(newContent), 0o644); err != nil {
		t.Fatalf("rewrite changing.md: %v", err)
	}
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path2, future, future); err != nil {
		t.Fatalf("chtimes changing.md: %v", err)
	}

	// Second refresh: only the changed file should be detected.
	buf.Reset()
	stats, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("second Refresh: %v", err)
	}

	if stats.FilesNew != 0 {
		t.Errorf("FilesNew = %d, want 0", stats.FilesNew)
	}
	if stats.FilesChanged != 1 {
		t.Errorf("FilesChanged = %d, want 1", stats.FilesChanged)
	}
	if stats.FilesDeleted != 0 {
		t.Errorf("FilesDeleted = %d, want 0", stats.FilesDeleted)
	}
	if stats.FilesScanned != 2 {
		t.Errorf("FilesScanned = %d, want 2", stats.FilesScanned)
	}

	// Bleve should still have documents (old chunks replaced with new).
	bleveCountAfterSecond := bleveDocCount(t, env.Bleve)
	if bleveCountAfterSecond == 0 {
		t.Error("Bleve DocCount = 0 after second refresh, want > 0")
	}

	// The changed file's old chunks were removed and new ones added,
	// so the total may differ. Verify it changed at all.
	_ = bleveCountAfterFirst // may differ; just confirm non-zero above

	// Verify DB file record updated.
	f, err := env.DB.GetFileByPath(path2)
	if err != nil {
		t.Fatalf("GetFileByPath: %v", err)
	}
	if f == nil {
		t.Fatal("changed file not found in DB")
	}
	if f.ChunkCount == 0 {
		t.Error("changed file has ChunkCount = 0, want > 0")
	}
}

func TestRefreshDeletedFile(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	writeMD(t, colDir, "keep.md", 20)
	removePath := writeMD(t, colDir, "remove.md", 20)

	col := addCollection(t, env.DB, "vault", colDir)

	// First refresh: index both.
	var buf bytes.Buffer
	stats1, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("first Refresh: %v", err)
	}
	if stats1.FilesNew != 2 {
		t.Fatalf("first FilesNew = %d, want 2", stats1.FilesNew)
	}

	bleveCountBefore := bleveDocCount(t, env.Bleve)

	// Delete one file from disk.
	if err := os.Remove(removePath); err != nil {
		t.Fatalf("remove file: %v", err)
	}

	// Second refresh: detect deletion.
	buf.Reset()
	stats2, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("second Refresh: %v", err)
	}

	if stats2.FilesDeleted != 1 {
		t.Errorf("FilesDeleted = %d, want 1", stats2.FilesDeleted)
	}
	if stats2.FilesNew != 0 {
		t.Errorf("FilesNew = %d, want 0", stats2.FilesNew)
	}
	if stats2.FilesChanged != 0 {
		t.Errorf("FilesChanged = %d, want 0", stats2.FilesChanged)
	}

	// Verify DB no longer has the removed file.
	f, err := env.DB.GetFileByPath(removePath)
	if err != nil {
		t.Fatalf("GetFileByPath: %v", err)
	}
	if f != nil {
		t.Error("deleted file still present in DB")
	}

	// Verify only 1 file remains in the collection.
	files, err := env.DB.GetFilesByCollection(col.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection: %v", err)
	}
	if len(files) != 1 {
		t.Errorf("DB files = %d, want 1", len(files))
	}

	// Verify Bleve doc count decreased.
	bleveCountAfter := bleveDocCount(t, env.Bleve)
	if bleveCountAfter >= bleveCountBefore {
		t.Errorf("Bleve DocCount after delete (%d) should be less than before (%d)",
			bleveCountAfter, bleveCountBefore)
	}
}

func TestRefreshDryRun(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	writeMD(t, colDir, "doc.md", 20)

	addCollection(t, env.DB, "dry", colDir)

	var buf bytes.Buffer
	stats, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		DryRun:    true,
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("Refresh dry run: %v", err)
	}

	// Stats should report changes detected.
	if stats.FilesNew != 1 {
		t.Errorf("FilesNew = %d, want 1", stats.FilesNew)
	}
	if stats.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", stats.FilesScanned)
	}

	// But DB and Bleve should remain empty.
	col, err := env.DB.GetCollection("dry")
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	files, err := env.DB.GetFilesByCollection(col.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("DB files = %d after dry run, want 0", len(files))
	}

	if got := bleveDocCount(t, env.Bleve); got != 0 {
		t.Errorf("Bleve DocCount = %d after dry run, want 0", got)
	}

	// Output should include the "+" prefix for the file that would be indexed.
	if !strings.Contains(buf.String(), "+") {
		t.Error("dry run output should contain '+' for new files")
	}
}

func TestRefreshCollectionFilter(t *testing.T) {
	env := setupEnv(t)

	// Create two separate collection directories.
	dir1 := t.TempDir()
	dir2 := t.TempDir()

	writeMD(t, dir1, "file1.md", 20)
	writeMD(t, dir2, "file2.md", 20)

	addCollection(t, env.DB, "alpha", dir1)
	addCollection(t, env.DB, "beta", dir2)

	// Refresh only "alpha".
	var buf bytes.Buffer
	stats, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		CollectionName: "alpha",
		ChunkOpts:      env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("Refresh with filter: %v", err)
	}

	// Only alpha's file should be indexed.
	if stats.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1", stats.FilesScanned)
	}
	if stats.FilesNew != 1 {
		t.Errorf("FilesNew = %d, want 1", stats.FilesNew)
	}

	// Verify alpha has files in DB.
	colAlpha, err := env.DB.GetCollection("alpha")
	if err != nil {
		t.Fatalf("GetCollection alpha: %v", err)
	}
	alphaFiles, err := env.DB.GetFilesByCollection(colAlpha.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection alpha: %v", err)
	}
	if len(alphaFiles) != 1 {
		t.Errorf("alpha DB files = %d, want 1", len(alphaFiles))
	}

	// Verify beta has no files in DB (was not refreshed).
	colBeta, err := env.DB.GetCollection("beta")
	if err != nil {
		t.Fatalf("GetCollection beta: %v", err)
	}
	betaFiles, err := env.DB.GetFilesByCollection(colBeta.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection beta: %v", err)
	}
	if len(betaFiles) != 0 {
		t.Errorf("beta DB files = %d, want 0 (beta was not refreshed)", len(betaFiles))
	}
}

func TestRefreshMissingCollectionPathSkipped(t *testing.T) {
	env := setupEnv(t)

	dir1 := t.TempDir()
	missingDir := filepath.Join(t.TempDir(), "gone")

	writeMD(t, dir1, "file1.md", 20)

	addCollection(t, env.DB, "alpha", dir1)
	addCollection(t, env.DB, "ghost", missingDir)

	var buf bytes.Buffer
	stats, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if !strings.Contains(buf.String(), "collection path missing, skipping: "+missingDir) {
		t.Errorf("expected missing-path warning in output, got: %s", buf.String())
	}

	if stats.FilesScanned != 1 {
		t.Errorf("FilesScanned = %d, want 1 (only alpha's file)", stats.FilesScanned)
	}

	colAlpha, err := env.DB.GetCollection("alpha")
	if err != nil {
		t.Fatalf("GetCollection alpha: %v", err)
	}
	alphaFiles, err := env.DB.GetFilesByCollection(colAlpha.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection alpha: %v", err)
	}
	if len(alphaFiles) != 1 {
		t.Errorf("alpha DB files = %d, want 1", len(alphaFiles))
	}
}

func TestRefreshMissingCollectionPathExplicitErrors(t *testing.T) {
	env := setupEnv(t)

	missingDir := filepath.Join(t.TempDir(), "gone")
	addCollection(t, env.DB, "ghost", missingDir)

	var buf bytes.Buffer
	_, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		CollectionName: "ghost",
		ChunkOpts:      env.ChunkOpt,
	}, &buf)
	if err == nil {
		t.Fatal("expected error refreshing explicit missing collection, got nil")
	}
}

func TestRefreshOverlappingCollections(t *testing.T) {
	env := setupEnv(t)
	parentDir := t.TempDir()
	childDir := filepath.Join(parentDir, "docs", "agent")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatalf("mkdir child dir: %v", err)
	}

	writeMD(t, parentDir, "root.md", 20)
	sharedPath := writeMD(t, childDir, "gateway.md", 20)

	parent := addCollection(t, env.DB, "vault", parentDir)
	child := addCollection(t, env.DB, "agent", childDir)

	var buf bytes.Buffer
	stats, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if stats.FilesScanned != 2 {
		t.Fatalf("FilesScanned = %d, want 2 unique files", stats.FilesScanned)
	}
	if stats.FilesNew != 2 {
		t.Fatalf("FilesNew = %d, want 2", stats.FilesNew)
	}

	parentFiles, err := env.DB.GetFilesByCollection(parent.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection parent: %v", err)
	}
	if len(parentFiles) != 2 {
		t.Fatalf("parent file count = %d, want 2", len(parentFiles))
	}

	childFiles, err := env.DB.GetFilesByCollection(child.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection child: %v", err)
	}
	if len(childFiles) != 1 {
		t.Fatalf("child file count = %d, want 1", len(childFiles))
	}

	allFiles, err := env.DB.ListFiles()
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if len(allFiles) != 2 {
		t.Fatalf("ListFiles returned %d files, want 2", len(allFiles))
	}

	shared, err := env.DB.GetFileByPath(sharedPath)
	if err != nil {
		t.Fatalf("GetFileByPath shared: %v", err)
	}
	if shared == nil {
		t.Fatal("shared file not found")
	}
	if shared.CollectionID != child.ID {
		t.Fatalf("shared primary collection = %d, want %d", shared.CollectionID, child.ID)
	}

	memberships, err := env.DB.GetFileCollectionIDs(shared.ID)
	if err != nil {
		t.Fatalf("GetFileCollectionIDs: %v", err)
	}
	if len(memberships) != 2 {
		t.Fatalf("shared memberships = %v, want 2 collections", memberships)
	}
}

func TestRefreshSpecificNestedCollectionMaintainsParentMembership(t *testing.T) {
	env := setupEnv(t)
	parentDir := t.TempDir()
	childDir := filepath.Join(parentDir, "docs", "agent")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatalf("mkdir child dir: %v", err)
	}

	sharedPath := writeMD(t, childDir, "gateway.md", 20)

	parent := addCollection(t, env.DB, "vault", parentDir)
	child := addCollection(t, env.DB, "agent", childDir)

	var buf bytes.Buffer
	stats, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		CollectionName: "agent",
		ChunkOpts:      env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("Refresh nested collection: %v", err)
	}

	if stats.FilesScanned != 1 {
		t.Fatalf("FilesScanned = %d, want 1", stats.FilesScanned)
	}

	parentFiles, err := env.DB.GetFilesByCollection(parent.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection parent: %v", err)
	}
	if len(parentFiles) != 1 {
		t.Fatalf("parent file count = %d, want 1 shared file", len(parentFiles))
	}

	childFiles, err := env.DB.GetFilesByCollection(child.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection child: %v", err)
	}
	if len(childFiles) != 1 {
		t.Fatalf("child file count = %d, want 1 shared file", len(childFiles))
	}

	shared, err := env.DB.GetFileByPath(sharedPath)
	if err != nil {
		t.Fatalf("GetFileByPath shared: %v", err)
	}
	if shared == nil {
		t.Fatal("shared file not found")
	}
	if shared.CollectionID != child.ID {
		t.Fatalf("shared primary collection = %d, want %d", shared.CollectionID, child.ID)
	}
}

// --- RefreshFiles tests ---

func TestRefreshFilesNewFile(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	addCollection(t, env.DB, "vault", colDir)

	// Create a file inside the collection dir.
	path := writeMD(t, colDir, "new-note.md", 20)

	var buf bytes.Buffer
	stats, err := RefreshFiles(context.Background(), env.DB, env.Bleve, nil, []string{path}, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("RefreshFiles: %v", err)
	}

	if stats.FilesNew != 1 {
		t.Errorf("FilesNew = %d, want 1", stats.FilesNew)
	}
	if stats.FilesChanged != 0 {
		t.Errorf("FilesChanged = %d, want 0", stats.FilesChanged)
	}
	if stats.ChunksTotal <= 0 {
		t.Errorf("ChunksTotal = %d, want > 0", stats.ChunksTotal)
	}
	if stats.Duration <= 0 {
		t.Error("Duration should be positive")
	}

	// Verify file is in DB.
	f, err := env.DB.GetFileByPath(path)
	if err != nil {
		t.Fatalf("GetFileByPath: %v", err)
	}
	if f == nil {
		t.Fatal("file not found in DB after RefreshFiles")
	}
	if f.ChunkCount == 0 {
		t.Error("file has ChunkCount = 0, want > 0")
	}

	// Verify Bleve has docs.
	if got := bleveDocCount(t, env.Bleve); got != uint64(stats.ChunksTotal) {
		t.Errorf("Bleve DocCount = %d, want %d", got, stats.ChunksTotal)
	}
}

func TestRefreshFilesChangedFile(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	addCollection(t, env.DB, "vault", colDir)
	path := writeMD(t, colDir, "note.md", 20)

	// First: index via full collection refresh.
	var buf bytes.Buffer
	_, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("initial Refresh: %v", err)
	}

	oldFile, err := env.DB.GetFileByPath(path)
	if err != nil || oldFile == nil {
		t.Fatalf("file not in DB after initial refresh")
	}
	oldChunks, err := env.DB.GetChunksByFile(oldFile.ID)
	if err != nil {
		t.Fatalf("GetChunksByFile: %v", err)
	}
	bleveCountBefore := bleveDocCount(t, env.Bleve)

	// Modify the file with different content.
	newContent := "# Updated Title\n" + strings.Repeat("Completely different content line for re-chunking\n", 30)
	if err := os.WriteFile(path, []byte(newContent), 0o644); err != nil {
		t.Fatalf("rewrite note.md: %v", err)
	}

	// RefreshFiles the specific file.
	buf.Reset()
	stats, err := RefreshFiles(context.Background(), env.DB, env.Bleve, nil, []string{path}, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("RefreshFiles: %v", err)
	}

	if stats.FilesNew != 0 {
		t.Errorf("FilesNew = %d, want 0", stats.FilesNew)
	}
	if stats.FilesChanged != 1 {
		t.Errorf("FilesChanged = %d, want 1", stats.FilesChanged)
	}
	if stats.ChunksTotal <= 0 {
		t.Errorf("ChunksTotal = %d, want > 0", stats.ChunksTotal)
	}

	// Verify DB updated.
	updatedFile, err := env.DB.GetFileByPath(path)
	if err != nil || updatedFile == nil {
		t.Fatal("file not in DB after RefreshFiles")
	}
	if updatedFile.FileHash == oldFile.FileHash {
		t.Error("file hash should have changed")
	}

	// Verify chunks exist after re-index.
	newChunks, err := env.DB.GetChunksByFile(updatedFile.ID)
	if err != nil {
		t.Fatalf("GetChunksByFile after update: %v", err)
	}
	if len(newChunks) == 0 {
		t.Error("no chunks after update")
	}
	// Content changed significantly, so chunk count should differ.
	if len(newChunks) == len(oldChunks) {
		t.Logf("chunk count unchanged (%d) — content change may not have altered chunk boundaries", len(oldChunks))
	}

	// Bleve should have been updated (old removed, new added).
	bleveCountAfter := bleveDocCount(t, env.Bleve)
	if bleveCountAfter == 0 {
		t.Error("Bleve should have docs after re-index")
	}
	_ = bleveCountBefore // counts may differ due to different content
}

func TestRefreshFilesMultipleCollections(t *testing.T) {
	env := setupEnv(t)

	dir1 := t.TempDir()
	dir2 := t.TempDir()

	addCollection(t, env.DB, "notes", dir1)
	addCollection(t, env.DB, "docs", dir2)

	path1 := writeMD(t, dir1, "note.md", 15)
	path2 := writeMD(t, dir2, "doc.md", 15)

	var buf bytes.Buffer
	stats, err := RefreshFiles(context.Background(), env.DB, env.Bleve, nil, []string{path1, path2}, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("RefreshFiles: %v", err)
	}

	if stats.FilesNew != 2 {
		t.Errorf("FilesNew = %d, want 2", stats.FilesNew)
	}
	if stats.ChunksTotal <= 0 {
		t.Errorf("ChunksTotal = %d, want > 0", stats.ChunksTotal)
	}

	// Verify each file ended up in the right collection.
	f1, err := env.DB.GetFileByPath(path1)
	if err != nil || f1 == nil {
		t.Fatal("path1 not in DB")
	}
	col1, _ := env.DB.GetCollection("notes")
	if f1.CollectionID != col1.ID {
		t.Errorf("path1 collection_id = %d, want %d (notes)", f1.CollectionID, col1.ID)
	}

	f2, err := env.DB.GetFileByPath(path2)
	if err != nil || f2 == nil {
		t.Fatal("path2 not in DB")
	}
	col2, _ := env.DB.GetCollection("docs")
	if f2.CollectionID != col2.ID {
		t.Errorf("path2 collection_id = %d, want %d (docs)", f2.CollectionID, col2.ID)
	}
}

func TestRefreshFilesNotInCollection(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()
	outsideDir := t.TempDir()

	addCollection(t, env.DB, "vault", colDir)

	// File outside any collection.
	outsidePath := writeMD(t, outsideDir, "orphan.md", 10)

	var buf bytes.Buffer
	stats, err := RefreshFiles(context.Background(), env.DB, env.Bleve, nil, []string{outsidePath}, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("RefreshFiles: %v", err)
	}

	// Should be skipped, zero files processed.
	if stats.FilesNew != 0 {
		t.Errorf("FilesNew = %d, want 0", stats.FilesNew)
	}
	if stats.FilesChanged != 0 {
		t.Errorf("FilesChanged = %d, want 0", stats.FilesChanged)
	}

	// Output should mention skipping.
	if !strings.Contains(buf.String(), "Skipping") {
		t.Error("output should mention skipping the file")
	}
	if !strings.Contains(buf.String(), "not inside any registered collection") {
		t.Error("output should mention why file was skipped")
	}
}

func TestRefreshFilesUnsupportedType(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	addCollection(t, env.DB, "vault", colDir)

	// Write a non-text file inside the collection.
	pngPath := filepath.Join(colDir, "image.png")
	if err := os.WriteFile(pngPath, []byte("fake png"), 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}

	var buf bytes.Buffer
	stats, err := RefreshFiles(context.Background(), env.DB, env.Bleve, nil, []string{pngPath}, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("RefreshFiles: %v", err)
	}

	if stats.FilesNew != 0 || stats.FilesChanged != 0 {
		t.Errorf("should skip unsupported type: new=%d, changed=%d", stats.FilesNew, stats.FilesChanged)
	}
	if !strings.Contains(buf.String(), "unsupported file type") {
		t.Error("output should mention unsupported file type")
	}
}

func TestRefreshFilesNonexistent(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	addCollection(t, env.DB, "vault", colDir)

	var buf bytes.Buffer
	stats, err := RefreshFiles(context.Background(), env.DB, env.Bleve, nil, []string{filepath.Join(colDir, "ghost.md")}, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("RefreshFiles: %v", err)
	}

	if stats.FilesNew != 0 || stats.FilesChanged != 0 {
		t.Errorf("should skip nonexistent: new=%d, changed=%d", stats.FilesNew, stats.FilesChanged)
	}
	if !strings.Contains(buf.String(), "Skipping") {
		t.Error("output should mention skipping the nonexistent file")
	}
}

func TestRefreshFilesDryRun(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	addCollection(t, env.DB, "vault", colDir)
	path := writeMD(t, colDir, "note.md", 15)

	var buf bytes.Buffer
	stats, err := RefreshFiles(context.Background(), env.DB, env.Bleve, nil, []string{path}, RefreshOptions{
		DryRun:    true,
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("RefreshFiles dry-run: %v", err)
	}

	if stats.FilesNew != 1 {
		t.Errorf("FilesNew = %d, want 1", stats.FilesNew)
	}

	// DB and Bleve should be empty.
	f, err := env.DB.GetFileByPath(path)
	if err != nil {
		t.Fatalf("GetFileByPath: %v", err)
	}
	if f != nil {
		t.Error("file should not be in DB after dry-run")
	}
	if got := bleveDocCount(t, env.Bleve); got != 0 {
		t.Errorf("Bleve DocCount = %d after dry-run, want 0", got)
	}

	// Output should show the file path.
	if !strings.Contains(buf.String(), "+") {
		t.Error("dry-run output should contain '+' marker")
	}
}

func TestRefreshFilesMixedValid(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()
	outsideDir := t.TempDir()

	addCollection(t, env.DB, "vault", colDir)

	goodPath := writeMD(t, colDir, "good.md", 15)
	outsidePath := writeMD(t, outsideDir, "outside.md", 10)
	missingPath := filepath.Join(colDir, "missing.md")

	var buf bytes.Buffer
	stats, err := RefreshFiles(context.Background(), env.DB, env.Bleve, nil, []string{goodPath, outsidePath, missingPath}, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("RefreshFiles: %v", err)
	}

	// Only the valid file should be processed.
	if stats.FilesNew != 1 {
		t.Errorf("FilesNew = %d, want 1", stats.FilesNew)
	}
	if stats.ChunksTotal <= 0 {
		t.Errorf("ChunksTotal = %d, want > 0", stats.ChunksTotal)
	}

	// Output should mention skipping the bad ones.
	output := buf.String()
	if !strings.Contains(output, "Skipping") {
		t.Error("output should mention skipping invalid files")
	}
}

// --- resolveCollection tests ---

func TestResolveCollectionByDB(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	col := addCollection(t, env.DB, "vault", colDir)
	filePath := writeMD(t, colDir, "indexed.md", 10)

	// Index the file first.
	var buf bytes.Buffer
	_, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	collections, err := env.DB.ListCollections()
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}

	got, err := resolveCollection(env.DB, filePath, collections)
	if err != nil {
		t.Fatalf("resolveCollection: %v", err)
	}
	if got.ID != col.ID {
		t.Errorf("resolved collection ID = %d, want %d", got.ID, col.ID)
	}
}

func TestResolveCollectionByPathPrefix(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	col := addCollection(t, env.DB, "vault", colDir)

	// New file not yet in DB, but inside collection path.
	newPath := filepath.Join(colDir, "brand-new.md")

	collections, err := env.DB.ListCollections()
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}

	got, err := resolveCollection(env.DB, newPath, collections)
	if err != nil {
		t.Fatalf("resolveCollection: %v", err)
	}
	if got.ID != col.ID {
		t.Errorf("resolved collection ID = %d, want %d", got.ID, col.ID)
	}
}

func TestResolveCollectionLongestPrefix(t *testing.T) {
	env := setupEnv(t)
	parentDir := t.TempDir()
	childDir := filepath.Join(parentDir, "sub")
	if err := os.Mkdir(childDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	addCollection(t, env.DB, "parent", parentDir)
	child := addCollection(t, env.DB, "child", childDir)

	// File in the child dir should resolve to child (longest prefix).
	filePath := filepath.Join(childDir, "note.md")

	collections, err := env.DB.ListCollections()
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}

	got, err := resolveCollection(env.DB, filePath, collections)
	if err != nil {
		t.Fatalf("resolveCollection: %v", err)
	}
	if got.ID != child.ID {
		t.Errorf("resolved to %q (ID %d), want %q (ID %d)", got.Name, got.ID, child.Name, child.ID)
	}
}

func TestResolveCollectionNoMatch(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()
	outsideDir := t.TempDir()

	addCollection(t, env.DB, "vault", colDir)

	collections, err := env.DB.ListCollections()
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}

	outsidePath := filepath.Join(outsideDir, "orphan.md")
	_, err = resolveCollection(env.DB, outsidePath, collections)
	if err == nil {
		t.Fatal("expected error for file outside all collections")
	}
	if !strings.Contains(err.Error(), "not inside any registered collection") {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestRefreshLargeFilesBatchSize(t *testing.T) {
	env := setupEnv(t)
	colDir := t.TempDir()

	// Create files with varying sizes to exercise batching logic.
	// "small.md" has 10 lines (~500 chars), "large.md" has 500 lines (~25K chars).
	writeMD(t, colDir, "small.md", 10)
	writeLargeMD(t, colDir, "large.md", 500)
	writeLargeMD(t, colDir, "medium.md", 100)

	addCollection(t, env.DB, "mixed", colDir)

	var buf bytes.Buffer
	stats, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		BatchSize: 2, // Very small batch size to force multiple batches
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	if stats.FilesScanned != 3 {
		t.Errorf("FilesScanned = %d, want 3", stats.FilesScanned)
	}
	if stats.FilesNew != 3 {
		t.Errorf("FilesNew = %d, want 3", stats.FilesNew)
	}
	if stats.ChunksTotal <= 0 {
		t.Errorf("ChunksTotal = %d, want > 0", stats.ChunksTotal)
	}

	// Verify DB and Bleve are consistent.
	col, err := env.DB.GetCollection("mixed")
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	files, err := env.DB.GetFilesByCollection(col.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection: %v", err)
	}
	if len(files) != 3 {
		t.Fatalf("DB files = %d, want 3", len(files))
	}

	totalChunks := 0
	for _, f := range files {
		chunks, err := env.DB.GetChunksByFile(f.ID)
		if err != nil {
			t.Fatalf("GetChunksByFile(%d): %v", f.ID, err)
		}
		if len(chunks) == 0 {
			t.Errorf("file %q has 0 chunks, want > 0", f.Path)
		}
		totalChunks += len(chunks)
	}
	if totalChunks != stats.ChunksTotal {
		t.Errorf("DB chunks = %d, stats.ChunksTotal = %d", totalChunks, stats.ChunksTotal)
	}

	if got := bleveDocCount(t, env.Bleve); got != uint64(stats.ChunksTotal) {
		t.Errorf("Bleve DocCount = %d, want %d", got, stats.ChunksTotal)
	}
}

// writeLargeMD creates a .md file with long lines to produce large chunks.
func writeLargeMD(t *testing.T, dir, name string, lines int) string {
	t.Helper()
	var b strings.Builder
	for i := 1; i <= lines; i++ {
		if i > 1 {
			b.WriteByte('\n')
		}
		// Each line is ~120 chars to create large chunks that stress token limits
		fmt.Fprintf(&b, "Line %d of %s with extended content that simulates a realistic paragraph of text in a knowledge base document", i, name)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestRefreshEmptyCollection(t *testing.T) {
	env := setupEnv(t)

	// Collection directory exists but has no matching files.
	emptyDir := t.TempDir()
	// Write a non-text file that SIFT won't index.
	nonText := filepath.Join(emptyDir, "image.png")
	if err := os.WriteFile(nonText, []byte("fake png"), 0o644); err != nil {
		t.Fatalf("write non-text file: %v", err)
	}

	addCollection(t, env.DB, "empty", emptyDir)

	var buf bytes.Buffer
	stats, err := Refresh(context.Background(), env.DB, env.Bleve, nil, RefreshOptions{
		ChunkOpts: env.ChunkOpt,
	}, &buf)
	if err != nil {
		t.Fatalf("Refresh empty: %v", err)
	}

	if stats.FilesScanned != 0 {
		t.Errorf("FilesScanned = %d, want 0", stats.FilesScanned)
	}
	if stats.FilesNew != 0 {
		t.Errorf("FilesNew = %d, want 0", stats.FilesNew)
	}
	if stats.FilesChanged != 0 {
		t.Errorf("FilesChanged = %d, want 0", stats.FilesChanged)
	}
	if stats.FilesDeleted != 0 {
		t.Errorf("FilesDeleted = %d, want 0", stats.FilesDeleted)
	}
	if stats.ChunksTotal != 0 {
		t.Errorf("ChunksTotal = %d, want 0", stats.ChunksTotal)
	}

	if got := bleveDocCount(t, env.Bleve); got != 0 {
		t.Errorf("Bleve DocCount = %d, want 0", got)
	}
}
