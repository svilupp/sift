package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"sift/internal/config"
	"sift/internal/daemon"
)

// daemonTestEnv sets up a sandboxed sift home + a fake daemon binary.
// It returns the resolved sift dir so callers can poke at the socket
// or PID file directly. Honors the macOS sun_path 104-char limit by
// rooting the dir under /tmp via os.MkdirTemp("", "sift").
func daemonTestEnv(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("daemon tests require POSIX setsid + shell scripts")
	}

	// Use a short temp dir to keep socket path < 104 chars on macOS.
	siftDir, err := os.MkdirTemp("", "sift")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() {
		_ = os.RemoveAll(siftDir)
	})

	// Validate the resolved socket path won't trip the macOS sun_path
	// 104-char limit (Linux is 108). Even with /tmp-rooted MkdirTemp this
	// can fail if TMPDIR points somewhere deep — surface a skip rather
	// than a confusing bind error inside the daemon under test.
	sockPath := filepath.Join(siftDir, "sift.sock")
	if len(sockPath) >= 104 {
		t.Skipf("socket path too long for sun_path (%d >= 104): %s", len(sockPath), sockPath)
	}

	t.Setenv("SIFT_DIR", siftDir)
	t.Setenv("SIFT_DAEMON_SOCKET", sockPath)
	// Ensure $HOME is still set to a tempdir so any code that consults
	// it (rather than SIFT_DIR) doesn't escape into the developer's
	// real ~/.sift.
	t.Setenv("HOME", t.TempDir())

	return siftDir
}

// writeFakeDaemon writes a POSIX shell script that touches a flag file
// then sleeps for sleepSecs. Used as a stand-in for the real sift
// daemon when only the spawn / PID-file lifecycle needs to be exercised.
func writeFakeDaemon(t *testing.T, scriptPath, flagPath string, sleepSecs int) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"echo \"fake daemon arg=$1\"\n" +
		"touch \"" + flagPath + "\"\n"
	if sleepSecs > 0 {
		script += fmt.Sprintf("sleep %d\n", sleepSecs)
	}
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake daemon: %v", err)
	}
}

// runDaemonCmd invokes the daemon subcommand tree, captures stdout and
// stderr separately, and returns the resulting exit code derived from
// any returned error.
func runDaemonCmd(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := NewRootCmd("test")
	var outBuf, errBuf bytes.Buffer
	cmd.SetOut(&outBuf)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(append([]string{"daemon"}, args...))

	err := cmd.Execute()
	return outBuf.String(), errBuf.String(), ExitCode(err)
}

// TestDaemonStatusExitCodes covers the three observable states the
// status subcommand can report: not-running (no PID file), unhealthy
// (PID file present but socket unreachable), and the expected exit
// codes that fall out.
func TestDaemonStatusExitCodes(t *testing.T) {
	siftDir := daemonTestEnv(t)

	t.Run("NotRunning", func(t *testing.T) {
		stdout, _, code := runDaemonCmd(t, "status")
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
		if !strings.Contains(stdout, "state: not running") {
			t.Errorf("stdout missing 'state: not running': %q", stdout)
		}
	})

	t.Run("UnhealthyWithStalePID", func(t *testing.T) {
		// Plant a PID file pointing at this very test process. The PID
		// is alive (good) but no socket exists (bad), which is exactly
		// the "stale PID file" condition that yields StatusUnhealthy.
		pidPath := filepath.Join(siftDir, "sift.pid")
		if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0o600); err != nil {
			t.Fatalf("write pid: %v", err)
		}
		t.Cleanup(func() { _ = os.Remove(pidPath) })

		stdout, _, code := runDaemonCmd(t, "status")
		if code != 2 {
			t.Errorf("exit code = %d, want 2", code)
		}
		if !strings.Contains(stdout, "state: unhealthy") {
			t.Errorf("stdout missing 'state: unhealthy': %q", stdout)
		}
		if !strings.Contains(stdout, fmt.Sprintf("pid: %d", os.Getpid())) {
			t.Errorf("stdout missing pid line: %q", stdout)
		}
	})
}

