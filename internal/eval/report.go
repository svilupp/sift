package eval

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"
)

// Report holds comparison data across variants.
type Report struct {
	Variants []Metrics            `json:"variants"`
	ByFamily map[string][]Metrics `json:"by_family"`
	Cases    []CaseComparison     `json:"cases,omitempty"`
}

// CaseComparison shows one case across all variants.
type CaseComparison struct {
	CaseID  string                 `json:"case_id"`
	Query   string                 `json:"query"`
	Gold    []GoldTarget           `json:"gold"`
	Results map[Variant]CaseResult `json:"results"`
}

// BuildReport combines multiple RunResults into a comparative Report.
func BuildReport(runs ...*RunResult) *Report {
	r := &Report{
		ByFamily: make(map[string][]Metrics),
	}

	// Aggregate metrics per variant.
	for _, run := range runs {
		m := ComputeMetrics(run)
		r.Variants = append(r.Variants, *m)

		byFam := ComputeMetricsByFamily(run)
		for fam, fm := range byFam {
			r.ByFamily[fam] = append(r.ByFamily[fam], *fm)
		}
	}

	// Build case comparisons.
	caseMap := make(map[string]*CaseComparison)
	for _, run := range runs {
		for _, cr := range run.Cases {
			cc, ok := caseMap[cr.CaseID]
			if !ok {
				cc = &CaseComparison{
					CaseID:  cr.CaseID,
					Query:   cr.Query,
					Gold:    cr.Gold,
					Results: make(map[Variant]CaseResult),
				}
				caseMap[cr.CaseID] = cc
			}
			cc.Results[cr.Variant] = cr
		}
	}
	for _, cc := range caseMap {
		r.Cases = append(r.Cases, *cc)
	}

	return r
}

// WriteReport writes a human-readable report to w.
func WriteReport(w io.Writer, report *Report) error {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)

	fmt.Fprintln(tw, "Variant\thit@1\thit@3\tsec@1\tsec@3\tMRR\tp50ms\tp95ms")
	for _, m := range report.Variants {
		fmt.Fprintf(tw, "%s\t%.2f\t%.2f\t%.2f\t%.2f\t%.2f\t%.1f\t%.1f\n",
			m.Variant,
			m.HitAt1Rate, m.HitAt3Rate,
			m.SectionHitAt1, m.SectionHitAt3,
			m.MeanMRR,
			m.MedianLatencyMs, m.P95LatencyMs,
		)
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush table: %w", err)
	}

	// Per-family breakdown if present.
	if len(report.ByFamily) > 0 {
		fmt.Fprintln(w)
		for fam, metrics := range report.ByFamily {
			fmt.Fprintf(w, "--- Family: %s ---\n", fam)
			tw2 := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
			fmt.Fprintln(tw2, "Variant\thit@1\thit@3\tsec@1\tsec@3\tMRR\tp50ms\tp95ms")
			for _, m := range metrics {
				fmt.Fprintf(tw2, "%s\t%.2f\t%.2f\t%.2f\t%.2f\t%.2f\t%.1f\t%.1f\n",
					m.Variant,
					m.HitAt1Rate, m.HitAt3Rate,
					m.SectionHitAt1, m.SectionHitAt3,
					m.MeanMRR,
					m.MedianLatencyMs, m.P95LatencyMs,
				)
			}
			if err := tw2.Flush(); err != nil {
				return fmt.Errorf("flush family table: %w", err)
			}
		}
	}

	return nil
}

// WriteJSON writes the report as JSON to w.
func WriteJSON(w io.Writer, report *Report) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return fmt.Errorf("encode report: %w", err)
	}
	return nil
}
