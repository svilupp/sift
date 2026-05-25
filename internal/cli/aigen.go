package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"sift/internal/aigen"
	"sift/internal/db"
	"sift/internal/index"
)

// generationMode controls when LLM summaries get generated.
type generationMode string

const (
	genNone    generationMode = "none"
	genMissing generationMode = "missing"
	genStale   generationMode = "stale"
	genAll     generationMode = "all"
)

// aigenRunOptions bundles all knobs the CLI passes to the aigen pool.
type aigenRunOptions struct {
	Mode           generationMode
	Concurrency    int
	APIKey         string
	BaseURL        string
	Model          string
	Database       *db.DB
	CollectionRoot string
	CollectionName string
	Progress       string // "auto"|"json"|"none"
	Out            io.Writer
	Errs           io.Writer
}

// collectGenerationJobs walks the maintain plans and returns one Job
// per folder that needs LLM work. The jobs use the absolute folder
// path; per-file payloads are derived from the live FolderIndex.
//
// `--generate=missing`: a folder enters the queue only if at least one
// file or the folder purpose is empty.
// `--generate=stale`: enter if a freshness signal flagged it
// (DecisionRegenerate, DecisionNew, or PurposeStale).
// `--generate=all`: every folder regenerates.
type pendingFolder struct {
	folder string
	plan   *index.MaintainPlan
}

// makeIndexHook returns a sync.RefreshOptions.IndexHook that batches
// folders for AI generation.
func makeIndexHook(mode generationMode, sink *[]pendingFolder) func(folder string, plan *index.MaintainPlan) error {
	return func(folder string, plan *index.MaintainPlan) error {
		if mode == genNone || plan == nil || plan.Next == nil {
			return nil
		}
		needs := false
		switch mode {
		case genAll:
			needs = len(plan.Next.Files) > 0
		case genMissing:
			if strings.TrimSpace(plan.Next.Purpose) == "" && len(plan.Next.Files) > 0 {
				needs = true
			}
			for _, fe := range plan.Next.Files {
				if strings.TrimSpace(fe.Summary) == "" {
					needs = true
					break
				}
			}
		case genStale:
			if plan.PurposeStale {
				needs = true
			}
			for _, ch := range plan.FileChanges {
				if ch.Decision == index.DecisionNew || ch.Decision == index.DecisionRegenerate {
					needs = true
					break
				}
			}
		}
		if !needs {
			return nil
		}
		*sink = append(*sink, pendingFolder{folder: folder, plan: plan})
		return nil
	}
}

// runAigenForPlans builds aigen Jobs from the supplied plans, runs the
// pool, and persists folder summaries by re-loading + Saving each
// `sift.toml` after the LLM responds.
func runAigenForPlans(ctx context.Context, pending []pendingFolder, opts aigenRunOptions) error {
	if opts.Mode == genNone || len(pending) == 0 {
		return nil
	}
	if opts.APIKey == "" {
		return fmt.Errorf("aigen: missing API key (set DEEPINFRA_API_KEY or pass --api-key)")
	}

	client := aigen.NewClient(opts.APIKey, opts.BaseURL)
	if opts.Model != "" {
		client.SetModel(opts.Model)
	}
	gen := aigen.NewFolderGenerator(client)

	jobs := make([]aigen.Job, 0, len(pending))
	for _, pf := range pending {
		batch, err := buildFileBatches(pf.folder, pf.plan, opts.Mode)
		if err != nil {
			fmt.Fprintf(opts.Errs, "Warning: build batch for %s: %v\n", pf.folder, err)
			continue
		}
		if len(batch) == 0 {
			continue
		}
		fc := aigen.FolderContext{
			Path:            pf.folder,
			RelPath:         relativeIfPossible(pf.folder, opts.CollectionRoot),
			ExistingPurpose: pf.plan.Next.Purpose,
			ExistingUseWhen: append([]string(nil), pf.plan.Next.UseWhen...),
		}
		folder := pf.folder
		plan := pf.plan
		dbase := opts.Database
		jobs = append(jobs, aigen.Job{
			Folder: fc,
			Files:  batch,
			OnDone: func(ctx context.Context, j aigen.Job, fr *aigen.FolderResult) error {
				return persistFolderSummaries(folder, plan, fr, dbase)
			},
		})
	}

	if len(jobs) == 0 {
		return nil
	}

	concurrency := opts.Concurrency
	if concurrency <= 0 {
		concurrency = 20
	}

	events := aigen.RunPool(ctx, jobs, aigen.PoolOptions{
		Concurrency:       concurrency,
		Generator:         gen,
		HeartbeatInterval: 30 * time.Second,
		Collection:        opts.CollectionName,
	})

	emitProgress(events, opts)
	return nil
}

