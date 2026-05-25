package cli

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sift/internal/bm25"
	"sift/internal/config"
	"sift/internal/db"
	"sift/internal/search"

	_ "modernc.org/sqlite"
)

// setupTestEnv creates a temp SIFT home and collection directory with sample files.
func setupTestEnv(t *testing.T) (siftHome string, collDir string) {
	t.Helper()

	siftHome = t.TempDir()
	t.Setenv("HOME", siftHome)

	collDir = filepath.Join(t.TempDir(), "vault")
	if err := os.MkdirAll(collDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Write sample markdown files with known content.
	files := map[string]string{
		"auth.md": `# Authentication Flow

The authentication flow begins with the client requesting a token.
The server validates the credentials against the database.
If valid, a JWT token is issued with an expiration time.
The client stores the token and sends it with subsequent requests.
Token refresh happens automatically before expiration.
This ensures secure access to protected resources.
OAuth2 is also supported for third-party integrations.
The system logs all authentication attempts for auditing.`,

		"database.md": `# Database Design

The database schema uses PostgreSQL with normalized tables.
Primary keys are auto-incrementing integers.
Foreign key constraints maintain referential integrity.
Indexes are created on frequently queried columns.
The query optimizer uses these indexes for fast lookups.
Connection pooling reduces overhead for concurrent access.
Migrations are managed with versioned SQL files.
Backups run nightly with point-in-time recovery.`,

		"search.md": `# Search Implementation

Full-text search is powered by an inverted index.
Documents are tokenized and stemmed before indexing.
BM25 scoring ranks results by relevance.
Vector embeddings capture semantic similarity.
Reciprocal Rank Fusion combines multiple ranking signals.
Reranking with a cross-encoder improves final results.
The search pipeline supports filtering by collection and time.
Results include file paths, line ranges, and content previews.`,
	}

	for name, content := range files {
		path := filepath.Join(collDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	return siftHome, collDir
}

func setupOverlappingCLIEnv(t *testing.T) (siftHome string, parentDir string, childDir string) {
	t.Helper()

	siftHome = t.TempDir()
	t.Setenv("HOME", siftHome)

	parentDir = filepath.Join(t.TempDir(), "vault")
	childDir = filepath.Join(parentDir, "docs", "agent")
	if err := os.MkdirAll(filepath.Join(childDir, "services"), 0o755); err != nil {
		t.Fatal(err)
	}

	files := map[string]string{
		filepath.Join(parentDir, "overview.md"):           "# Overview\n\nGateway ownership is summarized at the vault level.",
		filepath.Join(childDir, "services", "gateway.md"): "# Gateway\n\nGateway routing lives in the nested collection and should remain searchable via the parent after child removal.",
		filepath.Join(childDir, "api.md"):                 "# API\n\nThe nested API docs also mention gateway boundaries.",
	}
	for path, content := range files {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	return siftHome, parentDir, childDir
}

func TestIntegrationPipeline(t *testing.T) {
	siftHome, collDir := setupTestEnv(t)

	// 1. Config init
	t.Run("ConfigInit", func(t *testing.T) {
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"config", "init"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config init: %v", err)
		}

		// Verify files created.
		configPath := filepath.Join(siftHome, ".sift", "config.toml")
		if _, err := os.Stat(configPath); err != nil {
			t.Fatalf("config.toml not created: %v", err)
		}
		dbPath := filepath.Join(siftHome, ".sift", "sift.db")
		if _, err := os.Stat(dbPath); err != nil {
			t.Fatalf("sift.db not created: %v", err)
		}
	})

	// 2. Collections add
	t.Run("CollectionsAdd", func(t *testing.T) {
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"collections", "add", "vault", collDir})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("collections add: %v", err)
		}
		if !strings.Contains(buf.String(), "Added collection") {
			t.Fatalf("unexpected output: %s", buf.String())
		}
	})

	// 3. Collections list
	t.Run("CollectionsList", func(t *testing.T) {
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"collections"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("collections list: %v", err)
		}
		if !strings.Contains(buf.String(), "vault") {
			t.Fatalf("vault not in list: %s", buf.String())
		}
	})

	// 4. Refresh
	t.Run("Refresh", func(t *testing.T) {
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"refresh"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("refresh: %v", err)
		}
		output := buf.String()
		if !strings.Contains(output, "Done:") {
			t.Fatalf("unexpected refresh output: %s", output)
		}
	})

	// 5. Verify DB state
	t.Run("VerifyDB", func(t *testing.T) {
		dbPath, _ := config.DBPath()
		database, err := db.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()

		// Check collection exists.
		col, err := database.GetCollection("vault")
		if err != nil {
			t.Fatal(err)
		}
		if col == nil {
			t.Fatal("vault collection not found in DB")
		}

		// Check files indexed.
		fileCount, err := database.CollectionFileCount(col.ID)
		if err != nil {
			t.Fatal(err)
		}
		if fileCount != 3 {
			t.Fatalf("expected 3 files, got %d", fileCount)
		}

		// Check chunks created.
		chunkCount, err := database.CollectionChunkCount(col.ID)
		if err != nil {
			t.Fatal(err)
		}
		if chunkCount == 0 {
			t.Fatal("expected chunks, got 0")
		}
	})

	// 6. Verify Bleve state
	t.Run("VerifyBleve", func(t *testing.T) {
		blevePath, _ := config.BlevePath()
		bleveIdx, err := bm25.OpenBleve(blevePath, "standard")
		if err != nil {
			t.Fatal(err)
		}
		defer bleveIdx.Close()

		count, err := bleveIdx.DocCount()
		if err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			t.Fatal("expected docs in Bleve, got 0")
		}
	})

	// 7. Search — pretty output
	t.Run("SearchPretty", func(t *testing.T) {
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"search", "authentication token"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("search: %v", err)
		}
		output := buf.String()
		if !strings.Contains(output, "Search:") {
			t.Fatalf("missing search header: %s", output)
		}
		if !strings.Contains(output, "auth.md") {
			t.Fatalf("expected auth.md in results: %s", output)
		}
		if !strings.Contains(output, "sift feedback") {
			t.Fatalf("missing feedback tip: %s", output)
		}
	})

	// 8. Search — JSON output (with highlights)
	t.Run("SearchJSON", func(t *testing.T) {
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"search", "database query", "--json"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("search --json: %v", err)
		}

		var result map[string]any
		if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
			t.Fatalf("invalid JSON: %v\noutput: %s", err, buf.String())
		}
		if _, ok := result["search_id"]; !ok {
			t.Fatal("missing search_id in JSON")
		}
		results, ok := result["results"].([]any)
		if !ok {
			t.Fatal("missing results in JSON")
		}
		// BM25-only search: at least one result should have highlights.
		hasHighlights := false
		for _, r := range results {
			rm, ok := r.(map[string]any)
			if !ok {
				continue
			}
			if h, ok := rm["highlights"]; ok && h != nil {
				hasHighlights = true
				break
			}
		}
		if !hasHighlights {
			t.Fatal("expected at least one result with highlights in JSON output")
		}
	})

	// 9. Search — files output
	t.Run("SearchFiles", func(t *testing.T) {
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"search", "search index", "--files"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("search --files: %v", err)
		}
		output := strings.TrimSpace(buf.String())
		lines := strings.Split(output, "\n")
		// Should have at least one file path.
		if len(lines) == 0 || lines[0] == "" {
			t.Fatalf("expected file paths, got: %q", output)
		}
		// File paths should be deduplicated.
		seen := make(map[string]bool)
		for _, line := range lines {
			if seen[line] {
				t.Fatalf("duplicate file path: %s", line)
			}
			seen[line] = true
		}
	})

	// 10. Search session stored
	t.Run("SearchSessionStored", func(t *testing.T) {
		dbPath, _ := config.DBPath()
		database, err := db.Open(dbPath)
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()

		var count int
		err = database.QueryRow("SELECT COUNT(*) FROM search_sessions").Scan(&count)
		if err != nil {
			t.Fatal(err)
		}
		// We did 3 searches above.
		if count < 3 {
			t.Fatalf("expected at least 3 search sessions, got %d", count)
		}
	})

	// 11. Config show
	t.Run("ConfigShow", func(t *testing.T) {
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"config", "show"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config show: %v", err)
		}
		if !strings.Contains(buf.String(), "voyage-4-lite") {
			t.Fatalf("expected model in config show output: %s", buf.String())
		}
	})

	// 12. Config set/get roundtrip
	t.Run("ConfigSetGet", func(t *testing.T) {
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"config", "set", "api.voyage_api_key", "test-key-123"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config set: %v", err)
		}

		cmd = NewRootCmd("test")
		cmd.SetArgs([]string{"config", "get", "api.voyage_api_key"})
		buf.Reset()
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("config get: %v", err)
		}
		if !strings.Contains(buf.String(), "test-key-123") {
			t.Fatalf("expected test-key-123 in output: %s", buf.String())
		}
	})

	// 13. Incremental refresh — modify file, re-refresh
	t.Run("IncrementalRefresh", func(t *testing.T) {
		// Modify auth.md
		authPath := filepath.Join(collDir, "auth.md")
		content, err := os.ReadFile(authPath)
		if err != nil {
			t.Fatal(err)
		}
		content = append(content, []byte("\nNew line added for incremental test.\n")...)
		if err := os.WriteFile(authPath, content, 0644); err != nil {
			t.Fatal(err)
		}

		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"refresh"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("incremental refresh: %v", err)
		}
		output := buf.String()
		if !strings.Contains(output, "Done:") {
			t.Fatalf("unexpected output: %s", output)
		}
	})

	// 14. Dry run
	t.Run("DryRun", func(t *testing.T) {
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"refresh", "--dry-run"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("refresh dry-run: %v", err)
		}
	})

	// 15. Collection filter on search
	t.Run("SearchCollectionFilter", func(t *testing.T) {
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"search", "authentication", "-c", "vault"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("search -c vault: %v", err)
		}
		if !strings.Contains(buf.String(), "auth.md") {
			t.Fatalf("expected auth.md in filtered results: %s", buf.String())
		}
	})

	// 16. Collection filter — nonexistent
	t.Run("SearchBadCollection", func(t *testing.T) {
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"search", "test", "-c", "nonexistent"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for nonexistent collection")
		}
	})

	// 17. Pretty output — colors forced on
	t.Run("SearchPrettyColorsOn", func(t *testing.T) {
		colorMode = 1
		defer func() { colorMode = -1 }()

		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"search", "authentication token", "--pretty"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("search --pretty: %v", err)
		}
		output := buf.String()

		// Verify ANSI escape codes present.
		if !strings.Contains(output, "\033[") {
			t.Fatal("expected ANSI codes in pretty output with colors forced on")
		}
		// Verify bold cyan index label.
		wantLabel := ansiBold + ansiCyan + "[a]" + ansiReset
		if !strings.Contains(output, wantLabel) {
			t.Fatalf("expected styled index label in output")
		}
		// Verify bold file path (path may have directory prefix before "auth.md").
		if !strings.Contains(output, ansiBold) || !strings.Contains(output, "auth.md") {
			t.Fatalf("expected bold file path containing auth.md in output")
		}
		// Verify dim open command.
		if !strings.Contains(output, ansiDim+">") {
			t.Fatalf("expected dim open command in output")
		}
		// Verify bold yellow score.
		if !strings.Contains(output, ansiBold+ansiYellow+"(") {
			t.Fatalf("expected bold yellow score in output")
		}
		// Verify styled header.
		if !strings.Contains(output, ansiBold+"Search:") {
			t.Fatalf("expected bold Search: in header")
		}
		// Verify dim tip.
		if !strings.Contains(output, ansiDim+"Tip:") {
			t.Fatalf("expected dim tip line")
		}
	})

	// 18. Pretty output — colors forced off (no ANSI codes)
	t.Run("SearchPrettyColorsOff", func(t *testing.T) {
		colorMode = 0
		defer func() { colorMode = -1 }()

		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"search", "authentication", "--pretty"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("search --pretty: %v", err)
		}
		output := buf.String()

		// No ANSI codes should be present.
		if strings.Contains(output, "\033[") {
			t.Fatal("expected no ANSI codes with colors disabled")
		}
		// Should still have structure.
		if !strings.Contains(output, "[a]") {
			t.Fatal("expected index label in output")
		}
		if !strings.Contains(output, "auth.md") {
			t.Fatal("expected auth.md in output")
		}
		if !strings.Contains(output, ">") {
			t.Fatal("expected open command in output")
		}
	})

	// 19. Pretty output — separator between results
	t.Run("SearchPrettySeparator", func(t *testing.T) {
		colorMode = 0
		defer func() { colorMode = -1 }()

		// Broad query to get multiple results.
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"search", "the", "--pretty"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("search --pretty: %v", err)
		}
		output := buf.String()

		// Count results by counting index labels.
		resultCount := 0
		for _, line := range strings.Split(output, "\n") {
			if strings.HasPrefix(line, "[") && strings.Contains(line, "]") {
				resultCount++
			}
		}

		// If multiple results, expect separator lines.
		if resultCount >= 2 {
			if !strings.Contains(output, "──") {
				t.Fatalf("expected separator between %d results in pretty output", resultCount)
			}
		}
	})

	// 20. Pretty output — default mode unchanged (no ANSI, no separator)
	t.Run("SearchDefaultNoColors", func(t *testing.T) {
		colorMode = 1 // Even with colors forced on, default mode should not use them.
		defer func() { colorMode = -1 }()

		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"search", "authentication token"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("search: %v", err)
		}
		output := buf.String()

		// Default mode should NOT have separators.
		if strings.Contains(output, "──") {
			t.Fatal("default mode should not have separators")
		}
		// Default mode should NOT have styled index labels.
		if strings.Contains(output, ansiBold+ansiCyan+"[") {
			t.Fatal("default mode should not have styled index labels")
		}
	})
}

