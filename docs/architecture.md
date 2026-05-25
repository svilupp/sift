# Architecture

## Data Flow

```
Files on disk
  -> sync/refresh (scan, hash, diff)
  -> ignore/patterns (skip .siftignore matches)
  -> chunk/chunker (row-based split with overlap; sift.toml is excluded)
  -> voyage/client (batch embed, token-aware)
  -> db + bm25/bleve (store chunks, embeddings, BM25 docs)

Files on disk (parallel pass, same command)
  -> index/maintain (read sift.toml, recompute hashes, mark stale)
  -> aigen/pool (optional, opt-in: --generate=missing|stale|all)
  -> index/writer (deterministic emit; round-trips byte-identical)
```

```
Query
  -> search/engine (BM25 + vector in parallel)
  -> search/rrf (reciprocal rank fusion)
  -> voyage/client (rerank top-N)
  -> search/scoring (recency + feedback + path boost)
  -> search/dedup (identical content grouping)
  -> search/adaptive (score-cliff top-K)
  -> index/load + filter (decorate hits with folder purpose/use_when)
  -> cli/search (format output)
```

## Indexing Pipeline

### Scanning

`sift refresh` walks each collection directory, computing an xxhash64 of each file's content. Files whose hash hasn't changed since the last refresh are skipped. This makes incremental refreshes fast — only new/modified files are reprocessed.

### Chunking

Files are split into 25-line chunks with 5-line overlap (configurable). Chunks shorter than 200 characters are merged with their neighbors. Each chunk carries provenance metadata (file path, line range, collection name) prepended as XML tags, so the embedding model has source context.

### Embedding

Chunks are batched (200 per request) and sent to Voyage AI's `voyage-4-lite` model. Embeddings use binary quantization (512 dimensions), making them ~32x smaller than float32 vectors. This keeps storage and similarity computation fast.

Failed API calls are stored in a `dead_letters` table and can be retried later via `sift config retry-dead-letters`.

## Search Pipeline

### 1. BM25 (keyword search)

