package daemon

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"sift/internal/bm25"
	"sift/internal/chunk"
	"sift/internal/config"
	"sift/internal/db"
	"sift/internal/fileutil"
	"sift/internal/search"
	syncpkg "sift/internal/sync"
	"sift/internal/voyage"
)

// Dependencies is the set of collaborators a Handlers instance needs.
// Wiring happens in Serve; tests construct Dependencies directly with
// stubs.
type Dependencies struct {
	Engine        SearchEngine
	Voyage        *voyage.Client
	DB            *db.DB
	BleveIdx      *bm25.BleveIndex
	Cfg           *config.Config
	Version       string
	StartedAt     time.Time
	RequestCount  *atomic.Int64
	InFlight      *atomic.Int64
	SocketPath    string
	DaemonLogPath string
	IdleTimeout   time.Duration
	ShutdownFn    func()
	Logger        *slog.Logger
}

// Handlers groups the daemon's HTTP handlers. Construct with newHandlers.
type Handlers struct {
	deps   Dependencies
	logger *slog.Logger

	// refreshMu serialises /refresh requests. Bleve does not allow
	// concurrent writers within the same process, and the in-process
	// pipeline assumes a single refresher per collection. A second
	// /refresh while one is in flight returns 409 immediately.
	refreshMu sync.Mutex
}

// newHandlers wires up a Handlers with sensible defaults filled in for
// optional dependencies (Logger, Version).
func newHandlers(deps Dependencies) *Handlers {
	logger := deps.Logger
	if logger == nil {
		logger = slog.Default().With(slog.String("component", "daemon"))
	}
	if deps.Version == "" {
		deps.Version = "dev"
	}
	if deps.RequestCount == nil {
		deps.RequestCount = new(atomic.Int64)
	}
	if deps.StartedAt.IsZero() {
		deps.StartedAt = time.Now()
	}
	deps.Logger = logger
	return &Handlers{deps: deps, logger: logger}
}

// writeJSON writes v as indented JSON with the given status. Errors during
// encoding are logged but cannot be reported back to the client (headers
// already sent).
func (h *Handlers) writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		h.logger.Warn("write json", slog.Any("err", err))
	}
}

// writeError emits a uniform ErrorResponse envelope. NEVER write raw
// error strings — every error path must flow through here.
func (h *Handlers) writeError(w http.ResponseWriter, status int, code, message string, err error) {
	resp := ErrorResponse{Code: code, Message: message}
	if err != nil {
		resp.Details = err.Error()
	}
	h.logger.Warn("error response",
		slog.Int("status", status),
		slog.String("code", code),
		slog.String("message", message),
		slog.Any("err", err),
	)
	h.writeJSON(w, status, resp)
}

// Health responds to GET /health with daemon liveness data.
func (h *Handlers) Health(w http.ResponseWriter, _ *http.Request) {
	resp := HealthResponse{
		Ok:              true,
		Version:         h.deps.Version,
		UptimeS:         int64(time.Since(h.deps.StartedAt).Seconds()),
		PID:             os.Getpid(),
		RequestCount:    h.deps.RequestCount.Load(),
		Goroutines:      runtime.NumGoroutine(),
		StartedAt:       h.deps.StartedAt.UTC().Format(time.RFC3339),
		SocketPath:      h.deps.SocketPath,
		DaemonLogPath:   h.deps.DaemonLogPath,
		IdleTimeoutSecs: int64(h.deps.IdleTimeout.Seconds()),
	}
	if h.deps.InFlight != nil {
		resp.InFlight = h.deps.InFlight.Load()
	}
	if h.deps.Voyage != nil {
		resp.TLSDialsTotal = h.deps.Voyage.DialCount()
	}
	h.writeJSON(w, http.StatusOK, resp)
}

