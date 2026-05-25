package search

import "testing"

func TestAdaptiveTopK(t *testing.T) {
	tests := []struct {
		name   string
		scores []float64
		minK   int
		maxK   int
		want   int
	}{
		{
			name:   "empty scores",
			scores: nil,
			minK:   10, maxK: 20,
			want: 0,
		},
		{
			name:   "1 score",
			scores: []float64{0.9},
			minK:   10, maxK: 20,
			want: 1,
		},
		{
			name:   "4 scores",
			scores: []float64{0.9, 0.8, 0.7, 0.6},
			minK:   10, maxK: 20,
			want: 4,
		},
		{
			name:   "steep decline returns minK",
			scores: []float64{0.92, 0.85, 0.71, 0.40, 0.22},
			minK:   10, maxK: 20,
			want: 10,
		},
		{
			name:   "flat scores returns maxK (no cliff)",
			scores: []float64{0.45, 0.43, 0.41, 0.40, 0.39},
			minK:   10, maxK: 20,
			// No single step exceeds 5% relative drop → no cliff → maxK
			want: 20,
		},
		{
			name:   "all-zero scores returns maxK",
			scores: []float64{0, 0, 0, 0, 0},
			minK:   10, maxK: 20,
			want: 20,
		},
		{
			name:   "RRF-scale flat scores",
			scores: []float64{0.033, 0.032, 0.032, 0.031, 0.031},
			minK:   10, maxK: 20,
			want: 20, // dropRatio = 1 - 0.031/0.033 = 0.06 → maxK
		},
		{
			name:   "RRF-scale steep scores",
			scores: []float64{0.066, 0.033, 0.020, 0.015, 0.012},
			minK:   10, maxK: 20,
			want: 10, // dropRatio = 1 - 0.012/0.066 = 0.818 → minK
		},
		{
			name:   "boundary: dropRatio exactly 0.5 returns minK",
			scores: []float64{1.0, 0.9, 0.8, 0.7, 0.5},
			minK:   10, maxK: 20,
			want: 10,
		},
		{
			name:   "boundary: dropRatio exactly 0.1 returns maxK",
			scores: []float64{1.0, 0.99, 0.98, 0.95, 0.9},
			minK:   10, maxK: 20,
			want: 20,
		},
		{
			name:   "intermediate case",
			scores: []float64{1.0, 0.9, 0.8, 0.7, 0.65},
			minK:   10, maxK: 20,
			// dropRatio = 1 - 0.65/1.0 = 0.35
			// k = 20 - (0.35 - 0.1)/(0.5 - 0.1) * 10 = 20 - 6.25 = 13.75 → 14
			want: 14,
		},
		{
			name:   "non-monotonic scores clamped",
			scores: []float64{0.30, 0.50, 0.60, 0.70, 0.80},
			minK:   10, maxK: 20,
			// dropRatio = 1 - 0.80/0.30 = -1.67, clamped to 0 → maxK
			want: 20,
		},
		{
			name:   "minK > maxK swapped",
			scores: []float64{0.92, 0.85, 0.71, 0.40, 0.22},
			minK:   20, maxK: 10,
			want: 10,
		},
		{
			name:   "minK == maxK always returns that value",
			scores: []float64{0.92, 0.85, 0.71, 0.40, 0.22},
			minK:   15, maxK: 15,
			want: 15,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := AdaptiveTopK(tt.scores, tt.minK, tt.maxK)
			if got != tt.want {
				t.Errorf("AdaptiveTopK(%v, %d, %d) = %d, want %d", tt.scores, tt.minK, tt.maxK, got, tt.want)
			}
		})
	}
}
