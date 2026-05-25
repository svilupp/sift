package ref

import (
	"path/filepath"
	"testing"
)

func TestParseCodeRef(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		raw   string
		check func(t *testing.T, ref CodeRef)
	}{
		{
			name: "canonical range",
			raw:  "internal/search/search.go:464@k4m-475@q1z",
			check: func(t *testing.T, ref CodeRef) {
				t.Helper()
				if ref.RawPath != "internal/search/search.go" {
					t.Fatalf("RawPath = %q", ref.RawPath)
				}
				if ref.StartLine != 464 || ref.EndLine != 475 {
					t.Fatalf("lines = %d-%d, want 464-475", ref.StartLine, ref.EndLine)
				}
				if !ref.HasEnd {
					t.Fatalf("HasEnd = false, want true")
				}
				if ref.StartToken != "k4m" || ref.EndToken != "q1z" {
					t.Fatalf("tokens = %q/%q", ref.StartToken, ref.EndToken)
				}
				if got := ref.Canonical(); got != "internal/search/search.go:464@k4m-475@q1z" {
					t.Fatalf("Canonical() = %q", got)
				}
			},
		},
		{
			name: "github style single line",
			raw:  "internal/cli/search.go#L518@f2c",
			check: func(t *testing.T, ref CodeRef) {
				t.Helper()
				if ref.StartLine != 518 || ref.EndLine != 518 {
					t.Fatalf("lines = %d-%d, want 518-518", ref.StartLine, ref.EndLine)
				}
				if ref.HasEnd {
					t.Fatalf("HasEnd = true, want false")
				}
				if got := ref.Canonical(); got != "internal/cli/search.go:518@f2c" {
					t.Fatalf("Canonical() = %q", got)
				}
			},
		},
		{
			name: "github style range without tokens",
			raw:  "cmd/sift/main.go#L12-L28",
			check: func(t *testing.T, ref CodeRef) {
				t.Helper()
				if ref.StartLine != 12 || ref.EndLine != 28 {
					t.Fatalf("lines = %d-%d, want 12-28", ref.StartLine, ref.EndLine)
				}
				if !ref.HasEnd {
					t.Fatalf("HasEnd = false, want true")
				}
				if ref.HasAnyToken() {
					t.Fatalf("HasAnyToken = true, want false")
				}
				if got := ref.Canonical(); got != "cmd/sift/main.go:12-28" {
					t.Fatalf("Canonical() = %q", got)
				}
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ref, err := ParseCodeRef(tt.raw)
			if err != nil {
				t.Fatalf("ParseCodeRef(%q): %v", tt.raw, err)
			}
			tt.check(t, ref)
		})
	}
}

func TestParseCodeRefRejectsInvalidRanges(t *testing.T) {
	t.Parallel()

	if _, err := ParseCodeRef("internal/search/search.go:20-10"); err == nil {
		t.Fatalf("expected invalid descending range")
	}
}

func TestExtract(t *testing.T) {
	t.Parallel()

	sourcePath := filepath.Join(t.TempDir(), "PLAN.md")
	content := []byte("Inline `internal/search/search.go:12-14` ref.\n\n```text\ninternal/cli/search.go#L20@abc-L22@def\n```\n\nPlain internal/search/search.go:40@ghi text.\n")

	matches := Extract(sourcePath, content)
	if len(matches) != 3 {
		t.Fatalf("len(Extract) = %d, want 3", len(matches))
	}

	if matches[0].Ref.SourceLine != 1 {
		t.Fatalf("match[0].Ref.SourceLine = %d, want 1", matches[0].Ref.SourceLine)
	}
	if got := matches[1].Ref.Canonical(); got != "internal/cli/search.go:20@abc-22@def" {
		t.Fatalf("match[1].Canonical() = %q", got)
	}
	if got := matches[2].Ref.Canonical(); got != "internal/search/search.go:40@ghi" {
		t.Fatalf("match[2].Canonical() = %q", got)
	}
}
