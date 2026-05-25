package voyage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

const (
	// DefaultBaseURL is the Voyage AI API base URL.
	DefaultBaseURL = "https://api.voyageai.com"

	// ModelVoyage4Lite is the embedding model.
	ModelVoyage4Lite = "voyage-4-lite"

	// ModelRerank25Lite is the reranking model.
	ModelRerank25Lite = "rerank-2.5-lite"

	// DefaultDimensions is the default embedding dimensions.
	DefaultDimensions = 1024

	maxRetries     = 5
	initialBackoff = 1 * time.Second
	maxBackoff     = 30 * time.Second
)

// Usage tracks API token usage from a response.
type Usage struct {
	TotalTokens int `json:"total_tokens"`
}

// RerankResult represents a single reranking result.
type RerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

// retryConfig allows overriding retry parameters for testing.
type retryConfig struct {
	maxRetries     int
	initialBackoff time.Duration
	maxBackoff     time.Duration
}

// Client is an HTTP client for the Voyage AI API.
type Client struct {
	apiKey          string
	baseURL         string
	httpClient      *http.Client
	transport       *http.Transport
	dialCount       atomic.Int64
	logger          *slog.Logger
	EmbedModel      string
	EmbedDimensions int
	EmbedDtype      string
	RerankModel     string
	retryOverride   *retryConfig
}

// TransportConfig tunes the *http.Transport built for the Voyage
// client. Zero-valued fields fall back to the defaults baked in at
// construction time. It mirrors the shape of config.TransportConfig
// without taking a dependency on the config package.
type TransportConfig struct {
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	MaxConnsPerHost       int
	IdleConnTimeout       time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
}

// defaultTransportConfig is the tuning used when callers don't supply
// their own. Matches the historical hard-coded values.
var defaultTransportConfig = TransportConfig{
	MaxIdleConns:          16,
	MaxIdleConnsPerHost:   8,
	MaxConnsPerHost:       16,
	IdleConnTimeout:       5 * time.Minute,
	TLSHandshakeTimeout:   5 * time.Second,
	ResponseHeaderTimeout: 30 * time.Second,
}

// newTransport returns an *http.Transport tuned for the Voyage API: it keeps
// idle TLS connections warm so subsequent requests reuse the same handshake.
// Zero-valued fields in tc fall back to defaultTransportConfig values so
// callers can leave unfamiliar knobs alone.
func newTransport(tc TransportConfig) *http.Transport {
	pick := func(v, def int) int {
		if v > 0 {
			return v
		}
		return def
	}
	pickD := func(v, def time.Duration) time.Duration {
		if v > 0 {
			return v
		}
		return def
	}
	dialer := &net.Dialer{
		Timeout:   5 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          pick(tc.MaxIdleConns, defaultTransportConfig.MaxIdleConns),
		MaxIdleConnsPerHost:   pick(tc.MaxIdleConnsPerHost, defaultTransportConfig.MaxIdleConnsPerHost),
		MaxConnsPerHost:       pick(tc.MaxConnsPerHost, defaultTransportConfig.MaxConnsPerHost),
		IdleConnTimeout:       pickD(tc.IdleConnTimeout, defaultTransportConfig.IdleConnTimeout),
		TLSHandshakeTimeout:   pickD(tc.TLSHandshakeTimeout, defaultTransportConfig.TLSHandshakeTimeout),
		ResponseHeaderTimeout: pickD(tc.ResponseHeaderTimeout, defaultTransportConfig.ResponseHeaderTimeout),
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// newClient builds a Client with a shared transport and dial counter.
func newClient(apiKey, baseURL string, tc TransportConfig) *Client {
	logger := slog.Default().With(slog.String("component", "voyage"))
	c := &Client{
		apiKey:          apiKey,
		baseURL:         baseURL,
		transport:       newTransport(tc),
		logger:          logger,
		EmbedModel:      ModelVoyage4Lite,
		EmbedDimensions: DefaultDimensions,
		RerankModel:     ModelRerank25Lite,
	}

	// Wrap DialContext to count fresh dials; preserves the dialer's timeouts.
	baseDial := c.transport.DialContext
	c.transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := baseDial(ctx, network, addr)
		if err != nil {
			c.logger.Debug("dial failed",
				slog.String("network", network),
				slog.String("addr", addr),
				slog.String("err", err.Error()),
			)
			return nil, err
		}
		n := c.dialCount.Add(1)
		c.logger.Debug("dial",
			slog.String("network", network),
			slog.String("addr", addr),
			slog.Int64("count", n),
		)
		return conn, nil
	}

	c.httpClient = &http.Client{
		Timeout:   30 * time.Second,
		Transport: c.transport,
	}
	return c
}

// NewClient creates a new Voyage API client with default transport tuning.
// Prefer NewClientWithTransport when you have a configured TransportConfig.
func NewClient(apiKey string) *Client {
	return newClient(apiKey, DefaultBaseURL, defaultTransportConfig)
}

// NewClientWithTransport creates a new Voyage API client wired to the
// supplied transport tuning. Zero-valued fields in tc fall back to
// package defaults.
func NewClientWithTransport(apiKey, baseURL string, tc TransportConfig) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return newClient(apiKey, baseURL, tc)
}

