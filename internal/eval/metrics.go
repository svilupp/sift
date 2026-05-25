package eval

import (
	"math"
	"sort"
)

// Metrics holds aggregate metrics for a variant run.
type Metrics struct {
	Variant         Variant `json:"variant"`
	CaseCount       int     `json:"case_count"`
	HitAt1Rate      float64 `json:"hit_at1_rate"`
	HitAt3Rate      float64 `json:"hit_at3_rate"`
	SectionHitAt1   float64 `json:"section_hit_at1_rate"`
	SectionHitAt3   float64 `json:"section_hit_at3_rate"`
	MeanMRR         float64 `json:"mean_mrr"`
	MedianLatencyMs float64 `json:"median_latency_ms"`
	P95LatencyMs    float64 `json:"p95_latency_ms"`

	// V2-specific: for derived_to_raw family.
	RawSourceTop3Rate   float64 `json:"raw_source_top3_rate,omitempty"`
	DerivedOutranksRate float64 `json:"derived_outranks_rate,omitempty"`

	// Noise metrics.
	WrongFileTop3Rate    float64 `json:"wrong_file_top3_rate"`
	WrongSectionSameFile float64 `json:"wrong_section_same_file_rate"`
}

// ComputeMetrics aggregates CaseResults into Metrics.
func ComputeMetrics(results *RunResult) *Metrics {
	return computeMetrics(results.Variant, results.Cases)
}

// ComputeMetricsByFamily computes metrics grouped by case family.
func ComputeMetricsByFamily(results *RunResult) map[string]*Metrics {
	families := make(map[string][]CaseResult)
	for _, cr := range results.Cases {
		families[cr.Family] = append(families[cr.Family], cr)
	}
	out := make(map[string]*Metrics, len(families))
	for fam, cases := range families {
		out[fam] = computeMetrics(results.Variant, cases)
	}
	return out
}

func computeMetrics(v Variant, cases []CaseResult) *Metrics {
	n := len(cases)
	if n == 0 {
		return &Metrics{Variant: v}
	}

	m := &Metrics{
		Variant:   v,
		CaseCount: n,
	}

	var hitAt1, hitAt3, secHit1, secHit3 int
	var mrrSum float64
	var latencies []float64
	var wrongFileTop3, wrongSectionSameFile int
	var derivedCount, rawSourceTop3, derivedOutranks int

	for _, cr := range cases {
		if cr.HitAt1 {
			hitAt1++
		}
		if cr.HitAt3 {
			hitAt3++
		}
		if cr.SectionHit1 {
			secHit1++
		}
		if cr.SectionHit3 {
			secHit3++
		}
		mrrSum += cr.MRR
		latencies = append(latencies, cr.LatencyMs)

		// Noise: wrong file in top 3.
		goldPaths := make(map[string]bool, len(cr.Gold))
		goldSections := make(map[string]bool, len(cr.Gold))
		for _, g := range cr.Gold {
			goldPaths[g.Path] = true
			if g.SectionID != "" {
				goldSections[g.SectionID] = true
			}
		}

		for i, res := range cr.TopResults {
			if i >= 3 {
				break
			}
			if !goldPaths[res.FilePath] {
				wrongFileTop3++
			} else if res.SectionID != "" && !goldSections[res.SectionID] {
				wrongSectionSameFile++
			}
		}

		// Derived-to-raw family tracking.
		if cr.Family == "derived_to_raw" {
			derivedCount++
			for i, res := range cr.TopResults {
				if i >= 3 {
					break
				}
				for _, g := range cr.Gold {
					if res.FilePath == g.Path {
						rawSourceTop3++
						break
					}
				}
			}
			// Check if derived (non-gold) outranks gold.
			if len(cr.TopResults) > 0 && !goldPaths[cr.TopResults[0].FilePath] {
				derivedOutranks++
			}
		}
	}

	fn := float64(n)
	m.HitAt1Rate = float64(hitAt1) / fn
	m.HitAt3Rate = float64(hitAt3) / fn
	m.SectionHitAt1 = float64(secHit1) / fn
	m.SectionHitAt3 = float64(secHit3) / fn
	m.MeanMRR = mrrSum / fn

	sort.Float64s(latencies)
	m.MedianLatencyMs = percentile(latencies, 0.5)
	m.P95LatencyMs = percentile(latencies, 0.95)

	top3Slots := min(3, maxTopResults(cases))
	totalTop3 := float64(n * top3Slots)
	if totalTop3 > 0 {
		m.WrongFileTop3Rate = float64(wrongFileTop3) / totalTop3
		m.WrongSectionSameFile = float64(wrongSectionSameFile) / totalTop3
	}

	if derivedCount > 0 {
		m.RawSourceTop3Rate = float64(rawSourceTop3) / float64(derivedCount)
		m.DerivedOutranksRate = float64(derivedOutranks) / float64(derivedCount)
	}

	return m
}

func percentile(sorted []float64, p float64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sorted[0]
	}
	idx := p * float64(n-1)
	lo := int(math.Floor(idx))
	hi := int(math.Ceil(idx))
	if lo == hi {
		return sorted[lo]
	}
	frac := idx - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

func maxTopResults(cases []CaseResult) int {
	mx := 0
	for _, c := range cases {
		if len(c.TopResults) > mx {
			mx = len(c.TopResults)
		}
	}
	return mx
}
