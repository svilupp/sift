package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"sift/internal/bm25"
	"sift/internal/cache"
	"sift/internal/config"
	"sift/internal/daemon"
	"sift/internal/db"
	"sift/internal/fileutil"
	siftlog "sift/internal/log"
	"sift/internal/search"
	"sift/internal/voyage"
)

// Package-level function variables for daemon orchestration. Tests
// substitute these to drive runSearch through specific code paths
// (warm dial, auto-spawn, all-fail) without standing up a real daemon.
var (
	tryDial           = daemon.TryDial
	spawnDetached     = daemon.SpawnDetached
	dialUntilReady    = daemon.DialUntilReady
	searchViaDaemonFn = func(cmd *cobra.Command, f *searchFlags, client *daemon.Client) error {
		return searchViaDaemon(cmd, f, client)
	}
	runSearchInProcessFn = func(cmd *cobra.Command, f *searchFlags) error {
		return runSearchInProcess(cmd, f)
	}
)

type scoreComponents struct {
	BM25Rank   int     `json:"bm25_rank"`
	VectorRank int     `json:"vector_rank"`
	RRF        float64 `json:"rrf"`
	Rerank     float64 `json:"rerank"`
	Final      float64 `json:"final"`
}

type searchResult struct {
	Index           string                   `json:"index"`
	ChunkID         int64                    `json:"chunk_id"`
	File            string                   `json:"file"`
	Collection      string                   `json:"collection"`
	StartLine       int                      `json:"start_line"`
	EndLine         int                      `json:"end_line"`
	Content         string                   `json:"content"`
	Stale           bool                     `json:"stale,omitempty"`
	Score           float64                  `json:"score"`
	OpenCmd         string                   `json:"open_cmd"`
	Highlights      []string                 `json:"highlights,omitempty"`
	Components      *scoreComponents         `json:"components,omitempty"`
	Duplicates      []search.DuplicateRef    `json:"duplicates,omitempty"`
	Section         string                   `json:"section,omitempty"`
	Heading         string                   `json:"heading,omitempty"`
	HeadingLevel    int                      `json:"heading_level,omitempty"`
	SectionChars    int                      `json:"section_char_count,omitempty"`
	SubsectionCount int                      `json:"subsection_count,omitempty"`
	Siblings        []search.NeighborhoodRef `json:"siblings,omitempty"`
	Related         []search.NeighborhoodRef `json:"related,omitempty"`
	Links           []search.LinkRef         `json:"links,omitempty"`
	IndexAnnotation *search.IndexAnnotation  `json:"folder_index,omitempty"`
}

