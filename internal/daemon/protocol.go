// Package daemon defines the wire-protocol types for the SIFT daemon
// (HTTP-over-Unix-socket).
//
// These types are pure data — no logic, no I/O, no dependencies on the
// CLI, server, or client packages. Both the daemon server and any client
// (including the sift CLI in daemon-aware mode) import this package, so
// it must stay free of import cycles.
//
// Design notes:
//
//   - SearchResponse mirrors the JSON envelope produced today by
//     `sift search --json` in internal/cli/search.go (lines 35-57 plus the
//     enclosing map at lines 402-425). Field names and shapes are chosen
//     to be byte-equivalent to the in-process path so daemon and direct
//     callers produce identical output.
//
//   - We intentionally do NOT import internal/search here. internal/search
//     is a heavy package (pulls in db, voyage, index, config) and importing
//     it from internal/daemon would force every daemon client to depend on
//     the full search engine. Instead, we define small mirror types
//     (DuplicateRef, NeighborhoodRef, LinkRef) with the same JSON tags as
//     their counterparts in internal/search. Conversion happens at the
//     daemon's HTTP handler boundary.
//
//   - All types use json struct tags. Optional fields use `,omitempty` so
//     the wire format stays minimal when fields are zero-valued.
package daemon

// ---------------------------------------------------------------------------
// Search
// ---------------------------------------------------------------------------

// SearchRequest is the request body for POST /search.
type SearchRequest struct {
	Query            string  `json:"query"`
	Collection       string  `json:"collection,omitempty"`
	Since            string  `json:"since,omitempty"` // duration string, e.g. "2d", "1w"
	TopK             int     `json:"top_k,omitempty"`
	Threshold        float64 `json:"threshold,omitempty"`
	Adaptive         bool    `json:"adaptive,omitempty"`
	File             string  `json:"file,omitempty"`          // single-file BM25 search override
	OutputFormat     string  `json:"output_format,omitempty"` // "json" | "files" | "pretty"
	SearchID         string  `json:"search_id,omitempty"`     // optional client-provided id
	PathGlob         string  `json:"path_glob,omitempty"`
	SectionAggregate bool    `json:"section_aggregate,omitempty"`
}

// ScoreComponents mirrors cli.scoreComponents — score breakdown returned
// when the CLI was invoked with --json.
type ScoreComponents struct {
	BM25Rank   int     `json:"bm25_rank"`
	VectorRank int     `json:"vector_rank"`
	RRF        float64 `json:"rrf"`
	Rerank     float64 `json:"rerank"`
	Final      float64 `json:"final"`
}

