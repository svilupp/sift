package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"

	"sift/internal/config"
)

// Status enumerates the daemon's observable lifecycle states from the
// perspective of a CLI invocation looking up state via PID file + dial.
type Status int

const (
	// StatusUnknown is the zero value; lifecycle code never returns it
	// directly but it is the natural default for callers.
	StatusUnknown Status = iota

	// StatusRunning means the daemon is reachable on its socket and
	// answered /health within the configured timeout.
	StatusRunning

	// StatusNotRunning means there is no PID file (so no daemon was
	// ever started, or it shut down cleanly and removed it).
	StatusNotRunning

	// StatusUnhealthy means a PID file exists but the daemon does not
	// answer on its socket. This typically means a crashed daemon left
	// behind a stale PID file; callers should consider Restart.
	StatusUnhealthy
)

// String returns a stable, lower-case label suitable for log lines and
// CLI output.
func (s Status) String() string {
	switch s {
	case StatusRunning:
		return "running"
	case StatusNotRunning:
		return "not_running"
	case StatusUnhealthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

// StatusReport bundles the daemon state, the parsed PID (if any), the
// last-known health response, and an explanation when something is off.
type StatusReport struct {
	State  Status
	Health *HealthResponse // populated when State == StatusRunning
	PID    int             // 0 when not running or unreadable
	Err    error           // optional explanation when Unhealthy
}

// stopGracePeriod is the deadline for SIGTERM to take effect before we
// escalate to SIGKILL.
const stopGracePeriod = 5 * time.Second

// stopPollInterval drives the polling loops in Stop/Restart.
const stopPollInterval = 50 * time.Millisecond

// GetStatus probes the daemon and reports back. Resolution rules:
//   - No PID file -> StatusNotRunning.
//   - PID file present and TryDial succeeds within timeout -> StatusRunning.
//   - PID file present and TryDial fails -> StatusUnhealthy with Err set.
//
// The PID is populated whenever the file exists and parses, regardless of
// dial result.
func GetStatus(timeout time.Duration) StatusReport {
	logger := slog.With(slog.String("component", "daemon-lifecycle"), slog.String("op", "GetStatus"))

	pidPath, err := config.PIDPath()
	if err != nil {
		logger.Warn("resolve pid path", slog.Any("err", err))
		return StatusReport{State: StatusUnhealthy, Err: fmt.Errorf("resolve pid path: %w", err)}
	}

	pid, pidErr := readPIDFile(pidPath)
	if pidErr != nil {
		if errors.Is(pidErr, os.ErrNotExist) {
			logger.Info("no pid file, daemon not running", slog.String("path", pidPath))
			return StatusReport{State: StatusNotRunning}
		}
		logger.Warn("read pid file", slog.String("path", pidPath), slog.Any("err", pidErr))
		// Treat as unhealthy: the file is there but we can't parse it.
		return StatusReport{State: StatusUnhealthy, Err: fmt.Errorf("read pid file: %w", pidErr)}
	}

	client, dialErr := TryDial(timeout)
	if dialErr != nil {
		logger.Warn("dial failed despite pid file present",
			slog.Int("pid", pid),
			slog.Any("err", dialErr),
		)
		return StatusReport{State: StatusUnhealthy, PID: pid, Err: dialErr}
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	health, healthErr := client.Health(ctx)
	if healthErr != nil {
		logger.Warn("health probe failed",
			slog.Int("pid", pid),
			slog.Any("err", healthErr),
		)
		return StatusReport{State: StatusUnhealthy, PID: pid, Err: healthErr}
	}

	logger.Info("daemon healthy", slog.Int("pid", pid), slog.Int64("uptime_s", health.UptimeS))
	return StatusReport{State: StatusRunning, PID: pid, Health: health}
}

// Stop terminates the daemon if it is running, in three escalating
// phases:
//
//  1. Try a graceful POST /shutdown via the unix socket. If it works,
//     wait up to `timeout` for the socket file to disappear.
//  2. If the socket is unreachable, look up the PID and send SIGTERM,
//     waiting up to stopGracePeriod for the process to exit.
//  3. If still alive, send SIGKILL.
//
// Stale socket and PID files are removed at the end. Stop is idempotent:
// if the daemon is already gone it returns nil.
func Stop(timeout time.Duration) error {
	logger := slog.With(slog.String("component", "daemon-lifecycle"), slog.String("op", "Stop"))
	logger.Info("stop requested", slog.Duration("timeout", timeout))

	socketPath, err := config.SocketPath()
	if err != nil {
		logger.Warn("resolve socket path", slog.Any("err", err))
		return fmt.Errorf("resolve socket path: %w", err)
	}
	pidPath, err := config.PIDPath()
	if err != nil {
		logger.Warn("resolve pid path", slog.Any("err", err))
		return fmt.Errorf("resolve pid path: %w", err)
	}

	// Always clean up at the end, even on error paths, so a stale socket
	// or PID file doesn't block a future Restart.
	defer cleanupStaleFiles(logger, socketPath, pidPath)

	// 1) Graceful shutdown via /shutdown.
	client, dialErr := TryDial(50 * time.Millisecond)
	if dialErr == nil {
		defer client.Close()
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		if shutErr := client.Shutdown(ctx); shutErr == nil {
			logger.Info("graceful shutdown accepted, waiting for socket to disappear")
			waitForFileGone(socketPath, timeout)
			return nil
		} else {
			logger.Warn("graceful shutdown failed, falling back to signals", slog.Any("err", shutErr))
		}
	} else {
		logger.Info("daemon not reachable on socket, falling back to signals", slog.Any("err", dialErr))
	}

	// 2) PID-based termination.
	pid, pidErr := readPIDFile(pidPath)
	if pidErr != nil {
		if errors.Is(pidErr, os.ErrNotExist) {
			logger.Info("no pid file present, treating as already-stopped")
			return nil
		}
		logger.Warn("read pid file", slog.String("path", pidPath), slog.Any("err", pidErr))
		return fmt.Errorf("read pid file: %w", pidErr)
	}

	if !processAlive(pid) {
		logger.Info("pid file points at dead process, nothing to signal", slog.Int("pid", pid))
		return nil
	}

	logger.Info("sending SIGTERM", slog.Int("pid", pid))
	if err := signalPID(pid, syscall.SIGTERM); err != nil {
		// ESRCH = already gone — fine.
		if errors.Is(err, syscall.ESRCH) {
			logger.Info("process already gone before SIGTERM", slog.Int("pid", pid))
			return nil
		}
		logger.Warn("send SIGTERM", slog.Int("pid", pid), slog.Any("err", err))
		return fmt.Errorf("send SIGTERM to pid %d: %w", pid, err)
	}
	if waitForProcessExit(pid, stopGracePeriod) {
		logger.Info("process exited after SIGTERM", slog.Int("pid", pid))
		return nil
	}

	// 3) Force kill.
	logger.Warn("SIGTERM did not take effect, sending SIGKILL", slog.Int("pid", pid))
	if err := signalPID(pid, syscall.SIGKILL); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			logger.Info("process gone after SIGTERM after all", slog.Int("pid", pid))
			return nil
		}
		logger.Warn("send SIGKILL", slog.Int("pid", pid), slog.Any("err", err))
		return fmt.Errorf("send SIGKILL to pid %d: %w", pid, err)
	}
	// Best-effort wait; SIGKILL is non-catchable so this should be quick.
	waitForProcessExit(pid, stopGracePeriod)
	return nil
}

// Restart stops a running daemon (if any), waits for the socket to
// disappear, then spawns a fresh daemon and waits for it to become ready.
func Restart(timeout time.Duration) error {
	logger := slog.With(slog.String("component", "daemon-lifecycle"), slog.String("op", "Restart"))
	logger.Info("restart requested", slog.Duration("timeout", timeout))

	if err := Stop(timeout); err != nil {
		logger.Warn("stop failed during restart", slog.Any("err", err))
		return fmt.Errorf("restart: stop: %w", err)
	}

	socketPath, err := config.SocketPath()
	if err != nil {
		logger.Warn("resolve socket path", slog.Any("err", err))
		return fmt.Errorf("restart: resolve socket path: %w", err)
	}
	if !waitForFileGone(socketPath, 5*time.Second) {
		logger.Warn("socket still present after stop, removing and continuing",
			slog.String("path", socketPath),
		)
		if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
			logger.Warn("remove lingering socket", slog.Any("err", err))
		}
	}

	if err := SpawnDetached(); err != nil {
		logger.Warn("spawn failed during restart", slog.Any("err", err))
		return fmt.Errorf("restart: spawn: %w", err)
	}

	client, err := DialUntilReady(timeout)
	if err != nil {
		logger.Warn("daemon never became ready", slog.Any("err", err))
		return fmt.Errorf("restart: wait ready: %w", err)
	}
	defer client.Close()

	logger.Info("restart complete")
	return nil
}

