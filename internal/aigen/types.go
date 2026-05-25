// Package aigen provides AI-powered folder/file summary generation for
// SIFT's `sift.toml` folder-index format. It wraps a DeepInfra
// OpenAI-compatible HTTP client, a worker pool, and the prompt /
// schema scaffolding that produces per-folder summaries.
//
// Phase-5 of the folder-index plan. Off by default; opt in via
// `sift refresh --generate=missing|stale|all`.
package aigen

import (
	"context"
	"time"
)

// FileBatch is a single file's payload supplied to the prompt. The
// caller is responsible for word-count math and head/tail extraction;
// aigen only formats and submits.
type FileBatch struct {
	// Path is the file's path relative to the folder owning the
	// `sift.toml`. Forward slashes only.
	Path string

	// HeadWords is the joined first 500 words of the file (or full
	// content for short files). Empty allowed.
	HeadWords string

	// TailWords is the joined last 500 words of the file. May equal
	// HeadWords when the file is short.
	TailWords string

	// Words is the file's total word count.
	Words int

	// Bytes is the file size in bytes (for partition policy).
	Bytes int64

	// CurrentSummary, when non-empty, is supplied to the model to
	// preserve sticky text unless the content has materially changed.
	CurrentSummary string

	// FrontmatterTitle is the value of `title`/`name` if present.
	FrontmatterTitle string

	// FrontmatterTags lists frontmatter tags/keywords entries.
	FrontmatterTags []string

	// FrontmatterType is the frontmatter `type` if present (e.g.
	// "proposal", "design", "spec").
	FrontmatterType string
}

// FolderContext carries the folder-level context handed to the prompt.
type FolderContext struct {
	// Path is the absolute folder path.
	Path string

	// RelPath is the path relative to the collection root (used in
	// progress events). Empty allowed.
	RelPath string

	// ParentPurpose, when non-empty, is the parent folder's purpose
	// text — supplied to anchor the model's interpretation.
	ParentPurpose string

	// ExistingPurpose, when non-empty, is the folder's prior purpose
	// (sticky carry-over).
	ExistingPurpose string

	// ExistingUseWhen lists the folder's prior `use_when` cues.
	ExistingUseWhen []string
}

// FileSummary is one entry in the model's response.
type FileSummary struct {
	Path    string `json:"path"`
	Summary string `json:"summary"`
}

// FolderResult is the parsed model response for a folder batch.
type FolderResult struct {
	Purpose string        `json:"purpose"`
	UseWhen []string      `json:"use_when,omitempty"`
	Files   []FileSummary `json:"files"`
}

// CallStats summarizes resource usage for a single LLM call.
type CallStats struct {
	TokensIn     int
	TokensOut    int
	CachedTokens int
	LatencyMs    int64
	HTTPStatus   int
	Attempts     int
}

// GenerateResult bundles the FolderResult with cumulative stats from
// every LLM call (sub-batches + synthesis) made for the folder.
type GenerateResult struct {
	Folder *FolderResult
	Calls  []CallStats
}

// TotalTokensIn returns the cumulative input token count across all
// calls in the result.
func (g *GenerateResult) TotalTokensIn() int {
	if g == nil {
		return 0
	}
	n := 0
	for _, c := range g.Calls {
		n += c.TokensIn
	}
	return n
}

// TotalTokensOut returns the cumulative output token count across all
// calls in the result.
func (g *GenerateResult) TotalTokensOut() int {
	if g == nil {
		return 0
	}
	n := 0
	for _, c := range g.Calls {
		n += c.TokensOut
	}
	return n
}

// Generator is the interface satisfied by the production aigen client
// and by stub implementations used in tests. It mirrors the contract
// the worker pool depends on so we can swap real and fake LLMs without
// duplicating the pool logic.
type Generator interface {
	GenerateFolder(ctx context.Context, in GenerateInput) (*GenerateResult, error)
}

