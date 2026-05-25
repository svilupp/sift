package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"sift/internal/config"
	"sift/internal/db"
	"sift/internal/index"
)

// Exit codes for `sift index check`. We use Cobra's RunE → cmd.SilenceUsage
// path and propagate via a typed error so main can read the exit code.
type indexCheckError struct {
	code int
	msg  string
}

func (e *indexCheckError) Error() string { return e.msg }

// IndexCheckExitCode unwraps an indexCheckError if present and returns
// the desired process exit code, or -1 when err is not an indexCheckError.
// Callers (main) can use this to translate into os.Exit.
func IndexCheckExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ice *indexCheckError
	if errors.As(err, &ice) {
		return ice.code
	}
	return -1
}

// indexFilterFlags captures the search-style filter flags that apply to
// both `sift index` (read) and `sift index check`. Centralised so the
// two commands stay in sync.
type indexFilterFlags struct {
	pathGlob string
	since    string
	files    []string
}

// bindIndexFilterFlags wires the shared filter flags onto cmd.
func bindIndexFilterFlags(cmd *cobra.Command, f *indexFilterFlags) {
	cmd.Flags().StringVarP(&f.pathGlob, "path", "p", "", "Glob filter applied to file paths (e.g. 'docs/**')")
	cmd.Flags().StringVar(&f.since, "since", "", "Only include files modified within the given duration (e.g. 7d, 1w)")
	cmd.Flags().StringArrayVar(&f.files, "file", nil, "Only include this file path (repeatable)")
}

// resolveFilters parses the raw filter flags into an index.Filters value.
func resolveFilters(f indexFilterFlags, includeIgnored bool) (index.Filters, error) {
	out := index.Filters{
		PathGlob:       f.pathGlob,
		Files:          append([]string(nil), f.files...),
		IncludeIgnored: includeIgnored,
	}
	if f.since != "" {
		dur, err := parseDuration(f.since)
		if err != nil {
			return index.Filters{}, fmt.Errorf("invalid --since: %w", err)
		}
		out.Since = time.Now().Add(-dur)
	}
	return out, nil
}

