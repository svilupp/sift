package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// execer is the common interface between *sql.DB and *sql.Tx.
type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// DB wraps a SQLite database connection.
type DB struct {
	conn *sql.DB
}

// Tx wraps an active database transaction.
type Tx struct {
	tx *sql.Tx
}

// Collection represents a registered collection.
type Collection struct {
	ID        int64
	Name      string
	Path      string
	Tags      []string
	CreatedAt time.Time
}

// FileRecord represents an indexed file.
type FileRecord struct {
	ID           int64
	Path         string
	CollectionID int64
	FileHash     string
	Mtime        int64
	SizeBytes    int64
	LastIndexed  int64
	ChunkCount   int
	Title        string
}

// ChunkRecord represents a stored chunk.
type ChunkRecord struct {
	ID            int64
	FileID        int64
	Order         int
	StartLine     int
	EndLine       int
	CharCount     int
	CreatedAt     int64
	SectionID     string // anchor slug e.g. "trust-zones"
	Heading       string // raw heading text e.g. "Trust Zones"
	HeadingLevel  int    // 1-6 for H1-H6, 0 for preamble
	ChunkHash     string // xxhash64 of chunk content
	ParentChunkID *int64 // FK to parent section chunk (nullable)
}

// LinkRecord represents a stored link between files/sections.
type LinkRecord struct {
	ID              int64
	SourceFileID    int64
	SourceSectionID string
	SourceLine      int
	TargetPath      string
	TargetSection   string
	LinkType        string
	Raw             string
}

// Open opens or creates the SQLite database at the given path.
func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", "file:"+path+"?_pragma=busy_timeout%3d5000&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	for attempt := 0; attempt < 10; attempt++ {
		if err := sqlDB.Ping(); err != nil {
			if attempt < 9 && strings.Contains(err.Error(), "SQLITE_BUSY") {
				time.Sleep(25 * time.Millisecond)
				continue
			}
			sqlDB.Close()
			return nil, fmt.Errorf("ping db: %w", err)
		}
		break
	}

	return &DB{conn: sqlDB}, nil
}

// Close closes the database connection.
func (db *DB) Close() error {
	return db.conn.Close()
}

// QueryRow executes a query that returns at most one row.
func (db *DB) QueryRow(query string, args ...any) *sql.Row {
	return db.conn.QueryRow(query, args...)
}

// WithTx runs fn inside a transaction. Commits on nil error, rolls back otherwise.
func (db *DB) WithTx(fn func(*Tx) error) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if err := fn(&Tx{tx: tx}); err != nil {
		return err
	}
	return tx.Commit()
}

// Init creates all tables and sets the schema version.
func (db *DB) Init() error {
	if _, err := db.conn.Exec(schemaSQL); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	// Migration: add title column to files if not present (v1 -> v2).
	db.conn.Exec("ALTER TABLE files ADD COLUMN title TEXT DEFAULT ''") //nolint:errcheck // idempotent

	// Migration v2 -> v3: add search_cache table (idempotent via IF NOT EXISTS in schemaSQL).
	// The CREATE TABLE in schemaSQL handles this, but ensure the index exists for older DBs.
	db.conn.Exec(`CREATE INDEX IF NOT EXISTS idx_search_cache_created ON search_cache(created_at)`) //nolint:errcheck // idempotent

	// Migration v3 -> v5: section-aware chunks
	db.conn.Exec("ALTER TABLE chunks ADD COLUMN section_id TEXT DEFAULT ''")           //nolint:errcheck // idempotent
	db.conn.Exec("ALTER TABLE chunks ADD COLUMN heading TEXT DEFAULT ''")              //nolint:errcheck // idempotent
	db.conn.Exec("ALTER TABLE chunks ADD COLUMN heading_level INTEGER DEFAULT 0")      //nolint:errcheck // idempotent
	db.conn.Exec("ALTER TABLE chunks ADD COLUMN chunk_hash TEXT DEFAULT ''")           //nolint:errcheck // idempotent
	db.conn.Exec("ALTER TABLE chunks ADD COLUMN parent_chunk_id INTEGER DEFAULT NULL") //nolint:errcheck // idempotent

	// Migration v8 -> v9: add kind discriminator to dead_letters so the
	// table can host both `embed` and `index_summary` failures.
	db.conn.Exec("ALTER TABLE dead_letters ADD COLUMN kind TEXT NOT NULL DEFAULT 'embed'")              //nolint:errcheck // idempotent
	db.conn.Exec("CREATE INDEX IF NOT EXISTS idx_dead_letters_kind ON dead_letters(kind, resolved_at)") //nolint:errcheck // idempotent
	_, _ = db.conn.Exec(`CREATE TABLE IF NOT EXISTS file_collections (
		file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
		collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
		PRIMARY KEY (file_id, collection_id)
	)`)
	db.conn.Exec("CREATE INDEX IF NOT EXISTS idx_file_collections_collection ON file_collections(collection_id, file_id)") //nolint:errcheck // idempotent
	db.conn.Exec("CREATE INDEX IF NOT EXISTS idx_file_collections_file ON file_collections(file_id, collection_id)")       //nolint:errcheck // idempotent
	_, _ = db.conn.Exec(`
		INSERT OR IGNORE INTO file_collections (file_id, collection_id)
		SELECT id, collection_id
		FROM files
		WHERE collection_id IS NOT NULL
	`)

	var version int
	err := db.conn.QueryRow("SELECT version FROM schema_version WHERE version = ? LIMIT 1", SchemaVersion).Scan(&version)
	switch {
	case err == nil:
		return nil
	case !errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("check schema version: %w", err)
	}

	_, err = db.conn.Exec(
		"INSERT INTO schema_version (version, applied_at) VALUES (?, ?)",
		SchemaVersion, time.Now().Unix(),
	)
	if err != nil {
		return fmt.Errorf("set schema version: %w", err)
	}

	return nil
}

