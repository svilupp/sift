# Power Workflows

This page focuses on the highest-leverage SIFT workflows once a collection is already indexed.

## Search Like an Operator

Use `sift search` for indexed retrieval and `--file` for exact line-level lookup inside a few known files.

```bash
sift search "rate limiting"
sift search "rate limiting" --pretty
sift search "rate limiting" --collection work --since 1w
sift search "rrf boost" --file docs/architecture.md --file docs/SPEC.md
```

Use the output mode intentionally:

- Default output is compact and agent-friendly.
- `--pretty` is for humans reading in a terminal.
- `--json` is for automation.
- `--files` is for piping into other tools.
- `--file` skips the persistent index and runs a lightweight in-memory search over specific files.

## Close the Feedback Loop

If a search was good or bad, record that signal immediately.

```bash
sift search "auth flow"
sift feedback abc123 --positive a,b --negative d
```

This is the fastest way to improve recurring searches without changing prompts or query wording.

## Use Links and Anchors Deliberately

SIFT understands document structure, section anchors, markdown links, wikilinks, and common in-text document references such as `mem read ...`.

Good patterns:

```md
[Architecture](docs/architecture.md#trust-zones)
[[PLAN.2026-02-12#Overview]]
[Local section](#rollout-plan)
```

Keep links relative where possible. Heading anchors should use the slugified heading text:

- `## Trust Zones` -> `#trust-zones`
- `## Rollout Plan` -> `#rollout-plan`

Use `sift links` to inspect the graph:

```bash
sift links docs/architecture.md
sift links docs/architecture.md --section trust-zones
sift links docs/architecture.md --backlinks
sift links docs/architecture.md --json
```

`sift links` is for exploration, not validation. It shows what links exist and which sections are connected.

## Author Stable Code References

Use plain line refs while drafting, then stamp them.

Before:

```text
internal/search/search.go:464-475
internal/cli/search.go#L518
```

After `sift refs stamp`:

```text
internal/search/search.go:464@w7a-475@6oh
internal/cli/search.go:518@ork
```

Recommended workflow:

```bash
sift refs stamp PLAN.md --check
sift refs stamp PLAN.md --write
```

`stamp` does three things:

- fills in missing line tokens
- normalizes GitHub-style `#L12-L28` refs to canonical `:12-28` form
- preserves surrounding markdown formatting

## Repair Drifted Code References

Use `validate` when you want code-ref-specific inspection or safe repair.

```bash
sift refs validate PLAN.md
sift refs validate docs/ --fix
sift refs validate --collection vault --path "repos/**/CLAUDE.md" --strict
```

Important statuses:

- `valid_exact`: stored line and token still match
- `valid_shifted`: the ref drifted, but SIFT found a unique nearby repair
- `valid_unchecked`: the lines still exist, but the ref had no tokens
- `stale`: the token no longer matches nearby content
- `stale_ambiguous`: multiple nearby matches were found
- `missing_file`: target file no longer exists
- `out_of_bounds`: referenced lines exceed file length

## Lint a Folder or Repo

Use `sift refs lint` as the automated guardrail.

It checks:

- code refs
- markdown links
- wikilinks
- local anchors

Examples:

```bash
sift refs lint docs/
sift refs lint docs/ --fix
sift refs lint --collection vault --path "repos/**/CLAUDE.md"
sift refs lint --doc-only docs/
sift refs lint --code-only docs/
```

Behavior:

- exits non-zero on any broken file link, missing anchor, stale code ref, or unresolved target
- `--fix` only applies safe code-ref rewrites
- `--fix` never guesses at missing document links or anchor names

If a folder contains illustrative `file:line` examples that are not meant to resolve against the current repo, do one of these:

- lint a narrower curated folder that only contains real references
- use `--doc-only` for general docs-site integrity
- move intentionally fake examples out of the lint target

### Local Development Loop

```bash
sift refs lint docs/ --fix
sift refs lint docs/
```

Run the first command while editing. Run the second before committing.

### CI Pattern

```bash
sift refs lint docs/
```

For collection-scoped linting:

```bash
sift refs lint --collection vault --path "repos/**/CLAUDE.md"
```

This is the simplest way to enforce that a curated folder keeps its cross-references fresh.

## Practical Guidance

- Prefer relative paths in docs. They survive moves better and keep authored refs portable.
- Use `sift links` to understand the current graph before rewriting a doc set.
- Use `sift refs stamp --check` in large docs before committing `--write`.
- Use `sift refs lint --fix` locally and plain `sift refs lint` in CI.
- If a code block is structurally important, stamp it early instead of waiting for drift.

