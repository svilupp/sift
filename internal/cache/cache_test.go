package cache

import (
	"path/filepath"
	"sync"
	"testing"

	"sift/internal/db"
	"sift/internal/search"
)

func setupTestDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := database.Init(); err != nil {
		t.Fatalf("init db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

func TestCacheRoundtrip(t *testing.T) {
	database := setupTestDB(t)
	c := New(database, 300, 100)

	key := Key("test query", "vault", 0, "")
	cr := &CachedResult{
		Results: []search.Result{
			{ChunkID: 1, FilePath: "/a.md", FinalScore: 0.95},
			{ChunkID: 2, FilePath: "/b.md", FinalScore: 0.80},
		},
		TotalBM25:    5,
		TotalVec:     3,
		BM25TimeMs:   10,
		VecTimeMs:    20,
		RerankTimeMs: 50,
		Reranked:     true,
	}

	c.Put(key, cr, "test query", "vault")

	got, ok := c.Get(key)
	if !ok {
		t.Fatal("expected cache hit")
	}
	if len(got.Results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(got.Results))
	}
	if got.Results[0].ChunkID != 1 {
		t.Errorf("expected ChunkID 1, got %d", got.Results[0].ChunkID)
	}
	if got.TotalBM25 != 5 {
		t.Errorf("expected TotalBM25=5, got %d", got.TotalBM25)
	}
	if !got.Reranked {
		t.Error("expected Reranked=true")
	}
}

func TestCacheRoundtripEmpty(t *testing.T) {
	database := setupTestDB(t)
	c := New(database, 300, 100)

	key := Key("empty", "", 0, "")
	cr := &CachedResult{}

	c.Put(key, cr, "empty", "")

	got, ok := c.Get(key)
	if !ok {
		t.Fatal("expected cache hit for empty result")
	}
	if len(got.Results) != 0 {
		t.Errorf("expected 0 results, got %d", len(got.Results))
	}
}

func TestCacheMiss(t *testing.T) {
	database := setupTestDB(t)
	c := New(database, 300, 100)

	_, ok := c.Get("nonexistent")
	if ok {
		t.Error("expected cache miss")
	}
}

func TestCacheExpiry(t *testing.T) {
	database := setupTestDB(t)
	c := New(database, 1, 100) // 1 second TTL

	key := Key("test", "", 0, "")
	cr := &CachedResult{Results: []search.Result{{ChunkID: 1}}}
	c.Put(key, cr, "test", "")

	// Manually set created_at to past.
	if err := database.PutCacheEntry(key, &db.CacheEntry{
		CacheKey:    key,
		ResultsJSON: `{"results":[{"ChunkID":1}]}`,
		ResultCount: 1,
		CreatedAt:   1, // very old
	}); err != nil {
		t.Fatalf("put cache entry: %v", err)
	}

	_, ok := c.Get(key)
	if ok {
		t.Error("expected cache miss for expired entry")
	}
}

func TestCacheClear(t *testing.T) {
	database := setupTestDB(t)
	c := New(database, 300, 100)

	for i := range 3 {
		key := Key("query", "", int64(i*300), "")
		c.Put(key, &CachedResult{Results: []search.Result{{ChunkID: int64(i)}}}, "query", "")
	}

	c.Clear()

	for i := range 3 {
		key := Key("query", "", int64(i*300), "")
		_, ok := c.Get(key)
		if ok {
			t.Errorf("expected cache miss after clear for key %d", i)
		}
	}
}

func TestCacheMaxEntries(t *testing.T) {
	database := setupTestDB(t)
	c := New(database, 300, 5)

	for i := range 6 {
		key := Key("query", "", int64(i*300), "")
		c.Put(key, &CachedResult{Results: []search.Result{{ChunkID: int64(i)}}}, "query", "")
	}

	count, err := database.CacheEntryCount()
	if err != nil {
		t.Fatal(err)
	}
	if count > 5 {
		t.Errorf("expected at most 5 entries, got %d", count)
	}
}

func TestCacheKeyDeterminism(t *testing.T) {
	// Same inputs → same key.
	k1 := Key("query", "col", 1000, "")
	k2 := Key("query", "col", 1000, "")
	if k1 != k2 {
		t.Errorf("same inputs should produce same key: %s != %s", k1, k2)
	}

	// Different query → different key.
	k3 := Key("other", "col", 1000, "")
	if k1 == k3 {
		t.Error("different queries should produce different keys")
	}

	// Different collection → different key.
	k4 := Key("query", "other", 1000, "")
	if k1 == k4 {
		t.Error("different collections should produce different keys")
	}

	// Timestamps in same 5min bucket → same key.
	k5 := Key("query", "col", 600, "")
	k6 := Key("query", "col", 899, "")
	if k5 != k6 {
		t.Errorf("timestamps in same 5min bucket should produce same key: %s != %s", k5, k6)
	}

	// Timestamps in different 5min buckets → different keys.
	k7 := Key("query", "col", 600, "")
	k8 := Key("query", "col", 900, "")
	if k7 == k8 {
		t.Error("timestamps in different 5min buckets should produce different keys")
	}

	// Zero since (no --since case).
	k9 := Key("query", "col", 0, "")
	k10 := Key("query", "col", 0, "")
	if k9 != k10 {
		t.Error("zero since should be deterministic")
	}
}

func TestCacheConcurrent(t *testing.T) {
	database := setupTestDB(t)
	c := New(database, 300, 100)

	var wg sync.WaitGroup
	for i := range 10 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := Key("query", "", int64(i*300), "")
			c.Put(key, &CachedResult{Results: []search.Result{{ChunkID: int64(i)}}}, "query", "")
			c.Get(key)
		}(i)
	}
	wg.Wait()

	// If we got here without deadlock or panic, test passes.
	// Verify at least some entries exist.
	count, err := database.CacheEntryCount()
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Error("expected some cache entries after concurrent operations")
	}
}

func TestCacheKeyNoSince(t *testing.T) {
	// Ensure --since=0 produces a stable key.
	k := Key("test", "vault", 0, "")
	if k == "" {
		t.Error("expected non-empty key")
	}
}

func TestCacheKeyPathGlob(t *testing.T) {
	// Same query without path glob.
	k1 := Key("query", "col", 0, "")
	k2 := Key("query", "col", 0, "")
	if k1 != k2 {
		t.Error("same inputs (no path) should produce same key")
	}

	// Different path glob → different key.
	k3 := Key("query", "col", 0, "*/work/*")
	if k1 == k3 {
		t.Error("different path globs should produce different keys")
	}

	// Same path glob → same key.
	k4 := Key("query", "col", 0, "*/work/*")
	if k3 != k4 {
		t.Errorf("same path glob should produce same key: %s != %s", k3, k4)
	}

	// Different path globs → different keys.
	k5 := Key("query", "col", 0, "*/personal/*")
	if k3 == k5 {
		t.Error("different path patterns should produce different keys")
	}
}
