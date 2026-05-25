package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sift/internal/config"
	"sift/internal/search"
)

// TestMain lives in client_test.go and runs goleak.Find at exit.

// stubEngine is a SearchEngine test double. Searches either return Result
// or fail with Err depending on which is set.
type stubEngine struct {
	Result   *search.SearchResult
	Err      error
	Calls    int
	LastOpts search.SearchOptions
}

func (s *stubEngine) Search(_ context.Context, _ string, opts search.SearchOptions) (*search.SearchResult, error) {
	s.Calls++
	s.LastOpts = opts
	if s.Err != nil {
		return nil, s.Err
	}
	if s.Result != nil {
		return s.Result, nil
	}
	return &search.SearchResult{}, nil
}

// ---------------------------------------------------------------------------
// idleTimer
// ---------------------------------------------------------------------------

func TestIdleTimerFires(t *testing.T) {
	t.Parallel()
	var fired atomic.Bool
	timeout := 80 * time.Millisecond
	tm := newIdleTimer(timeout, func() {
		fired.Store(true)
	})
	defer tm.Stop()

	deadline := time.Now().Add(timeout * 5)
	for time.Now().Before(deadline) {
		if fired.Load() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("idle timer did not fire within %v", timeout*5)
}

func TestIdleTimerResets(t *testing.T) {
	t.Parallel()
	var fired atomic.Bool
	timeout := 100 * time.Millisecond
	tm := newIdleTimer(timeout, func() {
		fired.Store(true)
	})
	defer tm.Stop()

	// Reset faster than the timeout for a duration longer than the
	// timeout — fired must remain false throughout.
	end := time.Now().Add(timeout * 3)
	for time.Now().Before(end) {
		tm.Reset()
		if fired.Load() {
			t.Fatalf("idle timer fired despite resets")
		}
		time.Sleep(timeout / 4)
	}
}

func TestIdleTimerZero(t *testing.T) {
	t.Parallel()
	var fired atomic.Bool
	tm := newIdleTimer(0, func() { fired.Store(true) })
	defer tm.Stop()

	// Reset is a no-op; firing should never happen.
	tm.Reset()
	time.Sleep(150 * time.Millisecond)
	if fired.Load() {
		t.Fatalf("zero-timeout timer fired")
	}
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

func newTestHandlers(t *testing.T, eng SearchEngine, shutdownFn func()) *Handlers {
	t.Helper()
	cfg := config.Default()
	deps := Dependencies{
		Engine:       eng,
		Cfg:          cfg,
		Version:      "test",
		StartedAt:    time.Now().Add(-30 * time.Second),
		RequestCount: new(atomic.Int64),
		ShutdownFn:   shutdownFn,
	}
	return newHandlers(deps)
}

func TestHealthHandler(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, &stubEngine{}, nil)
	h.deps.RequestCount.Add(7)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/health", nil)
	h.Health(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 got %d", w.Code)
	}
	var resp HealthResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Ok {
		t.Errorf("ok: want true")
	}
	if resp.Version != "test" {
		t.Errorf("version: got %q want %q", resp.Version, "test")
	}
	if resp.UptimeS < 1 {
		t.Errorf("uptime_s: got %d want >=1", resp.UptimeS)
	}
	if resp.PID != os.Getpid() {
		t.Errorf("pid: got %d want %d", resp.PID, os.Getpid())
	}
	if resp.RequestCount != 7 {
		t.Errorf("request_count: got %d want 7", resp.RequestCount)
	}
	if resp.Goroutines < 1 {
		t.Errorf("goroutines: got %d", resp.Goroutines)
	}
	if resp.StartedAt == "" {
		t.Errorf("started_at: empty")
	}
}

func TestSearchHandlerError(t *testing.T) {
	t.Parallel()
	eng := &stubEngine{Err: errors.New("boom")}
	h := newTestHandlers(t, eng, nil)

	body := strings.NewReader(`{"query":"hello"}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/search", body)
	h.Search(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500 got %d", w.Code)
	}
	var resp ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Code == "" || resp.Message == "" {
		t.Fatalf("missing code/message: %+v", resp)
	}
	if !strings.Contains(resp.Details, "boom") {
		t.Errorf("details should contain error: %q", resp.Details)
	}
}

func TestSearchHandlerBadJSON(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, &stubEngine{}, nil)
	body := strings.NewReader(`{not-json`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/search", body)
	h.Search(w, r)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 got %d", w.Code)
	}
}

func TestSearchHandlerEmptyQuery(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, &stubEngine{}, nil)
	body := strings.NewReader(`{"query":""}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/search", body)
	h.Search(w, r)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status: want 400 got %d", w.Code)
	}
}

func TestSearchHandlerPlumbsPathGlobAndSectionAggregate(t *testing.T) {
	t.Parallel()
	eng := &stubEngine{}
	h := newTestHandlers(t, eng, nil)

	body := strings.NewReader(`{"query":"hello","path_glob":"docs/**","section_aggregate":true}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/search", body)
	h.Search(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 got %d body=%s", w.Code, w.Body.String())
	}
	if eng.Calls != 1 {
		t.Fatalf("engine calls: got %d want 1", eng.Calls)
	}
	if eng.LastOpts.PathGlob != "docs/**" {
		t.Errorf("PathGlob: got %q want %q", eng.LastOpts.PathGlob, "docs/**")
	}
	if !eng.LastOpts.SectionAggregate {
		t.Errorf("SectionAggregate: got false want true")
	}
}

func TestSearchHandlerDefaultsForOmittedFields(t *testing.T) {
	t.Parallel()
	eng := &stubEngine{}
	h := newTestHandlers(t, eng, nil)

	body := strings.NewReader(`{"query":"hello"}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/search", body)
	h.Search(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 got %d body=%s", w.Code, w.Body.String())
	}
	if eng.LastOpts.PathGlob != "" {
		t.Errorf("PathGlob: got %q want empty", eng.LastOpts.PathGlob)
	}
	if eng.LastOpts.SectionAggregate {
		t.Errorf("SectionAggregate: got true want false")
	}
}

func TestShutdownHandler(t *testing.T) {
	t.Parallel()
	called := make(chan struct{})
	var once sync.Once
	h := newTestHandlers(t, &stubEngine{}, func() {
		once.Do(func() { close(called) })
	})

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/shutdown", nil)
	h.Shutdown(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status: want 200 got %d", w.Code)
	}
	var resp map[string]bool
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp["ok"] {
		t.Fatalf("ok: want true")
	}

	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatalf("shutdownFn was not called within 1s")
	}
}

// ---------------------------------------------------------------------------
// Server lifecycle (Unix socket)
// ---------------------------------------------------------------------------

// withTempSiftDir points SIFT_DIR + SIFT_DAEMON_SOCKET at a fresh tempdir
// so Serve doesn't touch the real ~/.sift/ during tests. Returns the
// resolved socket path.
func withTempSiftDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("SIFT_DIR", dir)
	sockPath := filepath.Join(dir, "sift.sock")
	t.Setenv("SIFT_DAEMON_SOCKET", sockPath)
	return sockPath
}

// unixHTTPClient builds an *http.Client that dials the given Unix socket.
func unixHTTPClient(socket string) *http.Client {
	return &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socket)
			},
		},
	}
}

