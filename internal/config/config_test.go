package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

func TestDefault(t *testing.T) {
	cfg := Default()

	t.Run("Embedding", func(t *testing.T) {
		tests := []struct {
			name string
			got  any
			want any
		}{
			{"Model", cfg.Embedding.Model, "voyage-4-lite"},
			{"Dimensions", cfg.Embedding.Dimensions, 512},
			{"OutputDtype", cfg.Embedding.OutputDtype, "binary"},
			{"BatchSize", cfg.Embedding.BatchSize, 200},
			{"InputTypeQuery", cfg.Embedding.InputTypeQuery, "query"},
			{"InputTypeDocument", cfg.Embedding.InputTypeDocument, "document"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assertEqual(t, tt.got, tt.want)
			})
		}
	})

	t.Run("Reranking", func(t *testing.T) {
		tests := []struct {
			name string
			got  any
			want any
		}{
			{"Model", cfg.Reranking.Model, "rerank-2.5-lite"},
			{"Enabled", cfg.Reranking.Enabled, true},
			{"TopN", cfg.Reranking.TopN, 75},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assertEqual(t, tt.got, tt.want)
			})
		}
	})

	t.Run("Chunking", func(t *testing.T) {
		tests := []struct {
			name string
			got  any
			want any
		}{
			{"RowsPerChunk", cfg.Chunking.RowsPerChunk, 25},
			{"OverlapRows", cfg.Chunking.OverlapRows, 5},
			{"MinChunkChars", cfg.Chunking.MinChunkChars, 200},
			{"SkipEmptyRows", cfg.Chunking.SkipEmptyRows, true},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assertEqual(t, tt.got, tt.want)
			})
		}
	})

	t.Run("Search", func(t *testing.T) {
		tests := []struct {
			name string
			got  any
			want any
		}{
			{"DefaultTopK", cfg.Search.DefaultTopK, 20},
			{"PreviewChars", cfg.Search.PreviewChars, 500},
			{"PreviewLines", cfg.Search.PreviewLines, 0},
			{"BM25Weight", cfg.Search.BM25Weight, 1.0},
			{"VectorWeight", cfg.Search.VectorWeight, 1.0},
			{"RRFK", cfg.Search.RRFK, 60},
			{"RRFBoostTopN", cfg.Search.RRFBoostTopN, 3},
			{"RRFBoostFactor", cfg.Search.RRFBoostFactor, 2.0},
			{"MaxChunksPerFile", cfg.Search.MaxChunksPerFile, 3},
			{"MaxChunksForReranking", cfg.Search.MaxChunksForReranking, 3},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assertEqual(t, tt.got, tt.want)
			})
		}
	})

	t.Run("Scoring", func(t *testing.T) {
		tests := []struct {
			name string
			got  any
			want any
		}{
			{"RecencyWeight", cfg.Scoring.RecencyWeight, 0.2},
			{"RecencyHalfLifeDays", cfg.Scoring.RecencyHalfLifeDays, 30},
			{"FeedbackEnabled", cfg.Scoring.FeedbackEnabled, true},
			{"BacklinkWeight", cfg.Scoring.BacklinkWeight, 0.1},
			{"ReadSignalEnabled", cfg.Scoring.ReadSignalEnabled, true},
			{"ReadSignalWeight", cfg.Scoring.ReadSignalWeight, 0.05},
			{"ReadSignalPath", cfg.Scoring.ReadSignalPath, "memory/.read-signals.tsv"},
			{"ReadSignalDays", cfg.Scoring.ReadSignalDays, 14},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assertEqual(t, tt.got, tt.want)
			})
		}
	})

	t.Run("Agent", func(t *testing.T) {
		assertEqual(t, cfg.Agent.PreviewChars, 200)
		assertEqual(t, cfg.Agent.Hint, "")
	})

	t.Run("BM25", func(t *testing.T) {
		assertEqual(t, cfg.BM25.Analyzer, "standard")
	})

	t.Run("Output", func(t *testing.T) {
		assertEqual(t, cfg.Output.EditorCommand, "code -g {file}:{line}")
	})

	t.Run("Logs", func(t *testing.T) {
		tests := []struct {
			name string
			got  any
			want any
		}{
			{"RotateWeekly", cfg.Logs.RotateWeekly, true},
			{"MaxWeeks", cfg.Logs.MaxWeeks, 12},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assertEqual(t, tt.got, tt.want)
			})
		}
	})

	t.Run("API", func(t *testing.T) {
		assertEqual(t, cfg.API.VoyageAPIKey, "")
		assertEqual(t, cfg.API.DeepInfraPriority, false)
		assertEqual(t, cfg.API.RequestTimeoutSecs, 60)
	})

	t.Run("Daemon", func(t *testing.T) {
		assertEqual(t, cfg.Daemon.Enabled, true)
		assertEqual(t, cfg.Daemon.IdleTimeout.D(), 30*time.Minute)
		assertEqual(t, cfg.Daemon.SpawnTimeout.D(), 300*time.Millisecond)
		assertEqual(t, cfg.Daemon.DialTimeout.D(), 50*time.Millisecond)
		assertEqual(t, cfg.Daemon.IdleTimeoutDuration(), 30*time.Minute)
	})

	t.Run("Transport", func(t *testing.T) {
		assertEqual(t, cfg.Transport.MaxIdleConnsPerHost, 8)
		assertEqual(t, cfg.Transport.IdleConnTimeout.D(), 5*time.Minute)
		assertEqual(t, cfg.Transport.TLSHandshakeTimeout.D(), 5*time.Second)
		assertEqual(t, cfg.Transport.ResponseHeaderTimeout.D(), 30*time.Second)
	})
}

