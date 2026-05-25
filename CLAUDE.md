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
cmd/sift/main.go                 # entry point (also runs `sift __daemon` shim)
internal/
  cli/                           # Cobra commands (search, refresh, index, config, feedback, sql, daemon)
  config/                        # TOML config at ~/.sift/config.toml
  db/                            # SQLite layer (modernc.org/sqlite, no CGo)
  search/                        # Search orchestration, RRF fusion, scoring
  bm25/                          # Bleve BM25 + vector storage/search (renamed from internal/index in Phase-0)
  index/                         # Folder-index (sift.toml): schema, parser, writer, maintain, check, load, render
  aigen/                         # LLM-driven sift.toml generation (DeepInfra client, prompt v5, worker pool, detach)
  sync/                          # Filesystem scan + incremental indexing
  chunk/                         # Row-based text chunking with overlap + frontmatter extraction; skips sift.toml
  cache/                         # Search result cache (TTL + max entries)
  voyage/                        # Voyage API client (embeddings + reranking)
  fileutil/                      # Line-range file reader for previews
  ignore/                        # .siftignore pattern matching (doublestar globs)
  log/                           # JSONL weekly-rotated logs
  daemon/                        # Optional warm-process server: unix socket, HTTP/1.1, JSON
```

## Key Types & Locations

**Database** — `internal/db/db.go`
- `execer` interface (line ~14): shared by `*sql.DB` and `*sql.Tx`
- `DB` struct (line ~21): wraps `*sql.DB`
- `WithTx(fn)` (line ~90): transaction helper with auto commit/rollback
- Schema at `internal/db/schema.go` (version 9)

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
- `Config` struct: nested TOML sections (API, Embedding, Reranking, Chunking, Search, Scoring, Cache, Daemon, Transport, etc.)
- `Default()`: all default values
- `Duration` type at `duration.go`: TOML-friendly `time.Duration` wrapper
- Path helpers: `SocketPath()`, `PIDPath()`, `DaemonLogPath()` (respect `SIFT_DAEMON_SOCKET`, `SIFT_DIR`)

**Daemon** — `internal/daemon/`
- `Serve()` / `Client` / `SpawnDetached()`: unix-socket HTTP/1.1 server with JSON; `/health`, `/search`, `/refresh` (NDJSON), `/shutdown`
- `lifecycle.go`: `GetStatus`, `Stop`, `Restart` powering the `sift daemon` subcommand

**Chunking** — `internal/chunk/chunker.go`
- `Chunk` struct (line ~9): order, start/end lines, content, char count, `IsFrontmatter` flag
- `FromLines()` (line ~64): row-based splitting with overlap, tail merge, and frontmatter extraction
- `ExtractTitle()`: frontmatter title/name keys first, then H1/H2 heading fallback
- Frontmatter at `internal/chunk/frontmatter.go`: `ParseFrontmatter()` detects YAML (`---`) and TOML (`+++`) frontmatter
- Provenance at `internal/chunk/provenance.go`: XML source tags prepended to chunks

**Voyage Client** — `internal/voyage/client.go`
- `Embed()`: batch text -> vectors (voyage-4-lite, binary dtype)
- `Rerank()`: query + docs -> relevance scores (rerank-2.5-lite)
- Retry with exponential backoff (3 attempts)
- `NewClientWithTransport()` + `Preconnect()`: shared tuned `*http.Transport` to keep TLS warm

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

**Folder Index** — `internal/index/`
- `schema.go`: `FolderIndex`, `FileEntry`, `ChildFolder`, `SchemaVersion = 1`
- `parser.go` / `writer.go`: TOML round-trip; byte-deterministic emit
- `freshness.go`: freshness tuple `(content_hash, head_hash, tail_hash, words)`
- `sigcompute.go`: compute the freshness tuple from a file
- `maintain.go`: per-folder reconciliation (disk vs. recorded entries)
- `check.go`: lint pass; powers `sift index check` (defects: missing_sift_toml, parse_error, stale, missing_summary, orphaned, schema_version)
- `load.go` / `sections.go`: bulk tree load + per-file H1/H2 outline
- `render_json.go` / `render_md.go`: `sift index` output formats; `digest` field for agent prompt context
- `filter.go`: `--path` / `--since` / `--file` filters shared between `sift index` and `sift index check`
- Exclusion invariant: `sift.toml` is never chunked or embedded (skipped by `internal/chunk/chunker.go`)

**AI Generation** — `internal/aigen/`
- `client.go`: DeepInfra HTTP client (env: `DEEPINFRA_API_KEY`)
- `prompt.go` + `prompts/`: versioned prompt template (v5)
- `partition.go`: token-aware folder→batch partitioning, authority-ordered
- `pool.go`: bounded worker pool; one folder per slot, per-folder retry, per-folder dead-letter
- `detach.go`: fork-to-background helper for `sift refresh --detach`; PID file at `~/.sift/refresh-index.pid`
- `types.go`: NDJSON `PoolEvent` schema (`scan_done`, `folder_start`, `folder_done`, `folder_error`, `heartbeat`, `summary`) + `RunSummary`
- Failures land in `dead_letters` with `kind='index_summary'`, re-queued via `sift config retry-dead-letters`

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
sift search <query> [--collection -c] [--since] [--top-k -k] [--file] [--json|--files|--pretty]
                    [--with-index|--no-index]                          # folder-context decoration (default ON)
sift refresh [files...] [--collection -c] [--full] [--dry-run]
                        [--no-index | --index-only]                    # mechanical maintenance pass
                        [--generate=missing|stale|all|none]            # LLM pass (opt-in, default none)
                        [--concurrency N] [--progress=auto|json|none]
                        [--detach] [--status] [--api-key KEY]
sift index [path] [--collection -c] [--depth N] [--json|--markdown]    # read view (TTY=md, pipe=json)
                  [--sections] [--summaries] [--include-ignored]
                  [--path GLOB] [--since DUR] [--file PATH ...]
sift index check [path] [--collection -c] [--json|--markdown]          # lint (exit 2 on defects)
sift feedback <search_id> --positive a,b --negative c
sift collections add|remove|list
sift config init|show|set|get|stats|health|logs|rebuild-bm25|purge|retry-dead-letters|purge-dead-letters
sift daemon start|stop|restart|status|logs
sift sql <query> [--limit] [--schema]
sift keywords [--collection] [--path GLOB] [--depth N] [--top N] [--json]
sift links <file> [--section ID] [--backlinks] [--json]
sift refs stamp|validate|lint [files...] [--write|--check|--fix|--strict] [--collection] [--path GLOB]
sift eval run|report
```

