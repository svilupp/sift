package chunk

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultOptions(t *testing.T) {
	opts := DefaultOptions()

	if opts.RowsPerChunk != 25 {
		t.Errorf("RowsPerChunk = %d, want 25", opts.RowsPerChunk)
	}
	if opts.OverlapRows != 5 {
		t.Errorf("OverlapRows = %d, want 5", opts.OverlapRows)
	}
	if opts.MinChunkChars != 200 {
		t.Errorf("MinChunkChars = %d, want 200", opts.MinChunkChars)
	}
	if !opts.SkipEmptyRows {
		t.Error("SkipEmptyRows = false, want true")
	}
}

// makeLines generates n lines of content. Each line is "Line <num>" where num
// is 1-based, matching the original file line number.
func makeLines(t *testing.T, n int) []string {
	t.Helper()
	lines := make([]string, n)
	for i := range lines {
		lines[i] = fmt.Sprintf("Line %d content here", i+1)
	}
	return lines
}

// writeTempFile writes lines to a temporary file and returns the path.
func writeTempFile(t *testing.T, dir, name string, lines []string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	content := strings.Join(lines, "\n")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	return path
}

// assertChunkOrder verifies that chunks are sequentially ordered starting at 0.
func assertChunkOrder(t *testing.T, chunks []Chunk) {
	t.Helper()
	for i, c := range chunks {
		if c.Order != i {
			t.Errorf("chunk[%d].Order = %d, want %d", i, c.Order, i)
		}
	}
}

func TestBasicChunking(t *testing.T) {
	lines := makeLines(t, 100)
	opts := Options{
		RowsPerChunk:  45,
		OverlapRows:   5,
		MinChunkChars: 0,
		SkipEmptyRows: true,
	}

	chunks := FromLines(lines, opts)

	// 100 lines / 45 per chunk = 3 chunks (45 + 45 + 10)
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3", len(chunks))
	}

	assertChunkOrder(t, chunks)

	tests := []struct {
		idx       int
		wantStart int
		wantEnd   int
	}{
		{0, 1, 45},
		{1, 46, 90},
		{2, 91, 100},
	}
	for _, tt := range tests {
		c := chunks[tt.idx]
		if c.StartLine != tt.wantStart {
			t.Errorf("chunk[%d].StartLine = %d, want %d", tt.idx, c.StartLine, tt.wantStart)
		}
		if c.EndLine != tt.wantEnd {
			t.Errorf("chunk[%d].EndLine = %d, want %d", tt.idx, c.EndLine, tt.wantEnd)
		}
	}
}

func TestEmptyFile(t *testing.T) {
	tests := []struct {
		name  string
		lines []string
	}{
		{"nil lines", nil},
		{"empty slice", []string{}},
		{"only empty lines with skip", []string{"", "", "  ", "\t"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := DefaultOptions()
			chunks := FromLines(tt.lines, opts)
			if len(chunks) != 0 {
				t.Errorf("got %d chunks, want 0", len(chunks))
			}
		})
	}
}

func TestMinCharsFilter(t *testing.T) {
	// Create lines where each line is very short so a chunk of a few lines
	// falls below the min threshold.
	lines := []string{"a", "b", "c"}
	opts := Options{
		RowsPerChunk:  3,
		OverlapRows:   0,
		MinChunkChars: 25, // "a\nb\nc" = 5 chars, well below 25
		SkipEmptyRows: false,
	}

	chunks := FromLines(lines, opts)
	if len(chunks) != 0 {
		t.Errorf("got %d chunks, want 0 (all below MinChunkChars)", len(chunks))
	}
}

func TestMinCharsFilterPartial(t *testing.T) {
	// Mix of long and short chunks. First chunk is long enough, second is not.
	// The undersized tail should be merged into the previous chunk.
	var lines []string
	for i := 0; i < 5; i++ {
		lines = append(lines, fmt.Sprintf("This is a sufficiently long line number %d with extra padding", i+1))
	}
	lines = append(lines, "x")

	opts := Options{
		RowsPerChunk:  5,
		OverlapRows:   0,
		MinChunkChars: 25,
		SkipEmptyRows: false,
	}

	chunks := FromLines(lines, opts)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1 (tail merged into previous)", len(chunks))
	}
	// Tail merged: EndLine extends to line 6.
	if chunks[0].StartLine != 1 || chunks[0].EndLine != 6 {
		t.Errorf("chunk lines = %d-%d, want 1-6", chunks[0].StartLine, chunks[0].EndLine)
	}
	if chunks[0].Order != 0 {
		t.Errorf("chunk Order = %d, want 0", chunks[0].Order)
	}
}