// AddCollection inserts a new collection.
func (db *DB) AddCollection(name, path string, tags []string) (*Collection, error) {
	var tagsJSON *string
	if len(tags) > 0 {
		b, err := json.Marshal(tags)
		if err != nil {
			return nil, fmt.Errorf("marshal tags: %w", err)
		}
		s := string(b)
		tagsJSON = &s
	}

	now := time.Now().Unix()
	res, err := db.conn.Exec(
		"INSERT INTO collections (name, path, tags, created_at) VALUES (?, ?, ?, ?)",
		name, path, tagsJSON, now,
	)
	if err != nil {
		return nil, fmt.Errorf("insert collection: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("get collection id: %w", err)
	}
	return &Collection{
		ID:        id,
		Name:      name,
		Path:      path,
		Tags:      tags,
		CreatedAt: time.Unix(now, 0),
	}, nil
}

// ListCollections returns all registered collections.
func (db *DB) ListCollections() ([]Collection, error) {
	return listCollections(db.conn)
}

func listCollections(ex execer) ([]Collection, error) {
	rows, err := ex.Query("SELECT id, name, path, tags, created_at FROM collections ORDER BY name")
	if err != nil {
		return nil, fmt.Errorf("query collections: %w", err)
	}
	defer rows.Close()

	var cols []Collection
	for rows.Next() {
		var c Collection
		var tagsJSON sql.NullString
		var createdAt int64
		if err := rows.Scan(&c.ID, &c.Name, &c.Path, &tagsJSON, &createdAt); err != nil {
			return nil, fmt.Errorf("scan collection: %w", err)
		}
		c.CreatedAt = time.Unix(createdAt, 0)
		if tagsJSON.Valid {
			if err := json.Unmarshal([]byte(tagsJSON.String), &c.Tags); err != nil {
				return nil, fmt.Errorf("unmarshal tags: %w", err)
			}
		}
		cols = append(cols, c)
	}
	return cols, rows.Err()
}

// GetCollection returns a collection by name.
func (db *DB) GetCollection(name string) (*Collection, error) {
	return getCollectionByName(db.conn, name)
}

func getCollectionByName(ex execer, name string) (*Collection, error) {
	var c Collection
	var tagsJSON sql.NullString
	var createdAt int64
	err := ex.QueryRow(
		"SELECT id, name, path, tags, created_at FROM collections WHERE name = ?",
		name,
	).Scan(&c.ID, &c.Name, &c.Path, &tagsJSON, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get collection: %w", err)
	}
	c.CreatedAt = time.Unix(createdAt, 0)
	if tagsJSON.Valid {
		if err := json.Unmarshal([]byte(tagsJSON.String), &c.Tags); err != nil {
			return nil, fmt.Errorf("unmarshal tags: %w", err)
		}
	}
	return &c, nil
}

// GetCollectionByID returns a collection by ID.
func (db *DB) GetCollectionByID(id int64) (*Collection, error) {
	var c Collection
	var tagsJSON sql.NullString
	var createdAt int64
	err := db.conn.QueryRow(
		"SELECT id, name, path, tags, created_at FROM collections WHERE id = ?",
		id,
	).Scan(&c.ID, &c.Name, &c.Path, &tagsJSON, &createdAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get collection by id: %w", err)
	}
	c.CreatedAt = time.Unix(createdAt, 0)
	if tagsJSON.Valid {
		if err := json.Unmarshal([]byte(tagsJSON.String), &c.Tags); err != nil {
			return nil, fmt.Errorf("unmarshal tags: %w", err)
		}
	}
	return &c, nil
}

// RemoveCollection deletes a collection membership and any files orphaned by that removal.
func (db *DB) RemoveCollection(name string) error {
	tx, err := db.conn.Begin()
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var id int64
	err = tx.QueryRow("SELECT id FROM collections WHERE name = ?", name).Scan(&id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("collection %q not found", name)
		}
		return fmt.Errorf("find collection: %w", err)
	}

	rows, err := tx.Query("SELECT DISTINCT file_id FROM file_collections WHERE collection_id = ? ORDER BY file_id", id)
	if err != nil {
		return fmt.Errorf("list affected files: %w", err)
	}
	var affectedFileIDs []int64
	for rows.Next() {
		var fileID int64
		if scanErr := rows.Scan(&fileID); scanErr != nil {
			rows.Close()
			return fmt.Errorf("scan affected file id: %w", scanErr)
		}
		affectedFileIDs = append(affectedFileIDs, fileID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("iterate affected files: %w", err)
	}
	rows.Close()

	if _, err := tx.Exec("DELETE FROM file_collections WHERE collection_id = ?", id); err != nil {
		return fmt.Errorf("delete file memberships: %w", err)
	}

	var orphanFileIDs []int64
	for _, fileID := range affectedFileIDs {
		primaryID, selErr := selectPrimaryCollectionID(tx, fileID)
		switch {
		case errors.Is(selErr, sql.ErrNoRows):
			orphanFileIDs = append(orphanFileIDs, fileID)
		case selErr != nil:
			return fmt.Errorf("select new primary for file %d: %w", fileID, selErr)
		default:
			if _, err := tx.Exec("UPDATE files SET collection_id = ? WHERE id = ?", primaryID, fileID); err != nil {
				return fmt.Errorf("update primary collection for file %d: %w", fileID, err)
			}
		}
	}

	if err := deleteFilesByID(tx, orphanFileIDs); err != nil {
		return fmt.Errorf("delete orphan files: %w", err)
	}

	if _, err := tx.Exec("DELETE FROM collections WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete collection: %w", err)
	}

	return tx.Commit()
}

func selectPrimaryCollectionID(ex execer, fileID int64) (int64, error) {
	var collectionID int64
	err := ex.QueryRow(`
		SELECT fc.collection_id
		FROM file_collections fc
		JOIN collections c ON c.id = fc.collection_id
		WHERE fc.file_id = ?
		ORDER BY LENGTH(c.path) DESC, c.name ASC, c.id ASC
		LIMIT 1
	`, fileID).Scan(&collectionID)
	if err != nil {
		return 0, err
	}
	return collectionID, nil
}

func deleteFilesByID(ex execer, fileIDs []int64) error {
	if len(fileIDs) == 0 {
		return nil
	}

	placeholders := make([]string, len(fileIDs))
	args := make([]any, len(fileIDs))
	for i, fileID := range fileIDs {
		placeholders[i] = "?"
		args[i] = fileID
	}

	if _, err := ex.Exec("DELETE FROM files WHERE id IN ("+strings.Join(placeholders, ",")+")", args...); err != nil {
		return fmt.Errorf("delete files by id: %w", err)
	}
	return nil
}

// UpsertFile inserts or updates a file record, returning the file ID.
func (db *DB) UpsertFile(path string, collectionID int64, hash string, mtime, sizeBytes int64, title string) (int64, error) {
	return upsertFile(db.conn, path, collectionID, hash, mtime, sizeBytes, title)
}

// UpsertFile inserts or updates a file record within a transaction.
func (t *Tx) UpsertFile(path string, collectionID int64, hash string, mtime, sizeBytes int64, title string) (int64, error) {
	return upsertFile(t.tx, path, collectionID, hash, mtime, sizeBytes, title)
}

func upsertFile(ex execer, path string, collectionID int64, hash string, mtime, sizeBytes int64, title string) (int64, error) {
	now := time.Now().Unix()
	_, err := ex.Exec(`
		INSERT INTO files (path, collection_id, file_hash, mtime, size_bytes, last_indexed, chunk_count, title)
		VALUES (?, ?, ?, ?, ?, ?, 0, ?)
		ON CONFLICT(path) DO UPDATE SET
			collection_id = excluded.collection_id,
			file_hash = excluded.file_hash,
			mtime = excluded.mtime,
			size_bytes = excluded.size_bytes,
			last_indexed = ?,
			title = excluded.title
	`, path, collectionID, hash, mtime, sizeBytes, now, title, now)
	if err != nil {
		return 0, fmt.Errorf("upsert file: %w", err)
	}

	// Always query by path. LastInsertId is undefined after ON CONFLICT DO UPDATE
	// in SQLite — it may return a stale value from a previous INSERT.
	var id int64
	err = ex.QueryRow("SELECT id FROM files WHERE path = ?", path).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("get file id: %w", err)
	}
	if err := ensureFileCollection(ex, id, collectionID); err != nil {
		return 0, err
	}
	return id, nil
}

func ensureFileCollection(ex execer, fileID, collectionID int64) error {
	if collectionID <= 0 {
		return nil
	}
	if _, err := ex.Exec(
		"INSERT OR IGNORE INTO file_collections (file_id, collection_id) VALUES (?, ?)",
		fileID, collectionID,
	); err != nil {
		return fmt.Errorf("ensure file collection: %w", err)
	}
	return nil
}

