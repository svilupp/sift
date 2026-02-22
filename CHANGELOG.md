# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Keep it brief!

## [Unreleased]

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