[Bleve](https://blevesearch.com/) provides full-text search with the standard analyzer. BM25 scores are based on term frequency and inverse document frequency. Bleve also extracts highlight fragments used to center smart previews.

### 2. Vector Search (semantic)

Cosine similarity on binary embeddings stored in SQLite. The query is embedded with `input_type: query` for optimal retrieval performance.

### 3. Reciprocal Rank Fusion (RRF)

BM25 and vector results are merged using RRF:

```
score = Σ(weight * boost / (k + rank))
```

- `k = 60` (configurable) — controls how much top ranks are favored
- Top 3 results from each signal get a 2x weight multiplier, so strong matches from either signal rise to the top
- BM25 and vector weights default to 1.0 and can be tuned independently

### 4. Reranking

The top 75 fused candidates are sent to Voyage's `rerank-2.5-lite` model, which rescores them for semantic precision. This is the most expensive step but significantly improves result quality.

### 5. Scoring

After reranking, a multi-signal scoring formula is applied:

```
final = base_score * (1 + recency * weight) * feedback_boost * path_boost
```

**Recency decay** — exponential with a 30-day half-life. Recent files score higher; old files decay but never disappear.

**Feedback boost** — Bayesian-smoothed signal from `sift feedback`. Range [0.7, 1.3]. Even a single thumbs-up or thumbs-down shifts future rankings.

**Path boost** — configurable glob patterns in `config.toml`. Use this to permanently boost or demote paths (e.g., boost `**/topics/work/**`, demote `**/archive/**`).

### 6. Content Deduplication

Identical chunks (by xxhash of content) are grouped. The highest-scored version is kept as the primary result; duplicates are listed as "Also in:" references.

### 7. Adaptive Top-K

When `--top-k` isn't explicitly set, SIFT detects score cliffs between positions 1-5. If there's a clear quality drop-off, it returns fewer results (minimum 10) instead of padding with low-quality matches. When scores are uniform, it returns up to 20.

## Feedback Loop

The feedback system creates a closed loop for ranking improvement:

1. User searches and sees labeled results (`[a]`, `[b]`, ...)
2. User records feedback: `sift feedback <id> --positive a,b --negative d`
3. Feedback is stored per-chunk in SQLite
4. On subsequent searches, chunks with positive feedback get a boost (up to 1.3x); negative feedback applies a penalty (down to 0.7x)
5. The Bayesian smoothing prevents a single signal from dominating

## JSONL Logging

Every search, feedback event, and SQL query is logged to weekly-rotated JSONL files in `~/.sift/logs/`. This enables:

- Search analytics (what queries are run, how many results, latency)
- Feedback tracking
- Debugging and performance monitoring

View recent logs with `sift config logs`.

## Storage

SIFT uses two storage backends:

- **SQLite** (`modernc.org/sqlite`, pure Go) — files, chunks, embeddings, feedback, API usage, search sessions, dead letters
- **Bleve** — BM25 full-text index

Both live in `~/.sift/`. The `sift config health` command checks consistency between them.

## Caching

Search results are cached in SQLite with a SHA256 key derived from query + collection + 5-minute time bucket. Cache entries expire after 5 minutes (configurable) and are lazily evicted. The cache is invalidated after `sift refresh` or `sift feedback`.

## Folder Indexes (`sift.toml`)

SIFT keeps a per-folder metadata file, `sift.toml`, alongside the
content. It carries:

- mechanical fields (`content_hash`, `head_hash`, `tail_hash`, `words`,
  `file_count`) maintained by every `sift refresh`;
- editorial fields (`purpose`, `use_when`, file-level `summary`) written
  by an LLM when the user opts in via `--generate=missing|stale|all`,
  or hand-edited.

These files are committed alongside the content. They are
**never** chunked, embedded, or returned as search results — the
chunker explicitly skips `sift.toml`. The full schema lives in
[Folder Indexes / Format reference](folder-indexes/format.md).

### Why two passes in one command

`sift refresh` does mechanical maintenance unconditionally (cheap,
deterministic, byte-stable round-trip). LLM generation is opt-in and
runs after the chunk/embed pass:

```
sift refresh
  ├── chunk + embed corpus
  └── maintain sift.toml
        ├── always: hashes / counts / signatures
        └── opt-in: LLM purpose / use_when / summary  (--generate=...)
```

`--no-index` skips the second phase entirely; `--index-only` runs only
the second phase.

### `internal/index/`

The `internal/index/` package is the single source of truth for the
file format and the maintenance pipeline:

- `schema.go` — the in-memory `FolderIndex` shape and `SchemaVersion = 1`.
- `parser.go` / `writer.go` — TOML round-trip; output is
  byte-deterministic for byte-identical input.
- `freshness.go` — defines the freshness tuple
  `(content_hash, head_hash, tail_hash, words)`.
- `sigcompute.go` — computes the freshness tuple from a file.
- `maintain.go` — diff-and-update for a folder: reconciles disk vs.
  recorded entries.
- `check.go` — read-only lint pass; powers `sift index check`.
- `load.go` / `sections.go` — bulk load and per-file H1/H2 outline.
- `render_json.go` / `render_md.go` — `sift index` output formats.
- `filter.go` — `--path` / `--since` / `--file` filter engine, shared
  between `sift index` and `sift index check`.

Note: `internal/index/` was **renamed** from the previous BM25-only
package; the BM25 / vector storage code now lives at `internal/bm25/`.
This was a Phase-0 rename to free the `index` name for folder-index
work. External imports of `sift/internal/index` from before the rename
need to switch to `sift/internal/bm25`.

### `internal/aigen/`

The `internal/aigen/` package owns LLM-driven summary generation:

- `client.go` — DeepInfra HTTP client.
- `prompt.go` (+ `prompts/`) — versioned prompt template (currently
  v5).
- `partition.go` — splits a folder's files into batches that fit a
  token budget while preserving authority order.
- `pool.go` — bounded worker pool. Concurrency is per **folder slot**
  (not per file): one folder is in flight per slot, retries are
  scoped to the folder, and dead-lettering is per folder.
- `detach.go` — fork-to-background helper used by `sift refresh
  --detach`; writes a PID file under `~/.sift/`.

Failures land in the `dead_letters` table with `kind='index_summary'`
and re-run via `sift config retry-dead-letters`.

### `internal/bm25/` rename

Before Phase-0, the BM25 + vector storage layer was `internal/index/`.
That name was needed for the folder-index work (`sift index` is a
top-level command, and the package supporting it is `internal/index/`).
The rename was a pure mechanical move; no behaviour changed.

Migration note: any external code importing `sift/internal/index` for
BM25 storage should switch to `sift/internal/bm25`.

### Exclusion invariant

`sift.toml` files are explicitly skipped by `internal/chunk/chunker.go`
so they never enter the chunk pipeline. This keeps the search corpus
free of metadata noise and prevents a circular dependency where folder
descriptions would leak into search results that themselves carry
folder descriptions.
