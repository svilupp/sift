// Package daemon implements the SIFT background daemon: a long-lived
// process that serves search and refresh requests over an HTTP-on-Unix-
// socket transport. The daemon amortises model-load and TLS-handshake
// costs across CLI invocations.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/gofrs/flock"

	"sift/internal/bm25"
	"sift/internal/config"
	"sift/internal/db"
	"sift/internal/search"
	"sift/internal/voyage"
)

// shutdownGracePeriod is the deadline for in-flight requests to drain
// once srv.Shutdown is called.
const shutdownGracePeriod = 10 * time.Second

// SearchEngine is the subset of *search.Engine the daemon depends on.
// Defining it as an interface makes the handlers unit-testable without
// constructing a real engine (which requires DB + Bleve + Voyage).
type SearchEngine interface {
	Search(ctx context.Context, query string, opts search.SearchOptions) (*search.SearchResult, error)
}

// Serve runs the daemon HTTP server bound to a Unix socket. It returns
// once the server has shut down, either due to context cancellation, a
// signal (SIGINT/SIGTERM), an explicit /shutdown request, or idle
// timeout.
//
// Serve is single-instance: a flock on the PID file prevents two
// daemons from racing for the same socket.
func Serve(ctx context.Context, cfg *config.Config) error {
	logger := slog.Default().With(slog.String("component", "daemon"))

	// 1) Resolve filesystem paths up front so all error messages are precise.
	pidPath, err := config.PIDPath()
	if err != nil {
		logger.Error("resolve pid path", slog.Any("err", err))
		return fmt.Errorf("resolve pid path: %w", err)
	}
	socketPath, err := config.SocketPath()
	if err != nil {
		logger.Error("resolve socket path", slog.Any("err", err))
		return fmt.Errorf("resolve socket path: %w", err)
	}
	dbPath, err := config.DBPath()
	if err != nil {
		logger.Error("resolve db path", slog.Any("err", err))
		return fmt.Errorf("resolve db path: %w", err)
	}
	blevePath, err := config.BlevePath()
	if err != nil {
		logger.Error("resolve bleve path", slog.Any("err", err))
		return fmt.Errorf("resolve bleve path: %w", err)
	}

	// Ensure parent dir exists for socket/pid file.
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		logger.Error("create sift dir", slog.String("path", filepath.Dir(socketPath)), slog.Any("err", err))
		return fmt.Errorf("create sift dir: %w", err)
	}

	// 2) Single-instance lock via flock on the PID file.
	fl := flock.New(pidPath)
	locked, err := fl.TryLock()
	if err != nil {
		logger.Error("acquire daemon lock", slog.String("path", pidPath), slog.Any("err", err))
		return fmt.Errorf("acquire daemon lock %s: %w", pidPath, err)
	}
	if !locked {
		logger.Error("daemon already running", slog.String("pid_file", pidPath))
		return fmt.Errorf("daemon already running (pid file: %s)", pidPath)
	}
	defer func() {
		if err := fl.Unlock(); err != nil {
			logger.Warn("unlock daemon lock", slog.String("path", pidPath), slog.Any("err", err))
		}
	}()

	// 3) Write PID file.
	pid := os.Getpid()
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(pid)), 0o600); err != nil {
		logger.Error("write pid file", slog.String("path", pidPath), slog.Any("err", err))
		return fmt.Errorf("write pid file: %w", err)
	}
	defer func() {
		if err := os.Remove(pidPath); err != nil && !os.IsNotExist(err) {
			logger.Warn("remove pid file", slog.String("path", pidPath), slog.Any("err", err))
		}
	}()

	// 4) Open DB.
	database, err := db.Open(dbPath)
	if err != nil {
		logger.Error("open db", slog.String("path", dbPath), slog.Any("err", err))
		return fmt.Errorf("open db: %w", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			logger.Warn("close db", slog.Any("err", err))
		}
	}()

	// 5) Open Bleve.
	bleveIdx, err := bm25.OpenBleve(blevePath, cfg.BM25.Analyzer)
	if err != nil {
		logger.Error("open bleve", slog.String("path", blevePath), slog.Any("err", err))
		return fmt.Errorf("open bleve: %w", err)
	}
	defer func() {
		if err := bleveIdx.Close(); err != nil {
			logger.Warn("close bleve", slog.Any("err", err))
		}
	}()

	// 6) Construct Voyage client and warm TLS.
	voyageClient := voyage.NewClientWithTransport(cfg.API.VoyageAPIKey, "", voyage.TransportConfig{
		MaxIdleConns:          cfg.Transport.MaxIdleConns,
		MaxIdleConnsPerHost:   cfg.Transport.MaxIdleConnsPerHost,
		MaxConnsPerHost:       cfg.Transport.MaxConnsPerHost,
		IdleConnTimeout:       cfg.Transport.IdleConnTimeout.D(),
		TLSHandshakeTimeout:   cfg.Transport.TLSHandshakeTimeout.D(),
		ResponseHeaderTimeout: cfg.Transport.ResponseHeaderTimeout.D(),
	})
	if cfg.Embedding.Model != "" {
		voyageClient.EmbedModel = cfg.Embedding.Model
	}
	if cfg.Embedding.Dimensions > 0 {
		voyageClient.EmbedDimensions = cfg.Embedding.Dimensions
	}
	if cfg.Embedding.OutputDtype != "" {
		voyageClient.EmbedDtype = cfg.Embedding.OutputDtype
	}
	if cfg.Reranking.Model != "" {
		voyageClient.RerankModel = cfg.Reranking.Model
	}
	if cfg.API.RequestTimeoutSecs > 0 {
		voyageClient.SetTimeout(time.Duration(cfg.API.RequestTimeoutSecs) * time.Second)
	}

	// Preconnect: only meaningful when an API key is configured. Skip
	// entirely when the key is empty so we don't dial real Voyage from
	// tests or unconfigured installs.
	var preconnectWG sync.WaitGroup
	if cfg.API.VoyageAPIKey != "" {
		preconnectWG.Add(1)
		go func() {
			defer preconnectWG.Done()
			if err := voyageClient.Preconnect(ctx); err != nil {
				logger.Warn("voyage preconnect", slog.Any("err", err))
			}
		}()
	}

	// 7) Search engine.
	engine := search.NewEngine(database, bleveIdx, voyageClient, cfg)

	// 8) Bind Unix socket; clean up stale socket file if present.
	if _, err := os.Stat(socketPath); err == nil {
		logger.Info("removing stale socket", slog.String("path", socketPath))
		if err := os.Remove(socketPath); err != nil {
			logger.Error("remove stale socket", slog.String("path", socketPath), slog.Any("err", err))
			return fmt.Errorf("remove stale socket: %w", err)
		}
	} else if !os.IsNotExist(err) {
		logger.Error("stat socket", slog.String("path", socketPath), slog.Any("err", err))
		return fmt.Errorf("stat socket: %w", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		logger.Error("listen unix", slog.String("path", socketPath), slog.Any("err", err))
		return fmt.Errorf("listen unix %s: %w", socketPath, err)
	}
	if err := os.Chmod(socketPath, 0o600); err != nil {
		logger.Warn("chmod socket", slog.String("path", socketPath), slog.Any("err", err))
	}
	defer func() {
		if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
			logger.Warn("remove socket file", slog.String("path", socketPath), slog.Any("err", err))
		}
	}()

	// 9) Build server with all middleware.
	var requestCount atomic.Int64
	var inFlight atomic.Int64
	startedAt := time.Now()

	// Shutdown coordination: shutdownCh is closed by either /shutdown,
	// the idle timer, or an OS signal. A sync.Once guards both the
	// channel close and the cause store so we get exactly-once
	// first-writer-wins semantics with zero risk of close-of-closed
	// channel panics.
	shutdownCh := make(chan struct{})
	var shutdownOnce sync.Once
	var shutdownCause atomic.Value // string
	signalShutdown := func(cause string) {
		shutdownOnce.Do(func() {
			shutdownCause.Store(cause)
			close(shutdownCh)
		})
	}

	idle := newIdleTimer(cfg.Daemon.IdleTimeoutDuration(), func() {
		logger.Info("idle timeout reached, signalling shutdown")
		signalShutdown("idle")
	})
	defer idle.Stop()

	// Resolve daemon log path for /health (best-effort; empty on error).
	daemonLogPath, _ := config.DaemonLogPath()

	deps := Dependencies{
		Engine:        engine,
		Voyage:        voyageClient,
		DB:            database,
		BleveIdx:      bleveIdx,
		Cfg:           cfg,
		Version:       "dev",
		StartedAt:     startedAt,
		RequestCount:  &requestCount,
		InFlight:      &inFlight,
		SocketPath:    socketPath,
		DaemonLogPath: daemonLogPath,
		IdleTimeout:   cfg.Daemon.IdleTimeoutDuration(),
		ShutdownFn: func() {
			signalShutdown("request")
		},
		Logger: logger,
	}
	handlers := newHandlers(deps)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", handlers.Health)
	mux.HandleFunc("POST /search", handlers.Search)
	mux.HandleFunc("POST /refresh", handlers.Refresh)
	mux.HandleFunc("POST /shutdown", handlers.Shutdown)

	wrapped := wrapWithIdleReset(
		wrapWithInFlight(
			wrapWithRequestCount(
				wrapWithAccessLog(mux, logger),
				&requestCount,
			),
			&inFlight,
		),
		idle,
	)

	srv := &http.Server{
		Handler:           wrapped,
		ReadHeaderTimeout: 10 * time.Second,
	}

	// 10) Signal handling: convert SIGINT/SIGTERM to a shutdown signal.
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)

	go func() {
		select {
		case s := <-sigCh:
			logger.Info("signal received", slog.String("signal", s.String()))
			signalShutdown("signal:" + s.String())
		case <-ctx.Done():
			logger.Info("context done", slog.Any("err", ctx.Err()))
			signalShutdown("context")
		case <-shutdownCh:
			// shutdown was triggered some other way
		}
	}()

	// 11) When shutdownCh closes, gracefully shutdown the server.
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-shutdownCh
		cause, _ := shutdownCause.Load().(string)
		logger.Info("shutting down", slog.String("cause", cause))
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGracePeriod)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			logger.Warn("server shutdown", slog.Any("err", err))
		}
	}()

	logger.Info("daemon listening",
		slog.String("socket", socketPath),
		slog.Int("pid", pid),
		slog.String("idle_timeout", cfg.Daemon.IdleTimeout.String()),
	)

	// 12) Serve.
	serveErr := srv.Serve(listener)
	if errors.Is(serveErr, http.ErrServerClosed) {
		serveErr = nil
	}
	// Make sure shutdown goroutine has finished cleanup (it may still be
	// awaiting srv.Shutdown). Trigger it if Serve exited unexpectedly.
	signalShutdown("serve-exit")
	<-shutdownDone

	// Wait for the Preconnect goroutine to finish so we don't return
	// from Serve while a background dial is still in flight. Use a 2s
	// deadline so a hung Voyage dial cannot block daemon shutdown
	// indefinitely.
	preconnectDone := make(chan struct{})
	go func() {
		preconnectWG.Wait()
		close(preconnectDone)
	}()
	select {
	case <-preconnectDone:
	case <-time.After(2 * time.Second):
		logger.Warn("preconnect goroutine did not finish within 2s; proceeding")
	}

	if serveErr != nil {
		logger.Error("serve", slog.Any("err", serveErr))
		return fmt.Errorf("serve: %w", serveErr)
	}
	logger.Info("daemon exited cleanly")
	return nil
}

