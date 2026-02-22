package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"

	"sift/internal/chunk"
	"sift/internal/config"
	"sift/internal/db"
	"sift/internal/index"
	siftlog "sift/internal/log"
	"sift/internal/voyage"
)

func newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Configuration and administration",
		Long: `Setup, inspect, and maintain SIFT.

Setup:      init
Inspect:    show, get, stats, health, logs
Modify:     set
Maintain:   rebuild-bm25, purge, retry-dead-letters

Examples:
  sift config init                                    # first-time setup
  sift config set api.voyage_api_key sk-xxx           # set API key
  sift config get search.default_top_k                # read a setting
  sift config health                                  # check everything works
  sift config stats                                   # collections, API usage, storage`,
	}

	cmd.AddCommand(
		newConfigInitCmd(),
		newConfigShowCmd(),
		newConfigSetCmd(),
		newConfigGetCmd(),
		newConfigStatsCmd(),
		newConfigHealthCmd(),
		newConfigLogsCmd(),
		newConfigRebuildBM25Cmd(),
		newConfigPurgeCmd(),
		newConfigRetryDeadLettersCmd(),
	)

	return cmd
}

func newConfigInitCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Initialize SIFT (creates ~/.sift/)",
		Long: `Create ~/.sift/ directory with default config.toml and empty SQLite database.
Run this once before using any other command. Safe to re-run (won't overwrite).

Next steps after init:
  sift config set api.voyage_api_key <key>   # optional: enables vector search
  sift collections add <name> <path>         # register a folder
  sift refresh                               # index everything`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := config.Dir()
			if err != nil {
				return err
			}

			if err := os.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("create sift dir: %w", err)
			}

			cfg := config.Default()
			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save config: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s/config.toml\n", dir)

			dbPath, err := config.DBPath()
			if err != nil {
				return err
			}
			database, err := db.Open(dbPath)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer database.Close()

			if err := database.Init(); err != nil {
				return fmt.Errorf("init db: %w", err)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Created %s\n", dbPath)

			fmt.Fprintln(cmd.OutOrStdout(), "\nSIFT initialized. Set your Voyage API key:")
			fmt.Fprintln(cmd.OutOrStdout(), "  sift config set api.voyage_api_key <your-key>")

			return nil
		},
	}
}

func newConfigShowCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show",
		Short: "Show current configuration",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			data, err := toml.Marshal(cfg)
			if err != nil {
				return fmt.Errorf("marshal config: %w", err)
			}

			fmt.Fprint(cmd.OutOrStdout(), string(data))
			return nil
		},
	}
}

func newConfigSetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value (dot notation)",
		Long: `Set a config value using dot-notation keys. Persists to ~/.sift/config.toml.

Valid keys:
  api.voyage_api_key          Voyage API key (enables vector search + reranking)
  embedding.model             Embedding model (default: voyage-4-lite)
  embedding.dimensions        Vector dimensions (default: 512)
  embedding.output_dtype      Output type: float, binary (default: binary)
  embedding.batch_size        Texts per API call (default: 64)
  reranking.model             Rerank model (default: rerank-2.5-lite)
  reranking.enabled           Enable reranking (default: true)
  reranking.top_n             Candidates to rerank (default: 20)
  chunking.rows_per_chunk     Lines per chunk (default: 40)
  chunking.overlap_rows       Overlap between chunks (default: 5)
  chunking.min_chunk_chars    Drop chunks below this (default: 50)
  chunking.skip_empty_rows    Ignore blank lines (default: true)
  search.default_top_k        Default result count (default: 5)
  search.preview_chars        Preview length in chars (default: 300)
  search.bm25_weight          BM25 weight in RRF fusion (default: 1.0)
  search.vector_weight        Vector weight in RRF fusion (default: 1.0)
  search.rrf_k                RRF constant k (default: 60)
  search.rrf_boost_top_n      Boost top-N in same-rank results (default: 3)
  search.rrf_boost_factor     Boost multiplier (default: 1.5)
  search.max_chunks_per_file  Max chunks per file in results (default: 3)
  scoring.recency_weight      Recency signal weight (default: 0.15)
  scoring.recency_half_life_days  Decay half-life in days (default: 30)
  scoring.feedback_enabled    Use feedback signals (default: true)
  bm25.analyzer               Bleve analyzer: standard, simple, keyword (default: standard)
  output.editor_command       Editor template, e.g. "code -g {file}:{line}"
  logs.rotate_weekly          Rotate logs weekly (default: true)
  logs.max_weeks              Keep N weeks of logs (default: 4)

Examples:
  sift config set api.voyage_api_key sk-xxx
  sift config set search.default_top_k 10
  sift config set reranking.enabled false`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, value := args[0], args[1]

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			if err := setConfigValue(cfg, key, value); err != nil {
				return err
			}

			if err := cfg.Save(); err != nil {
				return fmt.Errorf("save config: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Set %s = %s\n", key, value)
			return nil
		},
	}
}

func newConfigGetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "get <key>",
		Short: "Get a config value (dot notation)",
		Long: `Get a config value. Uses the same dot-notation keys as "sift config set".
Run "sift config set --help" for the full list of valid keys.

Examples:
  sift config get api.voyage_api_key
  sift config get search.default_top_k`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			val, err := getConfigValue(cfg, args[0])
			if err != nil {
				return err
			}

			fmt.Fprintln(cmd.OutOrStdout(), val)
			return nil
		},
	}
}

// --- config stats ---

func newConfigStatsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "stats",
		Short: "Show SIFT usage statistics",
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			w := cmd.OutOrStdout()

			// Collections section.
			fmt.Fprintln(w, "Collections")
			fmt.Fprintln(w, strings.Repeat("-", 40))
			cols, err := database.ListCollections()
			if err != nil {
				return fmt.Errorf("list collections: %w", err)
			}
			for _, col := range cols {
				fileCount, fcErr := database.CollectionFileCount(col.ID)
				if fcErr != nil {
					fileCount = -1
				}
				chunkCount, ccErr := database.CollectionChunkCount(col.ID)
				if ccErr != nil {
					chunkCount = -1
				}
				fmt.Fprintf(w, "  %-20s %d files, %d chunks\n", col.Name, fileCount, chunkCount)
			}
			if len(cols) == 0 {
				fmt.Fprintln(w, "  (none)")
			}

			// Totals.
			totalFiles, _ := database.TotalFileCount()
			totalChunks, _ := database.TotalChunkCount()
			totalEmbeddings, _ := database.TotalEmbeddingCount()
			fmt.Fprintf(w, "\n  Total: %d files, %d chunks, %d embeddings\n", totalFiles, totalChunks, totalEmbeddings)

			// API usage section.
			fmt.Fprintf(w, "\nAPI Usage\n")
			fmt.Fprintln(w, strings.Repeat("-", 40))
			apiStats, err := database.GetAPIUsageStats()
			if err != nil {
				return fmt.Errorf("get api stats: %w", err)
			}
			var totalTokens int
			var totalRequests int
			for _, s := range apiStats {
				fmt.Fprintf(w, "  %-20s %d requests, %d tokens\n", s.Operation, s.RequestCount, s.TokenCount)
				totalTokens += s.TokenCount
				totalRequests += s.RequestCount
			}
			if len(apiStats) == 0 {
				fmt.Fprintln(w, "  (no API calls recorded)")
			} else {
				// Estimated cost: tokens * $0.00002 / 1K = tokens * 0.00000002
				estimatedCost := float64(totalTokens) * 0.00002 / 1000.0
				fmt.Fprintf(w, "\n  Total: %d requests, %d tokens\n", totalRequests, totalTokens)
				fmt.Fprintf(w, "  Estimated cost: $%.4f\n", estimatedCost)
			}

			// Feedback section.
			fmt.Fprintf(w, "\nFeedback\n")
			fmt.Fprintln(w, strings.Repeat("-", 40))
			fbTotals, err := database.GetFeedbackTotals()
			if err != nil {
				return fmt.Errorf("get feedback totals: %w", err)
			}
			if fbTotals.Total > 0 {
				fmt.Fprintf(w, "  Total: %d signals (%d positive, %d negative)\n",
					fbTotals.Total, fbTotals.Positive, fbTotals.Negative)
			} else {
				fmt.Fprintln(w, "  (no feedback recorded)")
			}

			// Search sessions section.
			fmt.Fprintf(w, "\nSearch Sessions\n")
			fmt.Fprintln(w, strings.Repeat("-", 40))
			sessionCount, err := database.SearchSessionCount()
			if err != nil {
				return fmt.Errorf("get session count: %w", err)
			}
			fmt.Fprintf(w, "  Total searches: %d\n", sessionCount)

			// Storage section.
			fmt.Fprintf(w, "\nStorage\n")
			fmt.Fprintln(w, strings.Repeat("-", 40))

			dbPath, err := config.DBPath()
			if err != nil {
				return err
			}
			if fi, statErr := os.Stat(dbPath); statErr == nil {
				fmt.Fprintf(w, "  Database: %s (%s)\n", dbPath, formatBytes(fi.Size()))
			}

			blevePath, err := config.BlevePath()
			if err != nil {
				return err
			}
			bleveSize, bleveSizeErr := dirSize(blevePath)
			if bleveSizeErr == nil {
				fmt.Fprintf(w, "  BM25 index: %s (%s)\n", blevePath, formatBytes(bleveSize))
			} else if !os.IsNotExist(bleveSizeErr) {
				fmt.Fprintf(w, "  BM25 index: %s (error: %v)\n", blevePath, bleveSizeErr)
			} else {
				fmt.Fprintf(w, "  BM25 index: not created\n")
			}

			return nil
		},
	}
}

