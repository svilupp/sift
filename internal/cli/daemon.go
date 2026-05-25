// Package cli — daemon subcommand tree.
//
// This file wires the user-facing `sift daemon ...` commands onto the
// lifecycle helpers in internal/daemon. The CLI is intentionally thin:
// it parses flags, delegates to daemon.GetStatus / Stop / Restart /
// SpawnDetached, and formats the result. All process management,
// socket I/O, and signal escalation lives behind the daemon package.
//
// Exit-code matrix (used by tests and shell scripting):
//
//	start    0 = running (already or now), 1 = spawn or readiness failure
//	stop     0 = stopped or already gone, 1 = stop failed
//	restart  0 = ran and ready, 1 = stop / spawn / readiness failure
//	status   0 = running, 1 = not running, 2 = unhealthy
//	logs     0 = printed (or no log yet), 1 = read error
package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"sift/internal/config"
	"sift/internal/daemon"
)

// defaultStopTimeout is the fallback for `sift daemon stop` and
// `sift daemon restart` when neither the user nor the config asks
// for a different value.
const defaultStopTimeout = 5 * time.Second

// statusProbeTimeout is the deadline used for the quick GetStatus
// probe done by `start`, `stop`, and `status` themselves. It is
// intentionally short — we are only checking whether a daemon is
// already up, not waiting for one to come up.
const statusProbeTimeout = 50 * time.Millisecond

// logsPollInterval governs how often `sift daemon logs -f` checks
// for appended bytes after reaching EOF.
const logsPollInterval = 200 * time.Millisecond

// newDaemonCmd builds the parent `sift daemon` command and attaches
// its subcommands.
func newDaemonCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the sift daemon process",
		Long: `Manage the long-lived sift daemon process.

The daemon serves search and refresh requests over a Unix socket so the
CLI can avoid paying database / index startup cost on every invocation.

Subcommands:
  start    spawn the daemon if not running, then wait for it to be ready
  stop     terminate the daemon (graceful, then SIGTERM, then SIGKILL)
  restart  stop and start in one step
  status   report whether the daemon is running, with health details
  logs     view the daemon log file, optionally tailing -f`,
	}

	cmd.AddCommand(
		newDaemonStartCmd(),
		newDaemonStopCmd(),
		newDaemonRestartCmd(),
		newDaemonStatusCmd(),
		newDaemonLogsCmd(),
	)

	return cmd
}

// loadDaemonTimeouts pulls per-subcommand defaults from the config
// file. If config can't be loaded, conservative literals are used so
// `sift daemon` still works on a freshly cloned machine.
func loadDaemonTimeouts() (spawnTimeout, stopTimeout time.Duration) {
	spawnTimeout = 300 * time.Millisecond
	stopTimeout = defaultStopTimeout
	if cfg, err := config.Load(); err == nil {
		if d := cfg.Daemon.SpawnTimeout.D(); d > 0 {
			spawnTimeout = d
		}
	}
	return spawnTimeout, stopTimeout
}

// ---------------------------------------------------------------------------
// start
// ---------------------------------------------------------------------------

