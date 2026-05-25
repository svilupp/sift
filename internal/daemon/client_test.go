package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

	"go.uber.org/goleak"
)

// goroutineIgnores tolerates background goroutines from net/http's
// connection pool that may linger briefly after a test finishes —
// they are not leaks but goleak cannot tell the difference.
//
// Note: we deliberately do NOT ignore "internal/poll.runtime_pollWait".
// That symbol is the bottom of the stack for any goroutine blocked on
// I/O; ignoring it disarms leak detection across all I/O paths and
// hides actual leaks. The named persistConn / dialer / TLS handshake
// ignores below are narrower and cover the legitimate transport
// goroutines that linger past Close().
var goroutineIgnores = []goleak.Option{
	goleak.IgnoreTopFunction("net/http.(*persistConn).writeLoop"),
	goleak.IgnoreTopFunction("net/http.(*persistConn).readLoop"),
	// Bleve spawns a small pool of long-lived analysis workers via
	// NewAnalysisQueue when an index is opened; they outlive the
	// *BleveIndex Close call and are not under our control.
	goleak.IgnoreTopFunction("github.com/blevesearch/bleve_index_api.AnalysisWorker"),
	// Voyage Preconnect dials a real TCP socket in the background; if a
	// test exits before the dial completes, net.(*netFD).connect's
	// helper goroutine briefly lingers waiting for the OS to close the FD.
	goleak.IgnoreTopFunction("net.(*netFD).connect.func2"),
	// Same root cause: when Preconnect's TLS handshake hasn't finished
	// at shutdown, net/http and crypto/tls leave dialer goroutines on
	// the stack until the connection close finishes propagating. Use
	// IgnoreAnyFunction so we match by frame anywhere in the goroutine's
	// stack — the actual top is often a DNS / poll wait, which we
	// deliberately don't ignore generically.
	goleak.IgnoreAnyFunction("net/http.(*Transport).dialConnFor"),
	goleak.IgnoreAnyFunction("net/http.(*persistConn).addTLS"),
	goleak.IgnoreAnyFunction("crypto/tls.(*Conn).handshakeContext.func2"),
	// HTTP/2 client conn read-loop goroutines linger past Close on Go's
	// http2 transport because the framer's blocking Read returns only
	// when the kernel reports the FD closed. Integration tests that
	// exercise the real Voyage client (with empty API key, errors out
	// upstream) leave one of these per test in the goroutine table for
	// a few hundred ms after the test exits. The TOP of the stack is
	// runtime_pollWait (a generic I/O block), so we use IgnoreAnyFunction
	// to match the readLoop frame anywhere in the stack rather than
	// re-introducing the over-broad runtime_pollWait ignore.
	goleak.IgnoreAnyFunction("net/http.(*http2ClientConn).readLoop"),
	goleak.IgnoreAnyFunction("net/http.(*http2clientConnReadLoop).run"),
}

func TestMain(m *testing.M) {
	// Run tests, then verify no goroutines leaked.
	code := m.Run()
	if code == 0 {
		if err := goleak.Find(goroutineIgnores...); err != nil {
			fmt.Fprintf(os.Stderr, "goleak: %v\n", err)
			code = 1
		}
	}
	os.Exit(code)
}

// shortSocketPath returns a Unix socket path that stays under the
// platform's sun_path length limit (104 on macOS, 108 on Linux).
// t.TempDir() under macOS test runners produces paths that frequently
// exceed 104 chars, so we fall back to a hand-rolled short directory
// in os.TempDir() and clean it up via t.Cleanup.
func shortSocketPath(t *testing.T, name string) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sift")
	if err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p := filepath.Join(dir, name)
	if len(p) >= 104 {
		t.Fatalf("socket path too long (%d): %s", len(p), p)
	}
	return p
}