// Search decodes a SearchRequest, runs the engine, and returns a
// SearchResponse byte-equivalent to `sift search --json`.
func (h *Handlers) Search(w http.ResponseWriter, r *http.Request) {
	var req SearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "decode body", err)
		return
	}
	if strings.TrimSpace(req.Query) == "" {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "query is required", nil)
		return
	}
	if h.deps.Engine == nil {
		h.writeError(w, http.StatusInternalServerError, "engine_unavailable", "search engine not configured", nil)
		return
	}

	// Translate Since duration string to a unix timestamp.
	var sinceUnix int64
	if req.Since != "" {
		dur, err := parseDurationLoose(req.Since)
		if err != nil {
			h.writeError(w, http.StatusBadRequest, "invalid_since", "parse since", err)
			return
		}
		sinceUnix = time.Now().Add(-dur).Unix()
	}

	topK := req.TopK
	threshold := req.Threshold
	if h.deps.Cfg != nil {
		if topK <= 0 {
			topK = h.deps.Cfg.Search.DefaultTopK
		}
		// If client supplied no threshold (zero), fall back to config.
		if threshold == 0 {
			threshold = h.deps.Cfg.Search.Threshold
		}
	}

	start := time.Now()
	result, err := h.deps.Engine.Search(r.Context(), req.Query, search.SearchOptions{
		Collection:       req.Collection,
		SinceUnix:        sinceUnix,
		TopK:             topK,
		Threshold:        threshold,
		Adaptive:         req.Adaptive,
		PathGlob:         req.PathGlob,
		SectionAggregate: req.SectionAggregate,
	})
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "search_failed", "search", err)
		return
	}
	elapsed := time.Since(start)

	searchID := req.SearchID
	if searchID == "" {
		searchID = generateSearchID()
	}

	// Decorate with folder/file metadata from sift.toml so daemon JSON
	// matches the in-process --json envelope (folder_index field).
	decorateForDaemon(result, req.Collection, h.deps.DB)

	resp := buildSearchResponse(req.Query, searchID, result, elapsed, h.deps.Cfg)
	h.writeJSON(w, http.StatusOK, resp)
}

// decorateForDaemon attaches IndexAnnotations to result.Results in-place.
// Mirrors cli.decorateResultsWithIndex but uses the daemon's *db.DB for
// collection lookups. Failures are non-fatal; results are returned
// unchanged when no DB or no resolvable collection root exists.
func decorateForDaemon(result *search.SearchResult, collection string, database *db.DB) {
	if result == nil || len(result.Results) == 0 || database == nil {
		return
	}

	if collection != "" {
		col, err := database.GetCollection(collection)
		if err != nil || col == nil || col.Path == "" {
			return
		}
		result.Results = search.Decorate(result.Results, search.DecorateOptions{
			CollectionRoot:  col.Path,
			MaxCacheEntries: 50,
		})
		return
	}

	pathByID := make(map[int64]string)
	bucket := make(map[int64][]int)
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
		slice := make([]search.Result, len(idxs))
		for k, idx := range idxs {
			slice[k] = result.Results[idx]
		}
		decorated := search.Decorate(slice, search.DecorateOptions{
			CollectionRoot:  root,
			MaxCacheEntries: 50,
		})
		for k, idx := range idxs {
			result.Results[idx] = decorated[k]
		}
	}
}

