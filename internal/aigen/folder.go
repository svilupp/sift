package aigen

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// FolderGenerator wires a Client into the Generator interface used by
// the worker pool. Construct via NewFolderGenerator.
type FolderGenerator struct {
	client *Client
	// SubBatchSize overrides the default N=10. Zero = default.
	SubBatchSize int
	// SkipSticky, when true, asks the model about every file even if
	// CurrentSummary is supplied. Default false (sticky preserves
	// summaries when content unchanged).
	SkipSticky bool
}

// NewFolderGenerator wraps a Client with the orchestration logic for
// single-batch and partitioned folder generation.
func NewFolderGenerator(c *Client) *FolderGenerator {
	return &FolderGenerator{client: c}
}

// stickyFilter is a Generator wrapper that performs the sticky-summary
// short-circuit (skip the LLM when CurrentSummary is set) and
// delegates the rest to an inner Generator. Useful for tests that
// supply a stub backend.
type stickyFilter struct {
	inner Generator
}

// NewFolderGeneratorFromGen wraps an arbitrary Generator with the
// same sticky-summary filter that FolderGenerator uses for its real
// path. Callers in tests should prefer this when they only have a
// stub Generator and don't want to spin up an HTTP test server.
func NewFolderGeneratorFromGen(g Generator) Generator {
	return &stickyFilter{inner: g}
}

func (s *stickyFilter) GenerateFolder(ctx context.Context, in GenerateInput) (*GenerateResult, error) {
	carried := map[string]string{}
	var pending []FileBatch
	for _, f := range in.Files {
		if strings.TrimSpace(f.CurrentSummary) != "" && f.CurrentSummary != "__regen__" {
			carried[f.Path] = strings.TrimSpace(f.CurrentSummary)
			continue
		}
		pending = append(pending, f)
	}
	if len(pending) == 0 {
		fr := &FolderResult{
			Purpose: in.Folder.ExistingPurpose,
			UseWhen: in.Folder.ExistingUseWhen,
		}
		for _, f := range in.Files {
			fr.Files = append(fr.Files, FileSummary{
				Path:    f.Path,
				Summary: carried[f.Path],
			})
		}
		return &GenerateResult{Folder: fr}, nil
	}
	res, err := s.inner.GenerateFolder(ctx, GenerateInput{
		Folder:      in.Folder,
		Files:       pending,
		Order:       in.Order,
		EmitWarning: in.EmitWarning,
	})
	if err != nil {
		return res, err
	}
	if res != nil && res.Folder != nil {
		mergeCarried(res.Folder, carried, in.Files)
	}
	return res, nil
}

// GenerateFolder runs the orchestration for one folder. Per-file
// fallback fires after two consecutive path-set mismatches in a
// sub-batch.
func (g *FolderGenerator) GenerateFolder(ctx context.Context, in GenerateInput) (*GenerateResult, error) {
	if g == nil || g.client == nil {
		return nil, fmt.Errorf("aigen: nil generator")
	}
	if len(in.Files) == 0 {
		return &GenerateResult{Folder: &FolderResult{
			Purpose: in.Folder.ExistingPurpose,
			UseWhen: in.Folder.ExistingUseWhen,
		}}, nil
	}

	// Sticky filtering: any file with CurrentSummary set is "carried"
	// without querying the LLM. Caller decides what counts as
	// "unchanged" (typically: content_hash matches).
	carried := map[string]string{}
	var pending []FileBatch
	for _, f := range in.Files {
		if !g.SkipSticky && strings.TrimSpace(f.CurrentSummary) != "" && f.CurrentSummary != "__regen__" {
			carried[f.Path] = strings.TrimSpace(f.CurrentSummary)
			continue
		}
		pending = append(pending, f)
	}

	res := &GenerateResult{Folder: &FolderResult{}}

	// If nothing remains to ask about, return the carried summaries
	// with the existing folder text. Pure sticky path: 0 LLM calls.
	if len(pending) == 0 {
		fr := &FolderResult{
			Purpose: in.Folder.ExistingPurpose,
			UseWhen: in.Folder.ExistingUseWhen,
		}
		for _, f := range in.Files {
			fr.Files = append(fr.Files, FileSummary{
				Path:    f.Path,
				Summary: carried[f.Path],
			})
		}
		finalizeResult(fr, in.Folder, pending, in.EmitWarning)
		res.Folder = fr
		return res, nil
	}

	// Partition decision.
	if !NeedsPartition(pending) {
		fr, calls, err := g.singleBatch(ctx, in.Folder, pending)
		res.Calls = append(res.Calls, calls...)
		if err != nil {
			return res, err
		}
		mergeCarried(fr, carried, in.Files)
		finalizeResult(fr, in.Folder, pending, in.EmitWarning)
		res.Folder = fr
		return res, nil
	}

	// Partitioned path: sub-batches sequentially, then synthesis.
	subBatches := Partition(pending, g.subBatchSize(), in.Order)
	type partial struct {
		summaries []FileSummary
		purpose   string
	}
	var partials []partial

	for _, sb := range subBatches {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		// Inherit folder context but treat the parent purpose as the
		// existing purpose for the model so it sees the running picture.
		fc := in.Folder
		fr, calls, err := g.singleBatch(ctx, fc, sb.Files)
		res.Calls = append(res.Calls, calls...)
		if err != nil {
			return res, err
		}
		partials = append(partials, partial{summaries: fr.Files, purpose: fr.Purpose})
	}

	// Synthesis: combine per-batch purposes into a folder-level purpose.
	allSummaries := []FileSummary{}
	purposeNotes := []string{}
	for _, p := range partials {
		allSummaries = append(allSummaries, p.summaries...)
		if t := strings.TrimSpace(p.purpose); t != "" {
			purposeNotes = append(purposeNotes, t)
		}
	}

	synthesized, calls, err := g.synthesizePurpose(ctx, in.Folder, purposeNotes, allSummaries)
	res.Calls = append(res.Calls, calls...)
	finalPurpose := strings.TrimSpace(synthesized)
	if err != nil || finalPurpose == "" {
		// Fallback: keep the last sub-batch's purpose, or the existing.
		if len(purposeNotes) > 0 {
			finalPurpose = purposeNotes[len(purposeNotes)-1]
		} else {
			finalPurpose = in.Folder.ExistingPurpose
		}
	}
	fr := &FolderResult{
		Purpose: finalPurpose,
		UseWhen: in.Folder.ExistingUseWhen,
		Files:   allSummaries,
	}
	mergeCarried(fr, carried, in.Files)
	finalizeResult(fr, in.Folder, pending, in.EmitWarning)
	res.Folder = fr
	return res, nil
}

