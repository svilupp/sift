# Folder Indexes for AI Agents

**TL;DR.** Agents should call `sift index` (piped → JSON) for
orientation, then use the `digest` and `purpose` fields to pick a
folder before drilling in with `sift search`. `sift search --with-index`
is on by default and decorates every result with its enclosing folder's
`purpose` / `use_when`.

## Why agents care

A search hit alone is "this chunk matched". An agent often needs more:

- *Should I trust this folder for this question?*
- *What is this folder for, generally?*
- *Are there other folders I should look at first?*

`sift.toml` answers all three. `sift index` exposes them as a single
JSON envelope; `sift search --with-index` decorates each result inline.

## The orientation pattern

```bash
# 1. Get the table of contents (one paragraph + structured tree)
sift index --collection vault --json --depth 2

# 2. Or just the digest, for a prompt-context insert
sift index --collection vault --json --summaries=false | jq -r '.digest'

# 3. Drill into a folder
sift index docs/architecture --collection vault --json | jq

# 4. Search, with folder context attached to each hit
sift search "rate limiter" --collection vault --json \
  | jq '.results[] | {path, score, folder: .folder_index.folder_path, purpose: .folder_index.folder_purpose}'
```

## Format defaults are agent-first

| Caller | Default format | Rationale |
|--------|----------------|-----------|
| Pipe / file | `json` | machine-readable, jq-friendly |
| TTY | `markdown` | human-readable in a terminal |

Agents almost always want JSON. Pass `--json` explicitly if you don't
trust auto-detection (e.g., calling sift through a wrapping shell that
might claim a TTY).

## Filters: scope before downloading

`sift index --collection NAME --depth 1` is much cheaper than walking
an entire vault. Combine with:

- `--path 'docs/**'` to scope to a subtree.
- `--since 7d` to focus on recent churn.
- `--depth N` to limit descent.

## Reading search decoration

Default-on `--with-index` adds a `folder_index` field to each result:

```json
{
  "id": "a",
  "path": "docs/architecture.md",
  "start_line": 42,
  "end_line": 58,
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

`folder_index` is omitted when the folder has no `sift.toml` or when
`--no-index` is set.

In human mode the same data renders as a grey suffix:

```text
[a] docs/architecture.md:42-58  [folder: docs/ - SIFT architecture overview]
```

## Worked example: agent in an unfamiliar repo

```bash
# Step 1: orient
sift index --collection vault --json --depth 1 | jq '.digest, .folders[0:5] | .[] | {path, purpose}'

# Step 2: pick a folder, drill
sift index docs/agent-next --collection vault --json | jq '.folders[0].files'

# Step 3: search inside it
sift search "tool routing policy" --collection vault --path 'docs/agent-next/**' --json \
  | jq '.results[] | {path, line: "\(.start_line)-\(.end_line)", folder: .folder_index.folder_path, summary: .folder_index.file_summary}'
```

This three-step pattern — orient, drill, search — is the
recommended baseline for agents working over a SIFT collection they
have not seen before.

## Good agent hygiene

- Cache the `digest` in your prompt context. It is small, sticky, and
  gives the model a navigation prior.
- Use `--depth` aggressively. A full-tree dump on a 500-folder vault is
  rarely useful for an agent prompt.
- Treat `--generate` as **not your responsibility**. The user runs it.
- If `folder_index` is missing on a result, the folder has no
  `sift.toml` yet — that is a useful signal in itself.
- `--no-index` exists for byte-stable diffs in regression tests; do
  not use it in production agent flows.