// Refresh runs sync.Refresh / sync.RefreshFiles in-process on the
// daemon, streaming progress events back as newline-delimited JSON
// (ProgressEvent). Concurrent /refresh requests are rejected with 409
// because Bleve does not support concurrent writers.
func (h *Handlers) Refresh(w http.ResponseWriter, r *http.Request) {
	if !h.refreshMu.TryLock() {
		h.writeError(w, http.StatusConflict, "refresh_busy", "another refresh is already running", nil)
		return
	}
	defer h.refreshMu.Unlock()

	var req RefreshRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request", "decode body", err)
		return
	}
	if h.deps.DB == nil || h.deps.BleveIdx == nil || h.deps.Cfg == nil {
		h.writeError(w, http.StatusInternalServerError, "engine_unavailable", "refresh dependencies not configured", nil)
		return
	}

	flusher, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)

	enc := json.NewEncoder(w)
	emit := func(ev ProgressEvent) {
		if ev.Ts == "" {
			ev.Ts = time.Now().UTC().Format(time.RFC3339)
		}
		if err := enc.Encode(ev); err != nil {
			h.logger.Warn("emit progress", slog.Any("err", err))
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
	}

	cfg := h.deps.Cfg
	opts := syncpkg.RefreshOptions{
		CollectionName: req.Collection,
		Full:           req.Full,
		DryRun:         req.DryRun,
		NoIndex:        req.NoIndex,
		BatchSize:      cfg.Embedding.BatchSize,
		ChunkOpts: chunk.Options{
			RowsPerChunk:  cfg.Chunking.RowsPerChunk,
			OverlapRows:   cfg.Chunking.OverlapRows,
			MinChunkChars: cfg.Chunking.MinChunkChars,
			SkipEmptyRows: cfg.Chunking.SkipEmptyRows,
		},
		SectionOpts: chunk.SectionOptions{
			MaxSectionChars: cfg.Chunking.MaxSectionChars,
			MinSectionChars: cfg.Chunking.MinSectionChars,
			OverlapLines:    cfg.Chunking.OverlapRows,
		},
		ChunkMode: cfg.Chunking.Mode,
	}

	// Pass nil voyage when no API key is configured so sync.Refresh
	// takes the BM25-only path, mirroring cli/refresh.go's behaviour.
	voy := h.deps.Voyage
	if cfg.API.VoyageAPIKey == "" {
		voy = nil
	}

	pw := &progressWriter{emit: emit, phase: "refresh"}

	var (
		stats  *syncpkg.RefreshStats
		err    error
		isFile = len(req.Files) > 0
	)
	if isFile {
		emit(ProgressEvent{Phase: "scan", Message: fmt.Sprintf("Refreshing %d file(s)...", len(req.Files))})
		stats, err = syncpkg.RefreshFiles(r.Context(), h.deps.DB, h.deps.BleveIdx, voy, req.Files, opts, pw)
	} else {
		emit(ProgressEvent{Phase: "scan", Message: "Refreshing..."})
		stats, err = syncpkg.Refresh(r.Context(), h.deps.DB, h.deps.BleveIdx, voy, opts, pw)
	}
	if err != nil {
		h.logger.Warn("refresh failed", slog.Any("err", err))
		emit(ProgressEvent{Phase: "error", Error: err.Error()})
		return
	}

	if !req.DryRun {
		if backlinkDocs, berr := h.deps.DB.RefreshBacklinkCounts(); berr != nil {
			emit(ProgressEvent{Phase: "store", Error: fmt.Sprintf("refresh backlinks: %v", berr)})
		} else if backlinkDocs > 0 {
			emit(ProgressEvent{Phase: "store", Message: fmt.Sprintf("  Backlinks: %d docs updated", backlinkDocs)})
		}
		if cfg.Cache.Enabled {
			if cerr := h.deps.DB.ClearCache(); cerr != nil {
				h.logger.Warn("clear cache", slog.Any("err", cerr))
			}
		}
	}

	var summary string
	if isFile {
		summary = fmt.Sprintf("Done: %d new, %d changed, %d chunks in %s",
			stats.FilesNew, stats.FilesChanged, stats.ChunksTotal, stats.Duration.Round(time.Millisecond))
	} else {
		summary = fmt.Sprintf("Done: %d files scanned, %d new, %d changed, %d deleted, %d chunks in %s",
			stats.FilesScanned, stats.FilesNew, stats.FilesChanged, stats.FilesDeleted, stats.ChunksTotal, stats.Duration.Round(time.Millisecond))
	}
	emit(ProgressEvent{
		Phase:      "done",
		FilesDone:  stats.FilesNew + stats.FilesChanged,
		FilesTotal: stats.FilesScanned,
		Message:    summary,
	})
	if stats.ChunksEmbedded > 0 {
		emit(ProgressEvent{
			Phase:   "done",
			Message: fmt.Sprintf("  Embeddings: %d embedded, %d errors, %d tokens", stats.ChunksEmbedded, stats.EmbedErrors, stats.EmbedTokens),
		})
	}
}

