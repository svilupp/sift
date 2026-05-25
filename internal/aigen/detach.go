package aigen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// DetachOptions configures a background-detach spawn.
type DetachOptions struct {
	// PIDFile is where the child writes its PID. Default:
	// $SIFT_DIR/refresh-index.pid (resolved by the caller; this
	// package does not import internal/config to keep the dep graph
	// clean).
	PIDFile string

	// LogFile is where stdout/stderr go after detach.
	LogFile string

	// Args are command-line args to re-exec with. The detach helper
	// strips the `--detach` flag from these so the child runs the
	// foreground path.
	Args []string

	// Env are extra environment variables to set on the child. Pass
	// nil to inherit os.Environ unchanged.
	Env []string
}

// Detach forks a copy of the running binary into the background. The
// parent returns immediately after writing the child's PID to PIDFile.
// Stdout/stderr of the child are redirected to LogFile (append).
//
// macOS-specific note: we use Setsid to detach from the controlling
// terminal. SIGHUP from terminal close is then ignored by the child.
//
// Limitations:
//   - This is a single-fork detach (not the full POSIX double-fork). On
//     macOS this is sufficient for shell-exit survival in practice.
//     If the parent shell process is killed via SIGTERM to its process
//     group, the child still inherits Setsid.
//   - Stdin is redirected to /dev/null.
//
// Returns the child PID on success.
func Detach(opts DetachOptions) (int, error) {
	if opts.PIDFile == "" {
		return 0, fmt.Errorf("detach: PIDFile required")
	}
	if opts.LogFile == "" {
		return 0, fmt.Errorf("detach: LogFile required")
	}

	exe, err := os.Executable()
	if err != nil {
		return 0, fmt.Errorf("detach: locate binary: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(opts.PIDFile), 0o755); err != nil {
		return 0, fmt.Errorf("detach: mkdir pid dir: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(opts.LogFile), 0o755); err != nil {
		return 0, fmt.Errorf("detach: mkdir log dir: %w", err)
	}

	// Open the log file (append, line-buffered semantics: line writes
	// from Go's stdlib buffering get flushed at \n boundaries).
	logF, err := os.OpenFile(opts.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, fmt.Errorf("detach: open log: %w", err)
	}
	defer func() { _ = logF.Close() }()

	// Strip --detach (and --status) from args; child runs foreground.
	args := stripDetachFlag(opts.Args)

	cmd := exec.Command(exe, args...)
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
	env := os.Environ()
	if len(opts.Env) > 0 {
		env = append(env, opts.Env...)
	}
	cmd.Env = env

	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("detach: start: %w", err)
	}
	pid := cmd.Process.Pid
	// Release: we do not Wait on the child.
	if err := cmd.Process.Release(); err != nil {
		// Non-fatal; child runs anyway.
		_ = err
	}

	if err := os.WriteFile(opts.PIDFile, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return pid, fmt.Errorf("detach: write pid: %w", err)
	}

	return pid, nil
}

// stripDetachFlag drops `--detach` (or `--detach=true`) from args so
// the spawned child does not recurse.
func stripDetachFlag(args []string) []string {
	out := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--detach" {
			continue
		}
		if strings.HasPrefix(a, "--detach=") {
			continue
		}
		out = append(out, a)
	}
	return out
}

// ReadPID returns the PID stored in the file, or 0 with an error.
func ReadPID(path string) (int, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil {
		return 0, fmt.Errorf("parse pid: %w", err)
	}
	return pid, nil
}

// IsAlive reports whether the process at pid exists and is reachable
// (kill(pid, 0) succeeds).
func IsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	if err := p.Signal(syscall.Signal(0)); err != nil {
		return false
	}
	return true
}

// CleanupPIDFile removes the pid file. Errors are ignored on absent
// files but bubble otherwise.
func CleanupPIDFile(path string) error {
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove pid file: %w", err)
	}
	return nil
}
