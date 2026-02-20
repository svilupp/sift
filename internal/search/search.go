package search

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"time"

	"sift/internal/chunk"
	"sift/internal/config"
	"sift/internal/db"
	"sift/internal/fileutil"
	"sift/internal/index"
	"sift/internal/voyage"
)

// SearchOptions controls search behavior.
type SearchOptions struct {
	Collection string
	SinceUnix  int64
	TopK       int
	Threshold  float64
	Adaptive   bool
	PathGlob   string // glob pattern to filter by file path (SQLite GLOB syntax)
}

// Result represents a single search result with all metadata.
type Result struct {
	ChunkID      int64
	FilePath     string
	Collection   string
	StartLine    int
	EndLine      int
	BM25Rank     int
	VectorRank   int
	RRFScore     float64
	RerankScore  float64
	FinalScore   float64
	CollectionID int64
	Mtime        int64
	Highlights   []string
}

// SearchResult holds the complete search output.
type SearchResult struct {
	Results         []Result
	TotalBM25       int
	TotalVec        int
	BM25TimeMs      int64
	VecTimeMs       int64
	RerankTimeMs    int64
	Reranked        bool
	TotalCandidates int
	FilteredCount   int
	Threshold       float64
	ContentDedupMap map[int64][]DuplicateRef // chunk_id -> duplicates (nil if dedup disabled)
}

// Engine orchestrates BM25 + vector search + RRF fusion + reranking + scoring.
type Engine struct {
	DB       *db.DB
	BleveIdx *index.BleveIndex
	Voyage   *voyage.Client
	Cfg      *config.Config
}

// scoredCandidate holds a fused result with resolved metadata and scoring state.
type scoredCandidate struct {
	fused           FusedResult
	chunkRec        *db.ChunkRecord
	fileRec         *db.FileRecord
	rerankScore     float64
	rawRerankScore  float64 // score before path boost, for JSON output
	reranked        bool
	highlights      []string
}

// NewEngine creates a search engine.
func NewEngine(database *db.DB, bleveIdx *index.BleveIndex, voyageClient *voyage.Client, cfg *config.Config) *Engine {
	return &Engine{
		DB:       database,
		BleveIdx: bleveIdx,
		Voyage:   voyageClient,
		Cfg:      cfg,
	}
}

