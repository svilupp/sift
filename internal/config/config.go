package config

import (
	"fmt"
	"os"
	"path/filepath"

	toml "github.com/pelletier/go-toml/v2"
)

// Config represents the SIFT configuration from config.toml.
type Config struct {
	API       APIConfig       `toml:"api"`
	Embedding EmbeddingConfig `toml:"embedding"`
	Reranking RerankingConfig `toml:"reranking"`
	Chunking  ChunkingConfig  `toml:"chunking"`
	Search    SearchConfig    `toml:"search"`
	Scoring   ScoringConfig   `toml:"scoring"`
	BM25      BM25Config      `toml:"bm25"`
	Output    OutputConfig    `toml:"output"`
	Logs      LogsConfig      `toml:"logs"`
	Cache     CacheConfig     `toml:"cache"`
}

type APIConfig struct {
	VoyageAPIKey       string `toml:"voyage_api_key"`
	RequestTimeoutSecs int    `toml:"request_timeout_seconds"`
}

type EmbeddingConfig struct {
	Model             string `toml:"model"`
	Dimensions        int    `toml:"dimensions"`
	OutputDtype       string `toml:"output_dtype"`
	BatchSize         int    `toml:"batch_size"`
	InputTypeQuery    string `toml:"input_type_query"`
	InputTypeDocument string `toml:"input_type_document"`
}

type RerankingConfig struct {
	Model   string `toml:"model"`
	Enabled bool   `toml:"enabled"`
	TopN    int    `toml:"top_n"`
}

type ChunkingConfig struct {
	RowsPerChunk  int  `toml:"rows_per_chunk"`
	OverlapRows   int  `toml:"overlap_rows"`
	MinChunkChars int  `toml:"min_chunk_chars"`
	SkipEmptyRows bool `toml:"skip_empty_rows"`
}

type SearchConfig struct {
	DefaultTopK           int     `toml:"default_top_k"`
	PreviewChars          int     `toml:"preview_chars"`
	PreviewLines          int     `toml:"preview_lines"`
	BM25Weight            float64 `toml:"bm25_weight"`
	VectorWeight          float64 `toml:"vector_weight"`
	RRFK                  int     `toml:"rrf_k"`
	RRFBoostTopN          int     `toml:"rrf_boost_top_n"`
	RRFBoostFactor        float64 `toml:"rrf_boost_factor"`
	MaxChunksPerFile      int     `toml:"max_chunks_per_file"`
	MaxChunksForReranking int     `toml:"max_chunks_for_reranking"`
	AdaptiveMinK          int     `toml:"adaptive_min_k"`
	AdaptiveMaxK          int     `toml:"adaptive_max_k"`
	Threshold             float64 `toml:"threshold"`
	DedupIdenticalContent bool    `toml:"dedup_identical_content"`
}

type CacheConfig struct {
	Enabled    bool `toml:"enabled"`
	TTLSeconds int  `toml:"ttl_seconds"`
	MaxEntries int  `toml:"max_entries"`
}

type ScoringConfig struct {
	RecencyWeight       float64     `toml:"recency_weight"`
	RecencyHalfLifeDays int         `toml:"recency_half_life_days"`
	FeedbackEnabled     bool        `toml:"feedback_enabled"`
	PathBoosts          []PathBoost `toml:"path_boost"`
}

// PathBoost maps a glob pattern to a score multiplier.
// Patterns use doublestar glob syntax (e.g. "**/pinned/*").
// Evaluated in order; first match wins.
type PathBoost struct {
	Pattern string  `toml:"pattern"`
	Boost   float64 `toml:"boost"`
}

type BM25Config struct {
	Analyzer string `toml:"analyzer"`
}

type OutputConfig struct {
	EditorCommand string `toml:"editor_command"`
}

type LogsConfig struct {
	RotateWeekly bool `toml:"rotate_weekly"`
	MaxWeeks     int  `toml:"max_weeks"`
}

// Default returns a Config with all defaults set.
func Default() *Config {
	return &Config{
		API: APIConfig{
			RequestTimeoutSecs: 60,
		},
		Embedding: EmbeddingConfig{
			Model:             "voyage-4-lite",
			Dimensions:        512,
			OutputDtype:       "binary",
			BatchSize:         200,
			InputTypeQuery:    "query",
			InputTypeDocument: "document",
		},
		Reranking: RerankingConfig{
			Model:   "rerank-2.5-lite",
			Enabled: true,
			TopN:    75,
		},
		Chunking: ChunkingConfig{
			RowsPerChunk:  25,
			OverlapRows:   5,
			MinChunkChars: 200,
			SkipEmptyRows: true,
		},
		Search: SearchConfig{
			DefaultTopK:           20,
			PreviewChars:          500,
			PreviewLines:          0, // 0 = disabled; when > 0, pretty mode shows N lines centered on match
			BM25Weight:            1.0,
			VectorWeight:          1.0,
			RRFK:                  60,
			RRFBoostTopN:          3,
			RRFBoostFactor:        2.0,
			MaxChunksPerFile:      3,
			MaxChunksForReranking: 3,
			AdaptiveMinK:          10,
			AdaptiveMaxK:          20,
			Threshold:             0.40,
			DedupIdenticalContent: true,
		},
		Scoring: ScoringConfig{
			RecencyWeight:       0.2,
			RecencyHalfLifeDays: 30,
			FeedbackEnabled:     true,
		},
		BM25: BM25Config{
			Analyzer: "standard",
		},
		Output: OutputConfig{
			EditorCommand: "code -g {file}:{line}",
		},
		Logs: LogsConfig{
			RotateWeekly: true,
			MaxWeeks:     12,
		},
		Cache: CacheConfig{
			Enabled:    true,
			TTLSeconds: 300,
			MaxEntries: 100,
		},
	}
}

// Dir returns the SIFT data directory (~/.sift/ or $SIFT_DIR if set).
func Dir() (string, error) {
	if dir := os.Getenv("SIFT_DIR"); dir != "" {
		return dir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("get home dir: %w", err)
	}
	return filepath.Join(home, ".sift"), nil
}

// ConfigPath returns the path to config.toml.
func ConfigPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// DBPath returns the path to sift.db.
func DBPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sift.db"), nil
}

// BlevePath returns the path to the Bleve index directory.
func BlevePath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "bleve"), nil
}

// LogDir returns the path to the logs directory.
func LogDir() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "logs"), nil
}

// LockPath returns the path to the lock file.
func LockPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, ".lock"), nil
}

// Load reads config from disk, returning defaults if file doesn't exist.
func Load() (*Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return nil, err
	}

	cfg := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := toml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return cfg, nil
}

// Save writes config to disk.
func (c *Config) Save() error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}

	data, err := toml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return os.WriteFile(path, data, 0644)
}

// Exists returns true if the SIFT directory has been initialized.
func Exists() (bool, error) {
	dir, err := Dir()
	if err != nil {
		return false, err
	}
	_, err = os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