// Shutdown acknowledges the request and triggers daemon shutdown via
// the configured shutdownFn. The actual srv.Shutdown happens in Serve;
// here we only kick the signal channel.
func (h *Handlers) Shutdown(w http.ResponseWriter, _ *http.Request) {
	h.writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	if h.deps.ShutdownFn != nil {
		// Run async so the response is fully delivered before we begin
		// graceful shutdown.
		go h.deps.ShutdownFn()
	}
}

// progressWriter converts io.Writer lines into ProgressEvent emissions.
// sync.Refresh writes human-readable status text; for now we forward
// each line as a Message under the configured phase.
type progressWriter struct {
	emit  func(ProgressEvent)
	phase string
	buf   []byte
}

func (p *progressWriter) Write(b []byte) (int, error) {
	p.buf = append(p.buf, b...)
	for {
		i := indexByte(p.buf, '\n')
		if i < 0 {
			break
		}
		line := strings.TrimRight(string(p.buf[:i]), "\r")
		p.buf = p.buf[i+1:]
		if line == "" {
			continue
		}
		p.emit(ProgressEvent{Phase: p.phase, Message: line})
	}
	return len(b), nil
}

// indexByte is bytes.IndexByte without the import.
func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// buildSearchResponse mirrors internal/cli/search.go's --json envelope so
// daemon output is byte-equivalent to the in-process path. The CLI does
// preview reading and path shortening; we keep the full file path and
// raw content here, matching the JSON-mode branches in cli/search.go.
func buildSearchResponse(query, searchID string, result *search.SearchResult, elapsed time.Duration, cfg *config.Config) SearchResponse {
	resp := SearchResponse{
		SearchID: searchID,
		Query:    query,
		Results:  make([]ResultEntry, 0, len(result.Results)),
		Meta: SearchMeta{
			TotalTimeMs:     elapsed.Milliseconds(),
			BM25TimeMs:      result.BM25TimeMs,
			VectorTimeMs:    result.VecTimeMs,
			RerankTimeMs:    result.RerankTimeMs,
			Reranked:        result.Reranked,
			ResultCount:     len(result.Results),
			BM25Results:     result.TotalBM25,
			VectorResults:   result.TotalVec,
			TotalCandidates: result.TotalCandidates,
			FilteredCount:   result.FilteredCount,
			WallParallelMs:  result.WallParallelMs,
		},
		FeedbackCmd: fmt.Sprintf("sift feedback %s --positive <indices> --negative <indices>", searchID),
	}

	editorCmd := ""
	if cfg != nil {
		editorCmd = cfg.Output.EditorCommand
	}

	for i, r := range result.Results {
		entry := ResultEntry{
			Index:           indexLabel(i),
			ChunkID:         r.ChunkID,
			File:            r.FilePath,
			Collection:      r.Collection,
			StartLine:       r.StartLine,
			EndLine:         r.EndLine,
			Content:         fileutil.ReadLines(r.FilePath, r.StartLine, r.EndLine, 0),
			Stale:           isStaleFile(r.FilePath, r.Mtime),
			Score:           r.FinalScore,
			OpenCmd:         renderOpenCmd(editorCmd, r.FilePath, r.StartLine),
			Highlights:      r.Highlights,
			Section:         r.SectionID,
			Heading:         r.Heading,
			HeadingLevel:    r.HeadingLevel,
			SectionChars:    r.SectionCharCount,
			SubsectionCount: r.SubsectionCount,
			Components: &ScoreComponents{
				BM25Rank:   r.BM25Rank,
				VectorRank: r.VectorRank,
				RRF:        r.RRFScore,
				Rerank:     r.RerankScore,
				Final:      r.FinalScore,
			},
			Siblings: convertNeighborhoods(r.Siblings),
			Related:  convertNeighborhoods(r.Related),
			Links:    convertLinks(r.Links),
		}
		if result.ContentDedupMap != nil {
			if dups, ok := result.ContentDedupMap[r.ChunkID]; ok && len(dups) > 0 {
				entry.Duplicates = convertDuplicates(dups)
			}
		}
		if r.IndexAnnotation != nil {
			entry.IndexAnnotation = convertIndexAnnotation(r.IndexAnnotation)
		}
		resp.Results = append(resp.Results, entry)
	}
	return resp
}

