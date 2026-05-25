//go:build integration

// Package daemon integration tests spin up a real daemon.Serve in-process
// (not via SpawnDetached) and exercise the full HTTP-over-Unix-socket
// pipeline through the production Client.
//
// Limitations / design notes:
//
//   - Serve() builds a real *search.Engine from the open DB + Bleve index +
//     Voyage client. We do NOT inject a stub engine because that would
//     require a non-trivial production-code change. Instead, every test
//     that touches /search uses an empty database (so BM25 returns no
//     hits) and an empty Voyage API key (so vector search fails — which is
//     non-fatal per search/search.go:347-358 and falls back to BM25-only).
//     Net effect: /search returns 200 with zero results, which is exactly
//     the shape these tests assert.
//
//   - Each test isolates the daemon in its own tempdir via SIFT_DIR +
//     SIFT_DAEMON_SOCKET. We use os.MkdirTemp("", "sift") rather than
//     t.TempDir() because the latter produces paths that on macOS test
//     runners frequently exceed sun_path's 104-char limit.
//
//   - goleak runs once at process exit via TestMain in client_test.go;
//     per-test goroutine assertions piggy-back on that and on the
//     defer-stop discipline in startTestDaemon.
//
// Run with: go test -tags integration ./internal/daemon/...
package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"sift/internal/config"
)

// startTestDaemon launches Serve in a background goroutine wired to a
// tempdir-isolated SIFT_DIR + short Unix socket path. It blocks until the
// daemon answers /health, then returns a dialed Client and a stop func
// that issues /shutdown and waits for Serve to return.
//
// The stop func is the only safe way to terminate the daemon — calling
// it ensures the Serve goroutine has exited and all tempdir cleanup has
// run. Tests MUST defer stop().
func startTestDaemon(t *testing.T, cfg *config.Config) (*Client, func()) {
	t.Helper()

	// Short tempdir so Unix socket path stays under 104 chars on macOS.
	dir, err := os.MkdirTemp("", "sift")
	if err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })

	sockPath := filepath.Join(dir, "d.sock")
	if len(sockPath) >= 104 {
		t.Fatalf("socket path too long (%d chars): %s", len(sockPath), sockPath)
	}

	t.Setenv("SIFT_DIR", dir)
	t.Setenv("SIFT_DAEMON_SOCKET", sockPath)

	// Sensible defaults for a test daemon: no upstream API calls, short
	// idle timeout disabled by default (caller can override).
	if cfg == nil {
		cfg = config.Default()
	}
	cfg.API.VoyageAPIKey = ""
	if cfg.Daemon.IdleTimeout == 0 {
		// Default to disabled so tests don't race the idle timer.
		cfg.Daemon.IdleTimeout = config.Duration(0)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- Serve(ctx, cfg)
	}()

	client, dialErr := DialUntilReady(5 * time.Second)
	if dialErr != nil {
		cancel()
		<-errCh
		t.Fatalf("daemon never became ready: %v", dialErr)
	}

	stopped := false
	var stopMu sync.Mutex
	stop := func() {
		stopMu.Lock()
		if stopped {
			stopMu.Unlock()
			return
		}
		stopped = true
		stopMu.Unlock()

		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		if err := client.Shutdown(shutCtx); err != nil {
			t.Logf("client.Shutdown: %v", err)
		}
		client.Close()

		select {
		case err := <-errCh:
			if err != nil {
				t.Errorf("Serve returned error: %v", err)
			}
		case <-time.After(15 * time.Second):
			cancel()
			<-errCh
			t.Errorf("Serve did not return within 15s")
		}
		cancel()
	}
	return client, stop
}

// ---------------------------------------------------------------------------
// 1) End-to-end search
// ---------------------------------------------------------------------------

func TestEndToEndSearch(t *testing.T) {
	client, stop := startTestDaemon(t, nil)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	resp, err := client.Search(ctx, SearchRequest{
		Query: "anything",
		TopK:  5,
	})
	if err != nil {
		t.Fatalf("client.Search: %v", err)
	}

	// Empty DB + empty Bleve + no Voyage key: BM25 returns nothing,
	// vector search errors but is non-fatal; final result count is 0.
	if resp.SearchID == "" {
		t.Errorf("search_id: empty")
	}
	if resp.Query != "anything" {
		t.Errorf("query: got %q want %q", resp.Query, "anything")
	}
	if len(resp.Results) != 0 {
		t.Errorf("results: got %d want 0 (empty index)", len(resp.Results))
	}
	if resp.Meta.ResultCount != 0 {
		t.Errorf("meta.result_count: got %d want 0", resp.Meta.ResultCount)
	}
	if resp.FeedbackCmd == "" {
		t.Errorf("feedback_cmd: empty")
	}
}

// ---------------------------------------------------------------------------
// 2) End-to-end health
// ---------------------------------------------------------------------------

