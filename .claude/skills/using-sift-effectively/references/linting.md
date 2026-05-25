# Linting Rollout

## Goal

Treat broken refs and links as a maintained surface, not as doc debt.

## Minimal Rollout

1. Start with a target folder.
2. Run `sift refs lint <folder> --fix` locally.
3. Manually resolve remaining `missing_file`, `missing_anchor`, `stale`, or `stale_ambiguous` results.
4. Add plain `sift refs lint <folder>` to CI.

Example:

```bash
sift refs lint docs/ --fix
sift refs lint docs/
```

## Collection-Scoped Rollout

If docs live inside indexed collections, scope lint to the subset that matters.

```bash
sift refs lint --collection vault --path "repos/**/CLAUDE.md"
```

This is useful when you only want to enforce high-value documents first.

## What `--fix` Can and Cannot Do

Safe:

- stamp missing code-ref tokens
- canonicalize `#L12-L28` refs to `:12-28`
- repair uniquely shifted code refs nearby

Unsafe and therefore not automatic:

- invent missing file paths
- rename markdown anchors
- choose between ambiguous code-ref matches
- rewrite document links to different targets

## Recommended Policy

- Run `sift refs lint <folder> --fix` before commit when editing docs with code refs.
- Run `sift refs lint <folder>` in CI.
- Use `sift links <file>` when investigating how a doc fits into the graph.
- Use `sift refs validate <file>` when you want detailed code-ref triage instead of full lint output.
- If a general docs folder contains illustrative `file:line` examples, use `--doc-only` there or lint a narrower curated folder that only contains intentional code refs.