func TestOverlappingCollectionRemovalKeepsParentSearch(t *testing.T) {
	_, parentDir, childDir := setupOverlappingCLIEnv(t)

	run := func(args ...string) string {
		t.Helper()
		cmd := NewRootCmd("test")
		cmd.SetArgs(args)
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, buf.String())
		}
		return buf.String()
	}

	run("config", "init")
	run("collections", "add", "vault", parentDir)
	run("collections", "add", "agent", childDir)
	run("refresh")

	before := run("search", "gateway", "--collection", "agent")
	if !strings.Contains(before, "gateway.md") {
		t.Fatalf("expected nested search to find gateway.md before removal:\n%s", before)
	}

	run("collections", "remove", "agent", "--force")

	after := run("search", "gateway", "--collection", "vault")
	if !strings.Contains(after, "gateway.md") {
		t.Fatalf("expected parent search to keep gateway.md after nested removal:\n%s", after)
	}

	dbPath, err := config.DBPath()
	if err != nil {
		t.Fatal(err)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	if _, err := database.GetCollection("agent"); err != nil {
		t.Fatal(err)
	}
	agentCol, err := database.GetCollection("agent")
	if err != nil {
		t.Fatal(err)
	}
	if agentCol != nil {
		t.Fatal("agent collection should be removed")
	}

	sharedFile := filepath.Join(childDir, "services", "gateway.md")
	file, err := database.GetFileByPath(sharedFile)
	if err != nil {
		t.Fatal(err)
	}
	if file == nil {
		t.Fatal("shared file should remain after child collection removal")
	}

	vault, err := database.GetCollection("vault")
	if err != nil {
		t.Fatal(err)
	}
	if vault == nil {
		t.Fatal("vault collection should still exist")
	}
	if file.CollectionID != vault.ID {
		t.Fatalf("shared file primary collection = %d, want %d", file.CollectionID, vault.ID)
	}
}

