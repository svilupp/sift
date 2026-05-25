# `sift.toml` Format Reference

**TL;DR.** Schema version 1. One `sift.toml` per folder. Mechanical
fields (counts, hashes) are tool-managed; editorial fields
(`purpose`, `use_when`, `summary`) are LLM- or human-written.
`sift refresh` round-trips byte-identical when nothing has changed.

## Top-level fields

```toml
schema_version = 1
ignore = false                          # optional, default false

purpose = """
Short paragraph describing what this folder is for. Multiline OK.
"""

use_when = [
  "agent is exploring the architecture",
  "looking for the BM25 + vector pipeline",
]

[refresh]
file_count = 4                          # tool-managed
word_count = 3120                       # tool-managed
```

| Field | Type | Source | Notes |
|-------|------|--------|-------|
| `schema_version` | int | tool | required; currently `1` |
| `ignore` | bool | human/LLM | "do not recommend"; does NOT exclude content from search |
| `purpose` | string | LLM/human | sticky; survives drift |
| `use_when` | string[] | human | hand-edited routing cues; never auto-generated |
| `refresh.file_count` | int | tool | indexed file count after `.siftignore` |
| `refresh.word_count` | int | tool | sum of `words` across files |

## Per-file entries

```toml
[files."architecture.md"]
ignore = false                          # optional
content_hash = "1f2a..."                # xxhash64 of full content
head_hash    = "8b3c..."                # xxhash64 of first 500 words
tail_hash    = "44e9..."                # xxhash64 of last 500 words
words        = 1240
summary      = "Describes the BM25 + vector + RRF + rerank pipeline."
```

| Field | Type | Source | Notes |
|-------|------|--------|-------|
| `ignore` | bool | human/LLM | "do not recommend" routing flag |
| `content_hash` | string | tool | `xxhash64`, hex |
| `head_hash` | string | tool | hash of joined first 500 words |
| `tail_hash` | string | tool | hash of joined last 500 words |
| `words` | int | tool | whitespace-split word count |
| `summary` | string | LLM/human | ≤2 sentences, ≤200 chars by convention |

The freshness tuple `(content_hash, head_hash, tail_hash, words)` is what
drives "stale" detection. Any drift in any of those four flips a stored
summary to stale.

Keys are sorted alphabetically by the writer.

## Per-child-folder entries

```toml
[folders."subfolder-name"]
ignore = false
```

Currently this section only carries the `ignore` flag. Richer metadata
about the child folder lives in the child's own `sift.toml`.

## Determinism

The writer guarantees byte-identical output for byte-identical input.
This means:

- Field order is fixed (struct-order in `internal/index/schema.go`).
- Map keys are alphabetised.
- Floats and ints are formatted canonically.
- `sift refresh` on an unchanged tree produces zero diff.

This invariant is load-bearing for git workflows and for the
idempotency check enforced by `sift refresh` regression tests.

## Versioning policy

- Bumping `schema_version` is a breaking change.
- Older versions: the parser accepts files with no `schema_version` set
  (treated as `1`) for backwards compatibility.
- Higher versions are rejected with a `parse_error` defect.

## Exclusion invariants

- `sift.toml` itself is never chunked, embedded, or returned by search.
  The chunker skips it explicitly.
- `.siftignore` and per-entry `ignore = true` are independent: the
  former excludes content from the BM25/vector index; the latter only
  hides the entry from routing/recommendation.

## Example

A complete folder-level file:

```toml
schema_version = 1

purpose = """
Documents the SIFT search pipeline: BM25, vector retrieval, RRF fusion,
neural reranking, and the multi-signal scoring formula.
"""

use_when = [
  "diagnose a ranking regression",
  "explain how reranking interacts with feedback",
]

[refresh]
file_count = 2
word_count = 3120

[files."architecture.md"]
content_hash = "1f2a8c40e31d7b22"
head_hash    = "8b3c12ee04f00009"
tail_hash    = "44e9bbeebf112233"
words        = 1240
summary      = "BM25 + vector + RRF + rerank pipeline overview."

[files."SPEC.md"]
content_hash = "0a0b0c..."
head_hash    = "0a0b0c..."
tail_hash    = "0a0b0c..."
words        = 1880
summary      = "Long-form behavioural spec (database schema, output formats)."
```