func TestOverlapContent(t *testing.T) {
	lines := makeLines(t, 20)
	opts := Options{
		RowsPerChunk:  10,
		OverlapRows:   3,
		MinChunkChars: 0,
		SkipEmptyRows: false,
	}

	chunks := FromLines(lines, opts)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}

	// Chunk 0: core lines 1-10, overlap extends to line 13 (3 overlap after).
	// No overlap before because it's the first chunk.
	c0Lines := strings.Split(chunks[0].Content, "\n")
	// Should have 10 core + 3 overlap = 13 lines.
	if len(c0Lines) != 13 {
		t.Errorf("chunk[0] content has %d lines, want 13 (10 core + 3 overlap after)", len(c0Lines))
	}
	// But StartLine/EndLine should reflect only core.
	if chunks[0].StartLine != 1 || chunks[0].EndLine != 10 {
		t.Errorf("chunk[0] line range = %d-%d, want 1-10", chunks[0].StartLine, chunks[0].EndLine)
	}

	// Chunk 1: core lines 11-20, overlap extends 3 before (lines 8-10).
	// No overlap after because it's the last chunk.
	c1Lines := strings.Split(chunks[1].Content, "\n")
	// Should have 3 overlap before + 10 core = 13 lines.
	if len(c1Lines) != 13 {
		t.Errorf("chunk[1] content has %d lines, want 13 (3 overlap before + 10 core)", len(c1Lines))
	}
	if chunks[1].StartLine != 11 || chunks[1].EndLine != 20 {
		t.Errorf("chunk[1] line range = %d-%d, want 11-20", chunks[1].StartLine, chunks[1].EndLine)
	}

	// Verify CharCount reflects core content only, not overlap.
	// Core for chunk 0: join lines 1-10 with newlines.
	coreContent := strings.Join(lines[:10], "\n")
	if chunks[0].CharCount != len(coreContent) {
		t.Errorf("chunk[0].CharCount = %d, want %d", chunks[0].CharCount, len(coreContent))
	}
}

func TestOverlapFirstAndLastChunk(t *testing.T) {
	lines := makeLines(t, 30)
	opts := Options{
		RowsPerChunk:  10,
		OverlapRows:   5,
		MinChunkChars: 0,
		SkipEmptyRows: false,
	}

	chunks := FromLines(lines, opts)
	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want 3", len(chunks))
	}

	// First chunk: no overlap before, 5 overlap after.
	c0Lines := strings.Split(chunks[0].Content, "\n")
	wantC0 := 10 + 5 // core + after overlap
	if len(c0Lines) != wantC0 {
		t.Errorf("first chunk content lines = %d, want %d", len(c0Lines), wantC0)
	}

	// Middle chunk: 5 overlap before, 5 overlap after.
	c1Lines := strings.Split(chunks[1].Content, "\n")
	wantC1 := 5 + 10 + 5 // before + core + after
	if len(c1Lines) != wantC1 {
		t.Errorf("middle chunk content lines = %d, want %d", len(c1Lines), wantC1)
	}

	// Last chunk: 5 overlap before, no overlap after.
	c2Lines := strings.Split(chunks[2].Content, "\n")
	wantC2 := 5 + 10 // before + core
	if len(c2Lines) != wantC2 {
		t.Errorf("last chunk content lines = %d, want %d", len(c2Lines), wantC2)
	}
}

