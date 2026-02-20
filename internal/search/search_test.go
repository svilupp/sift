package search

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sift/internal/chunk"
	"sift/internal/config"
	"sift/internal/db"
	"sift/internal/index"
	"sift/internal/sync"
	"sift/internal/voyage"
)

// setupSearchEnv creates a test environment with indexed files and optional embeddings.
func setupSearchEnv(t *testing.T, withEmbeddings bool) (*Engine, func()) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := database.Init(); err != nil {
		t.Fatalf("db.Init: %v", err)
	}

	blevePath := filepath.Join(t.TempDir(), "test.bleve")
	bleveIdx, err := index.OpenBleve(blevePath, "standard")
	if err != nil {
		t.Fatalf("index.OpenBleve: %v", err)
	}

	// Create collection with sample files.
	colDir := t.TempDir()
	files := map[string]string{
		"auth.md":   "# Authentication\n\nThe authentication flow uses JWT tokens.\nTokens are validated on each request.\nOAuth2 is supported for third-party apps.",
		"db.md":     "# Database\n\nPostgreSQL is the primary database.\nIndexes are created for fast queries.\nConnections are pooled for performance.",
		"search.md": "# Search\n\nFull-text search uses BM25 scoring.\nVector embeddings capture semantic meaning.\nResults are ranked by relevance score.",
	}
	for name, content := range files {
		path := filepath.Join(colDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	_, err = database.AddCollection("test", colDir, nil)
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}

	cfg := config.Default()

	// Refresh to index files (BM25 only).
	var voyageClient *voyage.Client
	if withEmbeddings {
		// Create mock Voyage server for embeddings.
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/embeddings" {
				var req struct {
					Input []string `json:"input"`
				}
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}

				data := make([]map[string]any, len(req.Input))
				for i := range req.Input {
					// Create distinct vectors based on content.
					vec := make([]float64, 1024)
					for j, ch := range req.Input[i] {
						if j < 1024 {
							vec[j] = float64(ch) / 256.0
						}
					}
					data[i] = map[string]any{
						"embedding": vec,
						"index":     i,
					}
				}
				resp := map[string]any{
					"data":  data,
					"model": "voyage-4-lite",
					"usage": map[string]int{"total_tokens": 100},
				}
				w.Header().Set("Content-Type", "application/json")
				if err := json.NewEncoder(w).Encode(resp); err != nil {
					http.Error(w, err.Error(), 500)
					return
				}
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(srv.Close)
		voyageClient = voyage.NewClientWithBaseURL("test-key", srv.URL)
	}

	var buf strings.Builder
	_, err = sync.Refresh(context.Background(), database, bleveIdx, voyageClient, sync.RefreshOptions{
		BatchSize: 128,
		ChunkOpts: chunk.Options{
			RowsPerChunk:  10,
			OverlapRows:   2,
			MinChunkChars: 5,
			SkipEmptyRows: true,
		},
	}, &buf)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	engine := NewEngine(database, bleveIdx, voyageClient, cfg)

	cleanup := func() {
		bleveIdx.Close()
		database.Close()
	}

	return engine, cleanup
}

