package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"sift/internal/cache"
	"sift/internal/config"
	"sift/internal/fileutil"
	"sift/internal/index"
	siftlog "sift/internal/log"
	"sift/internal/search"
	"sift/internal/voyage"
)

func newSearchCmd() *cobra.Command {
	var (
		collection  string
		since       string
		pathGlob    string
		topK        int
		jsonOutput  bool
		filesOutput bool
		pretty      bool
		reverse     bool
		threshold   float64
		noTips      bool
	)

	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search across collections",
		Long: `Hybrid search combining BM25, vector embeddings, and reranking.

Results are labeled a-z. Use these labels with "sift feedback" to improve
future rankings. The search_id in the output header identifies the session.

Output modes:
  (default)   AI-optimized: header line + [label] file:lines (score) + indented content
  --json      Machine-readable: full JSON with scores, components, metadata, feedback_cmd
  --files     Just unique file paths, one per line (good for piping)
  --pretty    Human-readable: editor commands, half the default results, --reverse on

--reverse shows best results last (bottom of terminal). Enabled by default with --pretty.
Use --no-tips to suppress the trailing tip line in default/pretty output.

Examples:
  sift search "Firestore storage limitation"
  sift search "How does the agent handle rate limiting?" --collection vault
  sift search "authentication flow" --since 2w --top-k 5
  sift search "API design" --path "*/topics/work/*"
  sift search "Returns API proof of concept" --json
  sift search "rate limiting" --files | xargs cat`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 {
				return cmd.Help()
			}
			query := args[0]
			start := time.Now()

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if topK == 0 {
				topK = cfg.Search.DefaultTopK
			}

			// Pretty mode: halve results when --top-k not explicitly set.
			if pretty && !cmd.Flags().Changed("top-k") {
				topK = max(1, topK/2)
			}

			// Pretty mode implies --reverse unless explicitly disabled.
			if pretty && !cmd.Flags().Changed("reverse") {
				reverse = true
			}

			// Use config threshold as default unless overridden.
			if !cmd.Flags().Changed("threshold") {
				threshold = cfg.Search.Threshold
			}

			// Adaptive mode: when --top-k not explicitly passed.
			adaptive := !cmd.Flags().Changed("top-k")

			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			blevePath, err := config.BlevePath()
			if err != nil {
				return err
			}
			bleveIdx, err := index.OpenBleve(blevePath, cfg.BM25.Analyzer)
			if err != nil {
				return err
			}
			defer bleveIdx.Close()

			// Create Voyage client if API key is set.
			var voyageClient *voyage.Client
			if cfg.API.VoyageAPIKey != "" {
				voyageClient = voyage.NewClient(cfg.API.VoyageAPIKey)
				voyageClient.EmbedModel = cfg.Embedding.Model
				voyageClient.EmbedDimensions = cfg.Embedding.Dimensions
				voyageClient.EmbedDtype = cfg.Embedding.OutputDtype
				voyageClient.RerankModel = cfg.Reranking.Model
				if cfg.API.RequestTimeoutSecs > 0 {
					voyageClient.SetTimeout(time.Duration(cfg.API.RequestTimeoutSecs) * time.Second)
				}
			}

			// Parse time filter.
			var sinceUnix int64
			if since != "" {
				dur, err := parseDuration(since)
				if err != nil {
					return fmt.Errorf("invalid --since: %w", err)
				}
				sinceUnix = time.Now().Add(-dur).Unix()
			}

			// Cache setup.
			var searchCache *cache.Cache
			cached := false
			if cfg.Cache.Enabled {
				searchCache = cache.New(database, cfg.Cache.TTLSeconds, cfg.Cache.MaxEntries)
			}

			var result *search.SearchResult

			// Check cache first.
			cacheKey := ""
			if searchCache != nil {
				cacheKey = cache.Key(query, collection, sinceUnix, pathGlob)
				if cr, ok := searchCache.Get(cacheKey); ok {
					// Cache hit: reconstruct SearchResult from cached data.
					result = &search.SearchResult{
						Results:      cr.Results,
						TotalBM25:    cr.TotalBM25,
						TotalVec:     cr.TotalVec,
						BM25TimeMs:   cr.BM25TimeMs,
						VecTimeMs:    cr.VecTimeMs,
						RerankTimeMs: cr.RerankTimeMs,
						Reranked:     cr.Reranked,
					}

					// Apply threshold filter post-hoc.
					result.TotalCandidates = len(result.Results)
					result.Threshold = threshold
					if result.Reranked && threshold > 0 {
						filtered := result.Results[:0]
						for _, r := range result.Results {
							if r.FinalScore >= threshold {
								filtered = append(filtered, r)
							}
						}
						result.Results = filtered
					}
					result.FilteredCount = result.TotalCandidates - len(result.Results)

					// Apply adaptive top-k or explicit.
					effectiveTopK := topK
					if adaptive && len(result.Results) > 0 {
						scores := make([]float64, len(result.Results))
						for i, r := range result.Results {
							scores[i] = r.FinalScore
						}
						effectiveTopK = search.AdaptiveTopK(scores, cfg.Search.AdaptiveMinK, cfg.Search.AdaptiveMaxK)
					}
					if len(result.Results) > effectiveTopK {
						result.Results = result.Results[:effectiveTopK]
					}

					cached = true
				}
			}

			// Cache miss: run full search.
			if result == nil {
				engine := search.NewEngine(database, bleveIdx, voyageClient, cfg)
				result, err = engine.Search(context.Background(), query, search.SearchOptions{
					Collection: collection,
					SinceUnix:  sinceUnix,
					TopK:       topK,
					Threshold:  threshold,
					Adaptive:   adaptive,
					PathGlob:   pathGlob,
				})
				if err != nil {
					return fmt.Errorf("search: %w", err)
				}

				// Store in cache (full candidate list before threshold/topk truncation).
				if searchCache != nil && cacheKey != "" {
					cr := &cache.CachedResult{
						Results:      result.Results,
						TotalBM25:    result.TotalBM25,
						TotalVec:     result.TotalVec,
						BM25TimeMs:   result.BM25TimeMs,
						VecTimeMs:    result.VecTimeMs,
						RerankTimeMs: result.RerankTimeMs,
						Reranked:     result.Reranked,
					}
					searchCache.Put(cacheKey, cr, query, collection)
				}
			}

			elapsed := time.Since(start)

			// Build output results.
			type scoreComponents struct {
				BM25Rank   int     `json:"bm25_rank"`
				VectorRank int     `json:"vector_rank"`
				RRF        float64 `json:"rrf"`
				Rerank     float64 `json:"rerank"`
				Final      float64 `json:"final"`
			}

			type searchResult struct {
				Index      string                `json:"index"`
				ChunkID    int64                 `json:"chunk_id"`
				File       string                `json:"file"`
				Collection string                `json:"collection"`
				StartLine  int                   `json:"start_line"`
				EndLine    int                   `json:"end_line"`
				Content    string                `json:"content"`
				Stale      bool                  `json:"stale,omitempty"`
				Score      float64               `json:"score"`
				OpenCmd    string                `json:"open_cmd"`
				Highlights []string              `json:"highlights,omitempty"`
				Components *scoreComponents      `json:"components,omitempty"`
				Duplicates []search.DuplicateRef `json:"duplicates,omitempty"`
			}

			previewChars := cfg.Search.PreviewChars
			previewLines := cfg.Search.PreviewLines

			var results []searchResult
			for i, r := range result.Results {
				idx := indexLabel(i)
				openCmd := strings.ReplaceAll(cfg.Output.EditorCommand, "{file}", r.FilePath)
				openCmd = strings.ReplaceAll(openCmd, "{line}", strconv.Itoa(r.StartLine))

				var contentPreview string
				if jsonOutput {
					contentPreview = readChunkPreview(r.FilePath, r.StartLine, r.EndLine, 0)
				} else if pretty && previewLines > 0 {
					contentPreview = readLineSnippet(r.FilePath, r.StartLine, r.EndLine, previewLines, r.Highlights)
				} else {
					// Both default and pretty: center on BM25 highlight when available.
					contentPreview = readHighlightPreview(r.FilePath, r.StartLine, r.EndLine, previewChars, r.Highlights)
				}
				stale := isStaleFile(r.FilePath, r.Mtime)

				sr := searchResult{
					Index:      idx,
					ChunkID:    r.ChunkID,
					File:       shortestPath(r.FilePath),
					Collection: r.Collection,
					StartLine:  r.StartLine,
					EndLine:    r.EndLine,
					Content:    contentPreview,
					Stale:      stale,
					Score:      r.FinalScore,
					OpenCmd:    openCmd,
					Highlights: r.Highlights,
				}

				if result.ContentDedupMap != nil {
					sr.Duplicates = result.ContentDedupMap[r.ChunkID]
				}

				if jsonOutput {
					sr.File = r.FilePath // JSON always uses absolute paths
					sr.Components = &scoreComponents{
						BM25Rank:   r.BM25Rank,
						VectorRank: r.VectorRank,
						RRF:        r.RRFScore,
						Rerank:     r.RerankScore,
						Final:      r.FinalScore,
					}
				}

				results = append(results, sr)
			}

			// Generate search ID.
			searchID := generateSearchID()

			// Store search session.
			resultsJSON, err := json.Marshal(results)
			if err != nil {
				return fmt.Errorf("marshal results: %w", err)
			}
			if err := database.InsertSearchSession(searchID, query, collection, string(resultsJSON)); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: failed to store search session: %v\n", err)
			}

			// Log search to JSONL (best-effort).
			logDir, logErr := config.LogDir()
			if logErr == nil {
				logger := siftlog.NewLogger(logDir, cfg.Logs.MaxWeeks)
				logEntry := map[string]any{
					"timestamp":  time.Now().UTC().Format(time.RFC3339),
					"search_id":  searchID,
					"query":      query,
					"collection": collection,
					"top_k":      topK,
					"results":    len(results),
					"elapsed_ms": elapsed.Milliseconds(),
					"cached":     cached,
				}
				_ = logger.Log("searches", logEntry)
			}

			w := cmd.OutOrStdout()

			// --files mode: file paths only.
			if filesOutput {
				printed := make(map[string]bool)
				for _, r := range results {
					if !printed[r.File] {
						fmt.Fprintln(w, r.File)
						printed[r.File] = true
					}
				}
				return nil
			}

			// --json mode: verbose JSON (unchanged).
			if jsonOutput {
				output := map[string]any{
					"search_id": searchID,
					"query":     query,
					"results":   results,
					"meta": map[string]any{
						"total_time_ms":    elapsed.Milliseconds(),
						"bm25_time_ms":     result.BM25TimeMs,
						"vector_time_ms":   result.VecTimeMs,
						"rerank_time_ms":   result.RerankTimeMs,
						"reranked":         result.Reranked,
						"result_count":     len(results),
						"bm25_results":     result.TotalBM25,
						"vector_results":   result.TotalVec,
						"total_candidates": result.TotalCandidates,
						"filtered_count":   result.FilteredCount,
						"cached":           cached,
					},
					"feedback_cmd": fmt.Sprintf("sift feedback %s --positive <indices> --negative <indices>", searchID),
				}
				enc := json.NewEncoder(w)
				enc.SetIndent("", "  ")
				return enc.Encode(output)
			}

			// Build score context header.
			if pretty {
				fmt.Fprintln(w, prettyHeader(query, searchID, result, len(results), elapsed, cached))
			} else {
				fmt.Fprintln(w, buildHeader(query, searchID, result, len(results), elapsed, cached))
			}
			fmt.Fprintln(w)

			if len(results) == 0 {
				tip := buildNoResultsTip(result, threshold)
				if !noTips && tip != "" {
					if pretty {
						tip = style(tip, ansiDim)
					}
					fmt.Fprintln(w, tip)
				}
				return nil
			}

			// Build display order: --reverse puts best results at the bottom
			// of the terminal (where the user's eye lands after scroll).
			displayOrder := results
			if reverse {
				displayOrder = make([]searchResult, len(results))
				for i, r := range results {
					displayOrder[len(results)-1-i] = r
				}
			}

			// --pretty mode: human-oriented with colors and previews.
			if pretty {
				sep := style("──────────────────────────────────────────────────", ansiDim)
				for i, r := range displayOrder {
					staleTag := ""
					if r.Stale {
						staleTag = " " + style("[stale]", ansiBold, ansiRed)
					}
					fmt.Fprintf(w, "%s %s%s %s%s\n",
						style("["+r.Index+"]", ansiBold, ansiCyan),
						style(r.File, ansiBold),
						style(fmt.Sprintf(":%d-%d", r.StartLine, r.EndLine), ansiDim),
						style(fmt.Sprintf("(%.2f)", r.Score), ansiBold, ansiYellow),
						staleTag)
					for line := range strings.SplitSeq(r.Content, "\n") {
						fmt.Fprintf(w, "    %s\n", line)
					}
					if len(r.Duplicates) > 0 {
						var dups []string
						for _, dup := range r.Duplicates {
							dups = append(dups, fmt.Sprintf("%s:%d-%d", shortestPath(dup.File), dup.StartLine, dup.EndLine))
						}
						fmt.Fprintf(w, "    %s\n", style("Also in: "+strings.Join(dups, ", "), ansiDim))
					}
					fmt.Fprintf(w, "    %s\n", style("> "+r.OpenCmd, ansiDim))
					if i < len(displayOrder)-1 {
						fmt.Fprintln(w, sep)
					}
				}
				fmt.Fprintln(w)
			} else {
				// Default format: AI-optimized with full content.
				for _, r := range displayOrder {
					fmt.Fprintf(w, "[%s] %s:%d-%d (%.2f)\n", r.Index, r.File, r.StartLine, r.EndLine, r.Score)
					// Indent full content.
					for line := range strings.SplitSeq(r.Content, "\n") {
						fmt.Fprintf(w, "    %s\n", line)
					}
					if len(r.Duplicates) > 0 {
						var dups []string
						for _, dup := range r.Duplicates {
							dups = append(dups, fmt.Sprintf("%s:%d-%d", shortestPath(dup.File), dup.StartLine, dup.EndLine))
						}
						fmt.Fprintf(w, "    Also in: %s\n", strings.Join(dups, ", "))
					}
					fmt.Fprintln(w)
				}
			}

			if !noTips {
				tip := buildTip(result, searchID, threshold)
				if tip != "" {
					if pretty {
						tip = style(tip, ansiDim)
					}
					fmt.Fprintln(w, tip)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&collection, "collection", "c", "", "Filter by collection")
	cmd.Flags().StringVarP(&pathGlob, "path", "p", "", "Filter by file path glob (e.g. \"*/topics/work/*\")")
	cmd.Flags().StringVar(&since, "since", "", "Time filter (e.g. 2d, 1w)")
	cmd.Flags().IntVarP(&topK, "top-k", "k", 0, "Number of results")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "JSON output")
	cmd.Flags().BoolVar(&filesOutput, "files", false, "File paths only")
	cmd.Flags().BoolVar(&pretty, "pretty", false, "Human-readable previews with editor commands")
	cmd.Flags().BoolVar(&reverse, "reverse", false, "Show best results last (default with --pretty)")
	cmd.Flags().Float64Var(&threshold, "threshold", 0, "Score threshold (default from config, only with reranking)")
	cmd.Flags().BoolVar(&noTips, "no-tips", false, "Suppress tip line")

	return cmd
}

