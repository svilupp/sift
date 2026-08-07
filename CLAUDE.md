# SIFT

Local-first hybrid search (BM25 + vector + RRF fusion + reranking) over collections of files,
plus a per-folder `sift.toml` metadata layer.

Run `make check` before finishing or handing off work (it is also a prerequisite of `make release`).
`make install` installs the `sift` binary from `./cmd/sift`. `make setup` installs golangci-lint and
goimports — `make check` fails on the lint step without them.

## Packages

| Package | Purpose |
|---|---|
| `cmd/sift` | Entry point; also hosts the `sift __daemon` shim |
| `internal/cli` | Cobra commands (registered in `root.go`) |
| `internal/config` | TOML config at `~/.sift/config.toml`; path helpers |
| `internal/db` | SQLite (modernc.org/sqlite, no CGo); `SchemaVersion = 9` in `schema.go` |
| `internal/search` | Pipeline orchestration, RRF fusion, scoring, dedup, adaptive top-K |
| `internal/bm25` | Bleve BM25 + vector storage/search (was `internal/index` pre-Phase-0) |
| `internal/index` | Folder-index (`sift.toml`) schema, parse, write, maintain, check, load, render |
| `internal/aigen` | LLM generation of `sift.toml` editorial text (DeepInfra, prompt v5, worker pool, detach) |
| `internal/sync` | Filesystem scan + incremental indexing (`refresh.go`) |
| `internal/chunk` | Row-based chunking with overlap, frontmatter extraction, provenance tags |
| `internal/cache` | Search result cache (TTL + max entries) |
| `internal/voyage` | Voyage API client: embeddings + reranking |
| `internal/anchor` | Markdown section parsing + heading slugs; link extraction; overlays |
| `internal/ref` | Stable `code_lines` / `doc_anchor` reference tokens: parse, resolve, validate, rewrite |
| `internal/keywords` | TF-IDF-style keyword extraction over indexed documents |
| `internal/readsignal` | Ingests TSV read-count exports used as a scoring signal (`LoadReadSignals`) |
| `internal/eval` | Search-quality eval harness (variants `v0_baseline`, `v1_section`, `v2_section_links`) |
| `internal/fileutil` | Line-range file reader for previews |
| `internal/ignore` | `.siftignore` glob matching (doublestar) |
| `internal/log` | JSONL weekly-rotated logs |
| `internal/daemon` | Warm-process server: unix socket, HTTP/1.1, JSON |

## Non-obvious invariants

- **`sift.toml` is never chunked or embedded.** The skip lives in `internal/sync/refresh.go` —
  `isFolderIndexFile()`, checked in both the explicit-files path and the scan path. It is *not*
  in `internal/chunk`. `internal/index/` is the single source of truth for the file format;
  external consumers (mem) call `sift index` rather than parsing `sift.toml`.
- **Round-trip determinism**: `sift refresh` on an unchanged tree produces zero git diff. The TOML
  writer fixes field order and sorts map keys.
- **Idempotency**: re-running any `--generate=...` mode on an up-to-date tree makes no LLM calls.
- **Sticky editorial text**: a generated `purpose` survives refreshes unless the folder's freshness
  signature drifts; the writer never overwrites non-empty editorial fields outside the LLM pass.
- **execer**: DB methods take the `execer` interface (`internal/db/db.go`) so they work against both
  `*sql.DB` and `*sql.Tx`. Multi-step mutations go through `db.WithTx`.
- **Dead letters**: failed API calls land in the `dead_letters` table; aigen failures use
  `kind='index_summary'`. Retry with `sift config retry-dead-letters`.
- **File locking**: `internal/cli/lock.go` uses `gofrs/flock` TryLock around refresh/config mutations.
- **Voyage retries**: `maxRetries = 5` in `internal/voyage/client.go`, exponential backoff.
  `NewClientWithTransport()` + `Preconnect()` share a tuned transport to keep TLS warm.
- **Scoring**: `ComputeFinalScore(base, mtime, now, recencyWeight, halfLifeDays, feedbackBoost)`
  = `base * (1 + recency*recencyWeight) * feedbackBoost` (`internal/search/scoring.go`).
  `PathBoost` is first-match-wins over configured glob patterns.
