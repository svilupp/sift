package db

import (
	"path/filepath"
	"testing"
)

// openTestDB is a helper that creates a fresh database in a temp directory.
func openTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open(%q): %v", path, err)
	}
	t.Cleanup(func() { d.Close() })

	if err := d.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}
	return d
}

func TestOpenInit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")

	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer d.Close()

	if err := d.Init(); err != nil {
		t.Fatalf("Init: %v", err)
	}

	// Verify all 10 tables exist by querying sqlite_master.
	wantTables := []string{
		"schema_version",
		"collections",
		"files",
		"chunks",
		"embeddings",
		"dead_letters",
		"api_usage",
		"feedback",
		"search_sessions",
		"search_cache",
	}

	for _, table := range wantTables {
		var name string
		err := d.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?", table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}

	// Verify schema version was recorded.
	var version int
	err = d.QueryRow("SELECT version FROM schema_version").Scan(&version)
	if err != nil {
		t.Fatalf("query schema_version: %v", err)
	}
	if version != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", version, SchemaVersion)
	}

	// Init is idempotent; calling again should not error.
	if err := d.Init(); err != nil {
		t.Errorf("second Init: %v", err)
	}
}

func TestCollectionCRUD(t *testing.T) {
	d := openTestDB(t)

	// Add a collection.
	col, err := d.AddCollection("vault", "/home/user/vault", []string{"notes", "personal"})
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}
	if col.ID == 0 {
		t.Error("expected non-zero collection ID")
	}
	if col.Name != "vault" {
		t.Errorf("Name = %q, want %q", col.Name, "vault")
	}
	if col.Path != "/home/user/vault" {
		t.Errorf("Path = %q, want %q", col.Path, "/home/user/vault")
	}
	if len(col.Tags) != 2 || col.Tags[0] != "notes" || col.Tags[1] != "personal" {
		t.Errorf("Tags = %v, want [notes personal]", col.Tags)
	}
	if col.CreatedAt.IsZero() {
		t.Error("CreatedAt should not be zero")
	}

	// List collections.
	cols, err := d.ListCollections()
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(cols) != 1 {
		t.Fatalf("ListCollections returned %d, want 1", len(cols))
	}
	if cols[0].Name != "vault" {
		t.Errorf("listed Name = %q, want %q", cols[0].Name, "vault")
	}
	if len(cols[0].Tags) != 2 {
		t.Errorf("listed Tags = %v, want 2 tags", cols[0].Tags)
	}

	// Get collection by name.
	got, err := d.GetCollection("vault")
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if got == nil {
		t.Fatal("GetCollection returned nil")
	}
	if got.ID != col.ID {
		t.Errorf("GetCollection ID = %d, want %d", got.ID, col.ID)
	}

	// Remove collection.
	if err := d.RemoveCollection("vault"); err != nil {
		t.Fatalf("RemoveCollection: %v", err)
	}

	// Verify removal.
	got, err = d.GetCollection("vault")
	if err != nil {
		t.Fatalf("GetCollection after remove: %v", err)
	}
	if got != nil {
		t.Error("expected nil after RemoveCollection")
	}

	cols, err = d.ListCollections()
	if err != nil {
		t.Fatalf("ListCollections after remove: %v", err)
	}
	if len(cols) != 0 {
		t.Errorf("ListCollections returned %d after remove, want 0", len(cols))
	}
}

func TestCollectionNotFound(t *testing.T) {
	d := openTestDB(t)

	got, err := d.GetCollection("nonexistent")
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for nonexistent collection, got %+v", got)
	}
}

func TestCollectionNilTags(t *testing.T) {
	d := openTestDB(t)

	col, err := d.AddCollection("bare", "/tmp/bare", nil)
	if err != nil {
		t.Fatalf("AddCollection with nil tags: %v", err)
	}
	if col.Tags != nil {
		t.Errorf("Tags = %v, want nil", col.Tags)
	}

	got, err := d.GetCollection("bare")
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if got.Tags != nil {
		t.Errorf("retrieved Tags = %v, want nil", got.Tags)
	}
}

func TestDuplicateCollection(t *testing.T) {
	d := openTestDB(t)

	_, err := d.AddCollection("dupe", "/a", nil)
	if err != nil {
		t.Fatalf("first AddCollection: %v", err)
	}

	_, err = d.AddCollection("dupe", "/b", nil)
	if err == nil {
		t.Fatal("expected error for duplicate collection name, got nil")
	}
}