func TestSkipEmptyRows(t *testing.T) {
	// File with interleaved empty lines.
	lines := []string{
		"Line 1",  // original line 1
		"",        // original line 2 (empty)
		"Line 3",  // original line 3
		"",        // original line 4 (empty)
		"",        // original line 5 (empty)
		"Line 6",  // original line 6
		"Line 7",  // original line 7
		"  ",      // original line 8 (whitespace-only)
		"Line 9",  // original line 9
		"Line 10", // original line 10
	}
	opts := Options{
		RowsPerChunk:  100,
		OverlapRows:   0,
		MinChunkChars: 0,
		SkipEmptyRows: true,
	}

	chunks := FromLines(lines, opts)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(chunks))
	}

	c := chunks[0]
	// StartLine should be 1 (first non-empty line's original number).
	if c.StartLine != 1 {
		t.Errorf("StartLine = %d, want 1", c.StartLine)
	}
	// EndLine should be 10 (last non-empty line's original number).
	if c.EndLine != 10 {
		t.Errorf("EndLine = %d, want 10", c.EndLine)
	}

	// Content should have 6 non-empty lines joined.
	contentLines := strings.Split(c.Content, "\n")
	if len(contentLines) != 6 {
		t.Errorf("content has %d lines, want 6 non-empty lines", len(contentLines))
	}

	// Verify original line numbers are preserved: the content should contain
	// "Line 1", "Line 3", "Line 6", "Line 7", "Line 9", "Line 10".
	want := []string{"Line 1", "Line 3", "Line 6", "Line 7", "Line 9", "Line 10"}
	for i, w := range want {
		if contentLines[i] != w {
			t.Errorf("content line %d = %q, want %q", i, contentLines[i], w)
		}
	}
}

func TestSkipEmptyRowsChunkBoundary(t *testing.T) {
	// Verify that with SkipEmptyRows, line numbers reflect originals
	// even when chunk boundaries fall differently.
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprintf("Content line %d", i))
		lines = append(lines, "") // empty line after each content line
	}
	// 40 total lines, 20 non-empty.

	opts := Options{
		RowsPerChunk:  10,
		OverlapRows:   0,
		MinChunkChars: 0,
		SkipEmptyRows: true,
	}

	chunks := FromLines(lines, opts)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2 (20 non-empty / 10 per chunk)", len(chunks))
	}

	// First chunk: non-empty lines 1,3,5,7,9,11,13,15,17,19 (original line numbers).
	if chunks[0].StartLine != 1 {
		t.Errorf("chunk[0].StartLine = %d, want 1", chunks[0].StartLine)
	}
	if chunks[0].EndLine != 19 {
		t.Errorf("chunk[0].EndLine = %d, want 19", chunks[0].EndLine)
	}

	// Second chunk: non-empty lines 21,23,25,27,29,31,33,35,37,39.
	if chunks[1].StartLine != 21 {
		t.Errorf("chunk[1].StartLine = %d, want 21", chunks[1].StartLine)
	}
	if chunks[1].EndLine != 39 {
		t.Errorf("chunk[1].EndLine = %d, want 39", chunks[1].EndLine)
	}
}

func TestNoSkipEmptyRows(t *testing.T) {
	lines := []string{
		"Line 1",
		"",
		"Line 3",
		"",
		"Line 5",
	}
	opts := Options{
		RowsPerChunk:  100,
		OverlapRows:   0,
		MinChunkChars: 0,
		SkipEmptyRows: false,
	}

	chunks := FromLines(lines, opts)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(chunks))
	}

	c := chunks[0]
	// All 5 lines should be included.
	contentLines := strings.Split(c.Content, "\n")
	if len(contentLines) != 5 {
		t.Errorf("content has %d lines, want 5", len(contentLines))
	}
	if c.StartLine != 1 {
		t.Errorf("StartLine = %d, want 1", c.StartLine)
	}
	if c.EndLine != 5 {
		t.Errorf("EndLine = %d, want 5", c.EndLine)
	}
}

func TestNoSkipEmptyRowsChunkSplit(t *testing.T) {
	// With SkipEmptyRows=false, empty lines count toward RowsPerChunk.
	lines := []string{
		"A", "", "", "", "", // 5 lines
		"B", "", "", "", "", // 5 lines
	}
	opts := Options{
		RowsPerChunk:  5,
		OverlapRows:   0,
		MinChunkChars: 0,
		SkipEmptyRows: false,
	}

	chunks := FromLines(lines, opts)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}

	if chunks[0].StartLine != 1 || chunks[0].EndLine != 5 {
		t.Errorf("chunk[0] lines = %d-%d, want 1-5", chunks[0].StartLine, chunks[0].EndLine)
	}
	if chunks[1].StartLine != 6 || chunks[1].EndLine != 10 {
		t.Errorf("chunk[1] lines = %d-%d, want 6-10", chunks[1].StartLine, chunks[1].EndLine)
	}
}