// dirSize returns the total size of all files in a directory (recursive).
func dirSize(path string) (int64, error) {
	var size int64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() {
			size += info.Size()
		}
		return nil
	})
	return size, err
}

// --- config health ---

func newConfigHealthCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "health",
		Short: "Check SIFT system health",
		RunE: func(cmd *cobra.Command, args []string) error {
			w := cmd.OutOrStdout()
			fmt.Fprintln(w, "SIFT Health Check")
			fmt.Fprintln(w, strings.Repeat("-", 40))

			// 1. Config file exists.
			configPath, err := config.ConfigPath()
			if err != nil {
				return err
			}
			if _, statErr := os.Stat(configPath); statErr == nil {
				fmt.Fprintf(w, "[OK] Config file: %s\n", configPath)
			} else {
				fmt.Fprintf(w, "[!!] Config file missing: %s\n", configPath)
			}

			// 2. DB accessible.
			dbPath, err := config.DBPath()
			if err != nil {
				return err
			}
			database, dbErr := db.Open(dbPath)
			if dbErr != nil {
				fmt.Fprintf(w, "[!!] Database: %v\n", dbErr)
			} else {
				// Check we can query it.
				var version int
				queryErr := database.QueryRow("SELECT COALESCE(MAX(version),0) FROM schema_version").Scan(&version)
				if queryErr != nil {
					fmt.Fprintf(w, "[!!] Database query failed: %v\n", queryErr)
				} else {
					fi, _ := os.Stat(dbPath)
					sizeStr := ""
					if fi != nil {
						sizeStr = fmt.Sprintf(" (%s)", formatBytes(fi.Size()))
					}
					fmt.Fprintf(w, "[OK] Database: schema v%d%s\n", version, sizeStr)
				}
				defer database.Close()
			}

			// 3. Bleve index accessible.
			blevePath, err := config.BlevePath()
			if err != nil {
				return err
			}
			if _, statErr := os.Stat(blevePath); statErr == nil {
				cfg, loadErr := config.Load()
				if loadErr != nil {
					fmt.Fprintf(w, "[!!] Bleve index: cannot load config: %v\n", loadErr)
				} else {
					bleveIdx, openErr := index.OpenBleve(blevePath, cfg.BM25.Analyzer)
					if openErr != nil {
						fmt.Fprintf(w, "[!!] Bleve index: %v\n", openErr)
					} else {
						docCount, countErr := bleveIdx.DocCount()
						if countErr != nil {
							fmt.Fprintf(w, "[!!] Bleve index: cannot get doc count: %v\n", countErr)
						} else {
							fmt.Fprintf(w, "[OK] Bleve index: %d documents\n", docCount)
						}

						// Compare Bleve and SQLite chunk counts.
						if countErr == nil && database != nil && dbErr == nil {
							totalChunks, chunkErr := database.TotalChunkCount()
							if chunkErr == nil {
								if docCount != uint64(totalChunks) {
									fmt.Fprintf(w, "[!!] Index drift: Bleve has %d docs, SQLite has %d chunks\n", docCount, totalChunks)
									fmt.Fprintf(w, "     Run 'sift config rebuild-bm25' to fix\n")
								} else {
									fmt.Fprintf(w, "[OK] Index consistency: %d docs in both Bleve and SQLite\n", totalChunks)
								}
							}
						}

						bleveIdx.Close()
					}
				}
			} else {
				fmt.Fprintln(w, "[!!] Bleve index: not found")
			}

			// 4. API key status.
			cfg, loadErr := config.Load()
			if loadErr != nil {
				fmt.Fprintf(w, "[!!] Config load: %v\n", loadErr)
			} else {
				if cfg.API.VoyageAPIKey != "" {
					maskedKey := cfg.API.VoyageAPIKey
					if len(maskedKey) > 8 {
						maskedKey = maskedKey[:4] + "..." + maskedKey[len(maskedKey)-4:]
					}
					fmt.Fprintf(w, "[OK] API key: set (%s)\n", maskedKey)
				} else {
					fmt.Fprintln(w, "[--] API key: not set (BM25-only mode)")
				}
			}

			// 5. Dead letters count.
			if database != nil && dbErr == nil {
				dlCount, dlErr := database.UnresolvedDeadLetterCount()
				if dlErr != nil {
					fmt.Fprintf(w, "[!!] Dead letters: %v\n", dlErr)
				} else if dlCount > 0 {
					fmt.Fprintf(w, "[!!] Dead letters: %d unresolved\n", dlCount)
				} else {
					fmt.Fprintln(w, "[OK] Dead letters: none")
				}
			}

			// 6. Stale files (in DB but not on filesystem).
			if database != nil && dbErr == nil {
				paths, pathErr := database.GetAllFilePaths()
				if pathErr != nil {
					fmt.Fprintf(w, "[!!] Stale file check: %v\n", pathErr)
				} else {
					var staleCount int
					for _, p := range paths {
						if _, statErr := os.Stat(p); os.IsNotExist(statErr) {
							staleCount++
						}
					}
					if staleCount > 0 {
						fmt.Fprintf(w, "[!!] Stale files: %d files in DB missing from filesystem\n", staleCount)
					} else {
						fmt.Fprintf(w, "[OK] Stale files: none (%d files checked)\n", len(paths))
					}
				}
			}

			// 7. Collections with 0 files.
			if database != nil && dbErr == nil {
				cols, colErr := database.ListCollections()
				if colErr != nil {
					fmt.Fprintf(w, "[!!] Collections: %v\n", colErr)
				} else {
					var emptyCount int
					for _, col := range cols {
						count, countErr := database.CollectionFileCount(col.ID)
						if countErr != nil {
							continue
						}
						if count == 0 {
							emptyCount++
							fmt.Fprintf(w, "[--] Collection %q has 0 files\n", col.Name)
						}
					}
					if emptyCount == 0 && len(cols) > 0 {
						fmt.Fprintf(w, "[OK] Collections: %d, all have files\n", len(cols))
					} else if len(cols) == 0 {
						fmt.Fprintln(w, "[--] Collections: none registered")
					}
				}
			}

			return nil
		},
	}
}

