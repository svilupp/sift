package eval

import (
	"math"
	"testing"
)

func almostEqual(a, b, tol float64) bool {
	return math.Abs(a-b) < tol
}

func TestComputeMetrics(t *testing.T) {
	cases := []CaseResult{
		{
			CaseID:  "c1",
			Variant: V0,
			Family:  "direct",
			Gold:    []GoldTarget{{Path: "a.md", SectionID: "intro"}},
			TopResults: []ResultEntry{
				{FilePath: "a.md", SectionID: "intro", Rank: 1, Score: 0.9},
				{FilePath: "b.md", SectionID: "other", Rank: 2, Score: 0.5},
				{FilePath: "c.md", SectionID: "misc", Rank: 3, Score: 0.3},
			},
			HitAt1:      true,
			HitAt3:      true,
			SectionHit1: true,
			SectionHit3: true,
			MRR:         1.0,
			LatencyMs:   5.0,
		},
		{
			CaseID:  "c2",
			Variant: V0,
			Family:  "direct",
			Gold:    []GoldTarget{{Path: "d.md", SectionID: "setup"}},
			TopResults: []ResultEntry{
				{FilePath: "x.md", SectionID: "other", Rank: 1, Score: 0.8},
				{FilePath: "y.md", SectionID: "blah", Rank: 2, Score: 0.6},
				{FilePath: "d.md", SectionID: "setup", Rank: 3, Score: 0.4},
			},
			HitAt1:      false,
			HitAt3:      true,
			SectionHit1: false,
			SectionHit3: true,
			MRR:         1.0 / 3.0,
			LatencyMs:   10.0,
		},
		{
			CaseID:  "c3",
			Variant: V0,
			Family:  "direct",
			Gold:    []GoldTarget{{Path: "z.md", SectionID: "final"}},
			TopResults: []ResultEntry{
				{FilePath: "q.md", SectionID: "nope", Rank: 1, Score: 0.7},
			},
			HitAt1:      false,
			HitAt3:      false,
			SectionHit1: false,
			SectionHit3: false,
			MRR:         0.0,
			LatencyMs:   3.0,
		},
	}

	rr := &RunResult{Variant: V0, Cases: cases}
	m := ComputeMetrics(rr)

	if m.CaseCount != 3 {
		t.Errorf("CaseCount = %d, want 3", m.CaseCount)
	}

	// hit@1: 1/3
	if !almostEqual(m.HitAt1Rate, 1.0/3.0, 0.01) {
		t.Errorf("HitAt1Rate = %.4f, want %.4f", m.HitAt1Rate, 1.0/3.0)
	}

	// hit@3: 2/3
	if !almostEqual(m.HitAt3Rate, 2.0/3.0, 0.01) {
		t.Errorf("HitAt3Rate = %.4f, want %.4f", m.HitAt3Rate, 2.0/3.0)
	}

	// sec@1: 1/3
	if !almostEqual(m.SectionHitAt1, 1.0/3.0, 0.01) {
		t.Errorf("SectionHitAt1 = %.4f, want %.4f", m.SectionHitAt1, 1.0/3.0)
	}

	// sec@3: 2/3
	if !almostEqual(m.SectionHitAt3, 2.0/3.0, 0.01) {
		t.Errorf("SectionHitAt3 = %.4f, want %.4f", m.SectionHitAt3, 2.0/3.0)
	}

	// MRR: (1.0 + 1/3 + 0) / 3 = 0.4444
	wantMRR := (1.0 + 1.0/3.0 + 0.0) / 3.0
	if !almostEqual(m.MeanMRR, wantMRR, 0.01) {
		t.Errorf("MeanMRR = %.4f, want %.4f", m.MeanMRR, wantMRR)
	}

	// Median latency: sorted [3.0, 5.0, 10.0] -> 5.0
	if !almostEqual(m.MedianLatencyMs, 5.0, 0.01) {
		t.Errorf("MedianLatencyMs = %.2f, want 5.0", m.MedianLatencyMs)
	}

	// P95 latency: near 10.0
	if m.P95LatencyMs < 9.0 {
		t.Errorf("P95LatencyMs = %.2f, want >= 9.0", m.P95LatencyMs)
	}
}

func TestComputeMetrics_Empty(t *testing.T) {
	rr := &RunResult{Variant: V1, Cases: nil}
	m := ComputeMetrics(rr)
	if m.CaseCount != 0 {
		t.Errorf("CaseCount = %d, want 0", m.CaseCount)
	}
	if m.MeanMRR != 0 {
		t.Errorf("MeanMRR = %f, want 0", m.MeanMRR)
	}
}

func TestComputeMetricsByFamily(t *testing.T) {
	cases := []CaseResult{
		{CaseID: "c1", Family: "direct", HitAt1: true, MRR: 1.0, LatencyMs: 2.0,
			Gold:       []GoldTarget{{Path: "a.md"}},
			TopResults: []ResultEntry{{FilePath: "a.md", Rank: 1}}},
		{CaseID: "c2", Family: "derived_to_raw", HitAt1: false, MRR: 0.5, LatencyMs: 4.0,
			Gold:       []GoldTarget{{Path: "b.md"}},
			TopResults: []ResultEntry{{FilePath: "x.md", Rank: 1}, {FilePath: "b.md", Rank: 2}}},
	}

	rr := &RunResult{Variant: V0, Cases: cases}
	byFam := ComputeMetricsByFamily(rr)

	if len(byFam) != 2 {
		t.Fatalf("expected 2 families, got %d", len(byFam))
	}

	dm := byFam["direct"]
	if dm.CaseCount != 1 {
		t.Errorf("direct CaseCount = %d, want 1", dm.CaseCount)
	}
	if !almostEqual(dm.HitAt1Rate, 1.0, 0.01) {
		t.Errorf("direct HitAt1Rate = %.2f, want 1.0", dm.HitAt1Rate)
	}

	drm := byFam["derived_to_raw"]
	if drm.CaseCount != 1 {
		t.Errorf("derived_to_raw CaseCount = %d, want 1", drm.CaseCount)
	}
}

func TestPercentile(t *testing.T) {
	tests := []struct {
		name   string
		vals   []float64
		p      float64
		expect float64
	}{
		{"single", []float64{5.0}, 0.5, 5.0},
		{"two_median", []float64{1.0, 3.0}, 0.5, 2.0},
		{"three_p50", []float64{1.0, 2.0, 3.0}, 0.5, 2.0},
		{"empty", nil, 0.5, 0.0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := percentile(tt.vals, tt.p)
			if !almostEqual(got, tt.expect, 0.01) {
				t.Errorf("percentile(%v, %.2f) = %.4f, want %.4f", tt.vals, tt.p, got, tt.expect)
			}
		})
	}
}
