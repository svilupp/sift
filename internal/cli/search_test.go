package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"sift/internal/daemon"
	"sift/internal/db"
)

func TestIsMeaningfulLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"", false},                         // empty
		{"   ", false},                      // whitespace only
		{"---", false},                      // markdown separator
		{"===", false},                      // markdown separator
		{"|---|---|---|", false},            // table separator
		{"***", false},                      // horizontal rule
		{"| | | |", false},                  // empty table row (only pipes and spaces)
		{"abc", false},                      // too short (3 chars)
		{"abcd", true},                      // exactly 4
		{"Hello world", true},               // normal text
		{"## GCP Projects", true},           // markdown heading
		{"| Store | Order ID | URL |", true}, // table header with content
		{"`ePxLjCpjSHhzCPYjapUs`", true},    // code with alphanumeric
		{"- item", true},                    // list item
		{"1. First", true},                  // numbered list
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := isMeaningfulLine(tt.line)
			if got != tt.want {
				t.Errorf("isMeaningfulLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}

// orchestratorTrace captures which path the orchestrator took.
type orchestratorTrace struct {
	tryDialCalls        int
	spawnCalls          int
	dialUntilReadyCalls int
	daemonSearchCalls   int
	inProcessCalls      int
}

// installOrchestratorStubs swaps the package-level function variables
// for fakes that record which path the orchestrator took. The returned
// trace reflects calls made between install and test end. Restoration
// happens via t.Cleanup.
func installOrchestratorStubs(t *testing.T,
	tryDialFn func(time.Duration) (*daemon.Client, error),
	spawnFn func() error,
	dialReadyFn func(time.Duration) (*daemon.Client, error),
	daemonSearchFn func(*cobra.Command, *searchFlags, *daemon.Client) error,
	inProcessFn func(*cobra.Command, *searchFlags) error,
) *orchestratorTrace {
	t.Helper()
	trace := &orchestratorTrace{}

	origTry := tryDial
	origSpawn := spawnDetached
	origDial := dialUntilReady
	origSearchFn := searchViaDaemonFn
	origInProc := runSearchInProcessFn

	tryDial = func(d time.Duration) (*daemon.Client, error) {
		trace.tryDialCalls++
		return tryDialFn(d)
	}
	spawnDetached = func() error {
		trace.spawnCalls++
		return spawnFn()
	}
	dialUntilReady = func(d time.Duration) (*daemon.Client, error) {
		trace.dialUntilReadyCalls++
		return dialReadyFn(d)
	}
	searchViaDaemonFn = func(cmd *cobra.Command, f *searchFlags, c *daemon.Client) error {
		trace.daemonSearchCalls++
		return daemonSearchFn(cmd, f, c)
	}
	runSearchInProcessFn = func(cmd *cobra.Command, f *searchFlags) error {
		trace.inProcessCalls++
		return inProcessFn(cmd, f)
	}

	t.Cleanup(func() {
		tryDial = origTry
		spawnDetached = origSpawn
		dialUntilReady = origDial
		searchViaDaemonFn = origSearchFn
		runSearchInProcessFn = origInProc
	})

	return trace
}

// fakeClient builds a *daemon.Client that the test can hand to the
// orchestrator. The client never actually dials — its Close() is a
// no-op (CloseIdleConnections on an unused transport).
func fakeClient() *daemon.Client {
	return daemon.NewClient("/tmp/sift-test-no-such-socket")
}

// dummyCmd builds a minimal *cobra.Command suitable for orchestrator
// invocation in tests. The Flags() set has "top-k" so the orchestrator
// can test Changed("top-k").
func dummyCmd() *cobra.Command {
	c := &cobra.Command{}
	c.Flags().Int("top-k", 0, "")
	return c
}

func TestSearchHonorsSIFTNoDaemon(t *testing.T) {
	t.Setenv("SIFT_NO_DAEMON", "1")

	trace := installOrchestratorStubs(t,
		func(time.Duration) (*daemon.Client, error) { return nil, errors.New("should-not-call") },
		func() error { return errors.New("should-not-call") },
		func(time.Duration) (*daemon.Client, error) { return nil, errors.New("should-not-call") },
		func(*cobra.Command, *searchFlags, *daemon.Client) error { return errors.New("should-not-call") },
		func(*cobra.Command, *searchFlags) error { return nil },
	)

	cmd := dummyCmd()
	if err := runSearchOrchestrated(cmd, &searchFlags{query: "x"}); err != nil {
		t.Fatalf("orchestrate: %v", err)
	}

	if trace.tryDialCalls != 0 {
		t.Errorf("tryDial was called %d times; expected 0 with SIFT_NO_DAEMON set", trace.tryDialCalls)
	}
	if trace.spawnCalls != 0 {
		t.Errorf("spawnDetached was called %d times; expected 0", trace.spawnCalls)
	}
	if trace.daemonSearchCalls != 0 {
		t.Errorf("searchViaDaemon was called %d times; expected 0", trace.daemonSearchCalls)
	}
	if trace.inProcessCalls != 1 {
		t.Errorf("runSearchInProcess was called %d times; expected 1", trace.inProcessCalls)
	}
}

func TestSearchFallsBackOnDaemonError(t *testing.T) {
	t.Setenv("SIFT_NO_DAEMON", "")

	// tryDial succeeds, but searchViaDaemon returns an error. The
	// orchestrator should fall back to in-process WITHOUT trying to
	// auto-spawn (we already have a daemon connection — the issue is
	// in handling the response, not in connecting).
	trace := installOrchestratorStubs(t,
		func(time.Duration) (*daemon.Client, error) { return fakeClient(), nil },
		func() error { return errors.New("should-not-call") },
		func(time.Duration) (*daemon.Client, error) { return nil, errors.New("should-not-call") },
		func(*cobra.Command, *searchFlags, *daemon.Client) error { return errors.New("daemon search exploded") },
		func(*cobra.Command, *searchFlags) error { return nil },
	)

	cmd := dummyCmd()
	if err := runSearchOrchestrated(cmd, &searchFlags{query: "x"}); err != nil {
		t.Fatalf("orchestrate: %v", err)
	}

	if trace.tryDialCalls != 1 {
		t.Errorf("tryDialCalls = %d, want 1", trace.tryDialCalls)
	}
	if trace.daemonSearchCalls != 1 {
		t.Errorf("daemonSearchCalls = %d, want 1", trace.daemonSearchCalls)
	}
	if trace.spawnCalls != 0 {
		t.Errorf("spawnCalls = %d, want 0 (no auto-spawn after warm-dial success)", trace.spawnCalls)
	}
	if trace.inProcessCalls != 1 {
		t.Errorf("inProcessCalls = %d, want 1", trace.inProcessCalls)
	}
}

func TestSearchAutoSpawn(t *testing.T) {
	t.Setenv("SIFT_NO_DAEMON", "")

	// tryDial fails (ECONNREFUSED equivalent), spawn succeeds,
	// dialUntilReady succeeds, daemon search succeeds. Expected: no
	// in-process fallback.
	trace := installOrchestratorStubs(t,
		func(time.Duration) (*daemon.Client, error) { return nil, errors.New("connection refused") },
		func() error { return nil },
		func(time.Duration) (*daemon.Client, error) { return fakeClient(), nil },
		func(*cobra.Command, *searchFlags, *daemon.Client) error { return nil },
		func(*cobra.Command, *searchFlags) error {
			t.Errorf("in-process should not be called on the auto-spawn happy path")
			return nil
		},
	)

	cmd := dummyCmd()
	if err := runSearchOrchestrated(cmd, &searchFlags{query: "x"}); err != nil {
		t.Fatalf("orchestrate: %v", err)
	}

	if trace.tryDialCalls != 1 {
		t.Errorf("tryDialCalls = %d, want 1", trace.tryDialCalls)
	}
	if trace.spawnCalls != 1 {
		t.Errorf("spawnCalls = %d, want 1", trace.spawnCalls)
	}
	if trace.dialUntilReadyCalls != 1 {
		t.Errorf("dialUntilReadyCalls = %d, want 1", trace.dialUntilReadyCalls)
	}
	if trace.daemonSearchCalls != 1 {
		t.Errorf("daemonSearchCalls = %d, want 1", trace.daemonSearchCalls)
	}
	if trace.inProcessCalls != 0 {
		t.Errorf("inProcessCalls = %d, want 0 (auto-spawn happy path)", trace.inProcessCalls)
	}
}

func TestSearchAllPathsFail(t *testing.T) {
	t.Setenv("SIFT_NO_DAEMON", "")

	// tryDial fails, spawn fails -> in-process is the only viable
	// path. The user-facing error returned is the in-process one.
	wantErr := errors.New("in-process explosion")

	trace := installOrchestratorStubs(t,
		func(time.Duration) (*daemon.Client, error) { return nil, errors.New("connection refused") },
		func() error { return errors.New("spawn failed") },
		func(time.Duration) (*daemon.Client, error) { return nil, errors.New("should-not-call") },
		func(*cobra.Command, *searchFlags, *daemon.Client) error { return errors.New("should-not-call") },
		func(*cobra.Command, *searchFlags) error { return wantErr },
	)

	cmd := dummyCmd()
	gotErr := runSearchOrchestrated(cmd, &searchFlags{query: "x"})
	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("want in-process error %v, got %v", wantErr, gotErr)
	}

	if trace.tryDialCalls != 1 {
		t.Errorf("tryDialCalls = %d, want 1", trace.tryDialCalls)
	}
	if trace.spawnCalls != 1 {
		t.Errorf("spawnCalls = %d, want 1", trace.spawnCalls)
	}
	if trace.dialUntilReadyCalls != 0 {
		t.Errorf("dialUntilReadyCalls = %d, want 0 (spawn failed first)", trace.dialUntilReadyCalls)
	}
	if trace.daemonSearchCalls != 0 {
		t.Errorf("daemonSearchCalls = %d, want 0", trace.daemonSearchCalls)
	}
	if trace.inProcessCalls != 1 {
		t.Errorf("inProcessCalls = %d, want 1", trace.inProcessCalls)
	}
}

// TestSearchSpawnReadyFailsFallsBack covers the variant where spawn
// succeeds but the spawned daemon never becomes ready in time.
// Orchestrator should fall back to in-process.
func TestSearchSpawnReadyFailsFallsBack(t *testing.T) {
	t.Setenv("SIFT_NO_DAEMON", "")

	trace := installOrchestratorStubs(t,
		func(time.Duration) (*daemon.Client, error) { return nil, errors.New("connection refused") },
		func() error { return nil },
		func(time.Duration) (*daemon.Client, error) { return nil, errors.New("not ready in time") },
		func(*cobra.Command, *searchFlags, *daemon.Client) error { return errors.New("should-not-call") },
		func(*cobra.Command, *searchFlags) error { return nil },
	)

	cmd := dummyCmd()
	if err := runSearchOrchestrated(cmd, &searchFlags{query: "x"}); err != nil {
		t.Fatalf("orchestrate: %v", err)
	}

	if trace.dialUntilReadyCalls != 1 {
		t.Errorf("dialUntilReadyCalls = %d, want 1", trace.dialUntilReadyCalls)
	}
	if trace.daemonSearchCalls != 0 {
		t.Errorf("daemonSearchCalls = %d, want 0", trace.daemonSearchCalls)
	}
	if trace.inProcessCalls != 1 {
		t.Errorf("inProcessCalls = %d, want 1", trace.inProcessCalls)
	}
}

// dummyCmdWithReverse builds a *cobra.Command with both "top-k" and
// "reverse" flag definitions so applyPrettyDefaults can call
// Changed("reverse") meaningfully.
func dummyCmdWithReverse() *cobra.Command {
	c := &cobra.Command{}
	c.Flags().Int("top-k", 0, "")
	c.Flags().Bool("reverse", false, "")
	return c
}

// TestApplyPrettyDefaults verifies the --pretty derivations (reverse=on,
// halve top-k) run pre-branch so both daemon and in-process paths see
// the same values. Regression guard for the bug where derivations only
// ran in runSearchInProcess and were lost on the daemon path.
func TestApplyPrettyDefaults(t *testing.T) {
	t.Run("pretty unset is a no-op", func(t *testing.T) {
		cmd := dummyCmdWithReverse()
		f := &searchFlags{topK: 0, reverse: false, pretty: false}
		applyPrettyDefaults(cmd, f)
		if f.reverse {
			t.Errorf("reverse = true, want false (pretty unset)")
		}
		if f.topK != 0 {
			t.Errorf("topK = %d, want 0 (pretty unset)", f.topK)
		}
	})

	t.Run("pretty implies reverse", func(t *testing.T) {
		cmd := dummyCmdWithReverse()
		f := &searchFlags{topK: 10, reverse: false, pretty: true}
		applyPrettyDefaults(cmd, f)
		if !f.reverse {
			t.Errorf("reverse = false, want true (pretty set)")
		}
	})

	t.Run("pretty halves explicit topK only when top-k not changed", func(t *testing.T) {
		cmd := dummyCmdWithReverse()
		f := &searchFlags{topK: 20, reverse: false, pretty: true}
		applyPrettyDefaults(cmd, f)
		if f.topK != 10 {
			t.Errorf("topK = %d, want 10 (halved)", f.topK)
		}
	})

	t.Run("explicit --top-k beats pretty halving", func(t *testing.T) {
		cmd := dummyCmdWithReverse()
		_ = cmd.Flags().Set("top-k", "7")
		f := &searchFlags{topK: 7, reverse: false, pretty: true}
		applyPrettyDefaults(cmd, f)
		if f.topK != 7 {
			t.Errorf("topK = %d, want 7 (--top-k explicit)", f.topK)
		}
	})

	t.Run("explicit --reverse=false beats pretty", func(t *testing.T) {
		cmd := dummyCmdWithReverse()
		_ = cmd.Flags().Set("reverse", "false")
		f := &searchFlags{topK: 10, reverse: false, pretty: true}
		applyPrettyDefaults(cmd, f)
		if f.reverse {
			t.Errorf("reverse = true, want false (explicit --reverse=false)")
		}
	})
}

// TestSearchDaemonPathSeesPrettyDefaults is the regression test for the
// confirmed bug: --pretty against the daemon must hand the daemon the
// derived reverse=true (and halved top-k). Pre-fix, the derivations
// only ran in runSearchInProcess, so daemon-served pretty searches
// showed best results at the TOP.
func TestSearchDaemonPathSeesPrettyDefaults(t *testing.T) {
	t.Setenv("SIFT_NO_DAEMON", "")

	var gotFlags *searchFlags
	installOrchestratorStubs(t,
		func(time.Duration) (*daemon.Client, error) { return fakeClient(), nil },
		func() error { return errors.New("should-not-call") },
		func(time.Duration) (*daemon.Client, error) { return nil, errors.New("should-not-call") },
		func(_ *cobra.Command, f *searchFlags, _ *daemon.Client) error {
			gotFlags = f
			return nil
		},
		func(*cobra.Command, *searchFlags) error {
			t.Errorf("in-process should not be called on the warm-dial path")
			return nil
		},
	)

	cmd := dummyCmdWithReverse()
	f := &searchFlags{query: "x", pretty: true, topK: 20}
	if err := runSearchOrchestrated(cmd, f); err != nil {
		t.Fatalf("orchestrate: %v", err)
	}

	if gotFlags == nil {
		t.Fatal("searchViaDaemon stub never saw flags")
	}
	if !gotFlags.reverse {
		t.Errorf("daemon saw reverse=false, want true (pretty implies reverse)")
	}
	if gotFlags.topK != 10 {
		t.Errorf("daemon saw topK=%d, want 10 (pretty halves)", gotFlags.topK)
	}
}

// newTestUnixSocketServer starts an httptest server on a Unix socket
// short enough to stay under macOS's 104-byte sun_path cap. Mirrors the
// helper in internal/daemon/client_test.go (unexported, so we duplicate).
func newTestUnixSocketServer(t *testing.T, h http.Handler) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sift-cli")
	if err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	p := filepath.Join(dir, "s.sock")
	if len(p) >= 104 {
		t.Fatalf("socket path too long (%d): %s", len(p), p)
	}
	ln, err := net.Listen("unix", p)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	srv := httptest.NewUnstartedServer(h)
	_ = srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(func() {
		srv.Close()
		_ = os.Remove(p)
	})
	return p
}

