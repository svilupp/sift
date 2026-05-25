package aigen

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// commonCapitalizedWords is a small allowlist of capitalized English
// tokens that frequently appear at the start of sentences after a
// period and should not be flagged as "interesting" proper nouns
// even when they are not technically the FIRST word of the purpose.
// Conservative — when in doubt, let it through and accept a benign
// warning rather than silently miss a real hallucination.
var commonCapitalizedWords = map[string]bool{
	"The": true, "This": true, "That": true, "These": true, "Those": true,
	"A": true, "An": true, "And": true, "Or": true, "But": true,
	"For": true, "Of": true, "In": true, "On": true, "At": true,
	"To": true, "From": true, "By": true, "With": true, "Without": true,
	"It": true, "Its": true, "Their": true, "His": true, "Her": true,
	"Use": true, "Used": true, "Useful": true, "Uses": true,
	"When": true, "Where": true, "What": true, "Which": true, "Who": true,
	"Why": true, "How": true,
	"Files": true, "File": true, "Folder": true, "Folders": true,
	"Notes": true, "Note": true, "Docs": true, "Doc": true,
	"Code": true, "Source": true, "Tests": true, "Test": true,
	"Includes": true, "Include": true, "Contains": true, "Contain": true,
	"Holds": true, "Hold": true, "Stores": true, "Store": true,
	"Provides": true, "Provide": true, "Tracks": true, "Track": true,
	"Captures": true, "Capture": true, "Records": true, "Record": true,
}

// reCapToken matches a capitalized word: starts with an uppercase
// letter, followed by 1+ alphanumerics. e.g. "Acme", "LEGO",
// "BetaCo".
var reCapToken = regexp.MustCompile(`[A-Z][a-zA-Z0-9]+`)

// reIDLike matches a token containing at least one digit and made up
// of letters/digits/dot/dash. e.g. "v0.1", "Q1", "2026", "GPT-4".
var reIDLike = regexp.MustCompile(`[A-Za-z0-9.\-]*\d[A-Za-z0-9.\-]*`)

// CheckPurposeGrounding inspects the model's generated `purpose`
// against the supplied file batches and returns a list of warnings
// for "interesting" tokens (proper nouns / IDs / dates) that do not
// appear anywhere in the file content (HeadWords/TailWords),
// frontmatter (Title/Tags), or paths.
//
// folderPath, when non-empty, is the folder owning the sift.toml.
// Its basename and ancestor folder basenames are added to the
// searchable corpus (path components only — never the full path) so
// that a token appearing only in the folder name (e.g. "2026" in
// design-workshop-2026/) does not trip the warning.
//
// Empty slice = grounded.
func CheckPurposeGrounding(result FolderResult, files []FileBatch, folderPath string) []string {
	purpose := strings.TrimSpace(result.Purpose)
	if purpose == "" {
		return nil
	}

	// Build a single haystack from all file payloads.
	var hay strings.Builder
	for _, f := range files {
		hay.WriteString(f.HeadWords)
		hay.WriteByte(' ')
		hay.WriteString(f.TailWords)
		hay.WriteByte(' ')
		hay.WriteString(f.Path)
		hay.WriteByte(' ')
		hay.WriteString(f.FrontmatterTitle)
		hay.WriteByte(' ')
		hay.WriteString(f.FrontmatterType)
		hay.WriteByte(' ')
		for _, tag := range f.FrontmatterTags {
			hay.WriteString(tag)
			hay.WriteByte(' ')
		}
	}
	// Append folder path components (basename + ancestor basenames).
	// Splitting on path separators ensures we add segments individually
	// rather than the full absolute path (which would over-match).
	for _, seg := range splitPathSegments(folderPath) {
		hay.WriteString(seg)
		hay.WriteByte(' ')
	}
	haystack := strings.ToLower(hay.String())

	tokens := interestingTokens(purpose)
	if len(tokens) == 0 {
		return nil
	}

	seen := map[string]bool{}
	var warnings []string
	for _, tok := range tokens {
		key := strings.ToLower(tok)
		if seen[key] {
			continue
		}
		seen[key] = true
		if !strings.Contains(haystack, key) {
			warnings = append(warnings, fmt.Sprintf("purpose token %q not found in any file content", tok))
		}
	}
	return warnings
}

// splitPathSegments returns the basename plus ancestor folder
// basenames of p. Empty for empty or root-only inputs. Forward and
// back slashes are both handled.
func splitPathSegments(p string) []string {
	if p == "" {
		return nil
	}
	// Normalize separators so filepath.Base/Dir behave on either OS-
	// style. We split manually to avoid OS coupling.
	p = strings.ReplaceAll(p, "\\", "/")
	parts := strings.Split(p, "/")
	out := parts[:0]
	for _, seg := range parts {
		if seg == "" || seg == "." {
			continue
		}
		out = append(out, seg)
	}
	// Drop the very first segment when it looks like a Unix root
	// drive prefix (empty before a leading slash already handled by
	// the empty filter). Keep all named ancestors.
	_ = filepath.Separator
	return out
}

