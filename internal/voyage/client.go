package voyage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	maxRetries     = 3
	initialBackoff = 100 * time.Millisecond
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

// Client is an HTTP client for the Voyage AI API.
type Client struct {
	apiKey          string
	baseURL         string
	httpClient      *http.Client
	EmbedModel      string
	EmbedDimensions int
	EmbedDtype      string
	RerankModel     string
}

// NewClient creates a new Voyage API client.
func NewClient(apiKey string) *Client {
	return &Client{
		apiKey:          apiKey,
		baseURL:         DefaultBaseURL,
		httpClient:      &http.Client{Timeout: 30 * time.Second},
		EmbedModel:      ModelVoyage4Lite,
		EmbedDimensions: DefaultDimensions,
		RerankModel:     ModelRerank25Lite,
	}
}

// NewClientWithBaseURL creates a new Voyage API client with a custom base URL (for testing).
func NewClientWithBaseURL(apiKey, baseURL string) *Client {
	return &Client{
		apiKey:          apiKey,
		baseURL:         baseURL,
		httpClient:      &http.Client{Timeout: 30 * time.Second},
		EmbedModel:      ModelVoyage4Lite,
		EmbedDimensions: DefaultDimensions,
		RerankModel:     ModelRerank25Lite,
	}
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
	backoff := initialBackoff

	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(backoff):
			}
			backoff *= 2
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.apiKey)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			// Network errors on the last attempt are fatal.
			if attempt == maxRetries {
				return nil, fmt.Errorf("http request: %w", err)
			}
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

		// Client errors (400, 401) should not be retried.
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return nil, fmt.Errorf("api error %d: %s", resp.StatusCode, string(respBody))
		}

		// 429 or 500+ — retry if we have attempts left.
		if attempt == maxRetries {
			return nil, fmt.Errorf("api error %d after %d retries: %s", resp.StatusCode, maxRetries, string(respBody))
		}
	}

	return nil, fmt.Errorf("exhausted retries")
}