// buildHeader constructs the score-context header line.
func buildHeader(query, searchID string, result *search.SearchResult, shown int, elapsed time.Duration, cached bool) string {
	var parts []string

	if result.TotalCandidates > 0 && result.FilteredCount > 0 && shown > 0 {
		// Filtered: N/M results
		parts = append(parts, fmt.Sprintf("%d/%d results", shown, result.TotalCandidates))
	} else if shown > 0 {
		parts = append(parts, fmt.Sprintf("%d results", shown))
	} else if result.TotalCandidates > 0 {
		return fmt.Sprintf("Search: %q | id:%s | No results above %.2f threshold. %d candidates below.",
			query, searchID, result.Threshold, result.TotalCandidates)
	} else {
		return fmt.Sprintf("Search: %q | id:%s | No results.", query, searchID)
	}

	// Score mode tag.
	tags := []string{}
	if result.Reranked {
		tags = append(tags, "reranked")
	} else {
		tags = append(tags, "bm25")
	}
	if cached {
		tags = append(tags, "cached")
	}

	return fmt.Sprintf("Search: %q | id:%s | %s (%s) | %dms",
		query, searchID, parts[0], strings.Join(tags, ", "), elapsed.Milliseconds())
}

// prettyHeader constructs a styled score-context header with ANSI colors.
func prettyHeader(query, searchID string, result *search.SearchResult, shown int, elapsed time.Duration, cached bool) string {
	prefix := style("Search:", ansiBold) + " " + style(fmt.Sprintf("%q", query), ansiBold, ansiCyan)
	id := style("id:"+searchID, ansiDim)

	if shown == 0 {
		if result.TotalCandidates > 0 {
			return fmt.Sprintf("%s | %s | No results above %.2f threshold. %d candidates below.",
				prefix, id, result.Threshold, result.TotalCandidates)
		}
		return fmt.Sprintf("%s | %s | No results.", prefix, id)
	}

	var resultPart string
	if result.TotalCandidates > 0 && result.FilteredCount > 0 {
		resultPart = fmt.Sprintf("%d/%d results", shown, result.TotalCandidates)
	} else {
		resultPart = fmt.Sprintf("%d results", shown)
	}

	tags := []string{}
	if result.Reranked {
		tags = append(tags, "reranked")
	} else {
		tags = append(tags, "bm25")
	}
	if cached {
		tags = append(tags, "cached")
	}

	return fmt.Sprintf("%s | %s | %s %s | %s",
		prefix, id, resultPart,
		style("("+strings.Join(tags, ", ")+")", ansiDim),
		style(fmt.Sprintf("%dms", elapsed.Milliseconds()), ansiDim))
}