// interestingTokens returns the subset of tokens in s that look like
// proper nouns, IDs, or numeric markers worth grounding. Skips:
//   - the very first word of the text (sentence start)
//   - the first word after each `.`/`!`/`?` (sentence start)
//   - common English words on the allowlist
//   - tokens with no letters and no digits.
//
// Compound tokens joined by `-`, `/`, em-dash (U+2014) or en-dash
// (U+2013) are split into their constituent pieces — the model
// frequently coins compounds like "AI-generated" or "Q3–Q4" whose
// parts are grounded individually but whose joined form is not
// verbatim in the source.
//
// Trailing possessive `'s` (straight or curly apostrophe) is
// stripped before matching: when "X" appears in source, "X's" never
// appears verbatim.
func interestingTokens(s string) []string {
	// Split on whitespace, but track sentence-start positions.
	var out []string
	var cur strings.Builder
	atSentenceStart := true

	flush := func() {
		w := cur.String()
		cur.Reset()
		if w == "" {
			return
		}
		// Strip leading/trailing punctuation for matching purposes.
		// Note: we keep `-`, `/`, em-dash, en-dash inside the token
		// because we want to recognize compound forms first, then
		// split them into sub-tokens below.
		trimmed := strings.TrimFunc(w, func(r rune) bool {
			return !unicode.IsLetter(r) && !unicode.IsDigit(r)
		})
		if trimmed == "" {
			atSentenceStart = endsSentence(w)
			return
		}

		// Split compound tokens on `-`, `/`, em-dash, en-dash. Each
		// piece is evaluated independently against the sentence-start
		// position of the whole compound.
		subs := splitCompound(trimmed)
		isCompound := len(subs) > 1
		for _, sub := range subs {
			sub = stripPossessive(sub)
			// After possessive stripping the sub-token may end with a
			// stray non-alphanumeric character; trim again.
			sub = strings.TrimFunc(sub, func(r rune) bool {
				return !unicode.IsLetter(r) && !unicode.IsDigit(r)
			})
			if sub == "" {
				continue
			}
			// In compound forms, pure-digit sub-tokens (e.g. "12" in
			// "12-slide", "15" in "15-item") are quantifier counts,
			// not IDs worth grounding. Skip them. Standalone numeric
			// tokens (a bare "2026") still flow through the non-
			// compound path below.
			if isCompound && isAllDigits(sub) {
				continue
			}

			isCap := reCapToken.MatchString(sub) && reCapTokenStart(sub)
			isID := reIDLike.MatchString(sub) && containsDigit(sub)

			if !atSentenceStart && isCap && !commonCapitalizedWords[sub] {
				out = append(out, sub)
			} else if isID {
				// IDs (anything with a digit) are flagged regardless
				// of sentence position — "Q1" or "2026" rarely starts
				// a sentence as a stop-word.
				out = append(out, sub)
			}
		}

		atSentenceStart = endsSentence(w)
	}

	for _, r := range s {
		if unicode.IsSpace(r) {
			flush()
			continue
		}
		cur.WriteRune(r)
	}
	flush()
	return out
}

// splitCompound breaks t at `-`, `/`, em-dash (U+2014), and en-dash
// (U+2013). Returns t unchanged when no separator is present.
func splitCompound(t string) []string {
	hasSep := false
	for _, r := range t {
		if r == '-' || r == '/' || r == '—' || r == '–' {
			hasSep = true
			break
		}
	}
	if !hasSep {
		return []string{t}
	}
	parts := strings.FieldsFunc(t, func(r rune) bool {
		return r == '-' || r == '/' || r == '—' || r == '–'
	})
	return parts
}

// stripPossessive removes a trailing `'s` / `'s` (curly apostrophe)
// in any case. e.g. "Smith's" → "Smith", "SWAP'S" → "SWAP".
func stripPossessive(t string) string {
	if len(t) < 2 {
		return t
	}
	// Two suffix forms: ASCII apostrophe + s, curly apostrophe + s.
	// Curly is a 3-byte UTF-8 sequence (U+2019).
	lower := strings.ToLower(t)
	if strings.HasSuffix(lower, "'s") {
		return t[:len(t)-2]
	}
	if strings.HasSuffix(lower, "’s") {
		return t[:len(t)-len("’s")]
	}
	return t
}

// reCapTokenStart returns true when the trimmed token begins with an
// uppercase ASCII letter (a quick check that mirrors the reCapToken
// pattern's anchor expectation).
func reCapTokenStart(s string) bool {
	if s == "" {
		return false
	}
	r := rune(s[0])
	return r >= 'A' && r <= 'Z'
}

// isAllDigits reports whether s is non-empty and contains only ASCII
// digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// containsDigit reports whether s has at least one ASCII digit.
func containsDigit(s string) bool {
	for _, r := range s {
		if r >= '0' && r <= '9' {
			return true
		}
	}
	return false
}

// endsSentence reports whether token w ends in `.`, `!`, or `?` —
// signaling that the next non-empty token is at a sentence start.
func endsSentence(w string) bool {
	if w == "" {
		return false
	}
	last := w[len(w)-1]
	return last == '.' || last == '!' || last == '?'
}