// DeleteChunksByFile removes all chunks for a file.
func (db *DB) DeleteChunksByFile(fileID int64) error {
	return deleteChunksByFile(db.conn, fileID)
}

// DeleteChunksByFile removes all chunks for a file within a transaction.
func (t *Tx) DeleteChunksByFile(fileID int64) error {
	return deleteChunksByFile(t.tx, fileID)
}

func deleteChunksByFile(ex execer, fileID int64) error {
	_, err := ex.Exec("DELETE FROM chunks WHERE file_id = ?", fileID)
	if err != nil {
		return fmt.Errorf("delete chunks by file: %w", err)
	}
	return nil
}

// InsertChunk inserts a chunk record and returns its ID.
func (db *DB) InsertChunk(fileID int64, order, startLine, endLine, charCount int) (int64, error) {
	return insertChunk(db.conn, fileID, order, startLine, endLine, charCount)
}

// InsertChunk inserts a chunk record within a transaction.
func (t *Tx) InsertChunk(fileID int64, order, startLine, endLine, charCount int) (int64, error) {
	return insertChunk(t.tx, fileID, order, startLine, endLine, charCount)
}

func insertChunk(ex execer, fileID int64, order, startLine, endLine, charCount int) (int64, error) {
	now := time.Now().Unix()
	res, err := ex.Exec(
		"INSERT INTO chunks (file_id, chunk_order, start_line, end_line, char_count, created_at) VALUES (?, ?, ?, ?, ?, ?)",
		fileID, order, startLine, endLine, charCount, now,
	)
	if err != nil {
		return 0, fmt.Errorf("insert chunk: %w", err)
	}
	return res.LastInsertId()
}

// UpdateFileChunkCount updates the chunk_count for a file.
func (db *DB) UpdateFileChunkCount(fileID int64, count int) error {
	return updateFileChunkCount(db.conn, fileID, count)
}

// UpdateFileChunkCount updates the chunk_count within a transaction.
func (t *Tx) UpdateFileChunkCount(fileID int64, count int) error {
	return updateFileChunkCount(t.tx, fileID, count)
}

func updateFileChunkCount(ex execer, fileID int64, count int) error {
	_, err := ex.Exec("UPDATE files SET chunk_count = ? WHERE id = ?", count, fileID)
	if err != nil {
		return fmt.Errorf("update file chunk count: %w", err)
	}
	return nil
}

// UpdateFilePrimaryCollection updates the canonical collection for a file.
func (db *DB) UpdateFilePrimaryCollection(fileID int64, collectionID int64) error {
	return updateFilePrimaryCollection(db.conn, fileID, collectionID)
}

// UpdateFilePrimaryCollection updates the canonical collection for a file within a transaction.
func (t *Tx) UpdateFilePrimaryCollection(fileID int64, collectionID int64) error {
	return updateFilePrimaryCollection(t.tx, fileID, collectionID)
}

func updateFilePrimaryCollection(ex execer, fileID int64, collectionID int64) error {
	if _, err := ex.Exec("UPDATE files SET collection_id = ? WHERE id = ?", collectionID, fileID); err != nil {
		return fmt.Errorf("update file primary collection: %w", err)
	}
	return nil
}

// GetFileByPath returns a file record by path.
func (db *DB) GetFileByPath(path string) (*FileRecord, error) {
	var f FileRecord
	var hash sql.NullString
	err := db.conn.QueryRow(
		"SELECT id, path, collection_id, file_hash, mtime, size_bytes, last_indexed, chunk_count, COALESCE(title,'') FROM files WHERE path = ?",
		path,
	).Scan(&f.ID, &f.Path, &f.CollectionID, &hash, &f.Mtime, &f.SizeBytes, &f.LastIndexed, &f.ChunkCount, &f.Title)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get file: %w", err)
	}
	if hash.Valid {
		f.FileHash = hash.String
	}
	return &f, nil
}

