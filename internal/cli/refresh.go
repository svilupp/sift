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

	"sift/internal/aigen"
	"sift/internal/bm25"
	"sift/internal/chunk"
	"sift/internal/config"
	"sift/internal/daemon"
	"sift/internal/db"
	"sift/internal/index"
	siftlog "sift/internal/log"
	"sift/internal/readsignal"
	"sift/internal/sync"
	"sift/internal/voyage"
)

// refreshFlags bundles every flag value the refresh command supports.
type refreshFlags struct {
	collection  string
	full        bool
	dryRun      bool
	noIndex     bool
	indexOnly   bool
	generate    string
	concurrency int
	progress    string
	detach      bool
	status      bool
	apiKey      string
}

func newRefreshCmd() *cobra.Command {
	flags := &refreshFlags{}

	cmd := &cobra.Command{
		Use:   "refresh [files...]",
		Short: "Incremental index refresh",
		Long: `Scan collection folders, detect new/changed/deleted files, chunk text,
generate embeddings (if API key set), and update the BM25 + vector indexes.
Refresh also maintains per-folder sift.toml metadata in a mechanical pass
(no LLM): adds/removes file entries and bumps signatures so summaries can
be flagged stale. LLM-generated purpose/use_when/summary text is opt-in.

By default only processes files changed since last refresh (uses content hash).
Use --full to reindex everything from scratch.
Pass file paths as arguments to refresh specific files only.

Folder-index maintenance (Phase-3, mechanical, always-on by default):
  --no-index            skip sift.toml maintenance entirely.
  --index-only          skip chunk/embed; run only sift.toml maintenance.

AI summary generation (Phase-5, opt-in, requires DEEPINFRA_API_KEY):
  --generate=MODE       missing|stale|all|none. Default 'none' (no LLM calls).
                        missing fills empty fields; stale refreshes drifted
                        entries; all rewrites everything; none skips the LLM.
  --concurrency N       parallel folder workers (default 20).
  --progress=MODE       auto|json|none. 'json' emits NDJSON events to stdout.
  --detach              fork to background; use --status to inspect.
  --status              print last heartbeat from background run, then exit.
  --api-key KEY         override DEEPINFRA_API_KEY for this run.

Failed LLM calls are recorded in dead_letters with kind='index_summary'
and can be re-queued via "sift config retry-dead-letters".

Examples:
  sift refresh                                    # incremental, all collections
  sift refresh -c vault                           # just one collection
  sift refresh notes.md ideas.md                  # refresh specific files
  sift refresh --full                             # reindex everything
  sift refresh --dry-run                          # show what would change
  sift refresh --no-index                         # skip sift.toml maintenance
  sift refresh --index-only                       # only update sift.toml signatures
  sift refresh --index-only --generate=missing    # AI-bootstrap sift.toml
  sift refresh --index-only --generate=stale --progress=json
  sift refresh --index-only --generate=all --detach   # fire-and-forget
  sift refresh --status                           # check on a detached run`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if flags.status {
				return runRefreshStatus(cmd)
			}
			if len(args) > 0 && flags.collection != "" {
				return fmt.Errorf("cannot use --collection with file arguments")
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if flags.detach {
				return runRefreshDetach(cmd)
			}

			return runRefreshOrchestrated(cmd, args, flags, cfg)
		},
	}

	cmd.Flags().StringVarP(&flags.collection, "collection", "c", "", "Refresh specific collection")
	cmd.Flags().BoolVar(&flags.full, "full", false, "Force full reindex")
	cmd.Flags().BoolVar(&flags.dryRun, "dry-run", false, "Show what would be indexed")
	cmd.Flags().BoolVar(&flags.noIndex, "no-index", false, "Skip sift.toml folder-index maintenance")
	cmd.Flags().BoolVar(&flags.indexOnly, "index-only", false, "Skip chunk/embed; run only folder-index maintenance")
	cmd.Flags().StringVar(&flags.generate, "generate", "none", "LLM mode: missing|stale|all|none (opt-in)")
	cmd.Flags().IntVar(&flags.concurrency, "concurrency", 20, "Parallel folder workers for LLM generation")
	cmd.Flags().StringVar(&flags.progress, "progress", "auto", "Progress UX: auto|json|none")
	cmd.Flags().BoolVar(&flags.detach, "detach", false, "Fork to background; logs to ~/.sift/")
	cmd.Flags().BoolVar(&flags.status, "status", false, "Print status of a detached run and exit")
	cmd.Flags().StringVar(&flags.apiKey, "api-key", "", "Override DEEPINFRA_API_KEY for this run")

	return cmd
}

