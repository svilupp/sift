package chunk

import (
	"bufio"
	"os"
	"strings"
)

// Chunk represents a section of a file.
type Chunk struct {
	Order         int    // Sequential index (0, 1, 2...)
	StartLine     int    // 1-based, without overlap
	EndLine       int    // 1-based, without overlap
	Content       string // The actual text content (with overlap for embedding)
	CharCount     int    // Character count of content without overlap
	IsFrontmatter bool   // true for the frontmatter chunk
}

// Options controls chunking behavior.
type Options struct {
	RowsPerChunk  int
	OverlapRows   int
	MinChunkChars int
	SkipEmptyRows bool
}

// DefaultOptions returns chunking defaults matching the SPEC.
func DefaultOptions() Options {
	return Options{
		RowsPerChunk:  25,
		OverlapRows:   5,
		MinChunkChars: 200,
		SkipEmptyRows: true,
	}
}

type numberedLine struct {
	lineNum int // 1-based
	text    string
}

// File reads a file and splits it into chunks.
func File(path string, opts Options) ([]Chunk, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024) // 1MB buffer for JSONL files
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return FromLines(lines, opts), nil
}

// FromLines chunks a slice of lines into Chunks.
func FromLines(lines []string, opts Options) []Chunk {
	if opts.RowsPerChunk <= 0 {
		opts.RowsPerChunk = 45
	}

	// Detect frontmatter and create a separate chunk for it.
	var fmChunk *Chunk
	fm := ParseFrontmatter(lines)
	if fm != nil {
		fmChunk = &Chunk{
			Order:         0,
			StartLine:     fm.StartLine,
			EndLine:       fm.EndLine,
			Content:       fm.Raw,
			CharCount:     len(fm.Raw),
			IsFrontmatter: true,
		}
		// Slice lines to start after the closing delimiter.
		lines = lines[fm.EndLine:]
	}

	// Filter empty lines if configured, but keep track of original line numbers.
	// Line numbers are offset to account for removed frontmatter lines.
	lineOffset := 0
	if fm != nil {
		lineOffset = fm.EndLine
	}
	var numbered []numberedLine
	for i, line := range lines {
		if opts.SkipEmptyRows && strings.TrimSpace(line) == "" {
			continue
		}
		numbered = append(numbered, numberedLine{lineNum: i + 1 + lineOffset, text: line})
	}

	if len(numbered) == 0 {
		if fmChunk != nil {
			return []Chunk{*fmChunk}
		}
		return nil
	}

	type chunkEntry struct {
		chunk    Chunk
		startIdx int // index into numbered slice
		endIdx   int // exclusive index into numbered slice
	}

	var entries []chunkEntry
	// Track the last undersized group so the tail merge can recover it.
	var lastSkipped *chunkEntry

	for i := 0; i < len(numbered); i += opts.RowsPerChunk {
		end := i + opts.RowsPerChunk
		if end > len(numbered) {
			end = len(numbered)
		}

		chunkLines := numbered[i:end]
		startLine := chunkLines[0].lineNum
		endLine := chunkLines[len(chunkLines)-1].lineNum

		// Core content (without overlap) for char counting.
		var coreBuilder strings.Builder
		for _, nl := range chunkLines {
			if coreBuilder.Len() > 0 {
				coreBuilder.WriteByte('\n')
			}
			coreBuilder.WriteString(nl.text)
		}
		charCount := coreBuilder.Len()

		entry := chunkEntry{
			chunk: Chunk{
				StartLine: startLine,
				EndLine:   endLine,
				CharCount: charCount,
			},
			startIdx: i,
			endIdx:   end,
		}

		if charCount < opts.MinChunkChars {
			lastSkipped = &entry
			continue
		}

		lastSkipped = nil
		entry.chunk.Content = buildContentWithOverlap(numbered, i, end, opts.OverlapRows)
		entries = append(entries, entry)
	}

	// Tail merge: if the final group of rows was below MinChunkChars and
	// there is a previous chunk, merge those rows into the previous chunk.
	if lastSkipped != nil && len(entries) > 0 {
		prev := &entries[len(entries)-1]
		prev.chunk.EndLine = lastSkipped.chunk.EndLine
		prev.chunk.CharCount += lastSkipped.chunk.CharCount
		prev.chunk.Content = buildContentWithOverlap(numbered, prev.startIdx, lastSkipped.endIdx, opts.OverlapRows)
		prev.endIdx = lastSkipped.endIdx
	}

	// Assign sequential Order values, offset by 1 when frontmatter is present.
	orderOffset := 0
	if fmChunk != nil {
		orderOffset = 1
	}
	chunks := make([]Chunk, 0, len(entries)+orderOffset)
	if fmChunk != nil {
		chunks = append(chunks, *fmChunk)
	}
	for i, e := range entries {
		e.chunk.Order = i + orderOffset
		chunks = append(chunks, e.chunk)
	}

	return chunks
}

// ExtractTitle extracts a title from file lines. It checks frontmatter first
// (title/name keys), then falls back to scanning for H1/H2 headings in the body.
func ExtractTitle(lines []string) string {
	fm := ParseFrontmatter(lines)
	if fm != nil {
		if title := ExtractTitleFromFrontmatter(fm); title != "" {
			return title
		}
		// Fall through to heading scan on body lines only.
		lines = lines[fm.EndLine:]
	}
	for _, line := range lines {
		if strings.HasPrefix(line, "# ") {
			return strings.TrimSpace(line[2:])
		}
		if strings.HasPrefix(line, "## ") {
			return strings.TrimSpace(line[3:])
		}
	}
	return ""
}

// buildContentWithOverlap adds overlap rows before and after the core chunk.
func buildContentWithOverlap(numbered []numberedLine, start, end, overlap int) string {
	overlapStart := start - overlap
	if overlapStart < 0 {
		overlapStart = 0
	}
	overlapEnd := end + overlap
	if overlapEnd > len(numbered) {
		overlapEnd = len(numbered)
	}

	var b strings.Builder
	for i := overlapStart; i < overlapEnd; i++ {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(numbered[i].text)
	}
	return b.String()
}