// readPIDFile parses the contents of pidPath as a positive integer PID.
// If the file is missing it returns os.ErrNotExist (unwrapped via
// errors.Is). All other errors carry context.
func readPIDFile(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	s := strings.TrimSpace(string(data))
	if s == "" {
		return 0, fmt.Errorf("pid file %s is empty", path)
	}
	pid, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("parse pid %q: %w", s, err)
	}
	if pid <= 0 {
		return 0, fmt.Errorf("pid %d is not positive", pid)
	}
	return pid, nil
}

// processAlive reports whether sending signal 0 to pid succeeds. Signal
// 0 is the standard POSIX existence check.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}

// signalPID sends sig to pid via os.FindProcess + Signal.
func signalPID(pid int, sig syscall.Signal) error {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return fmt.Errorf("find process %d: %w", pid, err)
	}
	return proc.Signal(sig)
}

// waitForProcessExit polls until the process is gone or the deadline
// elapses. Returns true when the process is no longer alive.
func waitForProcessExit(pid int, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(stopPollInterval)
	}
	return !processAlive(pid)
}

// waitForFileGone polls until path no longer exists or the deadline
// elapses. Returns true when the file is gone.
func waitForFileGone(path string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
			return true
		}
		time.Sleep(stopPollInterval)
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return true
	}
	return false
}

// cleanupStaleFiles removes the daemon socket and PID file. Missing
// files are not errors; other failures are logged at warn but never
// propagated, since cleanup is best-effort.
func cleanupStaleFiles(logger *slog.Logger, socketPath, pidPath string) {
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		logger.Warn("remove stale socket", slog.String("path", socketPath), slog.Any("err", err))
	}
	if err := os.Remove(pidPath); err != nil && !os.IsNotExist(err) {
		logger.Warn("remove stale pid file", slog.String("path", pidPath), slog.Any("err", err))
	}
}
