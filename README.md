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
sift search "architecture" --agent --read-command "mem read"  # wrapper-friendly hint override
```

## Output Modes

```
sift search <query>           # Default: AI-optimized (for piping to agents)
sift search <query> --pretty  # Human-readable with colors and editor commands
sift search <query> --json    # Machine-readable with full score components
sift search <query> --files   # Just file paths (good for xargs/piping)
sift search <query> --agent --read-command "mem read"  # Wrapper can override drill-down hint
```

**Default mode** returns 20 results, ordered best-first. Designed for AI agents that read from the top.

**Pretty mode** is designed for humans reading in a terminal:
- Shows **half** the default results (10) to keep output scannable
- **Reverses** the order so the most relevant results appear at the bottom, where the terminal auto-scrolls to
- Adds ANSI colors, visual separators, and editor open commands (e.g. `> code -g file.go:42`)
- Labels (`[a]`, `[b]`, ...) stay consistent: `[a]` is always the best match

Both defaults can be overridden: `--top-k 5` sets an explicit count, `--reverse=false` disables reverse ordering.

## File Search

Search within specific files at line granularity — no indexing or API calls needed:

```
sift search "store id" --file INFRASTRUCTURE.md        # Single file
sift search "auth flow" --file a.md --file b.md        # Multiple files (parallel)
cat rendered.md | sift search "cache" --file -          # From stdin
sift search "config" --file config.md --pretty          # With context lines
```

File search creates an ephemeral in-memory BM25 index, searches at individual line
granularity, and returns exact line numbers. Useful for large reference files where
you need precise lookup without full collection indexing.

## How It Works

Every query runs BM25 and vector search in parallel, then merges results:

1. **BM25** via [Bleve](https://blevesearch.com/) — keyword matching with highlight extraction
2. **Vector search** — cosine similarity on binary embeddings ([Voyage AI](https://www.voyageai.com/) `voyage-4-lite`)
3. **Reciprocal Rank Fusion** — merges both ranked lists with top-position boosting
4. **Reranking** — Voyage `rerank-2.5-lite` rescores the top 75 for semantic precision
5. **Scoring** — `base * (1 + recency) * feedback_boost * path_boost`
6. **Adaptive top-K** — score-cliff detection between positions 1-5

See [CLAUDE.md](CLAUDE.md) for the full pipeline details.

## Features

- Hybrid search: BM25 + vector + RRF fusion + reranking
- Folder indexes (`sift.toml`): committed per-folder metadata with optional LLM-generated `purpose`/`use_when`/`summary`; results are decorated with folder context by default
- Smart previews centered on BM25 keyword matches
- Adaptive top-K with score-cliff detection
- Content deduplication with "Also in:" references
- Frontmatter-aware chunking (YAML `---`, TOML `+++`); titles derived from frontmatter or H1/H2
- Feedback-based scoring (`sift feedback <id> --positive a,b --negative c`)
- Token-aware batch embedding with dead letter queue for resilience
- `.siftignore` for gitignore-style file filtering per collection
- Pure Go — no CGo, single binary (`modernc.org/sqlite`)
- File search: `--file` flag for line-level BM25 within specific files (no indexing needed)
- Works without API key (BM25-only mode)
- Configurable via `~/.sift/config.toml`
- Optional background daemon keeps the index and TLS connection warm — repeated queries drop from ~3-5 s to sub-second (auto-spawned, no setup)

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
- [Power Workflows](https://svilupp.github.io/sift/power-workflows/) — search loops, links, anchors, refs, and linting
- [CLI Reference](https://svilupp.github.io/sift/cli-reference/) — every command and flag
- [Folder Indexes](https://svilupp.github.io/sift/folder-indexes/overview/) — `sift.toml`, `sift index`, agent usage
- [Daemon](https://svilupp.github.io/sift/daemon/) — optional warm-process for sub-second repeated queries
- [.siftignore](https://svilupp.github.io/sift/siftignore/) — exclude files from indexing

In-repo references:

- [`.claude/skills/using-sift-effectively/SKILL.md`](.claude/skills/using-sift-effectively/SKILL.md) — Claude Code skill (auto-loads when working inside this repo) covering search, feedback, links, refs, and lint workflows
- [CLAUDE.md](CLAUDE.md) — architecture and code map
- [CHANGELOG.md](CHANGELOG.md) — release notes

## Maintenance

### Full Re-index

After changing chunking strategy, header parsing, or any logic that affects how files are split into chunks, run a full re-index to rebuild everything from scratch:

```bash
sift refresh --full
```

This bypasses all hash/mtime checks and for every file: deletes old chunks, re-chunks with current settings, rebuilds BM25, and re-generates embeddings. No purge needed.

### Other Commands

```bash
sift refresh                    # incremental (only changed files)
sift refresh -c vault           # single collection
sift refresh --dry-run          # preview what would change
sift config purge               # nuclear option: delete all data
sift config purge -c vault      # delete one collection's data
sift config rebuild-bm25        # rebuild BM25 index from existing DB chunks
```

## Development

```bash
make setup   # install golangci-lint, goimports
make check   # vet + lint + test
make build   # build binary
```

## License

[MIT](LICENSE)
