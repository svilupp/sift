# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Keep it brief!

## [Unreleased]

## [0.5.0] - 2026-05-09

- Add committed per-folder indexes (`sift.toml`) that carry `purpose`, `use_when`, and per-file summaries. `sift refresh` maintains them automatically; inspect a tree with `sift index [path]`. Search results decorate hits with their enclosing folder's context (toggle via `--with-index` / `--no-index`).
- Add opt-in LLM generation of folder/file summaries via `sift refresh --generate=missing|stale|all` (default `none`, no API calls). Requires `DEEPINFRA_API_KEY`.
- Add reference tooling: `sift links` for forward/backlink inspection, `sift refs stamp|validate|lint` for code-ref maintenance, and `sift keywords` for per-folder TF-IDF keywords.

## [0.4.0] - 2026-05-03

- Add background daemon that auto-spawns on first search and keeps the index and Voyage TLS connection warm — repeated queries drop from ~3-5 s to sub-second. Manage with `sift daemon start|stop|status|logs`; opt out via `SIFT_NO_DAEMON=1`.

## [0.3.0] - 2026-03-09

- Fix intermittent zero-result searches, stabilize result ordering, improve diagnostics and dead letter management
- Add richer vault search with section-level results, compact/agent views, directory keywords, and backlink-aware ranking across markdown, wikilinks, and common in-text document references

## [0.2.0] - 2026-02-24

### Added
- `--file` flag for line-level BM25 search within specific files (no indexing or API calls needed)

## [0.1.0] - 2026-02-22

### Added
- First official release
- Hybrid search: BM25 (Bleve) + vector (Voyage API) + RRF fusion + reranking
- SQLite storage (pure Go, no CGo)
- CLI: `search`, `refresh`, `feedback`, `collections`, `config`, `sql`
- Incremental indexing with content-hash change detection
- Adaptive top-K with score-cliff detection
- Search result caching (TTL-based)
- `.siftignore` support (gitignore-style globs)
- Content deduplication in search results
- Dead letter queue for failed API calls with retry
- JSONL weekly-rotated logging
