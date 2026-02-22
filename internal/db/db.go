package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	ID        int64
	FileID    int64
	Order     int
	StartLine int
	EndLine   int
	CharCount int
	CreatedAt int64
}

// Open opens or creates the SQLite database at the given path.
func Open(path string) (*DB, error) {
	sqlDB, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}

	if err := sqlDB.Ping(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("ping db: %w", err)
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

	_, err := db.conn.Exec(
		"INSERT OR IGNORE INTO schema_version (version, applied_at) VALUES (?, ?)",
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

// RemoveCollection deletes a collection and its associated files/chunks/embeddings.
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

	if _, err := tx.Exec("DELETE FROM files WHERE collection_id = ?", id); err != nil {
		return fmt.Errorf("delete files: %w", err)
	}

	if _, err := tx.Exec("DELETE FROM collections WHERE id = ?", id); err != nil {
		return fmt.Errorf("delete collection: %w", err)
	}

	return tx.Commit()
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
	return id, nil
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
		"SELECT id, path, collection_id, file_hash, mtime, size_bytes, last_indexed, chunk_count, COALESCE(title,'') FROM files WHERE collection_id = ?",
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
		"SELECT id, file_id, chunk_order, start_line, end_line, char_count, created_at FROM chunks WHERE file_id = ? ORDER BY chunk_order",
		fileID,
	)
	if err != nil {
		return nil, fmt.Errorf("query chunks: %w", err)
	}
	defer rows.Close()

	var chunks []ChunkRecord
	for rows.Next() {
		var c ChunkRecord
		if err := rows.Scan(&c.ID, &c.FileID, &c.Order, &c.StartLine, &c.EndLine, &c.CharCount, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan chunk: %w", err)
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

// CollectionFileCount returns the number of files in a collection.
func (db *DB) CollectionFileCount(collectionID int64) (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM files WHERE collection_id = ?", collectionID).Scan(&count)
	return count, err
}

// CollectionChunkCount returns the number of chunks in a collection.
func (db *DB) CollectionChunkCount(collectionID int64) (int, error) {
	var count int
	err := db.conn.QueryRow(
		"SELECT COALESCE(SUM(chunk_count), 0) FROM files WHERE collection_id = ?",
		collectionID,
	).Scan(&count)
	return count, err
}

// GetChunkWithFile returns a chunk and its parent file record by chunk ID.
// Returns nil, nil, nil if the chunk does not exist.
func (db *DB) GetChunkWithFile(chunkID int64) (*ChunkRecord, *FileRecord, error) {
	var c ChunkRecord
	err := db.conn.QueryRow(
		"SELECT id, file_id, chunk_order, start_line, end_line, char_count, created_at FROM chunks WHERE id = ?",
		chunkID,
	).Scan(&c.ID, &c.FileID, &c.Order, &c.StartLine, &c.EndLine, &c.CharCount, &c.CreatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil, nil
		}
		return nil, nil, fmt.Errorf("get chunk: %w", err)
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
		WHERE (? = 0 OR f.collection_id = ?)
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
		  AND (? = 0 OR f.collection_id = ?)
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
		INSERT INTO dead_letters (operation, file_path, chunk_info, error_message, error_code, attempts, first_failed_at, last_failed_at)
		VALUES (?, ?, ?, ?, ?, 1, ?, ?)
	`, operation, filePath, chunkInfo, errMsg, errCode, now, now)
	if err != nil {
		return fmt.Errorf("insert dead letter: %w", err)
	}
	return nil
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
}

// GetUnresolvedDeadLetters returns all dead letters where resolved_at IS NULL.
func (db *DB) GetUnresolvedDeadLetters() ([]DeadLetter, error) {
	rows, err := db.conn.Query(`
		SELECT id, operation, COALESCE(file_path,''), COALESCE(chunk_info,''),
		       COALESCE(error_message,''), COALESCE(error_code,''),
		       attempts, first_failed_at, last_failed_at
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
			&dl.FirstFailed, &dl.LastFailed); err != nil {
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

// CacheEntryCount returns the number of cache entries.
func (db *DB) CacheEntryCount() (int, error) {
	var count int
	err := db.conn.QueryRow("SELECT COUNT(*) FROM search_cache").Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count cache entries: %w", err)
	}
	return count, nil
}