// wrapWithRequestCount increments the shared counter on every request
// before delegating to next. Counts are observable via /health.
func wrapWithRequestCount(next http.Handler, c *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c.Add(1)
		next.ServeHTTP(w, r)
	})
}

// wrapWithInFlight increments inFlight on entry and decrements on exit
// so /health can report the live in-flight count.
func wrapWithInFlight(next http.Handler, inFlight *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inFlight.Add(1)
		defer inFlight.Add(-1)
		next.ServeHTTP(w, r)
	})
}

// statusRecorder captures the status code written by a handler so the
// access-log middleware can record it.
type statusRecorder struct {
	http.ResponseWriter
	status int
	wrote  bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wrote {
		s.status = code
		s.wrote = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	if !s.wrote {
		s.status = http.StatusOK
		s.wrote = true
	}
	return s.ResponseWriter.Write(b)
}

// Flush implements http.Flusher when the underlying writer supports it.
// Required so streaming endpoints (/refresh) can flush NDJSON lines.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// wrapWithAccessLog logs one slog line per request with route, status,
// and elapsed time. Errors are surfaced via the recorded status.
func wrapWithAccessLog(next http.Handler, logger *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger.Info("request",
			slog.String("route", r.Method+" "+r.URL.Path),
			slog.Int("status", rec.status),
			slog.Int64("dur_ms", time.Since(start).Milliseconds()),
		)
	})
}

// Compile-time guard: ensure *search.Engine satisfies SearchEngine.
var _ SearchEngine = (*search.Engine)(nil)