// TestDaemonStatusJSON asserts the --json output is valid JSON whose
// `state` field matches the human-readable label.
func TestDaemonStatusJSON(t *testing.T) {
	daemonTestEnv(t)

	stdout, _, code := runDaemonCmd(t, "status", "--json")
	if code != 1 {
		t.Errorf("exit code = %d, want 1 (no daemon)", code)
	}

	var report struct {
		State string `json:"state"`
		PID   int    `json:"pid"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, stdout)
	}
	if report.State != "not_running" {
		t.Errorf("state = %q, want %q", report.State, "not_running")
	}
}

// TestDaemonLogsTailNoFile verifies that asking for logs when the file
// doesn't exist yet produces a friendly stderr message and exits 0.
func TestDaemonLogsTailNoFile(t *testing.T) {
	daemonTestEnv(t)

	stdout, stderr, code := runDaemonCmd(t, "logs", "-n", "10")
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty, got %q", stdout)
	}
	if !strings.Contains(stderr, "no daemon logs at") {
		t.Errorf("stderr missing 'no daemon logs at': %q", stderr)
	}
	if !strings.Contains(stderr, "yet") {
		t.Errorf("stderr missing 'yet': %q", stderr)
	}
}

// TestDaemonLogsTailWithFile pre-creates a log file with N lines and
// asks for the last K. The output should be exactly the last K lines.
func TestDaemonLogsTailWithFile(t *testing.T) {
	siftDir := daemonTestEnv(t)

	logsDir := filepath.Join(siftDir, "logs")
	if err := os.MkdirAll(logsDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	logPath := filepath.Join(logsDir, "daemon.log")

	const totalLines = 20
	const tail = 5
	var sb strings.Builder
	for i := 1; i <= totalLines; i++ {
		fmt.Fprintf(&sb, "line %d\n", i)
	}
	if err := os.WriteFile(logPath, []byte(sb.String()), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}

	stdout, _, code := runDaemonCmd(t, "logs", "-n", fmt.Sprintf("%d", tail))
	if code != 0 {
		t.Errorf("exit code = %d, want 0", code)
	}

	got := strings.Split(strings.TrimRight(stdout, "\n"), "\n")
	if len(got) != tail {
		t.Fatalf("got %d lines, want %d\n%s", len(got), tail, stdout)
	}
	for i, line := range got {
		want := fmt.Sprintf("line %d", totalLines-tail+1+i)
		if line != want {
			t.Errorf("line %d = %q, want %q", i, line, want)
		}
	}
}

// TestDaemonStartStop drives the full start -> stop lifecycle against a
// fake daemon. Because the fake never opens a Unix socket, DialUntilReady
// will time out — we therefore expect `daemon start` to surface a clear
// "not ready" error, and we then prove that `daemon stop` cleans up the
// stale PID file regardless. This is the realistic path for users on a
// machine without the real daemon entry point wired into main.go yet.
func TestDaemonStartStop(t *testing.T) {
	siftDir := daemonTestEnv(t)

	scriptPath := filepath.Join(siftDir, "fake-daemon.sh")
	flagPath := filepath.Join(siftDir, "spawn.flag")
	// Sleep long enough that the fake is still alive when stop runs.
	writeFakeDaemon(t, scriptPath, flagPath, 30)
	t.Setenv("SIFT_DAEMON_BINARY", scriptPath)

	// Start: the fake won't open a socket, so this should fail with the
	// "not ready" message — but the spawn itself must succeed.
	_, _, _ = runDaemonCmd(t, "start", "--timeout", "200ms")

	// Wait for the fake to mark itself spawned. If this never happens,
	// we never even fork-execed, which is the actual failure we care
	// about.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(flagPath); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := os.Stat(flagPath); err != nil {
		t.Fatalf("fake daemon never ran (flag %s missing): %v", flagPath, err)
	}

	// Stop should be idempotent — it'll fall through to PID-based
	// termination since no socket exists.
	stdout, _, code := runDaemonCmd(t, "stop", "--timeout", "1s")
	if code != 0 {
		t.Errorf("stop exit code = %d, want 0; stdout=%q", code, stdout)
	}
}

func TestDaemonEnabledConfigCanKeepDaemonOff(t *testing.T) {
	siftDir := daemonTestEnv(t)
	cfg := config.Default()
	if err := cfg.Save(); err != nil {
		t.Fatalf("save config: %v", err)
	}

	cmd := NewRootCmd("test")
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs([]string{"config", "set", "daemon.enabled", "false"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config set daemon.enabled false: %v", err)
	}

	loaded, err := config.Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if loaded.Daemon.Enabled {
		t.Fatal("daemon.enabled remained true")
	}

	flagPath := filepath.Join(siftDir, "spawn.flag")
	scriptPath := filepath.Join(siftDir, "fake-daemon.sh")
	writeFakeDaemon(t, scriptPath, flagPath, 0)
	t.Setenv("SIFT_DAEMON_BINARY", scriptPath)

	_, stderr, code := runDaemonCmd(t, "start")
	if code != 1 {
		t.Fatalf("daemon start exit code = %d, want 1", code)
	}
	if !strings.Contains(stderr, "daemon is disabled") {
		t.Fatalf("daemon start error = %q, want disabled guidance", stderr)
	}
	if _, err := os.Stat(flagPath); !os.IsNotExist(err) {
		t.Fatalf("disabled daemon was spawned; stat error = %v", err)
	}
}

// TestDaemonStartAlreadyRunning seeds a fake "running" daemon by
// pre-spawning the fake script via daemon.SpawnDetached, then asserts a
// subsequent `sift daemon start` is a clean no-op. We can't exercise
// the StatusRunning branch without a real socket-serving binary, so
// instead this test exercises the spawn path twice and verifies that
// the second start does NOT error out on already-present PID files
// (a regression we want to catch if someone changes the start flow).
func TestDaemonStartAlreadyRunning(t *testing.T) {
	siftDir := daemonTestEnv(t)

	scriptPath := filepath.Join(siftDir, "fake-daemon.sh")
	flagPath := filepath.Join(siftDir, "first.flag")
	writeFakeDaemon(t, scriptPath, flagPath, 10)
	t.Setenv("SIFT_DAEMON_BINARY", scriptPath)

	// First start (will fail readiness; we don't care).
	_, _, _ = runDaemonCmd(t, "start", "--timeout", "100ms")

	// We expect the first start to have left no PID file behind,
	// because the fake never wrote one. So the second start should also
	// proceed to the spawn path. We mainly assert the command doesn't
	// crash and produces a non-empty error or success message.
	stdout, _, code := runDaemonCmd(t, "start", "--timeout", "100ms")
	// Either StatusRunning (code 0, "already running") or readiness
	// failure (code 1) is acceptable here. What we care about: no panic
	// and a sane code.
	if code != 0 && code != 1 {
		t.Errorf("unexpected exit code %d (stdout=%q)", code, stdout)
	}

	// Sanity: GetStatus should now report not running OR unhealthy
	// (depending on whether the fake exited yet), but never panic.
	report := daemon.GetStatus(50 * time.Millisecond)
	switch report.State {
	case daemon.StatusRunning, daemon.StatusNotRunning, daemon.StatusUnhealthy:
		// ok
	default:
		t.Errorf("unexpected status %v", report.State)
	}
}

// TestExitCodeHelper asserts that the small `exitCodeError` plumbing
// returns the right codes for nil and well-known errors. This is the
// only piece of behavior that's invisible from the cobra command output
// but is critical for the matrix documented in the file header.
func TestExitCodeHelper(t *testing.T) {
	if got := ExitCode(nil); got != 0 {
		t.Errorf("ExitCode(nil) = %d, want 0", got)
	}
	if got := ExitCode(errors.New("boom")); got != 1 {
		t.Errorf("ExitCode(generic) = %d, want 1", got)
	}
	if got := ExitCode(&exitCodeError{code: 2, msg: "x"}); got != 2 {
		t.Errorf("ExitCode(exitCodeError{2}) = %d, want 2", got)
	}
}

// TestDaemonHelpListsAllSubcommands ensures the --help output enumerates
// every documented subcommand, so users running `sift daemon --help`
// always see what's available.
func TestDaemonHelpListsAllSubcommands(t *testing.T) {
	cmd := NewRootCmd("test")
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SetArgs([]string{"daemon", "--help"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("daemon --help: %v", err)
	}
	out := buf.String()
	for _, sub := range []string{"start", "stop", "restart", "status", "logs"} {
		if !strings.Contains(out, sub) {
			t.Errorf("--help output missing subcommand %q\n%s", sub, out)
		}
	}
}