// buildTip constructs the contextual tip line for non-empty results.
func buildTip(result *search.SearchResult, searchID string, threshold float64) string {
	if result.FilteredCount > 0 {
		return fmt.Sprintf("Tip: %d below %.2f threshold. --threshold %.1f for more.\n  sift feedback %s --positive a,b --negative c",
			result.FilteredCount, threshold, threshold/2, searchID)
	}
	return fmt.Sprintf("Tip: sift feedback %s --positive a,b --negative c", searchID)
}

// buildNoResultsTip constructs a tip for zero-result scenarios.
func buildNoResultsTip(result *search.SearchResult, threshold float64) string {
	if result.TotalCandidates > 0 {
		return fmt.Sprintf("Tip: sift search \"query\" --threshold %.1f", threshold/2)
	}
	return "Try different keywords or check sift refresh."
}

// readChunkPreview reads lines from a file for the given line range.
func readChunkPreview(path string, startLine, endLine, maxChars int) string {
	s := fileutil.ReadLines(path, startLine, endLine, maxChars)
	if maxChars > 0 {
		return truncate(s, maxChars)
	}
	return s
}

// readHighlightPreview reads a preview centered on the first highlight fragment.
func readHighlightPreview(path string, startLine, endLine, maxChars int, highlights []string) string {
	content := fileutil.ReadLines(path, startLine, endLine, 0)
	if len(highlights) == 0 || maxChars <= 0 {
		return truncate(content, maxChars)
	}

	// Find the first highlight fragment in the content and center around it.
	// Strip any HTML tags Bleve might add.
	fragment := highlights[0]
	fragment = stripHTMLTags(fragment)

	// Convert to runes for correct multi-byte handling.
	runes := []rune(content)
	fragRunes := []rune(fragment)
	idx := runeIndex(runes, fragRunes)
	if idx < 0 {
		return truncate(content, maxChars)
	}

	// Center the window on the fragment.
	windowStart := max(idx-maxChars/4, 0)
	windowEnd := windowStart + maxChars
	if windowEnd > len(runes) {
		windowEnd = len(runes)
	}
	if windowStart > len(runes) {
		windowStart = len(runes)
	}

	result := string(runes[windowStart:windowEnd])
	if windowStart > 0 {
		result = "..." + result
	}
	if windowEnd < len(runes) {
		result = result + "..."
	}
	return result
}

