package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/spf13/cobra"

	"sift/internal/db"
	"sift/internal/keywords"
)

func newKeywordsCmd() *cobra.Command {
	var (
		collection string
		pathGlob   string
		depth      int
		topN       int
		jsonOutput bool
	)

	cmd := &cobra.Command{
		Use:   "keywords",
		Short: "Extract TF-IDF keywords per directory",
		Long: `Extract high-signal keywords per directory using filename, heading, and body weighting.

Examples:
  sift keywords
  sift keywords --collection vault
  sift keywords --path "memory/*" --depth 2 --top 8
  sift keywords --json`,
		RunE: func(cmd *cobra.Command, args []string) error {
			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			docs, err := loadKeywordDocuments(database, collection, pathGlob)
			if err != nil {
				return err
			}
			if len(docs) == 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "No indexed files matched. Run `sift refresh` first.")
				return nil
			}

			groups := keywords.Extract(docs, keywords.Options{
				Depth: depth,
				TopN:  topN,
			})

			if jsonOutput {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(map[string]any{"groups": groups})
			}

			w := cmd.OutOrStdout()
			for i, group := range groups {
				fmt.Fprintln(w, group.Directory)
				fmt.Fprintf(w, "  keywords: %s\n", strings.Join(group.Keywords, ", "))
				fmt.Fprintf(w, "  notes: %d\n", group.Notes)
				if i < len(groups)-1 {
					fmt.Fprintln(w)
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&collection, "collection", "", "Restrict to a collection")
	cmd.Flags().StringVar(&pathGlob, "path", "", "Restrict to relative file paths matching a glob")
	cmd.Flags().IntVar(&depth, "depth", 2, "Directory depth to group by")
	cmd.Flags().IntVar(&topN, "top", 8, "Top keywords per group")
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "JSON output")

	return cmd
}

func loadKeywordDocuments(database *db.DB, collectionFilter string, pathGlob string) ([]keywords.Document, error) {
	cols, err := database.ListCollections()
	if err != nil {
		return nil, fmt.Errorf("list collections: %w", err)
	}

	collectionByID := make(map[int64]db.Collection, len(cols))
	for _, col := range cols {
		collectionByID[col.ID] = col
	}

	buildDocs := func(files []db.FileRecord, relRoot db.Collection) ([]keywords.Document, error) {
		docs := make([]keywords.Document, 0, len(files))
		for _, file := range files {
			relPath, err := filepath.Rel(relRoot.Path, file.Path)
			if err != nil {
				return nil, fmt.Errorf("relative path for %s: %w", file.Path, err)
			}
			relPath = filepath.ToSlash(relPath)
			if pathGlob != "" {
				matched, err := doublestar.Match(pathGlob, relPath)
				if err != nil {
					return nil, fmt.Errorf("invalid --path glob %q: %w", pathGlob, err)
				}
				if !matched {
					continue
				}
			}

			content, err := os.ReadFile(file.Path)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: read %s: %v\n", file.Path, err)
				continue
			}
			docs = append(docs, keywords.Document{
				RelativePath: relPath,
				Content:      string(content),
			})
		}
		return docs, nil
	}

	if collectionFilter != "" {
		for _, col := range cols {
			if col.Name != collectionFilter {
				continue
			}
			files, err := database.GetFilesByCollection(col.ID)
			if err != nil {
				return nil, fmt.Errorf("get files for %s: %w", col.Name, err)
			}
			return buildDocs(files, col)
		}
		return nil, fmt.Errorf("collection %q not found", collectionFilter)
	}

	files, err := database.ListFiles()
	if err != nil {
		return nil, fmt.Errorf("list files: %w", err)
	}

	docs := make([]keywords.Document, 0, len(files))
	for _, file := range files {
		col, ok := collectionByID[file.CollectionID]
		if !ok {
			return nil, fmt.Errorf("primary collection %d missing for %s", file.CollectionID, file.Path)
		}
		fileDocs, err := buildDocs([]db.FileRecord{file}, col)
		if err != nil {
			return nil, err
		}
		docs = append(docs, fileDocs...)
	}

	return docs, nil
}