// runRefreshStatus reads the refresh-index PID file and reports whether
// a background refresh is running, plus the last few log lines.
func runRefreshStatus(cmd *cobra.Command) error {
	pidPath, err := config.RefreshIndexPIDPath()
	if err != nil {
		return err
	}
	logPath, err := config.RefreshIndexLogPath()
	if err != nil {
		return err
	}
	w := cmd.OutOrStdout()
	pid, err := aigen.ReadPID(pidPath)
	if err != nil {
		fmt.Fprintln(w, "no in-flight refresh (no pid file)")
		return nil
	}
	if !aigen.IsAlive(pid) {
		fmt.Fprintf(w, "stale pid file (pid %d not running)\n", pid)
		return nil
	}
	fmt.Fprintf(w, "refresh pid %d running, log %s\n", pid, logPath)
	tail, _ := tailFile(logPath, 5)
	if tail != "" {
		fmt.Fprintln(w, "  last lines:")
		for _, line := range strings.Split(strings.TrimRight(tail, "\n"), "\n") {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
	return nil
}

// runRefreshDetach forks the current process into the background.
func runRefreshDetach(cmd *cobra.Command) error {
	pidPath, err := config.RefreshIndexPIDPath()
	if err != nil {
		return err
	}
	logPath, err := config.RefreshIndexLogPath()
	if err != nil {
		return err
	}
	if existing, err := aigen.ReadPID(pidPath); err == nil && aigen.IsAlive(existing) {
		return fmt.Errorf("refresh already running (pid %d). Stop it first or use --status", existing)
	}
	pid, err := aigen.Detach(aigen.DetachOptions{
		PIDFile: pidPath,
		LogFile: logPath,
		Args:    os.Args[1:],
	})
	if err != nil {
		return fmt.Errorf("detach: %w", err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "refresh detached (pid %d). Logs: %s\n", pid, logPath)
	return nil
}

// tailFile returns the last n lines of a file (best-effort).
func tailFile(path string, n int) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	lines := make([]string, 0, n+8)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return strings.Join(lines, "\n"), nil
}

// runRefreshOrchestrated picks the daemon path when one is reachable,
// otherwise the in-process path. The daemon path is critical: while a
// daemon is running it holds an exclusive Bleve file lock, so the
// in-process pipeline would block indefinitely on OpenBleve.
//
// AI-generation modes (--generate != none) and --index-only force the
// in-process path: the daemon's protocol does not surface aigen events
// yet. The user-visible behavior is identical otherwise.
func runRefreshOrchestrated(cmd *cobra.Command, files []string, flags *refreshFlags, cfg *config.Config) error {
	logger := slog.With(slog.String("component", "cli"), slog.String("op", "refresh"))

	mode := generationMode(strings.ToLower(strings.TrimSpace(flags.generate)))
	switch mode {
	case "", genNone:
		mode = genNone
	case genMissing, genStale, genAll:
		// ok
	default:
		return fmt.Errorf("invalid --generate value %q (want missing|stale|all|none)", flags.generate)
	}
	flags.generate = string(mode)

	forceInProcess := flags.indexOnly || flags.noIndex || mode != genNone

	if forceInProcess {
		// The in-process path needs exclusive access to the Bleve
		// index files and the sift operation lock. If a daemon is
		// running it already holds those, so opening Bleve would
		// block indefinitely. Detect and fail fast with a clear,
		// actionable message instead of hanging.
		if err := guardAgainstRunningDaemon(cmd, flags); err != nil {
			return err
		}
		return runRefreshInProcess(cmd, files, flags, cfg)
	}

	if os.Getenv("SIFT_NO_DAEMON") != "" {
		logger.Debug("daemon disabled via SIFT_NO_DAEMON, using in-process path")
		return runRefreshInProcess(cmd, files, flags, cfg)
	}
	if cfg != nil && !cfg.Daemon.Enabled {
		logger.Debug("daemon disabled via cfg.Daemon.Enabled=false, using in-process path")
		return runRefreshInProcess(cmd, files, flags, cfg)
	}

	dialTimeout := 50 * time.Millisecond
	if cfg != nil {
		if d := cfg.Daemon.DialTimeout.D(); d > 0 {
			dialTimeout = d
		}
	}

	client, err := tryDial(dialTimeout)
	if err != nil {
		logger.Debug("daemon not reachable, using in-process path", slog.Any("err", err))
		return runRefreshInProcess(cmd, files, flags, cfg)
	}
	defer client.Close()

	if rerr := refreshViaDaemon(cmd, files, flags.collection, flags.full, flags.dryRun, flags.noIndex, client); rerr != nil {
		logger.Warn("daemon refresh failed", slog.Any("err", rerr))
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: daemon refresh failed: %v\n", rerr)
		fmt.Fprintln(cmd.ErrOrStderr(), "         Stop the daemon (`sift daemon stop`) and retry, or set SIFT_NO_DAEMON=1.")
		return rerr
	}
	return nil
}

// guardAgainstRunningDaemon returns a friendly, actionable error when
// the daemon is healthy and the user requested a refresh mode that
// must run in-process (--index-only, --no-index, --generate=*).
//
// Both the daemon and the in-process pipeline acquire the same Bleve
// directory and sift operation lock; running them concurrently would
// hang the in-process side on the Bleve open. We fail fast here with
// instructions instead.
func guardAgainstRunningDaemon(cmd *cobra.Command, flags *refreshFlags) error {
	if os.Getenv("SIFT_NO_DAEMON") != "" {
		// User has opted out; daemon should not be running, but if it
		// is the user has chosen to live with whatever happens.
		return nil
	}
	report := daemon.GetStatus(50 * time.Millisecond)
	if report.State != daemon.StatusRunning {
		return nil
	}
	reason := inProcessReason(flags)
	fmt.Fprintf(cmd.ErrOrStderr(),
		"Error: cannot acquire refresh lock — the sift daemon (pid %d) is holding it.\n"+
			"       %s requires in-process mode, which conflicts with a running daemon.\n"+
			"       Stop the daemon first:\n"+
			"           sift daemon stop\n"+
			"       Then retry, or run with SIFT_NO_DAEMON=1 set.\n",
		report.PID, reason,
	)
	return fmt.Errorf("daemon running: in-process refresh mode (%s) cannot proceed", reason)
}

// decorateLockError wraps an acquireLock failure with daemon-aware
// context. If a daemon is running we point the user at it; otherwise
// we return the original error.
func decorateLockError(err error, flags *refreshFlags) error {
	if !errors.Is(err, errLockHeld) {
		return err
	}
	if os.Getenv("SIFT_NO_DAEMON") != "" {
		return err
	}
	report := daemon.GetStatus(50 * time.Millisecond)
	if report.State != daemon.StatusRunning {
		return err
	}
	reason := inProcessReason(flags)
	return fmt.Errorf(
		"the sift daemon (pid %d) is holding the refresh lock. %s requires in-process mode; "+
			"run `sift daemon stop` and retry, or set SIFT_NO_DAEMON=1",
		report.PID, reason,
	)
}

// inProcessReason returns a short human label describing which flag
// forced the in-process path. The list mirrors runRefreshOrchestrated.
func inProcessReason(flags *refreshFlags) string {
	switch {
	case flags.indexOnly:
		return "--index-only"
	case flags.noIndex:
		return "--no-index"
	case flags.generate != "" && flags.generate != string(genNone):
		return "--generate=" + flags.generate
	default:
		return "this command"
	}
}

// refreshViaDaemon issues POST /refresh and prints the human-readable
// Message field from each ProgressEvent. Errors emitted as events are
// surfaced as a returned error so the caller can warn the user.
func refreshViaDaemon(cmd *cobra.Command, files []string, collection string, full, dryRun, noIndex bool, client *daemon.Client) error {
	req := daemon.RefreshRequest{
		Collection: collection,
		Full:       full,
		DryRun:     dryRun,
		NoIndex:    noIndex,
		Files:      files,
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}

	pr, pw := io.Pipe()
	out := cmd.OutOrStdout()
	errOut := cmd.ErrOrStderr()

	parseDone := make(chan error, 1)
	var streamErr error
	go func() {
		scanner := bufio.NewScanner(pr)
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		for scanner.Scan() {
			line := scanner.Bytes()
			if len(line) == 0 {
				continue
			}
			var ev daemon.ProgressEvent
			if err := json.Unmarshal(line, &ev); err != nil {
				fmt.Fprintln(out, scanner.Text())
				continue
			}
			if ev.Error != "" {
				fmt.Fprintf(errOut, "Error: %s\n", ev.Error)
				if ev.Phase == "error" {
					streamErr = fmt.Errorf("%s", ev.Error)
				}
				continue
			}
			if ev.Message != "" {
				fmt.Fprintln(out, ev.Message)
			}
		}
		parseDone <- scanner.Err()
	}()

	clientErr := client.Refresh(ctx, req, pw)
	_ = pw.Close()
	scanErr := <-parseDone

	if clientErr != nil {
		return clientErr
	}
	if streamErr != nil {
		return streamErr
	}
	if scanErr != nil {
		return fmt.Errorf("read refresh stream: %w", scanErr)
	}
	return nil
}

// runRefreshInProcess runs the original in-process refresh pipeline. It
// is used when the daemon is opted-out, unreachable, or has explicitly
// failed — and always when --index-only or --generate != none, since
// those features need direct access to the aigen pool.
func runRefreshInProcess(cmd *cobra.Command, args []string, flags *refreshFlags, cfg *config.Config) error {
	collection := flags.collection
	full := flags.full
	dryRun := flags.dryRun
	noIndex := flags.noIndex
	indexOnly := flags.indexOnly
	mode := generationMode(flags.generate)
	fl, err := acquireLock()
	if err != nil {
		return decorateLockError(err, flags)
	}
	defer func() { _ = fl.Unlock() }()

	database, err := openDB()
	if err != nil {
		return err
	}
	defer database.Close()

	blevePath, err := config.BlevePath()
	if err != nil {
		return err
	}
	bleveIdx, err := bm25.OpenBleve(blevePath, cfg.BM25.Analyzer)
	if err != nil {
		return err
	}
	defer bleveIdx.Close()

	var voyageClient *voyage.Client
	if cfg.API.VoyageAPIKey != "" {
		voyageClient = voyage.NewClientWithTransport(cfg.API.VoyageAPIKey, "", voyage.TransportConfig{
			MaxIdleConns:          cfg.Transport.MaxIdleConns,
			MaxIdleConnsPerHost:   cfg.Transport.MaxIdleConnsPerHost,
			MaxConnsPerHost:       cfg.Transport.MaxConnsPerHost,
			IdleConnTimeout:       cfg.Transport.IdleConnTimeout.D(),
			TLSHandshakeTimeout:   cfg.Transport.TLSHandshakeTimeout.D(),
			ResponseHeaderTimeout: cfg.Transport.ResponseHeaderTimeout.D(),
		})
		voyageClient.EmbedModel = cfg.Embedding.Model
		voyageClient.EmbedDimensions = cfg.Embedding.Dimensions
		voyageClient.EmbedDtype = cfg.Embedding.OutputDtype
		voyageClient.RerankModel = cfg.Reranking.Model
		if cfg.API.RequestTimeoutSecs > 0 {
			voyageClient.SetTimeout(time.Duration(cfg.API.RequestTimeoutSecs) * time.Second)
		}
		ctx := cmd.Context()
		go func() {
			if err := voyageClient.Preconnect(ctx); err != nil {
				slog.Warn("voyage preconnect failed", slog.String("component", "cli"), slog.String("op", "refresh"), slog.Any("err", err))
			}
		}()
	}

	sectionOpts := chunk.SectionOptions{
		MaxSectionChars: cfg.Chunking.MaxSectionChars,
		MinSectionChars: cfg.Chunking.MinSectionChars,
		OverlapLines:    cfg.Chunking.OverlapRows,
	}

	var pending []pendingFolder
	indexHook := makeIndexHook(mode, &pending)

	opts := sync.RefreshOptions{
		CollectionName: collection,
		Full:           full,
		DryRun:         dryRun,
		NoIndex:        noIndex,
		IndexOnly:      indexOnly,
		BatchSize:      cfg.Embedding.BatchSize,
		ChunkOpts: chunk.Options{
			RowsPerChunk:  cfg.Chunking.RowsPerChunk,
			OverlapRows:   cfg.Chunking.OverlapRows,
			MinChunkChars: cfg.Chunking.MinChunkChars,
			SkipEmptyRows: cfg.Chunking.SkipEmptyRows,
		},
		SectionOpts: sectionOpts,
		ChunkMode:   cfg.Chunking.Mode,
		IndexHook:   indexHook,
	}

	w := cmd.OutOrStdout()

	// Wire SIGINT/SIGTERM to a cancelable context so the aigen pool
	// can drain gracefully.
	baseCtx := cmd.Context()
	if baseCtx == nil {
		baseCtx = context.Background()
	}
	runCtx, cancel := context.WithCancel(baseCtx)
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigCh)
	go func() {
		select {
		case <-sigCh:
			fmt.Fprintln(cmd.ErrOrStderr(), "received interrupt; draining…")
			cancel()
		case <-runCtx.Done():
		}
	}()

	var stats *sync.RefreshStats
	if len(args) > 0 {
		stats, err = sync.RefreshFiles(runCtx, database, bleveIdx, voyageClient, args, opts, w)
	} else {
		fmt.Fprintln(w, "Refreshing...")
		stats, err = sync.Refresh(runCtx, database, bleveIdx, voyageClient, opts, w)
	}
	if err != nil {
		return err
	}

	if !dryRun {
		backlinkDocs, backlinkErr := database.RefreshBacklinkCounts()
		if backlinkErr != nil {
			return fmt.Errorf("refresh backlink counts: %w", backlinkErr)
		}
		if backlinkDocs > 0 {
			fmt.Fprintf(w, "  Backlinks: %d docs updated\n", backlinkDocs)
		}

		if cfg.Scoring.ReadSignalEnabled {
			collections, colErr := refreshCollectionsForSignals(database, collection)
			if colErr != nil {
				return colErr
			}
			records, sources, readErr := readsignal.LoadReadSignals(collections, cfg.Scoring.ReadSignalPath, cfg.Scoring.ReadSignalDays)
			if readErr != nil {
				fmt.Fprintf(w, "  Warning: read-signal import: %v\n", readErr)
			} else if replaceErr := database.ReplaceReadCounts(records); replaceErr != nil {
				return fmt.Errorf("store read counts: %w", replaceErr)
			} else if len(sources) > 0 {
				fmt.Fprintf(w, "  Read signals: %d docs from %d export(s)\n", len(records), len(sources))
			}
		}
	}

	if cfg.Cache.Enabled {
		_ = database.ClearCache()
	}

	// AI generation pass (Phase-5). Mode is gated to non-`none` and
	// dryRun is honored — generation never writes during a dry run.
	if mode != genNone && !dryRun && len(pending) > 0 {
		apiKey := strings.TrimSpace(flags.apiKey)
		if apiKey == "" {
			collectionRoot := pickCollectionRoot(database, collection)
			apiKey = aigen.LoadAPIKeyWithFallback(collectionRoot, "", cfg.API.DeepInfraAPIKey)
		}
		if apiKey == "" {
			// Persist as dead letters — caller can resume via
			// `sift config retry-dead-letters`.
			recordIndexDeadLetters(database, pending, "missing DEEPINFRA_API_KEY")
			fmt.Fprintln(w, "Warning: --generate set but no DEEPINFRA_API_KEY; folders dead-lettered.")
		} else {
			collRoot := pickCollectionRoot(database, collection)
			if err := runAigenForPlans(runCtx, pending, aigenRunOptions{
				Mode:           mode,
				Concurrency:    flags.concurrency,
				APIKey:         apiKey,
				Model:          "",
				Database:       database,
				CollectionRoot: collRoot,
				CollectionName: collection,
				Progress:       flags.progress,
				Out:            w,
				Errs:           cmd.ErrOrStderr(),
			}); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: aigen run: %v\n", err)
			}
		}
	}

	if len(args) > 0 {
		fmt.Fprintf(w, "Done: %d new, %d changed, %d chunks in %s\n",
			stats.FilesNew, stats.FilesChanged,
			stats.ChunksTotal, stats.Duration.Round(time.Millisecond))
	} else {
		fmt.Fprintf(w, "Done: %d files scanned, %d new, %d changed, %d deleted, %d chunks in %s\n",
			stats.FilesScanned, stats.FilesNew, stats.FilesChanged, stats.FilesDeleted,
			stats.ChunksTotal, stats.Duration.Round(time.Millisecond))
	}

	if stats.ChunksEmbedded > 0 {
		fmt.Fprintf(w, "  Embeddings: %d embedded, %d errors, %d tokens\n",
			stats.ChunksEmbedded, stats.EmbedErrors, stats.EmbedTokens)
	}

	logDir, logErr := config.LogDir()
	if logErr == nil {
		logger := siftlog.NewLogger(logDir, cfg.Logs.MaxWeeks)
		if cleanErr := logger.Cleanup(); cleanErr != nil {
			fmt.Fprintf(w, "Warning: log cleanup: %v\n", cleanErr)
		}
	}

	return nil
}