// DuplicateRef mirrors search.DuplicateRef. Defined here so internal/daemon
// has no dependency on internal/search.
type DuplicateRef struct {
	File      string  `json:"file"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Score     float64 `json:"score"`
}

// NeighborhoodRef mirrors search.NeighborhoodRef.
type NeighborhoodRef struct {
	FilePath   string `json:"file"`
	SectionID  string `json:"section,omitempty"`
	Heading    string `json:"heading,omitempty"`
	CharCount  int    `json:"char_count"`
	HeadingLvl int    `json:"heading_level,omitempty"`
}

// LinkRef mirrors search.LinkRef.
type LinkRef struct {
	TargetPath    string `json:"target_path"`
	TargetSection string `json:"target_section,omitempty"`
	LinkType      string `json:"link_type"`
}

// IndexAnnotation mirrors search.IndexAnnotation. Defined here so
// internal/daemon has no dependency on internal/search at the protocol
// boundary. Fields and JSON tags MUST stay in lockstep with
// internal/search/decorate.go's IndexAnnotation.
type IndexAnnotation struct {
	FolderPath    string   `json:"folder_path,omitempty"`
	FolderPurpose string   `json:"folder_purpose,omitempty"`
	FolderUseWhen []string `json:"folder_use_when,omitempty"`
	FileSummary   string   `json:"file_summary,omitempty"`
	FileWords     int      `json:"file_words,omitempty"`
}

// ResultEntry mirrors cli.searchResult — a single result row in the
// CLI's --json output. Field names and order match
// internal/cli/search.go lines 35-57.
type ResultEntry struct {
	Index           string            `json:"index"`
	ChunkID         int64             `json:"chunk_id"`
	File            string            `json:"file"`
	Collection      string            `json:"collection"`
	StartLine       int               `json:"start_line"`
	EndLine         int               `json:"end_line"`
	Content         string            `json:"content"`
	Stale           bool              `json:"stale,omitempty"`
	Score           float64           `json:"score"`
	OpenCmd         string            `json:"open_cmd"`
	Highlights      []string          `json:"highlights,omitempty"`
	Components      *ScoreComponents  `json:"components,omitempty"`
	Duplicates      []DuplicateRef    `json:"duplicates,omitempty"`
	Section         string            `json:"section,omitempty"`
	Heading         string            `json:"heading,omitempty"`
	HeadingLevel    int               `json:"heading_level,omitempty"`
	SectionChars    int               `json:"section_char_count,omitempty"`
	SubsectionCount int               `json:"subsection_count,omitempty"`
	Siblings        []NeighborhoodRef `json:"siblings,omitempty"`
	Related         []NeighborhoodRef `json:"related,omitempty"`
	Links           []LinkRef         `json:"links,omitempty"`
	IndexAnnotation *IndexAnnotation  `json:"folder_index,omitempty"`
}

// SearchMeta mirrors the "meta" block in the CLI's --json output
// (internal/cli/search.go lines 407-419).
type SearchMeta struct {
	TotalTimeMs     int64 `json:"total_time_ms"`
	BM25TimeMs      int64 `json:"bm25_time_ms"`
	VectorTimeMs    int64 `json:"vector_time_ms"`
	RerankTimeMs    int64 `json:"rerank_time_ms"`
	Reranked        bool  `json:"reranked"`
	ResultCount     int   `json:"result_count"`
	BM25Results     int   `json:"bm25_results"`
	VectorResults   int   `json:"vector_results"`
	TotalCandidates int   `json:"total_candidates"`
	FilteredCount   int   `json:"filtered_count"`
	Cached          bool  `json:"cached"`
	// WallParallelMs is reserved for future use: when the daemon runs BM25
	// and vector search concurrently, this is the wall-clock time of the
	// parallel phase. Not produced by today's CLI; emitted only by the
	// daemon when available.
	WallParallelMs int64 `json:"wall_parallel_ms,omitempty"`
}

// SearchResponse is the response body for POST /search.
//
// Mirrors the CLI --json envelope from internal/cli/search.go:402-425:
//
//	{
//	  "search_id":    "...",
//	  "query":        "...",
//	  "results":      [...],
//	  "meta":         {...},
//	  "feedback_cmd": "sift feedback ..."
//	}
//
// Daemon callers receive byte-equivalent JSON to the in-process --json path.
type SearchResponse struct {
	SearchID    string        `json:"search_id"`
	Query       string        `json:"query"`
	Results     []ResultEntry `json:"results"`
	Meta        SearchMeta    `json:"meta"`
	FeedbackCmd string        `json:"feedback_cmd"`
}

// ---------------------------------------------------------------------------
// Refresh
// ---------------------------------------------------------------------------

// RefreshRequest is the request body for POST /refresh.
type RefreshRequest struct {
	Collection string   `json:"collection,omitempty"`
	Full       bool     `json:"full,omitempty"`
	DryRun     bool     `json:"dry_run,omitempty"`
	NoIndex    bool     `json:"no_index,omitempty"`
	Files      []string `json:"files,omitempty"`
}

// ProgressEvent is a single line in a server-streamed refresh response
// (newline-delimited JSON).
type ProgressEvent struct {
	Ts         string `json:"ts"`    // RFC3339 timestamp
	Phase      string `json:"phase"` // "scan" | "chunk" | "embed" | "store" | "done"
	FilesDone  int    `json:"files_done"`
	FilesTotal int    `json:"files_total"`
	Message    string `json:"message,omitempty"`
	Error      string `json:"error,omitempty"`
}

// ---------------------------------------------------------------------------
// Health
// ---------------------------------------------------------------------------

// HealthResponse is the response body for GET /health.
type HealthResponse struct {
	Ok              bool   `json:"ok"`
	Version         string `json:"version"`
	UptimeS         int64  `json:"uptime_s"`
	PID             int    `json:"pid"`
	RequestCount    int64  `json:"request_count"`
	InFlight        int64  `json:"in_flight"`
	TLSDialsTotal   int64  `json:"tls_dials_total"`
	Goroutines      int    `json:"goroutines"`
	StartedAt       string `json:"started_at"` // RFC3339
	SocketPath      string `json:"socket_path,omitempty"`
	IdleTimeoutSecs int64  `json:"idle_timeout_secs"`
	DaemonLogPath   string `json:"daemon_log_path,omitempty"`
}

// ---------------------------------------------------------------------------
// Errors
// ---------------------------------------------------------------------------

// ErrorResponse is returned with non-2xx HTTP status codes.
type ErrorResponse struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Details string `json:"details,omitempty"`
}