func TestSingleChunk(t *testing.T) {
	lines := makeLines(t, 10)
	opts := Options{
		RowsPerChunk:  45,
		OverlapRows:   5,
		MinChunkChars: 0,
		SkipEmptyRows: true,
	}

	chunks := FromLines(lines, opts)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(chunks))
	}

	c := chunks[0]
	if c.Order != 0 {
		t.Errorf("Order = %d, want 0", c.Order)
	}
	if c.StartLine != 1 {
		t.Errorf("StartLine = %d, want 1", c.StartLine)
	}
	if c.EndLine != 10 {
		t.Errorf("EndLine = %d, want 10", c.EndLine)
	}

	// No overlap possible since there's only one chunk.
	wantContent := strings.Join(lines, "\n")
	if c.Content != wantContent {
		t.Errorf("Content mismatch:\ngot:  %q\nwant: %q", c.Content, wantContent)
	}
}

func TestFileRead(t *testing.T) {
	dir := t.TempDir()
	lines := makeLines(t, 50)
	path := writeTempFile(t, dir, "test.md", lines)

	opts := Options{
		RowsPerChunk:  45,
		OverlapRows:   5,
		MinChunkChars: 0,
		SkipEmptyRows: true,
	}

	chunks, err := File(path, opts)
	if err != nil {
		t.Fatalf("File() error: %v", err)
	}

	// 50 lines / 45 per chunk = 2 chunks.
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}

	assertChunkOrder(t, chunks)

	if chunks[0].StartLine != 1 || chunks[0].EndLine != 45 {
		t.Errorf("chunk[0] lines = %d-%d, want 1-45", chunks[0].StartLine, chunks[0].EndLine)
	}
	if chunks[1].StartLine != 46 || chunks[1].EndLine != 50 {
		t.Errorf("chunk[1] lines = %d-%d, want 46-50", chunks[1].StartLine, chunks[1].EndLine)
	}
}

func TestFileReadEmpty(t *testing.T) {
	dir := t.TempDir()
	path := writeTempFile(t, dir, "empty.md", nil)

	chunks, err := File(path, DefaultOptions())
	if err != nil {
		t.Fatalf("File() error: %v", err)
	}
	if len(chunks) != 0 {
		t.Errorf("got %d chunks for empty file, want 0", len(chunks))
	}
}

func TestFileReadNotFound(t *testing.T) {
	_, err := File("/nonexistent/path/file.md", DefaultOptions())
	if err == nil {
		t.Error("File() should return error for nonexistent file")
	}
}

func TestFileReadWithEmptyLines(t *testing.T) {
	dir := t.TempDir()
	lines := []string{
		"First line",
		"",
		"Third line with enough content to pass filter",
		"",
		"Fifth line also with sufficient content here",
	}
	path := writeTempFile(t, dir, "with_blanks.md", lines)

	opts := Options{
		RowsPerChunk:  100,
		OverlapRows:   0,
		MinChunkChars: 0,
		SkipEmptyRows: true,
	}

	chunks, err := File(path, opts)
	if err != nil {
		t.Fatalf("File() error: %v", err)
	}
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(chunks))
	}

	// 3 non-empty lines.
	contentLines := strings.Split(chunks[0].Content, "\n")
	if len(contentLines) != 3 {
		t.Errorf("content has %d lines, want 3", len(contentLines))
	}
	if chunks[0].StartLine != 1 {
		t.Errorf("StartLine = %d, want 1", chunks[0].StartLine)
	}
	if chunks[0].EndLine != 5 {
		t.Errorf("EndLine = %d, want 5", chunks[0].EndLine)
	}
}