// formatBytes formats a byte count as a human-readable string.
func formatBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}

// --- config logs ---

func newConfigLogsCmd() *cobra.Command {
	var (
		feedback bool
		sqlLogs  bool
		count    int
	)

	cmd := &cobra.Command{
		Use:   "logs",
		Short: "View recent log entries",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			siftDir, err := config.Dir()
			if err != nil {
				return err
			}
			logDir := filepath.Join(siftDir, "logs")
			logger := siftlog.NewLogger(logDir, cfg.Logs.MaxWeeks)

			logType := "searches"
			if feedback {
				logType = "feedback"
			} else if sqlLogs {
				logType = "sql"
			}

			entries, err := logger.ReadRecent(logType, count)
			if err != nil {
				return fmt.Errorf("read logs: %w", err)
			}

			if len(entries) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No %s log entries found.\n", logType)
				return nil
			}

			w := cmd.OutOrStdout()
			fmt.Fprintf(w, "Recent %s logs (%d entries):\n\n", logType, len(entries))
			for _, entry := range entries {
				var pretty json.RawMessage
				if err := json.Unmarshal(entry, &pretty); err != nil {
					fmt.Fprintf(w, "%s\n", entry)
					continue
				}
				formatted, err := json.MarshalIndent(pretty, "  ", "  ")
				if err != nil {
					fmt.Fprintf(w, "%s\n", entry)
					continue
				}
				fmt.Fprintf(w, "  %s\n", formatted)
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&feedback, "feedback", false, "Show feedback logs")
	cmd.Flags().BoolVar(&sqlLogs, "sql", false, "Show SQL query logs")
	cmd.Flags().IntVar(&count, "count", 20, "Number of entries to show")

	return cmd
}

// --- config rebuild-bm25 ---