func TestFileCRUD(t *testing.T) {
	d := openTestDB(t)

	col, err := d.AddCollection("docs", "/docs", nil)
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}

	// Upsert a file.
	fid, err := d.UpsertFile("/docs/readme.md", col.ID, "abc123", 1000, 4096, "")
	if err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	if fid == 0 {
		t.Error("expected non-zero file ID")
	}

	// Get by path.
	f, err := d.GetFileByPath("/docs/readme.md")
	if err != nil {
		t.Fatalf("GetFileByPath: %v", err)
	}
	if f == nil {
		t.Fatal("GetFileByPath returned nil")
	}
	if f.ID != fid {
		t.Errorf("file ID = %d, want %d", f.ID, fid)
	}
	if f.Path != "/docs/readme.md" {
		t.Errorf("Path = %q, want %q", f.Path, "/docs/readme.md")
	}
	if f.CollectionID != col.ID {
		t.Errorf("CollectionID = %d, want %d", f.CollectionID, col.ID)
	}
	if f.FileHash != "abc123" {
		t.Errorf("FileHash = %q, want %q", f.FileHash, "abc123")
	}
	if f.Mtime != 1000 {
		t.Errorf("Mtime = %d, want 1000", f.Mtime)
	}
	if f.SizeBytes != 4096 {
		t.Errorf("SizeBytes = %d, want 4096", f.SizeBytes)
	}

	// Get by collection.
	files, err := d.GetFilesByCollection(col.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("GetFilesByCollection returned %d, want 1", len(files))
	}
	if files[0].ID != fid {
		t.Errorf("files[0].ID = %d, want %d", files[0].ID, fid)
	}

	// File not found returns nil, nil.
	f, err = d.GetFileByPath("/nonexistent")
	if err != nil {
		t.Fatalf("GetFileByPath nonexistent: %v", err)
	}
	if f != nil {
		t.Error("expected nil for nonexistent file")
	}
}

func TestFileUpsert(t *testing.T) {
	d := openTestDB(t)

	col, err := d.AddCollection("proj", "/proj", nil)
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}

	// Insert initial file.
	fid1, err := d.UpsertFile("/proj/main.go", col.ID, "hash1", 100, 1024, "")
	if err != nil {
		t.Fatalf("UpsertFile (insert): %v", err)
	}

	// Upsert same path with updated hash and mtime.
	fid2, err := d.UpsertFile("/proj/main.go", col.ID, "hash2", 200, 2048, "")
	if err != nil {
		t.Fatalf("UpsertFile (update): %v", err)
	}

	if fid2 != fid1 {
		t.Errorf("upsert file ID changed: got %d, want %d", fid2, fid1)
	}

	// Verify updated values.
	f, err := d.GetFileByPath("/proj/main.go")
	if err != nil {
		t.Fatalf("GetFileByPath: %v", err)
	}
	if f.FileHash != "hash2" {
		t.Errorf("FileHash = %q, want %q", f.FileHash, "hash2")
	}
	if f.Mtime != 200 {
		t.Errorf("Mtime = %d, want 200", f.Mtime)
	}
	if f.SizeBytes != 2048 {
		t.Errorf("SizeBytes = %d, want 2048", f.SizeBytes)
	}
}