## Bootstrap Your Collection With `sift.toml`

First-time setup of folder indexes for a collection. Mechanical fields
are filled by every `sift refresh`; the LLM-written `purpose`,
`use_when`, and `summary` text needs an explicit opt-in.

```bash
# 1. Make sure the collection is registered and content is indexed.
sift collections add vault ~/Documents/GitHub/md-tower-vault
sift refresh -c vault

# 2. Mechanical-only sift.toml maintenance (no LLM): adds entries, hashes, counts.
sift refresh -c vault --index-only

# 3. AI-bootstrap purpose / use_when / summary for every folder (slow, $$).
DEEPINFRA_API_KEY=$KEY \
  sift refresh -c vault --index-only --generate=all --detach

# 4. Watch progress (the detached run streams to ~/.sift/refresh-index.log).
sift refresh --status

# 5. Inspect the result.
sift index -c vault | head -30
```

Expected output: `sift index -c vault | head` shows a markdown tree
with one-line folder purposes and per-file summaries.

## Hand-edit a `sift.toml` Purpose

The LLM is opinionated. Sometimes it is wrong, and you want to pin a
human-written `purpose` or `use_when` cue.

```bash
# 1. Find the file.
$EDITOR docs/architecture/sift.toml

# 2. Edit the editorial fields. Leave the [files."*"] entries alone —
#    those are tool-managed.
#
#    purpose = """
#    SIFT architecture and pipeline design notes.
#    """
#    use_when = ["explain reranking", "BM25 vs vector tradeoff"]

# 3. Verify the file still parses.
sift index check docs/architecture --json | jq .summary

# 4. Commit.
git add docs/architecture/sift.toml
git diff --cached docs/architecture/sift.toml

# 5. Future refresh runs preserve hand-edits unless the freshness
#    signature drifts AND --generate=stale is used.
sift refresh -c vault --index-only       # safe: no LLM calls
```

Expected output: `sift index check` reports `0 defects`. `git diff` is
limited to the editorial fields you touched.

## Use `sift.toml` From an AI Agent

Three-step orient → drill → search pattern. Pipe everything through
`--json` so `--with-index` decoration is reliable.

```bash
# 1. Orient: digest + top-level folders.
sift index -c vault --json --depth 1 \
  | jq '{digest, folders: .folders[0:5] | map({path, purpose})}'

# 2. Drill into the most promising folder.
sift index docs/agent-next-architecture -c vault --json \
  | jq '.folders[0].files | map({path, summary})'

# 3. Search with folder context attached.
sift search "tool routing policy" -c vault --json \
  | jq '.results[] | {path, lines: "\(.start_line)-\(.end_line)", folder: .folder_index.folder_path, purpose: .folder_index.folder_purpose}'
```

Expected output: each step yields a small JSON shape suitable for
prompt-context injection. The `folder_index` field is present on
every search hit whose folder has a `sift.toml`.

## Audit Stale Summaries

Find and refresh entries whose freshness signature has drifted.

```bash
# 1. Lint pass: shows exact stale + missing entries (no LLM, no writes).
sift index check -c vault --json \
  | jq '.summary'

# 2. Drill into a defect category.
sift index check -c vault --json \
  | jq '.defects | map(select(.kind == "stale")) | .[0:5]'

# 3. Targeted regeneration (only stale folders, no full rewrite).
DEEPINFRA_API_KEY=$KEY \
  sift refresh -c vault --index-only --generate=stale --progress=json \
  | jq -r 'select(.event == "folder_done" or .event == "summary") | [.event, .folder // "", .ms // 0] | @tsv'
```

Expected output: `summary` reports a non-zero `stale` count; the
generation pass reports `folders_done` matching that count.

## Recover From Interrupted Generation

A detached run may die or get rate-limited. Both are recoverable.

```bash
# 1. Check whether anything is running and what the log says.
sift refresh --status

# 2. Inspect dead-letter rows (folders that exhausted retries).
sift sql "SELECT id, kind, payload FROM dead_letters WHERE kind = 'index_summary' ORDER BY id DESC LIMIT 20"

# 3. Re-queue the dead-lettered folders. This re-runs only the failures.
DEEPINFRA_API_KEY=$KEY \
  sift config retry-dead-letters

# 4. If a generation run was killed mid-stream, re-run with --generate=missing.
#    Already-completed folders are skipped because the freshness signature matches.
sift refresh -c vault --index-only --generate=missing --detach
sift refresh --status
```

Expected output: `dead_letters` shrinks to zero after the retry; a
follow-up `sift index check` reports `0 missing_summary` defects.
