# Command Recipes

## Search

```bash
sift search "query"
sift search "query" --pretty
sift search "query" --json
sift search "query" --agent --read-command "mem read"
sift search "query" --collection work --since 1w
sift search "query" --file docs/architecture.md --file docs/SPEC.md
```

Use `--file` when you want exact line-level lookup over a few known files without depending on the persistent index.

## Feedback

```bash
sift search "auth flow"
sift feedback <search_id> --positive a,b --negative d
```

## Links and Backlinks

```bash
sift links docs/architecture.md
sift links docs/architecture.md --section trust-zones
sift links docs/architecture.md --backlinks
sift links docs/architecture.md --json
```

## Code Ref Authoring

Before stamping:

```text
internal/search/search.go:464-475
internal/cli/search.go#L518
```

After stamping:

```text
internal/search/search.go:464@w7a-475@6oh
internal/cli/search.go:518@ork
```

Commands:

```bash
sift refs stamp PLAN.md --check
sift refs stamp PLAN.md --write
```

## Code Ref Validation

```bash
sift refs validate PLAN.md
sift refs validate docs/ --fix
sift refs validate --collection vault --path "repos/**/CLAUDE.md" --strict
```

Status guide:

- `valid_exact`: token still matches at the stored line
- `valid_shifted`: token moved nearby and SIFT found a unique repair
- `valid_unchecked`: line range exists but no token was present
- `stale`: token no longer matches nearby content
- `stale_ambiguous`: multiple nearby candidates match
- `missing_file`: target file is gone
- `out_of_bounds`: referenced lines exceed file length

## Folder-Level Linting

```bash
sift refs lint docs/
sift refs lint docs/ --fix
sift refs lint --collection vault --path "repos/**/CLAUDE.md"
sift refs lint --doc-only docs/
sift refs lint --code-only docs/
```

`sift refs lint` checks:

- code refs
- markdown links
- wikilinks
- local anchors