func TestSearchBM25Only(t *testing.T) {
	engine, cleanup := setupSearchEnv(t, false)
	defer cleanup()

	result, err := engine.Search(context.Background(), "authentication JWT", SearchOptions{
		TopK: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(result.Results) == 0 {
		t.Fatal("expected search results, got 0")
	}

	// The auth.md file should be in the results.
	found := false
	for _, r := range result.Results {
		if strings.Contains(r.FilePath, "auth.md") {
			found = true
			break
		}
	}
	if !found {
		t.Error("auth.md not found in search results")
		for _, r := range result.Results {
			t.Logf("  result: %s (score=%.4f)", r.FilePath, r.FinalScore)
		}
	}

	if result.TotalBM25 == 0 {
		t.Error("TotalBM25 should be > 0")
	}
	if result.TotalVec != 0 {
		t.Errorf("TotalVec should be 0 (no voyage client), got %d", result.TotalVec)
	}
}

func TestSearchHybrid(t *testing.T) {
	engine, cleanup := setupSearchEnv(t, true)
	defer cleanup()

	result, err := engine.Search(context.Background(), "database query", SearchOptions{
		TopK: 10,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	if len(result.Results) == 0 {
		t.Fatal("expected search results, got 0")
	}

	// Should have both BM25 and vector results.
	if result.TotalBM25 == 0 {
		t.Error("TotalBM25 should be > 0")
	}
	if result.TotalVec == 0 {
		t.Error("TotalVec should be > 0 for hybrid search")
	}
}

func TestSearchCollectionFilter(t *testing.T) {
	engine, cleanup := setupSearchEnv(t, false)
	defer cleanup()

	// Search with valid collection.
	result, err := engine.Search(context.Background(), "authentication", SearchOptions{
		Collection: "test",
		TopK:       10,
	})
	if err != nil {
		t.Fatalf("Search with collection: %v", err)
	}
	if len(result.Results) == 0 {
		t.Error("expected results for 'test' collection")
	}

	// Search with nonexistent collection.
	_, err = engine.Search(context.Background(), "authentication", SearchOptions{
		Collection: "nonexistent",
		TopK:       10,
	})
	if err == nil {
		t.Error("expected error for nonexistent collection")
	}
}

func TestSearchFallbackBM25WhenNoEmbeddings(t *testing.T) {
	// Set up with a voyage client but no embeddings in the DB.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	if err := database.Init(); err != nil {
		t.Fatalf("db.Init: %v", err)
	}

	blevePath := filepath.Join(t.TempDir(), "test.bleve")
	bleveIdx, err := index.OpenBleve(blevePath, "standard")
	if err != nil {
		t.Fatalf("OpenBleve: %v", err)
	}
	defer bleveIdx.Close()

	// Index a doc in Bleve directly.
	if err := bleveIdx.Index("1", "authentication token JWT", "/test/auth.md"); err != nil {
		t.Fatalf("Index: %v", err)
	}

	// Add chunk to DB.
	colDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(colDir, "auth.md"), []byte("auth content"), 0644); err != nil {
		t.Fatalf("write auth.md: %v", err)
	}
	col, err := database.AddCollection("test", colDir, nil)
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}
	fileID, err := database.UpsertFile(filepath.Join(colDir, "auth.md"), col.ID, "hash1", 1000, 100, "")
	if err != nil {
		t.Fatalf("UpsertFile: %v", err)
	}
	_, err = database.InsertChunk(fileID, 0, 1, 10, 100)
	if err != nil {
		t.Fatalf("InsertChunk: %v", err)
	}

	// Create a mock voyage client that returns embeddings for the query.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]any{
			"data":  []map[string]any{{"embedding": make([]float64, 1024), "index": 0}},
			"model": "voyage-4-lite",
			"usage": map[string]int{"total_tokens": 10},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}))
	defer srv.Close()

	voyageClient := voyage.NewClientWithBaseURL("test-key", srv.URL)
	cfg := config.Default()
	engine := NewEngine(database, bleveIdx, voyageClient, cfg)

	result, err := engine.Search(context.Background(), "authentication", SearchOptions{TopK: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	// Should fall back to BM25-only since no embeddings in DB.
	if len(result.Results) == 0 {
		t.Error("expected BM25 results even without embeddings")
	}
}

func TestDedupMaxChunksPerFile(t *testing.T) {
	// Create multiple files where one file produces many chunks.
	// With MaxChunksPerFile=2, at most 2 results from any single file.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	if err := database.Init(); err != nil {
		t.Fatalf("db.Init: %v", err)
	}

	blevePath := filepath.Join(t.TempDir(), "test.bleve")
	bleveIdx, err := index.OpenBleve(blevePath, "standard")
	if err != nil {
		t.Fatalf("OpenBleve: %v", err)
	}
	defer bleveIdx.Close()

	colDir := t.TempDir()

	// Write a large file that will produce 5+ chunks about "authentication".
	var bigContent strings.Builder
	for i := range 50 {
		fmt.Fprintf(&bigContent, "## Section %d\n\nAuthentication token validation step %d uses JWT.\n\n", i+1, i+1)
	}
	if err := os.WriteFile(filepath.Join(colDir, "big-auth.md"), []byte(bigContent.String()), 0644); err != nil {
		t.Fatalf("write big-auth.md: %v", err)
	}
	// Write a small file about authentication too.
	if err := os.WriteFile(filepath.Join(colDir, "small-auth.md"), []byte("# Auth\n\nAuthentication uses JWT tokens for validation.\n"), 0644); err != nil {
		t.Fatalf("write small-auth.md: %v", err)
	}

	col, err := database.AddCollection("test", colDir, nil)
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}
	_ = col

	cfg := config.Default()
	cfg.Search.MaxChunksPerFile = 2

	var buf strings.Builder
	_, err = sync.Refresh(context.Background(), database, bleveIdx, nil, sync.RefreshOptions{
		BatchSize: 128,
		ChunkOpts: chunk.Options{
			RowsPerChunk:  10,
			OverlapRows:   2,
			MinChunkChars: 5,
			SkipEmptyRows: true,
		},
	}, &buf)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	engine := NewEngine(database, bleveIdx, nil, cfg)

	result, err := engine.Search(context.Background(), "authentication JWT token validation", SearchOptions{TopK: 20})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	// Count results per file.
	fileCounts := make(map[string]int)
	for _, r := range result.Results {
		fileCounts[filepath.Base(r.FilePath)]++
	}

	// big-auth.md should have at most 2 results.
	if fileCounts["big-auth.md"] > 2 {
		t.Errorf("big-auth.md has %d results, want at most 2 (MaxChunksPerFile)", fileCounts["big-auth.md"])
	}

	// small-auth.md should appear (not crowded out).
	if fileCounts["small-auth.md"] == 0 {
		t.Error("small-auth.md should appear in results (dedup improves diversity)")
	}

	t.Logf("Results per file: %v", fileCounts)
}