func newSearchCmd() *cobra.Command {
	var (
		collection  string
		since       string
		pathGlob    string
		topK        int
		agent       bool
		compact     bool
		jsonOutput  bool
		filesOutput bool
		pretty      bool
		reverse     bool
		threshold   float64
		noTips      bool
		readCommand string
		files       []string
		sections    bool
		withIndex   bool
		noIndex     bool
	)

	cmd := &cobra.Command{
		Use:   "search <query words...>",
		Short: "Search collections or individual files",
		Long: `Search always uses the local BM25 index. When an API key is configured,
Sift also uses vector embeddings and reranking; without a key it remains BM25-only.

Results are labeled a-z. Use these labels with "sift feedback" to improve
future rankings. The search_id in the output header identifies the session.

Output modes:
  (default)   ranked previews with scores
  --agent     agent-optimized section results with no numeric scores
  --compact   compact agent output grouped by file/section
  --json      Machine-readable: full JSON with scores, components, metadata, feedback_cmd
  --files     Just unique file paths, one per line (good for piping)
  --pretty    Human-readable: editor commands, half the default results, --reverse on

--reverse shows best results last (bottom of terminal). Enabled by default with --pretty.
Use --no-tips to suppress the trailing tip line in default/pretty output.

Folder index decoration (--with-index / --no-index):
  Each result is decorated with its enclosing folder's purpose/use_when from
  sift.toml (when present). Default ON in both human and JSON modes.
  Human mode appends a grey "[folder: ...]" suffix; JSON adds a "folder_index"
  field. Use --no-index to strip the decoration entirely.

File search (--file):
  Search within specific files at line granularity instead of indexed collections.
  Uses an ephemeral BM25 index — no database or API calls needed.
  Supports multiple files searched in parallel with cross-file ranking.

  sift search "store id" --file INFRASTRUCTURE.md
  sift search "auth flow" --file api.md --file auth.md --pretty
  cat template.md | sift search "cache" --file -

Examples:
  sift search "concurrent write limitation"
  sift search concurrent write limitation        # quoting is optional
  sift search "How does the agent handle rate limiting?" --collection vault
  sift search "authentication flow" --since 2w --top-k 5
  sift search "API design" --path "*/topics/work/*"
  sift search "ingestion API proof of concept" --json
  sift search "concurrent write limitation" --agent
  sift search "concurrent write limitation" --agent --read-command "mem read"
  sift search "storage layer" --compact
  sift search "rate limiting" --files | xargs cat
  sift search "store id" --file docs/INFRASTRUCTURE.md
  sift search "auth" --file a.md --file b.md --json
  sift search "store id" --json | jq '.results[0].folder_index'   # show decoration
  sift search "store id" --no-index                               # strip decoration

Longer, descriptive queries produce better semantic matches than single keywords.
Multi-keyword natural-language queries are the intended usage pattern.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			query := strings.Join(args, " ")
			start := time.Now()

			if compact {
				agent = true
			}
			if agent && (jsonOutput || filesOutput || pretty) {
				return fmt.Errorf("--agent/--compact cannot be combined with --json, --files, or --pretty")
			}
			if agent && reverse {
				return fmt.Errorf("--reverse is not supported with --agent/--compact")
			}
			if len(files) > 0 {
				if agent {
					return fmt.Errorf("--agent/--compact is not supported with --file")
				}
				return handleFileSearch(cmd, query, files, topK, jsonOutput, filesOutput, pretty, reverse, noTips, threshold, collection, since, pathGlob, start)
			}

			// Resolve --with-index / --no-index. Default ON in both
			// human and JSON modes; --no-index wins if both are set.
			indexEnabled := true
			if cmd.Flags().Changed("with-index") {
				indexEnabled = withIndex
			}
			if cmd.Flags().Changed("no-index") && noIndex {
				indexEnabled = false
			}

			f := &searchFlags{
				query:        query,
				start:        start,
				collection:   collection,
				since:        since,
				pathGlob:     pathGlob,
				topK:         topK,
				agent:        agent,
				compact:      compact,
				jsonOutput:   jsonOutput,
				filesOutput:  filesOutput,
				pretty:       pretty,
				reverse:      reverse,
				threshold:    threshold,
				noTips:       noTips,
				readCommand:  readCommand,
				files:        files,
				sections:     sections,
				indexEnabled: indexEnabled,
			}
			return runSearchOrchestrated(cmd, f)
		},
	}

	cmd.Flags().StringVarP(&collection, "collection", "c", "", "Filter by collection")
	cmd.Flags().StringVarP(&pathGlob, "path", "p", "", "Filter by file path glob (e.g. \"*/topics/work/*\")")
	cmd.Flags().StringVar(&since, "since", "", "Time filter (e.g. 2d, 1w)")
	cmd.Flags().IntVarP(&topK, "top-k", "k", 0, "Number of results")
	cmd.Flags().BoolVar(&agent, "agent", false, "Agent-optimized section output")
	cmd.Flags().BoolVar(&compact, "compact", false, "Compact agent output grouped by file")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "JSON output")
	cmd.Flags().BoolVar(&filesOutput, "files", false, "File paths only")
	cmd.Flags().BoolVar(&pretty, "pretty", false, "Human-readable previews with editor commands")
	cmd.Flags().BoolVar(&reverse, "reverse", false, "Show best results last (default with --pretty)")
	cmd.Flags().Float64Var(&threshold, "threshold", 0, "Score threshold (default from config, only with reranking)")
	cmd.Flags().BoolVar(&noTips, "no-tips", false, "Suppress tip line")
	cmd.Flags().StringVar(&readCommand, "read-command", "", "Reader command prefix used in agent hints (e.g. \"mem read\" from a wrapper CLI)")
	cmd.Flags().StringSliceVar(&files, "file", nil, "Search within specific file(s) at line granularity (repeatable)")
	cmd.Flags().BoolVar(&sections, "sections", false, "Aggregate results to section level")
	cmd.Flags().BoolVar(&withIndex, "with-index", true, "Decorate results with folder/file context from sift.toml (default on)")
	cmd.Flags().BoolVar(&noIndex, "no-index", false, "Strip folder/file context from results (overrides --with-index)")

	return cmd
}

// searchFlags carries the cobra-flag values (and a few derived values
// like the wall-clock start) into the in-process and via-daemon
// implementations. It is constructed inside the search command's RunE
// and never escapes that scope.
type searchFlags struct {
	query        string
	start        time.Time
	collection   string
	since        string
	pathGlob     string
	topK         int
	agent        bool
	compact      bool
	jsonOutput   bool
	filesOutput  bool
	pretty       bool
	reverse      bool
	threshold    float64
	noTips       bool
	readCommand  string
	files        []string
	sections     bool
	indexEnabled bool
}

// daemonFallbackWarnOnce ensures the user sees at most one stderr line
// per process when the daemon path fails and we silently degrade to the
// in-process implementation. We don't have request-level cardinality
// here, so once-per-process is the right granularity.
var daemonFallbackWarnOnce sync.Once

// warnDaemonFallback prints a one-shot warning to stderr the first time
// a true daemon failure forces a fallback. It does NOT fire on user
// opt-outs (SIFT_NO_DAEMON, cfg.Daemon.Enabled=false) — those paths
// pass nil for w to skip the print.
func warnDaemonFallback(w io.Writer) {
	if w == nil {
		return
	}
	daemonFallbackWarnOnce.Do(func() {
		fmt.Fprintln(w, "Warning: daemon unavailable, using in-process search (run 'sift daemon logs' for details)")
	})
}

// applyPrettyDefaults resolves --pretty-implied defaults (--reverse on,
// halve --top-k) on f BEFORE the orchestrator splits into daemon vs
// in-process paths. Must run pre-branch so daemon-served requests see
// the same derived values as in-process. Halving requires the resolved
// topK, so we materialize cfg.Search.DefaultTopK here when topK==0.
func applyPrettyDefaults(cmd *cobra.Command, f *searchFlags) {
	if !f.pretty {
		return
	}
	if !cmd.Flags().Changed("top-k") {
		topK := f.topK
		if topK == 0 {
			if cfg, err := config.Load(); err == nil && cfg != nil {
				topK = cfg.Search.DefaultTopK
			}
		}
		if topK > 0 {
			f.topK = max(1, topK/2)
		}
	}
	if !cmd.Flags().Changed("reverse") {
		f.reverse = true
	}
}

// runSearchOrchestrated picks between the daemon and in-process search
// paths and falls back gracefully on any daemon-side failure. See U12.
func runSearchOrchestrated(cmd *cobra.Command, f *searchFlags) error {
	logger := slog.With(slog.String("component", "cli"), slog.String("op", "search"))

	applyPrettyDefaults(cmd, f)

	// User opt-out via env var: silent, no fallback warning.
	if os.Getenv("SIFT_NO_DAEMON") != "" {
		logger.Debug("daemon disabled via SIFT_NO_DAEMON, using in-process path")
		return runSearchInProcessFn(cmd, f)
	}

	cfg, _ := config.Load() // best-effort; defaults are fine for timeouts

	// User opt-out via config: silent, no fallback warning.
	if cfg != nil && !cfg.Daemon.Enabled {
		logger.Debug("daemon disabled via cfg.Daemon.Enabled=false, using in-process path")
		if err := ensureDaemonStoppedForLocalMode(); err != nil {
			return err
		}
		return runSearchInProcessFn(cmd, f)
	}

	dialTimeout := 50 * time.Millisecond
	spawnTimeout := 300 * time.Millisecond
	if cfg != nil {
		if d := cfg.Daemon.DialTimeout.D(); d > 0 {
			dialTimeout = d
		}
		if d := cfg.Daemon.SpawnTimeout.D(); d > 0 {
			spawnTimeout = d
		}
	}

	stderr := cmd.ErrOrStderr()

	// 1) Try existing daemon (warm path).
	client, err := tryDial(dialTimeout)
	if err == nil {
		defer client.Close()
		if runErr := searchViaDaemonFn(cmd, f, client); runErr == nil {
			logger.Debug("daemon search ok", slog.String("phase", "daemon-call"))
			return nil
		} else {
			logger.Warn("daemon search failed, falling back to in-process",
				slog.String("phase", "daemon-call"), slog.Any("err", runErr))
			warnDaemonFallback(stderr)
			return runSearchInProcessFn(cmd, f)
		}
	}
	logger.Debug("daemon not running, attempting auto-spawn",
		slog.String("phase", "dial"), slog.Any("err", err))

	// 2) Auto-spawn.
	if err := spawnDetached(); err != nil {
		logger.Warn("daemon auto-spawn failed, using in-process path",
			slog.String("phase", "spawn"), slog.Any("err", err))
		warnDaemonFallback(stderr)
		return runSearchInProcessFn(cmd, f)
	}
	client2, err := dialUntilReady(spawnTimeout)
	if err != nil {
		logger.Warn("spawned daemon did not become ready in time, using in-process path",
			slog.String("phase", "dialready"), slog.Any("err", err))
		warnDaemonFallback(stderr)
		return runSearchInProcessFn(cmd, f)
	}
	defer client2.Close()
	if runErr := searchViaDaemonFn(cmd, f, client2); runErr != nil {
		logger.Warn("daemon search after spawn failed, falling back to in-process",
			slog.String("phase", "daemon-call"), slog.Any("err", runErr))
		warnDaemonFallback(stderr)
		return runSearchInProcessFn(cmd, f)
	}
	logger.Debug("daemon search ok (post-spawn)", slog.String("phase", "daemon-call"))
	return nil
}

// searchViaDaemon runs the search via a connected daemon client and
// formats the response according to the requested output mode. Returns
// an error if the daemon call fails OR if response formatting fails;
// the orchestrator treats any error as a signal to fall back to the
// in-process path.
func searchViaDaemon(cmd *cobra.Command, f *searchFlags, client *daemon.Client) error {
	output := "default"
	switch {
	case f.jsonOutput:
		output = "json"
	case f.filesOutput:
		output = "files"
	case f.pretty:
		output = "pretty"
	case f.agent && f.compact:
		output = "compact"
	case f.agent:
		output = "agent"
	}

	req := daemon.SearchRequest{
		Query:            f.query,
		Collection:       f.collection,
		Since:            f.since,
		PathGlob:         f.pathGlob,
		TopK:             f.topK,
		Threshold:        f.threshold,
		Adaptive:         !cmd.Flags().Changed("top-k"),
		SectionAggregate: f.sections || f.agent,
		OutputFormat:     output,
	}

	ctx := cmd.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	resp, err := client.Search(ctx, req)
	if err != nil {
		return fmt.Errorf("daemon search: %w", err)
	}

	// Persist session + JSONL log so `sift feedback <id>` and `sift logs`
	// work for daemon-served searches too. Best-effort: any failure logs
	// a warning but never trips fallback — the user already has results.
	writeDaemonSearchSession(cmd, f, resp)

	w := cmd.OutOrStdout()

	// --files mode: unique file paths only.
	if f.filesOutput {
		printed := make(map[string]bool)
		for _, r := range resp.Results {
			if !printed[r.File] {
				fmt.Fprintln(w, r.File)
				printed[r.File] = true
			}
		}
		return nil
	}

	// --json mode: the daemon SearchResponse is already byte-equivalent
	// to the CLI --json envelope.
	if f.jsonOutput {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(resp)
	}

	// All non-JSON, non-files modes need our local searchResult shape so
	// they can reuse the existing renderers. Convert.
	results := convertDaemonResults(resp.Results)
	elapsed := time.Duration(resp.Meta.TotalTimeMs) * time.Millisecond
	if elapsed == 0 {
		elapsed = time.Since(f.start)
	}

	if f.agent {
		renderAgentResults(w, f.query, resp.SearchID, results, elapsed,
			"", f.readCommand, f.collection, f.noTips, f.compact)
		return nil
	}

	// Reconstruct a minimal *search.SearchResult for header/tip helpers.
	sr := &search.SearchResult{
		TotalBM25:       resp.Meta.BM25Results,
		TotalVec:        resp.Meta.VectorResults,
		BM25TimeMs:      resp.Meta.BM25TimeMs,
		VecTimeMs:       resp.Meta.VectorTimeMs,
		RerankTimeMs:    resp.Meta.RerankTimeMs,
		Reranked:        resp.Meta.Reranked,
		TotalCandidates: resp.Meta.TotalCandidates,
		FilteredCount:   resp.Meta.FilteredCount,
		Threshold:       f.threshold,
	}

	if f.pretty {
		fmt.Fprintln(w, prettyHeader(f.query, resp.SearchID, sr, len(results), elapsed, resp.Meta.Cached))
	} else {
		fmt.Fprintln(w, buildHeader(f.query, resp.SearchID, sr, len(results), elapsed, resp.Meta.Cached))
	}
	fmt.Fprintln(w)

	if len(results) == 0 {
		tip := buildNoResultsTip(sr, f.threshold)
		if !f.noTips && tip != "" {
			if f.pretty {
				tip = style(tip, ansiDim)
			}
			fmt.Fprintln(w, tip)
		}
		return nil
	}

	displayOrder := results
	if f.reverse {
		displayOrder = make([]searchResult, len(results))
		for i, r := range results {
			displayOrder[len(results)-1-i] = r
		}
	}

	if f.pretty {
		sep := style("──────────────────────────────────────────────────", ansiDim)
		for i, r := range displayOrder {
			staleTag := ""
			if r.Stale {
				staleTag = " " + style("[stale]", ansiBold, ansiRed)
			}
			sectionTag := ""
			if r.Heading != "" {
				prefix := strings.Repeat("#", r.HeadingLevel) + " "
				sectionTag = " " + style(prefix+r.Heading, ansiBold, ansiGreen)
			}
			fmt.Fprintf(w, "%s %s%s%s %s%s\n",
				style("["+r.Index+"]", ansiBold, ansiCyan),
				style(r.File, ansiBold),
				sectionTag,
				style(fmt.Sprintf(":%d-%d", r.StartLine, r.EndLine), ansiDim),
				style(fmt.Sprintf("(%.2f)", r.Score), ansiBold, ansiYellow),
				staleTag)
			for line := range strings.SplitSeq(r.Content, "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
			if len(r.Duplicates) > 0 {
				var dups []string
				for _, dup := range r.Duplicates {
					dups = append(dups, fmt.Sprintf("%s:%d-%d", shortestPath(dup.File), dup.StartLine, dup.EndLine))
				}
				fmt.Fprintf(w, "    %s\n", style("Also in: "+strings.Join(dups, ", "), ansiDim))
			}
			if len(r.Links) > 0 {
				var linkStrs []string
				for _, l := range r.Links {
					s := shortestPath(l.TargetPath)
					if l.TargetSection != "" {
						s += "#" + l.TargetSection
					}
					linkStrs = append(linkStrs, s)
				}
				fmt.Fprintf(w, "    %s\n", style("→ links: "+strings.Join(linkStrs, ", "), ansiDim))
			}
			if suffix := formatIndexSuffix(r.IndexAnnotation, true); suffix != "" {
				fmt.Fprintf(w, "    %s\n", suffix)
			}
			fmt.Fprintf(w, "    %s\n", style("> "+r.OpenCmd, ansiDim))
			if i < len(displayOrder)-1 {
				fmt.Fprintln(w, sep)
			}
		}
		fmt.Fprintln(w)
	} else {
		for _, r := range displayOrder {
			if r.Heading != "" {
				prefix := strings.Repeat("#", r.HeadingLevel) + " "
				fmt.Fprintf(w, "[%s] %s %s%s:%d-%d (%.2f)\n", r.Index, r.File, prefix, r.Heading, r.StartLine, r.EndLine, r.Score)
			} else {
				fmt.Fprintf(w, "[%s] %s:%d-%d (%.2f)\n", r.Index, r.File, r.StartLine, r.EndLine, r.Score)
			}
			for line := range strings.SplitSeq(r.Content, "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
			if len(r.Duplicates) > 0 {
				var dups []string
				for _, dup := range r.Duplicates {
					dups = append(dups, fmt.Sprintf("%s:%d-%d", shortestPath(dup.File), dup.StartLine, dup.EndLine))
				}
				fmt.Fprintf(w, "    Also in: %s\n", strings.Join(dups, ", "))
			}
			if len(r.Links) > 0 {
				var linkStrs []string
				for _, l := range r.Links {
					s := shortestPath(l.TargetPath)
					if l.TargetSection != "" {
						s += "#" + l.TargetSection
					}
					linkStrs = append(linkStrs, s)
				}
				fmt.Fprintf(w, "    → links: %s\n", strings.Join(linkStrs, ", "))
			}
			if suffix := formatIndexSuffix(r.IndexAnnotation, true); suffix != "" {
				fmt.Fprintf(w, "    %s\n", suffix)
			}
			fmt.Fprintln(w)
		}
	}

	if !f.noTips {
		tip := buildTip(sr, resp.SearchID, f.threshold)
		if tip != "" {
			if f.pretty {
				tip = style(tip, ansiDim)
			}
			fmt.Fprintln(w, tip)
		}
	}

	return nil
}

// writeDaemonSearchSession mirrors the session insert + JSONL log block
// from runSearchInProcess so daemon-served searches are equally
// discoverable by `sift feedback <id>` and `sift logs`. All failures
// are logged at warn level; none propagate.
func writeDaemonSearchSession(cmd *cobra.Command, f *searchFlags, resp *daemon.SearchResponse) {
	logger := slog.With(slog.String("component", "cli"), slog.String("op", "search"))

	results := convertDaemonResults(resp.Results)
	resultsJSON, err := json.Marshal(results)
	if err != nil {
		logger.Warn("marshal daemon results for session", slog.Any("err", err))
		return
	}

	database, err := openDB()
	if err != nil {
		logger.Warn("open db for daemon session", slog.Any("err", err))
		return
	}
	defer database.Close()

	if err := database.InsertSearchSession(resp.SearchID, f.query, f.collection, string(resultsJSON)); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to store search session: %v\n", err)
	}

	cfg, err := config.Load()
	if err != nil {
		logger.Warn("load config for daemon log", slog.Any("err", err))
		return
	}
	logDir, err := config.LogDir()
	if err != nil {
		return
	}
	elapsed := time.Duration(resp.Meta.TotalTimeMs) * time.Millisecond
	if elapsed == 0 {
		elapsed = time.Since(f.start)
	}
	siftLogger := siftlog.NewLogger(logDir, cfg.Logs.MaxWeeks)
	logEntry := map[string]any{
		"timestamp":      time.Now().UTC().Format(time.RFC3339),
		"search_id":      resp.SearchID,
		"query":          f.query,
		"collection":     f.collection,
		"top_k":          f.topK,
		"results":        len(results),
		"elapsed_ms":     elapsed.Milliseconds(),
		"cached":         resp.Meta.Cached,
		"bm25_time_ms":   resp.Meta.BM25TimeMs,
		"vec_time_ms":    resp.Meta.VectorTimeMs,
		"rerank_time_ms": resp.Meta.RerankTimeMs,
		"total_bm25":     resp.Meta.BM25Results,
		"total_vec":      resp.Meta.VectorResults,
		"reranked":       resp.Meta.Reranked,
	}
	if len(results) > 0 {
		logEntry["top_score"] = results[0].Score
		logEntry["cutoff_score"] = results[len(results)-1].Score
	}
	if err := siftLogger.Log("searches", logEntry); err != nil {
		logger.Warn("write search log", slog.Any("err", err))
	}
}

// convertDaemonResults converts a slice of daemon.ResultEntry into the
// CLI's local searchResult shape. The two structs are field-for-field
// equivalent; only the embedded ref slices use a different package
// type.
func convertDaemonResults(in []daemon.ResultEntry) []searchResult {
	out := make([]searchResult, len(in))
	for i, r := range in {
		var dups []search.DuplicateRef
		for _, d := range r.Duplicates {
			dups = append(dups, search.DuplicateRef{
				File:      d.File,
				StartLine: d.StartLine,
				EndLine:   d.EndLine,
				Score:     d.Score,
			})
		}
		var sibs []search.NeighborhoodRef
		for _, n := range r.Siblings {
			sibs = append(sibs, search.NeighborhoodRef{
				FilePath:   n.FilePath,
				SectionID:  n.SectionID,
				Heading:    n.Heading,
				CharCount:  n.CharCount,
				HeadingLvl: n.HeadingLvl,
			})
		}
		var rel []search.NeighborhoodRef
		for _, n := range r.Related {
			rel = append(rel, search.NeighborhoodRef{
				FilePath:   n.FilePath,
				SectionID:  n.SectionID,
				Heading:    n.Heading,
				CharCount:  n.CharCount,
				HeadingLvl: n.HeadingLvl,
			})
		}
		var lks []search.LinkRef
		for _, l := range r.Links {
			lks = append(lks, search.LinkRef{
				TargetPath:    l.TargetPath,
				TargetSection: l.TargetSection,
				LinkType:      l.LinkType,
			})
		}
		var comp *scoreComponents
		if r.Components != nil {
			comp = &scoreComponents{
				BM25Rank:   r.Components.BM25Rank,
				VectorRank: r.Components.VectorRank,
				RRF:        r.Components.RRF,
				Rerank:     r.Components.Rerank,
				Final:      r.Components.Final,
			}
		}
		var ann *search.IndexAnnotation
		if r.IndexAnnotation != nil {
			ann = &search.IndexAnnotation{
				FolderPath:    r.IndexAnnotation.FolderPath,
				FolderPurpose: r.IndexAnnotation.FolderPurpose,
				FileSummary:   r.IndexAnnotation.FileSummary,
				FileWords:     r.IndexAnnotation.FileWords,
			}
			if len(r.IndexAnnotation.FolderUseWhen) > 0 {
				ann.FolderUseWhen = append(ann.FolderUseWhen, r.IndexAnnotation.FolderUseWhen...)
			}
		}
		out[i] = searchResult{
			Index:           r.Index,
			ChunkID:         r.ChunkID,
			File:            r.File,
			Collection:      r.Collection,
			StartLine:       r.StartLine,
			EndLine:         r.EndLine,
			Content:         r.Content,
			Stale:           r.Stale,
			Score:           r.Score,
			OpenCmd:         r.OpenCmd,
			Highlights:      r.Highlights,
			Components:      comp,
			Duplicates:      dups,
			Section:         r.Section,
			Heading:         r.Heading,
			HeadingLevel:    r.HeadingLevel,
			SectionChars:    r.SectionChars,
			SubsectionCount: r.SubsectionCount,
			Siblings:        sibs,
			Related:         rel,
			Links:           lks,
			IndexAnnotation: ann,
		}
	}
	return out
}

// runSearchInProcess is the original in-process body of the search
// command. It is used as the fallback path AND when the user sets
// SIFT_NO_DAEMON to opt out of daemon use entirely.
func runSearchInProcess(cmd *cobra.Command, f *searchFlags) error {
	// Re-bind for minimal-diff readability of the original body.
	query := f.query
	start := f.start
	collection := f.collection
	since := f.since
	pathGlob := f.pathGlob
	topK := f.topK
	agent := f.agent
	compact := f.compact
	jsonOutput := f.jsonOutput
	filesOutput := f.filesOutput
	pretty := f.pretty
	reverse := f.reverse
	threshold := f.threshold
	noTips := f.noTips
	readCommand := f.readCommand
	sections := f.sections

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if topK == 0 {
		topK = cfg.Search.DefaultTopK
	}

	// Use config threshold as default unless overridden.
	if !cmd.Flags().Changed("threshold") {
		threshold = cfg.Search.Threshold
	}

	// Adaptive mode: when --top-k not explicitly passed.
	adaptive := !cmd.Flags().Changed("top-k")
	if agent {
		sections = true
	}

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

	// Create Voyage client if API key is set.
	var voyageClient *voyage.Client
	if voyage.HasAPIKey(cfg.API.VoyageAPIKey) {
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
				slog.Warn("voyage preconnect failed", slog.String("component", "cli"), slog.String("op", "search"), slog.Any("err", err))
			}
		}()
	}

	// Parse time filter.
	var sinceUnix int64
	if since != "" {
		dur, err := parseDuration(since)
		if err != nil {
			return fmt.Errorf("invalid --since: %w", err)
		}
		sinceUnix = time.Now().Add(-dur).Unix()
	}

	// Cache setup.
	var searchCache *cache.Cache
	cached := false
	if cfg.Cache.Enabled {
		searchCache = cache.New(database, cfg.Cache.TTLSeconds, cfg.Cache.MaxEntries)
	}

	var result *search.SearchResult

	// Check cache first.
	cacheKey := ""
	if searchCache != nil {
		cacheKey = cache.Key(query, collection, sinceUnix, pathGlob, topK, threshold, adaptive, sections)
		if cr, ok := searchCache.Get(cacheKey); ok {
			// Cache hit: reconstruct the shaped SearchResult for this query.
			result = &search.SearchResult{
				Results:         cr.Results,
				TotalBM25:       cr.TotalBM25,
				TotalVec:        cr.TotalVec,
				BM25TimeMs:      cr.BM25TimeMs,
				VecTimeMs:       cr.VecTimeMs,
				RerankTimeMs:    cr.RerankTimeMs,
				Reranked:        cr.Reranked,
				TotalCandidates: cr.TotalCandidates,
				FilteredCount:   cr.FilteredCount,
				DroppedStale:    cr.DroppedStale,
				Threshold:       cr.Threshold,
				ContentDedupMap: cr.ContentDedupMap,
			}
			cached = true
		}
	}

	// Cache miss: run full search.
	if result == nil {
		engine := search.NewEngine(database, bleveIdx, voyageClient, cfg)
		result, err = engine.Search(context.Background(), query, search.SearchOptions{
			Collection:       collection,
			SinceUnix:        sinceUnix,
			TopK:             topK,
			Threshold:        threshold,
			Adaptive:         adaptive,
			PathGlob:         pathGlob,
			SectionAggregate: sections,
		})
		if err != nil {
			return fmt.Errorf("search: %w", err)
		}

		// Store the shaped result set for this specific query variant.
		if searchCache != nil && cacheKey != "" {
			cr := &cache.CachedResult{
				Results:         result.Results,
				TotalBM25:       result.TotalBM25,
				TotalVec:        result.TotalVec,
				BM25TimeMs:      result.BM25TimeMs,
				VecTimeMs:       result.VecTimeMs,
				RerankTimeMs:    result.RerankTimeMs,
				Reranked:        result.Reranked,
				TotalCandidates: result.TotalCandidates,
				FilteredCount:   result.FilteredCount,
				DroppedStale:    result.DroppedStale,
				Threshold:       result.Threshold,
				ContentDedupMap: result.ContentDedupMap,
			}
			searchCache.Put(cacheKey, cr, query, collection)
		}
	}

	// PHASE-6: decorate results with folder/file metadata from
	// `sift.toml`. Runs after scoring/dedup/adaptive top-K (already
	// applied inside engine.Search) and after cache lookup, so both
	// fresh and cached result sets render the same suffix.
	if f.indexEnabled && result != nil && len(result.Results) > 0 {
		decorateResultsWithIndex(result, collection, database)
	}

	elapsed := time.Since(start)

	// Build output results.
	previewChars := cfg.Search.PreviewChars
	previewLines := cfg.Search.PreviewLines

	var results []searchResult
	for i, r := range result.Results {
		idx := indexLabel(i)
		openCmd := strings.ReplaceAll(cfg.Output.EditorCommand, "{file}", r.FilePath)
		openCmd = strings.ReplaceAll(openCmd, "{line}", strconv.Itoa(r.StartLine))

		var contentPreview string
		if jsonOutput {
			contentPreview = readChunkPreview(r.FilePath, r.StartLine, r.EndLine, 0)
		} else if agent && !compact {
			contentPreview = readAgentPreview(r.FilePath, r.StartLine, r.EndLine, cfg.Agent.PreviewChars)
		} else if pretty && previewLines > 0 {
			contentPreview = readLineSnippet(r.FilePath, r.StartLine, r.EndLine, previewLines, r.Highlights)
		} else {
			// Both default and pretty: center on BM25 highlight when available.
			contentPreview = readHighlightPreview(r.FilePath, r.StartLine, r.EndLine, previewChars, r.Highlights)
		}
		stale := isStaleFile(r.FilePath, r.Mtime)

		sr := searchResult{
			Index:           idx,
			ChunkID:         r.ChunkID,
			File:            shortestPath(r.FilePath),
			Collection:      r.Collection,
			StartLine:       r.StartLine,
			EndLine:         r.EndLine,
			Content:         contentPreview,
			Stale:           stale,
			Score:           r.FinalScore,
			OpenCmd:         openCmd,
			Highlights:      r.Highlights,
			Section:         r.SectionID,
			Heading:         r.Heading,
			HeadingLevel:    r.HeadingLevel,
			SectionChars:    r.SectionCharCount,
			SubsectionCount: r.SubsectionCount,
			Siblings:        r.Siblings,
			Related:         r.Related,
			Links:           r.Links,
			IndexAnnotation: r.IndexAnnotation,
		}

		if result.ContentDedupMap != nil {
			sr.Duplicates = result.ContentDedupMap[r.ChunkID]
		}

		if jsonOutput {
			sr.File = r.FilePath // JSON always uses absolute paths
			sr.Components = &scoreComponents{
				BM25Rank:   r.BM25Rank,
				VectorRank: r.VectorRank,
				RRF:        r.RRFScore,
				Rerank:     r.RerankScore,
				Final:      r.FinalScore,
			}
		}

		results = append(results, sr)
	}

	// Generate search ID.
	searchID := generateSearchID()

	// Store search session.
	resultsJSON, err := json.Marshal(results)
	if err != nil {
		return fmt.Errorf("marshal results: %w", err)
	}
	if err := database.InsertSearchSession(searchID, query, collection, string(resultsJSON)); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to store search session: %v\n", err)
	}

	// Log search to JSONL (best-effort).
	logDir, logErr := config.LogDir()
	if logErr == nil {
		logger := siftlog.NewLogger(logDir, cfg.Logs.MaxWeeks)
		logEntry := map[string]any{
			"timestamp":      time.Now().UTC().Format(time.RFC3339),
			"search_id":      searchID,
			"query":          query,
			"collection":     collection,
			"top_k":          topK,
			"results":        len(results),
			"elapsed_ms":     elapsed.Milliseconds(),
			"cached":         cached,
			"bm25_time_ms":   result.BM25TimeMs,
			"vec_time_ms":    result.VecTimeMs,
			"rerank_time_ms": result.RerankTimeMs,
			"total_bm25":     result.TotalBM25,
			"total_vec":      result.TotalVec,
			"reranked":       result.Reranked,
			"dropped_stale":  result.DroppedStale,
		}
		if len(result.Results) > 0 {
			logEntry["top_score"] = result.Results[0].FinalScore
			logEntry["cutoff_score"] = result.Results[len(result.Results)-1].FinalScore
		}
		_ = logger.Log("searches", logEntry)
	}

	w := cmd.OutOrStdout()

	// --files mode: file paths only.
	if filesOutput {
		printed := make(map[string]bool)
		for _, r := range results {
			if !printed[r.File] {
				fmt.Fprintln(w, r.File)
				printed[r.File] = true
			}
		}
		return nil
	}

	// --json mode: verbose JSON (unchanged).
	if jsonOutput {
		output := map[string]any{
			"search_id": searchID,
			"query":     query,
			"results":   results,
			"meta": map[string]any{
				"total_time_ms":    elapsed.Milliseconds(),
				"bm25_time_ms":     result.BM25TimeMs,
				"vector_time_ms":   result.VecTimeMs,
				"rerank_time_ms":   result.RerankTimeMs,
				"reranked":         result.Reranked,
				"result_count":     len(results),
				"bm25_results":     result.TotalBM25,
				"vector_results":   result.TotalVec,
				"total_candidates": result.TotalCandidates,
				"filtered_count":   result.FilteredCount,
				"cached":           cached,
			},
			"feedback_cmd": fmt.Sprintf("sift feedback %s --positive <indices> --negative <indices>", searchID),
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	if agent {
		renderAgentResults(w, query, searchID, results, elapsed, cfg.Agent.Hint, readCommand, collection, noTips, compact)
		return nil
	}

	// Build score context header.
	if pretty {
		fmt.Fprintln(w, prettyHeader(query, searchID, result, len(results), elapsed, cached))
	} else {
		fmt.Fprintln(w, buildHeader(query, searchID, result, len(results), elapsed, cached))
	}
	fmt.Fprintln(w)

	if len(results) == 0 {
		tip := buildNoResultsTip(result, threshold)
		if !noTips && tip != "" {
			if pretty {
				tip = style(tip, ansiDim)
			}
			fmt.Fprintln(w, tip)
		}
		return nil
	}

	// Build display order: --reverse puts best results at the bottom
	// of the terminal (where the user's eye lands after scroll).
	displayOrder := results
	if reverse {
		displayOrder = make([]searchResult, len(results))
		for i, r := range results {
			displayOrder[len(results)-1-i] = r
		}
	}

	// --pretty mode: human-oriented with colors and previews.
	if pretty {
		sep := style("──────────────────────────────────────────────────", ansiDim)
		for i, r := range displayOrder {
			staleTag := ""
			if r.Stale {
				staleTag = " " + style("[stale]", ansiBold, ansiRed)
			}
			sectionTag := ""
			if r.Heading != "" {
				prefix := strings.Repeat("#", r.HeadingLevel) + " "
				sectionTag = " " + style(prefix+r.Heading, ansiBold, ansiGreen)
			}
			fmt.Fprintf(w, "%s %s%s%s %s%s\n",
				style("["+r.Index+"]", ansiBold, ansiCyan),
				style(r.File, ansiBold),
				sectionTag,
				style(fmt.Sprintf(":%d-%d", r.StartLine, r.EndLine), ansiDim),
				style(fmt.Sprintf("(%.2f)", r.Score), ansiBold, ansiYellow),
				staleTag)
			for line := range strings.SplitSeq(r.Content, "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
			if len(r.Duplicates) > 0 {
				var dups []string
				for _, dup := range r.Duplicates {
					dups = append(dups, fmt.Sprintf("%s:%d-%d", shortestPath(dup.File), dup.StartLine, dup.EndLine))
				}
				fmt.Fprintf(w, "    %s\n", style("Also in: "+strings.Join(dups, ", "), ansiDim))
			}
			if len(r.Links) > 0 {
				var linkStrs []string
				for _, l := range r.Links {
					s := shortestPath(l.TargetPath)
					if l.TargetSection != "" {
						s += "#" + l.TargetSection
					}
					linkStrs = append(linkStrs, s)
				}
				fmt.Fprintf(w, "    %s\n", style("→ links: "+strings.Join(linkStrs, ", "), ansiDim))
			}
			if suffix := formatIndexSuffix(r.IndexAnnotation, true); suffix != "" {
				fmt.Fprintf(w, "    %s\n", suffix)
			}
			fmt.Fprintf(w, "    %s\n", style("> "+r.OpenCmd, ansiDim))
			if i < len(displayOrder)-1 {
				fmt.Fprintln(w, sep)
			}
		}
		fmt.Fprintln(w)
	} else {
		// Default format: AI-optimized with full content.
		for _, r := range displayOrder {
			if r.Heading != "" {
				prefix := strings.Repeat("#", r.HeadingLevel) + " "
				fmt.Fprintf(w, "[%s] %s %s%s:%d-%d (%.2f)\n", r.Index, r.File, prefix, r.Heading, r.StartLine, r.EndLine, r.Score)
			} else {
				fmt.Fprintf(w, "[%s] %s:%d-%d (%.2f)\n", r.Index, r.File, r.StartLine, r.EndLine, r.Score)
			}
			// Indent full content.
			for line := range strings.SplitSeq(r.Content, "\n") {
				fmt.Fprintf(w, "    %s\n", line)
			}
			if len(r.Duplicates) > 0 {
				var dups []string
				for _, dup := range r.Duplicates {
					dups = append(dups, fmt.Sprintf("%s:%d-%d", shortestPath(dup.File), dup.StartLine, dup.EndLine))
				}
				fmt.Fprintf(w, "    Also in: %s\n", strings.Join(dups, ", "))
			}
			if len(r.Links) > 0 {
				var linkStrs []string
				for _, l := range r.Links {
					s := shortestPath(l.TargetPath)
					if l.TargetSection != "" {
						s += "#" + l.TargetSection
					}
					linkStrs = append(linkStrs, s)
				}
				fmt.Fprintf(w, "    → links: %s\n", strings.Join(linkStrs, ", "))
			}
			if suffix := formatIndexSuffix(r.IndexAnnotation, true); suffix != "" {
				fmt.Fprintf(w, "    %s\n", suffix)
			}
			fmt.Fprintln(w)
		}
	}

	if !noTips {
		tip := buildTip(result, searchID, threshold)
		if tip != "" {
			if pretty {
				tip = style(tip, ansiDim)
			}
			fmt.Fprintln(w, tip)
		}
	}

	return nil
}

// buildHeader constructs the score-context header line.
func buildHeader(query, searchID string, result *search.SearchResult, shown int, elapsed time.Duration, cached bool) string {
	var parts []string

	if result.TotalCandidates > 0 && result.FilteredCount > 0 && shown > 0 {
		// Filtered: N/M results
		parts = append(parts, fmt.Sprintf("%d/%d results", shown, result.TotalCandidates))
	} else if shown > 0 {
		parts = append(parts, fmt.Sprintf("%d results", shown))
	} else if result.TotalCandidates > 0 {
		return fmt.Sprintf("Search: %q | id:%s | No results above %.2f threshold. %d candidates below.",
			query, searchID, result.Threshold, result.TotalCandidates)
	} else {
		return fmt.Sprintf("Search: %q | id:%s | No results.", query, searchID)
	}

	// Score mode tag.
	tags := []string{}
	if result.Reranked {
		tags = append(tags, "reranked")
	} else {
		tags = append(tags, "bm25")
	}
	if cached {
		tags = append(tags, "cached")
	}

	return fmt.Sprintf("Search: %q | id:%s | %s (%s) | %dms",
		query, searchID, parts[0], strings.Join(tags, ", "), elapsed.Milliseconds())
}

// prettyHeader constructs a styled score-context header with ANSI colors.
func prettyHeader(query, searchID string, result *search.SearchResult, shown int, elapsed time.Duration, cached bool) string {
	prefix := style("Search:", ansiBold) + " " + style(fmt.Sprintf("%q", query), ansiBold, ansiCyan)
	id := style("id:"+searchID, ansiDim)

	if shown == 0 {
		if result.TotalCandidates > 0 {
			return fmt.Sprintf("%s | %s | No results above %.2f threshold. %d candidates below.",
				prefix, id, result.Threshold, result.TotalCandidates)
		}
		return fmt.Sprintf("%s | %s | No results.", prefix, id)
	}

	var resultPart string
	if result.TotalCandidates > 0 && result.FilteredCount > 0 {
		resultPart = fmt.Sprintf("%d/%d results", shown, result.TotalCandidates)
	} else {
		resultPart = fmt.Sprintf("%d results", shown)
	}

	tags := []string{}
	if result.Reranked {
		tags = append(tags, "reranked")
	} else {
		tags = append(tags, "bm25")
	}
	if cached {
		tags = append(tags, "cached")
	}

	return fmt.Sprintf("%s | %s | %s %s | %s",
		prefix, id, resultPart,
		style("("+strings.Join(tags, ", ")+")", ansiDim),
		style(fmt.Sprintf("%dms", elapsed.Milliseconds()), ansiDim))
}

// buildTip constructs the contextual tip line for non-empty results.
func buildTip(result *search.SearchResult, searchID string, threshold float64) string {
	if result.FilteredCount > 0 {
		return fmt.Sprintf("Tip: %d below %.2f threshold. --threshold %.1f for more.\n  sift feedback %s --positive a,b --negative c",
			result.FilteredCount, threshold, threshold/2, searchID)
	}
	return fmt.Sprintf("Tip: sift feedback %s --positive a,b --negative c", searchID)
}

// buildNoResultsTip constructs a tip for zero-result scenarios.
func buildNoResultsTip(result *search.SearchResult, threshold float64) string {
	if result.TotalCandidates > 0 {
		return fmt.Sprintf("Tip: sift search \"query\" --threshold %.1f", threshold/2)
	}
	return "Try different keywords or check sift refresh."
}

func renderAgentResults(w io.Writer, query, searchID string, results []searchResult, elapsed time.Duration, customHint string, readCommand string, collection string, noTips bool, compact bool) {
	fmt.Fprintf(w, "Search: %q | id:%s | %d results | %dms\n", query, searchID, len(results), elapsed.Milliseconds())
	fmt.Fprintln(w)

	if len(results) == 0 {
		if !noTips {
			fmt.Fprintln(w, agentHint(customHint, readCommand, collection))
		}
		return
	}

	if compact {
		renderCompactAgentResults(w, results)
	} else {
		for i, r := range results {
			fmt.Fprintf(w, "[%s] %s%s (%d chars)\n", r.Index, r.File, agentSectionFlag(r), agentCharCount(r))
			if r.Content != "" {
				for line := range strings.SplitSeq(r.Content, "\n") {
					fmt.Fprintf(w, "    %s\n", line)
				}
			}
			if len(r.Siblings) > 0 {
				fmt.Fprintf(w, "    siblings: %s\n", formatNeighborhoodList(r.Siblings, false))
			}
			if len(r.Related) > 0 {
				fmt.Fprintf(w, "    related: %s\n", formatNeighborhoodList(r.Related, true))
			}
			if i < len(results)-1 {
				fmt.Fprintln(w)
			}
		}
	}

	if noTips {
		return
	}

	fmt.Fprintln(w)
	fmt.Fprintln(w, agentHint(customHint, readCommand, collection))
	if !compact {
		fmt.Fprintf(w, "Feedback: sift feedback %s --positive a,b --negative c\n", searchID)
	}
}

func renderCompactAgentResults(w io.Writer, results []searchResult) {
	type fileGroup struct {
		Label    string
		File     string
		Sections []searchResult
	}

	groupIndex := make(map[string]int)
	var groups []fileGroup
	for _, result := range results {
		idx, ok := groupIndex[result.File]
		if !ok {
			idx = len(groups)
			groupIndex[result.File] = idx
			groups = append(groups, fileGroup{
				Label: indexLabel(idx),
				File:  result.File,
			})
		}
		groups[idx].Sections = append(groups[idx].Sections, result)
	}

	for i, group := range groups {
		fmt.Fprintf(w, "[%s] %s\n", group.Label, group.File)
		for _, result := range group.Sections {
			fmt.Fprintf(w, "    %s\n", compactSectionLine(result))
		}
		if i < len(groups)-1 {
			fmt.Fprintln(w)
		}
	}
}

func compactSectionLine(result searchResult) string {
	label := compactSectionLabel(result)
	if result.Section != "" {
		label += fmt.Sprintf(" [%s]", result.Section)
	}

	counts := fmt.Sprintf("%d chars", agentCharCount(result))
	if result.SubsectionCount > 0 {
		counts += fmt.Sprintf(", %d subsections", result.SubsectionCount)
	}
	return fmt.Sprintf("%s (%s)", label, counts)
}

func compactSectionLabel(result searchResult) string {
	if result.Heading != "" {
		level := result.HeadingLevel
		if level <= 0 {
			level = 2
		}
		return strings.Repeat("#", level) + " " + result.Heading
	}
	if result.Section != "" {
		return "Section"
	}
	return "Preamble"
}

func formatNeighborhoodList(refs []search.NeighborhoodRef, includePath bool) string {
	parts := make([]string, 0, len(refs))
	for _, ref := range refs {
		label := ref.Heading
		if label == "" {
			if ref.SectionID != "" {
				label = ref.SectionID
			} else {
				label = "Preamble"
			}
		}
		if includePath {
			label = shortestPath(ref.FilePath) + " > " + label
		}
		parts = append(parts, fmt.Sprintf("%s (%dc)", label, ref.CharCount))
	}
	return strings.Join(parts, ", ")
}

func agentSectionFlag(result searchResult) string {
	if result.Section == "" {
		return ""
	}
	return fmt.Sprintf(" --section %q", result.Section)
}

func agentCharCount(result searchResult) int {
	if result.SectionChars > 0 {
		return result.SectionChars
	}
	if result.Content != "" {
		return len([]rune(result.Content))
	}
	return 0
}

func agentHint(customHint string, readCommand string, collection string) string {
	if strings.TrimSpace(customHint) != "" {
		return customHint
	}
	if strings.TrimSpace(readCommand) == "" {
		if strings.TrimSpace(collection) == "" {
			return `HINT: Scope search with --collection NAME, then use sift read <file> --collection NAME --section "<section>".`
		}
		return fmt.Sprintf(`HINT: sift read <file> --collection %q --section "<section>" to read a result.`, collection)
	}
	return fmt.Sprintf(`HINT: %s <file> --section "<section>" to read a section.`, strings.TrimSpace(readCommand))
}

func readAgentPreview(path string, startLine, endLine, maxChars int) string {
	if maxChars <= 0 {
		maxChars = 200
	}

	content := fileutil.ReadLines(path, startLine, endLine, 0)
	var lines []string
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lines = append(lines, trimmed)
		if len(lines) == 2 {
			break
		}
	}
	if len(lines) == 0 {
		return truncate(strings.TrimSpace(content), maxChars)
	}
	return truncate(strings.Join(lines, " "), maxChars)
}

// readChunkPreview reads lines from a file for the given line range.
func readChunkPreview(path string, startLine, endLine, maxChars int) string {
	s := fileutil.ReadLines(path, startLine, endLine, maxChars)
	if maxChars > 0 {
		return truncate(s, maxChars)
	}
	return s
}

// readHighlightPreview reads a preview centered on the first highlight fragment.
func readHighlightPreview(path string, startLine, endLine, maxChars int, highlights []string) string {
	content := fileutil.ReadLines(path, startLine, endLine, 0)
	if len(highlights) == 0 || maxChars <= 0 {
		return truncate(content, maxChars)
	}

	// Find the first highlight fragment in the content and center around it.
	// Strip any HTML tags Bleve might add.
	fragment := highlights[0]
	fragment = stripHTMLTags(fragment)

	// Convert to runes for correct multi-byte handling.
	runes := []rune(content)
	fragRunes := []rune(fragment)
	idx := runeIndex(runes, fragRunes)
	if idx < 0 {
		return truncate(content, maxChars)
	}

	// Center the window on the fragment.
	windowStart := max(idx-maxChars/4, 0)
	windowEnd := windowStart + maxChars
	if windowEnd > len(runes) {
		windowEnd = len(runes)
	}
	if windowStart > len(runes) {
		windowStart = len(runes)
	}

	result := string(runes[windowStart:windowEnd])
	if windowStart > 0 {
		result = "..." + result
	}
	if windowEnd < len(runes) {
		result = result + "..."
	}
	return result
}

// isMeaningfulLine returns true if the line has enough alphanumeric content to be worth showing.
func isMeaningfulLine(line string) bool {
	count := 0
	for _, r := range line {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			count++
			if count >= 4 {
				return true
			}
		}
	}
	return false
}

// readLineSnippet returns a focused line-aligned snippet from a chunk,
// centered on the lines most relevant to the given highlights.
// windowLines controls how many lines to show (e.g. 7).
// Falls back to readHighlightPreview if no highlight match is found.
func readLineSnippet(path string, startLine, endLine, windowLines int, highlights []string) string {
	content := fileutil.ReadLines(path, startLine, endLine, 0)
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")
	if len(lines) <= windowLines {
		return content // chunk is already small enough
	}

	// Find the best line by scoring each line against highlight tokens.
	bestLine := findBestLine(lines, highlights)

	// Expand outward from bestLine, collecting meaningful lines.
	type snippetLine struct {
		idx  int
		text string
	}
	var collected []snippetLine

	// Always include the best line itself.
	collected = append(collected, snippetLine{bestLine, lines[bestLine]})

	// Expand alternating above and below.
	above, below := bestLine-1, bestLine+1
	for len(collected) < windowLines && (above >= 0 || below < len(lines)) {
		if above >= 0 {
			if isMeaningfulLine(lines[above]) {
				collected = append([]snippetLine{{above, lines[above]}}, collected...)
			}
			above--
		}
		if len(collected) >= windowLines {
			break
		}
		if below < len(lines) {
			if isMeaningfulLine(lines[below]) {
				collected = append(collected, snippetLine{below, lines[below]})
			}
			below++
		}
	}

	// Build snippet from collected lines.
	var snippetLines []string
	for _, cl := range collected {
		snippetLines = append(snippetLines, cl.text)
	}
	snippet := strings.Join(snippetLines, "\n")

	// Add ellipsis based on whether we reached the edges.
	firstIdx := collected[0].idx
	lastIdx := collected[len(collected)-1].idx
	if firstIdx > 0 {
		snippet = "...\n" + snippet
	}
	if lastIdx < len(lines)-1 {
		snippet = snippet + "\n..."
	}
	return snippet
}

// findBestLine scores each line against highlight fragments and returns
// the index of the highest-scoring line. Returns 0 if no match found.
func findBestLine(lines []string, highlights []string) int {
	if len(highlights) == 0 {
		return 0
	}

	// Extract tokens from all highlight fragments.
	var tokens []string
	for _, h := range highlights {
		clean := stripHTMLTags(h)
		for _, word := range strings.Fields(clean) {
			w := strings.ToLower(strings.Trim(word, ".,;:!?\"'`()[]{}"))
			if len(w) >= 2 { // skip single-char noise
				tokens = append(tokens, w)
			}
		}
	}
	if len(tokens) == 0 {
		return 0
	}

	// Deduplicate tokens.
	seen := make(map[string]bool)
	unique := tokens[:0]
	for _, t := range tokens {
		if !seen[t] {
			seen[t] = true
			unique = append(unique, t)
		}
	}
	tokens = unique

	// Score each line by how many distinct tokens it contains.
	bestIdx := 0
	bestScore := 0
	for i, line := range lines {
		lower := strings.ToLower(line)
		score := 0
		for _, tok := range tokens {
			if strings.Contains(lower, tok) {
				score++
			}
		}
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}
	return bestIdx
}

