package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"sift/internal/cli"
	"sift/internal/config"
	"sift/internal/daemon"
)

// daemonLogSizeWarnBytes is the threshold above which runDaemon logs a
// warning at startup. The internal/log package provides JSONL weekly
// rotation but does not expose an slog.Handler, and the daemon log itself
// is the captured stderr from spawn-redirection — not a JSONL stream. So
// we do the simplest correct thing: warn when the file grows past 100MB.
const daemonLogSizeWarnBytes = 100 * 1024 * 1024

// Build-time variables set via ldflags.
var (
	version   = "0.5.0"
	commit    = "unknown"
	buildTime = "unknown"
)

func versionString() string {
	return version + " (commit: " + commit + ", built: " + buildTime + ")"
}

func main() {
	// Daemon shim: when invoked as `sift __daemon`, bypass cobra and run
	// the daemon server directly. The check must happen before any cobra
	// setup so cobra never sees `__daemon` as an unknown command.
	if len(os.Args) >= 2 && os.Args[1] == "__daemon" {
		runDaemon()
		return
	}

	if err := cli.NewRootCmd(versionString()).Execute(); err != nil {
		// Cobra prints the error itself; we only need to translate the
		// exit code. `sift index check` uses code 2 for "defects found"
		// (lint convention) and code 1 for tool errors. Everything else
		// is a generic 1.
		if code := cli.IndexCheckExitCode(err); code > 0 {
			os.Exit(code)
		}
		os.Exit(1)
	}
}

func runDaemon() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	logger := slog.Default().With(slog.String("component", "daemon-shim"))
	logger.Info("daemon shim starting", slog.String("version", versionString()))

	// Best-effort daemon-log size check. The daemon's stderr is appended
	// to ~/.sift/logs/daemon.log by spawn-redirection; if that file has
	// grown unboundedly the operator should know. We don't truncate or
	// rotate here — surfacing the size keeps blast radius minimal.
	if logPath, err := config.DaemonLogPath(); err == nil {
		if info, statErr := os.Stat(logPath); statErr == nil && info.Size() > daemonLogSizeWarnBytes {
			logger.Warn("daemon log file is large; consider rotating",
				slog.String("path", logPath),
				slog.String("dir", filepath.Dir(logPath)),
				slog.Int64("size_bytes", info.Size()),
				slog.Int64("threshold_bytes", daemonLogSizeWarnBytes),
			)
		}
	}

	cfg, err := config.Load()
	if err != nil {
		logger.Error("load config", slog.Any("err", err))
		os.Exit(1)
	}

	if err := daemon.Serve(ctx, cfg); err != nil {
		logger.Error("serve", slog.Any("err", err))
		os.Exit(1)
	}

	logger.Info("daemon shim exited cleanly")
	os.Exit(0)
}
