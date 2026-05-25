package cache

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"time"

	"sift/internal/db"
	"sift/internal/search"
)

// Cache provides a SQLite-backed warm search cache.
type Cache struct {
	db  *db.DB
	ttl time.Duration
	max int
}

// CachedResult mirrors search.SearchResult for cache storage.
type CachedResult struct {
	Results         []search.Result                 `json:"results"`
	TotalBM25       int                             `json:"total_bm25"`
	TotalVec        int                             `json:"total_vec"`
	BM25TimeMs      int64                           `json:"bm25_time_ms"`
	VecTimeMs       int64                           `json:"vec_time_ms"`
	RerankTimeMs    int64                           `json:"rerank_time_ms"`
	Reranked        bool                            `json:"reranked"`
	TotalCandidates int                             `json:"total_candidates"`
	FilteredCount   int                             `json:"filtered_count"`
	DroppedStale    int                             `json:"dropped_stale"`
	Threshold       float64                         `json:"threshold"`
	ContentDedupMap map[int64][]search.DuplicateRef `json:"content_dedup_map,omitempty"`
}

// New creates a new cache.
func New(database *db.DB, ttlSeconds, maxEntries int) *Cache {
	return &Cache{
		db:  database,
		ttl: time.Duration(ttlSeconds) * time.Second,
		max: maxEntries,
	}
}

// Key computes a deterministic cache key from query parameters and result shape.
// Timestamps in the same 5-minute bucket produce the same key.
func Key(query, collection string, sinceUnix int64, pathGlob string, topK int, threshold float64, adaptive bool, extra ...bool) string {
	sinceQuantized := sinceUnix - (sinceUnix % 300)
	data := fmt.Sprintf("%s\x00%s\x00%d\x00%s\x00%d\x00%.6f\x00%t",
		query, collection, sinceQuantized, pathGlob, topK, threshold, adaptive)
	for _, b := range extra {
		data += fmt.Sprintf("\x00%t", b)
	}
	h := sha256.Sum256([]byte(data))
	return fmt.Sprintf("%x", h)
}

// Get retrieves a cached result. Returns nil, false if not found or expired.
func (c *Cache) Get(key string) (*CachedResult, bool) {
	entry, ok := c.db.GetCacheEntry(key)
	if !ok {
		return nil, false
	}

	// Check TTL.
	if time.Since(time.Unix(entry.CreatedAt, 0)) > c.ttl {
		return nil, false
	}

	var result CachedResult
	if err := json.Unmarshal([]byte(entry.ResultsJSON), &result); err != nil {
		return nil, false
	}

	// Restore metadata from cache entry columns.
	result.TotalBM25 = entry.TotalBM25
	result.TotalVec = entry.TotalVec
	result.BM25TimeMs = entry.BM25TimeMs
	result.VecTimeMs = entry.VecTimeMs
	result.RerankTimeMs = entry.RerankTimeMs
	result.Reranked = entry.Reranked

	return &result, true
}

// Put stores a search result in the cache. Performs lazy eviction of expired
// and overflow entries. Query and collection are stored for diagnostics.
func (c *Cache) Put(key string, result *CachedResult, query, collection string) {
	data, err := json.Marshal(result)
	if err != nil {
		return
	}

	now := time.Now().Unix()
	entry := &db.CacheEntry{
		CacheKey:         key,
		Query:            query,
		CollectionFilter: collection,
		ResultsJSON:      string(data),
		ResultCount:      len(result.Results),
		Reranked:         result.Reranked,
		TotalBM25:        result.TotalBM25,
		TotalVec:         result.TotalVec,
		BM25TimeMs:       result.BM25TimeMs,
		VecTimeMs:        result.VecTimeMs,
		RerankTimeMs:     result.RerankTimeMs,
		CreatedAt:        now,
	}

	if err := c.db.PutCacheEntry(key, entry); err != nil {
		return
	}

	// Lazy eviction: remove expired entries.
	expireBefore := now - int64(c.ttl.Seconds())
	_ = c.db.DeleteExpiredCache(expireBefore, 50)

	// Enforce max entries.
	count, err := c.db.CacheEntryCount()
	if err == nil && count > c.max {
		_ = c.db.DeleteExpiredCache(now+1, count-c.max) // delete oldest
	}
}

// Clear removes all cache entries.
func (c *Cache) Clear() {
	_ = c.db.ClearCache()
}
