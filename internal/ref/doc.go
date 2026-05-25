package ref

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"sift/internal/anchor"
)

var inlineCodeSpanRe = regexp.MustCompile("`[^`]*`")

type DocumentLink struct {
	SourcePath    string
	SourceLine    int
	SourceSection string
	TargetPath    string
	ResolvedPath  string
	TargetSection string
	LinkType      string
	Raw           string
}

type LinkValidationStatus string

const (
	LinkStatusValid         LinkValidationStatus = "valid"
	LinkStatusMissingFile   LinkValidationStatus = "missing_file"
	LinkStatusMissingAnchor LinkValidationStatus = "missing_anchor"
)

type LinkValidationResult struct {
	Link    DocumentLink
	Status  LinkValidationStatus
	Message string
}

func (e *Engine) ValidateDocumentLinks(sourcePath string, content []byte) ([]LinkValidationResult, error) {
	lines := strings.Split(string(content), "\n")
	linkLines := sanitizeLinesForRenderedLinks(lines)
	sections := anchor.ParseSections(sourcePath, lines)
	currentAnchors := make(map[string]struct{}, len(sections))
	for _, sec := range sections {
		if sec.ID == "" {
			continue
		}
		currentAnchors[sec.ID] = struct{}{}
	}

	links := make([]anchor.Link, 0)
	links = append(links, anchor.ExtractMarkdownLinks(sourcePath, linkLines, sections)...)
	links = append(links, anchor.ExtractWikilinks(sourcePath, linkLines, sections)...)
	links = append(links, anchor.ExtractPathReferences(sourcePath, linkLines, sections)...)
	if fm := anchor.ExtractFrontmatterSource(sourcePath, lines); fm != nil {
		links = append(links, *fm)
	}

	results := make([]LinkValidationResult, 0, len(links))
	for _, link := range links {
		result := LinkValidationResult{
			Link: DocumentLink{
				SourcePath:    link.SourcePath,
				SourceLine:    link.SourceLine,
				SourceSection: link.SourceSection,
				TargetPath:    link.TargetPath,
				ResolvedPath:  link.TargetPath,
				TargetSection: link.TargetSection,
				LinkType:      link.LinkType,
				Raw:           link.Raw,
			},
			Status: LinkStatusValid,
		}

		if filepath.Clean(link.TargetPath) == filepath.Clean(sourcePath) {
			if link.TargetSection != "" {
				if _, ok := currentAnchors[link.TargetSection]; !ok {
					result.Status = LinkStatusMissingAnchor
					result.Message = "target anchor does not exist"
				}
			}
			results = append(results, result)
			continue
		}

		info, err := os.Stat(link.TargetPath)
		if err != nil || info.IsDir() {
			result.Status = LinkStatusMissingFile
			result.Message = "target document does not exist"
			results = append(results, result)
			continue
		}

		if link.TargetSection != "" {
			anchors, anchorErr := e.sectionSet(link.TargetPath)
			if anchorErr != nil {
				result.Status = LinkStatusMissingFile
				result.Message = fmt.Sprintf("read target document: %v", anchorErr)
				results = append(results, result)
				continue
			}
			if _, ok := anchors[link.TargetSection]; !ok {
				result.Status = LinkStatusMissingAnchor
				result.Message = "target anchor does not exist"
				results = append(results, result)
				continue
			}
		}

		results = append(results, result)
	}

	return results, nil
}

func sanitizeLinesForRenderedLinks(lines []string) []string {
	out := make([]string, len(lines))
	inFence := false

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			out[i] = ""
			continue
		}
		if inFence {
			out[i] = ""
			continue
		}
		out[i] = inlineCodeSpanRe.ReplaceAllString(line, "")
	}

	return out
}

func (e *Engine) sectionSet(path string) (map[string]struct{}, error) {
	path = filepath.Clean(path)
	if e.sections == nil {
		e.sections = make(map[string]map[string]struct{})
	}
	if set, ok := e.sections[path]; ok {
		return set, nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	sections := anchor.ParseSections(path, lines)
	set := make(map[string]struct{}, len(sections))
	for _, sec := range sections {
		if sec.ID == "" {
			continue
		}
		set[sec.ID] = struct{}{}
	}
	e.sections[path] = set
	return set, nil
}
