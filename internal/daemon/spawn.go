package daemon

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"

	"sift/internal/config"
)

// daemonBinaryEnv lets tests substitute the binary that SpawnDetached
// invokes. When set, it is used verbatim as the executable; otherwise
// os.Executable() resolves the current sift binary.
const daemonBinaryEnv = "SIFT_DAEMON_BINARY"

// daemonSpawnedEnv is set in the child's environment so the child can
// distinguish a daemon invocation from a normal CLI invocation. The CLI
// entry point inspects this when deciding whether to run the daemon
// shim instead of the cobra root.
const daemonSpawnedEnv = "SIFT_DAEMON_SPAWNED"

// SpawnDetached forks a new sift process running the daemon entry shim.
//
// It does NOT wait for the daemon to become ready — callers should poll
// via DialUntilReady. The child runs in a new session (setsid) so it
// survives termination of the parent, with stdout/stderr redirected to
// the daemon log file (~/.sift/logs/daemon.log).
//
// Honors SIFT_DAEMON_BINARY for tests that want to substitute a fake
// daemon executable.
func SpawnDetached() error {
	logger := slog.With(slog.String("component", "daemon-lifecycle"), slog.String("op", "SpawnDetached"))

	// 1) Resolve the daemon binary.
	binary := os.Getenv(daemonBinaryEnv)
	if binary == "" {
		exe, err := os.Executable()
		if err != nil {
			logger.Error("resolve self binary", slog.Any("err", err))
			return fmt.Errorf("resolve self binary: %w", err)
		}
		// Refuse to spawn a Go test binary as a daemon. Without this guard,
		// any test that exercises the auto-spawn path leaks a `*.test __daemon`
		// process per call (the child has no main() to handle __daemon and
		// gets stuck running the test suite again under setsid). Tests that
		// genuinely want to exercise spawning must set SIFT_DAEMON_BINARY.
		if strings.HasSuffix(exe, ".test") || strings.Contains(exe, ".test/") {
			logger.Warn("refusing to spawn test binary as daemon",
				slog.String("binary", exe))
			return fmt.Errorf("refusing to spawn test binary as daemon: %s", exe)
		}
		binary = exe
	}
	logger.Info("spawning daemon", slog.String("binary", binary))

	// 2) Open daemon log file (append mode), creating the parent dir if
	// needed. The child inherits the fd via Stdout/Stderr.
	logPath, err := config.DaemonLogPath()
	if err != nil {
		logger.Error("resolve daemon log path", slog.Any("err", err))
		return fmt.Errorf("resolve daemon log path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		logger.Error("create log dir",
			slog.String("path", filepath.Dir(logPath)),
			slog.Any("err", err),
		)
		return fmt.Errorf("create log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		logger.Error("open daemon log", slog.String("path", logPath), slog.Any("err", err))
		return fmt.Errorf("open daemon log %s: %w", logPath, err)
	}
	// Always close our handle once Start has dup'd it into the child. If
	// Start fails we still need to close to avoid leaking the fd.
	defer func() {
		if cerr := logFile.Close(); cerr != nil {
			logger.Warn("close log file", slog.Any("err", cerr))
		}
	}()

	// 3) Build the child command.
	cmd := exec.Command(binary, "__daemon")
	cmd.Env = append(os.Environ(), daemonSpawnedEnv+"=1")
	cmd.Stdin = nil
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.SysProcAttr = detachedSysProcAttr()

	// 4) Start the child without waiting for it.
	if err := cmd.Start(); err != nil {
		logger.Error("start daemon",
			slog.String("binary", binary),
			slog.Any("err", err),
		)
		return fmt.Errorf("start daemon %s: %w", binary, err)
	}

	pid := cmd.Process.Pid
	// Release so we don't keep a wait-handle that would zombie the child
	// when this process exits.
	if err := cmd.Process.Release(); err != nil {
		// Non-fatal: the child has already started; reaping is the only
		// thing at risk and the parent may exit shortly anyway.
		logger.Warn("release child handle", slog.Int("pid", pid), slog.Any("err", err))
	}

	logger.Info("daemon spawned", slog.Int("pid", pid), slog.String("log_path", logPath))
	return nil
}

// detachedSysProcAttr returns the SysProcAttr needed to fully detach the
// child from the parent's controlling terminal. Setsid is supported on
// macOS and Linux, the only supported platforms for the daemon.
func detachedSysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}