// GetFilesByCollection returns all file records for a collection.
func (db *DB) GetFilesByCollection(collectionID int64) ([]FileRecord, error) {
	rows, err := db.conn.Query(
		`SELECT f.id, f.path, f.collection_id, f.file_hash, f.mtime, f.size_bytes, f.last_indexed, f.chunk_count, COALESCE(f.title,'')
		 FROM files f
		 JOIN file_collections fc ON fc.file_id = f.id
		 WHERE fc.collection_id = ?
		 ORDER BY f.path`,
		collectionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query files: %w", err)
	}
	defer rows.Close()

	var files []FileRecord
	for rows.Next() {
		var f FileRecord
		var hash sql.NullString
		if err := rows.Scan(&f.ID, &f.Path, &f.CollectionID, &hash, &f.Mtime, &f.SizeBytes, &f.LastIndexed, &f.ChunkCount, &f.Title); err != nil {
			return nil, fmt.Errorf("scan file: %w", err)
		}
		if hash.Valid {
			f.FileHash = hash.String
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// ListFiles returns all unique file records ordered by path.
func (db *DB) ListFiles() ([]FileRecord, error) {
	rows, err := db.conn.Query(
		"SELECT id, path, collection_id, file_hash, mtime, size_bytes, last_indexed, chunk_count, COALESCE(title,'') FROM files ORDER BY path",
	)
	if err != nil {
		return nil, fmt.Errorf("query files: %w", err)
	}
	defer rows.Close()

	var files []FileRecord
	for rows.Next() {
		var f FileRecord
		var hash sql.NullString
		if err := rows.Scan(&f.ID, &f.Path, &f.CollectionID, &hash, &f.Mtime, &f.SizeBytes, &f.LastIndexed, &f.ChunkCount, &f.Title); err != nil {
			return nil, fmt.Errorf("scan file: %w", err)
		}
		if hash.Valid {
			f.FileHash = hash.String
		}
		files = append(files, f)
	}
	return files, rows.Err()
}

// GetFileCollectionIDs returns all collection memberships for a file.
func (db *DB) GetFileCollectionIDs(fileID int64) ([]int64, error) {
	rows, err := db.conn.Query(
		"SELECT collection_id FROM file_collections WHERE file_id = ? ORDER BY collection_id",
		fileID,
	)
	if err != nil {
		return nil, fmt.Errorf("query file collection ids: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan file collection id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// FileBelongsToCollection reports whether a file has the given collection membership.
func (db *DB) FileBelongsToCollection(fileID, collectionID int64) (bool, error) {
	var exists int
	err := db.conn.QueryRow(
		"SELECT EXISTS(SELECT 1 FROM file_collections WHERE file_id = ? AND collection_id = ?)",
		fileID, collectionID,
	).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check file collection membership: %w", err)
	}
	return exists != 0, nil
}

// ReplaceFileCollections replaces all collection memberships for a file.
func (db *DB) ReplaceFileCollections(fileID int64, collectionIDs []int64) error {
	return replaceFileCollections(db.conn, fileID, collectionIDs)
}

// ReplaceFileCollections replaces all collection memberships for a file within a transaction.
func (t *Tx) ReplaceFileCollections(fileID int64, collectionIDs []int64) error {
	return replaceFileCollections(t.tx, fileID, collectionIDs)
}

func replaceFileCollections(ex execer, fileID int64, collectionIDs []int64) error {
	if _, err := ex.Exec("DELETE FROM file_collections WHERE file_id = ?", fileID); err != nil {
		return fmt.Errorf("clear file collections: %w", err)
	}
	if len(collectionIDs) == 0 {
		return nil
	}

	unique := make(map[int64]struct{}, len(collectionIDs))
	ids := make([]int64, 0, len(collectionIDs))
	for _, id := range collectionIDs {
		if id <= 0 {
			continue
		}
		if _, ok := unique[id]; ok {
			continue
		}
		unique[id] = struct{}{}
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	for _, id := range ids {
		if _, err := ex.Exec(
			"INSERT INTO file_collections (file_id, collection_id) VALUES (?, ?)",
			fileID, id,
		); err != nil {
			return fmt.Errorf("insert file collection %d: %w", id, err)
		}
	}
	return nil
}

// DeleteFile removes a file and its chunks/embeddings (via CASCADE).
func (db *DB) DeleteFile(fileID int64) error {
	_, err := db.conn.Exec("DELETE FROM files WHERE id = ?", fileID)
	if err != nil {
		return fmt.Errorf("delete file: %w", err)
	}
	return nil
}

// GetChunksByFile returns all chunks for a file, ordered by chunk_order.
func (db *DB) GetChunksByFile(fileID int64) ([]ChunkRecord, error) {
	rows, err := db.conn.Query(
		`SELECT id, file_id, chunk_order, start_line, end_line, char_count, created_at,
		        COALESCE(section_id,''), COALESCE(heading,''), COALESCE(heading_level,0),
		        COALESCE(chunk_hash,''), parent_chunk_id
		 FROM chunks WHERE file_id = ? ORDER BY chunk_order`,
		fileID,
	)
	if err != nil {
		return nil, fmt.Errorf("query chunks: %w", err)
	}
	defer rows.Close()

	var chunks []ChunkRecord
	for rows.Next() {
		var c ChunkRecord
		var parentID sql.NullInt64
		if err := rows.Scan(&c.ID, &c.FileID, &c.Order, &c.StartLine, &c.EndLine, &c.CharCount, &c.CreatedAt,
			&c.SectionID, &c.Heading, &c.HeadingLevel, &c.ChunkHash, &parentID); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		if parentID.Valid {
			v := parentID.Int64
			c.ParentChunkID = &v
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

// CollectionFileCount returns the number of files in a collection.
func (db *DB) CollectionFileCount(collectionID int64) (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM file_collections WHERE collection_id = ?", collectionID).Scan(&count)
	return count, err
}

// CollectionChunkCount returns the number of chunks in a collection.
func (db *DB) CollectionChunkCount(collectionID int64) (int, error) {
	var count int
	err := db.conn.QueryRow(
		`SELECT COALESCE(SUM(f.chunk_count), 0)
		 FROM files f
		 JOIN file_collections fc ON fc.file_id = f.id
		 WHERE fc.collection_id = ?`,
		collectionID,
	).Scan(&count)
	return count, err
}

// GetChunkWithFile returns a chunk and its parent file record by chunk ID.
// Returns nil, nil, nil if the chunk does not exist.
func (db *DB) GetChunkWithFile(chunkID int64) (*ChunkRecord, *FileRecord, error) {
	var c ChunkRecord
	var parentID sql.NullInt64
	err := db.conn.QueryRow(
		`SELECT id, file_id, chunk_order, start_line, end_line, char_count, created_at,
		        COALESCE(section_id,''), COALESCE(heading,''), COALESCE(heading_level,0),
		        COALESCE(chunk_hash,''), parent_chunk_id
		 FROM chunks WHERE id = ?`,
		chunkID,
	).Scan(&c.ID, &c.FileID, &c.Order, &c.StartLine, &c.EndLine, &c.CharCount, &c.CreatedAt,
		&c.SectionID, &c.Heading, &c.HeadingLevel, &c.ChunkHash, &parentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("get chunk: %w", err)
	}
	if parentID.Valid {
		v := parentID.Int64
		c.ParentChunkID = &v
	}

	var f FileRecord
	var hash sql.NullString
	err = db.conn.QueryRow(
		"SELECT id, path, collection_id, file_hash, mtime, size_bytes, last_indexed, chunk_count, COALESCE(title,'') FROM files WHERE id = ?",
		c.FileID,
	).Scan(&f.ID, &f.Path, &f.CollectionID, &hash, &f.Mtime, &f.SizeBytes, &f.LastIndexed, &f.ChunkCount, &f.Title)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("get file for chunk: %w", err)
	}
	if hash.Valid {
		f.FileHash = hash.String
	}
	return &c, &f, nil
}

// InsertSearchSession stores a search session for later feedback.
func (db *DB) InsertSearchSession(searchID, query, collection, resultsJSON string) error {
	now := time.Now().Unix()
	_, err := db.conn.Exec(
		"INSERT INTO search_sessions (search_id, query, collection_filter, results_json, created_at) VALUES (?, ?, ?, ?, ?)",
		searchID, query, collection, resultsJSON, now,
	)
	if err != nil {
		return fmt.Errorf("insert search session: %w", err)
	}
	return nil
}

// EmbeddingRecord represents a stored embedding vector.
type EmbeddingRecord struct {
	ChunkID int64
	Vector  []byte
	Model   string
	Dims    int
}

// UpsertEmbedding inserts or updates an embedding for a chunk.
func (db *DB) UpsertEmbedding(chunkID int64, vector []byte, model string, dims int) error {
	_, err := db.conn.Exec(`
		INSERT INTO embeddings (chunk_id, vector, model, dimensions)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(chunk_id) DO UPDATE SET
			vector = excluded.vector,
			model = excluded.model,
			dimensions = excluded.dimensions
	`, chunkID, vector, model, dims)
	if err != nil {
		return fmt.Errorf("upsert embedding: %w", err)
	}
	return nil
}

// ChunkWithFile holds a chunk ID and its file path/line info for retry.
type ChunkWithFile struct {
	ChunkID   int64
	FilePath  string
	StartLine int
	EndLine   int
}

// QueryChunksWithoutEmbeddings returns chunks that have no corresponding embedding.
func (db *DB) QueryChunksWithoutEmbeddings() ([]ChunkWithFile, error) {
	rows, err := db.conn.Query(`
		SELECT c.id, f.path, c.start_line, c.end_line
		FROM chunks c
		JOIN files f ON f.id = c.file_id
		WHERE NOT EXISTS (SELECT 1 FROM embeddings e WHERE e.chunk_id = c.id)
		ORDER BY c.id
	`)
	if err != nil {
		return nil, fmt.Errorf("query unembedded chunks: %w", err)
	}
	defer rows.Close()

	var result []ChunkWithFile
	for rows.Next() {
		var cw ChunkWithFile
		if err := rows.Scan(&cw.ChunkID, &cw.FilePath, &cw.StartLine, &cw.EndLine); err != nil {
			return nil, fmt.Errorf("scan unembedded chunk: %w", err)
		}
		result = append(result, cw)
	}
	return result, rows.Err()
}

// GetEmbedding returns the embedding vector for a chunk.
func (db *DB) GetEmbedding(chunkID int64) ([]byte, error) {
	var vector []byte
	err := db.conn.QueryRow("SELECT vector FROM embeddings WHERE chunk_id = ?", chunkID).Scan(&vector)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get embedding: %w", err)
	}
	return vector, nil
}

// GetAllEmbeddings returns all stored embeddings.
func (db *DB) GetAllEmbeddings() ([]EmbeddingRecord, error) {
	rows, err := db.conn.Query("SELECT chunk_id, vector, model, dimensions FROM embeddings")
	if err != nil {
		return nil, fmt.Errorf("query embeddings: %w", err)
	}
	defer rows.Close()

	var records []EmbeddingRecord
	for rows.Next() {
		var r EmbeddingRecord
		if err := rows.Scan(&r.ChunkID, &r.Vector, &r.Model, &r.Dims); err != nil {
			return nil, fmt.Errorf("scan embedding: %w", err)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// GetFilteredEmbeddings returns embeddings filtered by collection, time, and/or path glob(s).
// Supports comma-separated glob patterns (e.g. "*/pinned/*,*/index/*").
// Pass 0 for collectionID or sinceUnix, and "" for pathGlob, to skip those filters.
func (db *DB) GetFilteredEmbeddings(collectionID int64, sinceUnix int64, pathGlob string) ([]EmbeddingRecord, error) {
	pathFilter, pathArgs := buildPathFilter(pathGlob)

	query := `
		SELECT e.chunk_id, e.vector, e.model, e.dimensions
		FROM embeddings e
		JOIN chunks c ON c.id = e.chunk_id
		JOIN files f ON f.id = c.file_id
		WHERE (? = 0 OR EXISTS (
			SELECT 1 FROM file_collections fc
			WHERE fc.file_id = f.id AND fc.collection_id = ?
		))
		  AND (? = 0 OR f.mtime >= ?)
		  ` + pathFilter

	args := []any{collectionID, collectionID, sinceUnix, sinceUnix}
	args = append(args, pathArgs...)
	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query filtered embeddings: %w", err)
	}
	defer rows.Close()

	var records []EmbeddingRecord
	for rows.Next() {
		var r EmbeddingRecord
		if err := rows.Scan(&r.ChunkID, &r.Vector, &r.Model, &r.Dims); err != nil {
			return nil, fmt.Errorf("scan embedding: %w", err)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// buildPathFilter builds a SQL clause for one or more comma-separated glob patterns.
// Returns empty string and nil args if pathGlob is empty (no filtering).
func buildPathFilter(pathGlob string) (string, []any) {
	if pathGlob == "" {
		return "", nil
	}
	patterns := strings.Split(pathGlob, ",")
	clauses := make([]string, len(patterns))
	args := make([]any, len(patterns))
	for i, p := range patterns {
		clauses[i] = "f.path GLOB ?"
		args[i] = strings.TrimSpace(p)
	}
	return "AND (" + strings.Join(clauses, " OR ") + ")", args
}

// GetChunkIDsByPathGlob returns chunk IDs for files matching the given glob pattern(s).
// Supports comma-separated patterns (e.g. "*/pinned/*,*/index/*") which are OR'd together.
// Optionally filters by collection and/or time. Pass 0 or "" to skip filters.
func (db *DB) GetChunkIDsByPathGlob(pattern string, collectionID int64, sinceUnix int64) ([]int64, error) {
	pathFilter, pathArgs := buildPathFilter(pattern)

	query := `
		SELECT c.id
		FROM chunks c
		JOIN files f ON f.id = c.file_id
		WHERE 1=1
		  ` + pathFilter + `
		  AND (? = 0 OR EXISTS (
			SELECT 1 FROM file_collections fc
			WHERE fc.file_id = f.id AND fc.collection_id = ?
		  ))
		  AND (? = 0 OR f.mtime >= ?)`

	args := append(pathArgs, collectionID, collectionID, sinceUnix, sinceUnix)
	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query chunks by path glob: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan chunk id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// GetEmbeddingsByChunkIDs returns embeddings for the given chunk IDs.
func (db *DB) GetEmbeddingsByChunkIDs(chunkIDs []int64) ([]EmbeddingRecord, error) {
	if len(chunkIDs) == 0 {
		return nil, nil
	}

	// Build query with placeholders.
	placeholders := make([]string, len(chunkIDs))
	args := make([]any, len(chunkIDs))
	for i, id := range chunkIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := "SELECT chunk_id, vector, model, dimensions FROM embeddings WHERE chunk_id IN (" +
		strings.Join(placeholders, ",") + ")"
	rows, err := db.conn.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query embeddings by ids: %w", err)
	}
	defer rows.Close()

	var records []EmbeddingRecord
	for rows.Next() {
		var r EmbeddingRecord
		if err := rows.Scan(&r.ChunkID, &r.Vector, &r.Model, &r.Dims); err != nil {
			return nil, fmt.Errorf("scan embedding: %w", err)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// InsertDeadLetter records a failed operation for later retry.
func (db *DB) InsertDeadLetter(operation, filePath, chunkInfo, errMsg, errCode string) error {
	now := time.Now().Unix()
	_, err := db.conn.Exec(`
		INSERT INTO dead_letters (operation, file_path, chunk_info, error_message, error_code, attempts, first_failed_at, last_failed_at, kind)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?, 'embed')
	`, operation, filePath, chunkInfo, errMsg, errCode, now, now)
	if err != nil {
		return fmt.Errorf("insert dead letter: %w", err)
	}
	return nil
}

// InsertIndexSummaryDeadLetter records a folder-level summary failure
// in the dead_letters table. folderPath identifies the failed folder,
// filePath is optional (for per-file fallback failures), and errMsg
// captures the underlying error.
//
// kind is set to 'index_summary' so the retry-dead-letters command can
// dispatch on it.
func (db *DB) InsertIndexSummaryDeadLetter(folderPath, filePath, errMsg string) error {
	now := time.Now().Unix()
	_, err := db.conn.Exec(`
		INSERT INTO dead_letters (operation, file_path, chunk_info, error_message, error_code, attempts, first_failed_at, last_failed_at, kind)
		VALUES ('index_summary', ?, ?, ?, '', 1, ?, ?, 'index_summary')
	`, folderPath, filePath, errMsg, now, now)
	if err != nil {
		return fmt.Errorf("insert index summary dead letter: %w", err)
	}
	return nil
}

// GetUnresolvedDeadLettersByKind returns unresolved dead letters of a
// specific kind. Pass "" to get all kinds.
func (db *DB) GetUnresolvedDeadLettersByKind(kind string) ([]DeadLetter, error) {
	q := `
		SELECT id, operation, COALESCE(file_path,''), COALESCE(chunk_info,''),
		       COALESCE(error_message,''), COALESCE(error_code,''),
		       attempts, first_failed_at, last_failed_at,
		       COALESCE(kind,'embed')
		FROM dead_letters
		WHERE resolved_at IS NULL`
	args := []any{}
	if kind != "" {
		q += " AND kind = ?"
		args = append(args, kind)
	}
	q += " ORDER BY last_failed_at DESC"

	rows, err := db.conn.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("query dead letters by kind: %w", err)
	}
	defer rows.Close()

	var out []DeadLetter
	for rows.Next() {
		var dl DeadLetter
		if err := rows.Scan(&dl.ID, &dl.Operation, &dl.FilePath, &dl.ChunkInfo,
			&dl.ErrorMessage, &dl.ErrorCode, &dl.Attempts,
			&dl.FirstFailed, &dl.LastFailed, &dl.Kind); err != nil {
			return nil, fmt.Errorf("scan dead letter: %w", err)
		}
		out = append(out, dl)
	}
	return out, rows.Err()
}

// InsertAPIUsage records an API usage event.
func (db *DB) InsertAPIUsage(operation string, requestCount, tokenCount int, latencyMs int64) error {
	now := time.Now().Unix()
	_, err := db.conn.Exec(`
		INSERT INTO api_usage (timestamp, operation, request_count, token_count, latency_ms)
		VALUES (?, ?, ?, ?, ?)
	`, now, operation, requestCount, tokenCount, latencyMs)
	if err != nil {
		return fmt.Errorf("insert api usage: %w", err)
	}
	return nil
}

// DeadLetter represents an unresolved dead letter entry.
type DeadLetter struct {
	ID           int64
	Operation    string
	FilePath     string
	ChunkInfo    string
	ErrorMessage string
	ErrorCode    string
	Attempts     int
	FirstFailed  int64
	LastFailed   int64
	// Kind discriminates row purpose: "embed" (default) or
	// "index_summary" for AI summary failures.
	Kind string
}

// GetUnresolvedDeadLetters returns all dead letters where resolved_at IS NULL.
func (db *DB) GetUnresolvedDeadLetters() ([]DeadLetter, error) {
	rows, err := db.conn.Query(`
		SELECT id, operation, COALESCE(file_path,''), COALESCE(chunk_info,''),
		       COALESCE(error_message,''), COALESCE(error_code,''),
		       attempts, first_failed_at, last_failed_at,
		       COALESCE(kind,'embed')
		FROM dead_letters
		WHERE resolved_at IS NULL
		ORDER BY last_failed_at DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("query dead letters: %w", err)
	}
	defer rows.Close()

	var letters []DeadLetter
	for rows.Next() {
		var dl DeadLetter
		if err := rows.Scan(&dl.ID, &dl.Operation, &dl.FilePath, &dl.ChunkInfo,
			&dl.ErrorMessage, &dl.ErrorCode, &dl.Attempts,
			&dl.FirstFailed, &dl.LastFailed, &dl.Kind); err != nil {
			return nil, fmt.Errorf("scan dead letter: %w", err)
		}
		letters = append(letters, dl)
	}
	return letters, rows.Err()
}

// ResolveDeadLetter marks a dead letter as resolved.
func (db *DB) ResolveDeadLetter(id int64) error {
	now := time.Now().Unix()
	_, err := db.conn.Exec("UPDATE dead_letters SET resolved_at = ? WHERE id = ?", now, id)
	if err != nil {
		return fmt.Errorf("resolve dead letter: %w", err)
	}
	return nil
}

// IncrementDeadLetterAttempts increments the attempt count and updates last_failed_at.
func (db *DB) IncrementDeadLetterAttempts(id int64) error {
	now := time.Now().Unix()
	_, err := db.conn.Exec(
		"UPDATE dead_letters SET attempts = attempts + 1, last_failed_at = ? WHERE id = ?",
		now, id,
	)
	if err != nil {
		return fmt.Errorf("increment dead letter attempts: %w", err)
	}
	return nil
}

// SearchSession represents a stored search session.
type SearchSession struct {
	SearchID         string
	Query            string
	CollectionFilter string
	ResultsJSON      string
	CreatedAt        int64
}

// GetSearchSession returns a search session by its search_id.
func (db *DB) GetSearchSession(searchID string) (*SearchSession, error) {
	var s SearchSession
	err := db.conn.QueryRow(
		"SELECT search_id, query, COALESCE(collection_filter,''), results_json, created_at FROM search_sessions WHERE search_id = ?",
		searchID,
	).Scan(&s.SearchID, &s.Query, &s.CollectionFilter, &s.ResultsJSON, &s.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get search session: %w", err)
	}
	return &s, nil
}

// InsertFeedback records a feedback signal for a search result.
func (db *DB) InsertFeedback(searchID string, resultIndex int, docPath string, chunkID int64, signal, query string) error {
	now := time.Now().Unix()
	_, err := db.conn.Exec(
		"INSERT INTO feedback (search_id, result_index, doc_path, chunk_id, signal, query, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)",
		searchID, fmt.Sprintf("%d", resultIndex), docPath, chunkID, signal, query, now,
	)
	if err != nil {
		return fmt.Errorf("insert feedback: %w", err)
	}
	return nil
}

// GetFeedbackForChunk returns the number of positive and negative feedback signals for a chunk.
func (db *DB) GetFeedbackForChunk(chunkID int64) (positive int, negative int, err error) {
	err = db.conn.QueryRow(
		"SELECT COALESCE(SUM(CASE WHEN signal = 'positive' THEN 1 ELSE 0 END), 0), COALESCE(SUM(CASE WHEN signal = 'negative' THEN 1 ELSE 0 END), 0) FROM feedback WHERE chunk_id = ?",
		chunkID,
	).Scan(&positive, &negative)
	if err != nil {
		return 0, 0, fmt.Errorf("get feedback for chunk: %w", err)
	}
	return positive, negative, nil
}

// GetAllFilePaths returns all file paths stored in the database.
func (db *DB) GetAllFilePaths() ([]string, error) {
	rows, err := db.conn.Query("SELECT path FROM files")
	if err != nil {
		return nil, fmt.Errorf("query file paths: %w", err)
	}
	defer rows.Close()

	var paths []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("scan file path: %w", err)
		}
		paths = append(paths, p)
	}
	return paths, rows.Err()
}

// UnresolvedDeadLetterCount returns the number of unresolved dead letters.
func (db *DB) UnresolvedDeadLetterCount() (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM dead_letters WHERE resolved_at IS NULL").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count dead letters: %w", err)
	}
	return count, nil
}

// APIUsageStats holds aggregated API usage by operation.
type APIUsageStats struct {
	Operation    string
	RequestCount int
	TokenCount   int
}

// GetAPIUsageStats returns aggregated API usage grouped by operation.
func (db *DB) GetAPIUsageStats() ([]APIUsageStats, error) {
	rows, err := db.conn.Query(`
		SELECT operation, COALESCE(SUM(request_count), 0), COALESCE(SUM(token_count), 0)
		FROM api_usage
		GROUP BY operation
		ORDER BY operation
	`)
	if err != nil {
		return nil, fmt.Errorf("query api usage stats: %w", err)
	}
	defer rows.Close()

	var stats []APIUsageStats
	for rows.Next() {
		var s APIUsageStats
		if err := rows.Scan(&s.Operation, &s.RequestCount, &s.TokenCount); err != nil {
			return nil, fmt.Errorf("scan api usage stats: %w", err)
		}
		stats = append(stats, s)
	}
	return stats, rows.Err()
}

// FeedbackTotals holds aggregate feedback counts.
type FeedbackTotals struct {
	Total    int
	Positive int
	Negative int
}

// GetFeedbackTotals returns total feedback signal counts.
func (db *DB) GetFeedbackTotals() (*FeedbackTotals, error) {
	var ft FeedbackTotals
	err := db.conn.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN signal = 'positive' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN signal = 'negative' THEN 1 ELSE 0 END), 0)
		FROM feedback
	`).Scan(&ft.Total, &ft.Positive, &ft.Negative)
	if err != nil {
		return nil, fmt.Errorf("get feedback totals: %w", err)
	}
	return &ft, nil
}

// SearchSessionCount returns the total number of search sessions.
func (db *DB) SearchSessionCount() (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM search_sessions").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count search sessions: %w", err)
	}
	return count, nil
}

// TotalFileCount returns the total number of files across all collections.
func (db *DB) TotalFileCount() (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM files").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count total files: %w", err)
	}
	return count, nil
}

// TotalChunkCount returns the total number of chunks across all files.
func (db *DB) TotalChunkCount() (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count total chunks: %w", err)
	}
	return count, nil
}

// TotalEmbeddingCount returns the total number of embeddings.
func (db *DB) TotalEmbeddingCount() (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM embeddings").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count total embeddings: %w", err)
	}
	return count, nil
}

// CacheEntry represents a cached search result.
type CacheEntry struct {
	CacheKey         string
	Query            string
	CollectionFilter string
	ResultsJSON      string
	ResultCount      int
	Reranked         bool
	TotalBM25        int
	TotalVec         int
	BM25TimeMs       int64
	VecTimeMs        int64
	RerankTimeMs     int64
	CreatedAt        int64
}

// GetCacheEntry retrieves a cache entry by key. Returns ok=false if not found.
func (db *DB) GetCacheEntry(key string) (*CacheEntry, bool) {
	var e CacheEntry
	var reranked int
	err := db.conn.QueryRow(
		`SELECT cache_key, query, collection_filter, results_json, result_count,
		        reranked, total_bm25, total_vec, bm25_time_ms, vec_time_ms, rerank_time_ms, created_at
		 FROM search_cache WHERE cache_key = ?`, key,
	).Scan(&e.CacheKey, &e.Query, &e.CollectionFilter, &e.ResultsJSON, &e.ResultCount,
		&reranked, &e.TotalBM25, &e.TotalVec, &e.BM25TimeMs, &e.VecTimeMs, &e.RerankTimeMs, &e.CreatedAt)
	if err != nil {
		return nil, false
	}
	e.Reranked = reranked != 0
	return &e, true
}

// PutCacheEntry inserts or replaces a cache entry.
func (db *DB) PutCacheEntry(key string, e *CacheEntry) error {
	reranked := 0
	if e.Reranked {
		reranked = 1
	}
	_, err := db.conn.Exec(`
		INSERT OR REPLACE INTO search_cache
			(cache_key, query, collection_filter, results_json, result_count,
			 reranked, total_bm25, total_vec, bm25_time_ms, vec_time_ms, rerank_time_ms, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		key, e.Query, e.CollectionFilter, e.ResultsJSON, e.ResultCount,
		reranked, e.TotalBM25, e.TotalVec, e.BM25TimeMs, e.VecTimeMs, e.RerankTimeMs, e.CreatedAt)
	if err != nil {
		return fmt.Errorf("put cache entry: %w", err)
	}
	return nil
}

// DeleteExpiredCache removes cache entries older than the given timestamp, up to limit.
func (db *DB) DeleteExpiredCache(beforeUnix int64, limit int) error {
	_, err := db.conn.Exec(
		"DELETE FROM search_cache WHERE cache_key IN (SELECT cache_key FROM search_cache WHERE created_at < ? LIMIT ?)",
		beforeUnix, limit)
	if err != nil {
		return fmt.Errorf("delete expired cache: %w", err)
	}
	return nil
}

// ClearCache removes all cache entries.
func (db *DB) ClearCache() error {
	_, err := db.conn.Exec("DELETE FROM search_cache")
	if err != nil {
		return fmt.Errorf("clear cache: %w", err)
	}
	return nil
}

// ExtCount holds a file extension and its count.
type ExtCount struct {
	Ext   string
	Count int
}

// GetFileExtensionCounts returns file counts grouped by extension, ordered by count descending.
func (db *DB) GetFileExtensionCounts() ([]ExtCount, error) {
	rows, err := db.conn.Query("SELECT path FROM files")
	if err != nil {
		return nil, fmt.Errorf("query file extensions: %w", err)
	}
	defer rows.Close()

	counts := make(map[string]int)
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			return nil, fmt.Errorf("scan extension path: %w", err)
		}
		ext := filepath.Ext(filepath.Base(path))
		if ext == "" {
			continue
		}
		counts[ext]++
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	result := make([]ExtCount, 0, len(counts))
	for ext, count := range counts {
		result = append(result, ExtCount{Ext: ext, Count: count})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Count != result[j].Count {
			return result[i].Count > result[j].Count
		}
		return result[i].Ext < result[j].Ext
	})
	return result, nil
}

// DeadLetterBreakdown holds a dead letter operation and its count.
type DeadLetterBreakdown struct {
	Operation string
	Count     int
}

// GetDeadLetterBreakdown returns unresolved dead letter counts grouped by operation.
func (db *DB) GetDeadLetterBreakdown() ([]DeadLetterBreakdown, error) {
	rows, err := db.conn.Query(`
		SELECT operation, COUNT(*) FROM dead_letters
		WHERE resolved_at IS NULL
		GROUP BY operation ORDER BY COUNT(*) DESC
	`)
	if err != nil {
		return nil, fmt.Errorf("query dead letter breakdown: %w", err)
	}
	defer rows.Close()
	var result []DeadLetterBreakdown
	for rows.Next() {
		var dl DeadLetterBreakdown
		if err := rows.Scan(&dl.Operation, &dl.Count); err != nil {
			return nil, fmt.Errorf("scan dead letter breakdown: %w", err)
		}
		result = append(result, dl)
	}
	return result, rows.Err()
}

// OldestUnresolvedDeadLetterTime returns the unix timestamp of the oldest unresolved dead letter.
// Returns 0 if there are no unresolved dead letters.
func (db *DB) OldestUnresolvedDeadLetterTime() (int64, error) {
	var ts sql.NullInt64
	err := db.conn.QueryRow("SELECT MIN(first_failed_at) FROM dead_letters WHERE resolved_at IS NULL").Scan(&ts)
	if err != nil {
		return 0, fmt.Errorf("oldest dead letter: %w", err)
	}
	if !ts.Valid {
		return 0, nil
	}
	return ts.Int64, nil
}

// PurgeDeadLetters marks unresolved dead letters as resolved without retrying.
// Filters by olderThanUnix (if > 0) and operation (if non-empty).
func (db *DB) PurgeDeadLetters(olderThanUnix int64, operation string) (int, error) {
	now := time.Now().Unix()
	query := "UPDATE dead_letters SET resolved_at = ? WHERE resolved_at IS NULL"
	args := []any{now}

	if olderThanUnix > 0 {
		query += " AND first_failed_at < ?"
		args = append(args, olderThanUnix)
	}
	if operation != "" {
		query += " AND operation = ?"
		args = append(args, operation)
	}

	res, err := db.conn.Exec(query, args...)
	if err != nil {
		return 0, fmt.Errorf("purge dead letters: %w", err)
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// CacheEntryCount returns the number of cache entries.
func (db *DB) CacheEntryCount() (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM search_cache").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count cache entries: %w", err)
	}
	return count, nil
}

// InsertChunkV2 inserts a chunk record with section-aware fields and returns its ID.
func (db *DB) InsertChunkV2(fileID int64, order, startLine, endLine, charCount int, sectionID, heading string, headingLevel int, chunkHash string, parentChunkID *int64) (int64, error) {
	return insertChunkV2(db.conn, fileID, order, startLine, endLine, charCount, sectionID, heading, headingLevel, chunkHash, parentChunkID)
}

// InsertChunkV2 inserts a chunk record with section-aware fields within a transaction.
func (t *Tx) InsertChunkV2(fileID int64, order, startLine, endLine, charCount int, sectionID, heading string, headingLevel int, chunkHash string, parentChunkID *int64) (int64, error) {
	return insertChunkV2(t.tx, fileID, order, startLine, endLine, charCount, sectionID, heading, headingLevel, chunkHash, parentChunkID)
}

func insertChunkV2(ex execer, fileID int64, order, startLine, endLine, charCount int, sectionID, heading string, headingLevel int, chunkHash string, parentChunkID *int64) (int64, error) {
	now := time.Now().Unix()
	res, err := ex.Exec(
		`INSERT INTO chunks (file_id, chunk_order, start_line, end_line, char_count, created_at, section_id, heading, heading_level, chunk_hash, parent_chunk_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		fileID, order, startLine, endLine, charCount, now, sectionID, heading, headingLevel, chunkHash, parentChunkID,
	)
	if err != nil {
		return 0, fmt.Errorf("insert chunk v2: %w", err)
	}
	return res.LastInsertId()
}

// GetChunkByFileAndOrder returns a chunk by file ID and chunk order.
// Returns nil, nil if not found.
func (db *DB) GetChunkByFileAndOrder(fileID int64, order int) (*ChunkRecord, error) {
	var c ChunkRecord
	var parentID sql.NullInt64
	err := db.conn.QueryRow(
		`SELECT id, file_id, chunk_order, start_line, end_line, char_count, created_at,
		        COALESCE(section_id,''), COALESCE(heading,''), COALESCE(heading_level,0),
		        COALESCE(chunk_hash,''), parent_chunk_id
		 FROM chunks WHERE file_id = ? AND chunk_order = ?`,
		fileID, order,
	).Scan(&c.ID, &c.FileID, &c.Order, &c.StartLine, &c.EndLine, &c.CharCount, &c.CreatedAt,
		&c.SectionID, &c.Heading, &c.HeadingLevel, &c.ChunkHash, &parentID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get chunk by file and order: %w", err)
	}
	if parentID.Valid {
		v := parentID.Int64
		c.ParentChunkID = &v
	}
	return &c, nil
}

// InsertLink inserts a link record.
func (db *DB) InsertLink(fileID int64, sectionID string, line int, targetPath, targetSection, linkType, raw string) error {
	return insertLink(db.conn, fileID, sectionID, line, targetPath, targetSection, linkType, raw)
}

// InsertLink inserts a link record within a transaction.
func (t *Tx) InsertLink(fileID int64, sectionID string, line int, targetPath, targetSection, linkType, raw string) error {
	return insertLink(t.tx, fileID, sectionID, line, targetPath, targetSection, linkType, raw)
}

func insertLink(ex execer, fileID int64, sectionID string, line int, targetPath, targetSection, linkType, raw string) error {
	_, err := ex.Exec(
		`INSERT INTO links (source_file_id, source_section_id, source_line, target_path, target_section, link_type, raw)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		fileID, sectionID, line, targetPath, targetSection, linkType, raw,
	)
	if err != nil {
		return fmt.Errorf("insert link: %w", err)
	}
	return nil
}

// DeleteLinksByFile removes all links for a file.
func (db *DB) DeleteLinksByFile(fileID int64) error {
	return deleteLinksByFile(db.conn, fileID)
}

// DeleteLinksByFile removes all links for a file within a transaction.
func (t *Tx) DeleteLinksByFile(fileID int64) error {
	return deleteLinksByFile(t.tx, fileID)
}

func deleteLinksByFile(ex execer, fileID int64) error {
	_, err := ex.Exec("DELETE FROM links WHERE source_file_id = ?", fileID)
	if err != nil {
		return fmt.Errorf("delete links by file: %w", err)
	}
	return nil
}

// GetLinksByFileAndSection returns all links from a specific file and section.
func (db *DB) GetLinksByFileAndSection(fileID int64, sectionID string) ([]LinkRecord, error) {
	rows, err := db.conn.Query(
		`SELECT id, source_file_id, COALESCE(source_section_id,''), source_line,
		        target_path, COALESCE(target_section,''), COALESCE(link_type,'markdown'), COALESCE(raw,'')
		 FROM links WHERE source_file_id = ? AND source_section_id = ?
		 ORDER BY source_line`,
		fileID, sectionID,
	)
	if err != nil {
		return nil, fmt.Errorf("query links by file and section: %w", err)
	}
	defer rows.Close()

	var links []LinkRecord
	for rows.Next() {
		var l LinkRecord
		if err := rows.Scan(&l.ID, &l.SourceFileID, &l.SourceSectionID, &l.SourceLine,
			&l.TargetPath, &l.TargetSection, &l.LinkType, &l.Raw); err != nil {
			return nil, fmt.Errorf("scan link: %w", err)
		}
		links = append(links, l)
	}
	return links, rows.Err()
}

// GetLinksFromFile returns all links from a specific file.
func (db *DB) GetLinksFromFile(fileID int64) ([]LinkRecord, error) {
	rows, err := db.conn.Query(
		`SELECT id, source_file_id, COALESCE(source_section_id,''), source_line,
		        target_path, COALESCE(target_section,''), COALESCE(link_type,'markdown'), COALESCE(raw,'')
		 FROM links WHERE source_file_id = ?
		 ORDER BY source_line`,
		fileID,
	)
	if err != nil {
		return nil, fmt.Errorf("query links from file: %w", err)
	}
	defer rows.Close()

	var links []LinkRecord
	for rows.Next() {
		var l LinkRecord
		if err := rows.Scan(&l.ID, &l.SourceFileID, &l.SourceSectionID, &l.SourceLine,
			&l.TargetPath, &l.TargetSection, &l.LinkType, &l.Raw); err != nil {
			return nil, fmt.Errorf("scan link: %w", err)
		}
		links = append(links, l)
	}
	return links, rows.Err()
}

// GetBacklinks returns all links pointing to a target path.
// If targetSection is empty, returns links to all sections of that path.
// If targetSection is non-empty, returns only links to that specific section.
func (db *DB) GetBacklinks(targetPath string, targetSection string) ([]LinkRecord, error) {
	var rows *sql.Rows
	var err error
	if targetSection == "" {
		rows, err = db.conn.Query(
			`SELECT id, source_file_id, COALESCE(source_section_id,''), source_line,
			        target_path, COALESCE(target_section,''), COALESCE(link_type,'markdown'), COALESCE(raw,'')
			 FROM links WHERE target_path = ?
			 ORDER BY source_file_id, source_line`,
			targetPath,
		)
	} else {
		rows, err = db.conn.Query(
			`SELECT id, source_file_id, COALESCE(source_section_id,''), source_line,
			        target_path, COALESCE(target_section,''), COALESCE(link_type,'markdown'), COALESCE(raw,'')
			 FROM links WHERE target_path = ? AND target_section = ?
			 ORDER BY source_file_id, source_line`,
			targetPath, targetSection,
		)
	}
	if err != nil {
		return nil, fmt.Errorf("query backlinks: %w", err)
	}
	defer rows.Close()

	var links []LinkRecord
	for rows.Next() {
		var l LinkRecord
		if err := rows.Scan(&l.ID, &l.SourceFileID, &l.SourceSectionID, &l.SourceLine,
			&l.TargetPath, &l.TargetSection, &l.LinkType, &l.Raw); err != nil {
			return nil, fmt.Errorf("scan backlink: %w", err)
		}
		links = append(links, l)
	}
	return links, rows.Err()
}
