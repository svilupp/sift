package search

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"sort"
	"strings"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/mapping"
	"golang.org/x/sync/errgroup"
)

// FileSearchResult represents a single line-level search result.
type FileSearchResult struct {
	LineNumber    int      // 1-based line number
	FilePath      string   // source file path (or "<stdin>")
	Content       string   // the matching line content
	Score         float64  // BM25 relevance score
	Highlights    []string // Bleve highlight fragments
	ContextBefore []string // lines above (formatted as "linenum: content")
	ContextAfter  []string // lines below (formatted as "linenum: content")
}

// FileSearchOptions controls file search behavior.
type FileSearchOptions struct {
	TopK      int
	Context   int     // lines of context (like grep -C)
	Threshold float64 // minimum score filter
	Analyzer  string  // Bleve analyzer name
}

// SearchFiles performs line-level BM25 search across the given files.
// Each file gets an ephemeral in-memory Bleve index. Multiple files are
// searched in parallel using errgroup.
func SearchFiles(ctx context.Context, query string, filePaths []string, opts FileSearchOptions) ([]FileSearchResult, error) {
	if opts.TopK <= 0 {
		opts.TopK = 20
	}
	if opts.Analyzer == "" {
		opts.Analyzer = "standard"
	}

	expandedQuery := ExpandCodeIdentifiers(query)

	type fileResult struct {
		results []FileSearchResult
	}

	g, _ := errgroup.WithContext(ctx)
	g.SetLimit(runtime.NumCPU())

	resultsCh := make([]fileResult, len(filePaths))

	for i, fp := range filePaths {
		g.Go(func() error {
			lines, err := readFileLines(fp)
			if err != nil {
				return fmt.Errorf("%s: %w", fp, err)
			}

			results, err := searchFileLines(expandedQuery, fp, lines, opts)
			if err != nil {
				return fmt.Errorf("%s: %w", fp, err)
			}
			resultsCh[i] = fileResult{results: results}
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}

	// Merge all results and sort by score descending.
	var all []FileSearchResult
	for _, fr := range resultsCh {
		all = append(all, fr.results...)
	}

	sort.Slice(all, func(i, j int) bool {
		return all[i].Score > all[j].Score
	})

	// Apply threshold filter.
	if opts.Threshold > 0 {
		filtered := all[:0]
		for _, r := range all {
			if r.Score >= opts.Threshold {
				filtered = append(filtered, r)
			}
		}
		all = filtered
	}

	// Truncate to top-K.
	if len(all) > opts.TopK {
		all = all[:opts.TopK]
	}

	return all, nil
}

// searchFileLines builds an ephemeral Bleve index over the lines and queries it.
func searchFileLines(query, filePath string, lines []string, opts FileSearchOptions) ([]FileSearchResult, error) {
	idx, err := bleve.NewMemOnly(buildFileMapping(opts.Analyzer))
	if err != nil {
		return nil, fmt.Errorf("create mem index: %w", err)
	}
	defer idx.Close()

	// Index each non-empty line with code identifier expansion.
	batch := idx.NewBatch()
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		docID := fmt.Sprintf("%d", i+1) // 1-based line number
		doc := map[string]string{
			"content": ExpandCodeIdentifiers(trimmed),
		}
		if err := batch.Index(docID, doc); err != nil {
			return nil, fmt.Errorf("index line %d: %w", i+1, err)
		}
	}
	if err := idx.Batch(batch); err != nil {
		return nil, fmt.Errorf("batch index: %w", err)
	}

	// Search.
	matchQuery := bleve.NewMatchQuery(query)
	req := bleve.NewSearchRequest(matchQuery)
	req.Size = opts.TopK
	req.Highlight = bleve.NewHighlight()

	res, err := idx.Search(req)
	if err != nil {
		return nil, fmt.Errorf("bleve search: %w", err)
	}

	var results []FileSearchResult
	for _, hit := range res.Hits {
		lineNum := 0
		if _, err := fmt.Sscanf(hit.ID, "%d", &lineNum); err != nil || lineNum < 1 || lineNum > len(lines) {
			continue
		}

		r := FileSearchResult{
			LineNumber: lineNum,
			FilePath:   filePath,
			Content:    lines[lineNum-1],
			Score:      hit.Score,
		}

		if frags, ok := hit.Fragments["content"]; ok {
			r.Highlights = frags
		}

		// Attach context lines.
		if opts.Context > 0 {
			start := max(lineNum-opts.Context, 1)
			end := min(lineNum+opts.Context, len(lines))
			for n := start; n < lineNum; n++ {
				r.ContextBefore = append(r.ContextBefore, fmt.Sprintf("%d: %s", n, lines[n-1]))
			}
			for n := lineNum + 1; n <= end; n++ {
				r.ContextAfter = append(r.ContextAfter, fmt.Sprintf("%d: %s", n, lines[n-1]))
			}
		}

		results = append(results, r)
	}

	return results, nil
}

// buildFileMapping creates a Bleve mapping for line-level indexing.
func buildFileMapping(analyzer string) mapping.IndexMapping {
	contentField := bleve.NewTextFieldMapping()
	contentField.Analyzer = analyzer
	contentField.Store = true

	docMapping := bleve.NewDocumentMapping()
	docMapping.AddFieldMappingsAt("content", contentField)
	docMapping.Dynamic = false

	indexMapping := bleve.NewIndexMapping()
	indexMapping.DefaultMapping = docMapping
	indexMapping.DefaultAnalyzer = analyzer

	return indexMapping
}

// readFileLines reads all lines from a file path.
// If path is "-", reads from stdin.
func readFileLines(path string) ([]string, error) {
	var r io.Reader
	if path == "-" {
		r = os.Stdin
	} else {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		r = f
	}

	var lines []string
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	return lines, scanner.Err()
}
