package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	toml "github.com/pelletier/go-toml/v2"
	"github.com/spf13/cobra"

	"sift/internal/anchor"
	"sift/internal/bm25"
	"sift/internal/chunk"
	"sift/internal/config"
	"sift/internal/db"
	"sift/internal/eval"
	"sift/internal/search"
	"sift/internal/sync"
	"sift/internal/voyage"
)

func newEvalCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "eval",
		Short: "Evaluation experiment commands",
		Long:  "Run anchor and link expansion experiments against test corpora.",
	}
	cmd.AddCommand(newEvalRunCmd())
	cmd.AddCommand(newEvalReportCmd())
	return cmd
}

func newEvalRunCmd() *cobra.Command {
	var (
		casesPath    string
		corpusPath   string
		collection   string
		variant      string
		output       string
		topK         int
		decay        float64
		headingBoost float64
		hybrid       bool
	)

	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run eval cases against a variant",
		Long: `Set up a temporary sift instance, index a corpus, and run eval cases.

Results are written to a JSON file and a summary is printed to stdout.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := context.Background()
			w := cmd.ErrOrStderr()

			// 1. Validate variant flag.
			validVariants := map[string]bool{"v0": true, "v1": true, "v2": true, "v3": true, "v4": true, "all": true}
			if !validVariants[variant] {
				return fmt.Errorf("invalid variant %q: must be v0, v1, v2, v3, v4, or all", variant)
			}

			// 2. Resolve corpus to absolute path.
			absCorpus, err := filepath.Abs(corpusPath)
			if err != nil {
				return fmt.Errorf("resolve corpus path: %w", err)
			}
			info, err := os.Stat(absCorpus)
			if err != nil {
				return fmt.Errorf("stat corpus: %w", err)
			}
			if !info.IsDir() {
				return fmt.Errorf("corpus path is not a directory: %s", absCorpus)
			}

			// 3. Load cases.
			cases, err := eval.LoadCases(casesPath)
			if err != nil {
				return fmt.Errorf("load cases: %w", err)
			}
			fmt.Fprintf(w, "Loaded %d eval cases\n", len(cases))

			// 4. Create temp HOME.
			tmpHome, err := os.MkdirTemp("", "sift-eval-*")
			if err != nil {
				return fmt.Errorf("create temp dir: %w", err)
			}
			defer os.RemoveAll(tmpHome)

			siftDir := filepath.Join(tmpHome, ".sift")
			if err := os.MkdirAll(siftDir, 0755); err != nil {
				return fmt.Errorf("create sift dir: %w", err)
			}

			// 5. Load config: user's real config for hybrid mode, defaults for BM25-only.
			var cfg *config.Config
			if hybrid {
				loaded, loadErr := config.Load()
				if loadErr != nil {
					return fmt.Errorf("load user config for hybrid mode: %w", loadErr)
				}
				cfg = loaded
				fmt.Fprintf(w, "Hybrid mode: using embeddings (%s) + reranking (%s)\n",
					cfg.Embedding.Model, cfg.Reranking.Model)
			} else {
				cfg = config.Default()
			}
			cfgData, err := toml.Marshal(cfg)
			if err != nil {
				return fmt.Errorf("marshal config: %w", err)
			}
			if err := os.WriteFile(filepath.Join(siftDir, "config.toml"), cfgData, 0644); err != nil {
				return fmt.Errorf("write config: %w", err)
			}

			// 6. Open DB and initialize schema.
			dbPath := filepath.Join(siftDir, "sift.db")
			database, err := db.Open(dbPath)
			if err != nil {
				return fmt.Errorf("open db: %w", err)
			}
			defer database.Close()

			if err := database.Init(); err != nil {
				return fmt.Errorf("init db: %w", err)
			}

			// 7. Open Bleve index.
			blevePath := filepath.Join(siftDir, "bleve")
			bleveIdx, err := bm25.OpenBleve(blevePath, cfg.BM25.Analyzer)
			if err != nil {
				return fmt.Errorf("open bleve: %w", err)
			}
			defer bleveIdx.Close()

			// 8. Add collection.
			_, err = database.AddCollection(collection, absCorpus, nil)
			if err != nil {
				return fmt.Errorf("add collection: %w", err)
			}
			fmt.Fprintf(w, "Added collection %q -> %s\n", collection, absCorpus)

			// 9. Create Voyage client if hybrid mode.
			var voyageClient *voyage.Client
			if hybrid && voyage.HasAPIKey(cfg.API.VoyageAPIKey) {
				voyageClient = voyage.NewClient(cfg.API.VoyageAPIKey)
			}

			// 10. Run refresh.
			refreshOpts := sync.RefreshOptions{
				CollectionName: collection,
				ChunkOpts: chunk.Options{
					RowsPerChunk:  cfg.Chunking.RowsPerChunk,
					OverlapRows:   cfg.Chunking.OverlapRows,
					MinChunkChars: cfg.Chunking.MinChunkChars,
					SkipEmptyRows: cfg.Chunking.SkipEmptyRows,
				},
			}
			stats, err := sync.Refresh(ctx, database, bleveIdx, voyageClient, refreshOpts, w)
			if err != nil {
				return fmt.Errorf("refresh: %w", err)
			}
			mode := "BM25-only"
			if hybrid {
				mode = fmt.Sprintf("hybrid (BM25 + %d embeddings)", stats.ChunksEmbedded)
			}
			fmt.Fprintf(w, "Indexed %d chunks from %d files in %s [%s]\n",
				stats.ChunksTotal, stats.FilesScanned, stats.Duration, mode)

			// 11. Create search engine.
			engine := search.NewEngine(database, bleveIdx, voyageClient, cfg)

			searchFn := func(ctx context.Context, query string, k int) ([]eval.ResultEntry, float64, error) {
				start := time.Now()
				sr, err := engine.Search(ctx, query, search.SearchOptions{
					Collection: collection,
					TopK:       k,
				})
				if err != nil {
					return nil, 0, err
				}
				elapsed := float64(time.Since(start).Microseconds()) / 1000.0

				entries := make([]eval.ResultEntry, len(sr.Results))
				for i, r := range sr.Results {
					entries[i] = eval.ResultEntry{
						FilePath:  relPath(corpusPath, r.FilePath),
						StartLine: r.StartLine,
						EndLine:   r.EndLine,
						Score:     r.FinalScore,
						Rank:      i + 1,
					}
				}
				return entries, elapsed, nil
			}

			// 11. Build corpus index for V1-V4 section mapping.
			var sectionMapper eval.SectionMapper
			needSections := variant == "v1" || variant == "v2" || variant == "v3" || variant == "v4" || variant == "all"
			if needSections {
				corpusIdx, err := anchor.BuildCorpusIndex(absCorpus)
				if err != nil {
					return fmt.Errorf("build corpus index: %w", err)
				}
				sectionMapper = &anchorMapper{idx: corpusIdx}
				fmt.Fprintf(w, "Built corpus index: %d files, %d links\n",
					len(corpusIdx.Files), len(corpusIdx.Links))
			}

			runner := &eval.Runner{
				SearchFn:   searchFn,
				SectionMap: sectionMapper,
			}

			// 12. Determine variants to run.
			var variants []eval.Variant
			switch variant {
			case "v0":
				variants = []eval.Variant{eval.V0}
			case "v1":
				variants = []eval.Variant{eval.V0, eval.V1}
			case "v2":
				variants = []eval.Variant{eval.V0, eval.V1, eval.V2}
			case "v3":
				variants = []eval.Variant{eval.V0, eval.V1, eval.V3}
			case "v4":
				variants = []eval.Variant{eval.V0, eval.V1, eval.V3, eval.V4}
			case "all":
				variants = []eval.Variant{eval.V0, eval.V1, eval.V2, eval.V3, eval.V4}
			}

			// 13. Run each variant.
			var runs []*eval.RunResult
			for _, v := range variants {
				fmt.Fprintf(w, "\nRunning variant %s...\n", v)
				result, err := runner.Run(ctx, eval.RunConfig{
					Variant:      v,
					TopK:         topK,
					HeadingBoost: headingBoost,
					Decay:        decay,
				}, cases)
				if err != nil {
					return fmt.Errorf("run variant %s: %w", v, err)
				}
				runs = append(runs, result)
				fmt.Fprintf(w, "  Completed %d cases in %s\n",
					len(result.Cases), result.EndTime.Sub(result.StartTime))
			}

			// 14. Build report and write output.
			report := buildEvalReport(runs)

			f, err := os.Create(output)
			if err != nil {
				return fmt.Errorf("create output file: %w", err)
			}
			defer f.Close()

			enc := json.NewEncoder(f)
			enc.SetIndent("", "  ")
			if err := enc.Encode(report); err != nil {
				return fmt.Errorf("write report json: %w", err)
			}
			fmt.Fprintf(w, "\nResults written to %s\n", output)

			// 15. Print summary to stdout.
			printEvalReport(cmd.OutOrStdout(), report)

			return nil
		},
	}

	cmd.Flags().StringVar(&casesPath, "cases", "", "path to JSONL cases file")
	cmd.Flags().StringVar(&corpusPath, "corpus", "", "path to corpus directory")
	cmd.Flags().StringVar(&collection, "collection", "", "collection name")
	cmd.Flags().StringVar(&variant, "variant", "all", "variant to run: v0, v1, v2, v3, v4, or all")
	cmd.Flags().StringVar(&output, "output", "eval_results.json", "output file path")
	cmd.Flags().IntVar(&topK, "top-k", 10, "number of results to evaluate")
	cmd.Flags().Float64Var(&decay, "decay", 0.5, "link expansion decay factor (used by v2, v4)")
	cmd.Flags().Float64Var(&headingBoost, "heading-boost", 2.0, "heading boost factor (used by v3, v4)")
	cmd.Flags().BoolVar(&hybrid, "hybrid", false, "use full hybrid search (embeddings + reranking) from user config")
	_ = cmd.MarkFlagRequired("cases")
	_ = cmd.MarkFlagRequired("corpus")
	_ = cmd.MarkFlagRequired("collection")

	return cmd
}

func newEvalReportCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "report <results.json>",
		Short: "Display eval results report",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("read results: %w", err)
			}

			var report evalReport
			if err := json.Unmarshal(data, &report); err != nil {
				return fmt.Errorf("parse results: %w", err)
			}

			printEvalReport(cmd.OutOrStdout(), &report)
			return nil
		},
	}
	return cmd
}

// anchorMapper adapts the anchor package to the eval.SectionMapper interface.
// relPath returns the relative path from root to path, falling back to filepath.Base.
func relPath(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.Base(path)
	}
	return rel
}

type anchorMapper struct {
	idx *anchor.CorpusIndex
}

func (m *anchorMapper) MapToSections(results []eval.ResultEntry) []eval.ResultEntry {
	// Convert to anchor.ChunkResult for aggregation.
	chunks := make([]anchor.ChunkResult, len(results))
	for i, r := range results {
		chunks[i] = anchor.ChunkResult{
			FilePath:  r.FilePath,
			StartLine: r.StartLine,
			EndLine:   r.EndLine,
			Score:     r.Score,
		}
	}

	sectionResults := anchor.AggregateSections(m.idx, chunks)

	// Sort by score descending.
	sort.Slice(sectionResults, func(i, j int) bool {
		return sectionResults[i].Score > sectionResults[j].Score
	})

	entries := make([]eval.ResultEntry, len(sectionResults))
	for i, sr := range sectionResults {
		entries[i] = eval.ResultEntry{
			FilePath:  sr.FilePath,
			SectionID: sr.Section.ID,
			StartLine: sr.Section.StartLine,
			EndLine:   sr.Section.EndLine,
			Score:     sr.Score,
			Rank:      i + 1,
		}
	}
	return entries
}

func (m *anchorMapper) ExpandLinks(results []eval.ResultEntry, decay float64) []eval.ResultEntry {
	// Convert entries to SectionResults for ExpandOneHop.
	sectionResults := make([]anchor.SectionResult, len(results))
	for i, r := range results {
		sectionResults[i] = anchor.SectionResult{
			Section: anchor.Section{
				ID:        r.SectionID,
				StartLine: r.StartLine,
				EndLine:   r.EndLine,
				FilePath:  r.FilePath,
			},
			Score:    r.Score,
			FilePath: r.FilePath,
		}
	}

	expanded := anchor.ExpandOneHop(m.idx, sectionResults, decay)

	// Sort by score descending.
	sort.Slice(expanded, func(i, j int) bool {
		return expanded[i].Score > expanded[j].Score
	})

	entries := make([]eval.ResultEntry, len(expanded))
	for i, sr := range expanded {
		entries[i] = eval.ResultEntry{
			FilePath:  sr.FilePath,
			SectionID: sr.Section.ID,
			StartLine: sr.Section.StartLine,
			EndLine:   sr.Section.EndLine,
			Score:     sr.Score,
			Rank:      i + 1,
		}
	}
	return entries
}

func (m *anchorMapper) BoostHeadings(query string, results []eval.ResultEntry, factor float64) []eval.ResultEntry {
	// Convert entries to SectionResults for HeadingBoost.
	sectionResults := make([]anchor.SectionResult, len(results))
	for i, r := range results {
		sectionResults[i] = anchor.SectionResult{
			Section: anchor.Section{
				ID:        r.SectionID,
				StartLine: r.StartLine,
				EndLine:   r.EndLine,
				FilePath:  r.FilePath,
			},
			Score:    r.Score,
			FilePath: r.FilePath,
		}
	}

	boosted := anchor.HeadingBoost(query, sectionResults, factor)

	// Sort by score descending.
	sort.Slice(boosted, func(i, j int) bool {
		return boosted[i].Score > boosted[j].Score
	})

	entries := make([]eval.ResultEntry, len(boosted))
	for i, sr := range boosted {
		entries[i] = eval.ResultEntry{
			FilePath:  sr.FilePath,
			SectionID: sr.Section.ID,
			StartLine: sr.Section.StartLine,
			EndLine:   sr.Section.EndLine,
			Score:     sr.Score,
			Rank:      i + 1,
		}
	}
	return entries
}

// evalReport holds the full eval output.
type evalReport struct {
	Runs     []*eval.RunResult          `json:"runs"`
	Metrics  []*eval.Metrics            `json:"metrics"`
	ByFamily map[string][]*eval.Metrics `json:"by_family,omitempty"`
}

func buildEvalReport(runs []*eval.RunResult) *evalReport {
	r := &evalReport{
		Runs:     runs,
		ByFamily: make(map[string][]*eval.Metrics),
	}

	for _, run := range runs {
		m := eval.ComputeMetrics(run)
		r.Metrics = append(r.Metrics, m)

		famMetrics := eval.ComputeMetricsByFamily(run)
		for fam, fm := range famMetrics {
			r.ByFamily[fam] = append(r.ByFamily[fam], fm)
		}
	}
	return r
}

func printEvalReport(w io.Writer, r *evalReport) {
	fmt.Fprintf(w, "\n=== Eval Report ===\n\n")

	// Overall metrics table.
	fmt.Fprintf(w, "%-20s %8s %8s %8s %8s %8s %10s %10s\n",
		"Variant", "Hit@1", "Hit@3", "SecH@1", "SecH@3", "MRR", "Med(ms)", "P95(ms)")
	fmt.Fprintf(w, "%-20s %8s %8s %8s %8s %8s %10s %10s\n",
		"-------", "-----", "-----", "------", "------", "---", "-------", "-------")

	for _, m := range r.Metrics {
		fmt.Fprintf(w, "%-20s %7.1f%% %7.1f%% %7.1f%% %7.1f%% %8.3f %9.1fms %9.1fms\n",
			m.Variant,
			m.HitAt1Rate*100,
			m.HitAt3Rate*100,
			m.SectionHitAt1*100,
			m.SectionHitAt3*100,
			m.MeanMRR,
			m.MedianLatencyMs,
			m.P95LatencyMs,
		)
	}

	// Noise metrics.
	fmt.Fprintf(w, "\n%-20s %12s %15s\n", "Variant", "WrongFile@3", "WrongSec/Same")
	fmt.Fprintf(w, "%-20s %12s %15s\n", "-------", "-----------", "-------------")
	for _, m := range r.Metrics {
		fmt.Fprintf(w, "%-20s %11.1f%% %14.1f%%\n",
			m.Variant,
			m.WrongFileTop3Rate*100,
			m.WrongSectionSameFile*100,
		)
	}

	// Per-family breakdown.
	if len(r.ByFamily) > 0 {
		fmt.Fprintf(w, "\n--- By Family ---\n")
		for fam, metrics := range r.ByFamily {
			fmt.Fprintf(w, "\n  %s:\n", fam)
			for _, m := range metrics {
				fmt.Fprintf(w, "    %-18s  Hit@1=%5.1f%%  Hit@3=%5.1f%%  MRR=%.3f  n=%d\n",
					m.Variant, m.HitAt1Rate*100, m.HitAt3Rate*100, m.MeanMRR, m.CaseCount)
			}
		}
	}

	fmt.Fprintln(w)
}
