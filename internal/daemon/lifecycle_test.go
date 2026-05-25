package daemon

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// withLifecycleEnv sets up SIFT_DIR + a short SIFT_DAEMON_SOCKET path.
// macOS sun_path is 104 chars, and t.TempDir() under macOS Xcode test
// runners produces paths well over that. We mkdir-temp under os.TempDir()
// for the socket path to stay safely short.
func withLifecycleEnv(t *testing.T) (siftDir string, socketPath string, pidPath string) {
	t.Helper()

	siftDir = t.TempDir()
	t.Setenv("SIFT_DIR", siftDir)

	// Short directory for the socket. /tmp is universally short.
	sockDir, err := os.MkdirTemp("", "sift")
	if err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(sockDir) })
	socketPath = filepath.Join(sockDir, "d.sock")
	t.Setenv("SIFT_DAEMON_SOCKET", socketPath)

	pidPath = filepath.Join(siftDir, "sift.pid")
	return siftDir, socketPath, pidPath
}

func TestGetStatus_NoPIDFile(t *testing.T) {
	_, _, _ = withLifecycleEnv(t)

	rep := GetStatus(100 * time.Millisecond)
	if rep.State != StatusNotRunning {
		t.Fatalf("State = %v; want StatusNotRunning", rep.State)
	}
	if rep.PID != 0 {
		t.Fatalf("PID = %d; want 0", rep.PID)
	}
	if rep.Health != nil {
		t.Fatalf("Health = %+v; want nil", rep.Health)
	}
}

func TestGetStatus_StalePIDFileDialFails(t *testing.T) {
	_, _, pidPath := withLifecycleEnv(t)

	// Pick a PID that almost certainly doesn't exist (PID 1 always
	// exists, so use a high improbable one — the dial will still fail
	// because no daemon is listening on the socket regardless).
	const fakePID = 999999
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(fakePID)), 0o600); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	rep := GetStatus(100 * time.Millisecond)
	if rep.State != StatusUnhealthy {
		t.Fatalf("State = %v; want StatusUnhealthy", rep.State)
	}
	if rep.PID != fakePID {
		t.Fatalf("PID = %d; want %d", rep.PID, fakePID)
	}
	if rep.Err == nil {
		t.Fatalf("Err = nil; want non-nil dial error")
	}
}

func TestStop_NoDaemon(t *testing.T) {
	_, _, _ = withLifecycleEnv(t)

	if err := Stop(500 * time.Millisecond); err != nil {
		t.Fatalf("Stop on cold environment returned error: %v", err)
	}
}

func TestStop_RemovesStaleSocket(t *testing.T) {
	_, socketPath, pidPath := withLifecycleEnv(t)

	// Pre-create the socket file (regular file is fine for the cleanup
	// test — we are not dialing).
	if err := os.WriteFile(socketPath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("create stale socket: %v", err)
	}
	// Do not write a PID file: simulates a daemon that died before
	// recording its PID.
	if err := Stop(500 * time.Millisecond); err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if _, err := os.Stat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("stale socket %s still present after Stop (err=%v)", socketPath, err)
	}
	if _, err := os.Stat(pidPath); err == nil {
		t.Fatalf("pid file unexpectedly created at %s", pidPath)
	}
}

func TestRestart_StopThenSpawn(t *testing.T) {
	siftDir, _, _ := withLifecycleEnv(t)

	// Fake daemon binary: writes a flag file and exits immediately.
	scriptPath := filepath.Join(siftDir, "fake-daemon.sh")
	flagPath := filepath.Join(siftDir, "restart.flag")
	fakeDaemonScript(t, scriptPath, flagPath, 0)
	t.Setenv("SIFT_DAEMON_BINARY", scriptPath)

	// Restart calls DialUntilReady at the end, which will fail because
	// our fake daemon exits without binding a socket. We accept that
	// error and only assert the flag file was written (proving the
	// spawn-after-stop pipeline ran).
	_ = Restart(300 * time.Millisecond)

	if !waitForFile(t, flagPath, 2*time.Second) {
		t.Fatalf("flag file %s never appeared; spawn did not run", flagPath)
	}
}

// listenSocket is a small helper for future tests that need a real
// listener on socketPath; not used in U9 but exported as a hook for
// follow-on work units. Kept package-level so it doesn't appear unused.
//
//nolint:unused // used by follow-on work units
func listenSocket(t *testing.T, socketPath string) net.Listener {
	t.Helper()
	ln, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	return ln
}
