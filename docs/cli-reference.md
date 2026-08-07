# CLI Reference

## `sift search`

Search across collections using hybrid BM25 + vector search.

```
sift search <query> [flags]
```

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--collection` | `-c` | string | | Filter by collection name. |
| `--path` | `-p` | string | | Filter by file path glob (e.g. `"*/topics/work/*"`). |
| `--since` | | string | | Time filter: `2d`, `1w`, `4h`. |
| `--top-k` | `-k` | int | 20 | Number of results. When omitted, adaptive top-K applies; explicit value falls back to `search.default_top_k` (default 20). |
| `--json` | | bool | | Full JSON output with scores and metadata. |
| `--files` | | bool | | File paths only (one per line). |
| `--pretty` | | bool | | Human-readable with colors and editor commands. |
| `--agent` | | bool | | Agent-optimized section output without numeric scores. |
| `--compact` | | bool | | Compact agent output grouped by file/section. |
| `--reverse` | | bool | | Best results last. Enabled by default with `--pretty`. |
| `--threshold` | | float | 0.40 | Score threshold (only with reranking). |
| `--no-tips` | | bool | | Suppress trailing tip line. |
| `--read-command` | | string | | Override the drill-down command shown in agent hints, e.g. `"mem read"` from a wrapper CLI. |
| `--file` | | string[] | | Search specific file(s) directly without using the persistent index. Repeatable. |
| `--sections` | | bool | | Aggregate results to markdown sections when section metadata exists. |
| `--with-index` | | bool | true | Decorate results with folder/file context from `sift.toml`. Default ON. |
| `--no-index` | | bool | | Strip folder/file context from results (overrides `--with-index`). |

**Examples:**

```bash
sift search "authentication flow"
sift search "rate limiting" --collection vault --since 1w
sift search "API design" --path "*/topics/work/*"
sift search "database migration" --json
sift search "meeting notes" --files | xargs cat
sift search "debugging tips" --pretty --top-k 5
sift search "architecture" --agent
sift search "architecture" --agent --read-command "mem read"
sift search "rrf boost" --file docs/architecture.md --file docs/SPEC.md
sift search "store id" --json | jq '.results[0].folder_index'   # decoration
sift search "store id" --no-index                               # strip decoration
```

`--read-command` is presentation-only. It lets wrappers keep search output self-consistent without changing how SIFT indexes or searches content.

### Folder-index decoration

When `--with-index` is on (the default), each result is enriched with the
enclosing folder's `purpose` / `use_when` text from `sift.toml`. In human
mode this appears as a grey suffix:

```text
[a] docs/architecture.md:42-58  [folder: docs/ - SIFT architecture overview]
```

In JSON mode each result gains a `folder_index` field:

```json
{
  "id": "a",
  "path": "docs/architecture.md",
  "score": 0.81,
  "folder_index": {
    "folder_path": "docs",
    "folder_purpose": "SIFT architecture overview and pipeline design.",
    "folder_use_when": ["pipeline question", "design rationale"],
    "file_summary": "Describes the BM25 + vector + RRF + rerank pipeline.",
    "file_words": 612
  }
}
```

See [Folder Indexes / Agent usage](folder-indexes/agent-usage.md) for the full envelope.

## `sift links`

Show forward links or backlinks for a document and, optionally, a section anchor.

```
sift links <file> [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--section` | string | | Filter to a specific section anchor. |
| `--backlinks` | bool | | Show incoming links instead of outgoing links. |
| `--json` | bool | | Machine-readable output. |

**Examples:**

```bash
sift links docs/architecture.md
sift links docs/architecture.md --section trust-zones
sift links docs/architecture.md --backlinks
sift links docs/architecture.md --json
```

## `sift refs`

Maintain code refs and lint document links.

### `sift refs stamp`

Add missing code-ref tokens and normalize syntax.

```
sift refs stamp [files-or-directories...] [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--write` | bool | | Rewrite files in place. |
| `--check` | bool | | Dry-run and fail if any file would change. |
| `--json` | bool | | Machine-readable output. |
| `--path` | string[] | | Glob(s) used when scanning collections or directories. |
| `--collection` | string[] | | Collection(s) to scan. |
| `--token-length` | int | 3 | Token length to emit. |

**Examples:**

```bash
sift refs stamp PLAN.md --write
sift refs stamp docs/ --check
sift refs stamp --collection vault --path "repos/**/CLAUDE.md" --write
```

### `sift refs validate`

Validate code refs only and optionally apply safe repairs.

```
sift refs validate [files-or-directories...] [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--json` | bool | | Machine-readable output. |
| `--fix` | bool | | Apply safe auto-fixes in place. |
| `--strict` | bool | | Exit non-zero on stale or broken code refs. |
| `--window` | int | 120 | Nearby search window for shifted refs. |
| `--path` | string[] | | Glob(s) used when scanning collections or directories. |
| `--collection` | string[] | | Collection(s) to scan. |

**Examples:**

```bash
sift refs validate PLAN.md
sift refs validate docs/ --fix
sift refs validate --collection vault --path "repos/**/CLAUDE.md" --strict
```

### `sift refs lint`

Run the strict reference linter across files or directories.

Checks:

- code refs
- markdown links
- wikilinks
- local anchors

```
sift refs lint [files-or-directories...] [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--json` | bool | | Machine-readable output. |
| `--fix` | bool | | Apply safe code-ref fixes before reporting remaining issues. |
| `--window` | int | 120 | Nearby search window for shifted code refs. |
| `--code-only` | bool | | Lint code refs only. |
| `--doc-only` | bool | | Lint markdown links and anchors only. |
| `--path` | string[] | | Glob(s) used when scanning collections or directories. |
| `--collection` | string[] | | Collection(s) to scan. |

**Examples:**

```bash
sift refs lint docs/
sift refs lint docs/ --fix
sift refs lint --collection vault --path "repos/**/CLAUDE.md"
sift refs lint --doc-only docs/
```

**Recommended usage:**

```bash
# local editing loop
sift refs lint docs/ --fix
sift refs lint docs/

# CI
sift refs lint docs/
```

If a folder contains illustrative `file:line` examples that are not real repo refs, either lint a narrower curated folder or use `--doc-only`.

## `sift refresh`

Scan collections and update the search index. Refresh also maintains
per-folder `sift.toml` metadata in a mechanical pass (no LLM); LLM
generation of `purpose` / `use_when` / `summary` text is opt-in.

```
sift refresh [files...] [flags]
```

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--collection` | `-c` | string | | Refresh specific collection only. |
| `--full` | | bool | | Force full reindex (ignore content hashes). |
| `--dry-run` | | bool | | Show what would change without writing. |
| `--no-index` | | bool | | Skip `sift.toml` folder-index maintenance. |
| `--index-only` | | bool | | Skip chunk/embed; run only `sift.toml` maintenance. |
| `--generate` | | string | `none` | LLM mode: `missing` \| `stale` \| `all` \| `none`. |
| `--concurrency` | | int | 20 | Parallel folder workers for LLM generation. |
| `--progress` | | string | `auto` | Progress UX: `auto` \| `json` \| `none`. |
| `--detach` | | bool | | Fork to background; logs to `~/.sift/`. |
| `--status` | | bool | | Print status of a detached run and exit. |
| `--api-key` | | string | | Override `DEEPINFRA_API_KEY` for this run. |

**Examples:**

```bash
sift refresh                                    # incremental, all collections
sift refresh -c vault                           # one collection
sift refresh notes.md ideas.md                  # specific files
sift refresh --full                             # reindex everything
sift refresh --dry-run                          # preview changes
sift refresh --no-index                         # skip sift.toml maintenance
sift refresh --index-only                       # only update sift.toml signatures
sift refresh --index-only --generate=missing    # AI-bootstrap empty fields
sift refresh --index-only --generate=stale --progress=json
sift refresh --index-only --generate=all --detach
sift refresh --status                           # check on a detached run
```

!!! note
    Cannot use `--collection` with file arguments.

### `--generate` modes

| Mode | Calls LLM for | Cost shape |
|------|---------------|-----------|
| `none` (default) | nothing | free |
| `missing` | folders with empty `purpose`/`summary` only | bounded by gaps |
| `stale` | folders whose freshness signature drifted | bounded by churn |
| `all` | every folder, regardless of state | full bootstrap cost |

LLM failures are recorded in `dead_letters` with `kind='index_summary'`
and re-queued via `sift config retry-dead-letters`.

### `--progress=json` event schema

When `--progress=json`, refresh emits NDJSON to stdout (one JSON object
per line). Common events:

```json
{"event":"scan_done","folders_total":42,"stale":7,"skipped":35,"timestamp":"..."}
{"event":"folder_start","path":"docs/architecture","files":4,"words":3120}
{"event":"folder_done","path":"docs/architecture","ms":842,"tokens_in":2104,"tokens_out":312,"cost_usd":0.0008,"attempts":1}
{"event":"folder_error","path":"docs/legacy","err":"rate limited","attempts":3,"dead_lettered":true}
{"event":"heartbeat","in_flight":4,"completed":21,"queue_remaining":17,"elapsed_s":12.4,"eta_s":9.8,"spent_usd":0.014}
{"event":"summary","summary":{"folders_total":42,"folders_done":40,"folders_failed":2,"folders_dead_lettered":2,"wall_seconds":31.2,"concurrency":20,"calls_total":42,"tokens_in":80312,"tokens_out":7042}}
```

See [Folder Indexes / Refresh & generation](folder-indexes/refresh-and-generation.md) for the full event reference.

## `sift index`

Read or lint per-folder `sift.toml` metadata. With no subcommand, walks
the tree and emits a structured table of contents (purpose / use_when /
per-file summaries). On a TTY the default is markdown; piped, JSON.

```
sift index [path] [flags]
```

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--collection` | `-c` | string | | Walk this collection's root path. |
| `--depth` | | int | -1 | Limit descent depth (`0`=root, `1`=children, `-1`=unbounded). |
| `--orient` | | bool | | Emit compact semantic JSON; defaults to depth 1. |
| `--json` | | bool | | Emit JSON envelope. |
| `--markdown` | | bool | | Emit markdown tree. |
| `--include-ignored` | | bool | | Include folders/files marked `ignore = true`. |
| `--sections` | | bool | (auto) | Include H1/H2 outlines per file. On for JSON, off for markdown. |
| `--summaries` | | bool | true | Include editorial purpose/summaries; false keeps local extractive orientation. |
| `--path` | `-p` | string | | Glob filter applied to file paths (e.g. `docs/**`). |
| `--since` | | string | | Only include files modified within the duration (e.g. `7d`). |
| `--file` | | string[] | | Only include this file path (repeatable). |

With `--collection`, `[path]` is collection-relative. Returned paths
remain collection-relative for direct reuse. Format auto-detection:
stdout is a TTY → `markdown`, piped → `json`.
Pass `--json` or `--markdown` to override. `--json` and `--markdown` are
mutually exclusive.

**Examples:**

```bash
sift collections --json                   # discover collection names
sift index -c vault --orient              # semantic root + direct children
sift index docs -c vault --orient         # drill into a branch
sift index docs -c vault --json           # detailed subtree + line ranges
sift index --path 'docs/**' --since 7d    # AND-combined filters
```

### Compact orientation shape

```json
{
  "schema_version": 1,
  "collection": "vault",
  "root": "/Users/me/vault",
  "sub_path": "docs",
  "digest": "docs — Architecture: Explains the system boundaries.",
  "stats": {"folder_count": 3, "file_count": 8, "total_words": 4200},
  "folders": [{
    "path": "docs",
    "purpose": "Architecture: Explains the system boundaries.",
    "purpose_source": "extractive",
    "children": ["architecture", "guides"],
    "files": [{
      "path": "docs/README.md",
      "title": "Documentation",
      "summary": "Explains how to navigate the project documentation.",
      "summary_source": "extractive",
      "topics": ["Quick start", "Concepts"],
      "words": 640
    }]
  }]
}
```

`editorial` means the text came from `sift.toml`. `extractive`,
`headings`, and `title` are deterministic local fallbacks and never call
an AI provider. Use `--orient --summaries=false` to ignore all editorial
fields and inspect the strictly local extractive view.

### JSON envelope shape

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
      "purpose": "SIFT architecture overview.",
      "use_when": ["pipeline question"],
      "file_count": 4,
      "word_count": 3120,
      "last_modified": "2026-05-01T12:14:33Z",
      "files": [
        {
          "path": "docs/architecture.md",
          "kind": "md",
          "bytes": 8420,
          "words": 1240,
          "mtime": "2026-04-30T18:02:11Z",
          "content_hash": "1f2a...",
          "summary": "Describes the BM25 + vector pipeline.",
          "title": "Architecture",
          "excerpt": "Explains the search and indexing pipeline.",
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

The `digest` field is a one-paragraph human/agent orientation generated
from the tree. See [Folder Indexes / `sift index` deep dive](folder-indexes/sift-index-command.md).

### `sift index check`

Lint `sift.toml` files in a tree. No writes, no LLM calls. Reports
missing files, parse errors, stale entries, orphaned entries, and
unsupported schema versions. Missing summaries are optional.

```
sift index check [path] [flags]
```

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--collection` | `-c` | string | | Walk this collection's root path. |
| `--json` | | bool | | Emit JSON. |
| `--markdown` | | bool | | Emit pasteable markdown checklist. |
| `--include-ignored` | | bool | | Lint folders/files marked `ignore = true` too. |
| `--all` | | bool | | Include non-text files (e.g. `.go`, `.py`, `.rs`) in orphan checks. |
| `--require-summaries` | | bool | | Report empty file summaries as defects. |
| `--path` | `-p` | string | | Glob filter applied to file paths. |
| `--since` | | string | | Duration filter (e.g. `7d`). |
| `--file` | | string[] | | Only include this file path (repeatable). |

Format auto-detection: TTY → human, piped → JSON. Override with `--json`
or `--markdown`. `--json` and `--markdown` are mutually exclusive.

**Exit codes:**

| Code | Meaning |
|------|---------|
| `0` | no defects |
| `1` | tool error (bad path, IO failure) |
| `2` | defects found (lint-style) |

**Examples:**

```bash
sift index check                              # current directory
sift index check ./docs
sift index check --collection vault
sift index check --collection vault --require-summaries
sift index check ./docs --json | jq .summary
sift index check ./docs --markdown > REPORT.md
sift index check --path 'docs/**'
```

See [Folder Indexes / overview](folder-indexes/overview.md) for what defects
mean and how to fix them.

## `sift read`

Read a file, exact line range, or Markdown section without an editor or
API call. With `--collection`, the file is collection-relative.

```
sift read <file> [flags]
```

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--collection` | `-c` | string | | Resolve the file inside this collection. |
| `--section` | | string | | Heading title or slug, such as `Data Flow` or `data-flow`; numeric prefixes are optional. |
| `--start-line` | | int | 1 | First line to read. |
| `--end-line` | | int | EOF | Last line to read. |
| `--json` | | bool | | Emit `{file, section, start_line, end_line, content}`. |

```bash
sift read docs/architecture.md -c vault --section data-flow
sift read docs/architecture.md -c vault --start-line 40 --end-line 80 --json
```

## `sift feedback`

Record positive or negative feedback on search results to improve future rankings.

```
sift feedback <search_id> [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--positive` | string | | Comma-separated result labels (e.g. `a,b`). |
| `--negative` | string | | Comma-separated result labels (e.g. `c,d`). |

**Example:**

```bash
sift search "auth flow"                          # header shows id:abc123
sift feedback abc123 --positive a,b --negative d
```

## `sift collections`

Manage indexed collections. Run bare `sift collections` to list all, or
`sift collections --json` for the stable agent discovery envelope:
`{"collections":[{"name","path","file_count","tags"}]}`.

### `sift collections add`

```
sift collections add <name> <path> [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--tags` | string | | Comma-separated tags. |

**Example:**

```bash
sift collections add vault ~/docs/vault/
sift collections add work ~/projects/ --tags work,code
```

### `sift collections remove`

```
sift collections remove <name> [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--force` | bool | | Skip confirmation prompt. |

## `sift config`

Configuration and administration.

### `sift config init`

Initialize SIFT. Creates `~/.sift/` with default config and empty database. Safe to re-run.

### `sift config show`

Print current configuration as TOML.

### `sift config set`

```
sift config set <key> <value>
```

Set a config value using dot-notation keys. See [Configuration](configuration.md) for all keys.

### `sift config get`

```
sift config get <key>
```

Read a config value.

### `sift config stats`

Show usage statistics: collections, file/chunk counts, API usage, feedback totals, storage sizes.

### `sift config health`

Run system health checks: config file, database, Bleve index consistency, API key status, dead letters, stale files.

### `sift config logs`

```
sift config logs [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--feedback` | bool | | Show feedback logs instead of search logs. |
| `--sql` | bool | | Show SQL query logs. |
| `--count` | int | 20 | Number of entries to show. |

### `sift config rebuild-bm25`

Destroy and recreate the Bleve BM25 index from chunks in SQLite. Use when `sift config health` reports index drift.

### `sift config purge`

```
sift config purge [flags]
```

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--force` | | bool | | Skip confirmation. |
| `--collection` | `-c` | string | | Purge only this collection. |

### `sift config retry-dead-letters`

Retry failed operations from the `dead_letters` table. Handles both
embedding failures (`kind='embedding'`) and `sift.toml` summary
generation failures (`kind='index_summary'`).

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--operation` | string | | Filter by operation type (`embed`, `rerank`). |

### `sift config purge-dead-letters`

Mark dead letters as resolved without retrying the failed operations.
Useful for clearing noise from transient failures.

```
sift config purge-dead-letters [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--all` | bool | | Resolve all unresolved dead letters. |
| `--older-than` | string | | Resolve dead letters older than the duration (e.g. `7d`, `2w`). |
| `--operation` | string | | Filter by operation type (`embed`, `rerank`). |

**Examples:**

```bash
sift config purge-dead-letters --all
sift config purge-dead-letters --older-than 7d
sift config purge-dead-letters --operation rerank
```

## `sift sql`

Run a read-only SQL query against the SIFT SQLite database.

```
sift sql <query> [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--limit` | int | 100 | Maximum rows to display. |
| `--schema` | bool | | Print all table schemas instead of running a query. |

!!! warning
    Internal debugging tool. The schema may change between versions.

**Tables:** `collections`, `files`, `file_collections`, `chunks`, `embeddings`, `feedback`, `dead_letters`, `api_usage`, `search_sessions`, `search_cache`, `links`, `backlink_counts`, `read_counts`, `schema_version` — full column lists via `sift sql --schema`.

**Examples:**

```bash
sift sql "SELECT name, path FROM collections"
sift sql "SELECT COUNT(*) FROM chunks"
sift sql "SELECT path, chunk_count FROM files ORDER BY mtime DESC LIMIT 10"
sift sql --schema
```

## `sift keywords`

Extract high-signal keywords per directory using filename, heading, and body weighting.

```
sift keywords [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--collection` | string | | Restrict to a collection. |
| `--path` | string | | Restrict to relative file paths matching a glob. |
| `--depth` | int | 2 | Directory depth to group by. |
| `--top` | int | 8 | Top keywords per group. |
| `--json` | bool | | JSON output. |

**Examples:**

```bash
sift keywords
sift keywords --collection vault
sift keywords --path "memory/*" --depth 2 --top 8
sift keywords --json
```

## `sift eval`

Run anchor and link expansion experiments against test corpora. Used for
internal regression testing of search-quality variants.

### `sift eval run`

Set up a temporary sift instance, index a corpus, run eval cases, and
write a JSON results file.

```
sift eval run [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--cases` | string | | Path to JSONL cases file. |
| `--corpus` | string | | Path to corpus directory. |
| `--collection` | string | | Collection name. |
| `--variant` | string | `all` | Variant: `v0`, `v1`, `v2`, `v3`, `v4`, or `all`. |
| `--top-k` | int | 10 | Number of results to evaluate. |
| `--decay` | float | 0.5 | Link expansion decay factor (v2, v4). |
| `--heading-boost` | float | 2 | Heading boost factor (v3, v4). |
| `--hybrid` | bool | | Use full hybrid search (embeddings + reranking). |
| `--output` | string | `eval_results.json` | Output file path. |

### `sift eval report`

Display a previously-written eval results file.

```
sift eval report <results.json>
```

## `sift daemon`

Manage the long-lived sift daemon process. The daemon serves search and
refresh requests over a Unix socket, amortising the Voyage TLS handshake
and SQLite/Bleve open cost across calls. See the [Daemon](daemon.md) page
for the full design and wire protocol.

```
sift daemon [start|stop|restart|status|logs]
```

The CLI auto-spawns the daemon on first search; manual lifecycle is only
needed for power users and after editing `config.toml`.

### `sift daemon start`

Spawn the daemon if not already running, then poll `/health` until ready.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--timeout` | string | (config) | Time to wait for readiness (e.g. `500ms`, `2s`). Defaults to `daemon.spawn_timeout`. |

### `sift daemon stop`

Terminate the daemon. Tries `POST /shutdown`, then SIGTERM, then SIGKILL.
Idempotent: exits 0 with a "not running" message if no daemon is up.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--timeout` | string | `5s` | Maximum time to wait for shutdown. |

### `sift daemon restart`

Stop and start the daemon in one step. Useful after upgrading the binary
or editing `~/.sift/config.toml`.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--timeout` | string | `5s` | Maximum time to wait for the cycle. |

### `sift daemon status`

Print whether the daemon is running, with PID, uptime, and request count.

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--json` | bool | | Emit machine-readable JSON. |

**Exit codes:**

| Code | Meaning |
|------|---------|
| `0` | running |
| `1` | not running |
| `2` | unhealthy (PID file present but socket unreachable) |

### `sift daemon logs`

Print or tail `~/.sift/logs/daemon.log`.

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--tail` | `-n` | int | 50 | Number of trailing lines to print. |
| `--follow` | `-f` | bool | | Tail the log, printing new lines as they arrive. |

**Examples:**

```bash
sift daemon status
sift daemon status --json
sift daemon logs -n 100
sift daemon logs -f
sift daemon restart           # after editing config.toml
```

## Environment Variables

| Variable | Effect |
|---|---|
| `SIFT_DIR` | Override `~/.sift/` data directory (relocates socket, PID, logs, db). |
| `SIFT_NO_DAEMON` | Any non-empty value disables daemon dial/spawn; CLI runs in-process. |
| `SIFT_DAEMON_SOCKET` | Override socket path (`~/.sift/sift.sock`). Test hook. |
| `SIFT_DAEMON_SPAWNED` | Set automatically when spawning the daemon child. Not for end-user use. |
| `DEEPINFRA_API_KEY` | API key for AI-generated `sift.toml` summaries. Required by `sift refresh --generate=...`. |
| `VOYAGE_API_KEY` | Read by `sift config init` as a default for `api.voyage_api_key`. |
