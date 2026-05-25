---
name: using-sift-effectively
description: Work with SIFT's high-signal search, feedback, links, anchors, code refs, and linting workflows. Use when searching indexed collections, exploring document backlinks, authoring or validating code refs, or setting up folder-level reference checks in CI.
---

# Using SIFT Effectively

## When to Use

- Retrieve information from indexed collections with `sift search`
- Improve recurring search quality with `sift feedback`
- Inspect document graphs with `sift links`
- Author stable code refs with `sift refs stamp`
- Repair or lint refs and anchors with `sift refs validate` and `sift refs lint`

## First Move

```bash
# Find the material
sift search "your topic"

# Inspect the document graph when links matter
sift links path/to/doc.md

# Enforce reference integrity on a doc set
sift refs lint docs/
```

## High-Signal Workflow

### 1. Search and tighten fast

Use indexed retrieval first. If you already know the relevant file set, switch to direct file search.

```bash
sift search "rate limiting"
sift search "rate limiting" --pretty
sift search "rate limiting" --agent --read-command "mem read"
sift search "rrf boost" --file docs/architecture.md --file docs/SPEC.md
```

### 2. Record feedback immediately

```bash
sift search "auth flow"
sift feedback <search_id> --positive a,b --negative d
```

### 3. Keep authored references portable

- Prefer relative markdown links.
- Prefer relative code refs.
- Use heading slugs like `#trust-zones`, not raw heading text.
- Draft code refs plainly, then stamp them.

```bash
sift refs stamp PLAN.md --check
sift refs stamp PLAN.md --write
```

### 4. Use the right reference command

| Goal | Command |
|------|---------|
| Explore outgoing links or backlinks | `sift links <file>` |
| Add code-ref tokens and canonicalize syntax | `sift refs stamp ...` |
| Inspect or repair code refs only | `sift refs validate ...` |
| Fail on broken refs, links, or anchors | `sift refs lint ...` |

### 5. Run lint in two phases

Local loop:

```bash
sift refs lint docs/ --fix
sift refs lint docs/
```

CI:

```bash
sift refs lint docs/
```

`--fix` only applies safe code-ref rewrites. It does not invent missing file paths or anchor names.

## Daemon (warm queries)

A background daemon auto-spawns on first `sift search` / `sift refresh` and keeps the index and Voyage TLS connection warm — repeated queries drop from ~3-5 s to sub-second. No setup required; falls back to in-process on any failure.

```bash
sift daemon status         # check if running
sift daemon restart        # after editing config.toml
SIFT_NO_DAEMON=1 sift ...  # opt out for one call
```

## Agent Notes

- `sift links` is graph inspection, not validation.
- `sift refs validate` is code-ref-specific and supports repair.
- `sift refs lint` is the strict guardrail for folders, repos, and CI.
- A `valid_unchecked` code ref means the lines still exist but the ref had no tokens yet.
- A `missing_anchor` result almost always means the heading slug changed or the target heading was removed.

## References

- Command recipes and status meanings: [references/commands.md](references/commands.md)
- Lint rollout and CI patterns: [references/linting.md](references/linting.md)
