# Folder Indexes for AI Agents

**TL;DR.** Agents should discover collections with `sift collections
--json`, call `sift index --orient` for a shallow semantic map, then
repeat that command on a relevant collection-relative branch. Search
and read only after narrowing the scope.

## Why agents care

A search hit alone is "this chunk matched". An agent often needs more:

- *Should I trust this folder for this question?*
- *What is this folder for, generally?*
- *Are there other folders I should look at first?*

`sift.toml` answers all three when summaries exist. In fully local mode,
`sift index --orient` fills the same routing role with document titles,
verbatim opening prose, and headings. Every purpose/summary includes a
`*_source` field so an agent can distinguish `editorial`, `extractive`,
`headings`, and `title` evidence.

To audit only signals derived from current local files—even if the tree
already contains generated or human-written summaries—pass
`--orient --summaries=false`.

## The orientation pattern

```bash
# 0. Discover the available namespaces
sift collections --json

# 1. Get a compact semantic root plus its direct children (depth 1)
sift index --collection vault --orient

# 2. Drill into a relevant branch; the path remains collection-relative
sift index docs/architecture --collection vault --orient

# 3. Search the selected branch
sift search rate limiter --collection vault --path 'docs/architecture/**' --json \
  | jq '.results[] | {file, score, start_line, end_line, summary: .folder_index.file_summary}'

# 4. Read an exact result or section
sift read docs/architecture/pipeline.md --collection vault --section data-flow --json
```

## Format defaults are agent-first

| Caller | Default format | Rationale |
|--------|----------------|-----------|
| Pipe / file | `json` | machine-readable, jq-friendly |
| TTY | `markdown` | human-readable in a terminal |

Agents almost always want `--orient` for navigation and `--json` for
detailed inspection. Pass `--json` explicitly if you don't
trust auto-detection (e.g., calling sift through a wrapping shell that
might claim a TTY).

## Filters: scope before downloading

`sift index --collection NAME --orient` defaults to depth 1 and is much
cheaper than walking an entire vault. Combine the detailed view with:

- `--path 'docs/**'` to scope to a subtree.
- `--since 7d` to focus on recent churn.
- `--depth N` to limit descent.

## Reading search decoration

Default-on `--with-index` adds a `folder_index` field to each result:

```json
{
  "id": "a",
    "file": "docs/architecture.md",
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
sift index --collection vault --orient | jq '{digest, folders: [.folders[] | {path, purpose, children}]}'

# Step 2: pick a folder, drill
sift index docs/agent-next --collection vault --orient | jq '.folders[] | {path, purpose, files}'

# Step 3: search inside it
sift search tool routing policy --collection vault --path 'docs/agent-next/**' --json \
  | jq '.results[] | {file, line: "\(.start_line)-\(.end_line)", folder: .folder_index.folder_path, summary: .folder_index.file_summary}'

# Step 4: read only the useful leaf
sift read docs/agent-next/tools.md --collection vault --section routing-policy
```

This three-step pattern — orient, drill, search — is the
recommended baseline for agents working over a SIFT collection they
have not seen before.

## Good agent hygiene

- Cache the `digest` in your prompt context. It is small and gives the
  gives the model a navigation prior.
- Start with `--orient`; use `--depth` explicitly only when you need a
  wider horizon. A full-tree dump on a 500-folder vault is
  rarely useful for an agent prompt.
- Treat `summary_source=extractive` as useful routing evidence, not an
  authored interpretation of the entire file.
- Treat `--generate` as **not your responsibility**. The user runs it.
- If `folder_index` is missing on a result, the folder has no
  `sift.toml` yet — that is a useful signal in itself.
- `--no-index` exists for byte-stable diffs in regression tests; do
  not use it in production agent flows.
