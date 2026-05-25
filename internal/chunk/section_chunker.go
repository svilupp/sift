package chunk

import (
	"fmt"
	"strings"

	"github.com/cespare/xxhash/v2"

	"sift/internal/anchor"
)

// SectionChunk extends Chunk with section metadata.
type SectionChunk struct {
	Chunk                      // embedded: Order, StartLine, EndLine, Content, CharCount, IsFrontmatter
	SectionID    string        // anchor slug e.g. "trust-zones"
	Heading      string        // raw heading text
	HeadingLevel int           // 1-6 or 0 for preamble
	ParentIdx    int           // index of parent section chunk (-1 for top-level)
	Links        []anchor.Link // cross-references found in this section
	ChunkHash    string        // xxhash64 hex of Content
}

// SectionOptions controls section-based chunking.
type SectionOptions struct {
	MaxSectionChars int // split sections larger than this (default: 2000)
	MinSectionChars int // merge sections smaller than this (default: 100)
	OverlapLines    int // overlap at boundaries (default: 5)
}

// DefaultSectionOptions returns default section chunking options.
func DefaultSectionOptions() SectionOptions {
	return SectionOptions{
		MaxSectionChars: 2000,
		MinSectionChars: 100,
		OverlapLines:    5,
	}
}

// FromSections chunks a file by markdown section boundaries.
// Each section becomes one or more chunks. Oversized sections are split
// into sub-chunks using row-based splitting. Undersized sections are merged
// with subsequent sections at the same level.
func FromSections(filePath string, content []byte, opts SectionOptions) []SectionChunk {
	if opts.MaxSectionChars <= 0 {
		opts.MaxSectionChars = 2000
	}
	if opts.OverlapLines < 0 {
		opts.OverlapLines = 0
	}

	lines := strings.Split(string(content), "\n")

	var chunks []SectionChunk
	order := 0

	// 1. Detect and extract frontmatter.
	fm := ParseFrontmatter(lines)
	fmEndLine := 0
	if fm != nil {
		fmEndLine = fm.EndLine
		sc := SectionChunk{
			Chunk: Chunk{
				Order:         order,
				StartLine:     fm.StartLine,
				EndLine:       fm.EndLine,
				Content:       fm.Raw,
				CharCount:     len(fm.Raw),
				IsFrontmatter: true,
			},
			SectionID:    "frontmatter",
			HeadingLevel: 0,
			ParentIdx:    -1,
			ChunkHash:    hashContent(fm.Raw),
		}
		chunks = append(chunks, sc)
		order++
	}

	// 2. Parse sections from the full set of lines (anchor uses 1-based lines on full file).
	sections := anchor.ParseSections(filePath, lines)

	// 3. No sections found — split into row-based sub-chunks if needed.
	if len(sections) == 0 {
		bodyStart := fmEndLine
		if bodyStart >= len(lines) {
			return chunks
		}
		body := strings.Join(lines[bodyStart:], "\n")
		body = strings.TrimRight(body, "\n")
		if strings.TrimSpace(body) == "" {
			return chunks
		}

		charCount := len(body)
		if charCount > opts.MaxSectionChars {
			// Split large no-heading file into sub-chunks.
			syntheticSec := anchor.Section{
				ID:        "",
				Heading:   "",
				Level:     0,
				StartLine: bodyStart + 1,
				EndLine:   len(lines),
				FilePath:  filePath,
			}
			subChunks := splitSection(lines, bodyStart, len(lines), syntheticSec, nil, opts, order)
			chunks = append(chunks, subChunks...)
		} else {
			sc := SectionChunk{
				Chunk: Chunk{
					Order:     order,
					StartLine: bodyStart + 1,
					EndLine:   len(lines),
					Content:   body,
					CharCount: charCount,
				},
				SectionID:    "",
				HeadingLevel: 0,
				ParentIdx:    -1,
				ChunkHash:    hashContent(body),
			}
			chunks = append(chunks, sc)
		}
		return chunks
	}

	// 4. Extract links for assignment to section chunks.
	mdLinks := anchor.ExtractMarkdownLinks(filePath, lines, sections)
	wikiLinks := anchor.ExtractWikilinks(filePath, lines, sections)
	refLinks := anchor.ExtractPathReferences(filePath, lines, sections)
	allLinks := append(mdLinks, wikiLinks...)
	allLinks = append(allLinks, refLinks...)

	// 5. Create chunks for each section.
	for _, sec := range sections {
		// Skip preamble sections that fall entirely within frontmatter.
		if fm != nil && sec.EndLine <= fmEndLine {
			continue
		}

		startIdx := sec.StartLine - 1 // convert to 0-based
		endIdx := sec.EndLine         // exclusive (endLine is 1-based inclusive, so 0-based exclusive = endLine)
		if endIdx > len(lines) {
			endIdx = len(lines)
		}
		// If preamble overlaps with frontmatter, adjust start.
		if fm != nil && startIdx < fmEndLine {
			startIdx = fmEndLine
		}

		if startIdx >= endIdx {
			continue
		}

		sectionLines := lines[startIdx:endIdx]
		coreContent := strings.Join(sectionLines, "\n")

		// Skip sections that are entirely whitespace (e.g., blank lines between frontmatter and first heading).
		if strings.TrimSpace(coreContent) == "" {
			continue
		}

		charCount := len(coreContent)

		// Collect links belonging to this section.
		var sectionLinks []anchor.Link
		for _, l := range allLinks {
			if l.SourceSection == sec.ID {
				sectionLinks = append(sectionLinks, l)
			}
		}

		if charCount > opts.MaxSectionChars {
			// Split oversized section into sub-chunks.
			subChunks := splitSection(lines, startIdx, endIdx, sec, sectionLinks, opts, order)
			chunks = append(chunks, subChunks...)
			order += len(subChunks)
		} else {
			// Single chunk for this section.
			sc := SectionChunk{
				Chunk: Chunk{
					Order:     order,
					StartLine: sec.StartLine,
					EndLine:   sec.EndLine,
					Content:   coreContent,
					CharCount: charCount,
				},
				SectionID:    sec.ID,
				Heading:      sec.Heading,
				HeadingLevel: sec.Level,
				ParentIdx:    -1,
				Links:        sectionLinks,
				ChunkHash:    hashContent(coreContent),
			}
			chunks = append(chunks, sc)
			order++
		}
	}

	// 6. Assign ParentIdx based on heading hierarchy.
	assignParentIndices(chunks)

	return chunks
}