// isMeaningfulLine returns true if the line has enough alphanumeric content to be worth showing.
func isMeaningfulLine(line string) bool {
	count := 0
	for _, r := range line {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			count++
			if count >= 4 {
				return true
			}
		}
	}
	return false
}

// readLineSnippet returns a focused line-aligned snippet from a chunk,
// centered on the lines most relevant to the given highlights.
// windowLines controls how many lines to show (e.g. 7).
// Falls back to readHighlightPreview if no highlight match is found.
func readLineSnippet(path string, startLine, endLine, windowLines int, highlights []string) string {
	content := fileutil.ReadLines(path, startLine, endLine, 0)
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")
	if len(lines) <= windowLines {
		return content // chunk is already small enough
	}

	// Find the best line by scoring each line against highlight tokens.
	bestLine := findBestLine(lines, highlights)

	// Expand outward from bestLine, collecting meaningful lines.
	type snippetLine struct {
		idx  int
		text string
	}
	var collected []snippetLine

	// Always include the best line itself.
	collected = append(collected, snippetLine{bestLine, lines[bestLine]})

	// Expand alternating above and below.
	above, below := bestLine-1, bestLine+1
	for len(collected) < windowLines && (above >= 0 || below < len(lines)) {
		if above >= 0 {
			if isMeaningfulLine(lines[above]) {
				collected = append([]snippetLine{{above, lines[above]}}, collected...)
			}
			above--
		}
		if len(collected) >= windowLines {
			break
		}
		if below < len(lines) {
			if isMeaningfulLine(lines[below]) {
				collected = append(collected, snippetLine{below, lines[below]})
			}
			below++
		}
	}

	// Build snippet from collected lines.
	var snippetLines []string
	for _, cl := range collected {
		snippetLines = append(snippetLines, cl.text)
	}
	snippet := strings.Join(snippetLines, "\n")

	// Add ellipsis based on whether we reached the edges.
	firstIdx := collected[0].idx
	lastIdx := collected[len(collected)-1].idx
	if firstIdx > 0 {
		snippet = "...\n" + snippet
	}
	if lastIdx < len(lines)-1 {
		snippet = snippet + "\n..."
	}
	return snippet
}