// waitForSocket polls for the socket file to appear and accept a dial.
func waitForSocket(t *testing.T, socket string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(socket); err == nil {
			c, err := net.DialTimeout("unix", socket, 50*time.Millisecond)
			if err == nil {
				_ = c.Close()
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("socket %s not ready within %v", socket, timeout)
}

func TestServeUnixSocketLifecycle(t *testing.T) {
	sockPath := withTempSiftDir(t)
	cfg := config.Default()
	cfg.API.VoyageAPIKey = "" // skip Voyage; daemon should still come up
	// Disable idle so we drive shutdown explicitly.
	cfg.Daemon.IdleTimeout = config.Duration(0)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- Serve(ctx, cfg)
	}()

	waitForSocket(t, sockPath, 3*time.Second)

	client := unixHTTPClient(sockPath)

	// /health
	resp, err := client.Get("http://unix/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/health status: %d", resp.StatusCode)
	}
	var hr HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&hr); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	resp.Body.Close()
	if !hr.Ok {
		t.Errorf("/health ok: want true")
	}

	// /shutdown
	resp, err = client.Post("http://unix/shutdown", "application/json", nil)
	if err != nil {
		t.Fatalf("POST /shutdown: %v", err)
	}
	resp.Body.Close()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("Serve did not return within 15s of /shutdown")
	}

	// Socket and PID file must be cleaned up.
	if _, err := os.Stat(sockPath); !os.IsNotExist(err) {
		t.Errorf("socket file still exists: err=%v", err)
	}
	pidPath, _ := config.PIDPath()
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Errorf("pid file still exists: err=%v", err)
	}
}