func TestDaemonTransportRoundTrip(t *testing.T) {
	original := Default()
	original.Daemon.Enabled = false
	original.Daemon.IdleTimeout = Duration(45 * time.Minute)
	original.Daemon.SpawnTimeout = Duration(500 * time.Millisecond)
	original.Daemon.DialTimeout = Duration(75 * time.Millisecond)
	original.Transport.MaxIdleConnsPerHost = 16
	original.Transport.IdleConnTimeout = Duration(2 * time.Minute)
	original.Transport.TLSHandshakeTimeout = Duration(10 * time.Second)
	original.Transport.ResponseHeaderTimeout = Duration(45 * time.Second)

	data, err := toml.Marshal(original)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// Durations should be encoded as strings, not integers.
	// go-toml may use single or double quotes; accept both.
	emitted := string(data)
	for _, pair := range [][2]string{
		{`idle_timeout = "45m0s"`, `idle_timeout = '45m0s'`},
		{`spawn_timeout = "500ms"`, `spawn_timeout = '500ms'`},
	} {
		if !contains(emitted, pair[0]) && !contains(emitted, pair[1]) {
			t.Errorf("expected TOML to contain %q or %q, got:\n%s", pair[0], pair[1], emitted)
		}
	}

	var loaded Config
	if err := toml.Unmarshal(data, &loaded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	assertEqual(t, loaded.Daemon.Enabled, false)
	assertEqual(t, loaded.Daemon.IdleTimeout.D(), 45*time.Minute)
	assertEqual(t, loaded.Daemon.SpawnTimeout.D(), 500*time.Millisecond)
	assertEqual(t, loaded.Daemon.DialTimeout.D(), 75*time.Millisecond)
	assertEqual(t, loaded.Transport.MaxIdleConnsPerHost, 16)
	assertEqual(t, loaded.Transport.IdleConnTimeout.D(), 2*time.Minute)
	assertEqual(t, loaded.Transport.TLSHandshakeTimeout.D(), 10*time.Second)
	assertEqual(t, loaded.Transport.ResponseHeaderTimeout.D(), 45*time.Second)
}

// TestLoadMissingDaemonTransportSection verifies backwards-compat:
// a config file written without the daemon/transport sections must
// still load with the default values populated.
func TestLoadMissingDaemonTransportSection(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	siftDir := filepath.Join(tmp, ".sift")
	if err := os.MkdirAll(siftDir, 0755); err != nil {
		t.Fatalf("create .sift dir: %v", err)
	}

	// Write a minimal config that lacks [daemon] and [transport].
	cfgPath := filepath.Join(siftDir, "config.toml")
	minimal := `
[api]
voyage_api_key = "k"
[embedding]
model = "voyage-4-lite"
`
	if err := os.WriteFile(cfgPath, []byte(minimal), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	want := Default()
	assertEqual(t, loaded.API.DeepInfraPriority, false)
	assertEqual(t, loaded.Daemon.Enabled, want.Daemon.Enabled)
	assertEqual(t, loaded.Daemon.IdleTimeout.D(), want.Daemon.IdleTimeout.D())
	assertEqual(t, loaded.Daemon.SpawnTimeout.D(), want.Daemon.SpawnTimeout.D())
	assertEqual(t, loaded.Daemon.DialTimeout.D(), want.Daemon.DialTimeout.D())
	assertEqual(t, loaded.Transport.MaxIdleConnsPerHost, want.Transport.MaxIdleConnsPerHost)
	assertEqual(t, loaded.Transport.IdleConnTimeout.D(), want.Transport.IdleConnTimeout.D())
	assertEqual(t, loaded.Transport.TLSHandshakeTimeout.D(), want.Transport.TLSHandshakeTimeout.D())
	assertEqual(t, loaded.Transport.ResponseHeaderTimeout.D(), want.Transport.ResponseHeaderTimeout.D())
}

func TestSaveLoadRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	siftDir := filepath.Join(tmp, ".sift")
	if err := os.MkdirAll(siftDir, 0755); err != nil {
		t.Fatalf("create .sift dir: %v", err)
	}

	original := Default()
	original.API.VoyageAPIKey = "test-key-abc123"
	original.API.DeepInfraPriority = true

	if err := original.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// API
	assertEqual(t, loaded.API.VoyageAPIKey, original.API.VoyageAPIKey)
	assertEqual(t, loaded.API.DeepInfraPriority, original.API.DeepInfraPriority)

	// Embedding
	assertEqual(t, loaded.Embedding.Model, original.Embedding.Model)
	assertEqual(t, loaded.Embedding.Dimensions, original.Embedding.Dimensions)
	assertEqual(t, loaded.Embedding.BatchSize, original.Embedding.BatchSize)
	assertEqual(t, loaded.Embedding.InputTypeQuery, original.Embedding.InputTypeQuery)
	assertEqual(t, loaded.Embedding.InputTypeDocument, original.Embedding.InputTypeDocument)

	// Reranking
	assertEqual(t, loaded.Reranking.Model, original.Reranking.Model)
	assertEqual(t, loaded.Reranking.Enabled, original.Reranking.Enabled)
	assertEqual(t, loaded.Reranking.TopN, original.Reranking.TopN)

	// Chunking
	assertEqual(t, loaded.Chunking.RowsPerChunk, original.Chunking.RowsPerChunk)
	assertEqual(t, loaded.Chunking.OverlapRows, original.Chunking.OverlapRows)
	assertEqual(t, loaded.Chunking.MinChunkChars, original.Chunking.MinChunkChars)
	assertEqual(t, loaded.Chunking.SkipEmptyRows, original.Chunking.SkipEmptyRows)

	// Search
	assertEqual(t, loaded.Search.DefaultTopK, original.Search.DefaultTopK)
	assertEqual(t, loaded.Search.PreviewChars, original.Search.PreviewChars)
	assertEqual(t, loaded.Search.BM25Weight, original.Search.BM25Weight)
	assertEqual(t, loaded.Search.VectorWeight, original.Search.VectorWeight)
	assertEqual(t, loaded.Search.RRFK, original.Search.RRFK)
	assertEqual(t, loaded.Search.RRFBoostTopN, original.Search.RRFBoostTopN)
	assertEqual(t, loaded.Search.RRFBoostFactor, original.Search.RRFBoostFactor)
	assertEqual(t, loaded.Search.MaxChunksPerFile, original.Search.MaxChunksPerFile)

	// Agent
	assertEqual(t, loaded.Agent.PreviewChars, original.Agent.PreviewChars)
	assertEqual(t, loaded.Agent.Hint, original.Agent.Hint)

	// Scoring
	assertEqual(t, loaded.Scoring.RecencyWeight, original.Scoring.RecencyWeight)
	assertEqual(t, loaded.Scoring.RecencyHalfLifeDays, original.Scoring.RecencyHalfLifeDays)
	assertEqual(t, loaded.Scoring.FeedbackEnabled, original.Scoring.FeedbackEnabled)
	assertEqual(t, loaded.Scoring.BacklinkWeight, original.Scoring.BacklinkWeight)
	assertEqual(t, loaded.Scoring.ReadSignalEnabled, original.Scoring.ReadSignalEnabled)
	assertEqual(t, loaded.Scoring.ReadSignalWeight, original.Scoring.ReadSignalWeight)
	assertEqual(t, loaded.Scoring.ReadSignalPath, original.Scoring.ReadSignalPath)
	assertEqual(t, loaded.Scoring.ReadSignalDays, original.Scoring.ReadSignalDays)

	// BM25
	assertEqual(t, loaded.BM25.Analyzer, original.BM25.Analyzer)

	// Output
	assertEqual(t, loaded.Output.EditorCommand, original.Output.EditorCommand)

	// Logs
	assertEqual(t, loaded.Logs.RotateWeekly, original.Logs.RotateWeekly)
	assertEqual(t, loaded.Logs.MaxWeeks, original.Logs.MaxWeeks)
}

func TestLoadMissing(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load with no config file: %v", err)
	}

	want := Default()

	// Spot-check that returned config matches defaults.
	assertEqual(t, cfg.Embedding.Model, want.Embedding.Model)
	assertEqual(t, cfg.Embedding.Dimensions, want.Embedding.Dimensions)
	assertEqual(t, cfg.Reranking.Model, want.Reranking.Model)
	assertEqual(t, cfg.Reranking.Enabled, want.Reranking.Enabled)
	assertEqual(t, cfg.Chunking.RowsPerChunk, want.Chunking.RowsPerChunk)
	assertEqual(t, cfg.Search.DefaultTopK, want.Search.DefaultTopK)
	assertEqual(t, cfg.Search.BM25Weight, want.Search.BM25Weight)
	assertEqual(t, cfg.Agent.PreviewChars, want.Agent.PreviewChars)
	assertEqual(t, cfg.Scoring.RecencyWeight, want.Scoring.RecencyWeight)
	assertEqual(t, cfg.Scoring.BacklinkWeight, want.Scoring.BacklinkWeight)
	assertEqual(t, cfg.BM25.Analyzer, want.BM25.Analyzer)
	assertEqual(t, cfg.Output.EditorCommand, want.Output.EditorCommand)
	assertEqual(t, cfg.Logs.MaxWeeks, want.Logs.MaxWeeks)
	assertEqual(t, cfg.API.VoyageAPIKey, "")
}

