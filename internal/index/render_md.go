package index

import (
	"fmt"
	"io"
	"strings"
)

// MarkdownPalette controls inline ANSI styling for terminal renders. The
// CLI passes a non-default palette only when stdout is a TTY; otherwise
// every code is empty so the output is plain text suitable for files,
// pipes, and snapshot tests.
type MarkdownPalette struct {
	Bold  string
	Dim   string
	Cyan  string
	Reset string
}

// NoColorPalette returns a palette with every code empty.
func NoColorPalette() MarkdownPalette { return MarkdownPalette{} }

// ANSIColorPalette returns the default colored palette.
func ANSIColorPalette() MarkdownPalette {
	return MarkdownPalette{
		Bold:  "\033[1m",
		Dim:   "\033[2m",
		Cyan:  "\033[36m",
		Reset: "\033[0m",
	}
}

// MarkdownSummaryMaxChars caps a summary in the markdown render so each
// line stays scannable. Full text remains in the JSON view.
const MarkdownSummaryMaxChars = 80

// RenderTreeMarkdown writes a tree-shaped TOC of fm to w. Folder header,
// bulleted file list, one-line summaries. ANSI styling is applied via
// the supplied palette (pass NoColorPalette for plain text). Distinct
// from RenderMarkdown (which serialises a CheckReport for index check).
func RenderTreeMarkdown(w io.Writer, fm *FolderMap, opts RenderOptions, palette MarkdownPalette) error {
	if fm == nil {
		_, err := fmt.Fprintln(w, "_no folder map_")
		return err
	}
	if _, err := fmt.Fprintf(w, "%ssift index%s\n", palette.Bold, palette.Reset); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Root: %s%s%s\n", palette.Dim, fm.Root, palette.Reset); err != nil {
		return err
	}
	if opts.Collection != "" {
		if _, err := fmt.Fprintf(w, "Collection: %s\n", opts.Collection); err != nil {
			return err
		}
	}
	digest := computeDigest(fm)
	if digest != "" {
		if _, err := fmt.Fprintf(w, "\n%s\n", digest); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}

	for _, f := range fm.Folders {
		if err := renderFolderMarkdown(w, f, opts, palette); err != nil {
			return err
		}
	}

	if len(fm.Errors) > 0 {
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
		for _, e := range fm.Errors {
			if _, err := fmt.Fprintf(w, "%s! %s%s\n", palette.Dim, e, palette.Reset); err != nil {
				return err
			}
		}
	}
	return nil
}

func renderFolderMarkdown(w io.Writer, f FolderNode, opts RenderOptions, p MarkdownPalette) error {
	indent := strings.Repeat("  ", f.Depth)
	header := f.Path
	if f.Path == "." {
		header = "(root)"
	}

	tag := ""
	if f.Ignore {
		tag = " [ignored]"
	} else if f.ParseError != "" {
		tag = " [parse_error]"
	} else if !f.HasIndex {
		tag = " [no sift.toml]"
	}

	wordSuffix := ""
	wc := f.Refresh.WordCount
	if wc > 0 {
		wordSuffix = fmt.Sprintf(" %s(%dw)%s", p.Dim, wc, p.Reset)
	}

	if _, err := fmt.Fprintf(w, "%s%s%s/%s%s%s\n",
		indent, p.Bold, header, p.Reset, wordSuffix, tag); err != nil {
		return err
	}

	if opts.Summaries && f.Purpose != "" {
		first := firstSentence(f.Purpose)
		if first != "" {
			if _, err := fmt.Fprintf(w, "%s  %s%s%s\n",
				indent, p.Dim, truncate(first, 200), p.Reset); err != nil {
				return err
			}
		}
	}

	for _, fl := range f.Files {
		if err := renderFileMarkdown(w, fl, indent+"  ", opts, p); err != nil {
			return err
		}
	}
	return nil
}

func renderFileMarkdown(w io.Writer, fl FileNode, indent string, opts RenderOptions, p MarkdownPalette) error {
	tag := ""
	if fl.Ignore {
		tag = " [ignored]"
	} else if !fl.Exists {
		tag = " [missing]"
	}
	kind := ""
	if fl.Kind != "" {
		kind = fmt.Sprintf(" %s.%s%s", p.Dim, fl.Kind, p.Reset)
	}

	if _, err := fmt.Fprintf(w, "%s- %s%s%s%s%s\n",
		indent, p.Cyan, fl.Name, p.Reset, kind, tag); err != nil {
		return err
	}

	if opts.Summaries && fl.Summary != "" {
		if _, err := fmt.Fprintf(w, "%s  %s%s%s\n",
			indent, p.Dim, truncate(fl.Summary, MarkdownSummaryMaxChars), p.Reset); err != nil {
			return err
		}
	}
	if opts.Sections && len(fl.Sections) > 0 {
		for _, s := range fl.Sections {
			if _, err := fmt.Fprintf(w, "%s    %s# %s (L%d-%d)%s\n",
				indent, p.Dim, s.Heading, s.StartLine, s.EndLine, p.Reset); err != nil {
				return err
			}
		}
	}
	return nil
}

// truncate cuts s at n runes (well, bytes — markdown summaries are ASCII
// most of the time, and we err on the side of fewer chars when not).
// Trailing ellipsis added when we cut.
func truncate(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= n {
		return s
	}
	if n <= 3 {
		return s[:n]
	}
	return strings.TrimRight(s[:n-3], " ") + "..."
}
