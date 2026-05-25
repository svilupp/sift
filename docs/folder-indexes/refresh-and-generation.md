# Refresh & LLM Generation

**TL;DR.** `sift refresh` always maintains `sift.toml` mechanically
(hashes, counts, signatures). LLM-written text (`purpose`, `use_when`,
`summary`) is opt-in via `--generate=missing|stale|all`. Long bootstraps
can `--detach`; failures land in `dead_letters` with `kind='index_summary'`.

## Two passes, one command

```
sift refresh
  ├── Phase A: chunk + embed corpus (BM25 + vector)
  └── Phase B: maintain sift.toml
        ├── B1 (mechanical, always-on): file lists, hashes, signatures
        └── B2 (LLM, opt-in):           purpose / use_when / summary
```

`--no-index` skips Phase B entirely. `--index-only` runs only Phase B
and is the canonical mode for bootstrapping or auditing folder indexes
without re-chunking.

## `--generate` modes

| Mode | Calls LLM for | Use when |
|------|---------------|----------|
| `none` (default) | nothing | day-to-day refresh; you don't want LLM cost |
| `missing` | empty `purpose` / `summary` fields | filling gaps after a manual edit or new files |
| `stale` | folders whose freshness signature drifted | scheduled maintenance |
| `all` | every folder | initial bootstrap or model swap |

## Concurrency

The worker pool runs at most one folder at a time per slot, with a
default of 20 slots (`--concurrency`). Reduce on rate-limited APIs;
increase for very large trees if your provider tolerates it.

The pool guarantees:

- Each folder gets at most one in-flight LLM call.
- Failed folders retry up to 3 times with exponential backoff.
- Persistent failures are dead-lettered and logged.
- Cancellation propagates cleanly; in-flight folders finish their
  current call.

## Progress UX

`--progress=auto|json|none` controls stdout output during the run:

- `auto` — sensible default; pretty bar on a TTY, nothing when piped.
- `json` — NDJSON events, one per line, machine-parseable.
- `none` — silent.

### NDJSON event schema

Every event has an `event` discriminator:

| Event | When emitted | Key fields |
|-------|--------------|-----------|
| `scan_done` | after the tree walk completes | `folders_total`, `stale`, `skipped` |
| `folder_start` | a worker picks up a folder | `path`, `files`, `words` |
| `folder_done` | folder generated successfully | `path`, `ms`, `tokens_in`, `tokens_out`, `cost_usd`, `attempts` |
| `folder_error` | folder failed (after retries) | `path`, `err`, `attempts`, `dead_lettered` |
| `heartbeat` | periodic (every few seconds) | `in_flight`, `completed`, `queue_remaining`, `elapsed_s`, `eta_s`, `spent_usd` |
| `summary` | terminal event | `summary` object (see below) |

Final `summary` payload:

```json
{
  "event": "summary",
  "summary": {
    "folders_total": 42,
    "folders_stale": 7,
    "folders_done": 40,
    "folders_failed": 2,
    "folders_dead_lettered": 2,
    "wall_seconds": 31.2,
    "concurrency": 20,
    "calls_total": 42,
    "calls_retried": 3,
    "rate_limited": 0,
    "timed_out": 0,
    "tokens_in": 80312,
    "tokens_out": 7042
  }
}
```

## Detached runs

`--detach` forks the process into the background, logs to
`~/.sift/refresh-index.log`, and writes `~/.sift/refresh-index.pid`.
Inspect with:

```bash
sift refresh --status
```

Output reports whether a run is in flight, the PID, and the last few
log lines.

Only one detached run can be active at a time; trying to start a second
is rejected.

## Dead letters

LLM failures (rate limits, timeouts, malformed responses) are recorded
in the `dead_letters` table with `kind='index_summary'` and the folder
path as payload. Re-queue with:

```bash
sift config retry-dead-letters
```

This re-runs only the failed folders without re-walking the tree.

## Cost tracking

Every successful `folder_done` event carries `tokens_in`, `tokens_out`,
and `cost_usd`. Aggregating across a run's NDJSON stream gives an exact
total:

```bash
sift refresh --index-only --generate=all --progress=json --collection vault \
  | jq -s 'map(select(.event=="folder_done")) | {folders: length, tokens_in: (map(.tokens_in)|add), cost: (map(.cost_usd)|add)}'
```

The final `summary` event also reports rolled-up totals.

## Examples

```bash
# Day-to-day: just keep hashes fresh
sift refresh

# Skip sift.toml entirely
sift refresh --no-index

# Bootstrap a new collection in one shot
sift refresh --index-only --generate=all --collection vault --detach
sift refresh --status

# Targeted: only fill gaps in a subtree
sift refresh --index-only --generate=missing --collection vault \
  --progress=json | tee /tmp/run.ndjson

# Recover after a flaky run
sift config retry-dead-letters
```
