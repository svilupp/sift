package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"sift/internal/cache"
	"sift/internal/chunk"
	"sift/internal/config"
	"sift/internal/index"
	siftlog "sift/internal/log"
	"sift/internal/sync"
	"sift/internal/voyage"
)

func newRefreshCmd() *cobra.Command {
	var (
		collection string
		full       bool
		dryRun     bool
	)

	cmd := &cobra.Command{
		Use:   "refresh [files...]",
		Short: "Incremental index refresh",
		Long: `Scan collection folders, detect new/changed/deleted files, chunk text,
generate embeddings (if API key set), and update the BM25 + vector indexes.

By default only processes files changed since last refresh (uses content hash).
Use --full to reindex everything from scratch.
Pass file paths as arguments to refresh specific files only.

Examples:
  sift refresh                          # incremental, all collections
  sift refresh -c vault                 # just one collection
  sift refresh notes.md ideas.md        # refresh specific files
  sift refresh --full                   # reindex everything
  sift refresh --dry-run                # show what would change, no writes`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && collection != "" {
				return fmt.Errorf("cannot use --collection with file arguments")
			}

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
			bleveIdx, err := index.OpenBleve(blevePath, cfg.BM25.Analyzer)
			if err != nil {
				return err
			}
			defer bleveIdx.Close()

			// Create Voyage client if API key is set.
			var voyageClient *voyage.Client
			if cfg.API.VoyageAPIKey != "" {
				voyageClient = voyage.NewClient(cfg.API.VoyageAPIKey)
				voyageClient.EmbedModel = cfg.Embedding.Model
				voyageClient.EmbedDimensions = cfg.Embedding.Dimensions
				voyageClient.EmbedDtype = cfg.Embedding.OutputDtype
				voyageClient.RerankModel = cfg.Reranking.Model
				if cfg.API.RequestTimeoutSecs > 0 {
					voyageClient.SetTimeout(time.Duration(cfg.API.RequestTimeoutSecs) * time.Second)
				}
			}

			opts := sync.RefreshOptions{
				CollectionName: collection,
				Full:           full,
				DryRun:         dryRun,
				BatchSize:      cfg.Embedding.BatchSize,
				ChunkOpts: chunk.Options{
					RowsPerChunk:  cfg.Chunking.RowsPerChunk,
					OverlapRows:   cfg.Chunking.OverlapRows,
					MinChunkChars: cfg.Chunking.MinChunkChars,
					SkipEmptyRows: cfg.Chunking.SkipEmptyRows,
				},
			}

			w := cmd.OutOrStdout()

			var stats *sync.RefreshStats
			if len(args) > 0 {
				stats, err = sync.RefreshFiles(context.Background(), database, bleveIdx, voyageClient, args, opts, w)
			} else {
				fmt.Fprintln(w, "Refreshing...")
				stats, err = sync.Refresh(context.Background(), database, bleveIdx, voyageClient, opts, w)
			}
			if err != nil {
				return err
			}

			// Invalidate search cache after refresh.
			if cfg.Cache.Enabled {
				cache.New(database, cfg.Cache.TTLSeconds, cfg.Cache.MaxEntries).Clear()
			}

			if len(args) > 0 {
				fmt.Fprintf(w, "Done: %d new, %d changed, %d chunks in %s\n",
					stats.FilesNew, stats.FilesChanged,
					stats.ChunksTotal, stats.Duration.Round(time.Millisecond))
			} else {
				fmt.Fprintf(w, "Done: %d files scanned, %d new, %d changed, %d deleted, %d chunks in %s\n",
					stats.FilesScanned, stats.FilesNew, stats.FilesChanged, stats.FilesDeleted,
					stats.ChunksTotal, stats.Duration.Round(time.Millisecond))
			}

			if stats.ChunksEmbedded > 0 {
				fmt.Fprintf(w, "  Embeddings: %d embedded, %d errors, %d tokens\n",
					stats.ChunksEmbedded, stats.EmbedErrors, stats.EmbedTokens)
			}

			// Clean up old log files.
			logDir, logErr := config.LogDir()
			if logErr == nil {
				logger := siftlog.NewLogger(logDir, cfg.Logs.MaxWeeks)
				if cleanErr := logger.Cleanup(); cleanErr != nil {
					fmt.Fprintf(w, "Warning: log cleanup: %v\n", cleanErr)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVarP(&collection, "collection", "c", "", "Refresh specific collection")
	cmd.Flags().BoolVar(&full, "full", false, "Force full reindex")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Show what would be indexed")

	return cmd
}