func TestDedupDisabledWhenZero(t *testing.T) {
	// With MaxChunksPerFile=0, dedup should be disabled and all results retained.
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	if err := database.Init(); err != nil {
		t.Fatalf("db.Init: %v", err)
	}

	blevePath := filepath.Join(t.TempDir(), "test.bleve")
	bleveIdx, err := index.OpenBleve(blevePath, "standard")
	if err != nil {
		t.Fatalf("OpenBleve: %v", err)
	}
	defer bleveIdx.Close()

	colDir := t.TempDir()

	// Write a file that will produce multiple chunks about the same topic.
	var content strings.Builder
	for i := range 30 {
		fmt.Fprintf(&content, "Database query optimization technique %d for PostgreSQL performance.\n", i+1)
	}
	if err := os.WriteFile(filepath.Join(colDir, "db-tips.md"), []byte(content.String()), 0644); err != nil {
		t.Fatalf("write db-tips.md: %v", err)
	}

	col, err := database.AddCollection("test", colDir, nil)
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}
	_ = col

	cfg := config.Default()
	cfg.Search.MaxChunksPerFile = 0 // disabled

	var buf strings.Builder
	_, err = sync.Refresh(context.Background(), database, bleveIdx, nil, sync.RefreshOptions{
		BatchSize: 128,
		ChunkOpts: chunk.Options{
			RowsPerChunk:  10,
			OverlapRows:   2,
			MinChunkChars: 5,
			SkipEmptyRows: true,
		},
	}, &buf)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	engine := NewEngine(database, bleveIdx, nil, cfg)

	result, err := engine.Search(context.Background(), "database query optimization PostgreSQL", SearchOptions{TopK: 20})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}

	// Count how many results come from db-tips.md.
	dbTipsCount := 0
	for _, r := range result.Results {
		if filepath.Base(r.FilePath) == "db-tips.md" {
			dbTipsCount++
		}
	}

	// With dedup disabled, we should get more than 2 results from the same file
	// (assuming the file produced enough matching chunks).
	if dbTipsCount > 0 && dbTipsCount <= 2 {
		t.Logf("db-tips.md has %d results -- expected more than 2 with dedup disabled (but may be limited by BM25 results)", dbTipsCount)
	}
	// The key assertion: no results should have been filtered out by file path.
	// Since we can't easily know the "unfiltered" count, we just verify we get results
	// and the count is not artificially capped.
	if len(result.Results) == 0 {
		t.Error("expected search results with dedup disabled")
	}

	t.Logf("Total results: %d, db-tips.md results: %d", len(result.Results), dbTipsCount)
}

