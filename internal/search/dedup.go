package search

import (
	"sift/internal/fileutil"

	"github.com/cespare/xxhash/v2"
)

// DuplicateRef tracks a duplicate chunk that was folded into the primary result.
type DuplicateRef struct {
	File      string  `json:"file"`
	StartLine int     `json:"start_line"`
	EndLine   int     `json:"end_line"`
	Score     float64 `json:"score"`
}

// DedupIdenticalContent groups candidates by content hash (xxhash of chunk text).
// For each group, keeps the highest-scored candidate and builds DuplicateRef entries
// for the rest. Returns filtered candidates and a map of chunkID -> duplicates.
func DedupIdenticalContent(candidates []scoredCandidate) ([]scoredCandidate, map[int64][]DuplicateRef) {
	if len(candidates) == 0 {
		return candidates, nil
	}

	// Group candidates by content hash.
	type group struct {
		bestIdx int
		indices []int
	}
	groups := make(map[uint64]*group)

	for i, c := range candidates {
		content := fileutil.ReadLines(c.fileRec.Path, c.chunkRec.StartLine, c.chunkRec.EndLine, 0)
		h := xxhash.Sum64([]byte(content))

		g, ok := groups[h]
		if !ok {
			groups[h] = &group{bestIdx: i, indices: []int{i}}
			continue
		}
		g.indices = append(g.indices, i)
		// Keep the highest-scored candidate as the winner.
		if candidates[i].rerankScore > candidates[g.bestIdx].rerankScore {
			g.bestIdx = i
		}
	}

	// Build filtered list and dedup map.
	winners := make(map[int]bool, len(groups))
	dedupMap := make(map[int64][]DuplicateRef)

	for _, g := range groups {
		winners[g.bestIdx] = true
		if len(g.indices) <= 1 {
			continue
		}
		winnerID := candidates[g.bestIdx].fused.ChunkID
		for _, idx := range g.indices {
			if idx == g.bestIdx {
				continue
			}
			c := candidates[idx]
			dedupMap[winnerID] = append(dedupMap[winnerID], DuplicateRef{
				File:      c.fileRec.Path,
				StartLine: c.chunkRec.StartLine,
				EndLine:   c.chunkRec.EndLine,
				Score:     c.rerankScore,
			})
		}
	}

	// Preserve original ordering among winners.
	filtered := make([]scoredCandidate, 0, len(winners))
	for i, c := range candidates {
		if winners[i] {
			filtered = append(filtered, c)
		}
	}

	return filtered, dedupMap
}
