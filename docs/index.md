# SIFT

**Search Index for Finding Things** — local-first hybrid search for personal knowledge files.

SIFT combines BM25 keyword search, vector embeddings, and neural reranking to find exactly what you need across your markdown, plaintext, and JSONL files.

## 30-Second Demo

```bash
# Initialize and add a folder
sift config init
sift collections add notes ~/notes/
sift refresh

# Search
sift search "authentication flow"

# Human-friendly output (best results at the bottom)
sift search "rate limiting" --pretty

# Give feedback to improve future rankings
sift feedback abc123 --positive a,b --negative d
```

## Key Features

- **Hybrid search** — BM25 + vector embeddings + RRF fusion + neural reranking via [Voyage AI](https://www.voyageai.com/)
- **Feedback loop** — thumbs-up/down on results automatically improves future rankings via Bayesian scoring
- **Smart previews** — results show the most relevant part of each chunk, centered on keyword matches
- **Adaptive top-K** — score-cliff detection stops early instead of padding with low-quality results
- **Content deduplication** — identical chunks grouped with "Also in:" references
- **JSONL logging** — every search is logged for analytics and debugging
- **Multi-signal scoring** — recency decay + feedback boost + configurable path boosts
- **Pure Go** — no CGo, single binary, runs anywhere Go compiles
- **Works without API key** — BM25-only mode when no Voyage API key is configured

## Install

```bash
go install sift/cmd/sift@latest
```

Or download a binary from the [releases page](https://github.com/svilupp/sift/releases).

## Next Steps

- [Getting Started](getting-started.md) — install, configure, first search
- [Configuration](configuration.md) — full `config.toml` reference
- [Architecture](architecture.md) — how the search pipeline works
- [CLI Reference](cli-reference.md) — every command and flag
