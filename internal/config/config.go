package config

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	toml "github.com/pelletier/go-toml/v2"
)

// Config represents the SIFT configuration from config.toml.
type Config struct {
	API       APIConfig       `toml:"api"`
	Embedding EmbeddingConfig `toml:"embedding"`
	Reranking RerankingConfig `toml:"reranking"`
	Chunking  ChunkingConfig  `toml:"chunking"`
	Search    SearchConfig    `toml:"search"`
	Agent     AgentConfig     `toml:"agent"`
	Scoring   ScoringConfig   `toml:"scoring"`
	BM25      BM25Config      `toml:"bm25"`
	Output    OutputConfig    `toml:"output"`
	Logs      LogsConfig      `toml:"logs"`
	Cache     CacheConfig     `toml:"cache"`
	Daemon    DaemonConfig    `toml:"daemon"`
	Transport TransportConfig `toml:"transport"`
}

// DaemonConfig governs the optional sift daemon process: whether it is
// enabled, when it self-exits after idleness, and the timeouts the CLI
// uses when spawning / dialing it.
type DaemonConfig struct {
	Enabled bool `toml:"enabled"`
	// IdleTimeout: how long the daemon stays alive with no requests
	// before exiting. Zero (empty string or "0") means never exit.
	IdleTimeout Duration `toml:"idle_timeout"`
	// SpawnTimeout: how long a CLI invocation waits for a freshly
	// spawned daemon to become reachable before falling back.
	SpawnTimeout Duration `toml:"spawn_timeout"`
	// DialTimeout: how long the CLI waits when dialing the unix socket
	// of an already-running daemon before treating it as unreachable.
	DialTimeout Duration `toml:"dial_timeout"`
}

// IdleTimeoutDuration is a convenience accessor returning the idle
// timeout as a time.Duration. Zero means "never exit".
func (d *DaemonConfig) IdleTimeoutDuration() time.Duration {
	return d.IdleTimeout.D()
}

// TransportConfig tunes the shared *http.Transport used by upstream
// API clients (e.g. Voyage). Each field corresponds to the same-named
// field on http.Transport.
type TransportConfig struct {
	MaxIdleConns          int      `toml:"max_idle_conns"`
	MaxIdleConnsPerHost   int      `toml:"max_idle_conns_per_host"`
	MaxConnsPerHost       int      `toml:"max_conns_per_host"`
	IdleConnTimeout       Duration `toml:"idle_conn_timeout"`
	TLSHandshakeTimeout   Duration `toml:"tls_handshake_timeout"`
	ResponseHeaderTimeout Duration `toml:"response_header_timeout"`
}

type APIConfig struct {
	VoyageAPIKey       string `toml:"voyage_api_key"`
	DeepInfraAPIKey    string `toml:"deepinfra_api_key"`
	DeepInfraPriority  bool   `toml:"deepinfra_priority"`
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
	Mode            string `toml:"mode"` // "row" or "section" (default: "row")
	RowsPerChunk    int    `toml:"rows_per_chunk"`
	OverlapRows     int    `toml:"overlap_rows"`
	MinChunkChars   int    `toml:"min_chunk_chars"`
	SkipEmptyRows   bool   `toml:"skip_empty_rows"`
	MaxSectionChars int    `toml:"max_section_chars"` // for section mode (default: 2000)
	MinSectionChars int    `toml:"min_section_chars"` // for section mode (default: 100)
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
	IncludeLinks          bool    `toml:"include_links"` // return links in results (default: true)
}

type AgentConfig struct {
	PreviewChars int    `toml:"preview_chars"`
	Hint         string `toml:"hint"`
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
	BacklinkWeight      float64     `toml:"backlink_weight"`
	ReadSignalEnabled   bool        `toml:"read_signal_enabled"`
	ReadSignalWeight    float64     `toml:"read_signal_weight"`
	ReadSignalPath      string      `toml:"read_signal_path"`
	ReadSignalDays      int         `toml:"read_signal_days"`
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
			Mode:            "section",
			RowsPerChunk:    25,
			OverlapRows:     5,
			MinChunkChars:   200,
			SkipEmptyRows:   true,
			MaxSectionChars: 2000,
			MinSectionChars: 100,
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
			IncludeLinks:          true,
		},
		Agent: AgentConfig{
			PreviewChars: 200,
			Hint:         "",
		},
		Scoring: ScoringConfig{
			RecencyWeight:       0.2,
			RecencyHalfLifeDays: 30,
			FeedbackEnabled:     true,
			BacklinkWeight:      0.1,
			ReadSignalEnabled:   true,
			ReadSignalWeight:    0.05,
			ReadSignalPath:      "memory/.read-signals.tsv",
			ReadSignalDays:      14,
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
		Daemon: DaemonConfig{
			Enabled:      true,
			IdleTimeout:  Duration(30 * time.Minute),
			SpawnTimeout: Duration(300 * time.Millisecond),
			DialTimeout:  Duration(50 * time.Millisecond),
		},
		Transport: TransportConfig{
			MaxIdleConns:          16,
			MaxIdleConnsPerHost:   8,
			MaxConnsPerHost:       16,
			IdleConnTimeout:       Duration(5 * time.Minute),
			TLSHandshakeTimeout:   Duration(5 * time.Second),
			ResponseHeaderTimeout: Duration(30 * time.Second),
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

// SocketPath returns the path to the daemon's unix socket.
//
// If SIFT_DAEMON_SOCKET is set it overrides the default location; the
// override applies to BOTH the server (listen) side and the client
// (dial) side, so they can never disagree. A warning is logged via
// log/slog when the override is used and differs from the default,
// since it tends to be the source of "daemon not reachable" surprises.
func SocketPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	def := filepath.Join(dir, "sift.sock")
	if override := os.Getenv("SIFT_DAEMON_SOCKET"); override != "" {
		if override != def {
			slog.Warn("SIFT_DAEMON_SOCKET overrides default socket path",
				"override", override,
				"default", def,
			)
		}
		return override, nil
	}
	return def, nil
}

// PIDPath returns the path to the daemon's PID file.
func PIDPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "sift.pid"), nil
}

// DaemonLogPath returns the path to the daemon's log file
// (~/.sift/logs/daemon.log). The parent logs/ directory is created
// lazily by the logger that opens this file (see internal/log).
func DaemonLogPath() (string, error) {
	logDir, err := LogDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(logDir, "daemon.log"), nil
}

// RefreshIndexPIDPath returns the path used by `sift refresh
// --detach` to track its background child.
func RefreshIndexPIDPath() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "refresh-index.pid"), nil
}

// RefreshIndexLogPath returns the path used by `sift refresh --detach`
// for stdout/stderr redirection.
func RefreshIndexLogPath() (string, error) {
	logDir, err := LogDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(logDir, "refresh-index.jsonl"), nil
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
