package search

import (
	"math"
	"testing"
)

func TestFuseRRF_KnownLists(t *testing.T) {
	// BM25: A=rank1, B=rank2, C=rank3
	// Vector: C=rank1, A=rank2, B=rank3
	bm25 := []RankedResult{
		{ChunkID: 1, Rank: 1}, // A
		{ChunkID: 2, Rank: 2}, // B
		{ChunkID: 3, Rank: 3}, // C
	}
	vector := []RankedResult{
		{ChunkID: 3, Rank: 1}, // C
		{ChunkID: 1, Rank: 2}, // A
		{ChunkID: 2, Rank: 3}, // B
	}

	results := FuseRRF(bm25, vector, 60, 0, 1.0, 1.0, 1.0) // no boosting

	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Expected scores (no boosting, all weight=1):
	// A: 1/(60+1) + 1/(60+2) = 1/61 + 1/62
	// B: 1/(60+2) + 1/(60+3) = 1/62 + 1/63
	// C: 1/(60+3) + 1/(60+1) = 1/63 + 1/61
	// A = 1/61 + 1/62 ≈ 0.016393 + 0.016129 = 0.032522
	// C = 1/61 + 1/63 ≈ 0.016393 + 0.015873 = 0.032266
	// B = 1/62 + 1/63 ≈ 0.016129 + 0.015873 = 0.032002

	// Verify ordering: A > C > B
	scoreMap := make(map[int64]float64)
	for _, r := range results {
		scoreMap[r.ChunkID] = r.RRFScore
	}

	if scoreMap[1] <= scoreMap[3] {
		t.Errorf("A (%.6f) should rank above C (%.6f)", scoreMap[1], scoreMap[3])
	}
	if scoreMap[3] <= scoreMap[2] {
		t.Errorf("C (%.6f) should rank above B (%.6f)", scoreMap[3], scoreMap[2])
	}

	// Verify A is first result.
	if results[0].ChunkID != 1 {
		t.Errorf("expected first result to be A (chunk 1), got chunk %d", results[0].ChunkID)
	}

	// Verify formula values.
	expectedA := 1.0/61.0 + 1.0/62.0
	if math.Abs(scoreMap[1]-expectedA) > 1e-10 {
		t.Errorf("A score = %f, expected %f", scoreMap[1], expectedA)
	}
}

func TestFuseRRF_WithBoosting(t *testing.T) {
	// Test that boosting changes results.
	bm25 := []RankedResult{
		{ChunkID: 1, Rank: 1},
		{ChunkID: 2, Rank: 2},
		{ChunkID: 3, Rank: 3},
		{ChunkID: 4, Rank: 4},
	}
	vector := []RankedResult{
		{ChunkID: 4, Rank: 1},
		{ChunkID: 3, Rank: 2},
		{ChunkID: 2, Rank: 3},
		{ChunkID: 1, Rank: 4},
	}

	// With boosting: top 3 get 2x weight.
	results := FuseRRF(bm25, vector, 60, 3, 2.0, 1.0, 1.0)

	// Verify top 3 positions get boosted.
	// Chunk 1: BM25 rank 1 (boosted), Vector rank 4 (not boosted)
	//   = 2.0/(60+1) + 1.0/(60+4) = 2/61 + 1/64
	// Chunk 4: BM25 rank 4 (not boosted), Vector rank 1 (boosted)
	//   = 1.0/(60+4) + 2.0/(60+1) = 1/64 + 2/61
	// These should be equal (same formula, symmetric)

	scoreMap := make(map[int64]float64)
	for _, r := range results {
		scoreMap[r.ChunkID] = r.RRFScore
	}

	// Chunk 1 and Chunk 4 should have equal scores.
	if math.Abs(scoreMap[1]-scoreMap[4]) > 1e-10 {
		t.Errorf("chunk 1 (%.6f) and chunk 4 (%.6f) should have equal scores", scoreMap[1], scoreMap[4])
	}

	// Chunk 2 and Chunk 3 should have equal scores.
	if math.Abs(scoreMap[2]-scoreMap[3]) > 1e-10 {
		t.Errorf("chunk 2 (%.6f) and chunk 3 (%.6f) should have equal scores", scoreMap[2], scoreMap[3])
	}

	// Boosted entries should score higher than unboosted with same symmetry.
	// Chunk 2: 2/62 + 2/63 (both boosted, rank 2 and 3)
	// Chunk 1: 2/61 + 1/64 (one boosted rank 1, one not boosted rank 4)
	// Chunk 2 score ≈ 0.0323 + 0.0317 = 0.0640
	// Chunk 1 score ≈ 0.0328 + 0.0156 = 0.0484
	// So chunk 2 > chunk 1
	if scoreMap[2] <= scoreMap[1] {
		t.Errorf("chunk 2 (%.6f) should rank above chunk 1 (%.6f) with boosting", scoreMap[2], scoreMap[1])
	}
}