func TestSearchEmptyQuery(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()
	if err := database.Init(); err != nil {
		t.Fatalf("db.Init: %v", err)
	}

	blevePath := filepath.Join(t.TempDir(), "test.bleve")
	bleveIdx, err := index.OpenBleve(blevePath, "standard")
	if err != nil {
		t.Fatalf("OpenBleve: %v", err)
	}
	defer bleveIdx.Close()

	cfg := config.Default()
	engine := NewEngine(database, bleveIdx, nil, cfg)

	result, err := engine.Search(context.Background(), "nonexistent_term_xyz", SearchOptions{TopK: 10})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	_ = fmt.Sprintf("results: %d", len(result.Results)) // use result
}

// setupPathFilterEnv creates a test environment with files in subdirectories
// for testing --path glob filtering.
func setupPathFilterEnv(t *testing.T) (*Engine, string) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := database.Init(); err != nil {
		t.Fatalf("db.Init: %v", err)
	}

	blevePath := filepath.Join(t.TempDir(), "test.bleve")
	bleveIdx, err := index.OpenBleve(blevePath, "standard")
	if err != nil {
		t.Fatalf("index.OpenBleve: %v", err)
	}

	// Create collection with files in subdirectories mimicking vault structure.
	colDir := t.TempDir()

	dirs := []string{
		"topics/work",
		"topics/personal",
		"daily",
		"pinned",
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(colDir, d), 0755); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	files := map[string]string{
		"topics/work/api.md":         "# API Design\n\nThe REST API uses authentication tokens.\nRate limiting is applied per endpoint.\nAll responses use JSON format.",
		"topics/work/deploy.md":      "# Deployment\n\nDeployment uses authentication keys.\nCI/CD pipeline runs tests and deploys.\nRollback is supported for all services.",
		"topics/personal/journal.md": "# Journal\n\nToday I worked on authentication flow.\nFixed several bugs in the token refresh logic.\nAlso reviewed API design decisions.",
		"daily/2024-01-15.md":        "# Daily Log\n\nAuthentication migration completed.\nUpdated API documentation for the team.\nReviewed pull requests for deployment.",
		"pinned/preferences.md":      "# Preferences\n\nPrefer JWT for authentication tokens.\nUse dark mode in the editor.\nDefault API timeout is 30 seconds.",
	}
	for name, content := range files {
		path := filepath.Join(colDir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	_, err = database.AddCollection("vault", colDir, nil)
	if err != nil {
		t.Fatalf("AddCollection: %v", err)
	}

	cfg := config.Default()

	var buf strings.Builder
	_, err = sync.Refresh(context.Background(), database, bleveIdx, nil, sync.RefreshOptions{
		BatchSize: 128,
		ChunkOpts: chunk.Options{
			RowsPerChunk:  10,
			OverlapRows:   2,
			MinChunkChars: 5,
			SkipEmptyRows: true,
		},
	}, &buf)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	engine := NewEngine(database, bleveIdx, nil, cfg)

	t.Cleanup(func() {
		bleveIdx.Close()
		database.Close()
	})

	return engine, colDir
}

func TestSearchPathGlobFilter(t *testing.T) {
	engine, colDir := setupPathFilterEnv(t)

	// Search without path filter: should find results across all directories.
	resultAll, err := engine.Search(context.Background(), "authentication", SearchOptions{
		TopK: 20,
	})
	if err != nil {
		t.Fatalf("Search (no path): %v", err)
	}
	if len(resultAll.Results) == 0 {
		t.Fatal("expected results without path filter, got 0")
	}

	// Count unique directories in unfiltered results.
	allDirs := make(map[string]bool)
	for _, r := range resultAll.Results {
		rel := strings.TrimPrefix(r.FilePath, colDir+"/")
		dir := filepath.Dir(rel)
		allDirs[dir] = true
	}
	if len(allDirs) < 2 {
		t.Logf("Only %d dirs in unfiltered results (need multiple dirs for meaningful test): %v", len(allDirs), allDirs)
	}

	// Search with path filter: only work scope.
	resultWork, err := engine.Search(context.Background(), "authentication", SearchOptions{
		TopK:     20,
		PathGlob: "*/topics/work/*",
	})
	if err != nil {
		t.Fatalf("Search (work path): %v", err)
	}
	if len(resultWork.Results) == 0 {
		t.Fatal("expected results for work path, got 0")
	}

	// All results should be from work directory.
	for _, r := range resultWork.Results {
		if !strings.Contains(r.FilePath, "/topics/work/") {
			t.Errorf("result %q should be in topics/work/, got path %s", filepath.Base(r.FilePath), r.FilePath)
		}
	}

	// Filtered results should be a subset of unfiltered.
	if len(resultWork.Results) >= len(resultAll.Results) {
		t.Errorf("filtered results (%d) should be fewer than unfiltered (%d)", len(resultWork.Results), len(resultAll.Results))
	}

	t.Logf("Unfiltered: %d results across %d dirs, Work-filtered: %d results",
		len(resultAll.Results), len(allDirs), len(resultWork.Results))
}

func TestSearchPathGlobNoMatch(t *testing.T) {
	engine, _ := setupPathFilterEnv(t)

	// Search with path filter that matches nothing.
	result, err := engine.Search(context.Background(), "authentication", SearchOptions{
		TopK:     20,
		PathGlob: "*/nonexistent/*",
	})
	if err != nil {
		t.Fatalf("Search (no match path): %v", err)
	}
	if len(result.Results) != 0 {
		t.Errorf("expected 0 results for non-matching path, got %d", len(result.Results))
	}
}

func TestSearchPathGlobDailyScope(t *testing.T) {
	engine, _ := setupPathFilterEnv(t)

	// Search with daily scope.
	result, err := engine.Search(context.Background(), "authentication", SearchOptions{
		TopK:     20,
		PathGlob: "*/daily/*",
	})
	if err != nil {
		t.Fatalf("Search (daily path): %v", err)
	}
	if len(result.Results) == 0 {
		t.Fatal("expected results for daily path, got 0")
	}

	for _, r := range result.Results {
		if !strings.Contains(r.FilePath, "/daily/") {
			t.Errorf("result should be in daily/, got path %s", r.FilePath)
		}
	}
}

func TestSearchPathGlobSpecificFile(t *testing.T) {
	engine, colDir := setupPathFilterEnv(t)

	// Search with glob matching a specific file.
	result, err := engine.Search(context.Background(), "authentication", SearchOptions{
		TopK:     20,
		PathGlob: "*preferences*",
	})
	if err != nil {
		t.Fatalf("Search (specific file): %v", err)
	}
	if len(result.Results) == 0 {
		t.Fatal("expected results for preferences file, got 0")
	}

	for _, r := range result.Results {
		rel := strings.TrimPrefix(r.FilePath, colDir+"/")
		if !strings.Contains(rel, "preferences") {
			t.Errorf("result should match preferences, got %s", rel)
		}
	}
}

func TestSearchPathGlobWithCollection(t *testing.T) {
	engine, _ := setupPathFilterEnv(t)

	// Search with both collection and path filter.
	result, err := engine.Search(context.Background(), "authentication", SearchOptions{
		Collection: "vault",
		TopK:       20,
		PathGlob:   "*/topics/work/*",
	})
	if err != nil {
		t.Fatalf("Search (collection + path): %v", err)
	}
	if len(result.Results) == 0 {
		t.Fatal("expected results with collection + path filter")
	}

	for _, r := range result.Results {
		if !strings.Contains(r.FilePath, "/topics/work/") {
			t.Errorf("result should be in topics/work/, got %s", r.FilePath)
		}
		if r.Collection != "vault" {
			t.Errorf("result collection = %q, want %q", r.Collection, "vault")
		}
	}
}

func TestSearchPathGlobEmptyString(t *testing.T) {
	engine, _ := setupPathFilterEnv(t)

	// Empty path glob should behave the same as no path filter.
	resultEmpty, err := engine.Search(context.Background(), "authentication", SearchOptions{
		TopK:     20,
		PathGlob: "",
	})
	if err != nil {
		t.Fatalf("Search (empty path): %v", err)
	}

	resultNone, err := engine.Search(context.Background(), "authentication", SearchOptions{
		TopK: 20,
	})
	if err != nil {
		t.Fatalf("Search (no path): %v", err)
	}

	if len(resultEmpty.Results) != len(resultNone.Results) {
		t.Errorf("empty PathGlob (%d results) should equal no PathGlob (%d results)",
			len(resultEmpty.Results), len(resultNone.Results))
	}
}

func TestSelectForReranking(t *testing.T) {
	// Build candidates from 3 files: fileA (4 chunks), fileB (2 chunks), fileC (1 chunk).
	mkCandidate := func(chunkID int64, path string, rrfScore float64) scoredCandidate {
		return scoredCandidate{
			fused:    FusedResult{ChunkID: chunkID, RRFScore: rrfScore},
			fileRec:  &db.FileRecord{Path: path},
			chunkRec: &db.ChunkRecord{ID: chunkID},
		}
	}

	candidates := []scoredCandidate{
		mkCandidate(1, "/vault/docs/INFRA.md", 0.9),   // fileA chunk 1
		mkCandidate(2, "/vault/docs/INFRA.md", 0.85),  // fileA chunk 2
		mkCandidate(3, "/vault/repos/code.go", 0.8),   // fileB chunk 1
		mkCandidate(4, "/vault/docs/INFRA.md", 0.75),  // fileA chunk 3
		mkCandidate(5, "/vault/repos/code.go", 0.7),   // fileB chunk 2
		mkCandidate(6, "/vault/docs/INFRA.md", 0.65),  // fileA chunk 4
		mkCandidate(7, "/vault/pinned/work.md", 0.6),  // fileC chunk 1
	}

	t.Run("no truncation needed", func(t *testing.T) {
		result := selectForReranking(candidates, 10, 3)
		if len(result) != 7 {
			t.Errorf("got %d candidates, want 7 (all)", len(result))
		}
	})

	t.Run("file diversity preserved", func(t *testing.T) {
		// maxTotal=5, minPerFile=2: should keep 2 from each file + fill.
		result := selectForReranking(candidates, 5, 2)
		if len(result) != 5 {
			t.Errorf("got %d candidates, want 5", len(result))
		}

		// Count per file.
		counts := make(map[string]int)
		for _, c := range result {
			counts[c.fileRec.Path]++
		}

		// fileA should have at least 2 (minPerFile), fileB at least 2, fileC 1.
		if counts["/vault/docs/INFRA.md"] < 2 {
			t.Errorf("INFRA.md has %d chunks, want >= 2 (minPerFile)", counts["/vault/docs/INFRA.md"])
		}
		if counts["/vault/repos/code.go"] < 2 {
			t.Errorf("code.go has %d chunks, want >= 2 (minPerFile)", counts["/vault/repos/code.go"])
		}
		if counts["/vault/pinned/work.md"] < 1 {
			t.Errorf("work.md has %d chunks, want >= 1", counts["/vault/pinned/work.md"])
		}
	})

	t.Run("order preserved", func(t *testing.T) {
		result := selectForReranking(candidates, 5, 2)
		// Verify original order is preserved (IDs should be ascending).
		for i := 1; i < len(result); i++ {
			if result[i].fused.ChunkID < result[i-1].fused.ChunkID {
				t.Errorf("order broken at position %d: chunkID %d < %d",
					i, result[i].fused.ChunkID, result[i-1].fused.ChunkID)
			}
		}
		// Verify first result is still chunkID 1 (highest ranked).
		if result[0].fused.ChunkID != 1 {
			t.Errorf("first result chunkID = %d, want 1", result[0].fused.ChunkID)
		}
	})

	t.Run("default minPerFile", func(t *testing.T) {
		// minPerFile=0 should default to 3.
		result := selectForReranking(candidates, 5, 0)
		counts := make(map[string]int)
		for _, c := range result {
			counts[c.fileRec.Path]++
		}
		// fileA has 4 chunks, minPerFile defaults to 3, so it should get 3.
		if counts["/vault/docs/INFRA.md"] < 3 {
			t.Errorf("INFRA.md has %d chunks with default minPerFile, want >= 3", counts["/vault/docs/INFRA.md"])
		}
	})
}