// GenerateInput is the call-level payload for a single folder.
type GenerateInput struct {
	Folder FolderContext
	Files  []FileBatch
	// Order tweaks the partitioner. "" or "authority" picks the
	// authority-ranked default; "mtime" is opt-in for log folders.
	Order string
	// EmitWarning, when non-nil, is invoked from finalizeResult for
	// each post-parse diagnostic (grounding warnings, summary
	// truncations). The worker pool sets this per-job to translate
	// callbacks into PoolEvents; standalone callers (e.g. the
	// experiments harness) leave it nil and fall back to slog
	// logging only. kind is one of "grounding_warning" or
	// "summary_truncated".
	EmitWarning func(folder, kind, message string) `json:"-"`
}

// PoolEvent kinds emitted on the pool's event channel.
type PoolEventKind string

const (
	EventScanDone         PoolEventKind = "scan_done"
	EventFolderStart      PoolEventKind = "folder_start"
	EventFolderDone       PoolEventKind = "folder_done"
	EventFolderError      PoolEventKind = "folder_error"
	EventHeartbeat        PoolEventKind = "heartbeat"
	EventSummary          PoolEventKind = "summary"
	EventGroundingWarning PoolEventKind = "grounding_warning"
	EventSummaryTruncated PoolEventKind = "summary_truncated"
)

// PoolEvent is one NDJSON-shaped progress message emitted by RunPool.
// Fields not relevant to a given Kind are zero. JSON keys mirror the
// schema documented in PROPOSAL2.md §"Progress events".
type PoolEvent struct {
	Kind         PoolEventKind `json:"event"`
	Timestamp    time.Time     `json:"timestamp,omitempty"`
	Path         string        `json:"path,omitempty"`
	Folder       string        `json:"folder,omitempty"`
	Files        int           `json:"files,omitempty"`
	Words        int           `json:"words,omitempty"`
	Ms           int64         `json:"ms,omitempty"`
	TokensIn     int           `json:"tokens_in,omitempty"`
	TokensOut    int           `json:"tokens_out,omitempty"`
	CostUSD      float64       `json:"cost_usd,omitempty"`
	Attempts     int           `json:"attempts,omitempty"`
	Err          string        `json:"err,omitempty"`
	DeadLettered bool          `json:"dead_lettered,omitempty"`

	// Heartbeat fields.
	InFlight       int     `json:"in_flight,omitempty"`
	Completed      int     `json:"completed,omitempty"`
	QueueRemaining int     `json:"queue_remaining,omitempty"`
	ElapsedSec     float64 `json:"elapsed_s,omitempty"`
	ETASec         float64 `json:"eta_s,omitempty"`
	SpentUSD       float64 `json:"spent_usd,omitempty"`

	// scan_done fields.
	FoldersTotal int `json:"folders_total,omitempty"`
	Stale        int `json:"stale,omitempty"`
	Skipped      int `json:"skipped,omitempty"`

	// summary fields (mirrors PROPOSAL2 schema).
	Summary *RunSummary `json:"summary,omitempty"`

	// Warning carries the human-readable message for diagnostic events
	// like grounding_warning and summary_truncated. Empty for other
	// kinds. The accompanying Path field identifies the folder.
	Warning string `json:"warning,omitempty"`
}

// RunSummary is the final stats payload attached to the summary event.
// Mirrors PROPOSAL2.md §"Final stats summary".
type RunSummary struct {
	Collection          string   `json:"collection,omitempty"`
	FoldersTotal        int      `json:"folders_total"`
	FoldersStale        int      `json:"folders_stale"`
	FoldersDone         int      `json:"folders_done"`
	FoldersFailed       int      `json:"folders_failed"`
	FoldersDeadLettered int      `json:"folders_dead_lettered"`
	WallSeconds         float64  `json:"wall_seconds"`
	Concurrency         int      `json:"concurrency"`
	CallsTotal          int      `json:"calls_total"`
	CallsRetried        int      `json:"calls_retried"`
	RateLimited         int      `json:"rate_limited"`
	TimedOut            int      `json:"timed_out"`
	TokensIn            int      `json:"tokens_in"`
	TokensOut           int      `json:"tokens_out"`
	CostUSD             float64  `json:"cost_usd"`
	LatencyMsP50        int64    `json:"latency_ms_p50"`
	LatencyMsP95        int64    `json:"latency_ms_p95"`
	Interrupted         bool     `json:"interrupted"`
	DeadLetterPaths     []string `json:"dead_letter_paths,omitempty"`
}
