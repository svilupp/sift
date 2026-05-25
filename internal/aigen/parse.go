package aigen

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// ParseFolderResponse extracts a FolderResult from raw model output.
// Three-tier fallback per PROPOSAL2 §"structured outputs":
//
//  1. Raw JSON.
//  2. ```json fenced``` block.
//  3. First balanced { ... } substring.
//
// Always validates schema shape (purpose string, files array of
// {path, summary}). Path-set alignment is the caller's job — see
// ValidatePathSet. Errors are wrapped with %w-friendly text.
func ParseFolderResponse(raw string) (*FolderResult, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, fmt.Errorf("parse response: empty body")
	}

	candidates := []string{strings.TrimSpace(raw)}
	if fenced := extractFencedJSON(raw); fenced != "" {
		candidates = append(candidates, fenced)
	}
	if braced := extractFirstBalancedBrace(raw); braced != "" {
		candidates = append(candidates, braced)
	}

	var lastErr error
	for _, c := range candidates {
		var fr FolderResult
		if err := json.Unmarshal([]byte(c), &fr); err != nil {
			lastErr = err
			continue
		}
		if err := validateShape(&fr); err != nil {
			lastErr = err
			continue
		}
		return &fr, nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("no candidate matched")
	}
	return nil, fmt.Errorf("parse response: %w", lastErr)
}

func validateShape(fr *FolderResult) error {
	if fr == nil {
		return fmt.Errorf("nil response")
	}
	for i, f := range fr.Files {
		if strings.TrimSpace(f.Path) == "" {
			return fmt.Errorf("files[%d]: empty path", i)
		}
		if strings.TrimSpace(f.Summary) == "" {
			return fmt.Errorf("files[%d] (%s): empty summary", i, f.Path)
		}
	}
	return nil
}

// ValidateUseWhen reports whether `use_when` is present with at least
// one non-empty cue. Returns a descriptive error otherwise. Kept
// separate from validateShape because the per-file fallback path
// produces partial FolderResults that cannot satisfy this constraint
// on its own.
func ValidateUseWhen(fr *FolderResult) error {
	if fr == nil {
		return fmt.Errorf("nil response")
	}
	cues := 0
	for _, c := range fr.UseWhen {
		if strings.TrimSpace(c) != "" {
			cues++
		}
	}
	if cues == 0 {
		return fmt.Errorf("use_when: missing or empty (need at least 1 non-empty cue)")
	}
	return nil
}

// ValidatePathSet reports whether the response covers exactly the
// expected paths (no extras, no omissions, no duplicates). Order is
// irrelevant. Returns descriptive errors for diagnostic logging.
func ValidatePathSet(want []string, fr *FolderResult) error {
	if fr == nil {
		return fmt.Errorf("validate path set: nil result")
	}
	wantSet := map[string]bool{}
	for _, p := range want {
		wantSet[p] = true
	}
	gotSet := map[string]int{}
	for _, f := range fr.Files {
		gotSet[f.Path]++
	}

	var missing, unknown, dup []string
	for p := range wantSet {
		if gotSet[p] == 0 {
			missing = append(missing, p)
		}
	}
	for p, n := range gotSet {
		if !wantSet[p] {
			unknown = append(unknown, p)
		}
		if n > 1 {
			dup = append(dup, p)
		}
	}
	if len(missing) == 0 && len(unknown) == 0 && len(dup) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(unknown)
	sort.Strings(dup)
	return fmt.Errorf("path set mismatch: missing=%v unknown=%v duplicates=%v",
		missing, unknown, dup)
}

// extractFencedJSON returns the contents of the first ```json fenced
// code block, or empty string if absent.
func extractFencedJSON(raw string) string {
	const fence = "```"
	i := strings.Index(raw, fence)
	if i < 0 {
		return ""
	}
	rest := raw[i+len(fence):]
	// Optional language tag (json / JSON).
	rest = strings.TrimPrefix(rest, "json")
	rest = strings.TrimPrefix(rest, "JSON")
	rest = strings.TrimLeft(rest, " \t\r\n")

	end := strings.Index(rest, fence)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(rest[:end])
}

// extractFirstBalancedBrace returns the first {...} substring whose
// braces nest cleanly. Cheap state machine; it's enough to recover from
// preamble like "Here is the JSON: { ... }".
func extractFirstBalancedBrace(raw string) string {
	start := strings.Index(raw, "{")
	if start < 0 {
		return ""
	}
	depth := 0
	inString := false
	escape := false
	for i := start; i < len(raw); i++ {
		c := raw[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return raw[start : i+1]
			}
		}
	}
	return ""
}