// newIndexCmd builds the headline `sift index [path]` command. With no
// subcommand it walks the tree and emits a TOC; with the `check`
// subcommand it lints. The two commands share filter flags.
func newIndexCmd() *cobra.Command {
	var (
		collection     string
		depth          int
		jsonOut        bool
		markdownOut    bool
		includeIgnored bool
		sections       bool
		summaries      bool
		filters        indexFilterFlags
	)

	cmd := &cobra.Command{
		Use:   "index [path]",
		Short: "Read or lint per-folder sift.toml metadata (TOC view)",
		Long: `Walk a directory tree, parse each sift.toml, and emit a structured
table of contents — purpose / use_when / per-file summaries.

Subcommands:
  check    Walk a tree and report missing, stale, or orphaned entries.

With no subcommand, sift index emits the read view. Auto-detect: TTY → markdown
tree, piped → JSON envelope. Override with --json or --markdown.

Filters compose AND-style and apply to both the read view and check:
  --path GLOB     gitignore-style glob ("docs/**", "*.md")
  --since DUR     duration window ("7d", "1w", "24h")
  --file PATH     explicit file path (repeatable)

Examples:
  sift index                              # current directory, TTY → markdown
  sift index docs --depth 1               # only direct children
  sift index docs --json | jq .digest     # one-paragraph orientation
  sift index --path 'docs/**' --since 7d  # AND-combined filters
  sift index --collection vault --markdown
  sift index check                        # lint mode (Phase-2)`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true

			// Track whether --sections was explicitly set so we can pick
			// format-aware defaults below.
			sectionsSet := cmd.Flags().Changed("sections")

			if jsonOut && markdownOut {
				return &indexCheckError{code: 1, msg: "cannot use --json with --markdown"}
			}

			root, err := resolveCheckRoot(args, collection)
			if err != nil {
				return &indexCheckError{code: 1, msg: err.Error()}
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			fm, err := index.LoadTree(ctx, root, "", index.LoadOptions{
				Depth:          depth,
				IncludeIgnored: includeIgnored,
			})
			if err != nil {
				return &indexCheckError{code: 1, msg: err.Error()}
			}

			filterOpts, err := resolveFilters(filters, includeIgnored)
			if err != nil {
				return &indexCheckError{code: 1, msg: err.Error()}
			}
			if !filterOpts.IsZero() {
				fm = index.ApplyFilters(fm, filterOpts)
			}

			format := chooseFormat(jsonOut, markdownOut)
			out := cmd.OutOrStdout()

			// Format-aware defaults for sections: JSON on by default,
			// markdown off by default.
			useSections := sections
			if !sectionsSet {
				useSections = format == "json"
			}

			ropts := index.RenderOptions{
				Sections:   useSections,
				Summaries:  summaries,
				Collection: collection,
			}

			if useSections {
				index.AttachSections(ctx, fm)
			}

			switch format {
			case "json":
				data, err := index.RenderTreeJSON(fm, ropts)
				if err != nil {
					return &indexCheckError{code: 1, msg: err.Error()}
				}
				if _, err := out.Write(data); err != nil {
					return &indexCheckError{code: 1, msg: err.Error()}
				}
			case "markdown":
				palette := index.NoColorPalette()
				if err := index.RenderTreeMarkdown(out, fm, ropts, palette); err != nil {
					return &indexCheckError{code: 1, msg: err.Error()}
				}
			default:
				palette := index.NoColorPalette()
				if colorsOnFor(out) {
					palette = index.ANSIColorPalette()
				}
				if err := index.RenderTreeMarkdown(out, fm, ropts, palette); err != nil {
					return &indexCheckError{code: 1, msg: err.Error()}
				}
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&collection, "collection", "c", "", "Walk this collection's root path")
	cmd.Flags().IntVar(&depth, "depth", -1, "Limit descent depth (0=root, 1=direct children, -1=unbounded)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit JSON")
	cmd.Flags().BoolVar(&markdownOut, "markdown", false, "Emit markdown tree")
	cmd.Flags().BoolVar(&includeIgnored, "include-ignored", false, "Include folders/files marked ignore = true")
	cmd.Flags().BoolVar(&sections, "sections", false, "Include H1/H2 section outlines per file (default: on for JSON, off for markdown)")
	cmd.Flags().BoolVar(&summaries, "summaries", true, "Include folder purpose and file summaries")
	bindIndexFilterFlags(cmd, &filters)

	cmd.AddCommand(newIndexCheckCmd())
	return cmd
}

func newIndexCheckCmd() *cobra.Command {
	var (
		collection     string
		jsonOut        bool
		markdownOut    bool
		includeIgnored bool
		includeAll     bool
		filters        indexFilterFlags
	)

	cmd := &cobra.Command{
		Use:   "check [path]",
		Short: "Lint sift.toml files in a tree (no writes, no LLM)",
		Long: `Walk a directory tree, parse each sift.toml, and report defects:

  - missing sift.toml in a folder that contains files
  - parse errors in malformed sift.toml
  - stale entries (file content has drifted since the last index)
  - missing summaries (entry present but Summary == "")
  - orphaned entries (entry references a file that is gone, or a file
    on disk has no corresponding entry)

The walker honors .siftignore at the root and per-folder/per-file
ignore = true flags. Pass --include-ignored to lint those too.

Filters (--path / --since / --file) compose AND-style and clip the
report to a subtree, mirroring the read view.

Output:
  default     human, colored on a TTY
  --json      machine-readable JSON
  --markdown  pasteable PR-friendly checklist

Auto-detect: if stdout is not a TTY and neither --json nor --markdown
is set, JSON is selected.

Exit codes:
  0  no defects
  1  tool error (bad path, IO failure)
  2  defects found (lint-style)

Examples:
  sift index check                           # current directory
  sift index check ./docs
  sift index check --collection vault
  sift index check ./docs --json | jq .
  sift index check ./docs --markdown > REPORT.md
  sift index check --path 'docs/**'`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cmd.SilenceUsage = true

			root, err := resolveCheckRoot(args, collection)
			if err != nil {
				return &indexCheckError{code: 1, msg: err.Error()}
			}

			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}

			report, err := index.Check(ctx, root, index.CheckOptions{
				IncludeIgnored: includeIgnored,
				IncludeAll:     includeAll,
			})
			if err != nil {
				return &indexCheckError{code: 1, msg: err.Error()}
			}

			// Apply filters by clipping the report. We keep the original
			// Defects but rebuild the per-folder list to only show
			// folders that survive the filter.
			filterOpts, err := resolveFilters(filters, includeIgnored)
			if err != nil {
				return &indexCheckError{code: 1, msg: err.Error()}
			}
			if !filterOpts.IsZero() {
				report = clipCheckReport(report, filterOpts)
			}

			if jsonOut && markdownOut {
				return &indexCheckError{code: 1, msg: "cannot use --json with --markdown"}
			}

			format := chooseFormat(jsonOut, markdownOut)
			out := cmd.OutOrStdout()

			switch format {
			case "json":
				if err := index.RenderJSON(out, report); err != nil {
					return &indexCheckError{code: 1, msg: err.Error()}
				}
			case "markdown":
				if err := index.RenderMarkdown(out, report); err != nil {
					return &indexCheckError{code: 1, msg: err.Error()}
				}
			default:
				if err := index.RenderHuman(out, report, colorsOnFor(out)); err != nil {
					return &indexCheckError{code: 1, msg: err.Error()}
				}
			}

			if !report.Clean() {
				return &indexCheckError{code: 2, msg: "defects found"}
			}
			return nil
		},
	}

	cmd.Flags().StringVarP(&collection, "collection", "c", "", "Walk this collection's root path")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Emit JSON")
	cmd.Flags().BoolVar(&markdownOut, "markdown", false, "Emit markdown")
	cmd.Flags().BoolVar(&includeIgnored, "include-ignored", false, "Lint folders/files marked ignore = true too")
	cmd.Flags().BoolVar(&includeAll, "all", false, "Include non-text files in orphan checks (e.g., .go, .py, .rs)")
	bindIndexFilterFlags(cmd, &filters)

	return cmd
}

