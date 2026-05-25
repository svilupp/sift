package anchor

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// Link represents a reference from one document to another.
type Link struct {
	SourcePath    string // file containing the link
	SourceLine    int    // 1-based line where the link appears, 0 if unknown
	SourceSection string // section ID where link appears
	TargetPath    string // resolved target file path (relative)
	TargetSection string // target section ID (from #fragment), empty if none
	LinkType      string // "markdown", "wikilink", "read_command", "include", "path_ref", "frontmatter_source", "attention_lines"
	Raw           string // original link text
}

var markdownLinkRe = regexp.MustCompile(`\[([^\]]*)\]\(([^)]+)\)`)
var wikilinkRe = regexp.MustCompile(`\[\[([^\]|]+?)(?:#([^\]|]+?))?(?:\|([^\]]+?))?\]\]`)
var frontmatterSourceRe = regexp.MustCompile(`(?i)^source:\s*(.+)$`)
var readCommandRe = regexp.MustCompile("(?i)mem\\s+read\\s+([^\\s`]+)")
var includeDirectiveRe = regexp.MustCompile(`\{\{\s*include(?:-toc)?\s+([^}\s]+)\s*\}\}`)
var inlineCodeRe = regexp.MustCompile("`([^`]+)`")

var collectionRootPrefixes = []string{"docs/", "memory/", "repos/", "logs/"}
var collectionRootMarkers = []string{"/docs/", "/memory/", "/repos/", "/logs/"}

// ExtractMarkdownLinks finds [text](path) and [text](path#heading) links in markdown lines.
// Only extracts local file links (not http/https URLs).
// Resolves relative paths against the source file's directory.
func ExtractMarkdownLinks(filePath string, lines []string, sections []Section) []Link {
	var links []Link

	for i, line := range lines {
		lineNum := i + 1
		matches := markdownLinkRe.FindAllStringSubmatch(line, -1)
		for _, m := range matches {
			raw := m[0]
			target := m[2]

			// Skip external links.
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
				continue
			}

			// Split target into path and fragment.
			targetPath := target
			targetSection := ""
			if idx := strings.Index(target, "#"); idx >= 0 {
				targetPath = target[:idx]
				targetSection = Slugify(target[idx+1:])
			}

			resolved := resolveTargetPath(filePath, targetPath)
			sectionID := sectionIDAtLine(sections, lineNum)

			links = append(links, Link{
				SourcePath:    filePath,
				SourceLine:    lineNum,
				SourceSection: sectionID,
				TargetPath:    resolved,
				TargetSection: targetSection,
				LinkType:      "markdown",
				Raw:           raw,
			})
		}
	}

	return links
}

// ExtractWikilinks finds [[target]] and [[target#heading|display]] wikilinks.
func ExtractWikilinks(filePath string, lines []string, sections []Section) []Link {
	var links []Link

	for i, line := range lines {
		lineNum := i + 1
		matches := wikilinkRe.FindAllStringSubmatch(line, -1)
		for _, m := range matches {
			raw := m[0]
			targetDoc := m[1]
			fragment := m[2]

			// Add .md extension if not already present.
			targetPath := targetDoc
			if !strings.HasSuffix(targetPath, ".md") {
				targetPath += ".md"
			}

			targetSection := ""
			if fragment != "" {
				targetSection = Slugify(fragment)
			}

			sectionID := sectionIDAtLine(sections, lineNum)

			links = append(links, Link{
				SourcePath:    filePath,
				SourceLine:    lineNum,
				SourceSection: sectionID,
				TargetPath:    resolveTargetPath(filePath, targetPath),
				TargetSection: targetSection,
				LinkType:      "wikilink",
				Raw:           raw,
			})
		}
	}

	return links
}

