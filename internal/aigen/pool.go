package aigen

import (
	"context"
	"fmt"
	"runtime/debug"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Job is one folder of work for the pool.
type Job struct {
	Folder FolderContext
	Files  []FileBatch
	// Order optionally drives the partitioner ("authority" default,
	// "mtime" opt-in).
	Order string
	// OnDone, if non-nil, is invoked from the worker after a successful
	// FolderResult is produced. Used by the sync hook to write
	// `sift.toml` atomically. Errors from OnDone propagate as a
	// folder_error event.
	OnDone func(ctx context.Context, j Job, fr *FolderResult) error
}

// PoolOptions tunes RunPool. Zero values are sane defaults.
type PoolOptions struct {
	// Concurrency caps in-flight folder workers. Default: 20.
	Concurrency int

	// Generator is the AI backend. Required.
	Generator Generator

	// HeartbeatInterval is the period between heartbeat events.
	// Default: 30s. Set negative to disable.
	HeartbeatInterval time.Duration

	// Now is an optional clock override (for tests).
	Now func() time.Time

	// Collection is the human-readable collection name; included in
	// the final summary event.
	Collection string

	// EventBuffer is the size of the events channel (default 64).
	EventBuffer int
}

// poolStats accumulates run-wide counters used in the summary event.
type poolStats struct {
	mu              sync.Mutex
	completed       int
	failed          int
	deadLettered    int
	calls           int
	callsRetried    int
	rateLimited     int
	timedOut        int
	tokensIn        int
	tokensOut       int
	costUSD         float64
	folderLatencies []int64
	deadLetterPaths []string
}

// RunPool spawns up to opts.Concurrency workers, each processing one
// Job at a time. Returns a buffered channel that receives every event
// (scan_done, folder_start, folder_done, folder_error, heartbeat,
// summary) and closes once the run is fully drained.
//
// Callers MUST drain the channel; closing the context only signals
// graceful drain — workers complete in-flight calls before exiting.
//
// Heartbeat goroutine runs alongside the workers.
func RunPool(ctx context.Context, jobs []Job, opts PoolOptions) <-chan PoolEvent {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 20
	}
	if opts.HeartbeatInterval == 0 {
		opts.HeartbeatInterval = 30 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.EventBuffer <= 0 {
		opts.EventBuffer = 64
	}

	events := make(chan PoolEvent, opts.EventBuffer)

	go func() {
		defer close(events)

		start := opts.Now()
		stats := &poolStats{}
		var inFlight atomic.Int64
		var queueRemaining atomic.Int64
		queueRemaining.Store(int64(len(jobs)))

		emit := func(ev PoolEvent) {
			ev.Timestamp = opts.Now()
			select {
			case events <- ev:
			case <-ctx.Done():
				// Best-effort: drop events when channel jammed and ctx done.
				select {
				case events <- ev:
				default:
				}
			}
		}

		// emitNonBlocking sends an event if the buffer has room and drops
		// it otherwise. Used for heartbeat events, which are diagnostic
		// and best-effort: a slow or stalled consumer must not be able to
		// pin the heartbeat goroutine on a blocking send (which would
		// keep the events channel from ever closing — see the goroutine
		// leak risk where hbCancel cannot unblock an in-flight send).
		emitNonBlocking := func(ev PoolEvent) bool {
			ev.Timestamp = opts.Now()
			select {
			case events <- ev:
				return true
			default:
				return false
			}
		}

		emit(PoolEvent{
			Kind:         EventScanDone,
			FoldersTotal: len(jobs),
			Stale:        len(jobs),
		})

		// Heartbeat goroutine.
		hbCtx, hbCancel := context.WithCancel(context.Background())
		defer hbCancel()
		var hbWG sync.WaitGroup
		if opts.HeartbeatInterval > 0 {
			hbWG.Add(1)
			go func() {
				defer hbWG.Done()
				ticker := time.NewTicker(opts.HeartbeatInterval)
				defer ticker.Stop()
				for {
					select {
					case <-hbCtx.Done():
						return
					case <-ticker.C:
						stats.mu.Lock()
						completed := stats.completed
						spent := stats.costUSD
						stats.mu.Unlock()
						elapsed := opts.Now().Sub(start).Seconds()
						queue := queueRemaining.Load()
						// Heartbeat events are best-effort; dropped if
						// the consumer is slow. A blocking send here
						// could deadlock the pool: hbCancel only stops
						// the ticker, it cannot unblock a send that is
						// already pending on `events`.
						emitNonBlocking(PoolEvent{
							Kind:           EventHeartbeat,
							InFlight:       int(inFlight.Load()),
							Completed:      completed,
							QueueRemaining: int(queue),
							ElapsedSec:     elapsed,
							SpentUSD:       spent,
						})
					}
				}
			}()
		}

		// Worker loop. We use a job channel + N workers rather than a
		// semaphore so cancellation drains cleanly.
		jobCh := make(chan Job, len(jobs))
		for _, j := range jobs {
			jobCh <- j
		}
		close(jobCh)

		var workerWG sync.WaitGroup
		for i := 0; i < opts.Concurrency; i++ {
			workerWG.Add(1)
			go func() {
				defer workerWG.Done()
				for j := range jobCh {
					if ctx.Err() != nil {
						// Drain remaining jobs without taking work.
						queueRemaining.Add(-1)
						continue
					}
					queueRemaining.Add(-1)
					inFlight.Add(1)
					processOneJob(ctx, j, opts.Generator, stats, emit)
					inFlight.Add(-1)
				}
			}()
		}
		workerWG.Wait()
		hbCancel()
		hbWG.Wait()

		// Final summary.
		stats.mu.Lock()
		latencies := append([]int64(nil), stats.folderLatencies...)
		summary := &RunSummary{
			Collection:          opts.Collection,
			FoldersTotal:        len(jobs),
			FoldersStale:        len(jobs),
			FoldersDone:         stats.completed,
			FoldersFailed:       stats.failed,
			FoldersDeadLettered: stats.deadLettered,
			WallSeconds:         opts.Now().Sub(start).Seconds(),
			Concurrency:         opts.Concurrency,
			CallsTotal:          stats.calls,
			CallsRetried:        stats.callsRetried,
			RateLimited:         stats.rateLimited,
			TimedOut:            stats.timedOut,
			TokensIn:            stats.tokensIn,
			TokensOut:           stats.tokensOut,
			CostUSD:             stats.costUSD,
			Interrupted:         ctx.Err() != nil,
			DeadLetterPaths:     append([]string(nil), stats.deadLetterPaths...),
		}
		stats.mu.Unlock()
		summary.LatencyMsP50, summary.LatencyMsP95 = computePercentiles(latencies)
		emit(PoolEvent{Kind: EventSummary, Summary: summary})
	}()

	return events
}

func processOneJob(ctx context.Context, j Job, gen Generator, stats *poolStats, emit func(PoolEvent)) {
	defer func() {
		if r := recover(); r != nil {
			stats.mu.Lock()
			stats.failed++
			stats.mu.Unlock()
			emit(PoolEvent{
				Kind: EventFolderError,
				Path: j.Folder.RelPath,
				Err:  fmt.Sprintf("panic: %v\n%s", r, string(debug.Stack())),
			})
		}
	}()

	startedAt := time.Now()
	emit(PoolEvent{
		Kind:  EventFolderStart,
		Path:  j.Folder.RelPath,
		Files: len(j.Files),
		Words: countWords(j.Files),
	})

	if gen == nil {
		emit(PoolEvent{
			Kind:         EventFolderError,
			Path:         j.Folder.RelPath,
			Err:          "no generator configured",
			DeadLettered: true,
		})
		stats.mu.Lock()
		stats.failed++
		stats.deadLettered++
		stats.deadLetterPaths = append(stats.deadLetterPaths, j.Folder.RelPath)
		stats.mu.Unlock()
		return
	}

	// emitWarning forwards finalizeResult diagnostics (grounding,
	// truncation) onto the pool's event stream as first-class
	// PoolEvents. nil-callable for direct (non-pool) callers; the pool
	// always sets it so observers can react to warnings without parsing
	// stderr.
	emitWarning := func(folder, kind, message string) {
		emit(PoolEvent{
			Kind:    PoolEventKind(kind),
			Path:    folder,
			Warning: message,
		})
	}

	res, err := gen.GenerateFolder(ctx, GenerateInput{
		Folder:      j.Folder,
		Files:       j.Files,
		Order:       j.Order,
		EmitWarning: emitWarning,
	})

	// Accumulate stats from any calls that ran (success or failure).
	if res != nil {
		recordCalls(stats, res.Calls)
	}

	if err != nil {
		stats.mu.Lock()
		stats.failed++
		stats.deadLettered++
		stats.deadLetterPaths = append(stats.deadLetterPaths, j.Folder.RelPath)
		stats.mu.Unlock()
		emit(PoolEvent{
			Kind:         EventFolderError,
			Path:         j.Folder.RelPath,
			Err:          err.Error(),
			Attempts:     totalAttempts(res),
			DeadLettered: true,
		})
		return
	}

	if j.OnDone != nil {
		if dErr := j.OnDone(ctx, j, res.Folder); dErr != nil {
			stats.mu.Lock()
			stats.failed++
			stats.mu.Unlock()
			emit(PoolEvent{
				Kind: EventFolderError,
				Path: j.Folder.RelPath,
				Err:  fmt.Sprintf("on_done: %v", dErr),
			})
			return
		}
	}

	ms := time.Since(startedAt).Milliseconds()
	tokensIn := res.TotalTokensIn()
	tokensOut := res.TotalTokensOut()
	cost := CostUSD(tokensIn, tokensOut)

	stats.mu.Lock()
	stats.completed++
	stats.folderLatencies = append(stats.folderLatencies, ms)
	stats.mu.Unlock()

	emit(PoolEvent{
		Kind:      EventFolderDone,
		Path:      j.Folder.RelPath,
		Files:     len(j.Files),
		Ms:        ms,
		TokensIn:  tokensIn,
		TokensOut: tokensOut,
		CostUSD:   cost,
		Attempts:  maxAttempts(res),
	})
}

func recordCalls(stats *poolStats, calls []CallStats) {
	stats.mu.Lock()
	defer stats.mu.Unlock()
	for _, c := range calls {
		stats.calls++
		if c.Attempts > 1 {
			stats.callsRetried++
		}
		if c.HTTPStatus == 429 {
			stats.rateLimited++
		}
		stats.tokensIn += c.TokensIn
		stats.tokensOut += c.TokensOut
		stats.costUSD += CostUSD(c.TokensIn, c.TokensOut)
	}
}

func totalAttempts(res *GenerateResult) int {
	if res == nil {
		return 0
	}
	n := 0
	for _, c := range res.Calls {
		n += c.Attempts
	}
	return n
}

func maxAttempts(res *GenerateResult) int {
	if res == nil {
		return 0
	}
	n := 0
	for _, c := range res.Calls {
		if c.Attempts > n {
			n = c.Attempts
		}
	}
	return n
}

func countWords(files []FileBatch) int {
	n := 0
	for _, f := range files {
		n += f.Words
	}
	return n
}

func computePercentiles(latencies []int64) (p50, p95 int64) {
	if len(latencies) == 0 {
		return 0, 0
	}
	sorted := append([]int64(nil), latencies...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	idx := func(p float64) int {
		i := int(float64(len(sorted)-1) * p)
		if i < 0 {
			i = 0
		}
		if i >= len(sorted) {
			i = len(sorted) - 1
		}
		return i
	}
	return sorted[idx(0.5)], sorted[idx(0.95)]
}
