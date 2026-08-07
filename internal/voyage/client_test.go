package voyage

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/goleak"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fastRetry returns a retryConfig with minimal delays for testing.
func fastRetry() *retryConfig {
	return &retryConfig{
		maxRetries:     5,
		initialBackoff: 1 * time.Millisecond,
		maxBackoff:     10 * time.Millisecond,
	}
}

func TestUnconfiguredClientNeverSendsRequests(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	for _, key := range []string{"", "  \t\n"} {
		client := NewClientWithBaseURL(key, srv.URL)
		if client.Configured() {
			t.Fatalf("Configured() = true for key %q", key)
		}

		if err := client.Preconnect(context.Background()); !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("Preconnect error = %v, want ErrNotConfigured", err)
		}
		if _, _, err := client.Embed(context.Background(), []string{"text"}, "query"); !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("Embed error = %v, want ErrNotConfigured", err)
		}
		if _, _, err := client.Rerank(context.Background(), "query", []string{"doc"}, 1); !errors.Is(err, ErrNotConfigured) {
			t.Fatalf("Rerank error = %v, want ErrNotConfigured", err)
		}
	}

	if got := requests.Load(); got != 0 {
		t.Fatalf("unconfigured client sent %d HTTP request(s), want 0", got)
	}
}

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
	c.retryOverride = fastRetry()
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

func TestRetryOn429WithRetryAfter(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := attempts.Add(1)
		if n <= 1 {
			w.Header().Set("Retry-After", "1")
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
	// Use fast retry but note Retry-After will override the backoff to 1s.
	// We use a short override so the test doesn't actually wait 1s.
	c.retryOverride = &retryConfig{
		maxRetries:     5,
		initialBackoff: 1 * time.Millisecond,
		maxBackoff:     10 * time.Millisecond,
	}
	vectors, _, err := c.Embed(context.Background(), []string{"hello"}, "document")
	if err != nil {
		t.Fatalf("expected success after retries, got: %v", err)
	}
	if len(vectors) != 1 {
		t.Errorf("expected 1 vector, got %d", len(vectors))
	}
	if got := attempts.Load(); got != 2 {
		t.Errorf("expected 2 attempts, got %d", got)
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
	c.retryOverride = fastRetry()
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
	c.retryOverride = fastRetry()
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

// installCountingDialer swaps the client's transport DialContext for one that
// increments the supplied counter on every fresh dial. The original (already
// counter-wrapped) dialer is preserved beneath this wrapper so DialCount()
// continues to work as well.
func installCountingDialer(c *Client, counter *atomic.Int64, tlsCfg *tls.Config) {
	c.transport.TLSClientConfig = tlsCfg
	base := c.transport.DialContext
	c.transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		counter.Add(1)
		return base(ctx, network, addr)
	}
}

func TestTransportReuse(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{}`)); err != nil {
			t.Errorf("write: %v", err)
		}
	}))
	defer srv.Close()

	c := NewClientWithBaseURL("test-key", srv.URL)
	defer c.transport.CloseIdleConnections()

	var dials atomic.Int64
	installCountingDialer(c, &dials, &tls.Config{InsecureSkipVerify: true})

	for i := 0; i < 2; i++ {
		req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/", nil)
		if err != nil {
			t.Fatalf("new request: %v", err)
		}
		resp, err := c.httpClient.Do(req)
		if err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
		if _, err := io.ReadAll(resp.Body); err != nil {
			t.Fatalf("drain %d: %v", i, err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatalf("close %d: %v", i, err)
		}
	}

	if got := dials.Load(); got != 1 {
		t.Errorf("expected 1 dial across 2 sequential requests, got %d", got)
	}
}

func TestPreconnectIdempotent(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClientWithBaseURL("test-key", srv.URL)
	defer c.transport.CloseIdleConnections()
	c.transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	for i := 0; i < 3; i++ {
		if err := c.Preconnect(context.Background()); err != nil {
			t.Fatalf("Preconnect call %d: %v", i, err)
		}
	}
}

func TestTimeoutHonored(t *testing.T) {
	// Server accepts the connection but never writes a response header.
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer srv.Close()

	c := NewClientWithBaseURL("test-key", srv.URL)
	defer c.transport.CloseIdleConnections()
	c.transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	// Tighten ResponseHeaderTimeout so the test is fast.
	c.transport.ResponseHeaderTimeout = 200 * time.Millisecond
	// Disable the outer client timeout so we are sure ResponseHeaderTimeout fires.
	c.httpClient.Timeout = 0

	start := time.Now()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := c.httpClient.Do(req)
	elapsed := time.Since(start)
	if err == nil {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Logf("close: %v", cerr)
		}
		t.Fatalf("expected timeout error, got status %d", resp.StatusCode)
	}
	// Lower bound: the failure must be the timeout firing, not some
	// other immediate transport error. Without this check the test
	// passes even if the request errors at t=0 with e.g. a TLS handshake
	// failure — which would mask a regression where the timeout knob
	// stops being honored. 100ms is half of ResponseHeaderTimeout (200ms),
	// allowing for scheduling slack while still proving the timeout fired.
	if elapsed < 100*time.Millisecond {
		t.Errorf("error returned too quickly (%v < 100ms): timeout did not fire, err=%v",
			elapsed, err)
	}
	if elapsed > c.transport.ResponseHeaderTimeout+1*time.Second {
		t.Errorf("expected error within %v + 1s grace, got %v: %v",
			c.transport.ResponseHeaderTimeout, elapsed, err)
	}
}

func TestDialCountExposed(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c := NewClientWithBaseURL("test-key", srv.URL)
	defer c.transport.CloseIdleConnections()
	c.transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}

	if got := c.DialCount(); got != 0 {
		t.Fatalf("expected 0 dials before any request, got %d", got)
	}

	if err := c.Preconnect(context.Background()); err != nil {
		t.Fatalf("Preconnect: %v", err)
	}
	first := c.DialCount()
	if first < 1 {
		t.Fatalf("expected DialCount >= 1 after first request, got %d", first)
	}

	// Force a fresh dial by closing idle connections, then issue another request.
	c.transport.CloseIdleConnections()
	if err := c.Preconnect(context.Background()); err != nil {
		t.Fatalf("Preconnect 2: %v", err)
	}
	second := c.DialCount()
	if second <= first {
		t.Errorf("expected DialCount to increment after fresh dial, got first=%d second=%d", first, second)
	}
}