func TestOpenDBRepairsMissingSignalTables(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	siftDir := filepath.Join(home, ".sift")
	if err := os.MkdirAll(siftDir, 0755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	dbPath := filepath.Join(siftDir, "sift.db")
	legacyDB, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("Open legacy db: %v", err)
	}
	if err := legacyDB.Init(); err != nil {
		t.Fatalf("Init legacy db: %v", err)
	}
	if err := legacyDB.Close(); err != nil {
		t.Fatalf("Close legacy db: %v", err)
	}

	rawDB, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}

	if _, err := rawDB.Exec("DROP TABLE backlink_counts"); err != nil {
		t.Fatalf("drop backlink_counts: %v", err)
	}
	if _, err := rawDB.Exec("DROP TABLE read_counts"); err != nil {
		t.Fatalf("drop read_counts: %v", err)
	}
	if err := rawDB.Close(); err != nil {
		t.Fatalf("close raw db: %v", err)
	}

	database, err := openDB()
	if err != nil {
		t.Fatalf("openDB: %v", err)
	}
	defer database.Close()

	for _, table := range []string{"backlink_counts", "read_counts"} {
		var name string
		if err := database.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?",
			table,
		).Scan(&name); err != nil {
			t.Fatalf("table %q not recreated: %v", table, err)
		}
	}
}

