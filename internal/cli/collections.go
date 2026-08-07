package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"sift/internal/bm25"
	"sift/internal/config"
	"sift/internal/db"
)

func newCollectionsCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "collections",
		Short: "Manage collections",
		Long: `A collection is a folder of files that SIFT indexes and searches.
Run bare "sift collections" to list all registered collections.

Workflow:
  sift collections add notes ~/notes/   # register a folder
  sift refresh                           # index its files
  sift search "my query"                 # search across all collections`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCollectionsList(cmd, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit machine-readable JSON")

	cmd.AddCommand(
		newCollectionsAddCmd(),
		newCollectionsRemoveCmd(),
	)

	return cmd
}

func newCollectionsAddCmd() *cobra.Command {
	var tags string
	cmd := &cobra.Command{
		Use:   "add <name> <path>",
		Short: "Add a collection",
		Long: `Register a folder as a collection. The path must be an existing directory.
After adding, run "sift refresh" to index its files.

Examples:
  sift collections add vault ~/docs/vault/
  sift collections add work ~/projects/ --tags work,code`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, dir := args[0], args[1]

			absPath, err := filepath.Abs(dir)
			if err != nil {
				return fmt.Errorf("resolve path: %w", err)
			}

			info, err := os.Stat(absPath)
			if err != nil {
				return fmt.Errorf("path %q: %w", absPath, err)
			}
			if !info.IsDir() {
				return fmt.Errorf("path %q is not a directory", absPath)
			}

			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			var tagList []string
			if tags != "" {
				tagList = strings.Split(tags, ",")
			}

			col, err := database.AddCollection(name, absPath, tagList)
			if err != nil {
				return fmt.Errorf("add collection: %w", err)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Added collection %q (%s)\n", col.Name, col.Path)
			return nil
		},
	}
	cmd.Flags().StringVar(&tags, "tags", "", "Comma-separated tags")
	return cmd
}

func newCollectionsRemoveCmd() *cobra.Command {
	var force bool
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a collection",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			if !force {
				fmt.Fprintf(cmd.OutOrStdout(), "Remove collection %q and all its indexed data? [y/N] ", name)
				var answer string
				_, _ = fmt.Fscanln(cmd.InOrStdin(), &answer)
				if strings.ToLower(answer) != "y" {
					fmt.Fprintln(cmd.OutOrStdout(), "Cancelled.")
					return nil
				}
			}

			// Acquire lock after confirmation to avoid holding it during interactive input.
			fl, err := acquireLock()
			if err != nil {
				return err
			}
			defer func() { _ = fl.Unlock() }()

			cfg, err := config.Load()
			if err != nil {
				return err
			}

			database, err := openDB()
			if err != nil {
				return err
			}
			defer database.Close()

			// Clean Bleve index before removing from DB.
			col, err := database.GetCollection(name)
			if err != nil {
				return err
			}
			if col == nil {
				return fmt.Errorf("collection %q not found", name)
			}

			blevePath, err := config.BlevePath()
			if err != nil {
				return err
			}
			if _, statErr := os.Stat(blevePath); statErr == nil {
				bleveIdx, err := bm25.OpenBleve(blevePath, cfg.BM25.Analyzer)
				if err != nil {
					return fmt.Errorf("open bleve: %w", err)
				}
				defer bleveIdx.Close()

				if err := deleteOrphanBleveDocsForCollection(database, bleveIdx, col.ID, cmd.ErrOrStderr()); err != nil {
					return err
				}
			}

			if err := database.RemoveCollection(name); err != nil {
				return err
			}
			_ = database.ClearCache()

			fmt.Fprintf(cmd.OutOrStdout(), "Removed collection %q\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "Skip confirmation")
	return cmd
}

type collectionListEntry struct {
	Name      string   `json:"name"`
	Path      string   `json:"path"`
	FileCount int      `json:"file_count"`
	Tags      []string `json:"tags"`
}

func runCollectionsList(cmd *cobra.Command, jsonOut bool) error {
	database, err := openDB()
	if err != nil {
		return err
	}
	defer database.Close()

	cols, err := database.ListCollections()
	if err != nil {
		return err
	}

	if jsonOut {
		entries := make([]collectionListEntry, 0, len(cols))
		for _, col := range cols {
			fileCount, countErr := database.CollectionFileCount(col.ID)
			if countErr != nil {
				fileCount = -1
			}
			tags := make([]string, 0, len(col.Tags))
			tags = append(tags, col.Tags...)
			entries = append(entries, collectionListEntry{
				Name:      col.Name,
				Path:      col.Path,
				FileCount: fileCount,
				Tags:      tags,
			})
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]any{"collections": entries})
	}

	if len(cols) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "No collections. Use: sift collections add <name> <path>")
		return nil
	}

	w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tPATH\tFILES\tTAGS")
	for _, col := range cols {
		fileCount, err := database.CollectionFileCount(col.ID)
		if err != nil {
			fileCount = -1
		}
		tags := strings.Join(col.Tags, ",")
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\n", col.Name, col.Path, fileCount, tags)
	}
	return w.Flush()
}

func openDB() (*db.DB, error) {
	dbPath, err := config.DBPath()
	if err != nil {
		return nil, err
	}

	database, err := db.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open db: %w", err)
	}
	if err := database.Init(); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("init db schema: %w", err)
	}
	return database, nil
}

func deleteOrphanBleveDocsForCollection(database *db.DB, bleveIdx *bm25.BleveIndex, collectionID int64, errW io.Writer) error {
	files, err := database.GetFilesByCollection(collectionID)
	if err != nil {
		return fmt.Errorf("get files: %w", err)
	}

	for _, f := range files {
		memberships, err := database.GetFileCollectionIDs(f.ID)
		if err != nil {
			return fmt.Errorf("get file memberships for %s: %w", f.Path, err)
		}
		if len(memberships) > 1 {
			continue
		}

		chunks, err := database.GetChunksByFile(f.ID)
		if err != nil {
			fmt.Fprintf(errW, "Warning: get chunks for %s: %v\n", f.Path, err)
			continue
		}
		for _, c := range chunks {
			if err := bleveIdx.Delete(strconv.FormatInt(c.ID, 10)); err != nil {
				fmt.Fprintf(errW, "Warning: bleve delete: %v\n", err)
			}
		}
	}

	return nil
}