// runeIndex finds the first occurrence of needle in haystack, returning the rune offset.
func runeIndex(haystack, needle []rune) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// stripHTMLTags removes simple HTML tags like <mark>...</mark> from Bleve highlights.
func stripHTMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// shortestPath returns the shortest representation of an absolute path:
// CWD-relative, ~/relative, or absolute — whichever is shorter.
func shortestPath(abs string) string {
	shortest := abs

	// Try CWD-relative.
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, abs); err == nil && !strings.HasPrefix(rel, "..") {
			if len(rel) < len(shortest) {
				shortest = rel
			}
		}
	}

	// Try ~/relative.
	if home, err := os.UserHomeDir(); err == nil {
		if strings.HasPrefix(abs, home) {
			homeRel := "~" + abs[len(home):]
			if len(homeRel) < len(shortest) {
				shortest = homeRel
			}
		}
	}

	return shortest
}

// decorateResultsWithIndex resolves the collection root for the active
// query and asks search.Decorate to attach IndexAnnotations to each
// hit. Errors are non-fatal: results are returned unchanged.
//
// When the search is unscoped (no --collection), we group hits by the
// collection ID stored on the result and call Decorate once per
// distinct collection root so a single search across two collections
// still walks the right `sift.toml` chain for each hit.
func decorateResultsWithIndex(result *search.SearchResult, collection string, database collectionLookup) {
	if result == nil || len(result.Results) == 0 || database == nil {
		return
	}

	// Fast path: an explicit --collection means every hit shares one root.
	if collection != "" {
		col, err := database.GetCollection(collection)
		if err != nil || col == nil || col.Path == "" {
			return
		}
		result.Results = search.Decorate(result.Results, search.DecorateOptions{
			CollectionRoot: col.Path,
		})
		return
	}

	// Multi-collection path: bucket by CollectionID.
	pathByID := make(map[int64]string)
	bucket := make(map[int64][]int) // collection_id -> indices into result.Results
	for i, r := range result.Results {
		if _, ok := pathByID[r.CollectionID]; !ok {
			col, err := database.GetCollectionByID(r.CollectionID)
			if err == nil && col != nil {
				pathByID[r.CollectionID] = col.Path
			} else {
				pathByID[r.CollectionID] = ""
			}
		}
		bucket[r.CollectionID] = append(bucket[r.CollectionID], i)
	}

	for cid, idxs := range bucket {
		root := pathByID[cid]
		if root == "" {
			continue
		}
		// Pull out, decorate, write back.
		slice := make([]search.Result, len(idxs))
		for k, idx := range idxs {
			slice[k] = result.Results[idx]
		}
		decorated := search.Decorate(slice, search.DecorateOptions{
			CollectionRoot: root,
		})
		for k, idx := range idxs {
			result.Results[idx] = decorated[k]
		}
	}
}

