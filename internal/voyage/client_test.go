package voyage

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestEmbed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/v1/embeddings" {
			t.Errorf("expected /v1/embeddings, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("bad auth header: %s", r.Header.Get("Authorization"))
		}

		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != ModelVoyage4Lite {
			t.Errorf("expected model %s, got %s", ModelVoyage4Lite, req.Model)
		}
		if req.InputType != "document" {
			t.Errorf("expected input_type document, got %s", req.InputType)
		}
		if len(req.Input) != 2 {
			t.Fatalf("expected 2 inputs, got %d", len(req.Input))
		}

		resp := embedResponse{
			Data: []embeddingData{
				{Embedding: make([]float32, 1024), Index: 0},
				{Embedding: make([]float32, 1024), Index: 1},
			},
			Model: ModelVoyage4Lite,
			Usage: Usage{TotalTokens: 42},
		}
		resp.Data[0].Embedding[0] = 0.5
		resp.Data[1].Embedding[0] = 0.7

		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}))
	defer srv.Close()

	c := NewClientWithBaseURL("test-key", srv.URL)
	vectors, usage, err := c.Embed(context.Background(), []string{"hello", "world"}, "document")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}

	if len(vectors) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vectors))
	}
	if len(vectors[0]) != 1024 {
		t.Errorf("expected 1024 dims, got %d", len(vectors[0]))
	}
	if vectors[0][0] != 0.5 {
		t.Errorf("expected vectors[0][0]=0.5, got %f", vectors[0][0])
	}
	if vectors[1][0] != 0.7 {
		t.Errorf("expected vectors[1][0]=0.7, got %f", vectors[1][0])
	}
	if usage.TotalTokens != 42 {
		t.Errorf("expected 42 tokens, got %d", usage.TotalTokens)
	}
}

func TestRerank(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/rerank" {
			t.Errorf("expected /v1/rerank, got %s", r.URL.Path)
		}

		var req rerankRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Model != ModelRerank25Lite {
			t.Errorf("expected model %s, got %s", ModelRerank25Lite, req.Model)
		}
		if req.Query != "test query" {
			t.Errorf("expected query 'test query', got %q", req.Query)
		}
		if len(req.Documents) != 3 {
			t.Errorf("expected 3 documents, got %d", len(req.Documents))
		}
		if req.TopK != 2 {
			t.Errorf("expected top_k=2, got %d", req.TopK)
		}

		resp := rerankResponse{
			Data: []RerankResult{
				{Index: 2, RelevanceScore: 0.95},
				{Index: 0, RelevanceScore: 0.72},
			},
			Model: ModelRerank25Lite,
			Usage: Usage{TotalTokens: 100},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}))
	defer srv.Close()

	c := NewClientWithBaseURL("test-key", srv.URL)
	results, usage, err := c.Rerank(context.Background(), "test query", []string{"doc a", "doc b", "doc c"}, 2)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Index != 2 || results[0].RelevanceScore != 0.95 {
		t.Errorf("unexpected first result: %+v", results[0])
	}
	if results[1].Index != 0 || results[1].RelevanceScore != 0.72 {
		t.Errorf("unexpected second result: %+v", results[1])
	}
	if usage.TotalTokens != 100 {
		t.Errorf("expected 100 tokens, got %d", usage.TotalTokens)
	}
}

func TestRetryOn429(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			if _, err := w.Write([]byte(`{"error":"rate limited"}`)); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			return
		}
		resp := embedResponse{
			Data:  []embeddingData{{Embedding: []float32{1.0}, Index: 0}},
			Usage: Usage{TotalTokens: 10},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}))
	defer srv.Close()

	c := NewClientWithBaseURL("test-key", srv.URL)
	vectors, _, err := c.Embed(context.Background(), []string{"hello"}, "document")
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if len(vectors) != 1 {
		t.Errorf("expected 1 vector, got %d", len(vectors))
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("expected 3 attempts, got %d", got)
	}
}

func TestRetryOn500(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusInternalServerError)
			if _, err := w.Write([]byte(`{"error":"server error"}`)); err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			return
		}
		resp := embedResponse{
			Data:  []embeddingData{{Embedding: []float32{1.0}, Index: 0}},
			Usage: Usage{TotalTokens: 10},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}))
	defer srv.Close()

	c := NewClientWithBaseURL("test-key", srv.URL)
	_, _, err := c.Embed(context.Background(), []string{"hello"}, "document")
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if got := attempts.Load(); got != 3 {
		t.Errorf("expected 3 attempts, got %d", got)
	}
}

func TestNoRetryOn400(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		if _, err := w.Write([]byte(`{"error":"bad request"}`)); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}))
	defer srv.Close()

	c := NewClientWithBaseURL("test-key", srv.URL)
	_, _, err := c.Embed(context.Background(), []string{"hello"}, "document")
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("expected 1 attempt (no retry), got %d", got)
	}
}

func TestNoRetryOn401(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
		if _, err := w.Write([]byte(`{"error":"unauthorized"}`)); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}))
	defer srv.Close()

	c := NewClientWithBaseURL("test-key", srv.URL)
	_, _, err := c.Embed(context.Background(), []string{"hello"}, "document")
	if err == nil {
		t.Fatal("expected error on 401")
	}
	if got := attempts.Load(); got != 1 {
		t.Errorf("expected 1 attempt (no retry), got %d", got)
	}
}

func TestContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Simulate slow server.
		time.Sleep(5 * time.Second)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClientWithBaseURL("test-key", srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_, _, err := c.Embed(ctx, []string{"hello"}, "document")
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

func TestEmbedCustomModelAndDimensions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req embedRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if req.Model != "voyage-3" {
			t.Errorf("expected model voyage-3, got %s", req.Model)
		}
		if req.OutputDimension != 512 {
			t.Errorf("expected output_dimension 512, got %d", req.OutputDimension)
		}

		resp := embedResponse{
			Data:  []embeddingData{{Embedding: make([]float32, 512), Index: 0}},
			Model: "voyage-3",
			Usage: Usage{TotalTokens: 20},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}))
	defer srv.Close()

	c := NewClientWithBaseURL("test-key", srv.URL)
	c.EmbedModel = "voyage-3"
	c.EmbedDimensions = 512

	vectors, usage, err := c.Embed(context.Background(), []string{"hello"}, "document")
	if err != nil {
		t.Fatalf("Embed: %v", err)
	}
	if len(vectors) != 1 {
		t.Fatalf("expected 1 vector, got %d", len(vectors))
	}
	if len(vectors[0]) != 512 {
		t.Errorf("expected 512 dims, got %d", len(vectors[0]))
	}
	if usage.TotalTokens != 20 {
		t.Errorf("expected 20 tokens, got %d", usage.TotalTokens)
	}
}

func TestRerankCustomModel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req rerankRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		if req.Model != "rerank-3" {
			t.Errorf("expected model rerank-3, got %s", req.Model)
		}

		resp := rerankResponse{
			Data: []RerankResult{
				{Index: 0, RelevanceScore: 0.9},
			},
			Model: "rerank-3",
			Usage: Usage{TotalTokens: 50},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}))
	defer srv.Close()

	c := NewClientWithBaseURL("test-key", srv.URL)
	c.RerankModel = "rerank-3"

	results, usage, err := c.Rerank(context.Background(), "test query", []string{"doc a"}, 1)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}
	if results[0].RelevanceScore != 0.9 {
		t.Errorf("expected relevance 0.9, got %f", results[0].RelevanceScore)
	}
	if usage.TotalTokens != 50 {
		t.Errorf("expected 50 tokens, got %d", usage.TotalTokens)
	}
}
