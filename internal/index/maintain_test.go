package index

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// makeSig returns a deterministic FileSig for a content string.
func makeSig(t *testing.T, body string) FileSig {
	t.Helper()
	return ComputeSig([]byte(body))
}

// uniformWords builds a body of n copies of the given token, joined by
// single spaces. Stable for hashing.
func uniformWords(token string, n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = token
	}
	return strings.Join(parts, " ")
}

func TestPlan_NewFolderCreatesIndex(t *testing.T) {
	t.Helper()
	dir := t.TempDir()

	in := PlanInput{
		FolderPath: dir,
		Prev:       nil,
		Files: []ObservedFile{
			{RelPath: "alpha.md", Sig: makeSig(t, uniformWords("alpha", 600))},
			{RelPath: "beta.md", Sig: makeSig(t, uniformWords("beta", 200))},
		},
	}
	plan, err := Plan(in)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.Created {
		t.Fatalf("expected Created=true for new folder")
	}
	if !plan.Changed {
		t.Fatalf("expected Changed=true on first plan")
	}
	if !plan.PurposeStale {
		t.Fatalf("expected PurposeStale=true for brand-new folder with content")
	}
	if got := len(plan.FileChanges); got != 2 {
		t.Fatalf("FileChanges count = %d, want 2", got)
	}
	for _, ch := range plan.FileChanges {
		if ch.Decision != DecisionNew {
			t.Fatalf("decision for %s = %s want new", ch.RelPath, ch.Decision)
		}
	}
	if plan.Next.Refresh.FileCount != 2 {
		t.Fatalf("FileCount = %d", plan.Next.Refresh.FileCount)
	}
}

func TestPlan_IdempotentSecondRun(t *testing.T) {
	dir := t.TempDir()
	files := []ObservedFile{
		{RelPath: "alpha.md", Sig: makeSig(t, uniformWords("alpha", 600))},
		{RelPath: "beta.md", Sig: makeSig(t, uniformWords("beta", 200))},
	}

	plan, err := PlanAndApply(PlanInput{FolderPath: dir, Files: files})
	if err != nil {
		t.Fatalf("first PlanAndApply: %v", err)
	}
	if !plan.Changed {
		t.Fatalf("first plan should write")
	}
	tomlPath := filepath.Join(dir, FilenameSiftToml)
	infoBefore, err := os.Stat(tomlPath)
	if err != nil {
		t.Fatalf("stat after first apply: %v", err)
	}

	prev, _, err := LoadOrZero(dir)
	if err != nil {
		t.Fatalf("LoadOrZero: %v", err)
	}
	plan2, err := Plan(PlanInput{FolderPath: dir, Prev: prev, Files: files})
	if err != nil {
		t.Fatalf("second Plan: %v", err)
	}
	if plan2.Changed {
		t.Fatalf("second plan should be no-op (Changed=true)")
	}
	if err := Apply(plan2); err != nil {
		t.Fatalf("second Apply: %v", err)
	}
	infoAfter, err := os.Stat(tomlPath)
	if err != nil {
		t.Fatalf("stat after second apply: %v", err)
	}
	if !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		t.Fatalf("idempotency violated: file mtime changed (before=%v after=%v)",
			infoBefore.ModTime(), infoAfter.ModTime())
	}
}