// convertIndexAnnotation maps the in-search annotation type into the
// wire-protocol mirror so daemon JSON matches the in-process envelope.
func convertIndexAnnotation(in *search.IndexAnnotation) *IndexAnnotation {
	if in == nil {
		return nil
	}
	out := &IndexAnnotation{
		FolderPath:    in.FolderPath,
		FolderPurpose: in.FolderPurpose,
		FileSummary:   in.FileSummary,
		FileWords:     in.FileWords,
	}
	if len(in.FolderUseWhen) > 0 {
		out.FolderUseWhen = append(out.FolderUseWhen, in.FolderUseWhen...)
	}
	return out
}

// isStaleFile reports whether a file's on-disk mtime is newer than the
// indexed mtime. Mirrors cli/search.go's helper of the same name.
func isStaleFile(path string, indexedMtime int64) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return true
	}
	return fi.ModTime().Unix() > indexedMtime
}

// renderOpenCmd substitutes {file}/{line} placeholders in editor_command.
func renderOpenCmd(template, file string, line int) string {
	if template == "" {
		return ""
	}
	out := strings.ReplaceAll(template, "{file}", file)
	out = strings.ReplaceAll(out, "{line}", strconv.Itoa(line))
	return out
}

// convertNeighborhoods rewires search.NeighborhoodRef into the wire type.
func convertNeighborhoods(in []search.NeighborhoodRef) []NeighborhoodRef {
	if len(in) == 0 {
		return nil
	}
	out := make([]NeighborhoodRef, 0, len(in))
	for _, n := range in {
		out = append(out, NeighborhoodRef{
			FilePath:   n.FilePath,
			SectionID:  n.SectionID,
			Heading:    n.Heading,
			CharCount:  n.CharCount,
			HeadingLvl: n.HeadingLvl,
		})
	}
	return out
}

func convertLinks(in []search.LinkRef) []LinkRef {
	if len(in) == 0 {
		return nil
	}
	out := make([]LinkRef, 0, len(in))
	for _, l := range in {
		out = append(out, LinkRef{
			TargetPath:    l.TargetPath,
			TargetSection: l.TargetSection,
			LinkType:      l.LinkType,
		})
	}
	return out
}

func convertDuplicates(in []search.DuplicateRef) []DuplicateRef {
	if len(in) == 0 {
		return nil
	}
	out := make([]DuplicateRef, 0, len(in))
	for _, d := range in {
		out = append(out, DuplicateRef{
			File:      d.File,
			StartLine: d.StartLine,
			EndLine:   d.EndLine,
			Score:     d.Score,
		})
	}
	return out
}

// indexLabel renders a 1-based result index as "1", "2", ... with letter
// continuation at "A", "B", etc. once numeric indices exhaust the
// configured bound. Mirrors CLI behaviour at low arity.
func indexLabel(i int) string {
	return strconv.Itoa(i + 1)
}

// generateSearchID creates a 16-hex-char random id (matches CLI format).
func generateSearchID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// Fallback: derive from time. Not unique under heavy load but
		// avoids a panic in the rare /dev/urandom failure mode.
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// parseDurationLoose accepts both Go duration syntax ("30m", "2h") and
// the SIFT shorthand ("2d", "1w") used elsewhere in the CLI.
func parseDurationLoose(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty duration")
	}
	// Try Go's parser first.
	if d, err := time.ParseDuration(s); err == nil {
		return d, nil
	}
	// Custom suffixes: d (day), w (week).
	last := s[len(s)-1]
	num := s[:len(s)-1]
	mult := time.Duration(0)
	switch last {
	case 'd':
		mult = 24 * time.Hour
	case 'w':
		mult = 7 * 24 * time.Hour
	default:
		return 0, fmt.Errorf("invalid duration %q", s)
	}
	n, err := strconv.Atoi(num)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", s, err)
	}
	return time.Duration(n) * mult, nil
}
