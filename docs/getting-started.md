# Getting Started

## Prerequisites

- **Go 1.24+** (for building from source)
- **Voyage AI API key** (optional — enables vector search and reranking)

Get a Voyage API key at [dash.voyageai.com](https://dash.voyageai.com/). SIFT works without one in BM25-only mode.

## Installation

=== "go install"

    ```bash
    go install github.com/svilupp/sift/cmd/sift@latest
    ```

=== "Binary"

    Download from the [releases page](https://github.com/svilupp/sift/releases) and place in your `$PATH`.

=== "From source"

    ```bash
    git clone https://github.com/svilupp/sift.git
    cd sift
    make install
    ```

## First-Time Setup

### 1. Initialize SIFT

```bash
sift config init
```

This creates `~/.sift/` with a default `config.toml` and an empty SQLite database.

### 2. Set your API key (optional)

```bash
sift config set api.voyage_api_key sk-your-key-here
```

Without an API key, SIFT uses BM25-only search (keyword matching). With a key, you get the full hybrid pipeline: vector embeddings + reranking.

### 3. Add a collection

A collection is a folder of files that SIFT indexes and searches.

```bash
sift collections add vault ~/docs/vault/
```

You can add multiple collections:

```bash
sift collections add notes ~/notes/
sift collections add projects ~/projects/ --tags work,code
```

### 4. Index your files

```bash
sift refresh
```

This scans all collections, chunks text files, generates embeddings (if API key is set), and builds the BM25 + vector indexes.

Subsequent refreshes are incremental — only new/changed files are reprocessed.

### 5. Search

```bash
# Simple search
sift search "authentication flow"

# Filter by collection
sift search "rate limiting" --collection vault

# Recent files only
sift search "meeting notes" --since 1w

# Human-friendly terminal output
sift search "API design" --pretty
```

## Output Modes

```
sift search <query>           # Default: AI-optimized (for piping to agents)
sift search <query> --pretty  # Human-readable with colors and editor commands
sift search <query> --json    # Machine-readable with full score components
sift search <query> --files   # Just file paths (good for xargs/piping)
```

**Default mode** returns 20 results ordered best-first. Designed for AI agents that read from the top.

**Pretty mode** is designed for humans reading in a terminal:

- Shows half the default results (10) to keep output scannable
- Reverses the order so the best result appears at the bottom (where your terminal scrolls to)
- Adds ANSI colors, visual separators, and editor open commands
- Labels (`[a]`, `[b]`, ...) stay consistent: `[a]` is always the best match

## Giving Feedback

After a search, use the search ID from the header to record feedback:

```bash
sift search "auth flow"                          # header shows id:abc123
sift feedback abc123 --positive a,b --negative d  # a,b were good, d was bad
```

Feedback permanently adjusts chunk rankings via Bayesian scoring. Even a single signal shifts future results.

## Folder Indexes (`sift.toml`)

`sift refresh` also maintains per-folder `sift.toml` files alongside your
content. They carry mechanical hashes and counts always; opt-in LLM
summaries describe each folder's `purpose` / `use_when` and per-file
`summary`. Search results are decorated with this context by default
(`--with-index`, on).

```bash
sift index                                       # read the tree (TTY=md, pipe=json)
sift index check                                 # lint for stale/missing entries
sift refresh --index-only --generate=missing     # AI-bootstrap empty fields
```

See [Folder Indexes](folder-indexes/overview.md) for the full design.

## Next Steps

- [Configuration reference](configuration.md) — tune every aspect of the search pipeline
- [Architecture](architecture.md) — understand how scoring, fusion, and feedback work
- [CLI Reference](cli-reference.md) — all commands and flags
- [Folder Indexes](folder-indexes/overview.md) — `sift.toml` and `sift index`
- [Daemon](daemon.md) — sub-second repeated queries via warm process
- [.siftignore](siftignore.md) — exclude files from indexing