func TestPrettyHeader(t *testing.T) {
	colorMode = 1
	defer func() { colorMode = -1 }()

	t.Run("WithResults", func(t *testing.T) {
		result := &search.SearchResult{
			TotalBM25:       5,
			TotalVec:        3,
			TotalCandidates: 8,
			Reranked:        false,
		}
		got := prettyHeader("test query", "abc123", result, 3, 42*time.Millisecond, false)

		// Check bold Search: prefix.
		if !strings.Contains(got, ansiBold+"Search:"+ansiReset) {
			t.Errorf("missing bold Search: in %q", got)
		}
		// Check bold cyan query.
		if !strings.Contains(got, ansiCyan) {
			t.Errorf("missing cyan query in %q", got)
		}
		// Check dim id.
		if !strings.Contains(got, ansiDim+"id:abc123"+ansiReset) {
			t.Errorf("missing dim id in %q", got)
		}
		// Check result count.
		if !strings.Contains(got, "3 results") {
			t.Errorf("missing result count in %q", got)
		}
		// Check dim bm25 tag.
		if !strings.Contains(got, "(bm25)") {
			t.Errorf("missing bm25 tag in %q", got)
		}
		// Check dim timing.
		if !strings.Contains(got, "42ms") {
			t.Errorf("missing timing in %q", got)
		}
	})

	t.Run("NoResults", func(t *testing.T) {
		result := &search.SearchResult{}
		got := prettyHeader("test", "def456", result, 0, 10*time.Millisecond, false)

		if !strings.Contains(got, "No results.") {
			t.Errorf("missing 'No results.' in %q", got)
		}
	})

	t.Run("Cached", func(t *testing.T) {
		result := &search.SearchResult{Reranked: true}
		got := prettyHeader("test", "ghi789", result, 2, 5*time.Millisecond, true)

		if !strings.Contains(got, "reranked") {
			t.Errorf("missing reranked tag in %q", got)
		}
		if !strings.Contains(got, "cached") {
			t.Errorf("missing cached tag in %q", got)
		}
	})

	t.Run("PlainWhenColorsOff", func(t *testing.T) {
		colorMode = 0
		defer func() { colorMode = 1 }()

		result := &search.SearchResult{}
		got := prettyHeader("test", "abc", result, 1, 10*time.Millisecond, false)

		if strings.Contains(got, "\033[") {
			t.Errorf("expected no ANSI codes with colors off: %q", got)
		}
		if !strings.Contains(got, "Search:") {
			t.Errorf("missing Search: in plain header: %q", got)
		}
	})
}

