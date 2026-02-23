# SIFT

[![CI status](https://github.com/svilupp/sift/workflows/CI/badge.svg)](https://github.com/svilupp/sift/actions)
[![Go](https://img.shields.io/badge/Go-1.24+-00ADD8?style=flat&logo=go&logoColor=white)](https://go.dev)
[![License](https://img.shields.io/badge/License-MIT-yellow?style=flat)](LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/svilupp/sift?style=flat)](https://goreportcard.com/report/github.com/svilupp/sift)

**Search Index for Finding Things** — local-first hybrid search for personal knowledge files.

SIFT combines BM25 keyword search, vector embeddings, and neural reranking to find what you need across markdown, plaintext, and JSONL files. Inspired by [Query Markup Documents (QMD)](https://github.com/tobi/qmd), but built for personal memory retrieval.

## What Makes SIFT Different

- **Feedback loop** — thumbs-up/down on results automatically improves future rankings via Bayesian scoring
- **JSONL search logging** — every search is logged for analytics, debugging, and understanding your retrieval patterns
- **Multi-signal scoring** — recency decay + feedback boost + configurable path boosts, all composable

## Quick Start

```bash
# Install (see Installation below)

# Initialize
sift config init
sift config set api.voyage_api_key sk-your-key  # optional: enables vector search

# Add a folder and index it
sift collections add notes ~/notes/
sift refresh

# Search
sift search "authentication flow"
sift search "rate limiting" --pretty
```

## Output Modes

```
sift search <query>           # Default: AI-optimized (for piping to agents)
sift search <query> --pretty  # Human-readable with colors and editor commands
sift search <query> --json    # Machine-readable with full score components
sift search <query> --files   # Just file paths (good for xargs/piping)
```

**Default mode** returns 20 results, ordered best-first. Designed for AI agents that read from the top.

**Pretty mode** is designed for humans reading in a terminal:
- Shows **half** the default results (10) to keep output scannable
- **Reverses** the order so the most relevant results appear at the bottom, where the terminal auto-scrolls to
- Adds ANSI colors, visual separators, and editor open commands (e.g. `> code -g file.go:42`)
- Labels (`[a]`, `[b]`, ...) stay consistent: `[a]` is always the best match

Both defaults can be overridden: `--top-k 5` sets an explicit count, `--reverse=false` disables reverse ordering.

## How It Works

Every query runs BM25 and vector search in parallel, then merges results:

1. **BM25** via [Bleve](https://blevesearch.com/) — keyword matching with highlight extraction
2. **Vector search** — cosine similarity on binary embeddings ([Voyage AI](https://www.voyageai.com/) `voyage-4-lite`)
3. **Reciprocal Rank Fusion** — merges both ranked lists with top-position boosting
4. **Reranking** — Voyage `rerank-2.5-lite` rescores the top 75 for semantic precision
5. **Scoring** — `base * (1 + recency) * feedback_boost * path_boost`
6. **Adaptive top-K** — score-cliff detection between positions 1-5

See the [architecture docs](https://svilupp.github.io/sift/architecture/) for the full pipeline details.

## Features

- Hybrid search: BM25 + vector + RRF fusion + reranking
- Smart previews centered on BM25 keyword matches
- Adaptive top-K with score-cliff detection
- Content deduplication with "Also in:" references
- Feedback-based scoring (`sift feedback <id> --positive a,b --negative c`)
- Token-aware batch embedding with dead letter queue for resilience
- `.siftignore` for gitignore-style file filtering per collection
- Pure Go — no CGo, single binary (`modernc.org/sqlite`)
- Works without API key (BM25-only mode)
- Configurable via `~/.sift/config.toml`

## Installation

### Quick install (macOS/Linux)

```bash
curl -fsSL https://zyedidia.github.io/eget.sh | sh       # install eget (GitHub binary manager)
eget svilupp/sift --to /usr/local/bin/sift                 # install sift
```

> [eget](https://github.com/zyedidia/eget) downloads the right binary for your OS/arch from GitHub releases automatically.

### Other options

- **Homebrew eget**: `brew install eget && eget svilupp/sift --to /usr/local/bin/sift`
- **Direct download**: grab a binary from the [releases page](https://github.com/svilupp/sift/releases)
- **From source**: `git clone https://github.com/svilupp/sift.git && cd sift && make install`

## Documentation

Full documentation at **[svilupp.github.io/sift](https://svilupp.github.io/sift/)**:

- [Getting Started](https://svilupp.github.io/sift/getting-started/) — install, configure, first search
- [Configuration](https://svilupp.github.io/sift/configuration/) — full `config.toml` reference
- [Architecture](https://svilupp.github.io/sift/architecture/) — search pipeline, scoring, feedback loop
- [CLI Reference](https://svilupp.github.io/sift/cli-reference/) — every command and flag
- [.siftignore](https://svilupp.github.io/sift/siftignore/) — exclude files from indexing

## Development

```bash
make setup   # install golangci-lint, goimports
make check   # vet + lint + test
make build   # build binary
```

## License

[MIT](LICENSE)