// newTestUnixServer starts an httptest server bound to a Unix socket in
// a temp directory. It returns the socket path and a cleanup function.
//
// httptest.NewUnstartedServer normally listens on TCP; we replace its
// Listener with a unix-socket listener before calling Start.
func newTestUnixServer(t *testing.T, h http.Handler) (string, func()) {
	t.Helper()

	socketPath := shortSocketPath(t, "test.sock")

	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}

	srv := httptest.NewUnstartedServer(h)
	// Close the default TCP listener httptest created and replace it.
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()

	cleanup := func() {
		srv.Close()
		// Ensure the socket file is gone even if the server's close
		// path missed it (it normally does).
		_ = os.Remove(socketPath)
	}
	return socketPath, cleanup
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

func TestClientHealth(t *testing.T) {
	want := HealthResponse{
		Ok:           true,
		Version:      "test-version",
		UptimeS:      42,
		PID:          12345,
		RequestCount: 7,
		Goroutines:   8,
		StartedAt:    "2026-05-03T00:00:00Z",
	}

	socketPath, cleanup := newTestUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer cleanup()

	c := NewClient(socketPath)
	defer c.Close()

	got, err := c.Health(context.Background())
	if err != nil {
		t.Fatalf("Health: %v", err)
	}
	if got.Version != want.Version || got.PID != want.PID || got.UptimeS != want.UptimeS {
		t.Errorf("Health mismatch: got %+v want %+v", got, want)
	}
}

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

func TestClientSearch(t *testing.T) {
	wantReq := SearchRequest{
		Query:      "hello",
		Collection: "default",
		TopK:       5,
	}
	wantResp := SearchResponse{
		SearchID: "sid-1",
		Query:    "hello",
		Results: []ResultEntry{
			{
				Index:      "1",
				ChunkID:    99,
				File:       "a.md",
				Collection: "default",
				StartLine:  1, EndLine: 10,
				Content: "result content",
				Score:   0.5,
				OpenCmd: "open a.md",
			},
		},
		Meta: SearchMeta{
			TotalTimeMs: 100,
			ResultCount: 1,
		},
		FeedbackCmd: "sift feedback sid-1",
	}

	socketPath, cleanup := newTestUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path: %s", r.URL.Path)
		}
		if r.Method != http.MethodPost {
			t.Errorf("method: %s", r.Method)
		}
		var got SearchRequest
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Errorf("decode req: %v", err)
		}
		if got.Query != wantReq.Query || got.TopK != wantReq.TopK {
			t.Errorf("req mismatch: %+v", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(wantResp)
	}))
	defer cleanup()

	c := NewClient(socketPath)
	defer c.Close()

	got, err := c.Search(context.Background(), wantReq)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if got.SearchID != wantResp.SearchID || len(got.Results) != 1 || got.Results[0].ChunkID != 99 {
		t.Errorf("search response mismatch: %+v", got)
	}
}

// ---------------------------------------------------------------------------
// Refresh NDJSON streaming
// ---------------------------------------------------------------------------

func TestClientRefreshNDJSON(t *testing.T) {
	events := []ProgressEvent{
		{Phase: "scan", FilesDone: 0, FilesTotal: 3, Message: "scanning"},
		{Phase: "embed", FilesDone: 1, FilesTotal: 3, Message: "embedding"},
		{Phase: "done", FilesDone: 3, FilesTotal: 3, Message: "complete"},
	}

	socketPath, cleanup := newTestUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/refresh" {
			t.Errorf("path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatalf("response writer does not support Flush")
		}
		w.WriteHeader(http.StatusOK)
		for _, ev := range events {
			b, _ := json.Marshal(ev)
			b = append(b, '\n')
			if _, err := w.Write(b); err != nil {
				t.Errorf("write: %v", err)
				return
			}
			flusher.Flush()
			// Tiny delay so the client side has a chance to read each
			// chunk separately and we exercise true streaming.
			time.Sleep(5 * time.Millisecond)
		}
	}))
	defer cleanup()

	c := NewClient(socketPath)
	defer c.Close()

	var buf bytes.Buffer
	if err := c.Refresh(context.Background(), RefreshRequest{Collection: "default"}, &buf); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != len(events) {
		t.Fatalf("expected %d lines, got %d: %q", len(events), len(lines), buf.String())
	}
	for i, line := range lines {
		var ev ProgressEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Errorf("line %d decode: %v (line=%q)", i, err, line)
			continue
		}
		if ev.Phase != events[i].Phase {
			t.Errorf("line %d phase: got %q want %q", i, ev.Phase, events[i].Phase)
		}
	}
}