func TestChunkCRUD(t *testing.T) {
	d := openTestDB(t)

	col, err := d.AddCollection("c", "/c", nil)
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}
	fid, err := d.UpsertFile("/c/f.md", col.ID, "h", 1, 100, "")
	if err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}

	// Insert chunks in non-sequential order to verify ORDER BY.
	type chunk struct {
		order     int
		startLine int
		endLine   int
		charCount int
	}
	chunks := []chunk{
		{order: 2, startLine: 11, endLine: 20, charCount: 200},
		{order: 0, startLine: 1, endLine: 5, charCount: 100},
		{order: 1, startLine: 6, endLine: 10, charCount: 150},
	}

	for _, c := range chunks {
		cid, err := d.InsertChunk(fid, c.order, c.startLine, c.endLine, c.charCount)
		if err != nil {
			t.Fatalf("InsertChunk(order=%d): %v", c.order, err)
		}
		if cid == 0 {
			t.Errorf("expected non-zero chunk ID for order=%d", c.order)
		}
	}

	// Retrieve and verify ordering.
	got, err := d.GetChunksByFile(fid)
	if err != nil {
		t.Fatalf("GetChunksByFile: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("GetChunksByFile returned %d chunks, want 3", len(got))
	}

	// Verify they come back ordered 0, 1, 2.
	for i, c := range got {
		if c.Order != i {
			t.Errorf("chunk[%d].Order = %d, want %d", i, c.Order, i)
		}
		if c.FileID != fid {
			t.Errorf("chunk[%d].FileID = %d, want %d", i, c.FileID, fid)
		}
	}

	// Verify specific fields for order=0 (second insert, but first in result).
	if got[0].StartLine != 1 || got[0].EndLine != 5 || got[0].CharCount != 100 {
		t.Errorf("chunk[0] fields mismatch: start=%d end=%d chars=%d",
			got[0].StartLine, got[0].EndLine, got[0].CharCount)
	}

	// DeleteChunksByFile.
	if err := d.DeleteChunksByFile(fid); err != nil {
		t.Fatalf("DeleteChunksByFile: %v", err)
	}
	got, err = d.GetChunksByFile(fid)
	if err != nil {
		t.Fatalf("GetChunksByFile after delete: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("expected 0 chunks after delete, got %d", len(got))
	}
}

func TestCascadeDelete(t *testing.T) {
	d := openTestDB(t)

	col, err := d.AddCollection("cascade", "/cascade", nil)
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}

	// Create files.
	fid1, err := d.UpsertFile("/cascade/a.md", col.ID, "h1", 1, 100, "")
	if err != nil {
		t.Fatalf("UpsertFile a: %v", err)
	}
	fid2, err := d.UpsertFile("/cascade/b.md", col.ID, "h2", 2, 200, "")
	if err != nil {
		t.Fatalf("UpsertFile b: %v", err)
	}

	// Create chunks.
	cid1, err := d.InsertChunk(fid1, 0, 1, 10, 50)
	if err != nil {
		t.Fatalf("InsertChunk a: %v", err)
	}
	_, err = d.InsertChunk(fid2, 0, 1, 5, 30)
	if err != nil {
		t.Fatalf("InsertChunk b: %v", err)
	}

	// Insert an embedding for the first chunk.
	_, err = d.conn.Exec(
		"INSERT INTO embeddings (chunk_id, vector, model, dimensions) VALUES (?, ?, ?, ?)",
		cid1, []byte{0x01, 0x02}, "test-model", 2,
	)
	if err != nil {
		t.Fatalf("insert embedding: %v", err)
	}

	// Verify data exists before cascade.
	files, err := d.GetFilesByCollection(col.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection: %v", err)
	}
	if len(files) != 2 {
		t.Fatalf("expected 2 files, got %d", len(files))
	}

	// RemoveCollection should cascade to files, chunks, embeddings.
	if err := d.RemoveCollection("cascade"); err != nil {
		t.Fatalf("RemoveCollection: %v", err)
	}

	// Files should be gone.
	files, err = d.GetFilesByCollection(col.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection after remove: %v", err)
	}
	if len(files) != 0 {
		t.Errorf("expected 0 files after cascade, got %d", len(files))
	}

	// Chunks should be gone.
	chunks, err := d.GetChunksByFile(fid1)
	if err != nil {
		t.Fatalf("GetChunksByFile after remove: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks after cascade, got %d", len(chunks))
	}

	// Embeddings should be gone.
	var embCount int
	err = d.QueryRow("SELECT COUNT(*) FROM embeddings WHERE chunk_id = ?", cid1).Scan(&embCount)
	if err != nil {
		t.Fatalf("count embeddings: %v", err)
	}
	if embCount != 0 {
		t.Errorf("expected 0 embeddings after cascade, got %d", embCount)
	}
}

func TestDeleteFile(t *testing.T) {
	d := openTestDB(t)

	col, err := d.AddCollection("df", "/df", nil)
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}

	fid, err := d.UpsertFile("/df/x.md", col.ID, "hx", 1, 50, "")
	if err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}

	// Insert chunks.
	_, err = d.InsertChunk(fid, 0, 1, 5, 20)
	if err != nil {
		t.Fatalf("InsertChunk 0: %v", err)
	}
	_, err = d.InsertChunk(fid, 1, 6, 10, 30)
	if err != nil {
		t.Fatalf("InsertChunk 1: %v", err)
	}

	// Delete the file.
	if err := d.DeleteFile(fid); err != nil {
		t.Fatalf("DeleteFile: %v", err)
	}

	// File should be gone.
	f, err := d.GetFileByPath("/df/x.md")
	if err != nil {
		t.Fatalf("GetFileByPath after delete: %v", err)
	}
	if f != nil {
		t.Error("expected nil file after DeleteFile")
	}

	// Chunks should be cascade-deleted.
	chunks, err := d.GetChunksByFile(fid)
	if err != nil {
		t.Fatalf("GetChunksByFile after delete: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("expected 0 chunks after cascade delete, got %d", len(chunks))
	}
}

