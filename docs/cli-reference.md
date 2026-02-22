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
| `--top-k` | `-k` | int | 20 | Number of results. When omitted, adaptive top-K is used. |
| `--json` | | bool | | Full JSON output with scores and metadata. |
| `--files` | | bool | | File paths only (one per line). |
| `--pretty` | | bool | | Human-readable with colors and editor commands. |
| `--reverse` | | bool | | Best results last. Enabled by default with `--pretty`. |
| `--threshold` | | float | 0.40 | Score threshold (only with reranking). |
| `--no-tips` | | bool | | Suppress trailing tip line. |

**Examples:**

```bash
sift search "authentication flow"
sift search "rate limiting" --collection vault --since 1w
sift search "API design" --path "*/topics/work/*"
sift search "database migration" --json
sift search "meeting notes" --files | xargs cat
sift search "debugging tips" --pretty --top-k 5
```

## `sift refresh`

Scan collections and update the search index.

```
sift refresh [files...] [flags]
```

| Flag | Short | Type | Default | Description |
|------|-------|------|---------|-------------|
| `--collection` | `-c` | string | | Refresh specific collection only. |
| `--full` | | bool | | Force full reindex (ignore content hashes). |
| `--dry-run` | | bool | | Show what would change without writing. |

**Examples:**

```bash
sift refresh                          # incremental, all collections
sift refresh -c vault                 # one collection
sift refresh notes.md ideas.md        # specific files
sift refresh --full                   # reindex everything
sift refresh --dry-run                # preview changes
```

!!! note
    Cannot use `--collection` with file arguments.

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

Manage indexed collections. Run bare `sift collections` to list all.

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

Retry failed embed operations from the dead letters table.

## `sift sql`

Run a read-only SQL query against the SIFT SQLite database.

```
sift sql <query> [flags]
```

| Flag | Type | Default | Description |
|------|------|---------|-------------|
| `--limit` | int | 100 | Maximum rows to display. |

!!! warning
    Internal debugging tool. The schema may change between versions.

**Tables:** `collections`, `files`, `chunks`, `embeddings`, `feedback`, `dead_letters`, `api_usage`, `search_sessions`

**Examples:**

```bash
sift sql "SELECT name, path FROM collections"
sift sql "SELECT COUNT(*) FROM chunks"
sift sql "SELECT path, chunk_count FROM files ORDER BY mtime DESC LIMIT 10"
```