- **Dedup**: `DedupIdenticalContent` (`internal/search/dedup.go`) groups by xxhash of chunk text,
  keeps the highest-scored, and records `DuplicateRef`s. Called from `Engine.Search`
  (`internal/search/search.go`) after scoring, before MaxChunksPerFile, gated on
  `config.Search.DedupIdenticalContent`.
- **Cache key**: `cache.Key` (`internal/cache/cache.go`) SHA256s query + collection + `since`
  (quantized to a 5-minute bucket) + pathGlob + topK + threshold + adaptive, plus any variadic
  `extra` bools.

## Daemon

`internal/daemon` serves `GET /health`, `POST /search`, `POST /refresh` (NDJSON), `POST /shutdown`
over a unix socket. `lifecycle.go` (`GetStatus`, `Stop`, `Restart`) backs `sift daemon`.

`search`/`refresh` auto-spawn the daemon and fall back in-process on failure. `SIFT_NO_DAEMON=1`
opts out. `--index-only`, `--no-index`, and `--generate != none` force the in-process path (the
daemon does not surface aigen events); see `inProcessReason` in `internal/cli/refresh.go`.

## Env vars

| Var | Effect |
|---|---|
| `SIFT_DIR` | Data directory, default `~/.sift` |
| `SIFT_DAEMON_SOCKET` | Overrides the socket path |
| `SIFT_NO_DAEMON` | Any non-empty value forces the in-process path |
| `DEEPINFRA_API_KEY` | aigen key; env first, then `.env` files walked up from the folder, then config `deepinfra_api_key` |

Voyage key comes from config `voyage_api_key` (no env fallback). Detach PID file:
`$SIFT_DIR/refresh-index.pid`.

## CLI

```
sift search <query...> [-c collection] [--since] [-k N] [--file] [--json|--files|--pretty]
                       [--agent] [--compact] [--sections]        # agent-facing output modes
                       [--with-index|--no-index]                 # folder-context decoration, default ON
sift read <file> [-c collection] [--section TITLE|SLUG] [--start-line N] [--end-line N] [--json]
sift refresh [files...] [-c collection] [--full] [--dry-run]
                        [--no-index | --index-only]
                        [--generate=missing|stale|all|none]      # LLM pass, default none
                        [--concurrency N]                        # default 20, one folder per slot
                        [--progress=auto|json|none] [--detach] [--status] [--api-key KEY]
sift index [path] [-c collection] [--depth N] [--json|--markdown] [--sections] [--summaries]
                  [--include-ignored] [--path GLOB] [--since DUR] [--file PATH ...]
sift index check [path] [-c collection] [--json|--markdown]      # lint, exit 2 on defects
sift links <file> [--section ID] [--backlinks] [--json]
sift keywords [-c collection] [--path GLOB] [--depth N] [--top N] [--json]
sift refs stamp|validate|lint [paths...] [--write|--check|--fix|--strict] [-c collection] [--path GLOB]
sift feedback <search_id> --positive a,b --negative c
sift collections [--json]                                        # bare command lists collections
sift collections add <name> <path> | remove <name>
sift config init|show|set|get|stats|health|logs|rebuild-bm25|purge|retry-dead-letters|purge-dead-letters
sift daemon start|stop|restart|status|logs
sift eval run|report
sift sql <query> [--limit] [--schema]
```

`sift index` output defaults: markdown on a TTY, JSON when piped. `index check` defects:
`missing_sift_toml`, `parse_error`, `stale`, `missing_summary`, `orphaned`, `schema_version`.

## Data flow

```
Files -> sync/refresh (scan, xxhash64 diff)
      -> ignore (skip .siftignore matches; sift.toml skipped via isFolderIndexFile)
      -> chunk (row split + overlap, frontmatter)
      -> voyage (embed batches)
      -> db + bm25/bleve

Files -> index/maintain (recompute freshness tuple; mark stale)
      -> aigen/pool (opt-in --generate)
      -> index/writer (deterministic emit)

Query -> search/engine (BM25 + vector in parallel)
      -> search/rrf  (score = Σ weight*boost/(k+rank))
      -> voyage/rerank
      -> search/scoring -> search/dedup -> search/adaptive (score-cliff between positions 1-5)
      -> index/load + filter (decorate with folder purpose/use_when when --with-index)
```
