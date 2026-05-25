package search

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"sift/internal/bm25"
	"sift/internal/chunk"
	"sift/internal/config"
	"sift/internal/db"
	"sift/internal/fileutil"
	"sift/internal/voyage"
)

// SearchOptions controls search behavior.
type SearchOptions struct {
	Collection       string
	SinceUnix        int64
	TopK             int
	Threshold        float64
	Adaptive         bool
	PathGlob         string // glob pattern to filter by file path (SQLite GLOB syntax)
	SectionAggregate bool   // aggregate results to section level
}

// Result represents a single search result with all metadata.
type Result struct {
	ChunkID          int64
	FilePath         string
	Collection       string
	StartLine        int
	EndLine          int
	BM25Rank         int
	VectorRank       int
	RRFScore         float64
	RerankScore      float64
	FinalScore       float64
	CollectionID     int64
	Mtime            int64
	Highlights       []string
	SectionID        string            // section anchor slug
	Heading          string            // section heading text
	HeadingLevel     int               // 1-6 or 0
	SectionCharCount int               // aggregated chars for the section
	SubsectionCount  int               // direct subsection count
	Siblings         []NeighborhoodRef // sibling sections in the same file
	Related          []NeighborhoodRef // same heading across files
	Links            []LinkRef         // outbound links from this section

	// IndexAnnotation is populated by Decorate (see decorate.go) when a
	// `sift.toml` is found at or above the file's folder. nil when no
	// folder index applies; rendered with `omitempty` in JSON.
	IndexAnnotation *IndexAnnotation `json:"folder_index,omitempty"`
}

// LinkRef is a simplified link reference for search output.
type LinkRef struct {
	TargetPath    string `json:"target_path"`
	TargetSection string `json:"target_section,omitempty"`
	LinkType      string `json:"link_type"`
}

// SearchResult holds the complete search output.
type SearchResult struct {
	Results         []Result
	TotalBM25       int
	TotalVec        int
	BM25TimeMs      int64
	VecTimeMs       int64
	WallParallelMs  int64 // max(bm25_ms, vec_ms): wall-clock time of the parallel BM25+vector phase
	RerankTimeMs    int64
	Reranked        bool
	TotalCandidates int
	FilteredCount   int
	DroppedStale    int
	Threshold       float64
	ContentDedupMap map[int64][]DuplicateRef // chunk_id -> duplicates (nil if dedup disabled)
}

// Engine orchestrates BM25 + vector search + RRF fusion + reranking + scoring.
type Engine struct {
	DB       *db.DB
	BleveIdx *bm25.BleveIndex
	Voyage   *voyage.Client
	Cfg      *config.Config

	// testHooks is an optional set of synchronisation/error hooks used by
	// tests to inject deterministic latency or failures into the BM25 and
	// vector search branches. Production code leaves this nil.
	testHooks *searchTestHooks
}

// searchTestHooks lets tests inject behaviour at the boundary of the BM25
// and vector goroutines. All fields are optional.
//
// The done WaitGroup, when non-nil, is incremented by the dispatcher in
// Search before each parallel goroutine starts and decremented from the
// goroutine's deferred wrapper after it has finished sending its result.
// Tests can wait on it (via WaitWithTimeout) to deterministically replace
// time.Sleep before goleak.VerifyNone, eliminating CI flake.
type searchTestHooks struct {
	bm25Delay time.Duration
	vecDelay  time.Duration
	bm25Err   error
	vecErr    error
	done      *sync.WaitGroup
}

// scoredCandidate holds a fused result with resolved metadata and scoring state.
type scoredCandidate struct {
	fused          FusedResult
	chunkRec       *db.ChunkRecord
	fileRec        *db.FileRecord
	rerankScore    float64
	rawRerankScore float64 // score before path boost, for JSON output
	reranked       bool
	highlights     []string
	sectionID      string
	heading        string
	headingLevel   int
}

