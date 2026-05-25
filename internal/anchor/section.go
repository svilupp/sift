package anchor

import (
	"regexp"
	"strings"
)

// Section represents a heading-delimited section in a markdown file.
type Section struct {
	ID        string // slugified heading path, e.g. "trust-zones"
	Heading   string // raw heading text, e.g. "Trust Zones"
	Level     int    // heading level 1-6
	StartLine int    // 1-based line of the heading
	EndLine   int    // 1-based last line before next same-or-higher-level heading (or EOF)
	FilePath  string // relative file path
}

// FileSections holds all sections parsed from a single file.
type FileSections struct {
	Path     string
	Sections []Section
}

var headingRe = regexp.MustCompile(`^(#{1,6})\s+(.+)$`)

// slugifyStripRe matches anything that is not alphanumeric or a hyphen.
var slugifyStripRe = regexp.MustCompile(`[^a-z0-9-]+`)

// slugifyCollapseRe collapses multiple consecutive hyphens.
var slugifyCollapseRe = regexp.MustCompile(`-{2,}`)

// ParseSections reads markdown lines and returns sections ordered by position.
// Each section spans from its heading to the line before the next heading of
// same or higher level, or EOF.
func ParseSections(filePath string, lines []string) []Section {
	type rawSection struct {
		heading   string
		level     int
		startLine int
	}

	var raws []rawSection

	// Check for content before first heading.
	firstHeadingLine := -1
	for i, line := range lines {
		if headingRe.MatchString(line) {
			firstHeadingLine = i
			break
		}
	}

	if firstHeadingLine > 0 {
		// There is content before the first heading.
		hasContent := false
		for i := 0; i < firstHeadingLine; i++ {
			if strings.TrimSpace(lines[i]) != "" {
				hasContent = true
				break
			}
		}
		if hasContent {
			raws = append(raws, rawSection{
				heading:   "",
				level:     0,
				startLine: 1,
			})
		}
	}

	for i, line := range lines {
		m := headingRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		level := len(m[1])
		heading := strings.TrimSpace(m[2])
		raws = append(raws, rawSection{
			heading:   heading,
			level:     level,
			startLine: i + 1, // 1-based
		})
	}

	if len(raws) == 0 {
		return nil
	}

	totalLines := len(lines)
	sections := make([]Section, len(raws))

	for i, r := range raws {
		endLine := totalLines // default to EOF

		// Scan forward for next heading of same or higher level (lower level number).
		for j := i + 1; j < len(raws); j++ {
			if raws[j].level <= r.level || r.level == 0 {
				endLine = raws[j].startLine - 1
				break
			}
		}

		sections[i] = Section{
			ID:        Slugify(r.heading),
			Heading:   r.heading,
			Level:     r.level,
			StartLine: r.startLine,
			EndLine:   endLine,
			FilePath:  filePath,
		}
	}

	return sections
}

// Slugify converts a heading text to a URL-safe slug.
func Slugify(heading string) string {
	s := strings.ToLower(heading)
	s = slugifyStripRe.ReplaceAllString(s, "-")
	s = slugifyCollapseRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	return s
}

// SectionAtLine returns the section enclosing a given 1-based line number, or nil.
func SectionAtLine(sections []Section, line int) *Section {
	var best *Section
	for i := range sections {
		s := &sections[i]
		if line >= s.StartLine && line <= s.EndLine {
			if best == nil || s.Level > best.Level {
				best = s
			}
		}
	}
	return best
}

// MapChunkToSection maps a chunk (with StartLine/EndLine) to its best enclosing section.
// Returns the deepest (highest level number) section that contains the chunk's midpoint.
// Using the midpoint avoids misclassification caused by chunk overlap rows that
// extend before the target heading.
func MapChunkToSection(sections []Section, chunkStartLine, chunkEndLine int) *Section {
	midpoint := (chunkStartLine + chunkEndLine) / 2
	return SectionAtLine(sections, midpoint)
}
