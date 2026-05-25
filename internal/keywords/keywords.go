package keywords

import (
	"math"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// Document is a single indexed file used for keyword extraction.
type Document struct {
	RelativePath string
	Content      string
}

// Group contains the top keywords for a directory bucket.
type Group struct {
	Directory string   `json:"directory"`
	Keywords  []string `json:"keywords"`
	Notes     int      `json:"notes"`
}

// Options controls keyword extraction.
type Options struct {
	Depth int
	TopN  int
}

type groupAccumulator struct {
	noteCount int
	termFreq  map[string]float64
}

// Extract computes TF-IDF keywords per directory group.
func Extract(docs []Document, opts Options) []Group {
	if opts.Depth <= 0 {
		opts.Depth = 2
	}
	if opts.TopN <= 0 {
		opts.TopN = 8
	}

	groups := make(map[string]*groupAccumulator)
	docFreq := make(map[string]int)

	for _, doc := range docs {
		groupKey := directoryKey(doc.RelativePath, opts.Depth)
		acc := groups[groupKey]
		if acc == nil {
			acc = &groupAccumulator{termFreq: make(map[string]float64)}
			groups[groupKey] = acc
		}
		acc.noteCount++

		terms := weightedTerms(doc)
		seen := make(map[string]struct{})
		for term, tf := range terms {
			acc.termFreq[term] += tf
			if _, ok := seen[term]; !ok {
				docFreq[term]++
				seen[term] = struct{}{}
			}
		}
	}

	totalGroups := float64(len(groups))
	results := make([]Group, 0, len(groups))
	for directory, acc := range groups {
		scored := make([]termScore, 0, len(acc.termFreq))
		for term, tf := range acc.termFreq {
			df := float64(docFreq[term])
			if df == 0 {
				continue
			}
			idf := math.Log(1.0 + totalGroups/df)
			scored = append(scored, termScore{
				Term:  term,
				Score: tf * idf,
			})
		}
		sort.Slice(scored, func(i, j int) bool {
			if scored[i].Score != scored[j].Score {
				return scored[i].Score > scored[j].Score
			}
			if isBigram(scored[i].Term) != isBigram(scored[j].Term) {
				return isBigram(scored[i].Term)
			}
			return scored[i].Term < scored[j].Term
		})

		keywords := selectTopTerms(scored, opts.TopN)
		results = append(results, Group{
			Directory: directory,
			Keywords:  keywords,
			Notes:     acc.noteCount,
		})
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].Directory < results[j].Directory
	})
	return results
}

type termScore struct {
	Term  string
	Score float64
}

func selectTopTerms(scored []termScore, topN int) []string {
	var selected []string
	for _, ts := range scored {
		if !isBigram(ts.Term) {
			continue
		}
		selected = append(selected, ts.Term)
		break
	}

	for _, ts := range scored {
		if len(selected) >= topN {
			break
		}
		if isBigram(ts.Term) {
			if contains(selected, ts.Term) {
				continue
			}
			selected = removeConstituentUnigrams(selected, ts.Term)
			selected = append(selected, ts.Term)
			continue
		}
		if contains(selected, ts.Term) {
			continue
		}
		selected = append(selected, ts.Term)
	}

	if len(selected) > topN {
		selected = selected[:topN]
	}
	return selected
}

func removeConstituentUnigrams(selected []string, bigram string) []string {
	parts := strings.Fields(bigram)
	if len(parts) != 2 {
		return selected
	}
	filtered := selected[:0]
	for _, term := range selected {
		if term == parts[0] || term == parts[1] {
			continue
		}
		filtered = append(filtered, term)
	}
	return filtered
}

func weightedTerms(doc Document) map[string]float64 {
	bodyTokens, headingTokens := splitContent(doc.Content)
	filename := strings.TrimSuffix(filepath.Base(doc.RelativePath), filepath.Ext(doc.RelativePath))

	terms := make(map[string]float64)
	addWeightedTokens(terms, tokenize(filename), 2)
	addWeightedTokens(terms, headingTokens, 3)
	addWeightedTokens(terms, bodyTokens, 1)
	return terms
}

func splitContent(content string) (bodyTokens []string, headingTokens []string) {
	content = stripURLs(content)
	content = strings.ReplaceAll(content, "`", " ")

	var bodyLines []string
	for _, rawLine := range strings.Split(content, "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			headingTokens = append(headingTokens, tokenize(strings.TrimSpace(strings.TrimLeft(line, "#")))...)
			continue
		}
		bodyLines = append(bodyLines, rawLine)
	}
	bodyTokens = tokenize(strings.Join(bodyLines, "\n"))
	return bodyTokens, headingTokens
}

func addWeightedTokens(dst map[string]float64, tokens []string, weight float64) {
	for _, token := range tokens {
		dst[token] += weight
	}
	for i := 0; i+1 < len(tokens); i++ {
		bigram := tokens[i] + " " + tokens[i+1]
		dst[bigram] += weight
	}
}

func tokenize(text string) []string {
	var normalized strings.Builder
	normalized.Grow(len(text))
	prevSpace := true
	for _, r := range strings.ToLower(text) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			normalized.WriteRune(r)
			prevSpace = false
			continue
		}
		if !prevSpace {
			normalized.WriteByte(' ')
			prevSpace = true
		}
	}

	rawTokens := strings.Fields(normalized.String())
	tokens := rawTokens[:0]
	for _, token := range rawTokens {
		if len(token) < 3 {
			continue
		}
		if _, stop := stopWords[token]; stop {
			continue
		}
		tokens = append(tokens, token)
	}
	return tokens
}

func directoryKey(relativePath string, depth int) string {
	relativePath = filepath.ToSlash(relativePath)
	dir := filepath.ToSlash(filepath.Dir(relativePath))
	if dir == "." || dir == "" {
		return "./"
	}

	parts := strings.Split(dir, "/")
	if len(parts) > depth {
		parts = parts[:depth]
	}
	return strings.Join(parts, "/") + "/"
}

func stripURLs(text string) string {
	parts := strings.Fields(text)
	filtered := parts[:0]
	for _, part := range parts {
		lower := strings.ToLower(part)
		if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
			continue
		}
		filtered = append(filtered, part)
	}
	return strings.Join(filtered, " ")
}

func isBigram(term string) bool {
	return strings.Contains(term, " ")
}

func contains(values []string, value string) bool {
	for _, existing := range values {
		if existing == value {
			return true
		}
	}
	return false
}

var stopWords = map[string]struct{}{
	"about": {}, "after": {}, "also": {}, "and": {}, "are": {}, "been": {},
	"being": {}, "between": {}, "both": {}, "build": {}, "built": {}, "could": {},
	"does": {}, "for": {}, "from": {}, "have": {}, "into": {}, "just": {},
	"more": {}, "most": {}, "need": {}, "notes": {}, "only": {}, "over": {},
	"same": {}, "should": {}, "than": {}, "that": {}, "the": {}, "their": {},
	"them": {}, "then": {}, "there": {}, "these": {}, "they": {}, "this": {},
	"through": {}, "topic": {}, "using": {}, "was": {}, "what": {}, "when": {},
	"with": {}, "your": {}, "able": {}, "across": {}, "because": {}, "before": {},
	"each": {}, "make": {}, "made": {}, "file": {}, "files": {},
}