func TestFuseRRF_SingleList(t *testing.T) {
	// Only BM25 results, no vector.
	bm25 := []RankedResult{
		{ChunkID: 1, Rank: 1},
		{ChunkID: 2, Rank: 2},
	}

	results := FuseRRF(bm25, nil, 60, 0, 1.0, 1.0, 1.0)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ChunkID != 1 {
		t.Errorf("expected first result chunk 1, got %d", results[0].ChunkID)
	}
	if results[0].BM25Rank != 1 {
		t.Errorf("expected BM25Rank=1, got %d", results[0].BM25Rank)
	}
	if results[0].VecRank != 0 {
		t.Errorf("expected VecRank=0, got %d", results[0].VecRank)
	}
}

func TestFuseRRF_Empty(t *testing.T) {
	results := FuseRRF(nil, nil, 60, 0, 1.0, 1.0, 1.0)
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

func TestFuseRRF_DefaultK(t *testing.T) {
	bm25 := []RankedResult{{ChunkID: 1, Rank: 1}}
	results := FuseRRF(bm25, nil, 0, 0, 1.0, 1.0, 1.0) // k=0 should default to 60
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	expected := 1.0 / 61.0
	if math.Abs(results[0].RRFScore-expected) > 1e-10 {
		t.Errorf("score = %f, expected %f (k defaulted to 60)", results[0].RRFScore, expected)
	}
}

func TestFuseRRF_AsymmetricWeights(t *testing.T) {
	// BM25: A=rank1, B=rank2
	// Vector: B=rank1, A=rank2
	// With equal weights these would be tied. With bm25Weight=2.0, vector=1.0
	// the BM25-favored result (A, rank 1 in BM25) should win.
	bm25 := []RankedResult{
		{ChunkID: 1, Rank: 1}, // A
		{ChunkID: 2, Rank: 2}, // B
	}
	vector := []RankedResult{
		{ChunkID: 2, Rank: 1}, // B
		{ChunkID: 1, Rank: 2}, // A
	}

	results := FuseRRF(bm25, vector, 60, 0, 1.0, 2.0, 1.0)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	scoreMap := make(map[int64]float64)
	for _, r := range results {
		scoreMap[r.ChunkID] = r.RRFScore
	}

	// A: bm25Weight*1/(60+1) + vectorWeight*1/(60+2) = 2.0/61 + 1.0/62
	expectedA := 2.0/61.0 + 1.0/62.0
	// B: bm25Weight*1/(60+2) + vectorWeight*1/(60+1) = 2.0/62 + 1.0/61
	expectedB := 2.0/62.0 + 1.0/61.0

	if math.Abs(scoreMap[1]-expectedA) > 1e-10 {
		t.Errorf("A score = %f, expected %f", scoreMap[1], expectedA)
	}
	if math.Abs(scoreMap[2]-expectedB) > 1e-10 {
		t.Errorf("B score = %f, expected %f", scoreMap[2], expectedB)
	}

	// A should rank higher because BM25 weight is doubled and A has BM25 rank 1.
	if scoreMap[1] <= scoreMap[2] {
		t.Errorf("A (%.6f) should rank above B (%.6f) with bm25Weight=2.0", scoreMap[1], scoreMap[2])
	}
	if results[0].ChunkID != 1 {
		t.Errorf("expected first result to be A (chunk 1), got chunk %d", results[0].ChunkID)
	}
}