func TestEndToEndHealth(t *testing.T) {
	client, stop := startTestDaemon(t, nil)
	defer stop()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	resp, err := client.Health(ctx)
	if err != nil {
		t.Fatalf("Health: %v", err)
	}

	// Serve runs in-process, NOT via SpawnDetached, so the daemon's
	// reported PID must match this test process.
	if resp.PID != os.Getpid() {
		t.Errorf("pid: got %d want %d (daemon should run in test process)", resp.PID, os.Getpid())
	}
	if !resp.Ok {
		t.Errorf("ok: want true")
	}
	if resp.Version == "" {
		t.Errorf("version: empty")
	}
	if resp.StartedAt == "" {
		t.Errorf("started_at: empty")
	}
}

// ---------------------------------------------------------------------------
// 3) Concurrent searches
// ---------------------------------------------------------------------------

func TestConcurrentSearches(t *testing.T) {
	client, stop := startTestDaemon(t, nil)
	defer stop()

	const N = 50
	var wg sync.WaitGroup
	errCh := make(chan error, N)

	// Capture pre-test request count so the assertion accommodates the
	// dial-until-ready /health probe (and any internal warmup polls).
	hctx, hcancel := context.WithTimeout(context.Background(), 2*time.Second)
	preHealth, err := client.Health(hctx)
	hcancel()
	if err != nil {
		t.Fatalf("pre Health: %v", err)
	}
	pre := preHealth.RequestCount

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_, err := client.Search(ctx, SearchRequest{
				Query: fmt.Sprintf("q-%d", i),
				TopK:  3,
			})
			if err != nil {
				errCh <- err
			}
		}(i)
	}
	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent search: %v", err)
	}

	// Drain: read /health and assert request_count rose by at least N.
	hctx2, hcancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer hcancel2()
	postHealth, err := client.Health(hctx2)
	if err != nil {
		t.Fatalf("post Health: %v", err)
	}
	delta := postHealth.RequestCount - pre
	// Exact accounting: N searches + the post-Health call itself = N+1.
	// /health is intentionally not counted as user activity by the idle
	// timer reset path, but it IS counted by the request-count metric
	// surfaced through HealthResponse.RequestCount. If the daemon ever
	// changes which paths increment that counter, this assertion will
	// catch it.
	if want := int64(N + 1); delta != want {
		t.Errorf("request_count delta: got %d want %d (N=%d searches + 1 post-health)",
			delta, want, N)
	}
}

// ---------------------------------------------------------------------------
// 4) Shutdown drains in-flight requests
// ---------------------------------------------------------------------------

// TestShutdownDrains verifies that Shutdown waits for in-flight requests
// to finish — the production server uses srv.Shutdown which closes the
// listener and waits for active handlers.
//
// We can't inject a slow handler without modifying production code, so
// the test fires a search, then immediately fires Shutdown from another
// goroutine. The search must complete cleanly (no connection-reset) and
// the daemon goroutine must exit with nil error.
func TestShutdownDrains(t *testing.T) {
	// Hand-rolled startup so we control the shutdown timing precisely.
	dir, err := os.MkdirTemp("", "sift")
	if err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	defer os.RemoveAll(dir)

	sockPath := filepath.Join(dir, "d.sock")
	t.Setenv("SIFT_DIR", dir)
	t.Setenv("SIFT_DAEMON_SOCKET", sockPath)

	cfg := config.Default()
	cfg.API.VoyageAPIKey = ""
	cfg.Daemon.IdleTimeout = config.Duration(0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Serve(ctx, cfg)
	}()

	client, err := DialUntilReady(5 * time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	// Fire a search and a shutdown concurrently. The search must not be
	// interrupted by the shutdown; the shutdown returns cleanly.
	var searchErr, shutErr error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		sctx, scancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer scancel()
		_, searchErr = client.Search(sctx, SearchRequest{Query: "drain", TopK: 1})
	}()
	go func() {
		defer wg.Done()
		// Tiny stagger so the search hits the wire first.
		time.Sleep(10 * time.Millisecond)
		shctx, shcancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shcancel()
		shutErr = client.Shutdown(shctx)
	}()
	wg.Wait()

	if searchErr != nil {
		t.Errorf("in-flight search interrupted by shutdown: %v", searchErr)
	}
	if shutErr != nil {
		t.Errorf("Shutdown: %v", shutErr)
	}

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Serve returned error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("Serve did not return within 15s")
	}
}

// ---------------------------------------------------------------------------
// 5) Idle timeout exits cleanly
// ---------------------------------------------------------------------------