func newConfigRebuildBM25Cmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rebuild-bm25",
		Short: "Rebuild BM25 index from database chunks",
		Long: `Destroy and recreate the Bleve BM25 index from chunks stored in SQLite.
Use when "sift config health" reports index drift (Bleve/SQLite count mismatch)
or after recovering from a crash during refresh.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			fl, err := acquireLock()
			if err != nil {
				return err
			}
			defer func() { _ = fl.Unlock() }()

			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			blevePath, err := config.BlevePath()
			if err != nil {
				return err
			}

			w := cmd.OutOrStdout()

			// Destroy existing Bleve index if it exists.
			if _, statErr := os.Stat(blevePath); statErr == nil {
				fmt.Fprintln(w, "Destroying existing BM25 index...")
				if err := os.RemoveAll(blevePath); err != nil {
					return fmt.Errorf("remove bleve index: %w", err)
				}
			}

			// Create new Bleve index.
			fmt.Fprintln(w, "Creating new BM25 index...")
			bleveIdx, err := index.OpenBleve(blevePath, cfg.BM25.Analyzer)
			if err != nil {
				return fmt.Errorf("create bleve index: %w", err)
			}
			defer bleveIdx.Close()

			// Get all collections.
			cols, err := database.ListCollections()
			if err != nil {
				return fmt.Errorf("list collections: %w", err)
			}

			totalChunks := 0
			for _, col := range cols {
				files, err := database.GetFilesByCollection(col.ID)
				if err != nil {
					return fmt.Errorf("get files for %q: %w", col.Name, err)
				}

				for _, f := range files {
					chunks, err := database.GetChunksByFile(f.ID)
					if err != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "Warning: get chunks for %s: %v\n", f.Path, err)
						continue
					}

					relPath := strings.TrimPrefix(f.Path, col.Path)
					relPath = strings.TrimPrefix(relPath, "/")

					for _, c := range chunks {
						content := readChunkPreview(f.Path, c.StartLine, c.EndLine, 0)
						if content == "" {
							continue
						}
						content = chunk.PrependProvenance(content, relPath, f.Title, col.Name)
						chunkIDStr := strconv.FormatInt(c.ID, 10)
						if err := bleveIdx.Index(chunkIDStr, content, f.Path); err != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "Warning: index chunk %d: %v\n", c.ID, err)
							continue
						}
						totalChunks++
					}
				}
			}

			fmt.Fprintf(w, "Rebuilt BM25 index: %d chunks reindexed\n", totalChunks)
			return nil
		},
	}
}

// --- config purge ---

func newConfigPurgeCmd() *cobra.Command {
	var (
		force      bool
		collection string
	)

	cmd := &cobra.Command{
		Use:   "purge",
		Short: "Delete all SIFT data or a specific collection",
		RunE: func(cmd *cobra.Command, args []string) error {
			siftDir, err := config.Dir()
			if err != nil {
				return err
			}

			w := cmd.OutOrStdout()

			if collection != "" {
				return purgeCollection(cmd, collection, force)
			}

			// Full purge.
			if !force {
				fmt.Fprint(w, "Delete ALL SIFT data (DB, index, logs)? [y/N] ")
				var answer string
				_, _ = fmt.Fscanln(cmd.InOrStdin(), &answer)
				if strings.ToLower(answer) != "y" {
					fmt.Fprintln(w, "Cancelled.")
					return nil
				}
			}

			dbPath := filepath.Join(siftDir, "sift.db")
			blevePath := filepath.Join(siftDir, "bleve")
			logsPath := filepath.Join(siftDir, "logs")

			for _, p := range []string{dbPath, dbPath + "-wal", dbPath + "-shm", blevePath, logsPath} {
				if err := os.RemoveAll(p); err != nil && !os.IsNotExist(err) {
					fmt.Fprintf(cmd.ErrOrStderr(), "Warning: remove %s: %v\n", p, err)
				}
			}

			fmt.Fprintln(w, "All SIFT data purged.")
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation")
	cmd.Flags().StringVarP(&collection, "collection", "c", "", "Purge only this collection")

	return cmd
}

func purgeCollection(cmd *cobra.Command, name string, force bool) error {
	w := cmd.OutOrStdout()

	if !force {
		fmt.Fprintf(w, "Delete collection %q and all its indexed data? [y/N] ", name)
		var answer string
		_, _ = fmt.Fscanln(cmd.InOrStdin(), &answer)
		if strings.ToLower(answer) != "y" {
			fmt.Fprintln(w, "Cancelled.")
			return nil
		}
	}

	fl, err := acquireLock()
	if err != nil {
		return err
	}
	defer func() { _ = fl.Unlock() }()

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	database, err := openDB()
	if err != nil {
		return err
	}
	defer database.Close()

	col, err := database.GetCollection(name)
	if err != nil {
		return err
	}
	if col == nil {
		return fmt.Errorf("collection %q not found", name)
	}

	// Clean Bleve entries.
	blevePath, err := config.BlevePath()
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(blevePath); statErr == nil {
		bleveIdx, err := index.OpenBleve(blevePath, cfg.BM25.Analyzer)
		if err != nil {
			return fmt.Errorf("open bleve: %w", err)
		}
		defer bleveIdx.Close()

		files, err := database.GetFilesByCollection(col.ID)
		if err != nil {
			return fmt.Errorf("get files: %w", err)
		}
		for _, f := range files {
			chunks, err := database.GetChunksByFile(f.ID)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Warning: get chunks for %s: %v\n", f.Path, err)
				continue
			}
			for _, c := range chunks {
				if err := bleveIdx.Delete(strconv.FormatInt(c.ID, 10)); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "Warning: bleve delete: %v\n", err)
				}
			}
		}
	}

	if err := database.RemoveCollection(name); err != nil {
		return err
	}

	fmt.Fprintf(w, "Purged collection %q\n", name)
	return nil
}

// --- config retry-dead-letters ---

func newConfigRetryDeadLettersCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "retry-dead-letters",
		Short: "Retry failed operations from the dead letters table",
		Long: `Retry embed operations that failed during refresh (e.g. API timeout, rate limit).
