package ref

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEngineValidateStatuses(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	docPath := filepath.Join(root, "PLAN.md")
	if err := os.WriteFile(docPath, []byte("# plan\n"), 0644); err != nil {
		t.Fatal(err)
	}

	exactFile := filepath.Join(root, "exact.go")
	exactLines := []string{
		"package sample",
		"func exact() {",
		"return err",
		"}",
	}
	writeLines(t, exactFile, exactLines)

	shiftedFile := filepath.Join(root, "shifted.go")
	shiftedLines := []string{
		"package sample",
		"",
		"func shifted() {",
		"helper()",
		"if err != nil {",
		"return fmt.Errorf(\"wrap: %w\", err)",
		"}",
		"}",
	}
	writeLines(t, shiftedFile, shiftedLines)

	ambiguousFile := filepath.Join(root, "ambiguous.go")
	ambiguousLines := []string{
		"package sample",
		"",
		"func ambiguous() {",
		"return err",
		"helper()",
		"return err",
		"}",
	}
	writeLines(t, ambiguousFile, ambiguousLines)

	engine := NewEngine(Options{
		CollectionRoots: []string{root},
		Window:          10,
		EndSlack:        3,
	})

	t.Run("valid exact", func(t *testing.T) {
		ref := CodeRef{
			Raw:        "exact.go:3@" + LineToken("return err", 3),
			SourcePath: docPath,
			RawPath:    "exact.go",
			StartLine:  3,
			EndLine:    3,
			StartToken: LineToken("return err", 3),
		}

		got := engine.Validate(ref)
		if got.Status != StatusValidExact {
			t.Fatalf("Status = %s, want %s", got.Status, StatusValidExact)
		}
	})

	t.Run("valid unchecked", func(t *testing.T) {
		ref := CodeRef{
			Raw:        "exact.go:3-4",
			SourcePath: docPath,
			RawPath:    "exact.go",
			StartLine:  3,
			EndLine:    4,
			HasEnd:     true,
		}

		got := engine.Validate(ref)
		if got.Status != StatusValidUnchecked {
			t.Fatalf("Status = %s, want %s", got.Status, StatusValidUnchecked)
		}
		if got.SuggestedRaw == "" {
			t.Fatalf("SuggestedRaw should be populated for unchecked refs")
		}
	})

	t.Run("valid shifted range", func(t *testing.T) {
		startToken := LineToken("if err != nil {", 3)
		endToken := LineToken("return fmt.Errorf(\"wrap: %w\", err)", 3)
		ref := CodeRef{
			Raw:        "shifted.go:3@" + startToken + "-4@" + endToken,
			SourcePath: docPath,
			RawPath:    "shifted.go",
			StartLine:  3,
			EndLine:    4,
			HasEnd:     true,
			StartToken: startToken,
			EndToken:   endToken,
		}

		got := engine.Validate(ref)
		if got.Status != StatusValidShifted {
			t.Fatalf("Status = %s, want %s", got.Status, StatusValidShifted)
		}
		if got.SuggestedStart != 5 || got.SuggestedEnd != 6 {
			t.Fatalf("suggested lines = %d-%d, want 5-6", got.SuggestedStart, got.SuggestedEnd)
		}
	})

	t.Run("stale ambiguous", func(t *testing.T) {
		token := LineToken("return err", 3)
		ref := CodeRef{
			Raw:        "ambiguous.go:2@" + token,
			SourcePath: docPath,
			RawPath:    "ambiguous.go",
			StartLine:  2,
			EndLine:    2,
			StartToken: token,
		}

		got := engine.Validate(ref)
		if got.Status != StatusStaleAmbiguous {
			t.Fatalf("Status = %s, want %s", got.Status, StatusStaleAmbiguous)
		}
	})

	t.Run("out of bounds", func(t *testing.T) {
		ref := CodeRef{
			Raw:        "exact.go:99",
			SourcePath: docPath,
			RawPath:    "exact.go",
			StartLine:  99,
			EndLine:    99,
		}

		got := engine.Validate(ref)
		if got.Status != StatusOutOfBounds {
			t.Fatalf("Status = %s, want %s", got.Status, StatusOutOfBounds)
		}
	})

	t.Run("missing file", func(t *testing.T) {
		ref := CodeRef{
			Raw:        "missing.go:3",
			SourcePath: docPath,
			RawPath:    "missing.go",
			StartLine:  3,
			EndLine:    3,
		}

		got := engine.Validate(ref)
		if got.Status != StatusMissingFile {
			t.Fatalf("Status = %s, want %s", got.Status, StatusMissingFile)
		}
	})
}

func writeLines(t *testing.T, path string, lines []string) {
	t.Helper()

	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(joinLines(lines)), 0644); err != nil {
		t.Fatal(err)
	}
}

func joinLines(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	out := lines[0]
	for _, line := range lines[1:] {
		out += "\n" + line
	}
	return out + "\n"
}
