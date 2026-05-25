//go:build integration

package daemon

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"sift/internal/config"
)

// BenchmarkHealthEndpoint measures round-trip /health latency through
// the Unix-socket transport. Goal stated by the U13 plan: < 2ms per
// request (i.e. < 2_000_000 ns/op).
//
// We start a real daemon (Serve) in-process so the measurement reflects
// the production code path. /health is intentionally minimal but it
// still does a JSON-encode of the full HealthResponse — so this is NOT
// a measurement of pure transport overhead. Treat the result as
// "request latency for the cheapest user-facing handler" rather than
// "daemon overhead". For pure transport overhead a no-op handler at a
// dedicated bench route would be needed, which is intentionally out of
// scope.
func BenchmarkHealthEndpoint(b *testing.B) {
	client, stop := startBenchDaemon(b)
	defer stop()

	ctx := context.Background()

	// Warm up so the first measured request doesn't pay one-time TLS /
	// connection-pool setup costs.
	for i := 0; i < 3; i++ {
		if _, err := client.Health(ctx); err != nil {
			b.Fatalf("warmup health: %v", err)
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := client.Health(ctx); err != nil {
			b.Fatalf("Health: %v", err)
		}
	}
	b.StopTimer()
}

// BenchmarkConcurrentSearches issues b.N searches via parallel goroutines
// and reports ns/op. This stresses request-handling throughput end-to-end.
//
// The daemon runs against an empty index, so each search returns ~0 results
// and the cost reflects HTTP plumbing + handler dispatch + an empty-result
// engine round-trip.
func BenchmarkConcurrentSearches(b *testing.B) {
	client, stop := startBenchDaemon(b)
	defer stop()

	// Warm up.
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if _, err := client.Search(ctx, SearchRequest{Query: "warmup", TopK: 1}); err != nil {
			b.Fatalf("warmup: %v", err)
		}
	}

	var counter atomic.Int64
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			i := counter.Add(1)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_, err := client.Search(ctx, SearchRequest{
				Query: fmt.Sprintf("q-%d", i),
				TopK:  3,
			})
			cancel()
			if err != nil {
				b.Errorf("Search: %v", err)
				return
			}
		}
	})
	b.StopTimer()
}

// startBenchDaemon mirrors startTestDaemon but works against *testing.B.
// We can't share the t-only helper because b.Helper(), b.Fatalf, etc.
// have different signatures.
func startBenchDaemon(b *testing.B) (*Client, func()) {
	b.Helper()

	dir, err := os.MkdirTemp("", "sift")
	if err != nil {
		b.Fatalf("tempdir: %v", err)
	}

	sockPath := filepath.Join(dir, "d.sock")
	if len(sockPath) >= 104 {
		b.Fatalf("socket path too long: %s", sockPath)
	}

	b.Setenv("SIFT_DIR", dir)
	b.Setenv("SIFT_DAEMON_SOCKET", sockPath)

	cfg := config.Default()
	cfg.API.VoyageAPIKey = ""
	cfg.Daemon.IdleTimeout = config.Duration(0)

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		errCh <- Serve(ctx, cfg)
	}()

	client, dialErr := DialUntilReady(5 * time.Second)
	if dialErr != nil {
		cancel()
		<-errCh
		_ = os.RemoveAll(dir)
		b.Fatalf("daemon ready: %v", dialErr)
	}

	stop := func() {
		shCtx, shCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = client.Shutdown(shCtx)
		shCancel()
		client.Close()
		select {
		case err := <-errCh:
			if err != nil {
				b.Logf("Serve: %v", err)
			}
		case <-time.After(15 * time.Second):
			b.Logf("Serve did not return within 15s")
		}
		cancel()
		_ = os.RemoveAll(dir)
	}
	return client, stop
}