Failed operations are stored in the dead_letters table and retried here.
Check "sift config health" to see if there are unresolved dead letters.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			deadLetters, err := database.GetUnresolvedDeadLetters()
			if err != nil {
				return fmt.Errorf("get dead letters: %w", err)
			}

			w := cmd.OutOrStdout()

			if len(deadLetters) == 0 {
				fmt.Fprintln(w, "No unresolved dead letters.")
				return nil
			}

			fmt.Fprintf(w, "Found %d unresolved dead letter(s):\n\n", len(deadLetters))

			// Check API key before attempting embed retries.
			var voyageClient *voyage.Client
			if cfg.API.VoyageAPIKey != "" {
				voyageClient = voyage.NewClient(cfg.API.VoyageAPIKey)
				voyageClient.EmbedModel = cfg.Embedding.Model
				voyageClient.EmbedDimensions = cfg.Embedding.Dimensions
				voyageClient.EmbedDtype = cfg.Embedding.OutputDtype
				voyageClient.RerankModel = cfg.Reranking.Model
			}

			var resolved, failed, skipped int
			for _, dl := range deadLetters {
				fmt.Fprintf(w, "  [%d] operation=%s file=%s error=%s attempts=%d\n",
					dl.ID, dl.Operation, dl.FilePath, dl.ErrorMessage, dl.Attempts)

				switch dl.Operation {
				case "embed":
					if voyageClient == nil {
						fmt.Fprintf(w, "       Skipped: no API key configured\n\n")
						skipped++
						continue
					}

					// Parse chunk_info to get chunk ID.
					var chunkInfo struct {
						ChunkID int64 `json:"chunk_id"`
					}
					if parseErr := json.Unmarshal([]byte(dl.ChunkInfo), &chunkInfo); parseErr != nil {
						fmt.Fprintf(w, "       Skipped: cannot parse chunk_info: %v\n\n", parseErr)
						if incErr := database.IncrementDeadLetterAttempts(dl.ID); incErr != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "Warning: increment attempts for %d: %v\n", dl.ID, incErr)
						}
						failed++
						continue
					}

					// Load chunk and file from DB.
					chunkRec, file, getErr := database.GetChunkWithFile(chunkInfo.ChunkID)
					if getErr != nil {
						fmt.Fprintf(w, "       Failed: load chunk %d: %v\n\n", chunkInfo.ChunkID, getErr)
						if incErr := database.IncrementDeadLetterAttempts(dl.ID); incErr != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "Warning: increment attempts for %d: %v\n", dl.ID, incErr)
						}
						failed++
						continue
					}
					if chunkRec == nil || file == nil {
						fmt.Fprintf(w, "       Skipped: chunk %d no longer exists\n\n", chunkInfo.ChunkID)
						if resolveErr := database.ResolveDeadLetter(dl.ID); resolveErr != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "Warning: resolve dead letter %d: %v\n", dl.ID, resolveErr)
						}
						resolved++
						continue
					}

					// Read chunk content from source file.
					content := readChunkPreview(file.Path, chunkRec.StartLine, chunkRec.EndLine, 0)
					if content == "" {
						fmt.Fprintf(w, "       Skipped: empty content for chunk %d\n\n", chunkInfo.ChunkID)
						if incErr := database.IncrementDeadLetterAttempts(dl.ID); incErr != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "Warning: increment attempts for %d: %v\n", dl.ID, incErr)
						}
						failed++
						continue
					}

					// Prepend provenance for consistency with normal indexing.
					col, colErr := database.GetCollectionByID(file.CollectionID)
					if colErr == nil && col != nil {
						relPath := strings.TrimPrefix(file.Path, col.Path)
						relPath = strings.TrimPrefix(relPath, "/")
						content = chunk.PrependProvenance(content, relPath, file.Title, col.Name)
					}

					// Call Voyage to embed.
					vectors, _, embedErr := voyageClient.Embed(context.Background(), []string{content}, cfg.Embedding.InputTypeDocument)
					if embedErr != nil {
						fmt.Fprintf(w, "       Failed: embed: %v\n\n", embedErr)
						if incErr := database.IncrementDeadLetterAttempts(dl.ID); incErr != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "Warning: increment attempts for %d: %v\n", dl.ID, incErr)
						}
						failed++
						continue
					}

					if len(vectors) == 0 || len(vectors[0]) == 0 {
						fmt.Fprintf(w, "       Failed: empty vector returned\n\n")
						if incErr := database.IncrementDeadLetterAttempts(dl.ID); incErr != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "Warning: increment attempts for %d: %v\n", dl.ID, incErr)
						}
						failed++
						continue
					}

					// Store embedding.
					var vecBytes []byte
					var dims int
					if index.IsBinaryDtype(cfg.Embedding.OutputDtype) {
						vecBytes = index.EncodeBinaryVector(vectors[0])
						dims = cfg.Embedding.Dimensions
					} else {
						vecBytes = index.EncodeVector(vectors[0])
						dims = len(vectors[0])
					}
					if storeErr := database.UpsertEmbedding(chunkInfo.ChunkID, vecBytes, cfg.Embedding.Model, dims); storeErr != nil {
						fmt.Fprintf(w, "       Failed: store embedding: %v\n\n", storeErr)
						if incErr := database.IncrementDeadLetterAttempts(dl.ID); incErr != nil {
							fmt.Fprintf(cmd.ErrOrStderr(), "Warning: increment attempts for %d: %v\n", dl.ID, incErr)
						}
						failed++
						continue
					}

					// Resolve dead letter.
					if resolveErr := database.ResolveDeadLetter(dl.ID); resolveErr != nil {
						fmt.Fprintf(cmd.ErrOrStderr(), "Warning: resolve dead letter %d: %v\n", dl.ID, resolveErr)
					}
					fmt.Fprintf(w, "       Resolved: embedded chunk %d\n\n", chunkInfo.ChunkID)
					resolved++

				default:
					// Rerank and other transient failures don't need retry.
					fmt.Fprintf(w, "       Skipped: %s failures are transient (will retry on next search)\n\n", dl.Operation)
					skipped++
				}
			}

			fmt.Fprintf(w, "Results: %d resolved, %d failed, %d skipped\n", resolved, failed, skipped)
			return nil
		},
	}
}

