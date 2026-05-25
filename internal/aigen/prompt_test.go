package aigen

import (
	"strings"
	"testing"
)

func TestSystemPromptEmbedded(t *testing.T) {
	if !strings.Contains(SystemPrompt(), "Banned openings") {
		t.Fatal("system prompt missing banned openings rules; embed broken?")
	}
	if !strings.Contains(SystemPrompt(), "200 characters MAX") {
		t.Fatal("system prompt missing 200-char rule; embed broken?")
	}
}

func TestBuildFolderPromptDeterministic(t *testing.T) {
	in := PromptInput{
		Folder: FolderContext{
			RelPath:         "docs/spec",
			ParentPurpose:   "Specifications root",
			ExistingPurpose: "Authoring spec",
			ExistingUseWhen: []string{"writing rfcs", "  "},
		},
		Files: []FileBatch{
			{Path: "b.md", Words: 100, HeadWords: "hello", TailWords: "hello"},
			{Path: "a.md", Words: 200, HeadWords: "alpha head", TailWords: "alpha tail",
				FrontmatterTitle: "Alpha", FrontmatterTags: []string{"x", "y"},
				CurrentSummary: "Existing alpha summary."},
		},
	}
	first := BuildFolderPrompt(in)
	second := BuildFolderPrompt(in)
	if first != second {
		t.Fatalf("non-deterministic prompt:\n--first--\n%s\n--second--\n%s", first, second)
	}

	// Files must be alphabetized regardless of input order.
	if !strings.Contains(first, "[1] path: a.md") {
		t.Errorf("expected a.md first; got:\n%s", first)
	}
	if !strings.Contains(first, "[2] path: b.md") {
		t.Errorf("expected b.md second; got:\n%s", first)
	}
	// Existing summary carried through.
	if !strings.Contains(first, "current_summary: Existing alpha summary.") {
		t.Errorf("expected current_summary to render; got:\n%s", first)
	}
	// Empty use_when entries skipped.
	if strings.Count(first, "  - ") != 1 {
		t.Errorf("expected one cue, got %d:\n%s", strings.Count(first, "  - "), first)
	}
	// Tail omitted when equal to head.
	if strings.Contains(first, "tail_500w") && !strings.Contains(first, "alpha tail") {
		t.Errorf("expected tail_500w only for differing tail; got:\n%s", first)
	}
}

func TestBuildFolderPromptSnapshotShape(t *testing.T) {
	out := BuildFolderPrompt(PromptInput{
		Folder: FolderContext{RelPath: "x"},
		Files:  []FileBatch{{Path: "a.md", Words: 1}},
	})
	wants := []string{
		"Folder: x",
		"Files:",
		"[1] path: a.md",
		"words: 1",
		`Respond with one JSON object`,
		"Cover EXACTLY the file paths listed above",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("missing %q in:\n%s", w, out)
		}
	}
}