func TestPlan_StickySummaryPreserved(t *testing.T) {
	body := uniformWords("hello", 1200)
	sig := makeSig(t, body)
	prev := &FolderIndex{
		SchemaVersion: SchemaVersion,
		Files: map[string]FileEntry{
			"doc.md": sig.AsEntry(FileEntry{Summary: "Sticky summary should survive."}),
		},
		Folders: map[string]ChildFolder{},
	}

	plan, err := Plan(PlanInput{
		FolderPath: t.TempDir(),
		Prev:       prev,
		Files:      []ObservedFile{{RelPath: "doc.md", Sig: sig}},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := plan.Next.Files["doc.md"].Summary; got != "Sticky summary should survive." {
		t.Fatalf("summary lost: %q", got)
	}
	if len(plan.FileChanges) != 1 || plan.FileChanges[0].Decision != DecisionNoop {
		t.Fatalf("expected single noop decision, got %+v", plan.FileChanges)
	}
}

func TestPlan_MechanicalEditPreservesSummary(t *testing.T) {
	prevBody := uniformWords("body", 1200)
	prevSig := makeSig(t, prevBody)
	prev := &FolderIndex{
		SchemaVersion: SchemaVersion,
		Files: map[string]FileEntry{
			"doc.md": prevSig.AsEntry(FileEntry{Summary: "Original blurb."}),
		},
		Folders: map[string]ChildFolder{},
	}
	// Change one middle word — head/tail windows stay stable, words
	// unchanged → DecisionUpdateMechanical.
	words := strings.Fields(prevBody)
	words[600] = "muTaTeD"
	curSig := makeSig(t, strings.Join(words, " "))
	if curSig.ContentHash == prevSig.ContentHash {
		t.Fatalf("test bug: mutation produced identical hash")
	}

	plan, err := Plan(PlanInput{
		FolderPath: t.TempDir(),
		Prev:       prev,
		Files:      []ObservedFile{{RelPath: "doc.md", Sig: curSig}},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.FileChanges[0].Decision != DecisionUpdateMechanical {
		t.Fatalf("decision = %s, want update_mechanical", plan.FileChanges[0].Decision)
	}
	if got := plan.Next.Files["doc.md"].Summary; got != "Original blurb." {
		t.Fatalf("summary not preserved on mechanical update: %q", got)
	}
	if !plan.Changed {
		t.Fatalf("expected Changed=true on mechanical update")
	}
}

func TestPlan_RegenerateDropsSummary(t *testing.T) {
	prevBody := uniformWords("body", 1200)
	prevSig := makeSig(t, prevBody)
	prev := &FolderIndex{
		SchemaVersion: SchemaVersion,
		Files: map[string]FileEntry{
			"doc.md": prevSig.AsEntry(FileEntry{Summary: "Original blurb."}),
		},
		Folders: map[string]ChildFolder{},
	}
	// Rewrite the intro entirely → head_hash flips → regenerate.
	words := strings.Fields(prevBody)
	for i := 0; i < 100; i++ {
		words[i] = "newIntro"
	}
	curSig := makeSig(t, strings.Join(words, " "))

	plan, err := Plan(PlanInput{
		FolderPath: t.TempDir(),
		Prev:       prev,
		Files:      []ObservedFile{{RelPath: "doc.md", Sig: curSig}},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.FileChanges[0].Decision != DecisionRegenerate {
		t.Fatalf("decision = %s, want regenerate", plan.FileChanges[0].Decision)
	}
	if got := plan.Next.Files["doc.md"].Summary; got != "" {
		t.Fatalf("summary should be cleared on regenerate; got %q", got)
	}
}

func TestPlan_DeletedFileDropsEntry(t *testing.T) {
	body := uniformWords("hi", 1200)
	prevSig := makeSig(t, body)
	prev := &FolderIndex{
		SchemaVersion: SchemaVersion,
		Files: map[string]FileEntry{
			"doc.md": prevSig.AsEntry(FileEntry{Summary: "Was here."}),
		},
		Folders: map[string]ChildFolder{},
	}
	plan, err := Plan(PlanInput{FolderPath: t.TempDir(), Prev: prev, Files: nil})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, ok := plan.Next.Files["doc.md"]; ok {
		t.Fatalf("deleted file should be removed from Next.Files")
	}
	if len(plan.FileChanges) != 1 || plan.FileChanges[0].Decision != DecisionDeleted {
		t.Fatalf("expected single deleted change, got %+v", plan.FileChanges)
	}
}

func TestPlan_PreservesPurposeAndUseWhen(t *testing.T) {
	body := uniformWords("hi", 1200)
	sig := makeSig(t, body)
	prev := &FolderIndex{
		SchemaVersion: SchemaVersion,
		Purpose:       "Architecture decisions for the X workstream.",
		UseWhen:       []string{"design tradeoffs", "rollout"},
		Files: map[string]FileEntry{
			"doc.md": sig.AsEntry(FileEntry{Summary: "Already summarized."}),
		},
		Folders: map[string]ChildFolder{},
	}
	plan, err := Plan(PlanInput{
		FolderPath: t.TempDir(),
		Prev:       prev,
		Files:      []ObservedFile{{RelPath: "doc.md", Sig: sig}},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Next.Purpose != prev.Purpose {
		t.Fatalf("Purpose mutated: %q vs %q", plan.Next.Purpose, prev.Purpose)
	}
	if len(plan.Next.UseWhen) != 2 || plan.Next.UseWhen[0] != "design tradeoffs" {
		t.Fatalf("UseWhen mutated: %+v", plan.Next.UseWhen)
	}
	if plan.PurposeStale {
		t.Fatalf("PurposeStale should be false on stable folder")
	}
}

func TestPlan_PurposeStaleWhenChurnHigh(t *testing.T) {
	body := uniformWords("hi", 1200)
	sig := makeSig(t, body)
	prev := &FolderIndex{
		SchemaVersion: SchemaVersion,
		Purpose:       "Old purpose.",
		Files: map[string]FileEntry{
			"a.md": sig.AsEntry(FileEntry{}),
			"b.md": sig.AsEntry(FileEntry{}),
			"c.md": sig.AsEntry(FileEntry{}),
		},
		Folders: map[string]ChildFolder{},
	}
	// Replace b.md and c.md with new files (churn 4/5 = 80%).
	newSig := makeSig(t, uniformWords("xx", 400))
	plan, err := Plan(PlanInput{
		FolderPath: t.TempDir(),
		Prev:       prev,
		Files: []ObservedFile{
			{RelPath: "a.md", Sig: sig}, // noop
			{RelPath: "d.md", Sig: newSig},
			{RelPath: "e.md", Sig: newSig},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.PurposeStale {
		t.Fatalf("expected PurposeStale=true; churn was 4/5")
	}
	// Purpose itself is preserved at this layer (Phase-5 will rewrite).
	if plan.Next.Purpose != "Old purpose." {
		t.Fatalf("Phase-3 must not rewrite Purpose; got %q", plan.Next.Purpose)
	}
}

func TestApply_NoWriteWhenChangedFalse(t *testing.T) {
	dir := t.TempDir()
	plan := &MaintainPlan{
		FolderPath: dir,
		Changed:    false,
		Next:       &FolderIndex{SchemaVersion: SchemaVersion},
	}
	if err := Apply(plan); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, FilenameSiftToml)); !os.IsNotExist(err) {
		t.Fatalf("expected no file written; stat err = %v", err)
	}
}

func TestPlan_FoldersMergedFromObservation(t *testing.T) {
	prev := &FolderIndex{
		SchemaVersion: SchemaVersion,
		Files:         map[string]FileEntry{},
		Folders: map[string]ChildFolder{
			"logs": {Ignore: true},
		},
	}
	plan, err := Plan(PlanInput{
		FolderPath: t.TempDir(),
		Prev:       prev,
		Folders: []ObservedChildFolder{
			{Name: "logs"},
			{Name: "drafts"},
		},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if !plan.Next.Folders["logs"].Ignore {
		t.Fatalf("existing logs ignore flag must be preserved")
	}
	if _, ok := plan.Next.Folders["drafts"]; !ok {
		t.Fatalf("new drafts folder should appear")
	}
}

func TestPlan_LoadOrZeroMissing(t *testing.T) {
	dir := t.TempDir()
	idx, existed, err := LoadOrZero(dir)
	if err != nil {
		t.Fatalf("LoadOrZero: %v", err)
	}
	if existed || idx != nil {
		t.Fatalf("expected (nil,false) for missing sift.toml")
	}
}

func TestPlan_LoadOrZeroPresent(t *testing.T) {
	dir := t.TempDir()
	body := uniformWords("hi", 1200)
	sig := makeSig(t, body)
	first, err := PlanAndApply(PlanInput{
		FolderPath: dir,
		Files:      []ObservedFile{{RelPath: "doc.md", Sig: sig}},
	})
	if err != nil {
		t.Fatalf("PlanAndApply: %v", err)
	}
	if !first.Changed {
		t.Fatalf("first apply should write")
	}
	idx, existed, err := LoadOrZero(dir)
	if err != nil {
		t.Fatalf("LoadOrZero after apply: %v", err)
	}
	if !existed || idx == nil {
		t.Fatalf("expected loaded index")
	}
	if got, ok := idx.Files["doc.md"]; !ok || got.ContentHash != sig.ContentHash {
		t.Fatalf("round-trip lost file entry: %+v", idx.Files)
	}
}
