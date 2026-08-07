package index

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"sift/internal/anchor"
	"sift/internal/chunk"
)

// SectionMaxBytes caps the size of a file we will read for live section
// extraction. Files larger than this get an empty Sections slice (rather
// than an error) so AttachSections stays cheap on a sprawling tree.
const SectionMaxBytes = 4 << 20 // 4 MB

// AttachSections walks a FolderMap and fills FileNode.Sections for every
// markdown-like file that exists on disk. It never writes; it only reads
// the file's raw content and parses headings via anchor.ParseSections.
//
// Files whose Kind is not text (per IsTextKind) are left alone so callers
// can treat nil as "did not attempt extraction." Errors are recorded in
// fm.Errors but never short-circuit — section attach is a best-effort
// enrichment, not a correctness step.
func AttachSections(ctx context.Context, fm *FolderMap) {
	if fm == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for fi := range fm.Folders {
		if err := ctx.Err(); err != nil {
			fm.Errors = append(fm.Errors, fmt.Sprintf("sections: %v", err))
			return
		}
		folder := &fm.Folders[fi]
		for fj := range folder.Files {
			file := &folder.Files[fj]
			if !file.Exists {
				continue
			}
			if !IsTextKind(file.Kind) {
				continue
			}
			if file.Bytes > SectionMaxBytes {
				continue
			}
			abs := filepath.Join(folder.AbsPath, file.Name)
			data, err := os.ReadFile(abs)
			if err != nil {
				fm.Errors = append(fm.Errors,
					fmt.Sprintf("sections %s: %v", file.Path, err))
				continue
			}
			lines := strings.Split(string(data), "\n")
			file.Title = chunk.ExtractTitle(lines)
			if file.Title == "" {
				file.Title = titleFromFilename(file.Name)
			}
			if file.Words == 0 {
				file.Words = len(strings.Fields(string(data)))
			}
			file.Excerpt = extractExcerpt(lines)
			parsed := anchor.ParseSections(file.Path, lines)
			if len(parsed) == 0 {
				// Empty (not nil) communicates "we tried, found none".
				file.Sections = []SectionNode{}
				continue
			}
			out := make([]SectionNode, 0, len(parsed))
			for _, s := range parsed {
				if s.Level == 0 || s.Heading == "" {
					// Skip preamble / synthetic sections; renderers
					// only show H1/H2 by default.
					continue
				}
				if s.Level > 2 {
					continue
				}
				out = append(out, SectionNode{
					Heading:   s.Heading,
					Level:     s.Level,
					StartLine: s.StartLine,
					EndLine:   s.EndLine,
				})
			}
			file.Sections = out
		}
	}
}

func titleFromFilename(name string) string {
	base := strings.TrimSuffix(filepath.Base(name), filepath.Ext(name))
	base = strings.NewReplacer("-", " ", "_", " ").Replace(base)
	return strings.TrimSpace(base)
}

// extractExcerpt returns a short verbatim prose paragraph. It is deliberately
// extractive: local-only orientation should add context without inventing it.
func extractExcerpt(lines []string) string {
	start := 0
	if fm := chunk.ParseFrontmatter(lines); fm != nil {
		start = fm.EndLine
	}
	var parts []string
	inFence := false
	for _, raw := range lines[start:] {
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		if line == "" {
			if len(parts) > 0 {
				break
			}
			continue
		}
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "|") ||
			strings.HasPrefix(line, "<!--") || strings.HasPrefix(line, "[![") ||
			strings.HasPrefix(line, "![") {
			continue
		}
		parts = append(parts, line)
	}
	return truncate(strings.Join(parts, " "), 280)
}
