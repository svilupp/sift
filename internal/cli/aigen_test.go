package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"sift/internal/aigen"
	"sift/internal/index"
)

func TestMakeIndexHookMissingMode(t *testing.T) {
	var pending []pendingFolder
	hook := makeIndexHook(genMissing, &pending)

	plan := &index.MaintainPlan{
		FolderPath: "/x",
		Next: &index.FolderIndex{
			Files: map[string]index.FileEntry{
				"a.md": {Summary: ""},
			},
		},
	}
	if err := hook("/x", plan); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending; got %d", len(pending))
	}

	pending = nil
	full := &index.MaintainPlan{
		FolderPath: "/y",
		Next: &index.FolderIndex{
			Purpose: "set",
			Files:   map[string]index.FileEntry{"a.md": {Summary: "S"}},
		},
	}
	_ = hook("/y", full)
	if len(pending) != 0 {
		t.Errorf("expected 0 pending for fully populated; got %d", len(pending))
	}
}

func TestMakeIndexHookStaleMode(t *testing.T) {
	var pending []pendingFolder
	hook := makeIndexHook(genStale, &pending)

	plan := &index.MaintainPlan{
		FolderPath:   "/x",
		PurposeStale: true,
		Next:         &index.FolderIndex{Files: map[string]index.FileEntry{"a.md": {}}},
	}
	_ = hook("/x", plan)
	if len(pending) != 1 {
		t.Fatalf("PurposeStale should add to queue; got %d", len(pending))
	}

	pending = nil
	plan2 := &index.MaintainPlan{
		FolderPath: "/y",
		FileChanges: []index.FileChange{
			{Decision: index.DecisionRegenerate},
		},
		Next: &index.FolderIndex{Files: map[string]index.FileEntry{"a.md": {}}},
	}
	_ = hook("/y", plan2)
	if len(pending) != 1 {
		t.Errorf("Regenerate change should enqueue; got %d", len(pending))
	}
}

func TestMakeIndexHookNoneMode(t *testing.T) {
	var pending []pendingFolder
	hook := makeIndexHook(genNone, &pending)
	plan := &index.MaintainPlan{Next: &index.FolderIndex{Files: map[string]index.FileEntry{"a.md": {}}}}
	_ = hook("/x", plan)
	if len(pending) != 0 {
		t.Errorf("none mode should never enqueue")
	}
}