// splitSection splits an oversized section into sub-chunks using row-based splitting.
func splitSection(allLines []string, startIdx, endIdx int, sec anchor.Section, links []anchor.Link, opts SectionOptions, startOrder int) []SectionChunk {
	// Determine rows per sub-chunk based on MaxSectionChars.
	// Estimate avg chars per line from section content.
	sectionLines := allLines[startIdx:endIdx]
	totalChars := 0
	for _, l := range sectionLines {
		totalChars += len(l) + 1 // +1 for newline
	}
	numLines := len(sectionLines)
	if numLines == 0 {
		return nil
	}
	avgCharsPerLine := totalChars / numLines
	if avgCharsPerLine == 0 {
		avgCharsPerLine = 40
	}
	rowsPerChunk := opts.MaxSectionChars / avgCharsPerLine
	if rowsPerChunk < 5 {
		rowsPerChunk = 5
	}

	var subChunks []SectionChunk
	partNum := 0

	for i := 0; i < numLines; i += rowsPerChunk {
		end := i + rowsPerChunk
		if end > numLines {
			end = numLines
		}

		subStart := startIdx + i
		subEnd := startIdx + end
		coreContent := strings.Join(allLines[subStart:subEnd], "\n")

		// Skip sub-chunks with no meaningful content.
		if strings.TrimSpace(coreContent) == "" {
			continue
		}

		contentWithOverlap := buildBoundedSectionOverlap(allLines, subStart, subEnd, startIdx, endIdx, opts.OverlapLines)

		sectionID := sec.ID
		if partNum > 0 {
			if sec.ID != "" {
				sectionID = fmt.Sprintf("%s::%d", sec.ID, partNum+1)
			} else {
				sectionID = fmt.Sprintf("part-%d", partNum+1)
			}
		}

		// Assign links: only to first sub-chunk for simplicity.
		var subLinks []anchor.Link
		if partNum == 0 {
			subLinks = links
		}

		sc := SectionChunk{
			Chunk: Chunk{
				Order:     startOrder + partNum,
				StartLine: subStart + 1, // 1-based
				EndLine:   subEnd,       // 1-based (subEnd is exclusive 0-based = inclusive 1-based)
				Content:   contentWithOverlap,
				CharCount: len(coreContent),
			},
			SectionID:    sectionID,
			Heading:      sec.Heading,
			HeadingLevel: sec.Level,
			ParentIdx:    -1,
			Links:        subLinks,
			ChunkHash:    hashContent(coreContent),
		}
		subChunks = append(subChunks, sc)
		partNum++
	}

	return subChunks
}

// buildBoundedSectionOverlap builds content string with overlap lines before and after,
// but never crosses the provided section bounds.
func buildBoundedSectionOverlap(allLines []string, startIdx, endIdx, sectionStart, sectionEnd, overlap int) string {
	overlapStart := startIdx - overlap
	if overlapStart < sectionStart {
		overlapStart = sectionStart
	}
	if overlapStart < 0 {
		overlapStart = 0
	}
	overlapEnd := endIdx + overlap
	if overlapEnd > sectionEnd {
		overlapEnd = sectionEnd
	}
	if overlapEnd > len(allLines) {
		overlapEnd = len(allLines)
	}
	return strings.Join(allLines[overlapStart:overlapEnd], "\n")
}

// assignParentIndices sets ParentIdx for each non-frontmatter SectionChunk.
// For each chunk, searches backward for the nearest chunk with a lower heading level.
func assignParentIndices(chunks []SectionChunk) {
	for i := range chunks {
		if chunks[i].IsFrontmatter || chunks[i].HeadingLevel == 0 {
			chunks[i].ParentIdx = -1
			continue
		}
		chunks[i].ParentIdx = -1
		for j := i - 1; j >= 0; j-- {
			if chunks[j].IsFrontmatter {
				continue
			}
			if chunks[j].HeadingLevel > 0 && chunks[j].HeadingLevel < chunks[i].HeadingLevel {
				chunks[i].ParentIdx = j
				break
			}
		}
	}
}

// hashContent returns the xxhash64 hex digest of s.
func hashContent(s string) string {
	return fmt.Sprintf("%016x", xxhash.Sum64String(s))
}

// WrapRowChunks converts legacy row-based Chunks to SectionChunks with empty section metadata.
func WrapRowChunks(chunks []Chunk) []SectionChunk {
	result := make([]SectionChunk, len(chunks))
	for i, c := range chunks {
		result[i] = SectionChunk{
			Chunk:        c,
			SectionID:    "",
			HeadingLevel: 0,
			ParentIdx:    -1,
			ChunkHash:    hashContent(c.Content),
		}
	}
	return result
}