// TestSearchViaDaemonPlumbsFlagsAndWritesSession asserts that
// searchViaDaemon (1) forwards PathGlob and SectionAggregate on the
// SearchRequest to the daemon and (2) persists a search_sessions row
// using resp.SearchID so `sift feedback <id>` works on daemon-served
// results.
func TestSearchViaDaemonPlumbsFlagsAndWritesSession(t *testing.T) {
	// Redirect SIFT_DIR so config.DBPath / openDB land in a temp tree.
	siftDir := t.TempDir()
	t.Setenv("SIFT_DIR", siftDir)

	var gotReq daemon.SearchRequest
	socketPath := newTestUnixSocketServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" {
			t.Errorf("path: got %q want /search", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotReq); err != nil {
			t.Fatalf("decode req: %v", err)
		}
		resp := daemon.SearchResponse{
			SearchID: "sid-test-1",
			Query:    gotReq.Query,
			Results:  []daemon.ResultEntry{},
			Meta:     daemon.SearchMeta{ResultCount: 0, TotalTimeMs: 5},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))

	client := daemon.NewClient(socketPath)
	defer client.Close()

	cmd := &cobra.Command{}
	cmd.Flags().Int("top-k", 0, "")
	cmd.Flags().Float64("threshold", 0, "")
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	f := &searchFlags{
		query:      "hello",
		collection: "default",
		pathGlob:   "docs/**",
		sections:   true,
		topK:       5,
		start:      time.Now(),
	}

	if err := searchViaDaemon(cmd, f, client); err != nil {
		t.Fatalf("searchViaDaemon: %v", err)
	}

	// (1) Daemon stub saw PathGlob and SectionAggregate.
	if gotReq.PathGlob != "docs/**" {
		t.Errorf("PathGlob: got %q want %q", gotReq.PathGlob, "docs/**")
	}
	if !gotReq.SectionAggregate {
		t.Errorf("SectionAggregate: got false want true")
	}

	// (2) Session row exists in the DB.
	database, err := openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer database.Close()
	if !sessionExists(t, database, "sid-test-1") {
		t.Errorf("search_sessions has no row for sid-test-1")
	}
}

func sessionExists(t *testing.T, database *db.DB, searchID string) bool {
	t.Helper()
	var got string
	row := database.QueryRow("SELECT search_id FROM search_sessions WHERE search_id = ?", searchID)
	if err := row.Scan(&got); err != nil {
		return false
	}
	return got == searchID
}