func TestCollectionCounts(t *testing.T) {
	d := openTestDB(t)

	col, err := d.AddCollection("counts", "/counts", nil)
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}

	// Initially zero.
	fc, err := d.CollectionFileCount(col.ID)
	if err != nil {
		t.Fatalf("CollectionFileCount (empty): %v", err)
	}
	if fc != 0 {
		t.Errorf("file count = %d, want 0", fc)
	}

	cc, err := d.CollectionChunkCount(col.ID)
	if err != nil {
		t.Fatalf("CollectionChunkCount (empty): %v", err)
	}
	if cc != 0 {
		t.Errorf("chunk count = %d, want 0", cc)
	}

	// Add files and set chunk counts.
	fid1, err := d.UpsertFile("/counts/a.md", col.ID, "h1", 1, 100, "")
	if err != nil {
		t.Fatalf("UpsertFile a: %v", err)
	}
	fid2, err := d.UpsertFile("/counts/b.md", col.ID, "h2", 2, 200, "")
	if err != nil {
		t.Fatalf("UpsertFile b: %v", err)
	}

	if err := d.UpdateFileChunkCount(fid1, 3); err != nil {
		t.Fatalf("UpdateFileChunkCount fid1: %v", err)
	}
	if err := d.UpdateFileChunkCount(fid2, 5); err != nil {
		t.Fatalf("UpdateFileChunkCount fid2: %v", err)
	}

	fc, err = d.CollectionFileCount(col.ID)
	if err != nil {
		t.Fatalf("CollectionFileCount: %v", err)
	}
	if fc != 2 {
		t.Errorf("file count = %d, want 2", fc)
	}

	cc, err = d.CollectionChunkCount(col.ID)
	if err != nil {
		t.Fatalf("CollectionChunkCount: %v", err)
	}
	if cc != 8 {
		t.Errorf("chunk count = %d, want 8 (3+5)", cc)
	}
}

func TestUpdateFileChunkCount(t *testing.T) {
	d := openTestDB(t)

	col, err := d.AddCollection("uc", "/uc", nil)
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}

	fid, err := d.UpsertFile("/uc/f.md", col.ID, "h", 1, 50, "")
	if err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}

	// Initially 0.
	f, err := d.GetFileByPath("/uc/f.md")
	if err != nil {
		t.Fatalf("GetFileByPath: %v", err)
	}
	if f.ChunkCount != 0 {
		t.Errorf("initial ChunkCount = %d, want 0", f.ChunkCount)
	}

	// Update.
	if err := d.UpdateFileChunkCount(fid, 7); err != nil {
		t.Fatalf("UpdateFileChunkCount: %v", err)
	}

	f, err = d.GetFileByPath("/uc/f.md")
	if err != nil {
		t.Fatalf("GetFileByPath after update: %v", err)
	}
	if f.ChunkCount != 7 {
		t.Errorf("ChunkCount = %d, want 7", f.ChunkCount)
	}
}

func TestListCollectionsOrdering(t *testing.T) {
	d := openTestDB(t)

	// Add collections in non-alphabetical order.
	names := []string{"zebra", "alpha", "middle"}
	for _, n := range names {
		_, err := d.AddCollection(n, "/"+n, nil)
		if err != nil {
			t.Fatalf("AddCollection(%q): %v", n, err)
		}
	}

	cols, err := d.ListCollections()
	if err != nil {
		t.Fatalf("ListCollections: %v", err)
	}
	if len(cols) != 3 {
		t.Fatalf("ListCollections returned %d, want 3", len(cols))
	}

	// Verify ORDER BY name.
	want := []string{"alpha", "middle", "zebra"}
	for i, w := range want {
		if cols[i].Name != w {
			t.Errorf("cols[%d].Name = %q, want %q", i, cols[i].Name, w)
		}
	}
}

