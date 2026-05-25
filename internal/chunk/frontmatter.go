package chunk

import (
	"strings"
)

// Frontmatter holds parsed frontmatter from a file.
type Frontmatter struct {
	Raw       string // The raw text between delimiters (no delimiters)
	StartLine int    // 1-based line of opening delimiter
	EndLine   int    // 1-based line of closing delimiter
	Format    string // "yaml" or "toml" (inferred from delimiter type)
}

// delimType returns "yaml", "toml", or "" for a trimmed line.
func delimType(line string) string {
	trimmed := strings.TrimRight(line, " \t")
	if len(trimmed) < 3 {
		return ""
	}
	allDash := true
	for _, r := range trimmed {
		if r != '-' {
			allDash = false
			break
		}
	}
	if allDash {
		return "yaml"
	}
	allPlus := true
	for _, r := range trimmed {
		if r != '+' {
			allPlus = false
			break
		}
	}
	if allPlus {
		return "toml"
	}
	return ""
}

// ParseFrontmatter detects and extracts frontmatter from the top of a file.
// Returns nil if no valid frontmatter is found.
func ParseFrontmatter(lines []string) *Frontmatter {
	// Find first non-blank line.
	openerIdx := -1
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		openerIdx = i
		break
	}
	if openerIdx < 0 {
		return nil
	}

	openerType := delimType(lines[openerIdx])
	if openerType == "" {
		return nil
	}

	// Search for matching closer.
	for i := openerIdx + 1; i < len(lines); i++ {
		ct := delimType(lines[i])
		if ct == openerType {
			raw := strings.Join(lines[openerIdx+1:i], "\n")
			return &Frontmatter{
				Raw:       raw,
				StartLine: openerIdx + 1, // 1-based
				EndLine:   i + 1,         // 1-based
				Format:    openerType,
			}
		}
	}

	return nil
}

// ExtractTitleFromFrontmatter extracts title/name from frontmatter raw text.
// Checks for "title" and "name" keys (simple line-level parsing, no full YAML/TOML decode).
func ExtractTitleFromFrontmatter(fm *Frontmatter) string {
	if fm == nil {
		return ""
	}
	for _, line := range strings.Split(fm.Raw, "\n") {
		trimmed := strings.TrimSpace(line)
		for _, key := range []string{"title", "name"} {
			val := extractKeyValue(trimmed, key)
			if val != "" {
				return val
			}
		}
	}
	return ""
}

// extractKeyValue checks if a line starts with "key:" or "key =" and returns the value.
// Handles quoted values and multi-line indicators (>, |) by returning "" for those.
func extractKeyValue(line, key string) string {
	// Try YAML style: "key: value" or "key:value"
	if strings.HasPrefix(line, key+":") {
		val := strings.TrimSpace(line[len(key)+1:])
		// Multi-line indicator — need first continuation line, but we only have this line.
		if val == ">" || val == "|" || val == ">-" || val == "|-" {
			return ""
		}
		return unquote(val)
	}
	// Try TOML style: "key = value"
	if strings.HasPrefix(line, key+" =") {
		val := strings.TrimSpace(line[len(key)+2:])
		return unquote(val)
	}
	return ""
}

// unquote strips surrounding quotes from a string.
func unquote(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
