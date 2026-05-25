package eval

import (
	"context"
	"time"
)

// Variant identifies which experiment variant to run.
type Variant string

const (
	V0 Variant = "v0_baseline"
	V1 Variant = "v1_section"
	V2 Variant = "v2_section_links"
	V3 Variant = "v3_heading_boost"
	V4 Variant = "v4_heading_links"
)

// CaseResult holds the output of running one case under one variant.
type CaseResult struct {
	CaseID      string        `json:"case_id"`
	Variant     Variant       `json:"variant"`
	Query       string        `json:"query"`
	Gold        []GoldTarget  `json:"gold"`
	Family      string        `json:"family"`
	TopResults  []ResultEntry `json:"top_results"`
	HitAt1      bool          `json:"hit_at1"`
	HitAt3      bool          `json:"hit_at3"`
	MRR         float64       `json:"mrr"`
	SectionHit1 bool          `json:"section_hit_at1"`
	SectionHit3 bool          `json:"section_hit_at3"`
	LatencyMs   float64       `json:"latency_ms"`
	Error       string        `json:"error,omitempty"`
}

// ResultEntry is a simplified result for the report.
type ResultEntry struct {
	FilePath  string  `json:"file_path"`
	SectionID string  `json:"section_id"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Score     float64 `json:"score"`
	Rank      int     `json:"rank"`
}

// RunConfig holds configuration for an eval run.
type RunConfig struct {
	Variant      Variant
	CasesPath    string
	CorpusPath   string
	Collection   string
	TopK         int
	HeadingBoost float64
	Decay        float64
}

// RunResult holds all case results for one variant run.
type RunResult struct {
	Variant   Variant      `json:"variant"`
	Cases     []CaseResult `json:"cases"`
	StartTime time.Time    `json:"start_time"`
	EndTime   time.Time    `json:"end_time"`
}

// SectionMapper remaps chunk results to section results.
// Implemented using the anchor package.
type SectionMapper interface {
	MapToSections(results []ResultEntry) []ResultEntry
	ExpandLinks(results []ResultEntry, decay float64) []ResultEntry
	BoostHeadings(query string, results []ResultEntry, factor float64) []ResultEntry
}

// Runner executes eval cases against a sift instance.
type Runner struct {
	// SearchFn returns: results, latencyMs, error.
	// The CLI wires this to the actual search engine.
	SearchFn func(ctx context.Context, query string, topK int) ([]ResultEntry, float64, error)

	// SectionMap provides section aggregation for V1-V4.
	// Nil means section mapping is unavailable.
	SectionMap SectionMapper
}

// Run executes all cases for a given variant.
func (r *Runner) Run(ctx context.Context, cfg RunConfig, cases []Case) (*RunResult, error) {
	rr := &RunResult{
		Variant:   cfg.Variant,
		StartTime: time.Now(),
	}

	for _, c := range cases {
		cr := r.runCase(ctx, cfg, c)
		rr.Cases = append(rr.Cases, cr)
	}

	rr.EndTime = time.Now()
	return rr, nil
}

func (r *Runner) runCase(ctx context.Context, cfg RunConfig, c Case) CaseResult {
	cr := CaseResult{
		CaseID:  c.ID,
		Variant: cfg.Variant,
		Query:   c.Query,
		Gold:    c.Gold,
		Family:  c.Family,
	}

	results, latMs, err := r.SearchFn(ctx, c.Query, cfg.TopK)
	if err != nil {
		cr.Error = err.Error()
		cr.LatencyMs = latMs
		return cr
	}

	// Apply section aggregation for V1/V2/V3/V4.
	if cfg.Variant == V1 || cfg.Variant == V2 || cfg.Variant == V3 || cfg.Variant == V4 {
		if r.SectionMap != nil {
			results = r.SectionMap.MapToSections(results)
		}
	}
	if cfg.Variant == V2 {
		if r.SectionMap != nil {
			results = r.SectionMap.ExpandLinks(results, cfg.Decay)
		}
	}
	if cfg.Variant == V3 || cfg.Variant == V4 {
		if r.SectionMap != nil {
			results = r.SectionMap.BoostHeadings(c.Query, results, cfg.HeadingBoost)
		}
	}
	if cfg.Variant == V4 {
		if r.SectionMap != nil {
			results = r.SectionMap.ExpandLinks(results, cfg.Decay)
		}
	}

	// Re-assign ranks after remapping.
	for i := range results {
		results[i].Rank = i + 1
	}

	// Trim to topK.
	if len(results) > cfg.TopK {
		results = results[:cfg.TopK]
	}

	cr.TopResults = results
	cr.LatencyMs = latMs

	computeCaseMetrics(&cr)
	return cr
}

func computeCaseMetrics(cr *CaseResult) {
	if len(cr.TopResults) == 0 || len(cr.Gold) == 0 {
		return
	}

	// Build gold lookup sets.
	goldPaths := make(map[string]bool, len(cr.Gold))
	goldSections := make(map[string]bool, len(cr.Gold))
	for _, g := range cr.Gold {
		goldPaths[g.Path] = true
		if g.SectionID != "" {
			goldSections[g.SectionID] = true
		}
	}

	// HitAt1/3: file path match.
	for i, res := range cr.TopResults {
		if goldPaths[res.FilePath] {
			if i == 0 {
				cr.HitAt1 = true
			}
			if i < 3 {
				cr.HitAt3 = true
			}
		}
	}

	// SectionHit1/3: exact section match.
	for i, res := range cr.TopResults {
		if res.SectionID != "" && goldSections[res.SectionID] {
			if i == 0 {
				cr.SectionHit1 = true
			}
			if i < 3 {
				cr.SectionHit3 = true
			}
		}
	}

	// MRR: 1/rank of first gold hit.
	// Use section_id if available, else file path.
	for _, res := range cr.TopResults {
		matched := false
		if res.SectionID != "" && goldSections[res.SectionID] {
			matched = true
		} else if goldPaths[res.FilePath] {
			matched = true
		}
		if matched {
			cr.MRR = 1.0 / float64(res.Rank)
			break
		}
	}
}
