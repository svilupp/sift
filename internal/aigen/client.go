package aigen

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// DefaultBaseURL is the OpenAI-compat DeepInfra endpoint.
const DefaultBaseURL = "https://api.deepinfra.com"

// DefaultModel is the LT-A winner. Keep `deepseek-ai/DeepSeek-V4-Flash`
// as the production default — strict-mode JSON output works cleanly.
const DefaultModel = "deepseek-ai/DeepSeek-V4-Flash"

// DefaultTimeout is the per-call HTTP timeout (PROPOSAL2 §"Concurrency").
const DefaultTimeout = 180 * time.Second

const (
	maxRetries     = 3
	initialBackoff = 2 * time.Second
	secondBackoff  = 8 * time.Second
	thirdBackoff   = 32 * time.Second
)

// retrySchedule lists per-attempt sleep before retrying.
// Index = attempt-1 (after attempt 1 failed → sleep[0] before attempt 2).
var retrySchedule = []time.Duration{initialBackoff, secondBackoff, thirdBackoff}

// USDPerInToken / USDPerOutToken: DeepInfra DeepSeek V4 pricing,
// $0.14 / 1M input, $0.28 / 1M output.
const (
	USDPerInToken  = 0.14 / 1_000_000.0
	USDPerOutToken = 0.28 / 1_000_000.0
)

// CostUSD estimates the dollar cost from token counts.
func CostUSD(tokensIn, tokensOut int) float64 {
	return float64(tokensIn)*USDPerInToken + float64(tokensOut)*USDPerOutToken
}

// TransportConfig mirrors voyage.TransportConfig so callers can share
// tuning without a cross-package dep.
type TransportConfig struct {
	MaxIdleConns          int
	MaxIdleConnsPerHost   int
	MaxConnsPerHost       int
	IdleConnTimeout       time.Duration
	TLSHandshakeTimeout   time.Duration
	ResponseHeaderTimeout time.Duration
}

var defaultTransport = TransportConfig{
	MaxIdleConns:          16,
	MaxIdleConnsPerHost:   8,
	MaxConnsPerHost:       16,
	IdleConnTimeout:       5 * time.Minute,
	TLSHandshakeTimeout:   5 * time.Second,
	ResponseHeaderTimeout: DefaultTimeout,
}

// Client is the DeepInfra OpenAI-compat HTTP client used by aigen.
type Client struct {
	apiKey     string
	baseURL    string
	model      string
	httpClient *http.Client
	logger     *slog.Logger

	// strictDisabled is set when a 400 from strict-mode pushed us back
	// to instruction mode; subsequent calls skip response_format.
	strictDisabled bool

	// retryOverride lets tests inject a tighter schedule.
	retryOverride []time.Duration
}

// NewClient builds a Client with the supplied API key. baseURL falls
// back to DefaultBaseURL.
func NewClient(apiKey, baseURL string) *Client {
	return NewClientWithTransport(apiKey, baseURL, defaultTransport)
}

// NewClientWithTransport allows callers to share an *http.Transport
// across the aigen and voyage clients.
func NewClientWithTransport(apiKey, baseURL string, tc TransportConfig) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	logger := slog.Default().With(slog.String("component", "aigen"))
	c := &Client{
		apiKey:  apiKey,
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   DefaultModel,
		logger:  logger,
		httpClient: &http.Client{
			Timeout:   DefaultTimeout,
			Transport: newTransport(tc),
		},
	}
	return c
}

func newTransport(tc TransportConfig) *http.Transport {
	pickI := func(v, def int) int {
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
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          pickI(tc.MaxIdleConns, defaultTransport.MaxIdleConns),
		MaxIdleConnsPerHost:   pickI(tc.MaxIdleConnsPerHost, defaultTransport.MaxIdleConnsPerHost),
		MaxConnsPerHost:       pickI(tc.MaxConnsPerHost, defaultTransport.MaxConnsPerHost),
		IdleConnTimeout:       pickD(tc.IdleConnTimeout, defaultTransport.IdleConnTimeout),
		TLSHandshakeTimeout:   pickD(tc.TLSHandshakeTimeout, defaultTransport.TLSHandshakeTimeout),
		ResponseHeaderTimeout: pickD(tc.ResponseHeaderTimeout, defaultTransport.ResponseHeaderTimeout),
		ExpectContinueTimeout: 1 * time.Second,
	}
}

