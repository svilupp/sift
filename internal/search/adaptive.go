package search

import "math"

// AdaptiveTopK determines how many results to return based on how steeply
// scores decline from position 1 to position 5. Steep decline means clear
// winners (return fewer); flat scores mean no clear winners (return more).
//
// The algorithm is scale-invariant: it works for both reranked scores (0-1)
// and RRF scores (0-0.05) because it uses ratios, not absolute values.
func AdaptiveTopK(scores []float64, minK, maxK int) int {
	if minK > maxK {
		minK, maxK = maxK, minK
	}
	if minK == maxK {
		return minK
	}

	kneePos := 4 // 5th result, 0-indexed

	if len(scores) == 0 {
		return 0
	}
	if len(scores) <= kneePos {
		return len(scores)
	}
	if scores[0] == 0 {
		return maxK
	}

	dropRatio := 1.0 - (scores[kneePos] / scores[0])

	// Clamp negative drop ratios (non-monotonic scores).
	if dropRatio < 0 {
		dropRatio = 0
	}

	// Check if there's a meaningful cliff (at least 5% single-step drop).
	hasCliff := false
	for i := range kneePos {
		if scores[i] > 0 {
			stepDrop := (scores[i] - scores[i+1]) / scores[i]
			if stepDrop > 0.05 {
				hasCliff = true
				break
			}
		}
	}
	if !hasCliff {
		return maxK
	}

	if dropRatio >= 0.5 {
		return minK
	}
	if dropRatio <= 0.1 {
		return maxK
	}

	// Linear interpolation between maxK (at 0.1) and minK (at 0.5).
	k := float64(maxK) - (dropRatio-0.1)/(0.5-0.1)*float64(maxK-minK)
	return int(math.Round(clamp(k, float64(minK), float64(maxK))))
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
