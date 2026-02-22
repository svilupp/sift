package search

import (
	"sort"
)

// RankedResult is an input to RRF fusion: a document with a rank position.
type RankedResult struct {
	ChunkID int64
	Rank    int     // 1-based rank position
	Score   float64 // Original score (BM25 or vector similarity)
}

// FusedResult is the output of RRF fusion.
type FusedResult struct {
	ChunkID  int64
	RRFScore float64
	BM25Rank int // 0 means not in BM25 results
	VecRank  int // 0 means not in vector results
}

// FuseRRF implements Reciprocal Rank Fusion with position boosting.
// Formula: score(doc) = sum( signalWeight * weight(rank) / (k + rank) )
// where weight(rank) = boostFactor if rank <= boostTopN, else 1.0
// and signalWeight is bm25Weight or vectorWeight for the respective signal.
// k is typically 60.
func FuseRRF(bm25 []RankedResult, vector []RankedResult, k int, boostTopN int, boostFactor float64, bm25Weight float64, vectorWeight float64) []FusedResult {
	if k <= 0 {
		k = 60
	}
	if boostTopN < 0 {
		boostTopN = 0
	}
	if boostFactor <= 0 {
		boostFactor = 1.0
	}
	if bm25Weight <= 0 {
		bm25Weight = 1.0
	}
	if vectorWeight <= 0 {
		vectorWeight = 1.0
	}

	// Map chunk ID to fused result.
	results := make(map[int64]*FusedResult)

	for _, r := range bm25 {
		fr, ok := results[r.ChunkID]
		if !ok {
			fr = &FusedResult{ChunkID: r.ChunkID}
			results[r.ChunkID] = fr
		}
		fr.BM25Rank = r.Rank

		weight := 1.0
		if r.Rank <= boostTopN {
			weight = boostFactor
		}
		fr.RRFScore += bm25Weight * weight / float64(k+r.Rank)
	}

	for _, r := range vector {
		fr, ok := results[r.ChunkID]
		if !ok {
			fr = &FusedResult{ChunkID: r.ChunkID}
			results[r.ChunkID] = fr
		}
		fr.VecRank = r.Rank

		weight := 1.0
		if r.Rank <= boostTopN {
			weight = boostFactor
		}
		fr.RRFScore += vectorWeight * weight / float64(k+r.Rank)
	}

	// Convert map to slice and sort by RRF score descending.
	fused := make([]FusedResult, 0, len(results))
	for _, fr := range results {
		fused = append(fused, *fr)
	}

	sort.Slice(fused, func(i, j int) bool {
		return fused[i].RRFScore > fused[j].RRFScore
	})

	return fused
}