// setConfigValue sets a config field by dot-notation key.
func setConfigValue(cfg *config.Config, key, value string) error {
	switch key {
	case "api.voyage_api_key":
		cfg.API.VoyageAPIKey = value
	case "embedding.model":
		cfg.Embedding.Model = value
	case "embedding.dimensions":
		return setInt(&cfg.Embedding.Dimensions, value)
	case "embedding.output_dtype":
		cfg.Embedding.OutputDtype = value
	case "embedding.batch_size":
		return setInt(&cfg.Embedding.BatchSize, value)
	case "reranking.model":
		cfg.Reranking.Model = value
	case "reranking.enabled":
		return setBool(&cfg.Reranking.Enabled, value)
	case "reranking.top_n":
		return setInt(&cfg.Reranking.TopN, value)
	case "chunking.rows_per_chunk":
		return setInt(&cfg.Chunking.RowsPerChunk, value)
	case "chunking.overlap_rows":
		return setInt(&cfg.Chunking.OverlapRows, value)
	case "chunking.min_chunk_chars":
		return setInt(&cfg.Chunking.MinChunkChars, value)
	case "chunking.skip_empty_rows":
		return setBool(&cfg.Chunking.SkipEmptyRows, value)
	case "search.default_top_k":
		return setInt(&cfg.Search.DefaultTopK, value)
	case "search.preview_chars":
		return setInt(&cfg.Search.PreviewChars, value)
	case "search.bm25_weight":
		return setFloat(&cfg.Search.BM25Weight, value)
	case "search.vector_weight":
		return setFloat(&cfg.Search.VectorWeight, value)
	case "search.rrf_k":
		return setInt(&cfg.Search.RRFK, value)
	case "search.rrf_boost_top_n":
		return setInt(&cfg.Search.RRFBoostTopN, value)
	case "search.rrf_boost_factor":
		return setFloat(&cfg.Search.RRFBoostFactor, value)
	case "search.max_chunks_per_file":
		return setInt(&cfg.Search.MaxChunksPerFile, value)
	case "scoring.recency_weight":
		return setFloat(&cfg.Scoring.RecencyWeight, value)
	case "scoring.recency_half_life_days":
		return setInt(&cfg.Scoring.RecencyHalfLifeDays, value)
	case "scoring.feedback_enabled":
		return setBool(&cfg.Scoring.FeedbackEnabled, value)
	case "bm25.analyzer":
		cfg.BM25.Analyzer = value
	case "output.editor_command":
		cfg.Output.EditorCommand = value
	case "logs.rotate_weekly":
		return setBool(&cfg.Logs.RotateWeekly, value)
	case "logs.max_weeks":
		return setInt(&cfg.Logs.MaxWeeks, value)
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}
	return nil
}