func TestIndexLabel(t *testing.T) {
	tests := []struct {
		i    int
		want string
	}{
		{0, "a"},
		{1, "b"},
		{25, "z"},
		{26, "aa"},
		{27, "ab"},
	}
	for _, tt := range tests {
		got := indexLabel(tt.i)
		if got != tt.want {
			t.Errorf("indexLabel(%d) = %q, want %q", tt.i, got, tt.want)
		}
	}
}

func TestParseDuration(t *testing.T) {
	tests := []struct {
		input   string
		wantErr bool
	}{
		{"2d", false},
		{"1w", false},
		{"24h", false},
		{"", true},
		{"x", true},
		{"2x", true},
	}
	for _, tt := range tests {
		_, err := parseDuration(tt.input)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseDuration(%q) error = %v, wantErr = %v", tt.input, err, tt.wantErr)
		}
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		s      string
		maxLen int
		want   string
	}{
		{"hello", 10, "hello"},
		{"hello world", 5, "hello..."},
		{"", 5, ""},
	}
	for _, tt := range tests {
		got := truncate(tt.s, tt.maxLen)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.s, tt.maxLen, got, tt.want)
		}
	}
}

func TestReadHighlightPreview(t *testing.T) {
	// Create a temp file with known content.
	dir := t.TempDir()
	path := filepath.Join(dir, "test.md")
	content := "line one\nline two has the keyword here in the middle\nline three ends"
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		maxChars   int
		highlights []string
		wantSub    string // substring that must appear
		wantNoSub  string // substring that must NOT appear (empty = skip)
	}{
		{
			name:       "centers on highlight",
			maxChars:   30,
			highlights: []string{"<mark>keyword</mark> here"},
			wantSub:    "keyword",
		},
		{
			name:       "no highlights falls back to truncate from start",
			maxChars:   20,
			highlights: nil,
			wantSub:    "line one",
		},
		{
			name:       "fills full budget around short highlight",
			maxChars:   60,
			highlights: []string{"<mark>keyword</mark>"},
			wantSub:    "keyword",
		},
		{
			name:       "strips HTML tags from highlight",
			maxChars:   40,
			highlights: []string{"<mark>keyword</mark> here"},
			wantSub:    "keyword here",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := readHighlightPreview(path, 1, 3, tt.maxChars, tt.highlights)
			if !strings.Contains(got, tt.wantSub) {
				t.Errorf("readHighlightPreview() = %q, want substring %q", got, tt.wantSub)
			}
			if tt.wantNoSub != "" && strings.Contains(got, tt.wantNoSub) {
				t.Errorf("readHighlightPreview() = %q, should not contain %q", got, tt.wantNoSub)
			}
			// Verify preview doesn't exceed budget (plus ellipsis overhead).
			maxWithEllipsis := tt.maxChars + 6 // "..." on each side
			if len([]rune(got)) > maxWithEllipsis {
				t.Errorf("preview length %d exceeds budget %d + ellipsis", len([]rune(got)), tt.maxChars)
			}
		})
	}
}

