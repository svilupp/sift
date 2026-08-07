package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"sift/internal/anchor"
)

type readEnvelope struct {
	Collection string `json:"collection,omitempty"`
	File       string `json:"file"`
	Section    string `json:"section,omitempty"`
	StartLine  int    `json:"start_line"`
	EndLine    int    `json:"end_line"`
	Content    string `json:"content"`
}

// newReadCmd provides the final step of the agent navigation contract:
// orient, search, then read an exact file/section without relying on an
// editor-specific command or a second tool.
func newReadCmd() *cobra.Command {
	var (
		collection string
		section    string
		startLine  int
		endLine    int
		jsonOut    bool
	)
	cmd := &cobra.Command{
		Use:   "read <file>",
		Short: "Read a file or Markdown section",
		Long: `Read an exact file, line range, or Markdown section. With --collection,
the file path is relative to that collection. This command is local-only and
does not require the daemon or any API key.

Examples:
  sift read docs/architecture.md --collection vault
  sift read docs/architecture.md --collection vault --section "Data Flow"
  sift read docs/architecture.md --collection vault --start-line 40 --end-line 80
  sift read docs/architecture.md --collection vault --section data-flow --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true
			if section != "" && (cmd.Flags().Changed("start-line") || cmd.Flags().Changed("end-line")) {
				return fmt.Errorf("cannot combine --section with --start-line or --end-line")
			}
			path, displayPath, err := resolveReadTarget(args[0], collection)
			if err != nil {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %q: %w", displayPath, err)
			}
			lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
			if len(lines) > 0 && lines[len(lines)-1] == "" {
				lines = lines[:len(lines)-1]
			}
			if len(lines) == 0 {
				lines = []string{""}
			}

			start, end := startLine, endLine
			matchedSection := ""
			if section != "" {
				wantSlug := anchor.Slugify(section)
				for _, candidate := range anchor.ParseSections(displayPath, lines) {
					if strings.EqualFold(candidate.Heading, section) || candidate.ID == wantSlug || unnumberedSectionSlug(candidate.Heading) == wantSlug {
						start, end = candidate.StartLine, candidate.EndLine
						matchedSection = candidate.Heading
						break
					}
				}
				if start == 0 {
					return fmt.Errorf("section %q not found in %q", section, displayPath)
				}
			}
			if start == 0 {
				start = 1
			}
			if end == 0 || end > len(lines) {
				end = len(lines)
			}
			if start < 1 || end < start || start > len(lines) {
				return fmt.Errorf("invalid line range %d-%d for %q (%d lines)", start, end, displayPath, len(lines))
			}
			content := strings.Join(lines[start-1:end], "\n")
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				enc.SetEscapeHTML(false)
				return enc.Encode(readEnvelope{
					Collection: collection,
					File:       filepath.ToSlash(displayPath),
					Section:    matchedSection,
					StartLine:  start,
					EndLine:    end,
					Content:    content,
				})
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), content)
			return err
		},
	}
	cmd.Flags().StringVarP(&collection, "collection", "c", "", "Resolve file relative to this collection")
	cmd.Flags().StringVar(&section, "section", "", "Read a Markdown heading by title or slug")
	cmd.Flags().IntVar(&startLine, "start-line", 0, "First line to read (1-based)")
	cmd.Flags().IntVar(&endLine, "end-line", 0, "Last line to read (1-based, default: EOF)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable JSON")
	return cmd
}

func unnumberedSectionSlug(heading string) string {
	heading = strings.TrimSpace(heading)
	i := 0
	for i < len(heading) && heading[i] >= '0' && heading[i] <= '9' {
		i++
	}
	if i == 0 {
		return ""
	}
	rest := strings.TrimLeft(heading[i:], " \t.):-—")
	return anchor.Slugify(rest)
}

func resolveReadTarget(arg, collection string) (string, string, error) {
	if collection == "" {
		abs, err := filepath.Abs(arg)
		if err != nil {
			return "", "", fmt.Errorf("resolve path: %w", err)
		}
		return abs, arg, nil
	}
	root, rel, err := resolveIndexTarget([]string{arg}, collection)
	if err != nil {
		return "", "", err
	}
	return filepath.Join(root, rel), rel, nil
}
