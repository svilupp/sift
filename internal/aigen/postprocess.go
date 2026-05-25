package aigen

import (
	"unicode/utf8"
)

// MaxFileSummaryChars is the hard cap enforced on per-file summaries
// after parsing. Prompt asks for ≤200; ~5% of outputs spill 1–28
// chars over. TruncateSummaries clamps the rest.
const MaxFileSummaryChars = 200

// TruncateSummaries enforces a per-file summary length cap on the
// parsed FolderResult, in place. For each FileSummary whose rune
// length exceeds maxChars, the summary is truncated. Truncation
// prefers the LAST sentence boundary (`.`/`!`/`?` followed by space
// or end-of-string) within the cap; otherwise it hard-cuts at
// maxChars-1 runes and appends a single ellipsis rune (U+2026,
// counted as 1 rune in the length budget even though it is 3 bytes
// in UTF-8). File order is preserved.
//
// Returns the count of summaries that were modified.
func TruncateSummaries(result *FolderResult, maxChars int) int {
	if result == nil || maxChars <= 0 {
		return 0
	}
	count := 0
	for i, f := range result.Files {
		if utf8.RuneCountInString(f.Summary) <= maxChars {
			continue
		}
		result.Files[i].Summary = truncateOne(f.Summary, maxChars)
		count++
	}
	return count
}

// truncateOne truncates s to at most maxChars runes, preferring the
// last sentence boundary (./!/?) followed by space or EOL within the
// cap. If no boundary is found, hard-cuts at maxChars-1 runes and
// appends "…".
func truncateOne(s string, maxChars int) string {
	// Walk runes, tracking byte-offsets. Capture the last byte-end
	// position for each rune index.
	runes := make([]rune, 0, maxChars)
	ends := make([]int, 0, maxChars)
	for i, r := range s {
		runes = append(runes, r)
		ends = append(ends, i+utf8.RuneLen(r))
		if len(runes) > maxChars {
			break
		}
	}
	// Look for sentence boundary within the first maxChars runes.
	// Boundary = ./!/? at position k where k+1 is space or beyond cap
	// (treat end-of-cap as EOL for boundary purposes).
	limit := maxChars
	if limit > len(runes) {
		limit = len(runes)
	}
	bestEnd := -1
	for k := 0; k < limit; k++ {
		r := runes[k]
		if r != '.' && r != '!' && r != '?' {
			continue
		}
		// Followed by whitespace or out-of-cap EOL.
		if k+1 < len(runes) {
			next := runes[k+1]
			if next == ' ' || next == '\t' || next == '\n' || next == '\r' {
				bestEnd = ends[k]
			}
		} else {
			// k is the last rune we have; treat as EOL.
			bestEnd = ends[k]
		}
	}
	if bestEnd > 0 {
		return s[:bestEnd]
	}
	// No sentence boundary; hard-cut at maxChars-1 runes + ellipsis.
	cut := maxChars - 1
	if cut < 0 {
		cut = 0
	}
	if cut >= len(ends) {
		// Source had ≤ maxChars-1 runes; nothing to do.
		return s
	}
	return s[:ends[cut-1]] + "…"
}
