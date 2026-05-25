package anchor

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
)

// ChunkResult is a simplified chunk result for the overlay.
type ChunkResult struct {
	FilePath  string
	StartLine int
	EndLine   int
	Score     float64
}

// SectionResult represents a section-level search result aggregated from chunk results.
type SectionResult struct {
	Section     Section
	ChunkScores []float64 // individual chunk scores that mapped to this section
	Score       float64   // aggregated score (max of chunk scores)
	FilePath    string
}

// CorpusIndex holds all parsed sections and links for a corpus.
type CorpusIndex struct {
	Files     map[string]*FileSections // path -> sections
	Links     []Link                   // all forward links
	Backlinks []Link                   // all reverse links
}

// BuildCorpusIndex scans a directory, parses all markdown files for sections and links,
// also processes .attention.json and .summary.md files.
func BuildCorpusIndex(rootDir string) (*CorpusIndex, error) {
	idx := &CorpusIndex{
		Files: make(map[string]*FileSections),
	}

	err := filepath.Walk(rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}

		relPath, relErr := filepath.Rel(rootDir, path)
		if relErr != nil {
			return relErr
		}

		switch {
		case strings.HasSuffix(path, ".attention.json"):
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			links, parseErr := ExtractAttentionLinks(relPath, data)
			if parseErr != nil {
				return nil // skip malformed attention files
			}
			idx.Links = append(idx.Links, links...)

		case strings.HasSuffix(path, ".md"):
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			lines := strings.Split(string(data), "\n")
			sections := ParseSections(relPath, lines)
			idx.Files[relPath] = &FileSections{
				Path:     relPath,
				Sections: sections,
			}

			// Extract links.
			mdLinks := ExtractMarkdownLinks(relPath, lines, sections)
			idx.Links = append(idx.Links, mdLinks...)

			wikiLinks := ExtractWikilinks(relPath, lines, sections)
			idx.Links = append(idx.Links, wikiLinks...)

			refLinks := ExtractPathReferences(relPath, lines, sections)
			idx.Links = append(idx.Links, refLinks...)

			// Check for frontmatter source (summary files).
			if fmLink := ExtractFrontmatterSource(relPath, lines); fmLink != nil {
				idx.Links = append(idx.Links, *fmLink)
			}
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	idx.Backlinks = BuildBacklinks(idx.Links)
	return idx, nil
}

// sectionKey returns a unique key for deduplication.
func sectionKey(filePath, sectionID string) string {
	return filePath + "::" + sectionID
}

// AggregateSections takes chunk-level search results and remaps them to section-level results.
// Score aggregation: use max chunk score per section.
func AggregateSections(idx *CorpusIndex, chunkResults []ChunkResult) []SectionResult {
	type agg struct {
		section Section
		scores  []float64
		maxS    float64
	}

	m := make(map[string]*agg)

	for _, cr := range chunkResults {
		fs, ok := idx.Files[cr.FilePath]
		if !ok {
			continue
		}
		sec := MapChunkToSection(fs.Sections, cr.StartLine, cr.EndLine)
		if sec == nil {
			continue
		}

		key := sectionKey(cr.FilePath, sec.ID)
		a, exists := m[key]
		if !exists {
			a = &agg{section: *sec}
			m[key] = a
		}
		a.scores = append(a.scores, cr.Score)
		if cr.Score > a.maxS {
			a.maxS = cr.Score
		}
	}

	results := make([]SectionResult, 0, len(m))
	for _, a := range m {
		results = append(results, SectionResult{
			Section:     a.section,
			ChunkScores: a.scores,
			Score:       a.maxS,
			FilePath:    a.section.FilePath,
		})
	}

	return results
}

// ExpandOneHop takes section results and adds sections linked by one hop.
// For each top section result, find all links FROM that section and all backlinks TO that section.
// Add the linked sections with a decayed score (original_score * decay).
// Deduplicate by section ID, keeping the highest score.
func ExpandOneHop(idx *CorpusIndex, sectionResults []SectionResult, decay float64) []SectionResult {
	decayFactor := decay

	// Build a map of existing results for dedup.
	bestScore := make(map[string]float64)
	resultMap := make(map[string]SectionResult)

	for _, sr := range sectionResults {
		key := sectionKey(sr.FilePath, sr.Section.ID)
		bestScore[key] = sr.Score
		resultMap[key] = sr
	}

	// For each section result, find linked sections.
	for _, sr := range sectionResults {
		decayedScore := sr.Score * decayFactor

		// Forward links from this section.
		for _, link := range idx.Links {
			if link.SourcePath != sr.FilePath || link.SourceSection != sr.Section.ID {
				continue
			}
			addLinkedSection(idx, resultMap, bestScore, link.TargetPath, link.TargetSection, decayedScore)
		}

		// Backlinks to this section.
		for _, bl := range idx.Backlinks {
			if bl.SourcePath != sr.FilePath || bl.SourceSection != sr.Section.ID {
				continue
			}
			addLinkedSection(idx, resultMap, bestScore, bl.TargetPath, bl.TargetSection, decayedScore)
		}
	}

	results := make([]SectionResult, 0, len(resultMap))
	for _, sr := range resultMap {
		results = append(results, sr)
	}
	return results
}

func addLinkedSection(
	idx *CorpusIndex,
	resultMap map[string]SectionResult,
	bestScore map[string]float64,
	targetPath, targetSection string,
	score float64,
) {
	fs, ok := idx.Files[targetPath]
	if !ok {
		return
	}

	// Find the target section.
	var sec *Section
	if targetSection == "" && len(fs.Sections) > 0 {
		sec = &fs.Sections[0]
	} else {
		for i := range fs.Sections {
			if fs.Sections[i].ID == targetSection {
				sec = &fs.Sections[i]
				break
			}
		}
	}
	if sec == nil {
		return
	}

	key := sectionKey(targetPath, sec.ID)
	if existing, ok := bestScore[key]; ok && existing >= score {
		return
	}

	bestScore[key] = score
	resultMap[key] = SectionResult{
		Section:  *sec,
		Score:    score,
		FilePath: targetPath,
	}
}

// HeadingBoost applies a score multiplier to section results where
// query terms appear in the section heading text.
// boostFactor is typically 1.5-2.5x.
func HeadingBoost(query string, results []SectionResult, boostFactor float64) []SectionResult {
	terms := tokenize(query)
	if len(terms) == 0 {
		return results
	}

	out := make([]SectionResult, len(results))
	copy(out, results)

	for i := range out {
		headingLower := strings.ToLower(out[i].Section.Heading)
		matched := 0
		for _, term := range terms {
			if strings.Contains(headingLower, term) {
				matched++
			}
		}
		if matched == 0 {
			continue
		}
		if matched == len(terms) {
			out[i].Score *= boostFactor
		} else {
			matchRatio := float64(matched) / float64(len(terms))
			out[i].Score *= 1 + (boostFactor-1)*matchRatio
		}
	}

	sort.Slice(out, func(i, j int) bool {
		return out[i].Score > out[j].Score
	})

	return out
}

// tokenize splits a query string into lowercase terms, stripping punctuation.
func tokenize(s string) []string {
	words := strings.Fields(s)
	terms := make([]string, 0, len(words))
	for _, w := range words {
		cleaned := strings.TrimFunc(strings.ToLower(w), func(r rune) bool {
			return unicode.IsPunct(r)
		})
		if cleaned != "" {
			terms = append(terms, cleaned)
		}
	}
	return terms
}
