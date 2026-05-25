package search

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeTempSearchFile(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.md")
	content := strings.Join(lines, "\n")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFileSearchSingleFile(t *testing.T) {
	path := writeTempSearchFile(t, []string{
		"# Authentication Guide",
		"",
		"The authentication flow uses JWT tokens.",
		"Tokens expire after 24 hours.",
		"Refresh tokens last 7 days.",
	})

	results, err := SearchFiles(context.Background(), "authentication JWT", []string{path}, FileSearchOptions{
		TopK:     10,
		Analyzer: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results, got 0")
	}
	// Best result should be the line with both "authentication" and "JWT".
	if results[0].LineNumber != 3 {
		t.Errorf("expected line 3, got %d: %q", results[0].LineNumber, results[0].Content)
	}
	if !strings.Contains(results[0].Content, "JWT") {
		t.Errorf("expected content with JWT, got %q", results[0].Content)
	}
}

func TestFileSearchMultipleFiles(t *testing.T) {
	file1 := writeTempSearchFile(t, []string{
		"Database connection pooling is important.",
		"Use connection limits to avoid overload.",
	})
	dir := t.TempDir()
	file2 := filepath.Join(dir, "api.md")
	if err := os.WriteFile(file2, []byte("API rate limiting prevents abuse.\nRate limits are per-user.\n"), 0644); err != nil {
		t.Fatal(err)
	}

	results, err := SearchFiles(context.Background(), "rate limiting", []string{file1, file2}, FileSearchOptions{
		TopK:     10,
		Analyzer: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	// Top result should come from file2 (has "rate limiting" directly).
	if results[0].FilePath != file2 {
		t.Errorf("expected top result from %s, got %s", file2, results[0].FilePath)
	}
}

func TestFileSearchParallel(t *testing.T) {
	var paths []string
	for i := 0; i < 5; i++ {
		dir := t.TempDir()
		path := filepath.Join(dir, "file.md")
		lines := []string{
			"Line one of the document.",
			"Search engine optimization techniques.",
			"Another line here.",
		}
		if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")), 0644); err != nil {
			t.Fatal(err)
		}
		paths = append(paths, path)
	}

	results, err := SearchFiles(context.Background(), "search engine", paths, FileSearchOptions{
		TopK:     20,
		Analyzer: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	// Each file has one matching line.
	if len(results) != 5 {
		t.Errorf("expected 5 results from 5 files, got %d", len(results))
	}
}

func TestFileSearchCamelCase(t *testing.T) {
	path := writeTempSearchFile(t, []string{
		"The swapId field identifies the trade.",
		"Other fields include name and description.",
	})

	results, err := SearchFiles(context.Background(), "swap id", []string{path}, FileSearchOptions{
		TopK:     10,
		Analyzer: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for camelCase expansion")
	}
	if !strings.Contains(results[0].Content, "swapId") {
		t.Errorf("expected match on swapId, got %q", results[0].Content)
	}
}

func TestFileSearchSnakeCase(t *testing.T) {
	path := writeTempSearchFile(t, []string{
		"The store_id column is the primary key.",
		"Other columns follow.",
	})

	results, err := SearchFiles(context.Background(), "store id", []string{path}, FileSearchOptions{
		TopK:     10,
		Analyzer: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for snake_case expansion")
	}
	if !strings.Contains(results[0].Content, "store_id") {
		t.Errorf("expected match on store_id, got %q", results[0].Content)
	}
}

func TestFileSearchContext(t *testing.T) {
	path := writeTempSearchFile(t, []string{
		"Line 1: introduction",
		"Line 2: background",
		"Line 3: the authentication keyword here",
		"Line 4: details",
		"Line 5: conclusion",
	})

	results, err := SearchFiles(context.Background(), "authentication", []string{path}, FileSearchOptions{
		TopK:     10,
		Context:  2,
		Analyzer: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	r := results[0]
	if len(r.ContextBefore) != 2 {
		t.Errorf("expected 2 context before lines, got %d", len(r.ContextBefore))
	}
	if len(r.ContextAfter) != 2 {
		t.Errorf("expected 2 context after lines, got %d", len(r.ContextAfter))
	}
	// Verify line numbers in context.
	if len(r.ContextBefore) > 0 && !strings.HasPrefix(r.ContextBefore[0], "1:") {
		t.Errorf("expected context before to start with '1:', got %q", r.ContextBefore[0])
	}
}

func TestFileSearchEmptyFile(t *testing.T) {
	path := writeTempSearchFile(t, []string{})

	results, err := SearchFiles(context.Background(), "anything", []string{path}, FileSearchOptions{
		TopK:     10,
		Analyzer: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for empty file, got %d", len(results))
	}
}

func TestFileSearchEmptyLines(t *testing.T) {
	path := writeTempSearchFile(t, []string{
		"",
		"",
		"The keyword appears here.",
		"",
		"",
	})

	results, err := SearchFiles(context.Background(), "keyword", []string{path}, FileSearchOptions{
		TopK:     10,
		Analyzer: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Errorf("expected 1 result (empty lines skipped), got %d", len(results))
	}
	if len(results) > 0 && results[0].LineNumber != 3 {
		t.Errorf("expected line 3, got %d", results[0].LineNumber)
	}
}

func TestFileSearchTopK(t *testing.T) {
	lines := make([]string, 20)
	for i := range lines {
		lines[i] = "This line contains the search keyword for matching."
	}
	path := writeTempSearchFile(t, lines)

	results, err := SearchFiles(context.Background(), "keyword", []string{path}, FileSearchOptions{
		TopK:     3,
		Analyzer: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Errorf("expected 3 results with TopK=3, got %d", len(results))
	}
}

func TestFileSearchThreshold(t *testing.T) {
	path := writeTempSearchFile(t, []string{
		"Exact match for authentication keyword here.",
		"Something vaguely related to auth.",
		"Completely unrelated line about cooking.",
	})

	results, err := SearchFiles(context.Background(), "authentication keyword", []string{path}, FileSearchOptions{
		TopK:      10,
		Threshold: 0.5,
		Analyzer:  "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	// All results should be above threshold.
	for _, r := range results {
		if r.Score < 0.5 {
			t.Errorf("result score %.4f below threshold 0.5: %q", r.Score, r.Content)
		}
	}
}

func TestFileSearchNoMatch(t *testing.T) {
	path := writeTempSearchFile(t, []string{
		"The quick brown fox jumps over the lazy dog.",
		"Pack my box with five dozen liquor jugs.",
	})

	results, err := SearchFiles(context.Background(), "kubernetes deployment", []string{path}, FileSearchOptions{
		TopK:     10,
		Analyzer: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results for non-matching query, got %d", len(results))
	}
}

func TestFileSearchLargeFile(t *testing.T) {
	lines := make([]string, 1000)
	for i := range lines {
		lines[i] = "This is a line of content in a large document about various topics."
	}
	lines[500] = "The specific kubernetes deployment configuration goes here."
	path := writeTempSearchFile(t, lines)

	start := time.Now()
	results, err := SearchFiles(context.Background(), "kubernetes deployment", []string{path}, FileSearchOptions{
		TopK:     10,
		Analyzer: "standard",
	})
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for large file")
	}
	if results[0].LineNumber != 501 { // 1-based
		t.Errorf("expected line 501, got %d", results[0].LineNumber)
	}
	if elapsed > 2*time.Second {
		t.Errorf("search took too long: %v", elapsed)
	}
}

func TestFileSearchNonexistentFile(t *testing.T) {
	_, err := SearchFiles(context.Background(), "test", []string{"/nonexistent/file.md"}, FileSearchOptions{
		TopK:     10,
		Analyzer: "standard",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent file")
	}
}

func TestFileSearchSpecialChars(t *testing.T) {
	path := writeTempSearchFile(t, []string{
		"| Store | ID | Region |",
		"|-------|-----|--------|",
		"| Acme  | 123 | US-East |",
		"```go",
		"func main() {}",
		"```",
		"https://example.com/api/v1/users",
	})

	results, err := SearchFiles(context.Background(), "Store ID Region", []string{path}, FileSearchOptions{
		TopK:     10,
		Analyzer: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results for markdown table")
	}
}

func TestFileSearchHighlights(t *testing.T) {
	path := writeTempSearchFile(t, []string{
		"The authentication system uses JWT tokens for security.",
		"Other systems exist but are not relevant here.",
	})

	results, err := SearchFiles(context.Background(), "authentication", []string{path}, FileSearchOptions{
		TopK:     10,
		Analyzer: "standard",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(results) == 0 {
		t.Fatal("expected results")
	}
	if len(results[0].Highlights) == 0 {
		t.Error("expected highlights in result")
	}
}
