# Sift daemon

The sift daemon is an optional long-lived process that serves search and
refresh requests over a Unix socket. It exists to amortise three otherwise
per-invocation costs:

- The Voyage TLS + HTTP/2 handshake (~2,000 ms cold).
- SQLite open + Bleve index open (~150 ms).
- Config + log initialisation.

When a daemon is running, repeated `sift search` calls reuse a single warm
TLS connection and a single open index, dropping warm-query latency from
roughly 3-5 s to 0.6-1.2 s. The CLI still has a working in-process fallback
at all times — the daemon is an optimisation, never a hard dependency.

## Auto-spawn behaviour

You normally do **not** need to run `sift daemon start`. Every `sift search`
(and, where supported, `sift refresh`) invocation does roughly this:

1. Try to dial `~/.sift/sift.sock` with a 50 ms timeout.
2. If that fails, spawn a detached daemon child and poll its `/health`
   endpoint for up to ~300 ms.
3. If that still fails, run the request in-process exactly as before.

The dial in step 1 takes 30-100 µs on macOS, so the per-invocation overhead
when no daemon is needed is negligible.

The daemon exits on its own after a configurable idle period (default
30 minutes of no requests). The next CLI call respawns it transparently.

## Manual lifecycle

For power users and scripts:

```text
sift daemon start            spawn the daemon and wait for /health
sift daemon stop             POST /shutdown, fall back to SIGTERM, then SIGKILL
sift daemon restart          stop and start in one step (use after editing config)
sift daemon status [--json]  print state, pid, uptime, request/dial counts
sift daemon logs [-f] [-n N] tail ~/.sift/logs/daemon.log (default last 50 lines)
```

Exit codes for `sift daemon status`:

- `0` — running
- `1` — not running
- `2` — unhealthy (PID file present but socket unreachable)

`stop` is idempotent — calling it when no daemon is running exits 0.

## Environment variables

| Variable | Effect |
|---|---|
| `SIFT_NO_DAEMON` | If set to any non-empty value, the CLI skips the dial / spawn dance entirely and runs in-process. Useful for debugging and for environments where forking detached children is undesirable. |
| `SIFT_DAEMON_SOCKET` | Override the default socket path (`~/.sift/sift.sock`). Applies to both server and client sides so they cannot disagree. Primarily a test hook. |
| `SIFT_DIR` | Override the entire `~/.sift/` data directory. Indirectly relocates the socket, PID file, log file, and config. |
| `SIFT_DAEMON_SPAWNED` | Set automatically by the parent when spawning the daemon child. Not for end-user use. |

## Config knobs

`~/.sift/config.toml`:

```toml
[daemon]
enabled = true            # currently informational; CLI gating is via SIFT_NO_DAEMON
idle_timeout = "30m"      # 0 = never exit
spawn_timeout = "300ms"   # how long `daemon start` and auto-spawn wait for /health
dial_timeout = "50ms"     # how long the CLI waits when probing an existing daemon

[transport]
max_idle_conns_per_host = 8
idle_conn_timeout = "5m"
tls_handshake_timeout = "5s"
response_header_timeout = "30s"
```

`[transport]` is the http.Transport tuning shared by upstream API clients
(currently Voyage). The `idle_conn_timeout` of 5 minutes is deliberately
much higher than stdlib's 90 s default so the daemon's connection pool
stays warm across bursty query patterns.

Config is read once at daemon startup. After editing the file, run
`sift daemon restart` to pick up the new values; there is no hot reload.

## Files

| Path | Purpose |
|---|---|
| `~/.sift/sift.sock` | Unix domain socket the daemon listens on (mode `0600`). |
| `~/.sift/sift.pid` | PID file used both as a single-instance lock and as a SIGTERM target for `daemon stop`. |
| `~/.sift/logs/daemon.log` | Daemon's own log (slog records, one per request). |
| `~/.sift/logs/searches-*.jsonl` | Per-search structured log (same as in-process). |

All paths are created lazily; nothing is left behind after a clean
shutdown except the log file.

## Wire protocol

HTTP/1.1 over a Unix socket, JSON bodies. Endpoints:

- `GET  /health` — returns `{ok, version, uptime_s, pid, request_count, tls_dials_total, goroutines}`.
- `POST /search` — `SearchRequest` -> `SearchResponse` (mirrors `SearchOptions` / `Result`).
- `POST /refresh` — `RefreshRequest` -> NDJSON progress stream.
- `POST /shutdown` — graceful shutdown; daemon exits.

Anything else (`config`, `sql`, `collections`, `feedback`) bypasses the
daemon and runs in-process.

## Troubleshooting

### "daemon spawned but not ready within 300ms"

The child process started but didn't bind the socket in time. Most common
causes:

- A previous daemon left a stale `~/.sift/sift.pid` whose PID still exists
  (different process). Run `sift daemon status` to inspect, then
  `sift daemon stop` (which falls through to SIGKILL via the PID file).
- The Voyage Preconnect call at startup is hanging because of a slow
  network. Check `sift daemon logs` for `preconnect` lines.

### Stale socket

A `sift.sock` file left behind by a hard-killed daemon. `sift daemon
start` removes it before binding, so this is self-healing on the next
spawn. If it persists, just `rm ~/.sift/sift.sock` and try again.

### Version mismatch (CLI vs daemon)

After upgrading the `sift` binary, the running daemon may still be on the
old version. The CLI compares versions via `/health` and auto-restarts
the daemon when they differ. If the auto-restart fails (e.g. socket
permissions, stale lock), run `sift daemon restart` manually.

### "another sift daemon is already running"

The PID file is locked. This is the single-instance guarantee, not a
bug. Run `sift daemon status` to confirm, or `sift daemon stop` to
terminate the existing instance.

### Daemon hang

If the daemon stops responding (PID present, socket present, requests
hang), kill it with `sift daemon stop` — the stop path escalates from
graceful POST `/shutdown` -> SIGTERM -> SIGKILL automatically. The next
CLI call respawns it.

## Performance summary

Reproducible via `bash scripts/perf/measure.sh`.

| Metric | Today (no daemon) | With daemon |
|---|---|---|
| First query, fresh process | ~5,000 ms | ~5,200 ms (one-time spawn cost) |
| Subsequent warm queries | ~3,000-5,000 ms | **~600-1,200 ms** |
| Cache hit | ~50 ms | ~50 ms |
| `sift daemon status` round-trip | n/a | <10 ms |
| TLS dials per 20 queries | 20 | ≤ 2 |

The headline: repeated queries drop from seconds to sub-second with no
user-visible change in workflow. The only command users typically need
to learn is `sift daemon restart` after editing config.

## Security

- The socket is created with mode `0600` — accessible only to the user
  who started the daemon.
- The Voyage API key lives in daemon memory only; the CLI does not
  re-read it after the first daemon spawn.
- No remote / TCP listening. Unix socket only. No multi-tenant socket
  directory — each user's `~/.sift/` is already isolated.