// NewEngine creates a search engine.
func NewEngine(database *db.DB, bleveIdx *bm25.BleveIndex, voyageClient *voyage.Client, cfg *config.Config) *Engine {
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
	var collectionID int64
	var collectionName string
	if opts.Collection != "" {
		col, err := e.DB.GetCollection(opts.Collection)
		if err != nil {
			return nil, err
		}
		if col == nil {
			return nil, fmt.Errorf("collection %q not found", opts.Collection)
		}
		collectionID = col.ID
		collectionName = col.Name
	}

	result := &SearchResult{}

	// Pre-compute valid chunk IDs when path glob is set.
	var pathChunkIDs map[int64]bool
	if opts.PathGlob != "" {
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

	runBM25 := func() ([]RankedResult, map[int64][]string, int64, error) {
		start := time.Now()
		bleveResults, err := e.BleveIdx.Search(expandedQuery, opts.TopK*3)
		if err != nil {
			return nil, nil, 0, fmt.Errorf("bm25 search: %w", err)
		}

		bm25Ranked := make([]RankedResult, 0, len(bleveResults))
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
			if pathChunkIDs == nil && (collectionID != 0 || opts.SinceUnix > 0) {
				_, fileRec, err := e.DB.GetChunkWithFile(chunkID)
				if err != nil || fileRec == nil {
					continue
				}
				if collectionID != 0 {
					inCollection, membershipErr := e.DB.FileBelongsToCollection(fileRec.ID, collectionID)
					if membershipErr != nil || !inCollection {
						continue
					}
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

		return bm25Ranked, highlightMap, time.Since(start).Milliseconds(), nil
	}

	fuseResults := func(bm25Ranked, vectorRanked []RankedResult) []FusedResult {
		if len(vectorRanked) > 0 {
			return FuseRRF(
				bm25Ranked,
				vectorRanked,
				e.Cfg.Search.RRFK,
				e.Cfg.Search.RRFBoostTopN,
				e.Cfg.Search.RRFBoostFactor,
				e.Cfg.Search.BM25Weight,
				e.Cfg.Search.VectorWeight,
			)
		}

		fusedResults := make([]FusedResult, len(bm25Ranked))
		for i, r := range bm25Ranked {
			fusedResults[i] = FusedResult{
				ChunkID:  r.ChunkID,
				RRFScore: r.Score,
				BM25Rank: r.Rank,
			}
		}
		return fusedResults
	}

	resolveCandidates := func(fusedResults []FusedResult, highlightMap map[int64][]string) ([]scoredCandidate, int) {
		droppedStale := 0
		candidates := make([]scoredCandidate, 0, len(fusedResults))
		for _, fr := range fusedResults {
			chunkRec, fileRec, err := e.DB.GetChunkWithFile(fr.ChunkID)
			if err != nil || fileRec == nil || chunkRec == nil {
				droppedStale++
				slog.Debug("dropped stale chunk (not in DB)",
					"component", "search",
					"op", "resolve_candidates",
					"chunk_id", fr.ChunkID,
					"collection", opts.Collection,
				)
				continue
			}
			candidates = append(candidates, scoredCandidate{
				fused:        fr,
				chunkRec:     chunkRec,
				fileRec:      fileRec,
				rerankScore:  fr.RRFScore, // default to RRF score
				highlights:   highlightMap[fr.ChunkID],
				sectionID:    canonicalSectionID(chunkRec.SectionID),
				heading:      chunkRec.Heading,
				headingLevel: chunkRec.HeadingLevel,
			})
		}
		return candidates, droppedStale
	}

	// 1+2. BM25 + Vector search in parallel.
	// Wall time becomes max(bm25, vec) instead of sum.
	type bm25Outcome struct {
		ranked     []RankedResult
		highlights map[int64][]string
		elapsedMs  int64
		err        error
	}
	type vecOutcome struct {
		ranked    []RankedResult
		elapsedMs int64
		err       error
	}

	bm25Ch := make(chan bm25Outcome, 1)
	vecCh := make(chan vecOutcome, 1)

	// Test-only WaitGroup so tests can deterministically wait for both
	// dispatch goroutines to finish their final send and return — without
	// resorting to a time.Sleep before goleak.VerifyNone.
	var doneWG *sync.WaitGroup
	if e.testHooks != nil && e.testHooks.done != nil {
		doneWG = e.testHooks.done
	}

	// Spawn BM25 goroutine.
	if doneWG != nil {
		doneWG.Add(1)
	}
	go func() {
		if doneWG != nil {
			defer doneWG.Done()
		}
		bmStart := time.Now()
		if e.testHooks != nil && e.testHooks.bm25Delay > 0 {
			time.Sleep(e.testHooks.bm25Delay)
		}
		if e.testHooks != nil && e.testHooks.bm25Err != nil {
			bm25Ch <- bm25Outcome{
				err:       e.testHooks.bm25Err,
				elapsedMs: time.Since(bmStart).Milliseconds(),
			}
			return
		}
		ranked, highlights, _, bmErr := runBM25()
		bm25Ch <- bm25Outcome{
			ranked:     ranked,
			highlights: highlights,
			elapsedMs:  time.Since(bmStart).Milliseconds(),
			err:        bmErr,
		}
	}()

	// Spawn vector goroutine only if Voyage client is configured.
	vecEnabled := e.Voyage != nil
	if vecEnabled {
		if doneWG != nil {
			doneWG.Add(1)
		}
		go func() {
			if doneWG != nil {
				defer doneWG.Done()
			}
			start := time.Now()
			if e.testHooks != nil && e.testHooks.vecDelay > 0 {
				time.Sleep(e.testHooks.vecDelay)
			}
			if e.testHooks != nil && e.testHooks.vecErr != nil {
				vecCh <- vecOutcome{
					err:       e.testHooks.vecErr,
					elapsedMs: time.Since(start).Milliseconds(),
				}
				return
			}
			ranked, vecErr := e.vectorSearch(ctx, query, collectionID, opts)
			vecCh <- vecOutcome{ranked: ranked, elapsedMs: time.Since(start).Milliseconds(), err: vecErr}
		}()
	} else {
		// Populate a zero-valued result so the receive below is symmetric and
		// no goroutine leaks (the buffered channel absorbs the send).
		vecCh <- vecOutcome{}
	}

	// Always receive from BOTH channels even on early error to prevent leaks.
	bm25Res := <-bm25Ch
	vecRes := <-vecCh

	// Symmetric degradation: if BM25 errors but vec succeeded, fall back to
	// vec-only ranking. If vec errors but BM25 succeeded, fall back to BM25-
	// only. Only return an error if both branches fail (or vec is disabled
	// and BM25 errors).
	var bm25Ranked []RankedResult
	var highlightMap map[int64][]string
	result.BM25TimeMs = bm25Res.elapsedMs
	if bm25Res.err != nil {
		if !vecEnabled || vecRes.err != nil {
			slog.Error("bm25 search failed and no vector fallback available",
				"component", "search",
				"op", "bm25",
				"query_len", len(query),
				"collection", opts.Collection,
				"bm25_ms", bm25Res.elapsedMs,
				"vec_ms", vecRes.elapsedMs,
				"err", bm25Res.err,
			)
			return nil, bm25Res.err
		}
		// BM25 failure is non-fatal when vec succeeded; fall back to vec-only.
		slog.Warn("bm25 search failed; falling back to vector only",
			"component", "search",
			"op", "bm25",
			"query_len", len(query),
			"collection", opts.Collection,
			"bm25_ms", bm25Res.elapsedMs,
			"vec_ms", vecRes.elapsedMs,
			"err", bm25Res.err,
		)
		bm25Ranked = nil
		highlightMap = nil
		result.TotalBM25 = 0
	} else {
		bm25Ranked = bm25Res.ranked
		highlightMap = bm25Res.highlights
		result.TotalBM25 = len(bm25Ranked)
	}

	var vectorRanked []RankedResult
	if vecEnabled {
		result.VecTimeMs = vecRes.elapsedMs
		if vecRes.err != nil {
			// Vector search failure is non-fatal; fall back to BM25 only.
			slog.Warn("vector search failed; falling back to BM25 only",
				"component", "search",
				"op", "vector",
				"query_len", len(query),
				"collection", opts.Collection,
				"bm25_ms", bm25Res.elapsedMs,
				"vec_ms", vecRes.elapsedMs,
				"err", vecRes.err,
			)
			vectorRanked = nil
		} else {
			vectorRanked = vecRes.ranked
		}
		result.TotalVec = len(vectorRanked)
	}

	// Wall-clock time of the parallel phase. Always set even when one
	// branch errored so callers can see the parallel-phase duration.
	if result.BM25TimeMs > result.VecTimeMs {
		result.WallParallelMs = result.BM25TimeMs
	} else {
		result.WallParallelMs = result.VecTimeMs
	}

	// 3. Fuse results.
	fusedResults := fuseResults(bm25Ranked, vectorRanked)

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
	candidates, droppedStale := resolveCandidates(fusedResults, highlightMap)
	if droppedStale > 0 && len(candidates) == 0 && len(bm25Ranked) > 0 {
		for attempt := 1; attempt <= 3 && len(candidates) == 0; attempt++ {
			slog.Warn("all candidates were stale index entries; retrying BM25",
				"component", "search",
				"op", "bm25_retry",
				"dropped_stale", droppedStale,
				"attempt", attempt,
				"max_attempts", 3,
				"collection", opts.Collection,
			)
			time.Sleep(time.Duration(attempt*25) * time.Millisecond)

			retryRanked, retryHighlights, retryTimeMs, retryErr := runBM25()
			result.BM25TimeMs += retryTimeMs
			if retryErr != nil {
				slog.Error("bm25 retry after stale candidates failed",
					"component", "search",
					"op", "bm25_retry",
					"attempt", attempt,
					"collection", opts.Collection,
					"err", retryErr,
				)
				break
			}

			bm25Ranked = retryRanked
			highlightMap = retryHighlights
			result.TotalBM25 = len(bm25Ranked)
			fusedResults = fuseResults(bm25Ranked, vectorRanked)
			if maxCandidates := rerankTopN * 2; len(fusedResults) > maxCandidates {
				fusedResults = fusedResults[:maxCandidates]
			}
			retryCandidates, retryDropped := resolveCandidates(fusedResults, highlightMap)
			droppedStale += retryDropped
			candidates = retryCandidates
		}
	}
	if droppedStale > 0 && len(candidates) == 0 {
		slog.Error("all candidates were stale index entries — index may need rebuild",
			"component", "search",
			"op", "resolve_candidates",
			"dropped_stale", droppedStale,
			"collection", opts.Collection,
		)
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
				slog.Warn("failed to insert dead letter",
					"component", "search",
					"op", "rerank_dead_letter",
					"collection", opts.Collection,
					"err", dlErr,
				)
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
	backlinkWeight := e.Cfg.Scoring.BacklinkWeight
	backlinkCounts := map[string]int{}
	if backlinkWeight > 0 {
		if counts, countErr := e.DB.GetBacklinkCounts(); countErr != nil {
			slog.Warn("backlink count lookup failed",
				"component", "search",
				"op", "backlink_counts",
				"collection", opts.Collection,
				"err", countErr,
			)
		} else {
			backlinkCounts = counts
		}
	}

	readSignalWeight := e.Cfg.Scoring.ReadSignalWeight
	readCounts := map[string]db.ReadCountRecord{}
	if e.Cfg.Scoring.ReadSignalEnabled && readSignalWeight > 0 {
		if counts, countErr := e.DB.GetReadCounts(); countErr != nil {
			slog.Warn("read count lookup failed",
				"component", "search",
				"op", "read_counts",
				"collection", opts.Collection,
				"err", countErr,
			)
		} else {
			readCounts = counts
		}
	}

	for i := range candidates {
		c := &candidates[i]
		baseScore := c.rerankScore

		feedbackBoost := 1.0
		if feedbackEnabled {
			good, bad, fbErr := e.DB.GetFeedbackForChunk(c.fused.ChunkID)
			if fbErr != nil {
				slog.Warn("feedback lookup failed",
					"component", "search",
					"op", "feedback_lookup",
					"chunk_id", c.fused.ChunkID,
					"collection", opts.Collection,
					"err", fbErr,
				)
			} else {
				feedbackBoost = FeedbackBoost(good, bad)
			}
		}

		mtime := time.Unix(c.fileRec.Mtime, 0)
		c.rerankScore = ComputeFinalScore(baseScore, mtime, now, recencyWeight, halfLifeDays, feedbackBoost)
		c.rerankScore *= BacklinkBoost(backlinkCounts[c.fileRec.Path], backlinkWeight)
		if readCount, ok := readCounts[c.fileRec.Path]; ok {
			c.rerankScore *= ReadBoost(readCount.TotalReads, readSignalWeight)
		}

		// Preserve the pre-path-boost score for JSON output.
		preBoostScore := c.rerankScore

		// Apply path-based score boost.
		pathBoost := PathBoost(c.fileRec.Path, e.Cfg.Scoring.PathBoosts)
		c.rerankScore *= pathBoost

		// Store the raw rerank score (before path boost) so JSON shows the components.
		c.rawRerankScore = preBoostScore
	}

	// Re-sort by final score descending with deterministic tie-breaking by chunk ID.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].rerankScore != candidates[j].rerankScore {
			return candidates[i].rerankScore > candidates[j].rerankScore
		}
		return candidates[i].fused.ChunkID < candidates[j].fused.ChunkID
	})

	// 5a. Optional section-level aggregation.
	if opts.SectionAggregate {
		candidates = aggregateToSections(candidates)
	}

	// 5b. Deduplicate identical content across files.
	if e.Cfg.Search.DedupIdenticalContent {
		candidates, result.ContentDedupMap = DedupIdenticalContent(candidates)
	}

	// 5c. Deduplicate by file path: keep at most MaxChunksPerFile per file.
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

	// 5d. Threshold filtering: only when reranked and threshold > 0.
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

	metadataCache := newSectionMetadataCache(e)
	existingResults := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		existingResults[resultKey(c.fileRec.Path, c.sectionID)] = struct{}{}
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
		sectionCharCount := c.chunkRec.CharCount
		subsectionCount := 0
		var siblings []NeighborhoodRef
		var related []NeighborhoodRef

		if c.sectionID != "" {
			summaries, summaryIndex, summaryErr := metadataCache.summariesForFile(c.fileRec.ID)
			if summaryErr != nil {
				slog.Warn("section metadata lookup failed",
					"component", "search",
					"op", "section_metadata",
					"file_path", c.fileRec.Path,
					"collection", opts.Collection,
					"err", summaryErr,
				)
			} else if summary, ok := summaryIndex[c.sectionID]; ok {
				sectionCharCount = summary.CharCount
				subsectionCount = summary.SubsectionCount
				siblings = buildSiblingRefs(summary, summaries, existingResults)
				if c.heading != "" {
					lookupCollectionID := c.fileRec.CollectionID
					if collectionID != 0 {
						lookupCollectionID = collectionID
					}
					relatedSummaries, relatedErr := metadataCache.summariesForHeading(lookupCollectionID, c.heading)
					if relatedErr != nil {
						slog.Warn("related section lookup failed",
							"component", "search",
							"op", "related_section",
							"file_path", c.fileRec.Path,
							"section_id", c.sectionID,
							"collection", opts.Collection,
							"err", relatedErr,
						)
					} else {
						related = buildRelatedRefs(summary, relatedSummaries, existingResults)
					}
				}
			}
		}

		displayCollectionID := c.fileRec.CollectionID
		displayCollectionName := colName
		if collectionID != 0 {
			displayCollectionID = collectionID
			displayCollectionName = collectionName
		}

		r := Result{
			ChunkID:          c.fused.ChunkID,
			FilePath:         c.fileRec.Path,
			Collection:       displayCollectionName,
			StartLine:        c.chunkRec.StartLine,
			EndLine:          c.chunkRec.EndLine,
			BM25Rank:         c.fused.BM25Rank,
			VectorRank:       c.fused.VecRank,
			RRFScore:         c.fused.RRFScore,
			RerankScore:      rerankForOutput,
			FinalScore:       c.rerankScore,
			CollectionID:     displayCollectionID,
			Mtime:            c.fileRec.Mtime,
			Highlights:       c.highlights,
			SectionID:        c.sectionID,
			Heading:          c.heading,
			HeadingLevel:     c.headingLevel,
			SectionCharCount: sectionCharCount,
			SubsectionCount:  subsectionCount,
			Siblings:         siblings,
			Related:          related,
		}

		// Load links if enabled.
		if e.Cfg.Search.IncludeLinks && c.sectionID != "" {
			links, linkErr := e.DB.GetLinksByFileAndSection(c.fileRec.ID, c.sectionID)
			if linkErr == nil {
				for _, l := range links {
					r.Links = append(r.Links, LinkRef{
						TargetPath:    l.TargetPath,
						TargetSection: l.TargetSection,
						LinkType:      l.LinkType,
					})
				}
			}
		}

		result.Results = append(result.Results, r)
	}

	result.DroppedStale = droppedStale

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
		slog.Warn("failed to record rerank api usage",
			"component", "search",
			"op", "rerank_api_usage",
			"err", usageErr,
		)
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
func (e *Engine) vectorSearch(ctx context.Context, query string, collectionID int64, opts SearchOptions) ([]RankedResult, error) {
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
	embeddings, err := e.DB.GetFilteredEmbeddings(collectionID, opts.SinceUnix, opts.PathGlob)
	if err != nil {
		return nil, fmt.Errorf("get embeddings: %w", err)
	}
	if len(embeddings) == 0 {
		return nil, nil
	}

	// Dispatch based on embedding dtype.
	var vecResults []bm25.VectorResult
	if bm25.IsBinaryDtype(e.Voyage.EmbedDtype) {
		queryBytes := bm25.EncodeBinaryVector(queryVec)
		vecResults = bm25.BinaryVectorSearch(queryBytes, embeddings, opts.TopK*3)
	} else {
		vecResults = bm25.VectorSearch(queryVec, embeddings, opts.TopK*3)
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

// aggregateToSections groups candidates by (filePath, sectionID) and keeps the
// highest-scored candidate per group. This collapses multiple chunks from the
// same section into a single representative result.
func aggregateToSections(candidates []scoredCandidate) []scoredCandidate {
	type key struct {
		path      string
		sectionID string
	}
	best := make(map[key]scoredCandidate)
	for _, c := range candidates {
		k := key{c.fileRec.Path, c.sectionID}
		if existing, ok := best[k]; !ok || c.rerankScore > existing.rerankScore {
			best[k] = c
		}
	}
	result := make([]scoredCandidate, 0, len(best))
	for _, c := range best {
		result = append(result, c)
	}
	sort.SliceStable(result, func(i, j int) bool {
		if result[i].rerankScore != result[j].rerankScore {
			return result[i].rerankScore > result[j].rerankScore
		}
		return result[i].fused.ChunkID < result[j].fused.ChunkID
	})
	return result
}