func TestSingleInstanceLock(t *testing.T) {
	withTempSiftDir(t)
	cfg := config.Default()
	cfg.Daemon.IdleTimeout = config.Duration(0)

	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	err1Ch := make(chan error, 1)
	go func() {
		err1Ch <- Serve(ctx1, cfg)
	}()

	// Wait for first instance to bind.
	sockPath, _ := config.SocketPath()
	waitForSocket(t, sockPath, 3*time.Second)

	// Second instance must fail with "already running".
	err2 := Serve(context.Background(), cfg)
	if err2 == nil {
		t.Fatalf("second Serve: want error, got nil")
	}
	if !strings.Contains(err2.Error(), "already running") {
		t.Errorf("second Serve error: want contains 'already running', got %q", err2.Error())
	}

	// Stop first.
	client := unixHTTPClient(sockPath)
	resp, err := client.Post("http://unix/shutdown", "application/json", nil)
	if err != nil {
		t.Fatalf("post shutdown: %v", err)
	}
	resp.Body.Close()

	select {
	case err := <-err1Ch:
		if err != nil {
			t.Fatalf("first Serve returned: %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("first Serve did not return in 15s")
	}
}

// TestRefreshHandlerRequiresDeps verifies the /refresh handler short-
// circuits with HTTP 500 engine_unavailable when DB / Bleve / Cfg
// aren't wired (e.g. in unit-test handlers built without real deps).
// End-to-end refresh behaviour is covered by integration_test.go.
func TestRefreshHandlerRequiresDeps(t *testing.T) {
	t.Parallel()
	h := newTestHandlers(t, &stubEngine{}, nil)
	body := strings.NewReader(`{"collection":"foo"}`)
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/refresh", body)
	h.Refresh(w, r)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status: want 500 got %d", w.Code)
	}
	var resp ErrorResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v\nbody=%s", err, w.Body.String())
	}
	if resp.Code != "engine_unavailable" {
		t.Errorf("code: got %q want %q", resp.Code, "engine_unavailable")
	}
}

// progressWriterTest sanity-checks the line splitter without involving
// the HTTP layer.
func TestProgressWriterSplitsLines(t *testing.T) {
	t.Parallel()
	var got []string
	pw := &progressWriter{
		phase: "scan",
		emit: func(ev ProgressEvent) {
			got = append(got, fmt.Sprintf("%s:%s", ev.Phase, ev.Message))
		},
	}
	_, _ = pw.Write([]byte("hello\nworld\npa"))
	_, _ = pw.Write([]byte("rtial\n"))
	want := []string{"scan:hello", "scan:world", "scan:partial"}
	if len(got) != len(want) {
		t.Fatalf("lines: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("line %d: got %q want %q", i, got[i], want[i])
		}
	}
}