func TestPathHelpers(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	// Make sure no stray override leaks in from the host env.
	t.Setenv("SIFT_DAEMON_SOCKET", "")

	tests := []struct {
		name   string
		fn     func() (string, error)
		suffix string
	}{
		{"Dir", Dir, ".sift"},
		{"ConfigPath", ConfigPath, filepath.Join(".sift", "config.toml")},
		{"DBPath", DBPath, filepath.Join(".sift", "sift.db")},
		{"BlevePath", BlevePath, filepath.Join(".sift", "bleve")},
		{"LockPath", LockPath, filepath.Join(".sift", ".lock")},
		{"LogDir", LogDir, filepath.Join(".sift", "logs")},
		{"SocketPath", SocketPath, filepath.Join(".sift", "sift.sock")},
		{"PIDPath", PIDPath, filepath.Join(".sift", "sift.pid")},
		{"DaemonLogPath", DaemonLogPath, filepath.Join(".sift", "logs", "daemon.log")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.fn()
			if err != nil {
				t.Fatalf("%s() error: %v", tt.name, err)
			}

			want := filepath.Join(tmp, tt.suffix)
			assertEqual(t, got, want)
		})
	}
}

func TestSocketPathEnvOverride(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	t.Run("override applied", func(t *testing.T) {
		override := filepath.Join(tmp, "custom.sock")
		t.Setenv("SIFT_DAEMON_SOCKET", override)

		got, err := SocketPath()
		if err != nil {
			t.Fatalf("SocketPath: %v", err)
		}
		assertEqual(t, got, override)
	})

	t.Run("empty override falls back to default", func(t *testing.T) {
		t.Setenv("SIFT_DAEMON_SOCKET", "")

		got, err := SocketPath()
		if err != nil {
			t.Fatalf("SocketPath: %v", err)
		}
		want := filepath.Join(tmp, ".sift", "sift.sock")
		assertEqual(t, got, want)
	})
}

