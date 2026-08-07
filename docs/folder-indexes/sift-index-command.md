# `sift index` Deep Dive

**TL;DR.** `sift index --orient` emits a compact semantic routing map
for agents. It defaults to the selected root plus one level, prefers
editorial summaries, and falls back locally to titles, verbatim prose,
and headings. Plain `sift index` remains the detailed tree view.

See the flag-table summary in [CLI Reference](../cli-reference.md#sift-index).

## Output formats

Auto-detection:

| stdout | default format |
|--------|----------------|
| TTY    | `markdown`     |
| pipe / file | `json`    |

Override with `--json` / `--markdown` (mutually exclusive).

`--orient` always emits JSON, is incompatible with `--markdown`, and
defaults to `--depth 1` unless depth is explicitly supplied.
`--summaries=false` makes orientation ignore editorial fields and use
only local extractive signals.

`--sections` defaults to `on` when emitting JSON and `off` when emitting
markdown — JSON consumers usually want the H1/H2 outline, terminal
viewers usually do not.

## Filters

When `--collection NAME` is present, the optional positional path is
relative to that collection:

```bash
sift index --collection vault --orient
sift index docs --collection vault --orient
sift index docs/architecture --collection vault --orient
```

Returned paths stay collection-relative at every step, so they can be
passed directly to `sift index`, `sift search --path`, or `sift read`.

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

The compact orientation envelope intentionally omits hashes, mtimes,
and section line ranges. Its folder entries contain `path`, `purpose`,
`purpose_source`, `use_when`, `children`, and compact `files`. File
entries contain `path`, `title`, `summary`, `summary_source`, `topics`,
and `words`.

## When to use which subcommand

| Goal | Command |
|------|---------|
| Quick semantic orientation | `sift index -c NAME --orient` |
| Drill into a branch | `sift index PATH -c NAME --orient` |
| Inspect detailed sections/metadata | `sift index PATH -c NAME --json` |
| Read the selected leaf | `sift read FILE -c NAME --section HEADING` |
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
# Discover collections, then get top-level semantic orientation
sift collections --json
sift index --collection vault --orient

# Drill into a subtree while keeping collection-relative paths
sift index docs --collection vault --orient

# Drill into a subtree, last 7 days only
sift index docs --collection vault --json --since 7d

# PR-grade lint report
sift index check ./docs --markdown > REPORT.md
```
