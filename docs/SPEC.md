# SIFT - Search Index for Finding Things

## Purpose

SIFT is a local-first semantic search engine for personal knowledge management. It provides hybrid search (BM25 + vector embeddings) with reranking, feedback-driven scoring, and usage tracking. SIFT is the retrieval engine that powers [Mem](https://github.com/svilupp/mem) - a personal memory system for AI agents.

**Primary use cases:**
- Memory retrieval for AI agents (JSON output, structured results)
- Personal knowledge search for humans (pretty output, quick file access)
- Searchable conversation archives and notes

**Design philosophy:**
- Fast and pragmatic over perfect
- Observable and debuggable (logs, stats, health checks)
- Easy iteration on scoring and retrieval strategies
- Configurable defaults with sensible starting points
- Works across machines via git-synced files with local-only vectors

**Role in the stack:**
- SIFT owns: indexing, search, reranking, scoring, feedback, usage stats
- Mem owns: index composition (routing table), daily logs, session extraction, reflection, git sync
- Vault is just another collection to SIFT - no special treatment

---

## How It Works

1. **Index:** Register collections (folders) containing text files. SIFT chunks files by rows, embeds chunks via Voyage API, and indexes for BM25 via Bleve.

2. **Search:** Query combines BM25 keyword matching and vector similarity using RRF (Reciprocal Rank Fusion) with position boosting. Top results are reranked via Voyage. Final scoring applies time decay and feedback boosts.

3. **Retrieve:** Results include file paths, line ranges, content previews, and scores. Each search gets a unique ID for feedback tracking.

4. **Feedback:** Mark results as positive/negative to build training data. Feedback boosts are applied to future searches via Bayesian-smoothed scoring.

---

## Installation & Setup

```bash
# Install
go install github.com/svilupp/go-training-range/sift/cmd/sift@latest

# Initialize (creates ~/.sift/, prompts for API key)
sift config init

# Add a collection
sift collections add vault ~/Documents/GitHub/md-tower-vault
sift collections add notes ~/notes

# Index
sift refresh

# Search
sift search "authentication flow"
sift search "auth" -c vault --since 2d --json
```

---

## File Structure

```
~/.sift/
├── config.toml                    # Main configuration
├── sift.db                        # SQLite: files, chunks, embeddings, feedback, usage
├── bleve/                         # Bleve BM25 index
├── logs/
│   ├── searches-2026-02-03.jsonl  # Weekly rotation, Monday-indexed
│   ├── feedback-2026-02-03.jsonl
│   └── sql-2026-02-03.jsonl       # SQL command logging
└── .lock                          # flock for concurrent operation prevention
```

---

## Configuration

**~/.sift/config.toml**

```toml
[api]
voyage_api_key = ""  # Set via: sift config set api.voyage_api_key <key>

[embedding]
model = "voyage-4-lite"
dimensions = 512               # Options: 256, 512, 1024, 2048 (binary quantized)
output_dtype = "binary"        # ~32x smaller than float32
batch_size = 200               # Max texts per Voyage API call (API limit: 1000)
input_type_query = "query"     # Voyage input_type for search queries
input_type_document = "document"  # Voyage input_type for indexed chunks

[reranking]
model = "rerank-2.5-lite"
enabled = true
top_n = 75                     # Rerank top N from initial retrieval

[chunking]
rows_per_chunk = 25
overlap_rows = 5               # Context: 5 rows before + 5 rows after
min_chunk_chars = 200          # Merge chunks shorter than this with neighbors
skip_empty_rows = true

[search]
default_top_k = 20
preview_chars = 500
bm25_weight = 1.0
vector_weight = 1.0
rrf_k = 60
rrf_boost_top_n = 3           # Boost top 3 positions
rrf_boost_factor = 2.0

[scoring]
recency_weight = 0.2
recency_half_life_days = 30
feedback_enabled = true

[bm25]
analyzer = "standard"          # Bleve analyzer: standard, keyword, simple

[output]
editor_command = "code -g {file}:{line}"

[logs]
rotate_weekly = true
max_weeks = 12
```

---

## Database Schema

```sql
-- Schema version for migrations
CREATE TABLE schema_version (
    version INTEGER PRIMARY KEY,
    applied_at INTEGER
);

-- Registered collections
CREATE TABLE collections (
    id INTEGER PRIMARY KEY,
    name TEXT UNIQUE NOT NULL,
    path TEXT NOT NULL,           -- Absolute path to folder
    tags TEXT,                    -- JSON array of tags
    created_at INTEGER
);

-- Indexed files
CREATE TABLE files (
    id INTEGER PRIMARY KEY,
    path TEXT UNIQUE NOT NULL,
    collection_id INTEGER REFERENCES collections(id),
    file_hash TEXT,               -- xxHash for content comparison
    mtime INTEGER,                -- Unix timestamp for fast sync
    size_bytes INTEGER,
    last_indexed INTEGER,
    chunk_count INTEGER
);
CREATE INDEX idx_files_collection ON files(collection_id);
CREATE INDEX idx_files_mtime ON files(mtime);

-- Chunks with explicit ordering for neighbor lookup
CREATE TABLE chunks (
    id INTEGER PRIMARY KEY,
    file_id INTEGER NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    chunk_order INTEGER NOT NULL,  -- 0, 1, 2... sequential
    start_line INTEGER NOT NULL,   -- Stored WITHOUT overlap
    end_line INTEGER NOT NULL,     -- Stored WITHOUT overlap
    char_count INTEGER,            -- For tracking/debugging
    created_at INTEGER,
    UNIQUE(file_id, chunk_order)
);
CREATE INDEX idx_chunks_file ON chunks(file_id, chunk_order);

-- Vector embeddings (separate for easy rebuild)
CREATE TABLE embeddings (
    chunk_id INTEGER PRIMARY KEY REFERENCES chunks(id) ON DELETE CASCADE,
    vector BLOB NOT NULL,          -- binary-quantized embedding (1 bit per dim, packed)
    model TEXT,
    dimensions INTEGER
);

-- Dead letter queue for failed operations
CREATE TABLE dead_letters (
    id INTEGER PRIMARY KEY,
    operation TEXT NOT NULL,       -- 'embed', 'rerank', etc.
    kind TEXT NOT NULL DEFAULT '', -- 'embedding' or 'index_summary' (schema v9)
    file_path TEXT,
    chunk_info TEXT,               -- JSON with start/end lines
    error_message TEXT,
    error_code TEXT,
    attempts INTEGER DEFAULT 1,
    first_failed_at INTEGER,
    last_failed_at INTEGER,
    resolved_at INTEGER            -- NULL until resolved
);

-- API usage tracking (for sift config stats)
CREATE TABLE api_usage (
    id INTEGER PRIMARY KEY,
    timestamp INTEGER,
    operation TEXT,                -- 'embed', 'rerank', 'search'
    request_count INTEGER,
    token_count INTEGER,           -- From Voyage usage.total_tokens
    latency_ms INTEGER
);

-- Search feedback
CREATE TABLE feedback (
    id INTEGER PRIMARY KEY,
    search_id TEXT NOT NULL,
    result_index TEXT NOT NULL,    -- "a", "b", etc.
    doc_path TEXT,
    chunk_id INTEGER,
    signal TEXT NOT NULL,          -- 'positive', 'negative'
    query TEXT,                    -- Denormalized for analysis
    created_at INTEGER
);
CREATE INDEX idx_feedback_chunk ON feedback(chunk_id, signal);
CREATE INDEX idx_feedback_path ON feedback(doc_path, signal);

-- Search sessions (ephemeral, for feedback correlation)
CREATE TABLE search_sessions (
    search_id TEXT PRIMARY KEY,
    query TEXT,
    collection_filter TEXT,       -- JSON array or null
    results_json TEXT,            -- Full result set for feedback lookup
    created_at INTEGER
);
```

---

## CLI Commands

### Top-Level Commands

```bash
sift search <query> [flags]        # Search across collections
sift refresh [flags]               # Incremental index refresh
sift feedback <search_id> [flags]  # Provide feedback on results
sift sql [query]                   # SQLite shell (read-only)
sift collections [subcommand]      # Manage collections
sift config [subcommand]           # Configuration and administration
```

### `sift config` (Configuration & Administration)

```bash
sift config init                   # Create ~/.sift/, prompt for Voyage API key
sift config show                   # Show current configuration
sift config set <key> <value>      # Set config value (dot notation)
sift config get <key>              # Get config value
sift config stats                  # Usage statistics (embeddings, searches, feedback)
sift config health                 # Check index health, show dead letters
sift config rebuild-bm25           # Rebuild Bleve index from DB
sift config purge                  # Purge all data
sift config purge -c <collection>  # Purge specific collection
sift config retry-dead-letters     # Retry failed operations
sift config logs                   # Tail recent search logs
sift config logs --feedback        # Show feedback logs
```

### `sift collections`

```bash
sift collections                   # List all collections
sift collections add <name> <path> # Add collection
sift collections add <name> <path> --tags work,2024
sift collections remove <name>     # Remove collection (keeps files)
```

### `sift search`

```bash
sift search <query>                # Pretty output (default)
sift search <query> --json         # JSON for agents
sift search <query> --files        # File paths only
sift search <query> -c <collection> # Filter by collection
sift search <query> --since 2d     # Time filter (2d, 1w, etc.)
sift search <query> -k 50          # Override top_k
sift search <query> --preview 500  # Override preview chars
```

### `sift refresh`

```bash
sift refresh                       # Incremental refresh all collections
sift refresh -c <collection>       # Refresh specific collection
sift refresh --full                # Force full reindex
sift refresh --dry-run             # Show what would be indexed
```

### `sift feedback`

```bash
sift feedback <search_id> --positive a,b --negative c,d
```

### `sift sql`

```bash
sift sql                           # Interactive SQLite shell (read-only)
sift sql "<query>"                 # Run single SQL query
```

### `sift config stats` Output

```
SIFT Usage Statistics
=====================
Since: 2026-01-15

Embeddings:
  Total requests:  47
  Total tokens:    125,400
  Estimated cost:  ~$0.13

Reranking:
  Total requests:  312
  Total tokens:    780,000
  Estimated cost:  ~$0.39

Searches:
  Total searches:  312
  Avg latency:     142ms (p95: 320ms)

Feedback:
  Total feedback:  89
  Positive:        67
  Negative:        22
  Coverage:        29% of searches

Collections:
  vault:  1,247 files, 4,891 chunks
  notes:  312 files, 1,023 chunks

Index:
  DB size:     24.3 MB
  Bleve size:  35.1 MB
  Last refresh: 2026-02-06 10:30:00
```

---

## Search Algorithm

### 1. Filter Candidates

Pre-filter chunks by collection and time scope before any scoring:

```sql
SELECT c.id, c.file_id, c.start_line, c.end_line
FROM chunks c
JOIN files f ON c.file_id = f.id
WHERE f.collection_id IN (?)
  AND f.mtime >= ?  -- time scope
```

### 2. Parallel Retrieval

Run BM25 (Bleve) and vector search concurrently on filtered candidates.

**BM25:** Query Bleve index. File path is indexed alongside content for path-based keyword matching.

**Vector:** Embed query via Voyage (with `input_type: "query"`), compute cosine similarity against stored vectors (embedded with `input_type: "document"`).

### 3. RRF Fusion with Position Boosting

```
score(doc) = Σ weight(rank) / (k + rank)

where weight(rank) = boost_factor if rank < boost_top_n else 1.0
```

Default: k=60, boost_top_n=3, boost_factor=2.0

### 4. Rerank Top N

Send top 75 (configurable via `reranking.top_n`) chunks to Voyage `rerank-2.5-lite` with original query. Returns relevance_score for each.

### 5. Final Scoring

Apply time decay and feedback boosts:

```
final = rerank_score
      * (1 + recency_score * recency_weight)
      * feedback_boost

recency_score = 0.5 ^ (age_days / half_life_days)

feedback_boost = bayesian_ratio(good_signals, bad_signals)
               = 0.7 + 0.6 * ((good + 1) / (good + bad + 2))
               # Maps to [0.7, 1.3] range
```

### 6. Return Results

Top K results with:
- Index label (a-z, aa-zz)
- File path, line range
- Content preview (truncated)
- Score components (in JSON mode)
- Search ID for feedback

---

## Output Formats

### Pretty (default)

```
Search: "authentication flow"
ID: a1b2c3 | 20 results | 142ms

[a] docs/api/auth.md:45-89 (0.87)
    The authentication flow begins with the client requesting
    a token from the auth server. The server validates...
    > code -g docs/api/auth.md:45

[b] docs/security/login.md:12-34 (0.72)
    Users must provide valid credentials before accessing
    protected resources. The system supports OAuth2...
    > code -g docs/security/login.md:12

...

Feedback: sift feedback a1b2c3 --positive a,b --negative c
```

### Files Only (--files)

```
docs/api/auth.md
docs/security/login.md
notes/2024/jan/meeting.md
```

### JSON (--json)

```json
{
  "search_id": "a1b2c3",
  "query": "authentication flow",
  "results": [
    {
      "index": "a",
      "chunk_id": 4521,
      "file": "docs/api/auth.md",
      "collection": "vault",
      "start_line": 45,
      "end_line": 89,
      "content": "The authentication flow begins with...",
      "score": 0.87,
      "components": {
        "bm25_rank": 2,
        "vector_rank": 1,
        "rrf": 0.65,
        "rerank": 0.87,
        "recency": 0.95,
        "feedback": 1.1,
        "final": 0.87
      },
      "open_cmd": "code -g docs/api/auth.md:45"
    }
  ],
  "meta": {
    "total_candidates": 1523,
    "bm25_time_ms": 12,
    "vector_time_ms": 28,
    "rerank_time_ms": 95,
    "total_time_ms": 142,
    "filters": {
      "collections": ["vault"],
      "since": null
    }
  },
  "feedback_cmd": "sift feedback a1b2c3 --positive <indices> --negative <indices>"
}
```

---

## Chunking Strategy

### Row-Based Chunking (All File Types)

Single chunking strategy for all file types (`.md`, `.txt`, `.jsonl`, etc.):

1. Read file lines, skip empty rows (configurable)
2. Group into chunks of N rows (default: 25, configurable via `chunking.rows_per_chunk`)
3. Store chunk as (file_id, chunk_order, start_line, end_line)
4. When embedding: extract lines with overlap (±5 rows)
5. Merge chunks with < min_chunk_chars (default: 200) into neighbors
6. Log char_count per chunk for monitoring

### Overlap Semantics

```
Chunk 0: lines 1-25   → embed with context lines 1-30
Chunk 1: lines 26-50  → embed with context lines 21-55
Chunk 2: lines 51-75  → embed with context lines 46-80
```

Stored line ranges do NOT overlap. Overlap is added only during embedding.

### Future: File-Type-Specific Chunking

The chunking interface supports multiple strategies for future use:

```go
type Chunker interface {
    Chunk(content []byte, path string) ([]Chunk, error)
}
```

---

## Sync & Refresh Algorithm

### Fast Sync Check

1. Get all file paths in registered collections
2. Sort by mtime (newest first) from filesystem
3. Compare against DB:
   - New files (not in DB) → queue for indexing
   - Changed mtime → verify with xxHash
   - Hash mismatch → queue for reindexing
   - Missing from filesystem → queue for deletion

### Incremental Refresh

```
1. Collect files to process (new, changed, deleted)
2. Delete removed files (CASCADE removes chunks + embeddings)
3. Chunk new/changed files
4. Filter chunks by min_char threshold
5. Batch embed via Voyage (up to batch_size per request)
6. Store chunks + embeddings in DB
7. Index content in Bleve (includes file path for BM25)
8. Update file metadata (hash, mtime, chunk_count)
9. Record API usage in api_usage table
```

### Progress Output

```
Refreshing...
  Scanning: 500 files
  Changed: 12 files (3 new, 7 modified, 2 deleted)
  Chunking: 89 chunks
  Embedding: batch 1/2 [=====>    ] 50%
  Indexing BM25...
Done: 89 chunks indexed in 2.3s (tokens: 4,200)
```

### Error Handling

- Retry Voyage API calls 3x with exponential backoff (100ms, 200ms, 400ms)
- On persistent failure: log to dead_letters table, continue with remaining chunks
- Dead letters visible via `sift config health`
- Retry via `sift config retry-dead-letters`

---

## Voyage API Integration

### Embedding

- **Endpoint**: `POST https://api.voyageai.com/v1/embeddings`
- **Model**: `voyage-4-lite` (512 dims default, binary-quantized)
- **Auth**: `Authorization: Bearer $VOYAGE_API_KEY`
- **Batch**: Up to 200 texts per request (API supports 1000)
- **Asymmetric**: Use `input_type: "document"` for chunks, `input_type: "query"` for search queries
- **Usage tracking**: `response.usage.total_tokens` recorded in api_usage table

### Reranking

- **Endpoint**: `POST https://api.voyageai.com/v1/rerank`
- **Model**: `rerank-2.5-lite`
- **Limits**: Max 1000 documents, max 32K tokens per query+doc, max 600K total tokens
- **Response**: `relevance_score` per document, sorted descending

### Error Codes

| Code | Meaning | Action |
|------|---------|--------|
| 400 | Invalid request | Fix request, log error |
| 401 | Invalid API key | Alert user, check config |
| 429 | Rate limit | Backoff + retry (3x) |
| 500+ | Server error | Retry with backoff |

### Go Client

Direct HTTP client (no third-party SDK dependency):

```go
type VoyageClient struct {
    apiKey     string
    httpClient *http.Client
    baseURL    string
}

type EmbedRequest struct {
    Input           []string `json:"input"`
    Model           string   `json:"model"`
    InputType       string   `json:"input_type,omitempty"`
    OutputDimension int      `json:"output_dimension,omitempty"`
}

type EmbedResponse struct {
    Data  []EmbeddingData `json:"data"`
    Model string          `json:"model"`
    Usage UsageInfo       `json:"usage"`
}

type RerankRequest struct {
    Query     string   `json:"query"`
    Documents []string `json:"documents"`
    Model     string   `json:"model"`
    TopK      int      `json:"top_k,omitempty"`
}

type RerankResponse struct {
    Data  []RerankData `json:"data"`
    Model string       `json:"model"`
    Usage UsageInfo    `json:"usage"`
}
```

---

## Feedback System

### Explicit Feedback

Every search result gets a letter ID (a-z). Feedback callout at bottom of every search:

```
Feedback: sift feedback a1b2c3 --positive a,b --negative c
```

### Feedback Scoring

Bayesian-smoothed ratio applied as multiplicative boost:

```
feedback_boost(chunk_id):
    good = count positive signals for chunk
    bad  = count negative signals for chunk
    total = good + bad

    if total == 0: return 1.0

    ratio = (good + 1) / (good + bad + 2)  # Bayesian smoothing
    return 0.7 + (0.6 * ratio)             # Maps [0,1] → [0.7, 1.3]
```

Examples:
- 0 signals → 1.0 (neutral)
- 1 good, 0 bad → 1.1
- 3 good, 0 bad → 1.18
- 0 good, 3 bad → 0.82
- 10 good, 1 bad → 1.21

---

## Logging

### Search Logs (searches-YYYY-MM-DD.jsonl)

```jsonl
{"ts":"2026-02-06T10:30:00Z","id":"a1b2c3","query":"auth flow","collections":["vault"],"filters":{"since":"48h"},"result_count":20,"top_scores":[0.87,0.72,0.65],"latency":{"total_ms":142,"bm25_ms":12,"vector_ms":28,"rerank_ms":95},"tokens":{"embed":42,"rerank":1200}}
```

### Feedback Logs (feedback-YYYY-MM-DD.jsonl)

```jsonl
{"ts":"2026-02-06T10:31:00Z","search_id":"a1b2c3","positive":["a","b"],"negative":["c"]}
```

### SQL Logs (sql-YYYY-MM-DD.jsonl)

```jsonl
{"ts":"2026-02-06T10:32:00Z","query":"SELECT * FROM chunks LIMIT 10","rows_returned":10,"latency_ms":2}
```

Weekly rotation, Monday-indexed. Configurable retention (default: 12 weeks).

---

## Cross-Machine Sync

### What Syncs (via git)

- Source files (markdown, text, JSONL) in registered collections

### What Stays Local

- `~/.sift/sift.db` (embeddings, chunks, feedback, usage)
- `~/.sift/bleve/` (BM25 index)
- `~/.sift/logs/`
- `~/.sift/config.toml` (API keys)

### Workflow

Machine A: Edit files, commit, push
Machine B: Pull, run `sift refresh` (auto-detects new/changed files, re-embeds)

The refresh is incremental -- only changed files are re-processed.

---

## Project Structure

```
sift/
├── cmd/sift/
│   └── main.go
├── internal/
│   ├── config/
│   │   └── config.go          # TOML parsing, defaults
│   ├── db/
│   │   ├── db.go              # SQLite connection, queries
│   │   ├── schema.go          # Schema definitions
│   │   └── migrations.go      # Version-based migrations
│   ├── voyage/
│   │   └── client.go          # Voyage API client (embed + rerank)
│   ├── bm25/
│   │   ├── bleve.go           # Bleve BM25 index
│   │   └── vector.go          # Binary-vector similarity search
│   ├── index/                 # Folder-index (sift.toml) source of truth
│   │   ├── schema.go          # FolderIndex / FileEntry / SchemaVersion = 1
│   │   ├── parser.go          # TOML round-trip parser
│   │   ├── writer.go          # Deterministic emit (byte-identical round-trip)
│   │   ├── freshness.go       # (content_hash, head_hash, tail_hash, words)
│   │   ├── sigcompute.go      # Compute freshness tuple from a file
│   │   ├── maintain.go        # Per-folder reconciliation
│   │   ├── check.go           # Lint pass (powers `sift index check`)
│   │   ├── load.go            # Bulk tree load
│   │   ├── sections.go        # Per-file H1/H2 outline
│   │   ├── render_json.go     # `sift index --json` envelope
│   │   ├── render_md.go       # `sift index --markdown` tree
│   │   └── filter.go          # --path / --since / --file
│   ├── aigen/                 # LLM-driven sift.toml generation
│   │   ├── client.go          # DeepInfra HTTP client
│   │   ├── prompt.go          # Versioned prompt template (v5)
│   │   ├── partition.go       # Token-aware folder→batch partitioning
│   │   ├── pool.go            # Bounded worker pool, per-folder retry
│   │   └── detach.go          # Fork-to-background helper
│   ├── chunk/
│   │   ├── chunker.go         # Row-based chunker (skips sift.toml)
│   │   ├── frontmatter.go     # YAML / TOML frontmatter extraction
│   │   ├── section_chunker.go # Section-boundary chunker (md/qmd)
│   │   └── provenance.go      # XML source tags prepended to chunks
│   ├── search/
│   │   ├── search.go          # Main search orchestration
│   │   ├── rrf.go             # RRF with boosting
│   │   ├── scoring.go         # Time decay, feedback boosts
│   │   ├── adaptive.go        # Score-cliff top-K
│   │   ├── dedup.go           # Identical-content grouping
│   │   └── decorate.go        # Folder-index decoration (--with-index)
│   ├── sync/
│   │   └── refresh.go         # Incremental sync logic
│   ├── daemon/                # Optional warm-process server (unix socket, HTTP/1.1)
│   │   ├── server.go
│   │   ├── client.go
│   │   └── lifecycle.go       # GetStatus, Stop, Restart
│   ├── cli/
│   │   ├── root.go
│   │   ├── config.go          # config init/show/set/get/stats/health/rebuild/purge/logs
│   │   ├── collections.go
│   │   ├── refresh.go
│   │   ├── search.go
│   │   ├── feedback.go
│   │   ├── sql.go
│   │   ├── index.go           # `sift index` and `sift index check`
│   │   ├── daemon.go          # `sift daemon start|stop|restart|status|logs`
│   │   ├── aigen.go           # --generate=... orchestration
│   │   ├── keywords.go
│   │   ├── links.go
│   │   └── eval.go
│   └── log/
│       └── logger.go          # JSONL logging, rotation
├── go.mod
├── go.sum
├── Makefile
├── SPEC.md
├── RESEARCH.md
└── README.md
```

---

## Dependencies

```
github.com/spf13/cobra            # CLI framework
github.com/pelletier/go-toml/v2   # TOML config parsing
github.com/blevesearch/bleve/v2   # BM25 full-text search (v2.5.7)
modernc.org/sqlite                # SQLite driver (pure Go, no CGo)
github.com/gofrs/flock            # File locking
github.com/cespare/xxhash/v2      # Fast hashing
```

---

## Decisions Made

| Question | Decision | Rationale |
|----------|----------|-----------|
| CLI namespace | `sift config init`, admin under `sift config` | Clean top-level, all management unified |
| Top-level commands | `search`, `refresh`, `feedback`, `sql`, `collections`, `config` | Minimal, most-used commands accessible |
| Reranker model | `rerank-2.5-lite` | Latest, best quality/speed for lite tier |
| Embedding model | `voyage-4-lite` with 1024 dims | Good quality, fast, configurable dims |
| Asymmetric embeddings | `input_type: query` vs `document` | Free quality win for retrieval |
| Scoring ownership | SIFT owns all scoring (moved from Mem) | Single engine for retrieval quality |
| Provenance boosting | Not implemented (no folder-based boosts) | Structure not finalized yet |
| SQLite driver | `modernc.org/sqlite` (pure Go) | No CGo, simpler builds |
| Voyage client | Custom HTTP client (no third-party SDK) | Two endpoints, no dependency needed |
| Chunking | Single row-based strategy for all file types | Simple, configurable, good enough for start |
| Missing vectors on search | Search what's available, no blocking | Consistency issues are short-lived |
| Deleted files | Purge as part of refresh | Keep index clean |
| BM25 implementation | Bleve with configurable analyzer | Mature, fast, single index |
| RRF boost params | top 3 positions, 2x boost | QMD-proven effective |
| Time decay field | File mtime | Simple, observable |
| Feedback format | `--positive a,b --negative c` | Ergonomic for agents and humans |
| Usage tracking | `api_usage` table + `sift config stats` | Cost awareness |
| Search ID format | 6-8 char alphanumeric | Short, memorable |
| Overlap | ±5 rows context for embedding only | Storage efficient |
| Init behavior | `sift config init` creates config + DB | Explicit step |
| SQL access | Read-only with logging | Safe debugging |
| API errors | Retry 3x, exponential backoff, dead letter queue | Resilient |
| Concurrency | flock file locking | Prevent corruption |
| Config format | TOML | Readable, well-supported |

---

## Open Questions & Uncertainties

### Optimal Embedding Dimensions

Voyage 4 Lite supports 256, 512, 1024, 2048 dims.
- Lower dims = faster, cheaper, less accurate?
- Need benchmarking on actual data.

**To resolve:** Start with 1024, experiment with 256/512.

### BM25 Analyzer Tuning

Bleve's "standard" analyzer may not be optimal for:
- Code snippets
- Technical jargon
- Conversation text

**To resolve:** Test alternatives, make configurable.

### Scoring Weight Defaults

Current defaults are educated guesses:
- recency_weight: 0.2
- recency_half_life_days: 30

**To resolve:** Tune based on feedback data.

### Batch Size for Embedding

Voyage supports up to 1000 texts per batch.
- Optimal size depends on text length and total token limits.
- Default 128 is conservative.

**To resolve:** Start with 128, monitor API errors, increase if stable.

### Dead Letter Retry Strategy

Current: manual retry via `sift config retry-dead-letters`.
- Should auto-retry on next refresh?

**To resolve:** Start manual, add auto-retry if needed.

### Path as BM25 Content

Currently: include file path in BM25 index.
- Weight relative to content?
- Separate field or concatenated?

**To resolve:** Implement as concatenated, tune if needed.

---

## Implementation Priority

### Phase 1: Foundation
1. `sift config init` + TOML config + SQLite schema
2. `sift collections add/list/remove`
3. `sift refresh` (file scanning + chunking + BM25 indexing via Bleve) -- no embeddings yet
4. `sift search` with BM25 only

### Phase 2: Vector Search
5. Voyage embedding client + `api_usage` tracking
6. Embedding during `sift refresh`
7. Vector similarity search
8. RRF fusion (hybrid BM25 + vector)

### Phase 3: Reranking & Scoring
9. Voyage reranking integration
10. Time decay scoring
11. `sift config stats`

### Phase 4: Feedback Loop
12. `sift feedback` command + SQLite storage
13. Feedback boost in scoring
14. `sift config health`

### Phase 5: Polish
15. `sift sql`
16. `sift config logs`
17. `sift config rebuild-bm25`
18. `sift config purge`
19. Error handling refinement, dead letters

---

## Folder Indexes (`sift.toml`)

`sift.toml` is a committed, per-folder metadata file. It carries
mechanical content signatures (managed by `sift refresh`) plus optional
editorial text (`purpose`, `use_when`, file `summary`) written by an LLM
or by hand. This section is the canonical contract for downstream
consumers (mem, agents).

### Schema version 1

Top-level keys:

| Key | Type | Source | Default | Notes |
|-----|------|--------|---------|-------|
| `schema_version` | int | tool | `1` | required; higher versions are rejected |
| `ignore` | bool | human/LLM | `false` | "do not recommend" routing flag; does NOT exclude content from search |
| `purpose` | string | LLM/human | `""` | sticky paragraph; multiline OK |
| `use_when` | string[] | human | `[]` | hand-edited routing cues; never auto-generated |
| `[refresh]` | table | tool | `{}` | machine-managed counts |
| `[refresh].file_count` | int | tool | `0` | indexed file count after `.siftignore` filter |
| `[refresh].word_count` | int | tool | `0` | sum of `[files."*"].words` |
| `[files."<rel>"]` | table[] | tool/LLM | none | per-file entries; keys sorted alphabetically |
| `[folders."<name>"]` | table[] | tool | none | direct-child folder entries; keys sorted alphabetically |

Per-file entry (`[files."<rel-path>"]`):

| Key | Type | Source | Default | Notes |
|-----|------|--------|---------|-------|
| `ignore` | bool | human/LLM | `false` | "do not recommend"; metadata-only |
| `content_hash` | string | tool | `""` | xxhash64 of full content, hex |
| `head_hash` | string | tool | `""` | xxhash64 of joined first 500 words |
| `tail_hash` | string | tool | `""` | xxhash64 of joined last 500 words |
| `words` | int | tool | `0` | whitespace-split word count |
| `summary` | string | LLM/human | `""` | ≤2 sentences, ≤200 chars by convention |

Per-child-folder entry (`[folders."<name>"]`):

| Key | Type | Source | Default | Notes |
|-----|------|--------|---------|-------|
| `ignore` | bool | human/LLM | `false` | "do not descend / recommend" |

### Invariants

- **Determinism**: byte-identical input produces byte-identical output.
  Field order is fixed; map keys sorted alphabetically.
- **Exclusion**: `sift.toml` is never chunked, embedded, or returned as
  a search result. The chunker explicitly skips it.
- **Sticky editorial**: hand-edited `purpose` / `use_when` / `summary`
  survive every refresh unless `--generate=all` is used (or, for stale
  entries, `--generate=stale`).
- **Freshness tuple**: `(content_hash, head_hash, tail_hash, words)`.
  Drift in any element flips a stored `summary` to stale.

### Versioning policy

- `schema_version = 1` is the initial schema.
- Bumping `schema_version` is a breaking change. Future versions must
  ship a migration in `internal/index/parser.go`.
- Files with no `schema_version` set are accepted as `1` for
  backwards compatibility.
- Files with `schema_version` higher than the binary's `SchemaVersion`
  are rejected with a `parse_error` defect.

### Example file

```toml
schema_version = 1

purpose = """
SIFT architecture and pipeline overview.
"""

use_when = ["pipeline question", "design rationale"]

[refresh]
file_count = 2
word_count = 3120

[files."architecture.md"]
content_hash = "1f2a8c40e31d7b22"
head_hash    = "8b3c12ee04f00009"
tail_hash    = "44e9bbeebf112233"
words        = 1240
summary      = "BM25 + vector + RRF + rerank pipeline overview."
```

## `sift index --json` Envelope

The read view emits a single JSON object. Field order is fixed by the
encoder.

```json
{
  "schema_version": 1,
  "collection": "vault",
  "root": "/Users/me/vault",
  "sub_path": "docs",
  "digest": "docs/ - architecture overview ... 12 folders, 84 files",
  "stats": {
    "folder_count": 12,
    "file_count": 84,
    "total_bytes": 412938,
    "total_words": 51284,
    "newest_mtime": "2026-05-01T12:14:33Z"
  },
  "folders": [
    {
      "path": "docs",
      "depth": 0,
      "has_index": true,
      "ignore": false,
      "parse_error": "",
      "purpose": "...",
      "use_when": ["..."],
      "file_count": 4,
      "word_count": 3120,
      "last_modified": "2026-05-01T12:14:33Z",
      "files": [
        {
          "path": "architecture.md",
          "kind": "md",
          "ignore": false,
          "bytes": 8420,
          "words": 1240,
          "mtime": "2026-04-30T18:02:11Z",
          "content_hash": "1f2a...",
          "summary": "...",
          "exists": true,
          "sections": [
            {"heading": "Data Flow", "level": 2, "start_line": 3, "end_line": 24}
          ]
        }
      ]
    }
  ],
  "errors": []
}
```

| Field | Type | Notes |
|-------|------|-------|
| `schema_version` | int | mirrors `sift.toml` schema version |
| `collection` | string | omitted when empty |
| `root` | string | absolute filesystem path of the walked tree |
| `sub_path` | string | relative subtree if scoped, else "" |
| `digest` | string | one-paragraph orientation (agent-friendly) |
| `stats` | object | folder/file/byte/word totals, newest mtime (RFC3339) |
| `folders` | array | flat list, sorted by path, depth-stamped |
| `errors` | array | non-fatal parse errors, one per affected folder |

`folders[].files[].sections` is omitted when `--sections=false` (default
in markdown output).

## `sift refresh --progress=json` NDJSON Events

When `--progress=json`, refresh emits one JSON object per line on
stdout. Every event has an `event` discriminator.

### Event types

| Event | Trigger | Required keys |
|-------|---------|---------------|
| `scan_done` | tree walk completes | `event`, `folders_total`, `stale`, `skipped`, `timestamp` |
| `folder_start` | worker picks up a folder | `event`, `path`, `files`, `words`, `timestamp` |
| `folder_done` | folder generated successfully | `event`, `path`, `ms`, `tokens_in`, `tokens_out`, `cost_usd`, `attempts`, `timestamp` |
| `folder_error` | folder failed (after retries) | `event`, `path`, `err`, `attempts`, `dead_lettered`, `timestamp` |
| `heartbeat` | periodic (every few seconds) | `event`, `in_flight`, `completed`, `queue_remaining`, `elapsed_s`, `eta_s`, `spent_usd`, `timestamp` |
| `summary` | terminal | `event`, `summary` (object below) |

### `summary.summary` payload

```json
{
  "collection": "vault",
  "folders_total": 42,
  "folders_stale": 7,
  "folders_done": 40,
  "folders_failed": 2,
  "folders_dead_lettered": 2,
  "wall_seconds": 31.2,
  "concurrency": 20,
  "calls_total": 42,
  "calls_retried": 3,
  "rate_limited": 0,
  "timed_out": 0,
  "tokens_in": 80312,
  "tokens_out": 7042
}
```

### Schema notes

- All timestamps are RFC3339 strings.
- Optional fields are omitted (not emitted as `null`).
- `cost_usd` and `spent_usd` are decimals; round at display time.
- `summary` is always the **last** event of a successful run.
- A run that errors before reaching `summary` may emit partial
  `folder_*` events; consumers should treat absence-of-`summary` as an
  abort.

## Version History

| Version | Date | Changes |
|---------|------|---------|
| 0.1.0 | 2026-02-06 | Initial specification |
| 0.2.0 | 2026-02-06 | Revised CLI namespace, added Mem integration context, updated dependencies, added research findings |
| 0.5.0 | 2026-05-09 | Added folder-index spec (sift.toml schema v1, `sift index` JSON envelope, refresh NDJSON events); package rename `internal/index` → `internal/bm25` |