// NewClientWithBaseURL creates a new Voyage API client with a custom base URL (for testing).
func NewClientWithBaseURL(apiKey, baseURL string) *Client {
	return newClient(apiKey, baseURL, defaultTransportConfig)
}

// DialCount returns the number of fresh TCP/TLS dials initiated through this
// client's transport. Useful for verifying connection reuse.
func (c *Client) DialCount() int64 {
	return c.dialCount.Load()
}

// Preconnect issues a HEAD request against the client's base URL to warm the
// TLS handshake and populate the idle-connection pool. It is safe to call
// many times; each call drains and closes the response body.
func (c *Client) Preconnect(ctx context.Context) error {
	url := c.baseURL + "/"
	c.logger.Info("preconnect start", slog.String("url", url))

	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		c.logger.Error("preconnect build request failed",
			slog.String("url", url),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("preconnect build request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		c.logger.Error("preconnect request failed",
			slog.String("url", url),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("preconnect: %w", err)
	}
	// Drain and close so the connection returns to the idle pool.
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		closeErr := resp.Body.Close()
		c.logger.Error("preconnect drain failed",
			slog.String("url", url),
			slog.String("err", err.Error()),
		)
		if closeErr != nil {
			return fmt.Errorf("preconnect drain: %w (close: %v)", err, closeErr)
		}
		return fmt.Errorf("preconnect drain: %w", err)
	}
	if err := resp.Body.Close(); err != nil {
		c.logger.Error("preconnect close failed",
			slog.String("url", url),
			slog.String("err", err.Error()),
		)
		return fmt.Errorf("preconnect close: %w", err)
	}

	c.logger.Info("preconnect finish",
		slog.String("url", url),
		slog.Int("status", resp.StatusCode),
		slog.Int64("dial_count", c.dialCount.Load()),
	)
	return nil
}

// embedRequest is the JSON body for /v1/embeddings.
type embedRequest struct {
	Input           []string `json:"input"`
	Model           string   `json:"model"`
	InputType       string   `json:"input_type,omitempty"`
	OutputDimension int      `json:"output_dimension,omitempty"`
	OutputDtype     string   `json:"output_dtype,omitempty"`
}

// embedResponse is the JSON response from /v1/embeddings.
type embedResponse struct {
	Data  []embeddingData `json:"data"`
	Model string          `json:"model"`
	Usage Usage           `json:"usage"`
}

type embeddingData struct {
	Embedding []float32 `json:"embedding"`
	Index     int       `json:"index"`
}

// rerankRequest is the JSON body for /v1/rerank.
type rerankRequest struct {
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	Model     string   `json:"model"`
	TopK      int      `json:"top_k,omitempty"`
}

// rerankResponse is the JSON response from /v1/rerank.
type rerankResponse struct {
	Data  []RerankResult `json:"data"`
	Model string         `json:"model"`
	Usage Usage          `json:"usage"`
}