func TestCharCountExcludesOverlap(t *testing.T) {
	lines := makeLines(t, 20)
	opts := Options{
		RowsPerChunk:  10,
		OverlapRows:   5,
		MinChunkChars: 0,
		SkipEmptyRows: false,
	}

	chunks := FromLines(lines, opts)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}

	// CharCount for chunk 0 should be the length of lines 1-10 joined with newlines.
	coreC0 := strings.Join(lines[:10], "\n")
	if chunks[0].CharCount != len(coreC0) {
		t.Errorf("chunk[0].CharCount = %d, want %d", chunks[0].CharCount, len(coreC0))
	}

	// CharCount for chunk 1 should be the length of lines 11-20 joined with newlines.
	coreC1 := strings.Join(lines[10:20], "\n")
	if chunks[1].CharCount != len(coreC1) {
		t.Errorf("chunk[1].CharCount = %d, want %d", chunks[1].CharCount, len(coreC1))
	}

	// Content length should be longer than CharCount due to overlap.
	if len(chunks[0].Content) <= chunks[0].CharCount {
		t.Error("chunk[0] Content should be longer than CharCount due to overlap")
	}
	if len(chunks[1].Content) <= chunks[1].CharCount {
		t.Error("chunk[1] Content should be longer than CharCount due to overlap")
	}
}

func TestZeroOverlap(t *testing.T) {
	lines := makeLines(t, 20)
	opts := Options{
		RowsPerChunk:  10,
		OverlapRows:   0,
		MinChunkChars: 0,
		SkipEmptyRows: false,
	}

	chunks := FromLines(lines, opts)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}

	// With zero overlap, content should equal core content.
	coreC0 := strings.Join(lines[:10], "\n")
	if chunks[0].Content != coreC0 {
		t.Errorf("chunk[0] content should equal core when overlap=0")
	}
	if chunks[0].CharCount != len(coreC0) {
		t.Errorf("chunk[0].CharCount = %d, want %d", chunks[0].CharCount, len(coreC0))
	}
}

func TestZeroRowsPerChunkFallback(t *testing.T) {
	lines := makeLines(t, 10)
	opts := Options{
		RowsPerChunk:  0, // should fall back to 45
		OverlapRows:   0,
		MinChunkChars: 0,
		SkipEmptyRows: false,
	}

	chunks := FromLines(lines, opts)
	// 10 lines with fallback RowsPerChunk=45 means 1 chunk.
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1 (fallback to 45 rows)", len(chunks))
	}
}

func TestNegativeRowsPerChunkFallback(t *testing.T) {
	lines := makeLines(t, 10)
	opts := Options{
		RowsPerChunk:  -5, // should fall back to 45
		OverlapRows:   0,
		MinChunkChars: 0,
		SkipEmptyRows: false,
	}

	chunks := FromLines(lines, opts)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1 (fallback to 45 rows)", len(chunks))
	}
}

func TestExactChunkBoundary(t *testing.T) {
	// Exactly divisible: 90 lines / 45 = 2 chunks with no remainder.
	lines := makeLines(t, 90)
	opts := Options{
		RowsPerChunk:  45,
		OverlapRows:   0,
		MinChunkChars: 0,
		SkipEmptyRows: false,
	}

	chunks := FromLines(lines, opts)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2", len(chunks))
	}
	if chunks[0].StartLine != 1 || chunks[0].EndLine != 45 {
		t.Errorf("chunk[0] lines = %d-%d, want 1-45", chunks[0].StartLine, chunks[0].EndLine)
	}
	if chunks[1].StartLine != 46 || chunks[1].EndLine != 90 {
		t.Errorf("chunk[1] lines = %d-%d, want 46-90", chunks[1].StartLine, chunks[1].EndLine)
	}
}

func TestOneLine(t *testing.T) {
	lines := []string{"Single line with enough characters for the minimum"}
	opts := Options{
		RowsPerChunk:  45,
		OverlapRows:   5,
		MinChunkChars: 0,
		SkipEmptyRows: true,
	}

	chunks := FromLines(lines, opts)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1", len(chunks))
	}
	if chunks[0].StartLine != 1 || chunks[0].EndLine != 1 {
		t.Errorf("chunk lines = %d-%d, want 1-1", chunks[0].StartLine, chunks[0].EndLine)
	}
	if chunks[0].Content != lines[0] {
		t.Errorf("Content = %q, want %q", chunks[0].Content, lines[0])
	}
}