func TestStripHTMLTags(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"<mark>hello</mark>", "hello"},
		{"no tags here", "no tags here"},
		{"<b>bold</b> and <i>italic</i>", "bold and italic"},
		{"", ""},
	}
	for _, tt := range tests {
		got := stripHTMLTags(tt.input)
		if got != tt.want {
			t.Errorf("stripHTMLTags(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestRuneIndex(t *testing.T) {
	tests := []struct {
		haystack string
		needle   string
		want     int
	}{
		{"hello world", "world", 6},
		{"hello world", "xyz", -1},
		{"hello", "", 0},
		{"café latte", "latte", 5},
	}
	for _, tt := range tests {
		got := runeIndex([]rune(tt.haystack), []rune(tt.needle))
		if got != tt.want {
			t.Errorf("runeIndex(%q, %q) = %d, want %d", tt.haystack, tt.needle, got, tt.want)
		}
	}
}

// writeFileSearchFile creates a temp file with the given content, returns its path.
func writeFileSearchFile(t *testing.T, name, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestFileSearchCLI(t *testing.T) {
	setupTestEnv(t) // sets HOME so config.Load works

	// Initialize config.
	cmd := NewRootCmd("test")
	cmd.SetArgs([]string{"config", "init"})
	var initBuf bytes.Buffer
	cmd.SetOut(&initBuf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config init: %v", err)
	}

	authFile := writeFileSearchFile(t, "auth.md", `# Authentication
The authentication flow uses JWT tokens.
Tokens expire after 24 hours.
Refresh tokens last 7 days.
OAuth2 is supported for third-party integrations.`)

	t.Run("Basic", func(t *testing.T) {
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"search", "authentication JWT", "--file", authFile})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("file search: %v", err)
		}
		output := buf.String()
		if !strings.Contains(output, "File search:") {
			t.Fatalf("missing file search header: %s", output)
		}
		if !strings.Contains(output, "JWT") {
			t.Fatalf("expected JWT in results: %s", output)
		}
	})

	t.Run("Multiple", func(t *testing.T) {
		dbFile := writeFileSearchFile(t, "db.md", `# Database
PostgreSQL handles persistence.
Connection pooling is important.`)

		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"search", "authentication", "--file", authFile, "--file", dbFile})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("file search multiple: %v", err)
		}
		output := buf.String()
		if !strings.Contains(output, "2 files") {
			t.Fatalf("expected '2 files' in header: %s", output)
		}
	})

	t.Run("JSON", func(t *testing.T) {
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"search", "authentication", "--file", authFile, "--json"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("file search --json: %v", err)
		}
		var result map[string]any
		if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
			t.Fatalf("invalid JSON: %v\noutput: %s", err, buf.String())
		}
		results, ok := result["results"].([]any)
		if !ok || len(results) == 0 {
			t.Fatal("expected results in JSON output")
		}
		// Check line number present.
		first := results[0].(map[string]any)
		if _, ok := first["line"]; !ok {
			t.Fatal("expected 'line' field in JSON result")
		}
	})

	t.Run("Pretty", func(t *testing.T) {
		colorMode = 1
		defer func() { colorMode = -1 }()

		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"search", "authentication", "--file", authFile, "--pretty"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("file search --pretty: %v", err)
		}
		output := buf.String()
		if !strings.Contains(output, "← match") {
			t.Fatalf("expected '← match' marker in pretty output: %s", output)
		}
	})

	t.Run("Files", func(t *testing.T) {
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"search", "authentication", "--file", authFile, "--files"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("file search --files: %v", err)
		}
		output := strings.TrimSpace(buf.String())
		if output != authFile {
			t.Fatalf("expected file path %s, got %s", authFile, output)
		}
	})

	t.Run("ConflictCollection", func(t *testing.T) {
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"search", "test", "--file", authFile, "--collection", "vault"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for --file + --collection")
		}
		if !strings.Contains(err.Error(), "cannot be combined") {
			t.Fatalf("expected 'cannot be combined' error, got: %v", err)
		}
	})

	t.Run("MissingFile", func(t *testing.T) {
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"search", "test", "--file", "/nonexistent/file.md"})
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		err := cmd.Execute()
		if err == nil {
			t.Fatal("expected error for missing file")
		}
	})
}