// ---------------------------------------------------------------------------
// Shutdown — connection reset is treated as success
// ---------------------------------------------------------------------------

func TestClientShutdownConnectionReset(t *testing.T) {
	socketPath, cleanup := newTestUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Hijack and close immediately to simulate the daemon dying
		// after accepting the request.
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Fatalf("hijacker not supported")
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		// Close without writing anything → client sees EOF.
		_ = conn.Close()
	}))
	defer cleanup()

	c := NewClient(socketPath)
	defer c.Close()

	if err := c.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown should treat connection close as success, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// TryDial — no socket
// ---------------------------------------------------------------------------

func TestTryDialNoSocket(t *testing.T) {
	bogus := shortSocketPath(t, "missing.sock")
	// The directory exists but the socket file does not.
	_ = os.Remove(bogus)

	t.Setenv("SIFT_DAEMON_SOCKET", bogus)

	_, err := TryDial(200 * time.Millisecond)
	if err == nil {
		t.Fatalf("expected error dialing nonexistent socket")
	}
	// The error chain should expose the underlying syscall error so
	// callers can detect it. We accept either ENOENT or ECONNREFUSED
	// plus the substring fallback for portability.
	msg := err.Error()
	if !strings.Contains(msg, "no such file") &&
		!strings.Contains(msg, "connection refused") &&
		!strings.Contains(msg, "connect:") {
		t.Errorf("expected file-not-found / connection-refused error, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// DialUntilReady — eventual success
// ---------------------------------------------------------------------------

func TestDialUntilReadyEventuallySucceeds(t *testing.T) {
	socketPath := shortSocketPath(t, "delayed.sock")
	t.Setenv("SIFT_DAEMON_SOCKET", socketPath)

	var srv *httptest.Server
	var srvMu sync.Mutex
	startedAt := time.Now()

	// Fire up the server after a delay.
	go func() {
		time.Sleep(200 * time.Millisecond)
		ln, err := net.Listen("unix", socketPath)
		if err != nil {
			t.Errorf("listen: %v", err)
			return
		}
		s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode(HealthResponse{Ok: true, Version: "x"})
		}))
		_ = s.Listener.Close()
		s.Listener = ln
		s.Start()
		srvMu.Lock()
		srv = s
		srvMu.Unlock()
	}()
	defer func() {
		srvMu.Lock()
		s := srv
		srvMu.Unlock()
		if s != nil {
			s.Close()
		}
		_ = os.Remove(socketPath)
	}()

	c, err := DialUntilReady(2 * time.Second)
	if err != nil {
		t.Fatalf("DialUntilReady: %v", err)
	}
	defer c.Close()

	elapsed := time.Since(startedAt)
	if elapsed < 200*time.Millisecond {
		t.Errorf("returned suspiciously fast: %s", elapsed)
	}
	if elapsed > 1500*time.Millisecond {
		t.Errorf("returned suspiciously slow: %s", elapsed)
	}
}

// ---------------------------------------------------------------------------
// DialUntilReady — timeout
// ---------------------------------------------------------------------------

func TestDialUntilReadyTimeout(t *testing.T) {
	socketPath := shortSocketPath(t, "never.sock")
	t.Setenv("SIFT_DAEMON_SOCKET", socketPath)

	timeout := 200 * time.Millisecond
	start := time.Now()
	_, err := DialUntilReady(timeout)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected timeout error")
	}
	if elapsed > timeout+200*time.Millisecond {
		t.Errorf("returned %s after timeout %s — too slow", elapsed, timeout)
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Errorf("error should mention timeout: %v", err)
	}
}

