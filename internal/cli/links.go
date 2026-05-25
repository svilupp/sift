package cli

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"sift/internal/db"
)

func newLinksCmd() *cobra.Command {
	var (
		section   string
		backlinks bool
		jsonOut   bool
	)

	cmd := &cobra.Command{
		Use:   "links <file>",
		Short: "Show links from or to a file",
		Long: `Display forward links from a file/section, or backlinks pointing to it.

Examples:
  sift links ARCHITECTURE.md
  sift links ARCHITECTURE.md --section trust-zones
  sift links observability.md --backlinks
  sift links PLAN.md --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			filePath := args[0]

			// Resolve to absolute path.
			absPath, err := filepath.Abs(filePath)
			if err != nil {
				return fmt.Errorf("resolve path: %w", err)
			}

			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			w := cmd.OutOrStdout()

			var links []db.LinkRecord

			if backlinks {
				links, err = database.GetBacklinks(absPath, section)
				if err != nil {
					return fmt.Errorf("get backlinks: %w", err)
				}
			} else {
				fileRec, lookupErr := database.GetFileByPath(absPath)
				if lookupErr != nil {
					return fmt.Errorf("lookup file: %w", lookupErr)
				}
				if fileRec == nil {
					return fmt.Errorf("file %q not indexed", filePath)
				}

				if section != "" {
					links, err = database.GetLinksByFileAndSection(fileRec.ID, section)
				} else {
					links, err = database.GetLinksFromFile(fileRec.ID)
				}
				if err != nil {
					return fmt.Errorf("get links: %w", err)
				}
			}

			if jsonOut {
				type linkOutput struct {
					SourceFileID    int64  `json:"source_file_id"`
					SourceSectionID string `json:"source_section_id,omitempty"`
					SourceLine      int    `json:"source_line"`
					TargetPath      string `json:"target_path"`
					TargetSection   string `json:"target_section,omitempty"`
					LinkType        string `json:"link_type"`
					Raw             string `json:"raw,omitempty"`
				}
				out := make([]linkOutput, len(links))
				for i, l := range links {
					out[i] = linkOutput{
						SourceFileID:    l.SourceFileID,
						SourceSectionID: l.SourceSectionID,
						SourceLine:      l.SourceLine,
						TargetPath:      l.TargetPath,
						TargetSection:   l.TargetSection,
						LinkType:        l.LinkType,
						Raw:             l.Raw,
					}
				}
				enc := json.NewEncoder(w)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			if len(links) == 0 {
				if backlinks {
					fmt.Fprintf(w, "No backlinks to %s", filePath)
				} else {
					fmt.Fprintf(w, "No links from %s", filePath)
				}
				if section != "" {
					fmt.Fprintf(w, "#%s", section)
				}
				fmt.Fprintln(w)
				return nil
			}

			// Display header.
			if backlinks {
				fmt.Fprintf(w, "Backlinks to %s", shortestPath(absPath))
			} else {
				fmt.Fprintf(w, "Links from %s", shortestPath(absPath))
			}
			if section != "" {
				fmt.Fprintf(w, "#%s", section)
			}
			fmt.Fprintf(w, " (%d)\n\n", len(links))

			// Resolve file IDs to paths for backlink display.
			filePathCache := make(map[int64]string)
			resolveFilePath := func(fileID int64) string {
				if p, ok := filePathCache[fileID]; ok {
					return p
				}
				// Look up files table for the source path.
				var path string
				row := database.QueryRow("SELECT path FROM files WHERE id = ?", fileID)
				if err := row.Scan(&path); err == nil {
					filePathCache[fileID] = path
				}
				return filePathCache[fileID]
			}

			for _, l := range links {
				target := shortestPath(l.TargetPath)
				if l.TargetSection != "" {
					target += "#" + l.TargetSection
				}
				if backlinks {
					source := shortestPath(resolveFilePath(l.SourceFileID))
					if l.SourceSectionID != "" {
						source += "#" + l.SourceSectionID
					}
					fmt.Fprintf(w, "  \u2190 %s [%s] %s\n", source, l.LinkType, l.Raw)
				} else {
					fmt.Fprintf(w, "  \u2192 %s [%s]\n", target, l.LinkType)
				}
			}

			return nil
		},
	}

	cmd.Flags().StringVar(&section, "section", "", "Filter by section ID")
	cmd.Flags().BoolVar(&backlinks, "backlinks", false, "Show backlinks instead of forward links")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON output")

	return cmd
}