// finalizeResult applies post-parse safety nets to the FolderResult
// before it is returned to the caller / persisted to sift.toml:
//
//  1. TruncateSummaries: enforces the per-file summary length cap
//     (prompt asks for ≤200 chars but ~5% of outputs spill over).
//  2. CheckPurposeGrounding: warns when the model's `purpose` text
//     mentions proper nouns / IDs / dates that do not appear in any
//     supplied file content.
//
// When emit is non-nil each diagnostic is forwarded via the callback
// (used by the worker pool to surface PoolEvents). Otherwise we fall
// back to slog logging (component="aigen") so direct callers — like
// the experiments harness that bypasses the pool — still see the
// signal in logs. Both paths are kept active when emit is non-nil so
// that tail-following stderr remains useful even with the structured
// stream.
//
// kind is one of "grounding_warning" or "summary_truncated".
func finalizeResult(fr *FolderResult, fc FolderContext, batches []FileBatch, emit func(folder, kind, message string)) {
	if fr == nil {
		return
	}
	logger := slog.Default().With(slog.String("component", "aigen"))
	if n := TruncateSummaries(fr, MaxFileSummaryChars); n > 0 {
		msg := fmt.Sprintf("truncated %d file summaries over %d-char cap", n, MaxFileSummaryChars)
		logger.Info("truncated file summaries over cap",
			slog.String("folder", fc.RelPath),
			slog.Int("count", n),
			slog.Int("max_chars", MaxFileSummaryChars))
		if emit != nil {
			emit(fc.RelPath, string(EventSummaryTruncated), msg)
		}
	}
	if warnings := CheckPurposeGrounding(*fr, batches, fc.Path); len(warnings) > 0 {
		for _, w := range warnings {
			logger.Warn("grounding_warning",
				slog.String("folder", fc.RelPath),
				slog.String("warning", w))
			if emit != nil {
				emit(fc.RelPath, string(EventGroundingWarning), w)
			}
		}
	}
}

func (g *FolderGenerator) subBatchSize() int {
	if g.SubBatchSize > 0 {
		return g.SubBatchSize
	}
	return SubBatchSizeDefault
}

// singleBatch runs one LLM call for the supplied files, then path-set
// validates. On mismatch it retries once with an explicit reminder; on
// second mismatch it falls back to per-file generation.
//
// Returns the parsed result, the per-call stats accumulated, and any
// error encountered.
func (g *FolderGenerator) singleBatch(ctx context.Context, fc FolderContext, files []FileBatch) (*FolderResult, []CallStats, error) {
	var stats []CallStats
	wantPaths := pathsOf(files)

	var lastReason string
	for attempt := 1; attempt <= 2; attempt++ {
		userMsg := BuildFolderPrompt(PromptInput{Folder: fc, Files: files})
		if attempt == 2 && lastReason != "" {
			userMsg = userMsg + "\nNote: previous response was rejected (" + lastReason + "). " +
				"Output exactly one entry per file path listed, no extras, no omissions, " +
				"and include `use_when` as a non-empty array of at least 1 distinct cue.\n"
		}
		resp, err := g.client.Call(ctx, CallRequest{
			System: SystemPrompt(),
			User:   userMsg,
			Strict: true,
		})
		if resp != nil {
			stats = append(stats, resp.Stats)
		}
		if err != nil {
			return nil, stats, err
		}
		fr, perr := ParseFolderResponse(resp.Content)
		if perr != nil {
			lastReason = "parse error"
			if attempt == 2 {
				return g.perFileFallback(ctx, fc, files, stats)
			}
			continue
		}
		if verr := ValidatePathSet(wantPaths, fr); verr != nil {
			lastReason = "path-set mismatch"
			if attempt == 2 {
				return g.perFileFallback(ctx, fc, files, stats)
			}
			continue
		}
		if verr := ValidateUseWhen(fr); verr != nil {
			lastReason = verr.Error()
			if attempt == 2 {
				// Second attempt also missing use_when: surface as a
				// retryable folder error so the pool dead-letters with a
				// clear message rather than persisting a sift.toml with
				// no retrieval cues.
				return nil, stats, fmt.Errorf("use_when validation failed after retry: %w", verr)
			}
			continue
		}
		return fr, stats, nil
	}
	return g.perFileFallback(ctx, fc, files, stats)
}