func TestRemoveCollectionNotFound(t *testing.T) {
	d := openTestDB(t)

	err := d.RemoveCollection("ghost")
	if err == nil {
		t.Fatal("expected error when removing non-existent collection, got nil")
	}
}

func TestGetFilesByCollectionEmpty(t *testing.T) {
	d := openTestDB(t)

	col, err := d.AddCollection("empty", "/empty", nil)
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}

	files, err := d.GetFilesByCollection(col.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection: %v", err)
	}
	if files != nil {
		t.Errorf("expected nil for empty collection, got %v", files)
	}
}

func TestMultipleCollectionsIsolation(t *testing.T) {
	d := openTestDB(t)

	col1, err := d.AddCollection("c1", "/c1", nil)
	if err != nil {
		t.Fatalf("AddCollection c1: %v", err)
	}
	col2, err := d.AddCollection("c2", "/c2", nil)
	if err != nil {
		t.Fatalf("AddCollection c2: %v", err)
	}

	// Add files to each collection.
	_, err = d.UpsertFile("/c1/a.md", col1.ID, "h1", 1, 100, "")
	if err != nil {
		t.Fatalf("UpsertFile c1: %v", err)
	}
	_, err = d.UpsertFile("/c2/b.md", col2.ID, "h2", 2, 200, "")
	if err != nil {
		t.Fatalf("UpsertFile c2: %v", err)
	}
	_, err = d.UpsertFile("/c2/c.md", col2.ID, "h3", 3, 300, "")
	if err != nil {
		t.Fatalf("UpsertFile c2 second: %v", err)
	}

	// Verify isolation.
	f1, err := d.GetFilesByCollection(col1.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection c1: %v", err)
	}
	if len(f1) != 1 {
		t.Errorf("c1 file count = %d, want 1", len(f1))
	}

	f2, err := d.GetFilesByCollection(col2.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection c2: %v", err)
	}
	if len(f2) != 2 {
		t.Errorf("c2 file count = %d, want 2", len(f2))
	}

	// Removing c1 should not affect c2.
	if err := d.RemoveCollection("c1"); err != nil {
		t.Fatalf("RemoveCollection c1: %v", err)
	}

	f2, err = d.GetFilesByCollection(col2.ID)
	if err != nil {
		t.Fatalf("GetFilesByCollection c2 after remove c1: %v", err)
	}
	if len(f2) != 2 {
		t.Errorf("c2 file count after c1 removal = %d, want 2", len(f2))
	}
}

func TestGetChunkIDsByPathGlob(t *testing.T) {
	d := openTestDB(t)

	col, err := d.AddCollection("vault", "/vault", nil)
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}

	// Create files in different subdirectories.
	paths := []string{
		"/vault/topics/work/api.md",
		"/vault/topics/work/auth.md",
		"/vault/topics/personal/journal.md",
		"/vault/daily/2024-01-01.md",
		"/vault/pinned/work.md",
	}
	for _, p := range paths {
		fid, err := d.UpsertFile(p, col.ID, "hash", 1000, 100, "")
		if err != nil {
			t.Fatalf("UpsertFile(%s): %v", p, err)
		}
		// Insert one chunk per file.
		_, err = d.InsertChunk(fid, 0, 1, 10, 50)
		if err != nil {
			t.Fatalf("InsertChunk for %s: %v", p, err)
		}
	}

	tests := []struct {
		name    string
		pattern string
		want    int
	}{
		{"all files", "*", 5},
		{"work scope", "*/topics/work/*", 2},
		{"personal scope", "*/topics/personal/*", 1},
		{"daily scope", "*/daily/*", 1},
		{"pinned files", "*/pinned/*.md", 1},
		{"all topics", "*/topics/*", 3},
		{"no match", "*/nonexistent/*", 0},
		{"specific file", "/vault/daily/2024-01-01.md", 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ids, err := d.GetChunkIDsByPathGlob(tt.pattern, 0, 0)
			if err != nil {
				t.Fatalf("GetChunkIDsByPathGlob(%q): %v", tt.pattern, err)
			}
			if len(ids) != tt.want {
				t.Errorf("GetChunkIDsByPathGlob(%q) = %d chunks, want %d", tt.pattern, len(ids), tt.want)
			}
		})
	}
}

