package aigen

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// drainAll drains the pool's event channel into a slice with a hard
// timeout. It returns the events seen and a boolean indicating whether
// the channel closed (true) or the timeout fired (false).
func drainAll(t *testing.T, ch <-chan PoolEvent, timeout time.Duration) ([]PoolEvent, bool) {
	t.Helper()
	var out []PoolEvent
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return out, true
			}
			out = append(out, ev)
		case <-deadline.C:
			return out, false
		}
	}
}

// stubGenerator returns canned results without making HTTP calls.
type stubGenerator struct {
	calls    atomic.Int32
	delay    time.Duration
	failPath string
	panicAt  string
}

func (s *stubGenerator) GenerateFolder(ctx context.Context, in GenerateInput) (*GenerateResult, error) {
	s.calls.Add(1)
	if in.Folder.RelPath == s.panicAt {
		panic("boom")
	}
	if s.delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.delay):
		}
	}
	if in.Folder.RelPath == s.failPath {
		return &GenerateResult{Calls: []CallStats{{Attempts: 1}}}, errors.New("forced failure")
	}
	files := make([]FileSummary, len(in.Files))
	for i, f := range in.Files {
		files[i] = FileSummary{Path: f.Path, Summary: "ok"}
	}
	return &GenerateResult{
		Folder: &FolderResult{Purpose: "P", Files: files},
		Calls:  []CallStats{{TokensIn: 100, TokensOut: 50, Attempts: 1}},
	}, nil
}

func mkJobs(n int) []Job {
	jobs := make([]Job, n)
	for i := 0; i < n; i++ {
		jobs[i] = Job{
			Folder: FolderContext{RelPath: fmt.Sprintf("folder-%02d", i)},
			Files:  []FileBatch{{Path: "a.md", Bytes: 100}},
		}
	}
	return jobs
}

func collectEvents(ch <-chan PoolEvent) []PoolEvent {
	var out []PoolEvent
	for ev := range ch {
		out = append(out, ev)
	}
	return out
}

func TestPoolFiftyFolders(t *testing.T) {
	gen := &stubGenerator{}
	jobs := mkJobs(50)
	ch := RunPool(context.Background(), jobs, PoolOptions{
		Concurrency:       20,
		Generator:         gen,
		HeartbeatInterval: -1,
	})
	evs := collectEvents(ch)

	var done, errs int
	var summary *PoolEvent
	for i := range evs {
		switch evs[i].Kind {
		case EventFolderDone:
			done++
		case EventFolderError:
			errs++
		case EventSummary:
			summary = &evs[i]
		}
	}
	if done != 50 {
		t.Errorf("expected 50 done, got %d", done)
	}
	if errs != 0 {
		t.Errorf("expected 0 errors, got %d", errs)
	}
	if summary == nil || summary.Summary == nil {
		t.Fatal("missing summary event")
	}
	if summary.Summary.FoldersDone != 50 {
		t.Errorf("summary.FoldersDone=%d", summary.Summary.FoldersDone)
	}
	if int(gen.calls.Load()) != 50 {
		t.Errorf("expected 50 generator calls, got %d", gen.calls.Load())
	}
}

func TestPoolPanicRecovery(t *testing.T) {
	gen := &stubGenerator{panicAt: "folder-02"}
	ch := RunPool(context.Background(), mkJobs(5), PoolOptions{
		Concurrency:       2,
		Generator:         gen,
		HeartbeatInterval: -1,
	})
	evs := collectEvents(ch)

	var done, errs int
	for _, ev := range evs {
		switch ev.Kind {
		case EventFolderDone:
			done++
		case EventFolderError:
			errs++
		}
	}
	if done != 4 || errs != 1 {
		t.Errorf("got done=%d errs=%d (want 4/1)", done, errs)
	}
}