// pickCollectionRoot returns the root path for a single named
// collection or the parent of the first one when name is empty.
func pickCollectionRoot(database *db.DB, name string) string {
	if database == nil {
		return ""
	}
	if name != "" {
		col, err := database.GetCollection(name)
		if err == nil && col != nil {
			return col.Path
		}
	}
	cols, err := database.ListCollections()
	if err != nil || len(cols) == 0 {
		return ""
	}
	return cols[0].Path
}

// recordIndexDeadLetters persists one row per pending folder so the
// retry-dead-letters command can resume them later.
func recordIndexDeadLetters(database *db.DB, pending []pendingFolder, errMsg string) {
	if database == nil {
		return
	}
	for _, pf := range pending {
		if err := database.InsertIndexSummaryDeadLetter(pf.folder, "", errMsg); err != nil {
			slog.Warn("dead letter insert failed",
				slog.String("folder", pf.folder),
				slog.String("err", err.Error()))
		}
	}
}

// _ keeps the index import live: helper functions reference index.*
// types via cli/aigen.go.
var _ = index.SchemaVersion

func refreshCollectionsForSignals(database interface {
	ListCollections() ([]db.Collection, error)
	GetCollection(string) (*db.Collection, error)
}, collection string) ([]db.Collection, error) {
	if collection == "" {
		cols, err := database.ListCollections()
		if err != nil {
			return nil, fmt.Errorf("list collections for read signals: %w", err)
		}
		return cols, nil
	}

	col, err := database.GetCollection(collection)
	if err != nil {
		return nil, fmt.Errorf("load collection %q for read signals: %w", collection, err)
	}
	if col == nil {
		return nil, fmt.Errorf("collection %q not found", collection)
	}
	return []db.Collection{*col}, nil
}
