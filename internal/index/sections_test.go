package index

import (
	"context"
	"path/filepath"
	"testing"
)

func TestAttachSections_BasicMarkdown(t *testing.T) {
	root := t.TempDir()
	body := "# Title\n\nintro\n\n## Sub\n\nbody\n\n## Another\n\nmore\n"
	writeFileTH(t, filepath.Join(root, "doc.md"), body)
	writeIndexHelper(t, root, "Root.", []string{"doc.md"})

	fm, err := LoadTree(context.Background(), root, "", DefaultLoadOptions())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	AttachSections(context.Background(), fm)

	got := fm.Folders[0].Files[0].Sections
	if len(got) != 3 {
		t.Fatalf("want 3 H1/H2 sections, got %d: %+v", len(got), got)
	}
	if got[0].Heading != "Title" || got[0].Level != 1 {
		t.Fatalf("unexpected first section: %+v", got[0])
	}
	if got[1].Heading != "Sub" || got[1].Level != 2 {
		t.Fatalf("unexpected second section: %+v", got[1])
	}
	file := fm.Folders[0].Files[0]
	if file.Title != "Title" || file.Excerpt != "intro" || file.Words == 0 {
		t.Fatalf("local semantic metadata missing: %+v", file)
	}
}

func TestAttachSections_NonText(t *testing.T) {
	root := t.TempDir()
	writeFileTH(t, filepath.Join(root, "main.go"), "package main\n\nfunc main(){}\n")
	writeIndexHelper(t, root, "Root.", []string{"main.go"})

	fm, err := LoadTree(context.Background(), root, "", DefaultLoadOptions())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	AttachSections(context.Background(), fm)

	if got := fm.Folders[0].Files[0].Sections; got != nil {
		t.Fatalf("expected nil sections for non-text file, got %v", got)
	}
}

func TestAttachSections_NoHeadings(t *testing.T) {
	root := t.TempDir()
	writeFileTH(t, filepath.Join(root, "plain.md"), "no headings here, just prose.\n")
	writeIndexHelper(t, root, "Root.", []string{"plain.md"})

	fm, err := LoadTree(context.Background(), root, "", DefaultLoadOptions())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	AttachSections(context.Background(), fm)

	got := fm.Folders[0].Files[0].Sections
	if got == nil {
		t.Fatalf("expected non-nil empty slice for tried-but-found-none, got nil")
	}
	if len(got) != 0 {
		t.Fatalf("expected zero sections, got %+v", got)
	}
}

func TestAttachSections_FiltersDeepHeadings(t *testing.T) {
	root := t.TempDir()
	body := "# Top\n\n## Mid\n\n### Deep\n\nstuff\n"
	writeFileTH(t, filepath.Join(root, "x.md"), body)
	writeIndexHelper(t, root, "Root.", []string{"x.md"})

	fm, _ := LoadTree(context.Background(), root, "", DefaultLoadOptions())
	AttachSections(context.Background(), fm)
	got := fm.Folders[0].Files[0].Sections
	if len(got) != 2 {
		t.Fatalf("expected only H1 and H2, got %d: %+v", len(got), got)
	}
}
