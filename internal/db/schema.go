package db

const SchemaVersion = 9

const schemaSQL = `
CREATE TABLE IF NOT EXISTS schema_version (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER
);

CREATE TABLE IF NOT EXISTS collections (
    id INTEGER PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    path TEXT NOT NULL,
    tags TEXT,
    created_at INTEGER
);

CREATE TABLE IF NOT EXISTS files (
    id INTEGER PRIMARY KEY,
    path TEXT UNIQUE NOT NULL,
    collection_id INTEGER REFERENCES collections(id) ON DELETE CASCADE,
    file_hash TEXT,
    mtime INTEGER,
    size_bytes INTEGER,
    last_indexed INTEGER,
    chunk_count INTEGER,
    title TEXT DEFAULT ''
);
CREATE INDEX IF NOT EXISTS idx_files_collection ON files(collection_id);
CREATE INDEX IF NOT EXISTS idx_files_mtime ON files(mtime);

CREATE TABLE IF NOT EXISTS file_collections (
    file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    collection_id INTEGER NOT NULL REFERENCES collections(id) ON DELETE CASCADE,
    PRIMARY KEY (file_id, collection_id)
);
CREATE INDEX IF NOT EXISTS idx_file_collections_collection ON file_collections(collection_id, file_id);
CREATE INDEX IF NOT EXISTS idx_file_collections_file ON file_collections(file_id, collection_id);

CREATE TABLE IF NOT EXISTS chunks (
    id INTEGER PRIMARY KEY,
    file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    chunk_order INTEGER NOT NULL,
    start_line INTEGER NOT NULL,
    end_line INTEGER NOT NULL,
    char_count INTEGER,
    created_at INTEGER,
    section_id TEXT DEFAULT '',
    heading TEXT DEFAULT '',
    heading_level INTEGER DEFAULT 0,
    chunk_hash TEXT DEFAULT '',
    parent_chunk_id INTEGER DEFAULT NULL,
    UNIQUE(file_id, chunk_order)
);
CREATE INDEX IF NOT EXISTS idx_chunks_file ON chunks(file_id, chunk_order);

CREATE TABLE IF NOT EXISTS embeddings (
    chunk_id INTEGER PRIMARY KEY REFERENCES chunks(id) ON DELETE CASCADE,
    vector BLOB NOT NULL,
    model TEXT,
    dimensions INTEGER
);

CREATE TABLE IF NOT EXISTS dead_letters (
    id INTEGER PRIMARY KEY,
    operation TEXT NOT NULL,
    file_path TEXT,
    chunk_info TEXT,
    error_message TEXT,
    error_code TEXT,
    attempts INTEGER DEFAULT 1,
    first_failed_at INTEGER,
    last_failed_at INTEGER,
    resolved_at INTEGER,
    kind TEXT NOT NULL DEFAULT 'embed'
);

CREATE TABLE IF NOT EXISTS api_usage (
    id INTEGER PRIMARY KEY,
    timestamp INTEGER,
    operation TEXT,
    request_count INTEGER,
    token_count INTEGER,
    latency_ms INTEGER
);

CREATE TABLE IF NOT EXISTS feedback (
    id INTEGER PRIMARY KEY,
    search_id TEXT NOT NULL,
    result_index TEXT NOT NULL,
    doc_path TEXT,
    chunk_id INTEGER,
    signal TEXT NOT NULL,
    query TEXT,
    created_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_feedback_chunk ON feedback(chunk_id, signal);
CREATE INDEX IF NOT EXISTS idx_feedback_path ON feedback(doc_path, signal);

CREATE TABLE IF NOT EXISTS search_sessions (
    search_id TEXT PRIMARY KEY,
    query TEXT,
    collection_filter TEXT,
    results_json TEXT,
    created_at INTEGER
);

CREATE TABLE IF NOT EXISTS search_cache (
    cache_key TEXT PRIMARY KEY,
    query TEXT NOT NULL,
    collection_filter TEXT NOT NULL DEFAULT '',
    results_json TEXT NOT NULL,
    result_count INTEGER NOT NULL,
    reranked INTEGER NOT NULL DEFAULT 0,
    total_bm25 INTEGER NOT NULL DEFAULT 0,
    total_vec INTEGER NOT NULL DEFAULT 0,
    bm25_time_ms INTEGER NOT NULL DEFAULT 0,
    vec_time_ms INTEGER NOT NULL DEFAULT 0,
    rerank_time_ms INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_search_cache_created ON search_cache(created_at);

CREATE TABLE IF NOT EXISTS links (
    id INTEGER PRIMARY KEY,
    source_file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    source_section_id TEXT NOT NULL DEFAULT '',
    source_line INTEGER NOT NULL DEFAULT 0,
    target_path TEXT NOT NULL,
    target_section TEXT NOT NULL DEFAULT '',
    link_type TEXT NOT NULL DEFAULT 'markdown',
    raw TEXT NOT NULL DEFAULT '',
    UNIQUE(source_file_id, source_line, target_path, target_section)
);
CREATE INDEX IF NOT EXISTS idx_links_source ON links(source_file_id);
CREATE INDEX IF NOT EXISTS idx_links_target ON links(target_path);

CREATE TABLE IF NOT EXISTS backlink_counts (
    doc_path TEXT PRIMARY KEY,
    backlink_count INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_backlink_counts_count ON backlink_counts(backlink_count);

CREATE TABLE IF NOT EXISTS read_counts (
    doc_path TEXT PRIMARY KEY,
    total_reads INTEGER NOT NULL DEFAULT 0,
    unique_days INTEGER NOT NULL DEFAULT 0,
    last_read TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_read_counts_total ON read_counts(total_reads);
`