func TestPoolDeadLetterOnError(t *testing.T) {
	gen := &stubGenerator{failPath: "folder-01"}
	ch := RunPool(context.Background(), mkJobs(3), PoolOptions{
		Concurrency:       2,
		Generator:         gen,
		HeartbeatInterval: -1,
	})
	evs := collectEvents(ch)

	var summary *PoolEvent
	for i := range evs {
		if evs[i].Kind == EventSummary {
			summary = &evs[i]
		}
	}
	if summary == nil || summary.Summary == nil {
		t.Fatal("no summary")
	}
	if summary.Summary.FoldersDeadLettered != 1 {
		t.Errorf("expected 1 dead-lettered, got %d", summary.Summary.FoldersDeadLettered)
	}
	if len(summary.Summary.DeadLetterPaths) != 1 || summary.Summary.DeadLetterPaths[0] != "folder-01" {
		t.Errorf("dead-letter paths: %v", summary.Summary.DeadLetterPaths)
	}
}

func TestPoolCancellation(t *testing.T) {
	gen := &stubGenerator{delay: 50 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	jobs := mkJobs(20)
	ch := RunPool(ctx, jobs, PoolOptions{
		Concurrency:       4,
		Generator:         gen,
		HeartbeatInterval: -1,
	})
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()
	evs := collectEvents(ch)

	var summary *PoolEvent
	for i := range evs {
		if evs[i].Kind == EventSummary {
			summary = &evs[i]
		}
	}
	if summary == nil || summary.Summary == nil {
		t.Fatal("no summary")
	}
	if !summary.Summary.Interrupted {
		t.Errorf("expected interrupted=true")
	}
	// Some folders finish before cancellation; total processed must be ≤ 20.
	if summary.Summary.FoldersDone > 20 {
		t.Errorf("done count out of range: %d", summary.Summary.FoldersDone)
	}
}

func TestPoolHeartbeat(t *testing.T) {
	gen := &stubGenerator{delay: 30 * time.Millisecond}
	ch := RunPool(context.Background(), mkJobs(8), PoolOptions{
		Concurrency:       2,
		Generator:         gen,
		HeartbeatInterval: 5 * time.Millisecond,
	})
	evs := collectEvents(ch)
	var hbs int
	for _, ev := range evs {
		if ev.Kind == EventHeartbeat {
			hbs++
		}
	}
	if hbs == 0 {
		t.Error("expected at least one heartbeat event")
	}
}

func TestPoolOnDoneCallback(t *testing.T) {
	gen := &stubGenerator{}
	var doneCount atomic.Int32
	jobs := mkJobs(3)
	for i := range jobs {
		jobs[i].OnDone = func(ctx context.Context, j Job, fr *FolderResult) error {
			doneCount.Add(1)
			return nil
		}
	}
	ch := RunPool(context.Background(), jobs, PoolOptions{
		Concurrency:       2,
		Generator:         gen,
		HeartbeatInterval: -1,
	})
	collectEvents(ch)
	if doneCount.Load() != 3 {
		t.Errorf("OnDone called %d times; want 3", doneCount.Load())
	}
}

func TestPoolNilGenerator(t *testing.T) {
	ch := RunPool(context.Background(), mkJobs(2), PoolOptions{
		Concurrency:       1,
		HeartbeatInterval: -1,
	})
	evs := collectEvents(ch)
	var errs int
	for _, ev := range evs {
		if ev.Kind == EventFolderError {
			errs++
		}
	}
	if errs != 2 {
		t.Errorf("expected 2 errors with nil generator; got %d", errs)
	}
}

// TestPoolHeartbeatDropsWhenConsumerStalls exercises the heartbeat
// goroutine-leak fix. Previously a blocking heartbeat emit could pin
// the pool main goroutine on hbWG.Wait() forever when the consumer
// stalled and the events buffer filled. Now heartbeats are best-effort
// and drop on a full buffer, so cancellation always drains the pool.
func TestPoolHeartbeatDropsWhenConsumerStalls(t *testing.T) {
	gen := &stubGenerator{delay: 5 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobs := mkJobs(2)
	ch := RunPool(ctx, jobs, PoolOptions{
		Concurrency:       1,
		Generator:         gen,
		HeartbeatInterval: 1 * time.Millisecond,
		// Tiny buffer so the heartbeat ticker fills it fast once the
		// consumer stops reading.
		EventBuffer: 2,
	})

	// Read just enough to let scan_done flow, then stall.
	first, ok := <-ch
	if !ok {
		t.Fatal("channel closed before any event")
	}
	if first.Kind != EventScanDone {
		t.Fatalf("first event = %v, want scan_done", first.Kind)
	}

	// Sleep long enough that, in the buggy version, the heartbeat
	// goroutine would be parked on a blocking send. With the fix in
	// place those sends drop and the goroutine keeps ticking.
	time.Sleep(50 * time.Millisecond)

	// Cancel and resume draining. The primary invariant the leak fix
	// must uphold: the pool closes the events channel within a bounded
	// window after cancel. Before the fix, a heartbeat send pinned on
	// a full buffer would block hbWG.Wait() forever and the channel
	// would never close.
	cancel()
	rest, closed := drainAll(t, ch, 2*time.Second)
	if !closed {
		t.Fatalf("pool did not close events channel after cancel; drained %d events then timed out", len(rest))
	}
	// We don't assert on summary presence: under a tiny buffer with
	// cancellation racing the final emit, the summary itself may drop
	// (emit's ctx.Done() branch is also best-effort). The channel
	// closing cleanly is what proves the heartbeat goroutine exited.
}

// TestPoolHeartbeatDoesNotBlockWorkers verifies that even when the
// events buffer is small and heartbeats fire constantly, workers still
// finish all jobs and the pool exits cleanly.
func TestPoolHeartbeatDoesNotBlockWorkers(t *testing.T) {
	gen := &stubGenerator{delay: 1 * time.Millisecond}
	jobs := mkJobs(8)
	ch := RunPool(context.Background(), jobs, PoolOptions{
		Concurrency:       2,
		Generator:         gen,
		HeartbeatInterval: 1 * time.Millisecond,
		EventBuffer:       2,
	})

	evs, closed := drainAll(t, ch, 5*time.Second)
	if !closed {
		t.Fatalf("pool did not close events within timeout; drained %d events", len(evs))
	}

	var done int
	var summary *PoolEvent
	for i := range evs {
		switch evs[i].Kind {
		case EventFolderDone:
			done++
		case EventSummary:
			summary = &evs[i]
		}
	}
	if done != 8 {
		t.Errorf("expected 8 folder_done events, got %d", done)
	}
	if summary == nil || summary.Summary == nil {
		t.Fatal("missing summary event")
	}
	if summary.Summary.FoldersDone != 8 {
		t.Errorf("summary.FoldersDone=%d; want 8", summary.Summary.FoldersDone)
	}
	if summary.Summary.Interrupted {
		t.Errorf("summary.Interrupted=true; want false")
	}
	if int(gen.calls.Load()) != 8 {
		t.Errorf("generator called %d times; want 8", gen.calls.Load())
	}
}

// groundingStubGenerator returns a result whose `purpose` mentions a
// proper noun ("Acme") that does not appear anywhere in the file
// batches. This triggers CheckPurposeGrounding inside finalizeResult.
//
// The stub deliberately calls the EmitWarning callback path indirectly
// — via finalizeResult — by re-entering through NewFolderGeneratorFromGen
// in the test, which threads EmitWarning. Here we short-circuit that
// by directly invoking the supplied callback so the assertion runs
// even when a future refactor stops calling finalizeResult on every
// path. (We still rely on the production path in the assertion below.)
type groundingStubGenerator struct{}

func (g *groundingStubGenerator) GenerateFolder(ctx context.Context, in GenerateInput) (*GenerateResult, error) {
	files := make([]FileSummary, len(in.Files))
	for i, f := range in.Files {
		files[i] = FileSummary{Path: f.Path, Summary: "ok"}
	}
	// Purpose mentions "Acme" which is not present in any file
	// payload — finalizeResult should produce a grounding warning.
	fr := &FolderResult{Purpose: "Tracks the Acme notes.", Files: files}
	finalizeResult(fr, in.Folder, in.Files, in.EmitWarning)
	return &GenerateResult{
		Folder: fr,
		Calls:  []CallStats{{Attempts: 1}},
	}, nil
}

func TestPoolEmitsGroundingWarning(t *testing.T) {
	jobs := []Job{{
		Folder: FolderContext{RelPath: "weekly"},
		Files:  []FileBatch{{Path: "week1.md", HeadWords: "hours logged", Bytes: 100}},
	}}
	ch := RunPool(context.Background(), jobs, PoolOptions{
		Concurrency:       1,
		Generator:         &groundingStubGenerator{},
		HeartbeatInterval: -1,
	})
	evs := collectEvents(ch)

	var warnings []PoolEvent
	for _, ev := range evs {
		if ev.Kind == EventGroundingWarning {
			warnings = append(warnings, ev)
		}
	}
	if len(warnings) == 0 {
		t.Fatalf("expected at least one grounding_warning event; got events: %+v", evs)
	}
	w := warnings[0]
	if w.Path != "weekly" {
		t.Errorf("warning.Path=%q; want %q", w.Path, "weekly")
	}
	if !strings.Contains(w.Warning, "Acme") {
		t.Errorf("warning.Warning=%q; want substring %q", w.Warning, "Acme")
	}
}

// truncationStubGenerator returns a per-file summary that exceeds
// MaxFileSummaryChars so finalizeResult emits a summary_truncated event.
type truncationStubGenerator struct{}

func (g *truncationStubGenerator) GenerateFolder(ctx context.Context, in GenerateInput) (*GenerateResult, error) {
	long := strings.Repeat("a", MaxFileSummaryChars+50)
	files := make([]FileSummary, len(in.Files))
	for i, f := range in.Files {
		files[i] = FileSummary{Path: f.Path, Summary: long}
	}
	fr := &FolderResult{Purpose: "Notes folder.", Files: files}
	finalizeResult(fr, in.Folder, in.Files, in.EmitWarning)
	return &GenerateResult{
		Folder: fr,
		Calls:  []CallStats{{Attempts: 1}},
	}, nil
}

func TestPoolEmitsSummaryTruncated(t *testing.T) {
	jobs := []Job{{
		Folder: FolderContext{RelPath: "notes"},
		Files:  []FileBatch{{Path: "n.md", HeadWords: "stuff", Bytes: 100}},
	}}
	ch := RunPool(context.Background(), jobs, PoolOptions{
		Concurrency:       1,
		Generator:         &truncationStubGenerator{},
		HeartbeatInterval: -1,
	})
	evs := collectEvents(ch)

	var truncs []PoolEvent
	for _, ev := range evs {
		if ev.Kind == EventSummaryTruncated {
			truncs = append(truncs, ev)
		}
	}
	if len(truncs) != 1 {
		t.Fatalf("expected 1 summary_truncated event; got %d (events: %+v)", len(truncs), evs)
	}
	if truncs[0].Path != "notes" {
		t.Errorf("path=%q; want notes", truncs[0].Path)
	}
	if truncs[0].Warning == "" {
		t.Errorf("expected non-empty warning message")
	}
}

func TestPoolEmptyJobs(t *testing.T) {
	ch := RunPool(context.Background(), nil, PoolOptions{
		Concurrency:       1,
		Generator:         &stubGenerator{},
		HeartbeatInterval: -1,
	})
	evs := collectEvents(ch)
	if len(evs) < 2 {
		t.Fatalf("expected at least scan_done + summary; got %d", len(evs))
	}
	if evs[0].Kind != EventScanDone || evs[len(evs)-1].Kind != EventSummary {
		t.Errorf("unexpected event order: %v", evs)
	}
}