// getConfigValue gets a config field by dot-notation key.
func getConfigValue(cfg *config.Config, key string) (string, error) {
	switch key {
	case "api.voyage_api_key":
		return cfg.API.VoyageAPIKey, nil
	case "embedding.model":
		return cfg.Embedding.Model, nil
	case "embedding.dimensions":
		return fmt.Sprint(cfg.Embedding.Dimensions), nil
	case "embedding.output_dtype":
		return cfg.Embedding.OutputDtype, nil
	case "embedding.batch_size":
		return fmt.Sprint(cfg.Embedding.BatchSize), nil
	case "reranking.model":
		return cfg.Reranking.Model, nil
	case "reranking.enabled":
		return fmt.Sprint(cfg.Reranking.Enabled), nil
	case "reranking.top_n":
		return fmt.Sprint(cfg.Reranking.TopN), nil
	case "chunking.rows_per_chunk":
		return fmt.Sprint(cfg.Chunking.RowsPerChunk), nil
	case "chunking.overlap_rows":
		return fmt.Sprint(cfg.Chunking.OverlapRows), nil
	case "chunking.min_chunk_chars":
		return fmt.Sprint(cfg.Chunking.MinChunkChars), nil
	case "chunking.skip_empty_rows":
		return fmt.Sprint(cfg.Chunking.SkipEmptyRows), nil
	case "search.default_top_k":
		return fmt.Sprint(cfg.Search.DefaultTopK), nil
	case "search.preview_chars":
		return fmt.Sprint(cfg.Search.PreviewChars), nil
	case "search.bm25_weight":
		return fmt.Sprint(cfg.Search.BM25Weight), nil
	case "search.vector_weight":
		return fmt.Sprint(cfg.Search.VectorWeight), nil
	case "search.rrf_k":
		return fmt.Sprint(cfg.Search.RRFK), nil
	case "search.rrf_boost_top_n":
		return fmt.Sprint(cfg.Search.RRFBoostTopN), nil
	case "search.rrf_boost_factor":
		return fmt.Sprint(cfg.Search.RRFBoostFactor), nil
	case "search.max_chunks_per_file":
		return fmt.Sprint(cfg.Search.MaxChunksPerFile), nil
	case "scoring.recency_weight":
		return fmt.Sprint(cfg.Scoring.RecencyWeight), nil
	case "scoring.recency_half_life_days":
		return fmt.Sprint(cfg.Scoring.RecencyHalfLifeDays), nil
	case "scoring.feedback_enabled":
		return fmt.Sprint(cfg.Scoring.FeedbackEnabled), nil
	case "bm25.analyzer":
		return cfg.BM25.Analyzer, nil
	case "output.editor_command":
		return cfg.Output.EditorCommand, nil
	case "logs.rotate_weekly":
		return fmt.Sprint(cfg.Logs.RotateWeekly), nil
	case "logs.max_weeks":
		return fmt.Sprint(cfg.Logs.MaxWeeks), nil
	default:
		return "", fmt.Errorf("unknown config key: %s", key)
	}
}

func setInt(target *int, value string) error {
	var n int
	if _, err := fmt.Sscanf(value, "%d", &n); err != nil {
		return fmt.Errorf("invalid integer: %s", value)
	}
	*target = n
	return nil
}

func setFloat(target *float64, value string) error {
	var f float64
	if _, err := fmt.Sscanf(value, "%f", &f); err != nil {
		return fmt.Errorf("invalid float: %s", value)
	}
	*target = f
	return nil
}

func setBool(target *bool, value string) error {
	switch strings.ToLower(value) {
	case "true", "1", "yes":
		*target = true
	case "false", "0", "no":
		*target = false
	default:
		return fmt.Errorf("invalid boolean: %s", value)
	}
	return nil
}