// ExtractPathReferences finds local document references expressed as commands,
// template directives, or inline code paths.
func ExtractPathReferences(filePath string, lines []string, sections []Section) []Link {
	var links []Link
	seen := make(map[string]struct{})

	appendUnique := func(lineNum int, sectionID, target, linkType, raw string) {
		link, ok := buildReferenceLink(filePath, lineNum, sectionID, target, linkType, raw)
		if !ok {
			return
		}
		key := strings.Join([]string{
			link.SourcePath,
			fmt.Sprintf("%d", link.SourceLine),
			link.SourceSection,
			link.TargetPath,
			link.TargetSection,
			link.LinkType,
			link.Raw,
		}, "\x00")
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		links = append(links, link)
	}

	for i, line := range lines {
		lineNum := i + 1
		sectionID := sectionIDAtLine(sections, lineNum)

		for _, match := range readCommandRe.FindAllStringSubmatch(line, -1) {
			appendUnique(lineNum, sectionID, match[1], "read_command", match[0])
		}

		for _, match := range includeDirectiveRe.FindAllStringSubmatch(line, -1) {
			appendUnique(lineNum, sectionID, match[1], "include", match[0])
		}

		for _, match := range inlineCodeRe.FindAllStringSubmatch(line, -1) {
			code := strings.TrimSpace(match[1])
			if isReadCommand(code) {
				continue
			}
			appendUnique(lineNum, sectionID, code, "path_ref", match[0])
		}
	}

	return links
}

type attentionFile struct {
	Document string           `json:"document"`
	Blocks   []attentionBlock `json:"blocks"`
}

type attentionBlock struct {
	Lines   [2]int `json:"lines"` // [start, end] 1-based
	Title   string `json:"title"`
	WhyLook string `json:"why_look"`
}

// ExtractAttentionLinks reads an attention.json file and creates links from each block
// back to the source document using the "lines" field.
func ExtractAttentionLinks(jsonPath string, data []byte) ([]Link, error) {
	var af attentionFile
	if err := json.Unmarshal(data, &af); err != nil {
		return nil, fmt.Errorf("parse attention JSON: %w", err)
	}

	// Derive the source markdown path from the json path.
	sourcePath := strings.TrimSuffix(jsonPath, ".attention.json") + ".md"

	var links []Link
	for _, b := range af.Blocks {
		links = append(links, Link{
			SourcePath:    jsonPath,
			SourceLine:    0,
			SourceSection: "",
			TargetPath:    sourcePath,
			TargetSection: "",
			LinkType:      "attention_lines",
			Raw:           fmt.Sprintf("lines %d-%d: %s", b.Lines[0], b.Lines[1], b.Title),
		})
	}

	return links, nil
}

// ExtractFrontmatterSource reads a summary.md file's frontmatter "source" field
// (which contains a wikilink like [[PLAN.2026-02-12]]) and creates a link.
func ExtractFrontmatterSource(filePath string, lines []string) *Link {
	inFrontmatter := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "---" || trimmed == "+++" {
			if inFrontmatter {
				// End of frontmatter, stop.
				break
			}
			inFrontmatter = true
			continue
		}
		if !inFrontmatter {
			continue
		}

		m := frontmatterSourceRe.FindStringSubmatch(trimmed)
		if m == nil {
			continue
		}

		sourceVal := strings.TrimSpace(m[1])
		// Try to parse as wikilink.
		wm := wikilinkRe.FindStringSubmatch(sourceVal)
		if wm == nil {
			continue
		}

		targetDoc := wm[1]
		targetPath := targetDoc
		if !strings.HasSuffix(targetPath, ".md") {
			targetPath += ".md"
		}

		targetSection := ""
		if wm[2] != "" {
			targetSection = Slugify(wm[2])
		}

		return &Link{
			SourcePath:    filePath,
			SourceLine:    i + 1,
			SourceSection: "",
			TargetPath:    targetPath,
			TargetSection: targetSection,
			LinkType:      "frontmatter_source",
			Raw:           sourceVal,
		}
	}

	return nil
}

