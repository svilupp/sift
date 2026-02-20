package config

import (
	"os"
	"path/filepath"
	"testing"
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
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				assertEqual(t, tt.got, tt.want)
			})
		}
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
		assertEqual(t, cfg.API.RequestTimeoutSecs, 60)
	})
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

	if err := original.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	loaded, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	// API
	assertEqual(t, loaded.API.VoyageAPIKey, original.API.VoyageAPIKey)

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

	// Scoring
	assertEqual(t, loaded.Scoring.RecencyWeight, original.Scoring.RecencyWeight)
	assertEqual(t, loaded.Scoring.RecencyHalfLifeDays, original.Scoring.RecencyHalfLifeDays)
	assertEqual(t, loaded.Scoring.FeedbackEnabled, original.Scoring.FeedbackEnabled)

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
	assertEqual(t, cfg.Scoring.RecencyWeight, want.Scoring.RecencyWeight)
	assertEqual(t, cfg.BM25.Analyzer, want.BM25.Analyzer)
	assertEqual(t, cfg.Output.EditorCommand, want.Output.EditorCommand)
	assertEqual(t, cfg.Logs.MaxWeeks, want.Logs.MaxWeeks)
	assertEqual(t, cfg.API.VoyageAPIKey, "")
}

func TestPathHelpers(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)

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