`search`/`refresh` auto-spawn the daemon and fall back to in-process on failure. `SIFT_NO_DAEMON=1` opts out. `--generate != none` and `--index-only` force the in-process path (the daemon does not yet surface aigen events).

## Data Flow

```
Files on disk
  -> sync/refresh (scan, hash, diff)
  -> ignore/patterns (skip .siftignore matches)
  -> chunk/chunker (row-based split + overlap; sift.toml is excluded)
  -> voyage/client (embed batch, token-aware)
  -> db + bm25/bleve (store chunks, embeddings, BM25 docs)

Files on disk (parallel pass)
  -> index/maintain (recompute hashes; mark stale)
  -> aigen/pool (opt-in: --generate=missing|stale|all)
  -> index/writer (deterministic emit; round-trips byte-identical)

Query
  -> search/engine (BM25 + vector parallel)
  -> search/rrf (reciprocal rank fusion)
  -> voyage/client (rerank top-N)
  -> search/scoring (recency + feedback boost)
  -> search/dedup (identical content grouping)
  -> search/adaptive (score-cliff top-K)
  -> index/load + filter (decorate hits with folder purpose/use_when when --with-index)
  -> cli/search (format output; human appends "[folder: ...]"; JSON adds folder_index)
```

## Folder Indexes

`sift.toml` is a per-folder, committed metadata file. It carries
mechanical hashes (managed by `sift refresh`) and editorial text
(`purpose`, `use_when`, file `summary`) that is opt-in LLM-generated or
hand-edited.

Key behaviours:

- `internal/index/` is the single source of truth for the file format.
  External consumers (mem) call `sift index` instead of parsing
  `sift.toml` directly.
- `internal/aigen/` runs the LLM pass; pool concurrency is one slot per
  folder (`--concurrency N`, default 20). Failures dead-letter with
  `kind='index_summary'`.
- `sift.toml` is never chunked or embedded; the chunker skips it.
- `sift search --with-index` (default ON) decorates each result with
  the enclosing folder's `purpose` / `use_when` / `summary`.

## Key Behaviours to Verify

- **Round-trip determinism**: `sift refresh` on an unchanged tree
  produces zero git diff. The TOML writer fixes field order and sorts
  map keys.
- **Idempotency**: re-running any `sift refresh --generate=...` mode on
  an already-up-to-date tree skips all LLM calls.
- **`sift.toml` exclusion**: search never returns `sift.toml` content
  as a chunk. Verify by `sift search '<a unique purpose snippet>'` —
  the source folder's content should hit, not the metadata file.
- **Sticky summaries**: an LLM `purpose` survives across refreshes
  unless the folder's freshness signature drifts. Hand-edits also
  survive (the writer never silently overwrites non-empty editorial
  fields outside the LLM pass).