func TestSQLSchema(t *testing.T) {
	setupTestEnv(t)

	cmd := NewRootCmd("test")
	cmd.SetArgs([]string{"config", "init"})
	var initBuf bytes.Buffer
	cmd.SetOut(&initBuf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config init: %v", err)
	}

	cmd = NewRootCmd("test")
	cmd.SetArgs([]string{"sql", "--schema"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("sql --schema: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "collections") {
		t.Fatalf("expected collections table in schema output: %s", output)
	}
	if !strings.Contains(output, "path") {
		t.Fatalf("expected column names in schema output: %s", output)
	}
}

func TestSearchCacheRespectsQueryShape(t *testing.T) {
	_, collDir := setupTestEnv(t)

	cmd := NewRootCmd("test")
	cmd.SetArgs([]string{"config", "init"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config init: %v", err)
	}

	cmd = NewRootCmd("test")
	cmd.SetArgs([]string{"collections", "add", "vault", collDir})
	buf.Reset()
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("collections add: %v", err)
	}

	cmd = NewRootCmd("test")
	cmd.SetArgs([]string{"refresh"})
	buf.Reset()
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	query := "authentication database search"

	cmd = NewRootCmd("test")
	cmd.SetArgs([]string{"search", query, "--top-k", "1"})
	buf.Reset()
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("search --top-k 1: %v", err)
	}

	type searchOutput struct {
		Results []json.RawMessage `json:"results"`
		Meta    struct {
			Cached bool `json:"cached"`
		} `json:"meta"`
	}

	runJSONSearch := func(topK string) searchOutput {
		t.Helper()
		cmd := NewRootCmd("test")
		cmd.SetArgs([]string{"search", query, "--top-k", topK, "--json"})
		var out bytes.Buffer
		cmd.SetOut(&out)
		if err := cmd.Execute(); err != nil {
			t.Fatalf("search --json --top-k %s: %v", topK, err)
		}

		var decoded searchOutput
		if err := json.Unmarshal(out.Bytes(), &decoded); err != nil {
			t.Fatalf("decode json output: %v\n%s", err, out.String())
		}
		return decoded
	}

	second := runJSONSearch("5")
	if second.Meta.Cached {
		t.Fatal("expected first --top-k 5 search to miss cache")
	}
	if len(second.Results) <= 1 {
		t.Fatalf("expected --top-k 5 search to return more than one result, got %d", len(second.Results))
	}

	third := runJSONSearch("5")
	if !third.Meta.Cached {
		t.Fatal("expected repeated --top-k 5 search to hit cache")
	}
	if len(third.Results) != len(second.Results) {
		t.Fatalf("cached result count = %d, want %d", len(third.Results), len(second.Results))
	}
}