// Embed calls the Voyage embeddings API.
// inputType should be "query" for search queries or "document" for indexing.
func (c *Client) Embed(ctx context.Context, texts []string, inputType string) ([][]float32, Usage, error) {
	reqBody := embedRequest{
		Input:           texts,
		Model:           c.EmbedModel,
		InputType:       inputType,
		OutputDimension: c.EmbedDimensions,
		OutputDtype:     c.EmbedDtype,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, Usage{}, fmt.Errorf("marshal embed request: %w", err)
	}

	respBody, err := c.doWithRetry(ctx, "/v1/embeddings", body)
	if err != nil {
		return nil, Usage{}, fmt.Errorf("embed: %w", err)
	}

	var resp embedResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, Usage{}, fmt.Errorf("unmarshal embed response: %w", err)
	}

	vectors := make([][]float32, len(resp.Data))
	for _, d := range resp.Data {
		vectors[d.Index] = d.Embedding
	}

	return vectors, resp.Usage, nil
}

// Rerank calls the Voyage reranking API.
func (c *Client) Rerank(ctx context.Context, query string, docs []string, topK int) ([]RerankResult, Usage, error) {
	reqBody := rerankRequest{
		Query:     query,
		Documents: docs,
		Model:     c.RerankModel,
		TopK:      topK,
	}

	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, Usage{}, fmt.Errorf("marshal rerank request: %w", err)
	}

	respBody, err := c.doWithRetry(ctx, "/v1/rerank", body)
	if err != nil {
		return nil, Usage{}, fmt.Errorf("rerank: %w", err)
	}

	var resp rerankResponse
	if err := json.Unmarshal(respBody, &resp); err != nil {
		return nil, Usage{}, fmt.Errorf("unmarshal rerank response: %w", err)
	}

	return resp.Data, resp.Usage, nil
}

// SetTimeout updates the HTTP client timeout.
func (c *Client) SetTimeout(d time.Duration) {
	c.httpClient.Timeout = d
}

// doWithRetry sends an HTTP POST with exponential backoff retry for 429/500+ errors.
func (c *Client) doWithRetry(ctx context.Context, path string, body []byte) ([]byte, error) {
	retries := maxRetries
	backoff := initialBackoff
	maxBO := maxBackoff
	if c.retryOverride != nil {
		retries = c.retryOverride.maxRetries
		backoff = c.retryOverride.initialBackoff
		maxBO = c.retryOverride.maxBackoff
	}
	curBackoff := backoff

	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			// Apply jitter: ±25%
			jittered := applyJitter(curBackoff)
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(jittered):
			}
			curBackoff *= 2
			if curBackoff > maxBO {
				curBackoff = maxBO
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.apiKey)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			if attempt == retries {
				return nil, fmt.Errorf("http request after %d retries: %w", retries, err)
			}
			c.logger.Warn("retry after network error",
				slog.String("op", "doWithRetry"),
				slog.String("path", path),
				slog.Int("attempt", attempt+1),
				slog.Int("max_attempts", retries+1),
				slog.Duration("delay", curBackoff),
				slog.String("err", err.Error()),
			)
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			return nil, fmt.Errorf("read response: %w", readErr)
		}

		if resp.StatusCode == http.StatusOK {
			return respBody, nil
		}

		// Client errors (400, 401, 403, 404) should not be retried.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return nil, fmt.Errorf("api error %d: %s", resp.StatusCode, string(respBody))
		}

		// 429: respect Retry-After header if present.
		if resp.StatusCode == http.StatusTooManyRequests {
			if ra := resp.Header.Get("Retry-After"); ra != "" {
				if secs, parseErr := strconv.Atoi(ra); parseErr == nil && secs > 0 {
					curBackoff = time.Duration(secs) * time.Second
				}
			}
		}

		if attempt == retries {
			return nil, fmt.Errorf("api error %d after %d retries: %s", resp.StatusCode, retries, string(respBody))
		}

		c.logger.Warn("retry after http error",
			slog.String("op", "doWithRetry"),
			slog.String("path", path),
			slog.Int("status", resp.StatusCode),
			slog.Int("attempt", attempt+1),
			slog.Int("max_attempts", retries+1),
			slog.Duration("delay", curBackoff),
		)
	}

	return nil, fmt.Errorf("exhausted retries")
}

// applyJitter adds ±25% random variation to a duration.
func applyJitter(d time.Duration) time.Duration {
	jitter := time.Duration(rand.Int64N(int64(d)/2)) - d/4
	return d + jitter
}