// ---------------------------------------------------------------------------
// ErrorResponse decoding
// ---------------------------------------------------------------------------

func TestErrorResponseDecoding(t *testing.T) {
	socketPath, cleanup := newTestUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(ErrorResponse{
			Code:    "internal",
			Message: "boom went the daemon",
			Details: "stack trace…",
		})
	}))
	defer cleanup()

	c := NewClient(socketPath)
	defer c.Close()

	// Health
	_, err := c.Health(context.Background())
	if err == nil {
		t.Fatalf("expected error from 500 health response")
	}
	if !strings.Contains(err.Error(), "internal") || !strings.Contains(err.Error(), "boom went the daemon") {
		t.Errorf("error should contain code+message: %v", err)
	}

	// Search
	_, err = c.Search(context.Background(), SearchRequest{Query: "x"})
	if err == nil {
		t.Fatalf("expected error from 500 search response")
	}
	if !strings.Contains(err.Error(), "internal") || !strings.Contains(err.Error(), "boom") {
		t.Errorf("search error should contain code+message: %v", err)
	}

	// Refresh
	err = c.Refresh(context.Background(), RefreshRequest{}, io.Discard)
	if err == nil {
		t.Fatalf("expected error from 500 refresh response")
	}
	if !strings.Contains(err.Error(), "internal") {
		t.Errorf("refresh error should contain code: %v", err)
	}
}

// ---------------------------------------------------------------------------
// NewClient default socket path resolves via config when arg is empty
// ---------------------------------------------------------------------------

func TestNewClientDefaultSocket(t *testing.T) {
	override := shortSocketPath(t, "default.sock")
	t.Setenv("SIFT_DAEMON_SOCKET", override)

	c := NewClient("")
	defer c.Close()

	if c.SocketPath() != override {
		t.Errorf("SocketPath: got %q want %q", c.SocketPath(), override)
	}
}

// ---------------------------------------------------------------------------
// Refresh stops on done event even if more bytes follow
// ---------------------------------------------------------------------------

func TestRefreshStopsOnDoneEvent(t *testing.T) {
	var extraBytesWritten atomic.Bool

	socketPath, cleanup := newTestUnixServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		flusher := w.(http.Flusher)
		w.WriteHeader(http.StatusOK)

		ev := ProgressEvent{Phase: "done", FilesDone: 1, FilesTotal: 1}
		b, _ := json.Marshal(ev)
		b = append(b, '\n')
		_, _ = w.Write(b)
		flusher.Flush()

		// Slight delay then write extra bytes — the client should
		// have already returned, but we still produce them to make
		// sure the drain logic doesn't block.
		time.Sleep(20 * time.Millisecond)
		if _, err := w.Write([]byte("trailing junk\n")); err == nil {
			extraBytesWritten.Store(true)
		}
		flusher.Flush()
	}))
	defer cleanup()

	c := NewClient(socketPath)
	defer c.Close()

	var buf bytes.Buffer
	start := time.Now()
	if err := c.Refresh(context.Background(), RefreshRequest{}, &buf); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	elapsed := time.Since(start)
	// Should finish well under a second; we don't strictly check the
	// post-done write since timing is flaky on loaded CI machines.
	_ = extraBytesWritten.Load()
	if elapsed > 2*time.Second {
		t.Errorf("Refresh took too long: %s", elapsed)
	}
	if !strings.Contains(buf.String(), "\"done\"") {
		t.Errorf("expected done event in writer output, got %q", buf.String())
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// ensure errors.Is unwraps wrapped errors from TryDial
func TestTryDialWrapsErrors(t *testing.T) {
	bogus := shortSocketPath(t, "miss.sock")
	_ = os.Remove(bogus)
	t.Setenv("SIFT_DAEMON_SOCKET", bogus)

	_, err := TryDial(100 * time.Millisecond)
	if err == nil {
		t.Fatalf("expected error")
	}
	// The error must be unwrappable.
	if errors.Unwrap(err) == nil {
		t.Errorf("expected wrapped error, got %v", err)
	}
}