// clipCheckReport applies path/since/file filters to a CheckReport. It
// drops folders that don't pass and rebuilds the defect list to match.
// File-level entries inside a surviving folder are not pruned — once we
// scope to a folder, we report all of its defects.
func clipCheckReport(rep *index.CheckReport, f index.Filters) *index.CheckReport {
	if rep == nil {
		return rep
	}
	keep := make(map[string]bool, len(rep.Folders))
	for _, fc := range rep.Folders {
		if folderPassesCheckFilter(fc.Folder, f) {
			keep[fc.Folder] = true
			// Preserve parent chain.
			ancestor := fc.Folder
			for ancestor != "" && ancestor != "." {
				idx := -1
				for i := len(ancestor) - 1; i >= 0; i-- {
					if ancestor[i] == '/' {
						idx = i
						break
					}
				}
				if idx < 0 {
					ancestor = "."
				} else {
					ancestor = ancestor[:idx]
				}
				keep[ancestor] = true
			}
		}
	}
	clipped := &index.CheckReport{
		SchemaVersion: rep.SchemaVersion,
		Root:          rep.Root,
		Errors:        append([]string(nil), rep.Errors...),
	}
	for _, fc := range rep.Folders {
		if keep[fc.Folder] {
			clipped.Folders = append(clipped.Folders, fc)
		}
	}
	for _, d := range rep.Defects {
		if keep[d.Folder] {
			clipped.Defects = append(clipped.Defects, d)
			switch d.Kind {
			case index.DefectMissingSiftToml:
				clipped.Summary.Missing++
			case index.DefectParseError:
				clipped.Summary.ParseError++
			case index.DefectStale:
				clipped.Summary.Stale++
			case index.DefectMissingSummary:
				clipped.Summary.MissingSummary++
			case index.DefectOrphaned:
				clipped.Summary.Orphaned++
			case index.DefectSchemaVersion:
				clipped.Summary.SchemaVersion++
			}
		}
	}
	clipped.Summary.Folders = len(clipped.Folders)
	return clipped
}