func TestGetChunkIDsByPathGlobWithCollectionFilter(t *testing.T) {
	d := openTestDB(t)

	col1, err := d.AddCollection("vault", "/vault", nil)
	if err != nil {
		t.Fatalf("AddCollection vault: %v", err)
	}
	col2, err := d.AddCollection("docs", "/docs", nil)
	if err != nil {
		t.Fatalf("AddCollection docs: %v", err)
	}

	// File in vault.
	fid1, err := d.UpsertFile("/vault/topics/work/api.md", col1.ID, "h1", 1000, 100, "")
	if err != nil {
		t.Fatalf("UpsertFile vault: %v", err)
	}
	_, err = d.InsertChunk(fid1, 0, 1, 10, 50)
	if err != nil {
		t.Fatalf("InsertChunk vault: %v", err)
	}

	// File in docs with similar path structure.
	fid2, err := d.UpsertFile("/docs/topics/work/readme.md", col2.ID, "h2", 1000, 100, "")
	if err != nil {
		t.Fatalf("UpsertFile docs: %v", err)
	}
	_, err = d.InsertChunk(fid2, 0, 1, 10, 50)
	if err != nil {
		t.Fatalf("InsertChunk docs: %v", err)
	}

	// Without collection filter, both match.
	ids, err := d.GetChunkIDsByPathGlob("*/topics/work/*", 0, 0)
	if err != nil {
		t.Fatalf("GetChunkIDsByPathGlob (no filter): %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 chunks without collection filter, got %d", len(ids))
	}

	// With vault collection filter, only one matches.
	ids, err = d.GetChunkIDsByPathGlob("*/topics/work/*", col1.ID, 0)
	if err != nil {
		t.Fatalf("GetChunkIDsByPathGlob (vault filter): %v", err)
	}
	if len(ids) != 1 {
		t.Errorf("expected 1 chunk with vault filter, got %d", len(ids))
	}
}

func TestGetFilteredEmbeddingsWithPathGlob(t *testing.T) {
	d := openTestDB(t)

	col, err := d.AddCollection("vault", "/vault", nil)
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}

	// Create two files.
	fid1, err := d.UpsertFile("/vault/topics/work/api.md", col.ID, "h1", 1000, 100, "")
	if err != nil {
		t.Fatalf("UpsertFile work: %v", err)
	}
	cid1, err := d.InsertChunk(fid1, 0, 1, 10, 50)
	if err != nil {
		t.Fatalf("InsertChunk work: %v", err)
	}
	if err := d.UpsertEmbedding(cid1, []byte{0x01, 0x02, 0x03, 0x04}, "test", 2); err != nil {
		t.Fatalf("UpsertEmbedding work: %v", err)
	}

	fid2, err := d.UpsertFile("/vault/topics/personal/journal.md", col.ID, "h2", 1000, 100, "")
	if err != nil {
		t.Fatalf("UpsertFile personal: %v", err)
	}
	cid2, err := d.InsertChunk(fid2, 0, 1, 10, 50)
	if err != nil {
		t.Fatalf("InsertChunk personal: %v", err)
	}
	if err := d.UpsertEmbedding(cid2, []byte{0x05, 0x06, 0x07, 0x08}, "test", 2); err != nil {
		t.Fatalf("UpsertEmbedding personal: %v", err)
	}

	// No path filter: both returned.
	embs, err := d.GetFilteredEmbeddings(0, 0, "")
	if err != nil {
		t.Fatalf("GetFilteredEmbeddings (no filter): %v", err)
	}
	if len(embs) != 2 {
		t.Errorf("expected 2 embeddings without path filter, got %d", len(embs))
	}

	// Path filter for work: only one.
	embs, err = d.GetFilteredEmbeddings(0, 0, "*/topics/work/*")
	if err != nil {
		t.Fatalf("GetFilteredEmbeddings (work): %v", err)
	}
	if len(embs) != 1 {
		t.Errorf("expected 1 embedding for work path, got %d", len(embs))
	}
	if len(embs) > 0 && embs[0].ChunkID != cid1 {
		t.Errorf("expected chunk ID %d, got %d", cid1, embs[0].ChunkID)
	}

	// Path filter with no match.
	embs, err = d.GetFilteredEmbeddings(0, 0, "*/nonexistent/*")
	if err != nil {
		t.Fatalf("GetFilteredEmbeddings (no match): %v", err)
	}
	if len(embs) != 0 {
		t.Errorf("expected 0 embeddings for non-matching path, got %d", len(embs))
	}
}
