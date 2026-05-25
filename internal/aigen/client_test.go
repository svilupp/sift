package aigen

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newTestClient(t *testing.T, h http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	c := NewClient("test-key", srv.URL)
	c.retryOverride = []time.Duration{1 * time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond}
	return c, srv
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func okBody(content string) map[string]any {
	return map[string]any{
		"choices": []map[string]any{
			{"message": map[string]any{"role": "assistant", "content": content}},
		},
		"usage": map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
	}
}

func TestClientCallOK(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("auth header: %q", got)
		}
		writeJSON(w, 200, okBody(`{"purpose":"P","files":[{"path":"a","summary":"s"}]}`))
	})

	resp, err := c.Call(context.Background(), CallRequest{
		System: "sys", User: "user", Strict: true,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !resp.UsedStrict {
		t.Errorf("expected strict mode used")
	}
	if resp.Stats.TokensIn != 10 || resp.Stats.TokensOut != 5 {
		t.Errorf("usage not captured: %+v", resp.Stats)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("expected 1 hit, got %d", hits)
	}
}

func TestClientRetryOn429(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n < 3 {
			writeJSON(w, 429, map[string]any{"error": "rate"})
			return
		}
		writeJSON(w, 200, okBody(`{"x":1}`))
	})
	resp, err := c.Call(context.Background(), CallRequest{System: "s", User: "u"})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp.Stats.Attempts != 3 {
		t.Errorf("attempts=%d want 3", resp.Stats.Attempts)
	}
}

func TestClientRetryOn500(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n < 2 {
			writeJSON(w, 500, map[string]any{"error": "boom"})
			return
		}
		writeJSON(w, 200, okBody(`ok`))
	})
	if _, err := c.Call(context.Background(), CallRequest{System: "s", User: "u"}); err != nil {
		t.Fatalf("err: %v", err)
	}
}

func TestClientFailFastOn401(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		writeJSON(w, 401, map[string]any{"error": "bad key"})
	})
	_, err := c.Call(context.Background(), CallRequest{System: "s", User: "u"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("expected 401 error; got %v", err)
	}
	if atomic.LoadInt32(&hits) != 1 {
		t.Errorf("401 should not retry; got %d hits", hits)
	}
}

func TestClientStrictDisabledOnSchema400(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		body, _ := io.ReadAll(r.Body)
		if n == 1 {
			if !strings.Contains(string(body), "response_format") {
				t.Errorf("first call should send response_format")
			}
			writeJSON(w, 400, map[string]any{"error": map[string]any{"message": "invalid response_format json_schema"}})
			// Make body contain literal markers used by the heuristic.
			fmt.Fprintln(w, "json_schema response_format")
			return
		}
		if strings.Contains(string(body), "response_format") {
			t.Errorf("second call should NOT send response_format")
		}
		writeJSON(w, 200, okBody(`hello`))
	})
	// First call: 400 → strict disabled.
	_, err := c.Call(context.Background(), CallRequest{System: "s", User: "u", Strict: true})
	if err == nil {
		t.Fatal("expected error on 400")
	}
	if !c.strictDisabled {
		t.Errorf("expected strictDisabled after schema 400")
	}
	// Second call: client should auto-fall to instruction mode.
	resp2, err := c.Call(context.Background(), CallRequest{System: "s", User: "u", Strict: true})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if resp2.UsedStrict {
		t.Errorf("expected UsedStrict=false after fallback")
	}
}

func TestClientContextCancel(t *testing.T) {
	srvDone := make(chan struct{}, 1)
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
		srvDone <- struct{}{}
	})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := c.Call(ctx, CallRequest{System: "s", User: "u"})
	if err == nil {
		t.Fatal("expected error on cancel")
	}
	// Drain the handler so httptest.Close doesn't block.
	select {
	case <-srvDone:
	case <-time.After(3 * time.Second):
	}
}

func TestClientRetryBudget(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		writeJSON(w, 503, map[string]any{"error": "down"})
	})
	_, err := c.Call(context.Background(), CallRequest{System: "s", User: "u"})
	if err == nil {
		t.Fatal("expected error after retries")
	}
	// 1 initial + 3 retries = 4 attempts.
	if atomic.LoadInt32(&hits) != 4 {
		t.Errorf("expected 4 attempts; got %d", hits)
	}
}

func TestLoadAPIKeyFromEnv(t *testing.T) {
	t.Setenv("DEEPINFRA_API_KEY", "from-env")
	got := LoadAPIKey("/tmp", "")
	if got != "from-env" {
		t.Errorf("got %q", got)
	}
}

func TestLoadAPIKeyFromDotEnvWalkUp(t *testing.T) {
	dir := t.TempDir()
	// Drop a .env at the dir root.
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("DEEPINFRA_API_KEY=from-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEEPINFRA_API_KEY", "")
	got := LoadAPIKey(sub, dir)
	if got != "from-file" {
		t.Errorf("got %q want from-file", got)
	}
}

func TestLoadAPIKeyMissing(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DEEPINFRA_API_KEY", "")
	if got := LoadAPIKey(dir, dir); got != "" {
		t.Errorf("expected empty; got %q", got)
	}
}

func TestLoadAPIKeyWithFallback(t *testing.T) {
	t.Helper()
	cases := []struct {
		name     string
		env      string
		dotenv   string // contents written to <dir>/.env, "" = no file
		fallback string
		want     string
	}{
		{name: "env wins", env: "from-env", dotenv: "DEEPINFRA_API_KEY=from-file\n", fallback: "from-config", want: "from-env"},
		{name: "dotenv beats fallback", env: "", dotenv: "DEEPINFRA_API_KEY=from-file\n", fallback: "from-config", want: "from-file"},
		{name: "fallback when env+dotenv empty", env: "", dotenv: "", fallback: "from-config", want: "from-config"},
		{name: "fallback trimmed", env: "", dotenv: "", fallback: "  spaced  ", want: "spaced"},
		{name: "all empty stays empty", env: "", dotenv: "", fallback: "", want: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.dotenv != "" {
				if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(tc.dotenv), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("DEEPINFRA_API_KEY", tc.env)
			got := LoadAPIKeyWithFallback(dir, dir, tc.fallback)
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestCostUSD(t *testing.T) {
	// 1M in tokens = $0.14, 1M out = $0.28.
	c := CostUSD(1_000_000, 1_000_000)
	if c < 0.41 || c > 0.43 {
		t.Errorf("cost=%.4f expected ~0.42", c)
	}
}