// perFileFallback runs one LLM call per file. Used when a sub-batch
// fails JSON parse twice or the path-set keeps drifting.
func (g *FolderGenerator) perFileFallback(ctx context.Context, fc FolderContext, files []FileBatch, prior []CallStats) (*FolderResult, []CallStats, error) {
	stats := prior
	fr := &FolderResult{}
	for _, f := range files {
		single := []FileBatch{f}
		userMsg := BuildFolderPrompt(PromptInput{Folder: fc, Files: single})
		resp, err := g.client.Call(ctx, CallRequest{
			System: SystemPrompt(),
			User:   userMsg,
			Strict: true,
		})
		if resp != nil {
			stats = append(stats, resp.Stats)
		}
		if err != nil {
			return fr, stats, err
		}
		parsed, perr := ParseFolderResponse(resp.Content)
		if perr != nil {
			return fr, stats, fmt.Errorf("per-file %s: %w", f.Path, perr)
		}
		if len(parsed.Files) == 0 {
			return fr, stats, fmt.Errorf("per-file %s: empty response", f.Path)
		}
		// Take the first entry, force the path back to f.Path.
		entry := parsed.Files[0]
		entry.Path = f.Path
		fr.Files = append(fr.Files, entry)
		if fr.Purpose == "" {
			fr.Purpose = parsed.Purpose
		}
	}
	return fr, stats, nil
}

// synthesizePurpose drafts a folder-level purpose from collected
// per-file summaries plus partial purposes. One short LLM call.
func (g *FolderGenerator) synthesizePurpose(ctx context.Context, fc FolderContext, partials []string, summaries []FileSummary) (string, []CallStats, error) {
	var b strings.Builder
	if rp := fc.RelPath; rp != "" {
		fmt.Fprintf(&b, "Folder: %s\n", rp)
	}
	if fc.ExistingPurpose != "" {
		fmt.Fprintf(&b, "Existing purpose: %s\n", oneLine(fc.ExistingPurpose))
	}
	if len(partials) > 0 {
		b.WriteString("Per-batch drafts:\n")
		for _, p := range partials {
			fmt.Fprintf(&b, "- %s\n", oneLine(p))
		}
	}
	b.WriteString("Per-file summaries:\n")
	for _, s := range summaries {
		fmt.Fprintf(&b, "- %s: %s\n", s.Path, oneLine(s.Summary))
	}
	b.WriteString("\nWrite ONE folder-level `purpose` paragraph (1-3 sentences, ≤400 chars) ")
	b.WriteString("that captures what this folder is for as a whole. Functional voice. ")
	b.WriteString("Output only the purpose text, no JSON, no preamble.\n")

	resp, err := g.client.Call(ctx, CallRequest{
		System: "You write terse, accurate folder-purpose paragraphs. No slop, no preamble.",
		User:   b.String(),
	})
	stats := []CallStats{}
	if resp != nil {
		stats = append(stats, resp.Stats)
	}
	if err != nil {
		return "", stats, err
	}
	return strings.TrimSpace(resp.Content), stats, nil
}

func pathsOf(files []FileBatch) []string {
	out := make([]string, len(files))
	for i, f := range files {
		out[i] = f.Path
	}
	return out
}

// mergeCarried inserts sticky-summary entries the LLM never saw, and
// orders the file list to match the input order.
func mergeCarried(fr *FolderResult, carried map[string]string, original []FileBatch) {
	if fr == nil {
		return
	}
	have := map[string]FileSummary{}
	for _, f := range fr.Files {
		have[f.Path] = f
	}
	merged := make([]FileSummary, 0, len(original))
	for _, f := range original {
		if entry, ok := have[f.Path]; ok {
			merged = append(merged, entry)
			continue
		}
		if s, ok := carried[f.Path]; ok {
			merged = append(merged, FileSummary{Path: f.Path, Summary: s})
		}
	}
	fr.Files = merged
}