func buildReferenceLink(filePath string, lineNum int, sectionID, target, linkType, raw string) (Link, bool) {
	targetPath, targetSection, ok := splitLocalTarget(target)
	if !ok {
		return Link{}, false
	}
	return Link{
		SourcePath:    filePath,
		SourceLine:    lineNum,
		SourceSection: sectionID,
		TargetPath:    resolveTargetPath(filePath, targetPath),
		TargetSection: targetSection,
		LinkType:      linkType,
		Raw:           raw,
	}, true
}

func splitLocalTarget(target string) (string, string, bool) {
	cleaned := strings.TrimSpace(strings.Trim(target, `"'`))
	if cleaned == "" {
		return "", "", false
	}
	lower := strings.ToLower(cleaned)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		return "", "", false
	}

	targetPath := cleaned
	targetSection := ""
	if idx := strings.Index(cleaned, "#"); idx >= 0 {
		targetPath = cleaned[:idx]
		targetSection = Slugify(cleaned[idx+1:])
	}
	if targetPath == "" {
		return "", "", false
	}
	if !strings.HasSuffix(strings.ToLower(targetPath), ".md") {
		return "", "", false
	}
	return targetPath, targetSection, true
}

func sectionIDAtLine(sections []Section, lineNum int) string {
	sec := SectionAtLine(sections, lineNum)
	if sec == nil {
		return ""
	}
	return sec.ID
}

func resolveTargetPath(sourcePath, targetPath string) string {
	targetPath = strings.TrimSpace(strings.Trim(targetPath, `"'`))
	if targetPath == "" {
		return sourcePath
	}
	if filepath.IsAbs(targetPath) {
		return filepath.Clean(targetPath)
	}
	if rootRef, ok := normalizeCollectionRootRef(targetPath); ok {
		if root, found := detectCollectionRoot(sourcePath); found {
			return filepath.Clean(filepath.Join(root, filepath.FromSlash(rootRef)))
		}
	}
	return filepath.Clean(filepath.Join(filepath.Dir(sourcePath), targetPath))
}

func normalizeCollectionRootRef(targetPath string) (string, bool) {
	normalized := filepath.ToSlash(filepath.Clean(strings.TrimSpace(strings.Trim(targetPath, `"'`))))
	for strings.HasPrefix(normalized, "./") {
		normalized = strings.TrimPrefix(normalized, "./")
	}
	for strings.HasPrefix(normalized, "../") {
		normalized = strings.TrimPrefix(normalized, "../")
	}
	for _, prefix := range collectionRootPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return normalized, true
		}
	}
	return "", false
}

func detectCollectionRoot(sourcePath string) (string, bool) {
	normalized := filepath.ToSlash(filepath.Clean(sourcePath))
	for _, prefix := range collectionRootPrefixes {
		if strings.HasPrefix(normalized, prefix) {
			return "", true
		}
	}

	rootIdx := -1
	for _, marker := range collectionRootMarkers {
		idx := strings.Index(normalized, marker)
		if idx >= 0 && (rootIdx < 0 || idx < rootIdx) {
			rootIdx = idx
		}
	}
	if rootIdx < 0 {
		return "", false
	}
	return filepath.FromSlash(normalized[:rootIdx]), true
}

func isReadCommand(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return strings.HasPrefix(value, "mem read ")
}

// BuildBacklinks takes a slice of forward links and returns reverse links.
// For each link A->B, create a backlink B->A.
func BuildBacklinks(links []Link) []Link {
	var backlinks []Link
	for _, l := range links {
		backlinks = append(backlinks, Link{
			SourcePath:    l.TargetPath,
			SourceLine:    0,
			SourceSection: l.TargetSection,
			TargetPath:    l.SourcePath,
			TargetSection: l.SourceSection,
			LinkType:      l.LinkType,
			Raw:           l.Raw,
		})
	}
	return backlinks
}