// integration-style: build pending plans for a temp folder, run the
// generator with a stub, verify summaries land in sift.toml AND that a
// re-run with --generate=stale produces zero LLM calls.
func TestStickySummaryZeroCallsOnRerun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.md"), []byte("# A\nhello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.md"), []byte("# B\nfoo bar"), 0o644); err != nil {
		t.Fatal(err)
	}

	// First, run mechanical maintenance to populate sift.toml.
	plan, err := index.PlanAndApply(index.PlanInput{
		FolderPath: dir,
		Files: []index.ObservedFile{
			{RelPath: "a.md", Sig: mustSig(t, filepath.Join(dir, "a.md"))},
			{RelPath: "b.md", Sig: mustSig(t, filepath.Join(dir, "b.md"))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Build generator that counts calls.
	var calls atomic.Int32
	gen := &fakeGenerator{calls: &calls}

	// First run via runAigenForPlans-like flow: build batches and call
	// gen directly (we don't need the pool here).
	pending := []pendingFolder{{folder: dir, plan: plan}}
	for _, pf := range pending {
		batch, err := buildFileBatches(pf.folder, pf.plan, genMissing)
		if err != nil {
			t.Fatal(err)
		}
		if len(batch) != 2 {
			t.Fatalf("first run: want 2 batched; got %d", len(batch))
		}
		res, err := gen.GenerateFolder(context.Background(), aigen.GenerateInput{
			Folder: aigen.FolderContext{Path: dir, RelPath: "x"},
			Files:  batch,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := persistFolderSummaries(dir, pf.plan, res.Folder, nil); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 1 {
		t.Errorf("first run should have 1 LLM call; got %d", calls.Load())
	}

	// Second run: re-load plan; should treat both files as having
	// current_summary set, so the generator records 0 calls.
	plan2, err := index.PlanAndApply(index.PlanInput{
		FolderPath: dir,
		Prev:       loadIdx(t, dir),
		Files: []index.ObservedFile{
			{RelPath: "a.md", Sig: mustSig(t, filepath.Join(dir, "a.md"))},
			{RelPath: "b.md", Sig: mustSig(t, filepath.Join(dir, "b.md"))},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for rel, fe := range plan2.Next.Files {
		if fe.Summary == "" {
			t.Errorf("expected sticky summary on %s", rel)
		}
	}
	batch, err := buildFileBatches(dir, plan2, genStale)
	if err != nil {
		t.Fatal(err)
	}
	for _, fb := range batch {
		if fb.CurrentSummary == "" {
			t.Errorf("re-run batch missing CurrentSummary for %s", fb.Path)
		}
	}

	// Now invoke the generator (it uses sticky filtering) — must be 0 calls.
	prevCalls := calls.Load()
	res, err := aigen.NewFolderGeneratorFromGen(gen).GenerateFolder(context.Background(), aigen.GenerateInput{
		Folder: aigen.FolderContext{Path: dir},
		Files:  batch,
	})
	_ = res
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != prevCalls {
		t.Errorf("sticky re-run should make 0 NEW calls; before=%d after=%d", prevCalls, calls.Load())
	}
}

func mustSig(t *testing.T, path string) index.FileSig {
	t.Helper()
	sig, err := index.SigOf(path)
	if err != nil {
		t.Fatal(err)
	}
	return sig
}

func loadIdx(t *testing.T, folder string) *index.FolderIndex {
	t.Helper()
	idx, ok, err := index.LoadOrZero(folder)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		return nil
	}
	return idx
}

// fakeGenerator returns canned summaries for any input.
type fakeGenerator struct {
	calls *atomic.Int32
}

func (f *fakeGenerator) GenerateFolder(ctx context.Context, in aigen.GenerateInput) (*aigen.GenerateResult, error) {
	f.calls.Add(1)
	files := []aigen.FileSummary{}
	for _, fb := range in.Files {
		files = append(files, aigen.FileSummary{Path: fb.Path, Summary: "Summary for " + fb.Path})
	}
	body, _ := json.Marshal(map[string]any{"files": files})
	_ = body
	return &aigen.GenerateResult{
		Folder: &aigen.FolderResult{Purpose: "Auto-generated.", Files: files},
		Calls:  []aigen.CallStats{{TokensIn: 50, TokensOut: 25, Attempts: 1}},
	}, nil
}

// TestMakeIndexHookMissingMode_BackfillsUnchangedPlan verifies the bug
// fix: when a mechanical refresh already wrote sift.toml (so
// plan.Changed will be false on the next run) but Purpose/Summary
// fields remained empty, --generate=missing must still enqueue the
// folder. The hook itself does not look at plan.Changed; it inspects
// the FolderIndex content.
func TestMakeIndexHookMissingMode_BackfillsUnchangedPlan(t *testing.T) {
	var pending []pendingFolder
	hook := makeIndexHook(genMissing, &pending)

	// plan.Changed == false (mechanical idempotent) but Purpose is
	// empty and the file has no Summary — this is the exact state the
	// bug left behind.
	plan := &index.MaintainPlan{
		FolderPath: "/x",
		Changed:    false,
		Next: &index.FolderIndex{
			Purpose: "",
			Files: map[string]index.FileEntry{
				"a.md": {Summary: ""},
			},
		},
	}
	if err := hook("/x", plan); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Fatalf("expected 1 pending despite plan.Changed=false; got %d", len(pending))
	}
}

// TestMakeIndexHookMissingMode_NoopOnFullyPopulated verifies the
// no-op scenario: an unchanged tree whose folders already have
// Purpose and per-file Summary set must not enqueue anything, even
// though the hook is invoked.
func TestMakeIndexHookMissingMode_NoopOnFullyPopulated(t *testing.T) {
	var pending []pendingFolder
	hook := makeIndexHook(genMissing, &pending)

	plan := &index.MaintainPlan{
		FolderPath: "/y",
		Changed:    false,
		Next: &index.FolderIndex{
			Purpose: "All set.",
			Files: map[string]index.FileEntry{
				"a.md": {Summary: "S"},
				"b.md": {Summary: "T"},
			},
		},
	}
	if err := hook("/y", plan); err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("expected 0 pending on fully populated unchanged folder; got %d", len(pending))
	}
}

// _ keeps strings imported in case tests grow.
var _ = strings.Contains
