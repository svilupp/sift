# SIFT

Local-first hybrid search engine (BM25 + vector + RRF fusion + reranking).

## Commands

```bash
go test ./...                    # all tests
go test -v -race ./...           # verbose with race detector
go build -o sift ./cmd/sift      # build binary
make build                       # or use Makefile
```

## Architecture

```
cmd/sift/main.go                 # entry point
internal/
  cli/                           # Cobra commands (search, refresh, config, feedback, sql)
  config/                        # TOML config at ~/.sift/config.toml
  db/                            # SQLite layer (modernc.org/sqlite, no CGo)
  search/                        # Search orchestration, RRF fusion, scoring
  index/                         # Bleve BM25 + vector storage/search
  sync/                          # Filesystem scan + incremental indexing
  chunk/                         # Row-based text chunking with overlap
  cache/                         # Search result cache (TTL + max entries)
  voyage/                        # Voyage API client (embeddings + reranking)
  fileutil/                      # Line-range file reader for previews
  ignore/                        # .siftignore pattern matching (doublestar globs)
  log/                           # JSONL weekly-rotated logs
```

## Key Types & Locations

**Database** — `internal/db/db.go`
- `execer` interface (line ~14): shared by `*sql.DB` and `*sql.Tx`
- `DB` struct (line ~21): wraps `*sql.DB`
- `WithTx(fn)` (line ~90): transaction helper with auto commit/rollback
- Schema at `internal/db/schema.go` (version 3)

**Search** — `internal/search/search.go`
- `Engine` struct (line ~60): orchestrates full pipeline
- `SearchOptions` (line ~20): collection, since, top_k, threshold, adaptive
- `Result` / `SearchResult` (lines ~29, ~46): per-chunk and aggregate results
- Pipeline: BM25 -> Vector -> RRF fusion -> Rerank -> Scoring -> Adaptive top-K

**Scoring** — `internal/search/scoring.go`
- `ComputeRecency()`: exponential decay (half-life configurable)
- `FeedbackBoost()`: Bayesian-smoothed positive/negative signal
- `ComputeFinalScore()`: base * (1 + recency) * feedback_boost

**RRF Fusion** — `internal/search/rrf.go`
- `RankedResult` (line ~7): chunk + rank + score
- `FusedResult` (line ~14): merged BM25 + vector ranks
- Formula: `score = Σ(weight * boost / (k + rank))`

**Adaptive Top-K** — `internal/search/adaptive.go`
- Score-cliff detection between positions 1-5
- Linear interpolation between minK and maxK

**Config** — `internal/config/config.go`
- `Config` struct (line ~10): nested TOML sections (API, Embedding, Reranking, Chunking, Search, Scoring, Cache, etc.)
- `Defaults()` (line ~91): all default values
- Paths: `~/.sift/` dir, `config.toml`, `sift.db`, `bleve/`, `.lock`

**Chunking** — `internal/chunk/chunker.go`
- `Chunk` struct (line ~9): order, start/end lines, content, char count
- `FromLines()` (line ~42): row-based splitting with overlap and tail merge
- Provenance at `internal/chunk/provenance.go`: XML source tags prepended to chunks

**Voyage Client** — `internal/voyage/client.go`
- `Embed()`: batch text -> vectors (voyage-4-lite, binary dtype)
- `Rerank()`: query + docs -> relevance scores (rerank-2.5-lite)
- Retry with exponential backoff (3 attempts)

**Cache** — `internal/cache/cache.go`
- SHA256 key from query + collection + 5-min time bucket
- `Get()` / `Put()` with TTL expiration and lazy eviction

**Indexing** — `internal/sync/refresh.go`
- `Refresh()` (line ~62): scan -> diff -> chunk -> embed -> store
- xxhash64 for content change detection
- Dead letters for failed embed/rerank operations

**Ignore** — `internal/ignore/ignore.go`
- `LoadPatterns(collectionPath)`: reads `{path}/.siftignore`, returns `[]string`
- `ShouldIgnore(path, root, patterns)`: gitignore-style glob matching via `doublestar`
- Integrated in `sync/refresh.go` (both `Refresh()` and `RefreshFiles()`)

**Dedup** — `internal/search/dedup.go`
- `DedupIdenticalContent(candidates)`: groups by xxhash of chunk content
- Keeps highest-scored, builds `DuplicateRef` list for rest
- Integrated in search pipeline at `search.go:293` (after scoring, before MaxChunksPerFile)

## Patterns

- **execer interface**: DB methods accept `execer` so they work in both raw and transactional contexts (`db/db.go:14`)
- **WithTx**: all multi-step DB mutations wrapped in `db.WithTx(func(tx *db.Tx) error { ... })`
- **Dead letters**: failed API calls stored in `dead_letters` table, retried via `config retry-dead-letters`
- **File locking**: `cli/lock.go` uses `gofrs/flock` TryLock for refresh/config mutations
- **Error wrapping**: `fmt.Errorf("context: %w", err)` throughout
- **Early return**: reduce nesting, handle errors immediately
- **`.siftignore`**: collection-root file with gitignore-style globs, loaded once per refresh, cached per collection in `RefreshFiles()`
- **Content dedup**: xxhash-based identical chunk grouping in search results, gated by `config.Search.DedupIdenticalContent`

## CLI Namespace

```
sift search <query> [--collection -c] [--since] [--top-k -k] [--json|--files|--pretty]
sift refresh [files...] [--collection -c] [--full] [--dry-run]
sift feedback <search_id> --positive a,b --negative c
sift collections add|remove|list
sift config init|show|set|get|stats|health|logs|rebuild-bm25|purge|retry-dead-letters
sift sql <query> [--limit]
```

## Data Flow

```
Files on disk
  -> sync/refresh (scan, hash, diff)
  -> ignore/patterns (skip .siftignore matches)
  -> chunk/chunker (row-based split + overlap)
  -> voyage/client (embed batch, token-aware)
  -> db + index/bleve (store chunks, embeddings, BM25 docs)

Query
  -> search/engine (BM25 + vector parallel)
  -> search/rrf (reciprocal rank fusion)
  -> voyage/client (rerank top-N)
  -> search/scoring (recency + feedback boost)
  -> search/dedup (identical content grouping)
  -> search/adaptive (score-cliff top-K)
  -> cli/search (format output)
```