func newDaemonStartCmd() *cobra.Command {
	var timeoutStr string
	cmd := &cobra.Command{
		Use:   "start",
		Short: "Spawn the daemon and wait for it to become ready",
		Long: `Start the sift daemon.

If a daemon is already running, prints its PID and exits 0. Otherwise
spawns a detached child via daemon.SpawnDetached and polls /health until
it answers or --timeout elapses.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			spawnDefault, _ := loadDaemonTimeouts()
			timeout, err := resolveTimeout(timeoutStr, spawnDefault)
			if err != nil {
				return err
			}

			logger := slog.With(slog.String("component", "cli-daemon"), slog.String("op", "start"))

			// 1) Already running? Bail out cleanly.
			report := daemon.GetStatus(statusProbeTimeout)
			if report.State == daemon.StatusRunning {
				fmt.Fprintf(cmd.OutOrStdout(), "daemon already running (pid=%d)\n", report.PID)
				logger.Info("already running, no-op", slog.Int("pid", report.PID))
				return nil
			}

			// 2) Spawn detached.
			if err := daemon.SpawnDetached(); err != nil {
				logger.Warn("spawn failed", slog.Any("err", err))
				return fmt.Errorf("spawn daemon: %w", err)
			}

			// 3) Wait until it answers /health.
			client, err := daemon.DialUntilReady(timeout)
			if err != nil {
				logger.Warn("daemon not ready", slog.Any("err", err))
				return fmt.Errorf("daemon spawned but not ready within %s — check `sift daemon logs`", timeout)
			}
			defer client.Close()

			// Best-effort health summary; failures here don't fail the
			// command since the daemon IS up.
			ctx, cancel := context.WithTimeout(cmd.Context(), 500*time.Millisecond)
			defer cancel()
			if h, hErr := client.Health(ctx); hErr == nil {
				fmt.Fprintf(cmd.OutOrStdout(),
					"daemon started (pid=%d, version=%s, uptime=%s)\n",
					h.PID, h.Version, formatSeconds(h.UptimeS),
				)
			} else {
				logger.Warn("post-spawn health probe failed", slog.Any("err", hErr))
				fmt.Fprintln(cmd.OutOrStdout(), "daemon started")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&timeoutStr, "timeout", "", "Time to wait for daemon readiness (e.g. 500ms, 2s); defaults to config.Daemon.SpawnTimeout")
	return cmd
}

// ---------------------------------------------------------------------------
// stop
// ---------------------------------------------------------------------------

func newDaemonStopCmd() *cobra.Command {
	var timeoutStr string
	cmd := &cobra.Command{
		Use:   "stop",
		Short: "Terminate the running daemon",
		Long: `Stop the sift daemon.

Tries POST /shutdown first; on failure falls back to SIGTERM and finally
SIGKILL. Stop is idempotent — calling it when no daemon is running
exits 0 with a "not running" message.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			timeout, err := resolveTimeout(timeoutStr, defaultStopTimeout)
			if err != nil {
				return err
			}

			logger := slog.With(slog.String("component", "cli-daemon"), slog.String("op", "stop"))

			// Capture prior state so we can distinguish "stopped now" from
			// "wasn't running in the first place".
			prior := daemon.GetStatus(statusProbeTimeout)

			if err := daemon.Stop(timeout); err != nil {
				logger.Warn("stop failed", slog.Any("err", err))
				return fmt.Errorf("stop daemon: %w", err)
			}

			if prior.State == daemon.StatusNotRunning {
				fmt.Fprintln(cmd.OutOrStdout(), "daemon was not running")
			} else {
				fmt.Fprintln(cmd.OutOrStdout(), "daemon stopped")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&timeoutStr, "timeout", "", "Maximum time to wait for shutdown (e.g. 5s, 10s); defaults to 5s")
	return cmd
}

// ---------------------------------------------------------------------------
// restart
// ---------------------------------------------------------------------------

func newDaemonRestartCmd() *cobra.Command {
	var timeoutStr string
	cmd := &cobra.Command{
		Use:   "restart",
		Short: "Stop and start the daemon",
		Long: `Restart the sift daemon.

Equivalent to "sift daemon stop && sift daemon start" but performed by
the daemon package so the socket / PID file lifecycle is handled in one
shot. Useful after upgrading the binary.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			timeout, err := resolveTimeout(timeoutStr, defaultStopTimeout)
			if err != nil {
				return err
			}

			logger := slog.With(slog.String("component", "cli-daemon"), slog.String("op", "restart"))

			if err := daemon.Restart(timeout); err != nil {
				logger.Warn("restart failed", slog.Any("err", err))
				return fmt.Errorf("restart daemon: %w", err)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "daemon restarted")
			return nil
		},
	}
	cmd.Flags().StringVar(&timeoutStr, "timeout", "", "Maximum time to wait for restart (e.g. 5s, 10s); defaults to 5s")
	return cmd
}

// ---------------------------------------------------------------------------
// status
// ---------------------------------------------------------------------------

// statusReportJSON is the wire-shape printed by `sift daemon status --json`.
// We define it explicitly (rather than json-marshaling daemon.StatusReport)
// so we can encode the State as a stable string label and surface Err as
// a printable string.
type statusReportJSON struct {
	State  string                 `json:"state"`
	PID    int                    `json:"pid,omitempty"`
	Health *daemon.HealthResponse `json:"health,omitempty"`
	Error  string                 `json:"error,omitempty"`
}

func newDaemonStatusCmd() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Report whether the daemon is running",
		Long: `Print the daemon's current state.

Exit codes:
  0  running
  1  not running
  2  unhealthy (PID file present but socket unreachable)`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			cmd.SilenceErrors = true
			report := daemon.GetStatus(statusProbeTimeout)

			if asJSON {
				wire := statusReportJSON{
					State:  report.State.String(),
					PID:    report.PID,
					Health: report.Health,
				}
				if report.Err != nil {
					wire.Error = report.Err.Error()
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				if err := enc.Encode(wire); err != nil {
					return fmt.Errorf("encode status: %w", err)
				}
			} else {
				printStatusHuman(cmd.OutOrStdout(), report)
			}

			switch report.State {
			case daemon.StatusRunning:
				return nil
			case daemon.StatusNotRunning:
				// Use a sentinel error that yields exit code 1 without
				// re-printing usage. cobra.SilenceUsage above also helps.
				return &exitCodeError{code: 1, msg: "daemon not running"}
			case daemon.StatusUnhealthy:
				return &exitCodeError{code: 2, msg: "daemon unhealthy"}
			default:
				return &exitCodeError{code: 2, msg: "daemon state unknown"}
			}
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Emit machine-readable JSON instead of human-friendly text")
	return cmd
}

// printStatusHuman formats a StatusReport as the lines documented in
// the U11 spec. Kept separate so the test suite can compare exact
// substrings deterministically.
func printStatusHuman(w io.Writer, r daemon.StatusReport) {
	switch r.State {
	case daemon.StatusRunning:
		fmt.Fprintln(w, "state: running")
		if r.PID != 0 {
			fmt.Fprintf(w, "pid: %d\n", r.PID)
		}
		if r.Health != nil {
			fmt.Fprintf(w, "uptime: %s\n", formatSeconds(r.Health.UptimeS))
			fmt.Fprintf(w, "tls_dials: %d\n", r.Health.TLSDialsTotal)
			fmt.Fprintf(w, "requests: %d\n", r.Health.RequestCount)
			fmt.Fprintf(w, "version: %s\n", r.Health.Version)
		}
	case daemon.StatusNotRunning:
		fmt.Fprintln(w, "state: not running")
	case daemon.StatusUnhealthy:
		fmt.Fprintln(w, "state: unhealthy")
		if r.PID != 0 {
			fmt.Fprintf(w, "pid: %d\n", r.PID)
		}
		if r.Err != nil {
			fmt.Fprintf(w, "error: %s\n", r.Err.Error())
		}
	default:
		fmt.Fprintln(w, "state: unknown")
		if r.Err != nil {
			fmt.Fprintf(w, "error: %s\n", r.Err.Error())
		}
	}
}

// formatSeconds renders an int64 second count as a Go duration string
// (e.g. "3m12s") so output matches the U11 spec examples.
func formatSeconds(s int64) string {
	if s < 0 {
		s = 0
	}
	return (time.Duration(s) * time.Second).String()
}

// ---------------------------------------------------------------------------
// logs
// ---------------------------------------------------------------------------

func newDaemonLogsCmd() *cobra.Command {
	var follow bool
	var tail int
	cmd := &cobra.Command{
		Use:   "logs",
		Short: "Print the daemon log file (optionally tailing)",
		Long: `Print the daemon log file at ~/.sift/logs/daemon.log.

By default, the last 50 lines are printed. With -f / --follow, the file
is then polled for appended bytes until interrupted (Ctrl+C).

If the log file does not exist yet (e.g. the daemon has never been
started), a notice is written to stderr and the command exits 0.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			if tail < 0 {
				return fmt.Errorf("--tail must be non-negative, got %d", tail)
			}

			path, err := config.DaemonLogPath()
			if err != nil {
				return fmt.Errorf("resolve daemon log path: %w", err)
			}

			f, err := os.Open(path)
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					fmt.Fprintf(cmd.ErrOrStderr(), "no daemon logs at %s yet\n", path)
					return nil
				}
				return fmt.Errorf("open daemon log: %w", err)
			}
			defer f.Close()

			// 1) Print the last `tail` lines (or all if file is shorter).
			endOffset, err := writeLastNLines(f, tail, cmd.OutOrStdout())
			if err != nil {
				return fmt.Errorf("read daemon log: %w", err)
			}

			if !follow {
				return nil
			}

			// 2) Tail mode: poll for appended bytes until interrupted.
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			if _, err := f.Seek(endOffset, io.SeekStart); err != nil {
				return fmt.Errorf("seek daemon log: %w", err)
			}

			return followLog(ctx, f, cmd.OutOrStdout())
		},
	}
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "Tail the log file, printing appended lines as they arrive")
	cmd.Flags().IntVarP(&tail, "tail", "n", 50, "Number of trailing lines to print before any follow")
	return cmd
}

