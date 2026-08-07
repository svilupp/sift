# Folder Indexes (`sift.toml`)

**TL;DR.** `sift.toml` is a committed, human-editable, per-folder index
of the files inside that folder. It carries deterministic content
hashes plus optional `purpose` / `use_when` / `summary` text so that
agents (and humans) can orient themselves in an unfamiliar tree
without grepping through every file.

## Why it exists

Hybrid search retrieves chunks. Chunks are good for "find the passage
about X". They are bad for "what is this folder for?" or "which folder
should I read first?". Folder indexes solve the orientation problem by
attaching a small, structured note to each folder that survives across
sessions and tooling.

The contract is intentionally narrow:

- One `sift.toml` per folder.
- Mechanical fields (counts, hashes) are managed by `sift refresh`.
- Editorial fields (`purpose`, `use_when`, `summary`) are written by an
  LLM (opt-in) or by hand.
- Content of `sift.toml` itself is **never** indexed for search — it is
  metadata, not corpus.

## Lifecycle

```
sift refresh                 -> mechanical maintenance (always)
sift refresh --generate=...  -> LLM fills/refreshes editorial fields
sift index                   -> read view (markdown for humans, JSON for agents)
sift index check             -> lint report
sift search --with-index     -> decorate results with folder context
```

## What's stored

Each `sift.toml` file declares:

- `schema_version` (currently `1`).
- Folder-level `purpose` (paragraph) and `use_when` (short cues).
- Per-file entries: content/head/tail hashes, word count, optional
  `summary`.
- Direct-child folder entries (just `ignore` flags; child summaries live
  in the child's own `sift.toml`).

See [Format reference](format.md) for the canonical schema.

## What it is NOT

- **Not a search filter.** `.siftignore` controls what gets chunked and
  embedded. `sift.toml`'s `ignore = true` only marks an entry as
  "do not recommend" for routing.
- **Not a chunk index.** Chunks live in SQLite + Bleve. `sift.toml`
  records only file-level signatures.
- **Not a queue.** Failed LLM generation is recorded in `dead_letters`,
  not in `sift.toml`.

## Defects reported by `sift index check`

| Kind | Meaning |
|------|---------|
| `missing_sift_toml` | folder has files but no `sift.toml` |
| `parse_error` | `sift.toml` exists but is malformed |
| `stale` | file's freshness signature drifted since the entry |
| `missing_summary` | entry exists but `summary == ""`; only checked with `--require-summaries` |
| `orphaned` | entry refers to a file that no longer exists |
| `schema_version` | `schema_version` is unsupported |

`sift index check` exits with code `2` when any enabled defects are
found. Empty summaries are valid in fully local mode; use `sift index
check --require-summaries` only when summary coverage is your policy.

## Common workflows

- [Bootstrap a new collection](../power-workflows.md#bootstrap-your-collection-with-sifttoml)
- [Hand-edit a folder's `purpose`](../power-workflows.md#hand-edit-a-sifttoml-purpose)
- [Use `sift index` from an AI agent](agent-usage.md)
- [Audit stale summaries](../power-workflows.md#audit-stale-summaries)
- [Recover from interrupted generation](../power-workflows.md#recover-from-interrupted-generation)