func TestExists(t *testing.T) {
	t.Run("NotInitialized", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)

		exists, err := Exists()
		if err != nil {
			t.Fatalf("Exists: %v", err)
		}
		assertEqual(t, exists, false)
	})

	t.Run("Initialized", func(t *testing.T) {
		tmp := t.TempDir()
		t.Setenv("HOME", tmp)

		siftDir := filepath.Join(tmp, ".sift")
		if err := os.MkdirAll(siftDir, 0755); err != nil {
			t.Fatalf("create .sift dir: %v", err)
		}

		exists, err := Exists()
		if err != nil {
			t.Fatalf("Exists: %v", err)
		}
		assertEqual(t, exists, true)
	})
}

func TestSaveCreatesFile(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	siftDir := filepath.Join(tmp, ".sift")
	if err := os.MkdirAll(siftDir, 0755); err != nil {
		t.Fatalf("create .sift dir: %v", err)
	}

	cfg := Default()
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	path, err := ConfigPath()
	if err != nil {
		t.Fatalf("ConfigPath: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("config file not created: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("config file is empty")
	}
}

func TestLoadOverridesDefaults(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

	siftDir := filepath.Join(tmp, ".sift")
	if err := os.MkdirAll(siftDir, 0755); err != nil {
		t.Fatalf("create .sift dir: %v", err)
	}

	// Save config with non-default values.
	cfg := Default()
	cfg.Embedding.Model = "voyage-custom"
	cfg.Embedding.Dimensions = 512
	cfg.Search.DefaultTopK = 50
	cfg.Reranking.Enabled = false
	cfg.Logs.MaxWeeks = 4

	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	assertEqual(t, loaded.Embedding.Model, "voyage-custom")
	assertEqual(t, loaded.Embedding.Dimensions, 512)
	assertEqual(t, loaded.Search.DefaultTopK, 50)
	assertEqual(t, loaded.Reranking.Enabled, false)
	assertEqual(t, loaded.Logs.MaxWeeks, 4)
}

// assertEqual is a test helper that compares two values using %v formatting.
func assertEqual(t *testing.T, got, want any) {
	t.Helper()
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}