// writeLastNLines reads f from the current position to EOF, prints
// the last n lines (in arrival order) to w, and returns the offset of
// EOF so a follower can seek there. When n == 0, all lines are
// printed; this matches `tail -n 0` behavior in some implementations
// but is mostly a safety net for callers passing 0 explicitly.
func writeLastNLines(f *os.File, n int, w io.Writer) (int64, error) {
	scanner := bufio.NewScanner(f)
	// Allow long log lines (slog records can be large).
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)

	if n <= 0 {
		// Print everything.
		for scanner.Scan() {
			if _, err := fmt.Fprintln(w, scanner.Text()); err != nil {
				return 0, err
			}
		}
	} else {
		ring := make([]string, 0, n)
		for scanner.Scan() {
			line := scanner.Text()
			if len(ring) < n {
				ring = append(ring, line)
			} else {
				// Shift left; this is O(N*K) but K is bounded by n which
				// is small in practice (default 50).
				copy(ring, ring[1:])
				ring[len(ring)-1] = line
			}
		}
		for _, line := range ring {
			if _, err := fmt.Fprintln(w, line); err != nil {
				return 0, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return 0, err
	}

	pos, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return 0, err
	}
	return pos, nil
}

// followLog polls f for appended bytes and writes them to w until ctx
// is done. The poll interval is fixed at logsPollInterval.
func followLog(ctx context.Context, f *os.File, w io.Writer) error {
	buf := make([]byte, 32*1024)
	ticker := time.NewTicker(logsPollInterval)
	defer ticker.Stop()
	for {
		// Drain any bytes available right now before sleeping again.
		for {
			n, err := f.Read(buf)
			if n > 0 {
				if _, werr := w.Write(buf[:n]); werr != nil {
					return werr
				}
			}
			if err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return err
			}
			if n == 0 {
				break
			}
		}

		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			// Loop and read again.
		}
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// resolveTimeout parses the user-supplied --timeout flag, falling back
// to def when empty. It rejects zero or negative durations because the
// underlying lifecycle helpers take them literally and would never wait.
func resolveTimeout(s string, def time.Duration) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return def, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("parse --timeout %q: %w", s, err)
	}
	if d <= 0 {
		return 0, fmt.Errorf("--timeout must be positive, got %s", d)
	}
	return d, nil
}

// exitCodeError is an error type that the cobra root, or main.go, can
// inspect to derive a non-default process exit code. We don't os.Exit
// here so tests can run cobra commands in-process.
type exitCodeError struct {
	code int
	msg  string
}

func (e *exitCodeError) Error() string { return e.msg }

// ExitCode returns the process exit code associated with err, or 0
// if err is nil. Returns 1 for any error that is not an *exitCodeError,
// matching cobra's default convention. Exposed for use by main.go and
// tests that want to assert the right exit code without spawning a
// subprocess.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ec *exitCodeError
	if errors.As(err, &ec) {
		return ec.code
	}
	return 1
}
