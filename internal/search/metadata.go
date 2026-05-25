package search

import (
	"fmt"
	"sort"
	"strings"

	"sift/internal/db"
)

const (
	siblingNeighborhoodLimit = 2
	relatedNeighborhoodLimit = 2
)

// NeighborhoodRef is a lightweight pointer to a nearby section.
type NeighborhoodRef struct {
	FilePath   string `json:"file"`
	SectionID  string `json:"section,omitempty"`
	Heading    string `json:"heading,omitempty"`
	CharCount  int    `json:"char_count"`
	HeadingLvl int    `json:"heading_level,omitempty"`
}

type sectionMetadataCache struct {
	engine    *Engine
	byFile    map[int64][]db.SectionSummary
	byFileMap map[int64]map[string]db.SectionSummary
	byHeading map[string][]db.SectionSummary
}

func newSectionMetadataCache(engine *Engine) *sectionMetadataCache {
	return &sectionMetadataCache{
		engine:    engine,
		byFile:    make(map[int64][]db.SectionSummary),
		byFileMap: make(map[int64]map[string]db.SectionSummary),
		byHeading: make(map[string][]db.SectionSummary),
	}
}

func (c *sectionMetadataCache) summariesForFile(fileID int64) ([]db.SectionSummary, map[string]db.SectionSummary, error) {
	if cached, ok := c.byFile[fileID]; ok {
		return cached, c.byFileMap[fileID], nil
	}

	summaries, err := c.engine.DB.GetSectionsByFile(fileID)
	if err != nil {
		return nil, nil, err
	}

	index := make(map[string]db.SectionSummary, len(summaries))
	for _, summary := range summaries {
		index[summary.SectionID] = summary
	}
	c.byFile[fileID] = summaries
	c.byFileMap[fileID] = index

	return summaries, index, nil
}

func (c *sectionMetadataCache) summariesForHeading(collectionID int64, heading string) ([]db.SectionSummary, error) {
	cacheKey := fmt.Sprintf("%d:%s", collectionID, strings.ToLower(strings.TrimSpace(heading)))
	if cached, ok := c.byHeading[cacheKey]; ok {
		return cached, nil
	}

	summaries, err := c.engine.DB.GetSectionsByHeading(collectionID, heading)
	if err != nil {
		return nil, err
	}
	c.byHeading[cacheKey] = summaries
	return summaries, nil
}

func buildSiblingRefs(current db.SectionSummary, summaries []db.SectionSummary, existing map[string]struct{}) []NeighborhoodRef {
	type ranked struct {
		summary  db.SectionSummary
		distance int
	}

	var candidates []ranked
	for _, summary := range summaries {
		if summary.SectionID == current.SectionID {
			continue
		}
		if summary.ParentSectionID != current.ParentSectionID {
			continue
		}
		if _, exists := existing[resultKey(summary.FilePath, summary.SectionID)]; exists {
			continue
		}
		candidates = append(candidates, ranked{
			summary:  summary,
			distance: abs(summary.FirstOrder - current.FirstOrder),
		})
	}

	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].distance != candidates[j].distance {
			return candidates[i].distance < candidates[j].distance
		}
		return candidates[i].summary.FirstOrder < candidates[j].summary.FirstOrder
	})

	refs := make([]NeighborhoodRef, 0, minInt(len(candidates), siblingNeighborhoodLimit))
	for _, candidate := range candidates {
		refs = append(refs, neighborhoodRef(candidate.summary))
		if len(refs) == siblingNeighborhoodLimit {
			break
		}
	}
	return refs
}

func buildRelatedRefs(current db.SectionSummary, summaries []db.SectionSummary, existing map[string]struct{}) []NeighborhoodRef {
	refs := make([]NeighborhoodRef, 0, relatedNeighborhoodLimit)
	for _, summary := range summaries {
		if summary.FilePath == current.FilePath {
			continue
		}
		if _, exists := existing[resultKey(summary.FilePath, summary.SectionID)]; exists {
			continue
		}
		refs = append(refs, neighborhoodRef(summary))
		if len(refs) == relatedNeighborhoodLimit {
			break
		}
	}
	return refs
}

func neighborhoodRef(summary db.SectionSummary) NeighborhoodRef {
	return NeighborhoodRef{
		FilePath:   summary.FilePath,
		SectionID:  summary.SectionID,
		Heading:    summary.Heading,
		CharCount:  summary.CharCount,
		HeadingLvl: summary.HeadingLevel,
	}
}

func canonicalSectionID(sectionID string) string {
	if idx := strings.Index(sectionID, "::"); idx >= 0 {
		return sectionID[:idx]
	}
	return sectionID
}

func resultKey(filePath string, sectionID string) string {
	return filePath + "\x00" + sectionID
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
