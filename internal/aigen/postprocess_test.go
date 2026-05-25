package aigen

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// TestTruncateSummaries covers the per-file summary cap enforcement.
//
// Length is measured in runes (utf8.RuneCountInString). The ellipsis
// rune "…" (U+2026) is counted as 1 rune even though it is 3 bytes.
func TestTruncateSummaries(t *testing.T) {
	t.Run("under cap unchanged", func(t *testing.T) {
		fr := &FolderResult{Files: []FileSummary{{Path: "a", Summary: "short summary."}}}
		n := TruncateSummaries(fr, 200)
		if n != 0 {
			t.Errorf("expected 0 truncations, got %d", n)
		}
		if fr.Files[0].Summary != "short summary." {
			t.Errorf("unexpected change: %q", fr.Files[0].Summary)
		}
	})

	t.Run("exact cap unchanged", func(t *testing.T) {
		s := strings.Repeat("a", 200)
		fr := &FolderResult{Files: []FileSummary{{Path: "a", Summary: s}}}
		n := TruncateSummaries(fr, 200)
		if n != 0 {
			t.Errorf("expected 0 truncations, got %d", n)
		}
		if fr.Files[0].Summary != s {
			t.Errorf("changed at exact cap")
		}
	})

	t.Run("over cap with sentence boundary", func(t *testing.T) {
		// Build: 178 chars + ". " (so period at index 178, space at 179)
		// + 21 more chars = 201 total.
		head := strings.Repeat("a", 178) + ". "
		tail := strings.Repeat("b", 21)
		s := head + tail
		if utf8.RuneCountInString(s) != 201 {
			t.Fatalf("setup: expected 201 runes, got %d", utf8.RuneCountInString(s))
		}
		fr := &FolderResult{Files: []FileSummary{{Path: "a", Summary: s}}}
		n := TruncateSummaries(fr, 200)
		if n != 1 {
			t.Errorf("expected 1 truncation, got %d", n)
		}
		// Should truncate at "<178 a's>." (179 chars; period included,
		// trailing space dropped).
		got := fr.Files[0].Summary
		want := strings.Repeat("a", 178) + "."
		if got != want {
			t.Errorf("got %q (len=%d), want %q (len=%d)",
				got, utf8.RuneCountInString(got), want, utf8.RuneCountInString(want))
		}
		if strings.Contains(got, "…") {
			t.Errorf("clean sentence should not have ellipsis: %q", got)
		}
	})

	t.Run("over cap no boundary uses ellipsis", func(t *testing.T) {
		// 250 'a' chars, no punctuation.
		s := strings.Repeat("a", 250)
		fr := &FolderResult{Files: []FileSummary{{Path: "a", Summary: s}}}
		n := TruncateSummaries(fr, 200)
		if n != 1 {
			t.Errorf("expected 1 truncation, got %d", n)
		}
		got := fr.Files[0].Summary
		runeLen := utf8.RuneCountInString(got)
		if runeLen != 200 {
			t.Errorf("expected 200-rune output, got %d (%q)", runeLen, got)
		}
		if !strings.HasSuffix(got, "…") {
			t.Errorf("expected ellipsis suffix, got %q", got)
		}
		// 199 'a's + "…"
		want := strings.Repeat("a", 199) + "…"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("multiple files mixed count", func(t *testing.T) {
		fr := &FolderResult{Files: []FileSummary{
			{Path: "a", Summary: "ok."},
			{Path: "b", Summary: strings.Repeat("x", 250)},
			{Path: "c", Summary: strings.Repeat("y", 199)},
			{Path: "d", Summary: strings.Repeat("z", 201)},
		}}
		n := TruncateSummaries(fr, 200)
		if n != 2 {
			t.Errorf("expected 2 truncations, got %d", n)
		}
		// Order preserved.
		if fr.Files[0].Path != "a" || fr.Files[1].Path != "b" ||
			fr.Files[2].Path != "c" || fr.Files[3].Path != "d" {
			t.Errorf("file order changed")
		}
		// Untouched entries unchanged.
		if fr.Files[0].Summary != "ok." {
			t.Errorf("file a changed: %q", fr.Files[0].Summary)
		}
		if utf8.RuneCountInString(fr.Files[2].Summary) != 199 {
			t.Errorf("file c changed unexpectedly")
		}
		// Truncated entries within cap.
		for _, idx := range []int{1, 3} {
			if utf8.RuneCountInString(fr.Files[idx].Summary) > 200 {
				t.Errorf("file %s exceeds cap: %d runes",
					fr.Files[idx].Path, utf8.RuneCountInString(fr.Files[idx].Summary))
			}
		}
	})

	t.Run("nil result safe", func(t *testing.T) {
		if got := TruncateSummaries(nil, 200); got != 0 {
			t.Errorf("expected 0, got %d", got)
		}
	})
}