// collectionLookup is the minimal slice of *db.DB we depend on for
// decoration. Allows tests to substitute a tiny stub.
type collectionLookup interface {
	GetCollection(name string) (*db.Collection, error)
	GetCollectionByID(id int64) (*db.Collection, error)
}

// formatIndexSuffix renders the grey, single-line suffix that follows
// a human-mode result when its IndexAnnotation has something useful.
// Returns "" when nothing should be shown.
//
// Priority: folder purpose first (truncated to 60 chars). If empty but
// the file has a summary, we use the summary instead. If both empty,
// we omit the suffix entirely.
func formatIndexSuffix(ann *search.IndexAnnotation, dim bool) string {
	if ann == nil {
		return ""
	}
	purpose := strings.TrimSpace(ann.FolderPurpose)
	summary := strings.TrimSpace(ann.FileSummary)
	const maxChars = 60

	var label, body string
	switch {
	case purpose != "":
		label = "folder"
		body = truncate(firstSentence(purpose), maxChars)
	case summary != "":
		label = "file"
		body = truncate(firstSentence(summary), maxChars)
	default:
		return ""
	}
	suffix := fmt.Sprintf("[%s: %s]", label, body)
	if dim {
		return style(suffix, ansiDim)
	}
	return suffix
}

// firstSentence collapses multi-line purpose/summary to its first line
// or first ". " split, whichever comes first. Keeps the suffix to a
// single visual line.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.Index(s, "\n"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, ". "); i >= 0 {
		s = s[:i+1]
	}
	return strings.TrimSpace(s)
}

