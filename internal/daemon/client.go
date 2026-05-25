package daemon

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"sift/internal/config"
)

// Client is an HTTP-over-Unix-socket client for the SIFT daemon.
//
// The HTTP host name in request URLs is irrelevant because the transport
// always dials the configured Unix socket; we use "http://unix" by
// convention so URLs are well-formed and easy to read in logs.
type Client struct {
	httpClient *http.Client
	socketPath string
	logger     *slog.Logger
}

// baseURL is a placeholder host. The HTTP transport ignores the host
// because DialContext is hardcoded to dial the Unix socket.
const baseURL = "http://unix"

// NewClient returns a Client that talks to the daemon at socketPath.
// If socketPath is empty, config.SocketPath() is consulted.
func NewClient(socketPath string) *Client {
	if socketPath == "" {
		// Best-effort default. If config resolution fails, we leave the
		// path empty; the first dial will surface a clear error.
		if p, err := config.SocketPath(); err == nil {
			socketPath = p
		}
	}

	dialer := &net.Dialer{}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, "unix", socketPath)
		},
		// Idle pool tuning: a short MaxIdleConns is fine for a single
		// daemon endpoint.
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
		ResponseHeaderTimeout: 0, // streaming endpoints (refresh) need none
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &Client{
		httpClient: &http.Client{Transport: transport},
		socketPath: socketPath,
		logger: slog.With(
			"component", "daemon-client",
			"socket", socketPath,
		),
	}
}

// TryDial creates a new Client and verifies the daemon is reachable by
// issuing a single GET /health within timeout. The returned error is
// wrapped with %w so callers can use errors.Is against os.ErrNotExist
// or syscall.ECONNREFUSED.
func TryDial(timeout time.Duration) (*Client, error) {
	c := NewClient("")

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if _, err := c.Health(ctx); err != nil {
		c.Close()
		return nil, fmt.Errorf("daemon dial: %w", err)
	}
	return c, nil
}

// DialUntilReady polls /health with exponential backoff (5ms, 10ms, 20ms,
// 40ms, capped at 50ms) until the daemon answers 200 OK or total elapsed
// exceeds timeout. On timeout it returns the most recent error.
func DialUntilReady(timeout time.Duration) (*Client, error) {
	c := NewClient("")

	deadline := time.Now().Add(timeout)
	backoff := 5 * time.Millisecond
	const maxBackoff = 50 * time.Millisecond

	var lastErr error
	for {
		// Each attempt gets the remaining time as deadline (clamped to
		// at least 50ms so a single attempt has a chance to complete).
		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		attemptTimeout := remaining
		if attemptTimeout < 50*time.Millisecond {
			attemptTimeout = 50 * time.Millisecond
		}

		ctx, cancel := context.WithTimeout(context.Background(), attemptTimeout)
		_, err := c.Health(ctx)
		cancel()
		if err == nil {
			return c, nil
		}
		lastErr = err

		if time.Now().Add(backoff).After(deadline) {
			// No time for another attempt after the sleep — exit now.
			break
		}
		time.Sleep(backoff)
		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}

	c.Close()
	if lastErr == nil {
		lastErr = errors.New("timeout")
	}
	return nil, fmt.Errorf("daemon dial: timeout after %s: %w", timeout, lastErr)
}

// Close releases idle HTTP connections held by the client.
func (c *Client) Close() error {
	c.httpClient.CloseIdleConnections()
	return nil
}

// SocketPath returns the Unix socket path this client dials.
func (c *Client) SocketPath() string {
	return c.socketPath
}

// Health issues GET /health.
func (c *Client) Health(ctx context.Context) (*HealthResponse, error) {
	const op = "Health"
	logger := c.logger.With("op", op)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"/health", nil)
	if err != nil {
		logger.Warn("build request failed", "err", err)
		return nil, fmt.Errorf("%s: build request: %w", op, err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		logger.Warn("request failed", "err", err)
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errResp := decodeErrorResponse(resp.Body)
		logger.Warn("non-200 response",
			"status", resp.StatusCode,
			"code", errResp.Code,
			"message", errResp.Message,
		)
		return nil, fmt.Errorf("health: %s: %s", errResp.Code, errResp.Message)
	}

	var out HealthResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		logger.Warn("decode failed", "err", err)
		return nil, fmt.Errorf("%s: decode: %w", op, err)
	}
	logger.Debug("ok", "uptime_s", out.UptimeS, "pid", out.PID)
	return &out, nil
}