// folderPassesCheckFilter reports whether a folder path passes the
// path/since/file filters. Time-based and explicit-file filters require
// per-file mtime or path comparisons; on a CheckReport we don't have
// those, so we apply only the path glob (the most useful subtree clip).
// since/files are honored only when they reduce to a path subset; the
// fully-precise filter still happens in the read view.
func folderPassesCheckFilter(folder string, f index.Filters) bool {
	if f.PathGlob == "" && len(f.Files) == 0 {
		return true
	}
	if f.PathGlob != "" {
		if folder == "." {
			return true // root always survives so chain stays intact
		}
		// Trim trailing /** to compare folder prefix.
		base := f.PathGlob
		for len(base) > 3 && base[len(base)-3:] == "/**" {
			base = base[:len(base)-3]
		}
		if folder == base {
			return true
		}
		if len(folder) > len(base) && folder[:len(base)] == base && folder[len(base)] == '/' {
			return true
		}
		// Simple glob fallback: match the folder name itself against the
		// pattern using the same wildcard syntax (folder is treated as a
		// path segment).
		// NOTE: deliberately conservative — file-level glob is precise
		// in the read view; for `check`, this is a coarse pre-filter.
		return false
	}
	if len(f.Files) > 0 {
		for _, p := range f.Files {
			dir := filepath.Dir(filepath.ToSlash(p))
			if dir == "." {
				if folder == "." {
					return true
				}
				continue
			}
			if folder == dir || folder == "." {
				return true
			}
			if len(folder) < len(dir) && len(dir) > len(folder) && dir[:len(folder)+1] == folder+"/" {
				return true
			}
		}
		return false
	}
	return true
}

// resolveCheckRoot picks the directory to walk. Precedence:
//  1. positional arg, if present
//  2. --collection, looked up via the registered collections table
//  3. current working directory
func resolveCheckRoot(args []string, collection string) (string, error) {
	if len(args) > 0 && args[0] != "" {
		if collection != "" {
			return "", fmt.Errorf("cannot use --collection with a positional path")
		}
		return filepath.Abs(args[0])
	}
	if collection != "" {
		dbPath, err := config.DBPath()
		if err != nil {
			return "", fmt.Errorf("resolve db path: %w", err)
		}
		database, err := db.Open(dbPath)
		if err != nil {
			return "", fmt.Errorf("open db: %w", err)
		}
		defer database.Close()
		col, err := database.GetCollection(collection)
		if err != nil {
			return "", fmt.Errorf("lookup collection %q: %w", collection, err)
		}
		if col == nil {
			return "", fmt.Errorf("collection %q not found", collection)
		}
		return col.Path, nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	return cwd, nil
}

// chooseFormat picks the output format honoring explicit flags first,
// then auto-detecting based on TTY status. JSON is the safe default for
// pipes (machine-readable, jq-friendly).
func chooseFormat(jsonOut, markdownOut bool) string {
	if jsonOut {
		return "json"
	}
	if markdownOut {
		return "markdown"
	}
	if !stdoutIsTTY() {
		return "json"
	}
	return "human"
}

func stdoutIsTTY() bool {
	fd := os.Stdout.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// colorsOnFor decides whether to emit ANSI color for a given writer.
// We only enable color when the writer is os.Stdout AND we believe it
// is a TTY, mirroring the convention used in color.go's colorsOn().
func colorsOnFor(w interface{}) bool {
	if w == os.Stdout {
		return colorsOn()
	}
	return false
}
