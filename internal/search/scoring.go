package search

import (
	"math"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"

	"sift/internal/config"
)

// ComputeRecency returns a recency score using exponential half-life decay.
// Formula: recency = 0.5 ^ (age_days / half_life_days)
// Returns 1.0 for files modified right now, ~0.5 at half_life_days, ~0.25 at 2x half_life_days.
func ComputeRecency(mtime time.Time, now time.Time, halfLifeDays float64) float64 {
	if halfLifeDays <= 0 {
		return 1.0
	}

	ageDays := now.Sub(mtime).Hours() / 24.0
	if ageDays < 0 {
		ageDays = 0
	}

	return math.Pow(0.5, ageDays/halfLifeDays)
}

// FeedbackBoost computes a Bayesian-smoothed feedback boost.
// Formula: boost = 0.7 + 0.6 * ((good + 1) / (good + bad + 2))
// Range: [0.7, 1.3]. Returns 1.0 when good=0 and bad=0.
func FeedbackBoost(good, bad int) float64 {
	if good == 0 && bad == 0 {
		return 1.0
	}
	return 0.7 + 0.6*(float64(good+1)/float64(good+bad+2))
}

// ComputeFinalScore combines the base score with recency and feedback boosts.
// Formula: final = base_score * (1 + recency * recency_weight) * feedback_boost
func ComputeFinalScore(baseScore float64, mtime time.Time, now time.Time, recencyWeight float64, halfLifeDays float64, feedbackBoost float64) float64 {
	recency := ComputeRecency(mtime, now, halfLifeDays)
	return baseScore * (1.0 + recency*recencyWeight) * feedbackBoost
}

// PathBoost returns a score multiplier based on the file path.
// Evaluates patterns in order; first match wins. Returns 1.0 if no match.
func PathBoost(path string, boosts []config.PathBoost) float64 {
	for _, pb := range boosts {
		if globMatchPath(path, pb.Pattern) {
			return pb.Boost
		}
	}
	return 1.0
}

// globMatchPath checks if path matches a glob pattern (e.g. "*/pinned/*").
// Uses doublestar for full glob support including **, ?, and [abc].
func globMatchPath(path string, pattern string) bool {
	matched, _ := doublestar.Match(pattern, path)
	return matched
}

// ExpandCodeIdentifiers expands camelCase and snake_case query terms into variants.
// "swapId" → "(swapId OR swap_id OR swap id)"
func ExpandCodeIdentifiers(query string) string {
	words := strings.Fields(query)
	var expanded []string
	for _, w := range words {
		variants := codeVariants(w)
		if len(variants) > 1 {
			expanded = append(expanded, "("+strings.Join(variants, " OR ")+")")
		} else {
			expanded = append(expanded, w)
		}
	}
	return strings.Join(expanded, " ")
}

// codeVariants generates camelCase/snake_case/space variants of a word.
func codeVariants(word string) []string {
	parts := splitCamelCase(word)
	if len(parts) <= 1 {
		// Try snake_case split.
		parts = strings.Split(word, "_")
		if len(parts) <= 1 {
			return []string{word}
		}
	}

	lower := make([]string, len(parts))
	for i, p := range parts {
		lower[i] = strings.ToLower(p)
	}

	variants := []string{
		word,                         // original: swapId
		strings.Join(lower, "_"),     // swap_id
		strings.Join(lower, " "),     // swap id
	}

	// Deduplicate.
	seen := make(map[string]bool, len(variants))
	unique := variants[:0]
	for _, v := range variants {
		if !seen[v] {
			seen[v] = true
			unique = append(unique, v)
		}
	}
	return unique
}

// splitCamelCase splits a camelCase string into parts.
// "swapId" → ["swap", "Id"], "HTMLParser" → ["HTML", "Parser"]
func splitCamelCase(s string) []string {
	if len(s) == 0 {
		return nil
	}
	var parts []string
	start := 0
	runes := []rune(s)
	for i := 1; i < len(runes); i++ {
		if isUpper(runes[i]) && !isUpper(runes[i-1]) {
			// lowerUpper boundary: swap|Id
			parts = append(parts, string(runes[start:i]))
			start = i
		} else if isUpper(runes[i-1]) && isUpper(runes[i]) && i+1 < len(runes) && !isUpper(runes[i+1]) {
			// UPPERLower boundary: HTM|LP -> HTML|Parser
			parts = append(parts, string(runes[start:i]))
			start = i
		}
	}
	parts = append(parts, string(runes[start:]))
	if len(parts) <= 1 {
		return nil // no splits found
	}
	return parts
}

func isUpper(r rune) bool {
	return r >= 'A' && r <= 'Z'
}