// Search runs the hybrid search pipeline:
// BM25 -> Vector -> RRF fusion -> Reranking -> Time-decay scoring.
func (e *Engine) Search(ctx context.Context, query string, opts SearchOptions) (*SearchResult, error) {
	if opts.TopK <= 0 {
		opts.TopK = e.Cfg.Search.DefaultTopK
	}

	// Determine collection filter.
	var collectionIDs map[int64]bool
	if opts.Collection != "" {
		col, err := e.DB.GetCollection(opts.Collection)
		if err != nil {
			return nil, err
		}
		if col == nil {
			return nil, fmt.Errorf("collection %q not found", opts.Collection)
		}
		collectionIDs = map[int64]bool{col.ID: true}
	}

	result := &SearchResult{}

	// Pre-compute valid chunk IDs when path glob is set.
	var pathChunkIDs map[int64]bool
	if opts.PathGlob != "" {
		var collectionID int64
		for id := range collectionIDs {
			collectionID = id
			break
		}
		ids, err := e.DB.GetChunkIDsByPathGlob(opts.PathGlob, collectionID, opts.SinceUnix)
		if err != nil {
			return nil, fmt.Errorf("path filter: %w", err)
		}
		pathChunkIDs = make(map[int64]bool, len(ids))
		for _, id := range ids {
			pathChunkIDs[id] = true
		}
	}

	// 0. Expand camelCase/snake_case query terms for better BM25 matching.
	expandedQuery := ExpandCodeIdentifiers(query)

	// 1. BM25 search via Bleve.
	bm25Start := time.Now()
	bleveResults, err := e.BleveIdx.Search(expandedQuery, opts.TopK*3)
	if err != nil {
		return nil, fmt.Errorf("bm25 search: %w", err)
	}
	result.BM25TimeMs = time.Since(bm25Start).Milliseconds()

	// Build BM25 ranked results, applying filters. Track highlights per chunk.
	var bm25Ranked []RankedResult
	highlightMap := make(map[int64][]string)
	rank := 0
	for _, br := range bleveResults {
		chunkID, err := strconv.ParseInt(br.ChunkID, 10, 64)
		if err != nil {
			continue
		}

		// Fast path filter when path glob pre-computed the valid set.
		if pathChunkIDs != nil && !pathChunkIDs[chunkID] {
			continue
		}

		// Filter by collection and time (skip if already handled by pathChunkIDs).
		if pathChunkIDs == nil && (collectionIDs != nil || opts.SinceUnix > 0) {
			_, fileRec, err := e.DB.GetChunkWithFile(chunkID)
			if err != nil || fileRec == nil {
				continue
			}
			if collectionIDs != nil && !collectionIDs[fileRec.CollectionID] {
				continue
			}
			if opts.SinceUnix > 0 && fileRec.Mtime < opts.SinceUnix {
				continue
			}
		}

		rank++
		bm25Ranked = append(bm25Ranked, RankedResult{
			ChunkID: chunkID,
			Rank:    rank,
			Score:   br.Score,
		})
		if len(br.Highlights) > 0 {
			highlightMap[chunkID] = br.Highlights
		}
	}
	result.TotalBM25 = len(bm25Ranked)

	// 2. Vector search (if Voyage client is available and embeddings exist).
	var vectorRanked []RankedResult
	if e.Voyage != nil {
		vecStart := time.Now()
		vectorRanked, err = e.vectorSearch(ctx, query, collectionIDs, opts)
		result.VecTimeMs = time.Since(vecStart).Milliseconds()
		if err != nil {
			// Vector search failure is non-fatal; fall back to BM25 only.
			vectorRanked = nil
		}
		result.TotalVec = len(vectorRanked)
	}

	// 3. Fuse results.
	var fusedResults []FusedResult
	if len(vectorRanked) > 0 {
		fusedResults = FuseRRF(
			bm25Ranked,
			vectorRanked,
			e.Cfg.Search.RRFK,
			e.Cfg.Search.RRFBoostTopN,
			e.Cfg.Search.RRFBoostFactor,
			e.Cfg.Search.BM25Weight,
			e.Cfg.Search.VectorWeight,
		)
	} else {
		// BM25-only mode: use BM25 scores directly.
		fusedResults = make([]FusedResult, len(bm25Ranked))
		for i, r := range bm25Ranked {
			fusedResults[i] = FusedResult{
				ChunkID:  r.ChunkID,
				RRFScore: r.Score,
				BM25Rank: r.Rank,
			}
		}
	}

	// 4. Rerank the top N candidates using Voyage reranker.

	rerankTopN := e.Cfg.Reranking.TopN
	if rerankTopN <= 0 {
		rerankTopN = 75
	}

	// Safety cap: don't resolve metadata for more candidates than we could ever need.
	if maxCandidates := rerankTopN * 2; len(fusedResults) > maxCandidates {
		fusedResults = fusedResults[:maxCandidates]
	}

	// Resolve chunk and file metadata for all candidates.
	candidates := make([]scoredCandidate, 0, len(fusedResults))
	for _, fr := range fusedResults {
		chunkRec, fileRec, err := e.DB.GetChunkWithFile(fr.ChunkID)
		if err != nil || fileRec == nil || chunkRec == nil {
			continue
		}
		candidates = append(candidates, scoredCandidate{
			fused:       fr,
			chunkRec:    chunkRec,
			fileRec:     fileRec,
			rerankScore: fr.RRFScore, // default to RRF score
			highlights:  highlightMap[fr.ChunkID],
		})
	}

	// File-aware candidate selection: ensure enough chunks per file reach the reranker.
	candidates = selectForReranking(candidates, rerankTopN, e.Cfg.Search.MaxChunksForReranking)

	// Attempt reranking if enabled and Voyage client is available.
	if e.Voyage != nil && e.Cfg.Reranking.Enabled && len(candidates) > 0 {
		rerankStart := time.Now()
		reranked, rerankErr := e.rerankCandidates(ctx, query, candidates, opts.TopK)
		result.RerankTimeMs = time.Since(rerankStart).Milliseconds()

		if rerankErr != nil {
			// Log to dead letters and fall back to RRF scores.
			dlErr := e.DB.InsertDeadLetter("rerank", "", query, rerankErr.Error(), "rerank_failure")
			if dlErr != nil {
				log.Printf("warning: failed to insert dead letter: %v", dlErr)
			}
		} else {
			candidates = reranked
			result.Reranked = true
		}
	}

	// 5. Apply time-decay scoring and feedback boost.
	now := time.Now()
	recencyWeight := e.Cfg.Scoring.RecencyWeight
	halfLifeDays := float64(e.Cfg.Scoring.RecencyHalfLifeDays)
	if halfLifeDays <= 0 {
		halfLifeDays = 30
	}

	feedbackEnabled := e.Cfg.Scoring.FeedbackEnabled
	for i := range candidates {
		c := &candidates[i]
		baseScore := c.rerankScore

		feedbackBoost := 1.0
		if feedbackEnabled {
			good, bad, fbErr := e.DB.GetFeedbackForChunk(c.fused.ChunkID)
			if fbErr != nil {
				log.Printf("warning: feedback lookup for chunk %d: %v", c.fused.ChunkID, fbErr)
			} else {
				feedbackBoost = FeedbackBoost(good, bad)
			}
		}

		mtime := time.Unix(c.fileRec.Mtime, 0)
		c.rerankScore = ComputeFinalScore(baseScore, mtime, now, recencyWeight, halfLifeDays, feedbackBoost)

		// Preserve the pre-path-boost score for JSON output.
		preBoostScore := c.rerankScore

		// Apply path-based score boost.
		pathBoost := PathBoost(c.fileRec.Path, e.Cfg.Scoring.PathBoosts)
		c.rerankScore *= pathBoost

		// Store the raw rerank score (before path boost) so JSON shows the components.
		c.rawRerankScore = preBoostScore
	}

	// Re-sort by final score descending.
	sort.Slice(candidates, func(i, j int) bool {
		return candidates[i].rerankScore > candidates[j].rerankScore
	})

	// 5a. Deduplicate identical content across files.
	if e.Cfg.Search.DedupIdenticalContent {
		candidates, result.ContentDedupMap = DedupIdenticalContent(candidates)
	}

	// 5b. Deduplicate by file path: keep at most MaxChunksPerFile per file.
	maxPerFile := e.Cfg.Search.MaxChunksPerFile
	if maxPerFile > 0 {
		fileCounts := make(map[string]int)
		filtered := candidates[:0]
		for _, c := range candidates {
			path := c.fileRec.Path
			if fileCounts[path] < maxPerFile {
				filtered = append(filtered, c)
				fileCounts[path]++
			}
		}
		candidates = filtered
	}

	// 5c. Threshold filtering: only when reranked and threshold > 0.
	result.TotalCandidates = len(candidates)
	result.Threshold = opts.Threshold
	if result.Reranked && opts.Threshold > 0 {
		filtered := candidates[:0]
		for _, c := range candidates {
			if c.rerankScore >= opts.Threshold {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered
	}
	result.FilteredCount = result.TotalCandidates - len(candidates)

	// 6. Truncate to topK (adaptive or explicit).
	topK := opts.TopK
	if opts.Adaptive && len(candidates) > 0 {
		scores := make([]float64, len(candidates))
		for i, c := range candidates {
			scores[i] = c.rerankScore
		}
		topK = AdaptiveTopK(scores, e.Cfg.Search.AdaptiveMinK, e.Cfg.Search.AdaptiveMaxK)
	}
	if len(candidates) > topK {
		candidates = candidates[:topK]
	}

	collectionNameCache := make(map[int64]string)
	for _, c := range candidates {
		// Resolve collection name.
		colName, ok := collectionNameCache[c.fileRec.CollectionID]
		if !ok {
			col, colErr := e.DB.GetCollectionByID(c.fileRec.CollectionID)
			if colErr == nil && col != nil {
				colName = col.Name
			}
			collectionNameCache[c.fileRec.CollectionID] = colName
		}

		rerankForOutput := c.rawRerankScore
		result.Results = append(result.Results, Result{
			ChunkID:      c.fused.ChunkID,
			FilePath:     c.fileRec.Path,
			Collection:   colName,
			StartLine:    c.chunkRec.StartLine,
			EndLine:      c.chunkRec.EndLine,
			BM25Rank:     c.fused.BM25Rank,
			VectorRank:   c.fused.VecRank,
			RRFScore:     c.fused.RRFScore,
			RerankScore:  rerankForOutput,
			FinalScore:   c.rerankScore,
			CollectionID: c.fileRec.CollectionID,
			Mtime:        c.fileRec.Mtime,
			Highlights:   c.highlights,
		})
	}

	return result, nil
}

// rerankCandidates calls Voyage Rerank on the candidate chunks and returns
// updated candidates with rerank scores. On error, returns nil so the caller
// can fall back to RRF scores.
func (e *Engine) rerankCandidates(ctx context.Context, query string, candidates []scoredCandidate, topK int) ([]scoredCandidate, error) {
	// Read chunk contents from disk and prepend provenance for reranker context.
	colNameCache := make(map[int64]string)
	colPathCache := make(map[int64]string)
	contents := make([]string, len(candidates))
	for i, c := range candidates {
		content := fileutil.ReadLines(c.fileRec.Path, c.chunkRec.StartLine, c.chunkRec.EndLine, 0)

		// Resolve collection name and path for provenance.
		colName, ok := colNameCache[c.fileRec.CollectionID]
		if !ok {
			col, colErr := e.DB.GetCollectionByID(c.fileRec.CollectionID)
			if colErr == nil && col != nil {
				colName = col.Name
				colPathCache[c.fileRec.CollectionID] = col.Path
			}
			colNameCache[c.fileRec.CollectionID] = colName
		}
		colPath := colPathCache[c.fileRec.CollectionID]
		relPath := strings.TrimPrefix(c.fileRec.Path, colPath)
		relPath = strings.TrimPrefix(relPath, "/")
		contents[i] = chunk.PrependProvenance(content, relPath, c.fileRec.Title, colName)
	}

	// Call Voyage rerank API.
	rerankResults, usage, err := e.Voyage.Rerank(ctx, query, contents, topK)
	if err != nil {
		return nil, fmt.Errorf("rerank api: %w", err)
	}

	// Record API usage.
	usageErr := e.DB.InsertAPIUsage("rerank", 1, usage.TotalTokens, 0)
	if usageErr != nil {
		log.Printf("warning: failed to record rerank api usage: %v", usageErr)
	}

	// Map rerank results back to candidates by index.
	reranked := make([]scoredCandidate, 0, len(rerankResults))
	for _, rr := range rerankResults {
		if rr.Index < 0 || rr.Index >= len(candidates) {
			continue
		}
		c := candidates[rr.Index]
		c.rerankScore = rr.RelevanceScore
		c.reranked = true
		reranked = append(reranked, c)
	}

	// Sort by rerank score descending.
	sort.Slice(reranked, func(i, j int) bool {
		return reranked[i].rerankScore > reranked[j].rerankScore
	})

	return reranked, nil
}

// selectForReranking picks candidates for reranking with file diversity.
// Takes up to maxTotal candidates, ensuring at least minPerFile chunks from
// each file that appears in the candidate set.
//
// Algorithm:
//  1. Group candidates by file path (using resolved metadata)
//  2. Take top minPerFile from each file (round 1)
//  3. Fill remaining slots with next-best candidates regardless of file (round 2)
func selectForReranking(candidates []scoredCandidate, maxTotal int, minPerFile int) []scoredCandidate {
	if len(candidates) <= maxTotal {
		return candidates
	}
	if minPerFile <= 0 {
		minPerFile = 3
	}

	// Group by file path.
	byFile := make(map[string][]int) // path -> indices in candidates slice
	for i, c := range candidates {
		byFile[c.fileRec.Path] = append(byFile[c.fileRec.Path], i)
	}

	selected := make(map[int]bool)

	// Round 1: take top minPerFile per file (candidates are already in RRF/score order).
	for _, indices := range byFile {
		count := minPerFile
		if count > len(indices) {
			count = len(indices)
		}
		for _, idx := range indices[:count] {
			selected[idx] = true
		}
	}

	// Round 2: fill remaining slots from overall ranking order.
	for i := range candidates {
		if len(selected) >= maxTotal {
			break
		}
		if !selected[i] {
			selected[i] = true
		}
	}

	// Build result preserving original order.
	result := make([]scoredCandidate, 0, len(selected))
	for i := range candidates {
		if selected[i] {
			result = append(result, candidates[i])
		}
	}
	return result
}

// vectorSearch embeds the query and runs a brute-force similarity search.
// Uses dot product for float32 embeddings and Hamming distance for binary.
func (e *Engine) vectorSearch(ctx context.Context, query string, collectionIDs map[int64]bool, opts SearchOptions) ([]RankedResult, error) {
	// Embed the query.
	vectors, _, err := e.Voyage.Embed(ctx, []string{query}, "query")
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}
	if len(vectors) == 0 || len(vectors[0]) == 0 {
		return nil, fmt.Errorf("empty query embedding")
	}
	queryVec := vectors[0]

	// Get embeddings, filtered by collection and time in SQL.
	var collectionID int64
	for id := range collectionIDs {
		collectionID = id
		break
	}
	embeddings, err := e.DB.GetFilteredEmbeddings(collectionID, opts.SinceUnix, opts.PathGlob)
	if err != nil {
		return nil, fmt.Errorf("get embeddings: %w", err)
	}
	if len(embeddings) == 0 {
		return nil, nil
	}

	// Dispatch based on embedding dtype.
	var vecResults []index.VectorResult
	if index.IsBinaryDtype(e.Voyage.EmbedDtype) {
		queryBytes := index.EncodeBinaryVector(queryVec)
		vecResults = index.BinaryVectorSearch(queryBytes, embeddings, opts.TopK*3)
	} else {
		vecResults = index.VectorSearch(queryVec, embeddings, opts.TopK*3)
	}

	ranked := make([]RankedResult, len(vecResults))
	for i, vr := range vecResults {
		ranked[i] = RankedResult{
			ChunkID: vr.ChunkID,
			Rank:    i + 1,
			Score:   float64(vr.Similarity),
		}
	}

	return ranked, nil
}
