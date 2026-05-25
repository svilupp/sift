# `sift index` Deep Dive

**TL;DR.** `sift index` walks a tree, parses each `sift.toml`, and emits
a structured table of contents. On a TTY it renders markdown; piped, it
renders a JSON envelope. Use `sift index check` for lint-style defect
reports.

See the flag-table summary in [CLI Reference](../cli-reference.md#sift-index).

## Output formats

Auto-detection:

| stdout | default format |
|--------|----------------|
| TTY    | `markdown`     |
| pipe / file | `json`    |

Override with `--json` / `--markdown` (mutually exclusive).

`--sections` defaults to `on` when emitting JSON and `off` when emitting
markdown — JSON consumers usually want the H1/H2 outline, terminal
viewers usually do not.

## Filters

All filters compose AND-style and apply to both `sift index` (read view)
and `sift index check`:

- `--path GLOB` — gitignore-style glob (`docs/**`, `*.md`).
- `--since DUR` — duration window (`7d`, `1w`, `24h`).
- `--file PATH` — explicit file path; repeatable.

The filter pipeline drops files that do not pass any active filter
**after** the tree has been loaded, so a `--depth 1` walk plus a
`--path 'docs/**'` filter produces a tightly scoped report.

## JSON envelope

The envelope is documented in [CLI Reference](../cli-reference.md#json-envelope-shape)
and lives at `internal/index/render_json.go`. Highlights:

- `schema_version` — currently `1`.
- `digest` — a one-paragraph orientation generated from the tree (folder
  count, leading folder purposes). Designed for agent prompt injection.
- `stats` — folder/file/byte/word counts and the newest file mtime.
- `folders[]` — flat list, sorted by path, depth-stamped.
- `errors[]` — non-fatal parse errors per folder.

## When to use which subcommand

| Goal | Command |
|------|---------|
| Read the tree (human or agent) | `sift index` |
| Generate a digest for prompt context | `sift index --json | jq .digest` |
| Spot missing/stale entries | `sift index check` |
| Generate a PR-friendly checklist | `sift index check --markdown > REPORT.md` |
| Programmatic CI gate | `sift index check --json --collection NAME` |

## Performance

- The walk is single-pass and skips folders pruned by `.siftignore` or
  `--include-ignored=false`.
- Loading a 500-folder tree completes well under 250 ms (Phase-8 bench).
- `--depth N` short-circuits descent; useful for shallow listings.

## Examples

```bash
# Top-level orientation
sift index --collection vault --depth 1

# Pipe-friendly digest for an agent prompt
sift index --collection vault --json --summaries=false | jq '.digest'

# Drill into a subtree, last 7 days only
sift index docs --path 'docs/**' --since 7d

# PR-grade lint report
sift index check ./docs --markdown > REPORT.md
```