// findBestLine scores each line against highlight fragments and returns
// the index of the highest-scoring line. Returns 0 if no match found.
func findBestLine(lines []string, highlights []string) int {
	if len(highlights) == 0 {
		return 0
	}

	// Extract tokens from all highlight fragments.
	var tokens []string
	for _, h := range highlights {
		clean := stripHTMLTags(h)
		for _, word := range strings.Fields(clean) {
			w := strings.ToLower(strings.Trim(word, ".,;:!?\"'`()[]{}"))
			if len(w) >= 2 { // skip single-char noise
				tokens = append(tokens, w)
			}
		}
	}
	if len(tokens) == 0 {
		return 0
	}

	// Deduplicate tokens.
	seen := make(map[string]bool)
	unique := tokens[:0]
	for _, t := range tokens {
		if !seen[t] {
			seen[t] = true
			unique = append(unique, t)
		}
	}
	tokens = unique

	// Score each line by how many distinct tokens it contains.
	bestIdx := 0
	bestScore := 0
	for i, line := range lines {
		lower := strings.ToLower(line)
		score := 0
		for _, tok := range tokens {
			if strings.Contains(lower, tok) {
				score++
			}
		}
		if score > bestScore {
			bestScore = score
			bestIdx = i
		}
	}
	return bestIdx
}