// SetModel overrides the chat-completions model.
func (c *Client) SetModel(m string) { c.model = m }

// SetTimeout updates the underlying HTTP client timeout.
func (c *Client) SetTimeout(d time.Duration) { c.httpClient.Timeout = d }

// Model returns the model in use (for logs / stats).
func (c *Client) Model() string { return c.model }

// chatRequest is the OpenAI-compatible request body.
type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	Temperature    float64        `json:"temperature,omitempty"`
	ResponseFormat map[string]any `json:"response_format,omitempty"`
	MaxTokens      int            `json:"max_tokens,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Usage   chatUsage    `json:"usage"`
	Error   *chatError   `json:"error,omitempty"`
}

type chatChoice struct {
	Message chatMessage `json:"message"`
}

type chatUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	CachedTokens     int `json:"cached_tokens,omitempty"`
}

type chatError struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

// CallRequest packages one chat completion request.
type CallRequest struct {
	System string
	User   string
	// Strict requests strict-mode JSON-schema enforcement when true.
	Strict bool
	// MaxTokens optional; default 0 = server default.
	MaxTokens int
}

// CallResponse holds the parsed model output and usage telemetry.
type CallResponse struct {
	Content string
	Stats   CallStats
	// UsedStrict reports whether the call actually went out with
	// response_format. Useful for telemetry and fallback bookkeeping.
	UsedStrict bool
}

// Call sends one chat completion request, retrying on retryable errors.
// It does NOT parse the response body — callers parse via
// ParseFolderResponse. Strict mode is disabled for the lifetime of the
// client after a 400 that mentions schema/response_format; subsequent
// calls auto-fall to instruction mode.
func (c *Client) Call(ctx context.Context, req CallRequest) (*CallResponse, error) {
	if c.apiKey == "" {
		return nil, fmt.Errorf("aigen: missing DeepInfra API key")
	}
	body := chatRequest{
		Model:       c.model,
		Temperature: 0.1,
		MaxTokens:   req.MaxTokens,
		Messages: []chatMessage{
			{Role: "system", Content: req.System},
			{Role: "user", Content: req.User},
		},
	}
	useStrict := req.Strict && !c.strictDisabled
	if useStrict {
		body.ResponseFormat = FolderResponseFormat()
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("aigen marshal: %w", err)
	}

	start := time.Now()
	respBody, status, attempts, err := c.doWithRetry(ctx, "/v1/openai/chat/completions", raw)
	latencyMs := time.Since(start).Milliseconds()
	stats := CallStats{LatencyMs: latencyMs, HTTPStatus: status, Attempts: attempts}
	if err != nil {
		// Strict mode disabling on 400 mentioning schema/response_format.
		if status == http.StatusBadRequest && useStrict &&
			(bytes.Contains(respBody, []byte("response_format")) ||
				bytes.Contains(respBody, []byte("json_schema"))) {
			c.strictDisabled = true
		}
		return &CallResponse{Stats: stats, UsedStrict: useStrict},
			fmt.Errorf("aigen call: %w", err)
	}

	var parsed chatResponse
	if perr := json.Unmarshal(respBody, &parsed); perr != nil {
		return &CallResponse{Stats: stats, UsedStrict: useStrict},
			fmt.Errorf("aigen unmarshal: %w", perr)
	}
	if parsed.Error != nil && parsed.Error.Message != "" {
		return &CallResponse{Stats: stats, UsedStrict: useStrict},
			fmt.Errorf("aigen api error: %s", parsed.Error.Message)
	}
	if len(parsed.Choices) == 0 {
		return &CallResponse{Stats: stats, UsedStrict: useStrict},
			fmt.Errorf("aigen: empty choices")
	}
	stats.TokensIn = parsed.Usage.PromptTokens
	stats.TokensOut = parsed.Usage.CompletionTokens
	stats.CachedTokens = parsed.Usage.CachedTokens

	return &CallResponse{
		Content:    parsed.Choices[0].Message.Content,
		Stats:      stats,
		UsedStrict: useStrict,
	}, nil
}

// doWithRetry performs the HTTP POST with bounded retry. Returns the
// response body, last status code, number of attempts made, and an
// error if every attempt failed.
func (c *Client) doWithRetry(ctx context.Context, path string, body []byte) ([]byte, int, int, error) {
	schedule := retrySchedule
	if c.retryOverride != nil {
		schedule = c.retryOverride
	}
	maxAttempts := len(schedule) + 1

	var lastBody []byte
	var lastStatus int
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if attempt > 1 {
			delay := jitter(schedule[attempt-2])
			select {
			case <-ctx.Done():
				return lastBody, lastStatus, attempt - 1, ctx.Err()
			case <-time.After(delay):
			}
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, 0, attempt, fmt.Errorf("build request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.apiKey)

		resp, err := c.httpClient.Do(req)
		if err != nil {
			lastBody = nil
			lastStatus = 0
			c.logger.Debug("aigen retryable network error",
				slog.Int("attempt", attempt),
				slog.String("err", err.Error()))
			if attempt == maxAttempts {
				return nil, 0, attempt, fmt.Errorf("network: %w", err)
			}
			continue
		}

		respBody, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			return nil, resp.StatusCode, attempt, fmt.Errorf("read response: %w", readErr)
		}
		lastBody = respBody
		lastStatus = resp.StatusCode

		if resp.StatusCode == http.StatusOK {
			return respBody, resp.StatusCode, attempt, nil
		}

		// Non-retryable 4xx (other than 429).
		if resp.StatusCode >= 400 && resp.StatusCode < 500 &&
			resp.StatusCode != http.StatusTooManyRequests {
			return respBody, resp.StatusCode, attempt,
				fmt.Errorf("api %d: %s", resp.StatusCode, truncate(respBody, 200))
		}

		c.logger.Debug("aigen retryable http error",
			slog.Int("attempt", attempt),
			slog.Int("status", resp.StatusCode))
		if attempt == maxAttempts {
			return respBody, resp.StatusCode, attempt,
				fmt.Errorf("api %d after %d attempts: %s",
					resp.StatusCode, attempt, truncate(respBody, 200))
		}
	}
	return lastBody, lastStatus, maxAttempts, fmt.Errorf("exhausted retries")
}

// jitter applies ±20% to a duration (PROPOSAL2 §"Retry").
func jitter(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	span := int64(d) / 5 // 20%
	if span <= 0 {
		return d
	}
	delta := rand.Int64N(span*2+1) - span
	return d + time.Duration(delta)
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

// LoadAPIKey returns the DeepInfra API key resolved from the environment
// or by walking up `.env` files starting at folder. The walk stops at
// repoRoot (or HOME if repoRoot is empty); it never traverses past the
// user's home directory.
func LoadAPIKey(folder, repoRoot string) string {
	return LoadAPIKeyWithFallback(folder, repoRoot, "")
}

// LoadAPIKeyWithFallback resolves the DeepInfra API key in this order:
//  1. env var DEEPINFRA_API_KEY
//  2. .env files walked up from folder, stopping at repoRoot or HOME
//  3. configFallback (typically cfg.API.DeepInfraAPIKey from
//     ~/.sift/config.toml)
//
// Returns "" if none are set. Callers that hold a CLI flag value should
// prefer it over this function entirely.
func LoadAPIKeyWithFallback(folder, repoRoot, configFallback string) string {
	if v := strings.TrimSpace(os.Getenv("DEEPINFRA_API_KEY")); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	stopAt := strings.TrimSpace(repoRoot)

	dir, _ := filepath.Abs(folder)
	for i := 0; i < 16 && dir != ""; i++ {
		if v := readEnvFromFile(filepath.Join(dir, ".env"), "DEEPINFRA_API_KEY"); v != "" {
			return v
		}
		if dir == "/" || dir == filepath.VolumeName(dir)+string(filepath.Separator) {
			break
		}
		if home != "" && dir == home {
			break
		}
		if stopAt != "" && dir == stopAt {
			// Final check on this dir done above; stop now.
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	if v := strings.TrimSpace(configFallback); v != "" {
		return v
	}
	return ""
}

// readEnvFromFile parses a `.env`-style file for the named key. Returns
// the trimmed value or "" on any parse / IO error.
func readEnvFromFile(path, key string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(line[:eq])
		v := strings.TrimSpace(line[eq+1:])
		if k != key {
			continue
		}
		// Strip surrounding quotes.
		if len(v) >= 2 {
			if (v[0] == '"' && v[len(v)-1] == '"') || (v[0] == '\'' && v[len(v)-1] == '\'') {
				v = v[1 : len(v)-1]
			}
		}
		return v
	}
	return ""
}