// emitProgress prints either NDJSON (--progress=json) or a
// human-friendly trailing summary (--progress=auto/none).
func emitProgress(events <-chan aigen.PoolEvent, opts aigenRunOptions) {
	out := opts.Out
	errs := opts.Errs
	jsonMode := opts.Progress == "json"
	silent := opts.Progress == "none"

	enc := json.NewEncoder(out)
	var summary *aigen.RunSummary
	deadLetters := 0
	for ev := range events {
		switch ev.Kind {
		case aigen.EventSummary:
			summary = ev.Summary
		case aigen.EventFolderError:
			if ev.DeadLettered {
				deadLetters++
			}
			if !jsonMode && !silent {
				fmt.Fprintf(errs, "  index: error %s: %s\n", ev.Path, ev.Err)
			}
		}
		if jsonMode {
			_ = enc.Encode(ev)
		}
	}
	if jsonMode || silent || summary == nil {
		return
	}
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "sift refresh --index summary")
	fmt.Fprintf(out, "  collection: %s        wallclock: %s        concurrency: %d\n",
		nz(summary.Collection, "(all)"),
		formatRunSeconds(summary.WallSeconds),
		summary.Concurrency)
	fmt.Fprintf(out, "  folders:    %d ok        %d failed (dead-lettered: %d)\n",
		summary.FoldersDone, summary.FoldersFailed, summary.FoldersDeadLettered)
	fmt.Fprintf(out, "  calls:      %d total     %d retried\n",
		summary.CallsTotal, summary.CallsRetried)
	fmt.Fprintf(out, "  cost:       $%.4f       tokens: %d in / %d out\n",
		summary.CostUSD, summary.TokensIn, summary.TokensOut)
	fmt.Fprintf(out, "  latency:    p50 %dms    p95 %dms\n",
		summary.LatencyMsP50, summary.LatencyMsP95)
	if summary.Interrupted {
		fmt.Fprintln(out, "  status:     interrupted")
	}
	if len(summary.DeadLetterPaths) > 0 {
		fmt.Fprintln(out, "")
		fmt.Fprintf(out, "  %d folder(s) dead-lettered. Resume with:\n", len(summary.DeadLetterPaths))
		fmt.Fprintln(out, "    sift config retry-dead-letters")
	}
}

func nz(s, fallback string) string {
	if strings.TrimSpace(s) == "" {
		return fallback
	}
	return s
}

func formatRunSeconds(sec float64) string {
	d := time.Duration(sec * float64(time.Second))
	if d < time.Minute {
		return fmt.Sprintf("%.1fs", sec)
	}
	mins := int(d.Minutes())
	rem := int(d.Seconds()) - mins*60
	return fmt.Sprintf("%dm %ds", mins, rem)
}

// buildFileBatches reads each file in the folder once to populate
// HeadWords/TailWords/CurrentSummary. Skips entries whose summary is
// already present unless mode==all.
func buildFileBatches(folder string, plan *index.MaintainPlan, mode generationMode) ([]aigen.FileBatch, error) {
	if plan == nil || plan.Next == nil {
		return nil, nil
	}
	out := make([]aigen.FileBatch, 0, len(plan.Next.Files))
	for rel, entry := range plan.Next.Files {
		if entry.Ignore {
			continue
		}
		full := filepath.Join(folder, rel)
		head, tail, err := readHeadTail(full, index.HeadTailWindow)
		if err != nil {
			// Skip unreadable files silently — caller logs at top-level.
			continue
		}
		info, statErr := os.Stat(full)
		bytes := int64(0)
		if statErr == nil {
			bytes = info.Size()
		}
		fb := aigen.FileBatch{
			Path:           rel,
			Words:          entry.Words,
			Bytes:          bytes,
			HeadWords:      head,
			TailWords:      tail,
			CurrentSummary: entry.Summary,
		}
		// `--generate=all` forces re-summarization → blank current.
		if mode == genAll {
			fb.CurrentSummary = "__regen__"
		}
		out = append(out, fb)
	}
	return out, nil
}

// readHeadTail returns the joined first and last `window` words of the
// file. Smaller than window → both equal full content.
func readHeadTail(path string, window int) (string, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	words := strings.Fields(string(data))
	if len(words) <= window {
		joined := strings.Join(words, " ")
		return joined, joined, nil
	}
	head := strings.Join(words[:window], " ")
	tail := strings.Join(words[len(words)-window:], " ")
	return head, tail, nil
}

// persistFolderSummaries reloads the folder's `sift.toml`, merges in
// the LLM's purpose/use_when/file summaries, and writes back via
// index.Save (atomic temp+rename per index/writer.go).
func persistFolderSummaries(folder string, plan *index.MaintainPlan, fr *aigen.FolderResult, dbase *db.DB) error {
	if fr == nil {
		return fmt.Errorf("nil folder result")
	}
	idx, ok, err := index.LoadOrZero(folder)
	if err != nil {
		return fmt.Errorf("reload sift.toml: %w", err)
	}
	if !ok || idx == nil {
		idx = plan.Next
	}
	if t := strings.TrimSpace(fr.Purpose); t != "" {
		idx.Purpose = t
	}
	if len(fr.UseWhen) > 0 && len(idx.UseWhen) == 0 {
		idx.UseWhen = append([]string(nil), fr.UseWhen...)
	}
	for _, fs := range fr.Files {
		if fs.Path == "" || strings.TrimSpace(fs.Summary) == "" {
			continue
		}
		entry, ok := idx.Files[fs.Path]
		if !ok {
			continue
		}
		entry.Summary = strings.TrimSpace(fs.Summary)
		idx.Files[fs.Path] = entry
	}
	if err := index.Save(filepath.Join(folder, index.FilenameSiftToml), idx); err != nil {
		return fmt.Errorf("save sift.toml: %w", err)
	}
	_ = dbase // reserved for future telemetry hooks
	return nil
}

// relativeIfPossible returns rel(p, root) if root is a prefix; else p.
func relativeIfPossible(p, root string) string {
	if root == "" {
		return p
	}
	rp, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	if strings.HasPrefix(rp, "..") {
		return p
	}
	return rp
}