// isStaleFile checks if a file has been modified since it was last indexed.
func isStaleFile(path string, indexedMtime int64) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return true // file missing = stale
	}
	return fi.ModTime().Unix() > indexedMtime
}

func generateSearchID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func indexLabel(i int) string {
	if i < 26 {
		return string(rune('a' + i))
	}
	return string(rune('a'+i/26-1)) + string(rune('a'+i%26))
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func parseDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("too short: %q", s)
	}

	numStr := s[:len(s)-1]
	unit := s[len(s)-1]

	var n int
	if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil {
		return 0, fmt.Errorf("invalid number: %q", numStr)
	}

	switch unit {
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown unit: %c (use h, d, or w)", unit)
	}
}

// handleFileSearch performs line-level BM25 search within specific files.
func handleFileSearch(cmd *cobra.Command, query string, files []string, topK int, jsonOutput, filesOutput, pretty, reverse, noTips bool, threshold float64, collection, since, pathGlob string, start time.Time) error {
	// Validate flag conflicts.
	if collection != "" {
		return fmt.Errorf("--file and --collection cannot be combined")
	}
	if since != "" {
		return fmt.Errorf("--file and --since cannot be combined")
	}
	if pathGlob != "" {
		return fmt.Errorf("--file and --path cannot be combined")
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if topK == 0 {
		topK = cfg.Search.DefaultTopK
	}
	if pretty && !cmd.Flags().Changed("top-k") {
		topK = max(1, topK/2)
	}
	if pretty && !cmd.Flags().Changed("reverse") {
		reverse = true
	}

	contextLines := 0
	if pretty {
		contextLines = 2
	}

	results, err := search.SearchFiles(context.Background(), query, files, search.FileSearchOptions{
		TopK:      topK,
		Context:   contextLines,
		Threshold: threshold,
		Analyzer:  cfg.BM25.Analyzer,
	})
	if err != nil {
		return err
	}

	elapsed := time.Since(start)
	w := cmd.OutOrStdout()

	// --files mode: unique file paths only.
	if filesOutput {
		printed := make(map[string]bool)
		for _, r := range results {
			if !printed[r.FilePath] {
				fmt.Fprintln(w, r.FilePath)
				printed[r.FilePath] = true
			}
		}
		return nil
	}

	// --json mode.
	if jsonOutput {
		type jsonResult struct {
			Index   string  `json:"index"`
			File    string  `json:"file"`
			Line    int     `json:"line"`
			Content string  `json:"content"`
			Score   float64 `json:"score"`
		}
		jResults := make([]jsonResult, len(results))
		for i, r := range results {
			jResults[i] = jsonResult{
				Index:   indexLabel(i),
				File:    r.FilePath,
				Line:    r.LineNumber,
				Content: r.Content,
				Score:   r.Score,
			}
		}
		output := map[string]any{
			"query":   query,
			"files":   files,
			"results": jResults,
			"meta": map[string]any{
				"total_time_ms": elapsed.Milliseconds(),
				"result_count":  len(results),
			},
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(output)
	}

	// Build display order.
	displayResults := results
	if reverse {
		displayResults = make([]search.FileSearchResult, len(results))
		for i, r := range results {
			displayResults[len(results)-1-i] = r
		}
	}

	// Header.
	fileCount := len(files)
	if pretty {
		fmt.Fprintf(w, "%s %s | %s | %s %s | %s\n",
			style("File search:", ansiBold),
			style(fmt.Sprintf("%q", query), ansiBold, ansiCyan),
			style(fmt.Sprintf("%d files", fileCount), ansiDim),
			fmt.Sprintf("%d results", len(results)),
			style("(bm25, line-level)", ansiDim),
			style(fmt.Sprintf("%dms", elapsed.Milliseconds()), ansiDim))
	} else {
		fmt.Fprintf(w, "File search: %q | %d files | %d results (bm25, line-level) | %dms\n",
			query, fileCount, len(results), elapsed.Milliseconds())
	}
	fmt.Fprintln(w)

	if len(results) == 0 {
		return nil
	}

	if pretty {
		sep := style("──────────────────────────────────────────────────", ansiDim)
		for i, r := range displayResults {
			idx := indexLabel(findOriginalIndex(r, results))
			fmt.Fprintf(w, "%s %s %s\n",
				style("["+idx+"]", ansiBold, ansiCyan),
				style(fmt.Sprintf("%s:%d", shortestPath(r.FilePath), r.LineNumber), ansiBold),
				style(fmt.Sprintf("(%.2f)", r.Score), ansiBold, ansiYellow))
			for _, cl := range r.ContextBefore {
				fmt.Fprintf(w, "    %s\n", style(cl, ansiDim))
			}
			fmt.Fprintf(w, "    %s    %s\n",
				fmt.Sprintf("%d: %s", r.LineNumber, r.Content),
				style("← match", ansiBold, ansiGreen))
			for _, cl := range r.ContextAfter {
				fmt.Fprintf(w, "    %s\n", style(cl, ansiDim))
			}
			if i < len(displayResults)-1 {
				fmt.Fprintln(w, sep)
			}
		}
		fmt.Fprintln(w)
	} else {
		for _, r := range displayResults {
			idx := indexLabel(findOriginalIndex(r, results))
			fmt.Fprintf(w, "[%s] %3d: %s (%.2f)\n", idx, r.LineNumber, r.Content, r.Score)
			fmt.Fprintf(w, "    %s\n", shortestPath(r.FilePath))
			fmt.Fprintln(w)
		}
	}

	return nil
}

// findOriginalIndex returns the index of a result in the original (score-sorted) slice.
func findOriginalIndex(r search.FileSearchResult, original []search.FileSearchResult) int {
	for i, o := range original {
		if o.FilePath == r.FilePath && o.LineNumber == r.LineNumber {
			return i
		}
	}
	return 0
}
