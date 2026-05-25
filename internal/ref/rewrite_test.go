package ref

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEngineStampDocument(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	docPath := filepath.Join(root, "PLAN.md")
	codePath := filepath.Join(root, "internal", "search", "search.go")
	writeLines(t, codePath, []string{
		"package search",
		"func main() {",
		"return err",
		"}",
	})

	content := []byte("- Main scoring logic: `internal/search/search.go:2-3`\n")
	engine := NewEngine(Options{CollectionRoots: []string{root}})

	result, err := engine.StampDocument(docPath, content)
	if err != nil {
		t.Fatalf("StampDocument: %v", err)
	}
	if !result.Changed {
		t.Fatalf("Changed = false, want true")
	}
	got := string(result.Content)
	if !strings.Contains(got, "internal/search/search.go:2@") {
		t.Fatalf("stamped content = %q", got)
	}
	if !strings.HasPrefix(got, "- Main scoring logic: `") {
		t.Fatalf("surrounding markdown should be preserved: %q", got)
	}
}

func TestEngineValidateDocumentFix(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	docPath := filepath.Join(root, "PLAN.md")
	codePath := filepath.Join(root, "shifted.go")
	writeLines(t, codePath, []string{
		"package sample",
		"",
		"func shifted() {",
		"helper()",
		"if err != nil {",
		"return fmt.Errorf(\"wrap: %w\", err)",
		"}",
		"}",
	})

	startToken := LineToken("if err != nil {", 3)
	endToken := LineToken("return fmt.Errorf(\"wrap: %w\", err)", 3)
	content := []byte("- See `shifted.go:3@" + startToken + "-4@" + endToken + "` for the guard.\n")

	engine := NewEngine(Options{
		CollectionRoots: []string{root},
		Window:          10,
		EndSlack:        3,
	})

	result, err := engine.ValidateDocument(docPath, content, true)
	if err != nil {
		t.Fatalf("ValidateDocument: %v", err)
	}
	if !result.Changed {
		t.Fatalf("Changed = false, want true")
	}
	if !strings.Contains(string(result.Content), "shifted.go:5@"+startToken+"-6@"+endToken) {
		t.Fatalf("fixed content = %q", string(result.Content))
	}
	if len(result.Results) != 1 {
		t.Fatalf("len(Results) = %d, want 1", len(result.Results))
	}
	if result.Results[0].Status != StatusValidShifted {
		t.Fatalf("Status = %s, want %s", result.Results[0].Status, StatusValidShifted)
	}
}

func TestEngineValidateDocumentNoFixLeavesContent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	docPath := filepath.Join(root, "PLAN.md")
	codePath := filepath.Join(root, "exact.go")
	writeLines(t, codePath, []string{
		"package sample",
		"func exact() {",
		"return err",
		"}",
	})

	content := []byte("- Main scoring logic: `exact.go:3`\n")
	engine := NewEngine(Options{CollectionRoots: []string{root}})

	result, err := engine.ValidateDocument(docPath, content, false)
	if err != nil {
		t.Fatalf("ValidateDocument: %v", err)
	}
	if result.Changed {
		t.Fatalf("Changed = true, want false")
	}
	if string(result.Content) != string(content) {
		t.Fatalf("content changed unexpectedly")
	}
}

func TestWriteLinesHelperCreatesParentDirs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "nested", "file.go")
	writeLines(t, target, []string{"package sample"})

	if _, err := os.Stat(target); err != nil {
		t.Fatalf("expected file to exist: %v", err)
	}
}
