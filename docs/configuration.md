# Configuration

SIFT stores its configuration in `~/.sift/config.toml`. You can edit it directly or use `sift config set`/`get`.

```bash
sift config show               # print current config
sift config set <key> <value>  # set a value
sift config get <key>          # read a value
```

## Full Reference

### `[api]`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `voyage_api_key` | string | `""` | Voyage AI API key. Enables vector search and reranking. |
| `request_timeout_seconds` | int | `60` | HTTP timeout for API calls. |

### `[embedding]`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `model` | string | `"voyage-4-lite"` | Embedding model. |
| `dimensions` | int | `512` | Vector dimensions. |
| `output_dtype` | string | `"binary"` | Output type: `float` or `binary`. Binary is ~32x smaller. |
| `batch_size` | int | `200` | Texts per embedding API call. |
| `input_type_query` | string | `"query"` | Input type for query embeddings. |
| `input_type_document` | string | `"document"` | Input type for document embeddings. |

### `[reranking]`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `model` | string | `"rerank-2.5-lite"` | Reranking model. |
| `enabled` | bool | `true` | Enable neural reranking after fusion. |
| `top_n` | int | `75` | Number of candidates to rerank. |

### `[chunking]`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `rows_per_chunk` | int | `25` | Lines per chunk. |
| `overlap_rows` | int | `5` | Overlapping lines between adjacent chunks. |
| `min_chunk_chars` | int | `200` | Merge chunks shorter than this with neighbors. |
| `skip_empty_rows` | bool | `true` | Ignore blank lines when chunking. |

### `[search]`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `default_top_k` | int | `20` | Default number of results. |
| `preview_chars` | int | `500` | Preview length in characters. |
| `preview_lines` | int | `0` | When > 0, pretty mode shows N lines centered on match. |
| `bm25_weight` | float | `1.0` | BM25 weight in RRF fusion. |
| `vector_weight` | float | `1.0` | Vector weight in RRF fusion. |
| `rrf_k` | int | `60` | RRF constant k (higher = less weight to top ranks). |
| `rrf_boost_top_n` | int | `3` | Top-N results from each signal get boosted weight. |
| `rrf_boost_factor` | float | `2.0` | Boost multiplier for top-N results. |
| `max_chunks_per_file` | int | `3` | Max chunks per file in results. |
| `max_chunks_for_reranking` | int | `3` | Max chunks per file sent to reranker. |
| `adaptive_min_k` | int | `10` | Minimum results for adaptive top-K. |
| `adaptive_max_k` | int | `20` | Maximum results for adaptive top-K. |
| `threshold` | float | `0.40` | Score threshold (only applied when reranking is active). |
| `dedup_identical_content` | bool | `true` | Group identical chunks with "Also in:" references. |

### `[scoring]`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `recency_weight` | float | `0.2` | Weight of recency signal in final score. |
| `recency_half_life_days` | int | `30` | Exponential decay half-life in days. |
| `feedback_enabled` | bool | `true` | Use feedback signals in scoring. |

#### `[[scoring.path_boost]]`

Array of glob patterns with score multipliers. First match wins.

```toml
[[scoring.path_boost]]
pattern = "**/topics/work/**"
boost = 1.3

[[scoring.path_boost]]
pattern = "**/archive/**"
boost = 0.7
```

### `[bm25]`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `analyzer` | string | `"standard"` | Bleve analyzer: `standard`, `simple`, or `keyword`. |

### `[output]`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `editor_command` | string | `"code -g {file}:{line}"` | Editor open command template. `{file}` and `{line}` are replaced. |

### `[logs]`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `rotate_weekly` | bool | `true` | Rotate log files weekly. |
| `max_weeks` | int | `12` | Number of weeks to retain logs. |

### `[cache]`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | bool | `true` | Enable search result caching. |
| `ttl_seconds` | int | `300` | Cache entry time-to-live (5 minutes). |
| `max_entries` | int | `100` | Maximum cached search results. |

### `[daemon]`

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `enabled` | bool | `true` | Currently informational; CLI gating is via `SIFT_NO_DAEMON`. |
| `idle_timeout` | duration | `"30m"` | Daemon exits after this much idle time (`"0"` = never exit). |
| `spawn_timeout` | duration | `"300ms"` | Max wait for `/health` after `daemon start` / auto-spawn. |
| `dial_timeout` | duration | `"50ms"` | Max wait when probing an existing daemon socket. |

### `[transport]`

`http.Transport` tuning shared by upstream API clients (currently
Voyage). The 5 minute idle timeout is deliberately higher than stdlib's
90 s default so the daemon's connection pool stays warm across bursty
queries.

| Key | Type | Default | Description |
|-----|------|---------|-------------|
| `max_idle_conns_per_host` | int | `8` | Idle connection pool size per host. |
| `idle_conn_timeout` | duration | `"5m"` | How long idle connections stay open. |
| `tls_handshake_timeout` | duration | `"5s"` | TLS handshake budget. |
| `response_header_timeout` | duration | `"30s"` | Time to wait for upstream response headers. |

## Environment Variables

| Variable | Description |
|----------|-------------|
| `SIFT_DIR` | Override the default `~/.sift/` data directory (relocates socket, PID, logs, db). |
| `SIFT_NO_DAEMON` | Any non-empty value disables daemon dial/spawn; CLI runs in-process. |
| `SIFT_DAEMON_SOCKET` | Override socket path (`~/.sift/sift.sock`). Test hook. |
| `SIFT_DAEMON_SPAWNED` | Set automatically when spawning the daemon child. Not for end-user use. |
| `VOYAGE_API_KEY` | Read by `sift config init` as the default for `api.voyage_api_key`. |
| `DEEPINFRA_API_KEY` | Used by `sift refresh --generate=...` for LLM-generated `sift.toml` summaries. |

## Data Directory

All SIFT data lives in `~/.sift/` (or `$SIFT_DIR`):

```
~/.sift/
  config.toml          # configuration
  sift.db              # SQLite database (files, chunks, embeddings, feedback)
  bleve/               # BM25 index
  logs/
    daemon.log         # daemon's own log (slog records)
    searches-*.jsonl   # weekly-rotated search/feedback/sql logs
  sift.sock            # daemon Unix socket (mode 0600)
  sift.pid             # daemon PID file (single-instance lock)
  refresh-index.pid    # PID file for `sift refresh --detach`
  refresh-index.log    # captured stderr for `sift refresh --detach`
  .lock                # file lock for concurrent access
```