// runeIndex finds the first occurrence of needle in haystack, returning the rune offset.
func runeIndex(haystack, needle []rune) int {
	if len(needle) == 0 {
		return 0
	}
	for i := 0; i <= len(haystack)-len(needle); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return i
		}
	}
	return -1
}

// stripHTMLTags removes simple HTML tags like <mark>...</mark> from Bleve highlights.
func stripHTMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
			continue
		}
		if r == '>' {
			inTag = false
			continue
		}
		if !inTag {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// shortestPath returns the shortest representation of an absolute path:
// CWD-relative, ~/relative, or absolute — whichever is shorter.
func shortestPath(abs string) string {
	shortest := abs

	// Try CWD-relative.
	if cwd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(cwd, abs); err == nil && !strings.HasPrefix(rel, "..") {
			if len(rel) < len(shortest) {
				shortest = rel
			}
		}
	}

	// Try ~/relative.
	if home, err := os.UserHomeDir(); err == nil {
		if strings.HasPrefix(abs, home) {
			homeRel := "~" + abs[len(home):]
			if len(homeRel) < len(shortest) {
				shortest = homeRel
			}
		}
	}

	return shortest
}

// isStaleFile checks if a file has been modified since it was last indexed.
func isStaleFile(path string, indexedMtime int64) bool {
	fi, err := os.Stat(path)
	if err != nil {
		return true // file missing = stale
	}
	return fi.ModTime().Unix() > indexedMtime
}

func generateSearchID() string {
	b := make([]byte, 3)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func indexLabel(i int) string {
	if i < 26 {
		return string(rune('a' + i))
	}
	return string(rune('a'+i/26-1)) + string(rune('a'+i%26))
}

func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}

func parseDuration(s string) (time.Duration, error) {
	if len(s) < 2 {
		return 0, fmt.Errorf("too short: %q", s)
	}

	numStr := s[:len(s)-1]
	unit := s[len(s)-1]

	var n int
	if _, err := fmt.Sscanf(numStr, "%d", &n); err != nil {
		return 0, fmt.Errorf("invalid number: %q", numStr)
	}

	switch unit {
	case 'h':
		return time.Duration(n) * time.Hour, nil
	case 'd':
		return time.Duration(n) * 24 * time.Hour, nil
	case 'w':
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown unit: %c (use h, d, or w)", unit)
	}
}