func TestTailMergeBelowMinChars(t *testing.T) {
	// 30-line file: 25 content + 5 short tail.
	// With RowsPerChunk=25, MinChunkChars=200, the first 25 lines should pass
	// the min filter, and the tail (5 short lines) should be merged in.
	var lines []string
	for i := 0; i < 25; i++ {
		lines = append(lines, fmt.Sprintf("This is a sufficiently long line number %d with extra padding text here", i+1))
	}
	for i := 0; i < 5; i++ {
		lines = append(lines, fmt.Sprintf("Short %d", i+26))
	}

	opts := Options{
		RowsPerChunk:  25,
		OverlapRows:   0,
		MinChunkChars: 200,
		SkipEmptyRows: false,
	}

	chunks := FromLines(lines, opts)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1 (tail merged into previous)", len(chunks))
	}
	if chunks[0].EndLine != 30 {
		t.Errorf("merged chunk EndLine = %d, want 30", chunks[0].EndLine)
	}
	if chunks[0].CharCount < 200 {
		t.Errorf("merged chunk CharCount = %d, want >= 200", chunks[0].CharCount)
	}
}

func TestTailMergeAboveMinChars(t *testing.T) {
	// 40-line file with substantial tail — both chunks should survive.
	var lines []string
	for i := 0; i < 40; i++ {
		lines = append(lines, fmt.Sprintf("This is a sufficiently long line number %d with extra padding text here", i+1))
	}

	opts := Options{
		RowsPerChunk:  25,
		OverlapRows:   0,
		MinChunkChars: 200,
		SkipEmptyRows: false,
	}

	chunks := FromLines(lines, opts)
	if len(chunks) != 2 {
		t.Fatalf("got %d chunks, want 2 (tail above MinChunkChars)", len(chunks))
	}
	for i, c := range chunks {
		if c.CharCount < 200 {
			t.Errorf("chunk[%d].CharCount = %d, want >= 200", i, c.CharCount)
		}
	}
}

func TestTailMergeSingleChunkBelowNew(t *testing.T) {
	// Single-chunk file: above old MinChunkChars (25) but below new (200).
	// Since there's no previous chunk to merge into, it should be kept.
	lines := []string{
		"Line one with some content",
		"Line two with some content",
		"Line three with some content",
	}

	opts := Options{
		RowsPerChunk:  25,
		OverlapRows:   0,
		MinChunkChars: 25, // passes min filter
		SkipEmptyRows: false,
	}

	chunks := FromLines(lines, opts)
	if len(chunks) != 1 {
		t.Fatalf("got %d chunks, want 1 (single chunk kept even if small)", len(chunks))
	}
}

func TestBuildContentWithOverlap(t *testing.T) {
	numbered := []numberedLine{
		{1, "line 1"},
		{2, "line 2"},
		{3, "line 3"},
		{4, "line 4"},
		{5, "line 5"},
		{6, "line 6"},
		{7, "line 7"},
		{8, "line 8"},
		{9, "line 9"},
		{10, "line 10"},
	}

	tests := []struct {
		name    string
		start   int
		end     int
		overlap int
		want    string
	}{
		{
			name:    "no overlap",
			start:   2,
			end:     5,
			overlap: 0,
			want:    "line 3\nline 4\nline 5",
		},
		{
			name:    "overlap both sides",
			start:   3,
			end:     7,
			overlap: 2,
			want:    "line 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9",
		},
		{
			name:    "overlap clipped at start",
			start:   0,
			end:     3,
			overlap: 5,
			want:    "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8",
		},
		{
			name:    "overlap clipped at end",
			start:   7,
			end:     10,
			overlap: 5,
			want:    "line 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10",
		},
		{
			name:    "full range",
			start:   0,
			end:     10,
			overlap: 5,
			want:    "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\nline 7\nline 8\nline 9\nline 10",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildContentWithOverlap(numbered, tt.start, tt.end, tt.overlap)
			if got != tt.want {
				t.Errorf("buildContentWithOverlap(%d, %d, %d)\ngot:  %q\nwant: %q", tt.start, tt.end, tt.overlap, got, tt.want)
			}
		})
	}
}