// Search issues POST /search.
func (c *Client) Search(ctx context.Context, req SearchRequest) (*SearchResponse, error) {
	const op = "Search"
	logger := c.logger.With("op", op)

	body, err := json.Marshal(req)
	if err != nil {
		logger.Warn("encode request failed", "err", err)
		return nil, fmt.Errorf("%s: encode: %w", op, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/search", bytes.NewReader(body))
	if err != nil {
		logger.Warn("build request failed", "err", err)
		return nil, fmt.Errorf("%s: build request: %w", op, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		logger.Warn("request failed", "err", err)
		return nil, fmt.Errorf("%s: %w", op, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errResp := decodeErrorResponse(resp.Body)
		logger.Warn("non-200 response",
			"status", resp.StatusCode,
			"code", errResp.Code,
			"message", errResp.Message,
		)
		return nil, fmt.Errorf("search: %s: %s", errResp.Code, errResp.Message)
	}

	var out SearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		logger.Warn("decode failed", "err", err)
		return nil, fmt.Errorf("%s: decode: %w", op, err)
	}
	logger.Debug("ok", "result_count", out.Meta.ResultCount, "search_id", out.SearchID)
	return &out, nil
}

// Refresh issues POST /refresh and streams the NDJSON response body
// line-by-line to w. Each line is written as soon as it is read so the
// caller can present live progress. The function returns when the
// response body ends or after a ProgressEvent with Phase == "done".
//
// On non-2xx response the body is decoded as ErrorResponse and a
// wrapped error is returned without writing anything to w.
func (c *Client) Refresh(ctx context.Context, req RefreshRequest, w io.Writer) error {
	const op = "Refresh"
	logger := c.logger.With("op", op)

	body, err := json.Marshal(req)
	if err != nil {
		logger.Warn("encode request failed", "err", err)
		return fmt.Errorf("%s: encode: %w", op, err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/refresh", bytes.NewReader(body))
	if err != nil {
		logger.Warn("build request failed", "err", err)
		return fmt.Errorf("%s: build request: %w", op, err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Accept", "application/x-ndjson")

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		logger.Warn("request failed", "err", err)
		return fmt.Errorf("%s: %w", op, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		errResp := decodeErrorResponse(resp.Body)
		logger.Warn("non-200 response",
			"status", resp.StatusCode,
			"code", errResp.Code,
			"message", errResp.Message,
		)
		return fmt.Errorf("refresh: %s: %s", errResp.Code, errResp.Message)
	}

	reader := bufio.NewReader(resp.Body)
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			// Always write what we got, including the trailing newline
			// when present, so downstream parsers see line boundaries.
			if _, werr := w.Write(line); werr != nil {
				logger.Warn("writer failed", "err", werr)
				return fmt.Errorf("%s: write: %w", op, werr)
			}

			// Best-effort done-detection: if this line decodes to a
			// ProgressEvent with Phase=="done", we're finished. We
			// don't error if it doesn't decode — server may emit
			// non-ProgressEvent NDJSON for richer events later.
			trimmed := bytes.TrimSpace(line)
			if len(trimmed) > 0 {
				var ev ProgressEvent
				if jerr := json.Unmarshal(trimmed, &ev); jerr == nil && ev.Phase == "done" {
					logger.Debug("done",
						"files_done", ev.FilesDone,
						"files_total", ev.FilesTotal,
					)
					// Drain any remaining bytes so the connection can
					// be returned to the idle pool.
					_, _ = io.Copy(io.Discard, resp.Body)
					return nil
				}
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				logger.Debug("stream eof")
				return nil
			}
			// Context-canceled errors should propagate as-is for
			// callers to detect.
			if ctx.Err() != nil {
				logger.Warn("context done", "err", ctx.Err())
				return ctx.Err()
			}
			logger.Warn("stream read failed", "err", readErr)
			return fmt.Errorf("%s: read: %w", op, readErr)
		}
	}
}

// Shutdown issues POST /shutdown. Connection-closed-by-server is treated
// as a successful outcome because the daemon is expected to terminate
// before flushing a clean response.
func (c *Client) Shutdown(ctx context.Context) error {
	const op = "Shutdown"
	logger := c.logger.With("op", op)

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/shutdown", nil)
	if err != nil {
		logger.Warn("build request failed", "err", err)
		return fmt.Errorf("%s: build request: %w", op, err)
	}

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		// A daemon that terminates between accepting the request and
		// writing a clean response can produce EOF / connection reset.
		// Treat those as success.
		if isConnectionClosedErr(err) {
			logger.Debug("connection closed by server (treated as success)", "err", err)
			return nil
		}
		logger.Warn("request failed", "err", err)
		return fmt.Errorf("%s: %w", op, err)
	}
	defer resp.Body.Close()

	// Drain so the underlying connection can be reused if the daemon
	// chooses not to close it.
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		logger.Debug("ok", "status", resp.StatusCode)
		return nil
	}

	errResp := decodeErrorResponse(resp.Body)
	logger.Warn("non-2xx response",
		"status", resp.StatusCode,
		"code", errResp.Code,
		"message", errResp.Message,
	)
	return fmt.Errorf("shutdown: %s: %s", errResp.Code, errResp.Message)
}

// decodeErrorResponse best-effort decodes an ErrorResponse from r. If
// decoding fails, a synthetic ErrorResponse with Code="unknown" is
// returned so callers always have something to format.
func decodeErrorResponse(r io.Reader) ErrorResponse {
	var out ErrorResponse
	if err := json.NewDecoder(r).Decode(&out); err != nil || out.Code == "" {
		if out.Code == "" {
			out.Code = "unknown"
		}
		if out.Message == "" {
			out.Message = "no error details"
		}
	}
	return out
}

// isConnectionClosedErr reports whether err looks like the server
// closed the connection without writing a complete response. This
// covers EOF, connection-reset, and broken-pipe variants from the
// http transport.
func isConnectionClosedErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	// http transport wraps these with url.Error; unwrap to inspect.
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return true
	}
	// Fallback substring check for "connection reset" / "broken pipe"
	// produced by the runtime when the peer disappears mid-write.
	msg := err.Error()
	for _, needle := range []string{
		"connection reset",
		"broken pipe",
		"EOF",
		"server closed",
	} {
		if containsFold(msg, needle) {
			return true
		}
	}
	return false
}

// containsFold is a tiny case-insensitive substring helper that avoids
// pulling in strings just for ToLower.
func containsFold(s, sub string) bool {
	return len(sub) <= len(s) && bytes.Contains(
		bytes.ToLower([]byte(s)),
		bytes.ToLower([]byte(sub)),
	)
}