func TestIdleExit(t *testing.T) {
	dir, err := os.MkdirTemp("", "sift")
	if err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	defer os.RemoveAll(dir)

	sockPath := filepath.Join(dir, "d.sock")
	t.Setenv("SIFT_DIR", dir)
	t.Setenv("SIFT_DAEMON_SOCKET", sockPath)

	cfg := config.Default()
	cfg.API.VoyageAPIKey = ""
	cfg.Daemon.IdleTimeout = config.Duration(200 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	start := time.Now()
	go func() {
		errCh <- Serve(ctx, cfg)
	}()

	// Don't dial — that resets the idle timer. Instead, poll the socket
	// file existence and let the timer fire. The daemon binds the socket
	// before the idle timer starts, so once it appears we know Serve is
	// past initialization.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(sockPath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Wait 600ms quietly. The idle timer (200ms) should fire.
	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Serve returned error: %v", err)
		}
		if elapsed := time.Since(start); elapsed > 5*time.Second {
			t.Errorf("idle exit too slow: %s", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatalf("daemon did not idle-exit within 3s")
	}
}

// ---------------------------------------------------------------------------
// 6) Stale socket cleanup
// ---------------------------------------------------------------------------

func TestStaleSocketCleanup(t *testing.T) {
	dir, err := os.MkdirTemp("", "sift")
	if err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	defer os.RemoveAll(dir)

	sockPath := filepath.Join(dir, "d.sock")
	t.Setenv("SIFT_DIR", dir)
	t.Setenv("SIFT_DAEMON_SOCKET", sockPath)

	// Pre-create a dummy file at SocketPath. Serve must remove it.
	if err := os.WriteFile(sockPath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("pre-create stale socket: %v", err)
	}

	cfg := config.Default()
	cfg.API.VoyageAPIKey = ""
	cfg.Daemon.IdleTimeout = config.Duration(0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Serve(ctx, cfg)
	}()

	client, err := DialUntilReady(5 * time.Second)
	if err != nil {
		// Drain
		cancel()
		<-errCh
		t.Fatalf("daemon never became ready (stale socket may have blocked): %v", err)
	}
	defer client.Close()

	// Sanity: the socket file is now a real socket, not a regular file.
	info, err := os.Stat(sockPath)
	if err != nil {
		t.Fatalf("stat socket: %v", err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Errorf("socket file is not a socket: mode=%s", info.Mode())
	}

	// Shutdown.
	shCtx, shCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := client.Shutdown(shCtx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	shCancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Errorf("Serve returned error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("Serve did not return within 15s")
	}
}

// ---------------------------------------------------------------------------
// 7) Single-instance lock
// ---------------------------------------------------------------------------

func TestSingleInstance(t *testing.T) {
	dir, err := os.MkdirTemp("", "sift")
	if err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	defer os.RemoveAll(dir)

	sockPath := filepath.Join(dir, "d.sock")
	t.Setenv("SIFT_DIR", dir)
	t.Setenv("SIFT_DAEMON_SOCKET", sockPath)

	cfg := config.Default()
	cfg.API.VoyageAPIKey = ""
	cfg.Daemon.IdleTimeout = config.Duration(0)

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	err1Ch := make(chan error, 1)
	go func() {
		err1Ch <- Serve(ctx1, cfg)
	}()

	// Wait for first instance to bind.
	deadline := time.Now().Add(5 * time.Second)
	bound := false
	for time.Now().Before(deadline) {
		if c, err := net.DialTimeout("unix", sockPath, 50*time.Millisecond); err == nil {
			_ = c.Close()
			bound = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !bound {
		cancel1()
		<-err1Ch
		t.Fatalf("first daemon never bound socket")
	}

	// Second instance must fail with an "already running"-flavored error.
	err2 := Serve(context.Background(), cfg)
	if err2 == nil {
		t.Fatalf("second Serve: want error, got nil")
	}
	if !strings.Contains(err2.Error(), "already running") {
		t.Errorf("second Serve error: want 'already running', got %q", err2.Error())
	}

	// Stop first instance.
	client := NewClient(sockPath)
	defer client.Close()
	shCtx, shCancel := context.WithTimeout(context.Background(), 5*time.Second)
	if err := client.Shutdown(shCtx); err != nil {
		t.Errorf("Shutdown: %v", err)
	}
	shCancel()

	select {
	case err := <-err1Ch:
		if err != nil {
			t.Errorf("first Serve: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("first Serve did not return within 15s")
	}
}

// ---------------------------------------------------------------------------
// 8) DialUntilReady tight timing
// ---------------------------------------------------------------------------

// TestDialUntilReadyTimingTight asserts that once the daemon is up,
// a fresh DialUntilReady against the same socket succeeds without error.
//
// We don't hold the call to a tight microsecond budget: CI runners
// (GitHub Actions, busy macOS laptops) routinely take 50-150ms for the
// first dial+/health round-trip even against an already-running daemon
// because of cold scheduler latency, the goroutine that runs httptest's
// Server.Serve, and the unix-socket accept queue. Asserting "elapsed
// < 50ms" makes the test flake without surfacing any real regression.
//
// Instead we use a generous envelope (250ms) plus the precise check
// that the dial succeeded — that's what the user-visible contract is.
func TestDialUntilReadyTimingTight(t *testing.T) {
	// Start a daemon with the default helper (it already returns post-ready).
	_, stop := startTestDaemon(t, nil)
	defer stop()

	// Now a fresh DialUntilReady against the same socket should be near-
	// instantaneous because /health responds immediately.
	start := time.Now()
	c, err := DialUntilReady(250 * time.Millisecond)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("DialUntilReady(250ms): %v (elapsed=%s)", err, elapsed)
	}
	defer c.Close()

	// Sanity-check we are not paying close-to-budget; a working daemon
	// should respond well inside this generous envelope.
	if elapsed >= 250*time.Millisecond {
		t.Errorf("DialUntilReady took %s — should succeed well within 250ms envelope", elapsed)
	}
}
