package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
	"github.com/spf13/cobra"

	coderef "sift/internal/ref"
)

func newRefsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "refs",
		Short: "Maintain code refs and lint document links",
		Long: `Work with repo-aware references inside markdown and plaintext docs.

Use "stamp" while authoring code refs.
Use "validate" to inspect or repair code refs.
Use "lint" to fail on broken code refs, markdown links, and anchors across a folder.`,
	}

	cmd.AddCommand(
		newRefsStampCmd(),
		newRefsValidateCmd(),
		newRefsLintCmd(),
	)

	return cmd
}

func newRefsStampCmd() *cobra.Command {
	var (
		write       bool
		jsonOut     bool
		check       bool
		tokenLength int
		patterns    []string
		collections []string
	)

	cmd := &cobra.Command{
		Use:   "stamp [files-or-directories...]",
		Short: "Add missing code-ref tokens and normalize syntax",
		Long: `Stamp code refs like internal/search/search.go:464-475 with compact validation tokens.

Examples:
  sift refs stamp CLAUDE.md --write
  sift refs stamp docs/ --check
  sift refs stamp --collection vault --path "repos/**/CLAUDE.md" --write`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if write && check {
				return fmt.Errorf("--write and --check cannot be used together")
			}
			if tokenLength < 2 || tokenLength > 8 {
				return fmt.Errorf("--token-length must be between 2 and 8")
			}

			files, roots, err := collectRefFiles(args, collections, patterns)
			if err != nil {
				return err
			}

			engine := coderef.NewEngine(coderef.Options{
				CollectionRoots: roots,
				TokenLength:     tokenLength,
			})

			reports, changed, err := runRefDocuments(files, write, func(path string, content []byte) (coderef.DocumentResult, error) {
				return engine.StampDocument(path, content)
			})
			if err != nil {
				return err
			}

			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"mode":    "stamp",
					"changed": changed,
					"files":   reports,
				})
			}

			printRefReports(cmd.OutOrStdout(), "stamp", reports)
			if check && changed > 0 {
				return fmt.Errorf("%d file(s) would change", changed)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&write, "write", false, "Rewrite files in place")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON output")
	cmd.Flags().BoolVar(&check, "check", false, "Dry-run and fail if any file would change")
	cmd.Flags().StringArrayVar(&patterns, "path", nil, "Glob(s) for collection scanning")
	cmd.Flags().StringArrayVar(&collections, "collection", nil, "Collection name(s) to scan")
	cmd.Flags().IntVar(&tokenLength, "token-length", 3, "Token length to emit")

	return cmd
}

func newRefsValidateCmd() *cobra.Command {
	var (
		jsonOut     bool
		fix         bool
		strict      bool
		window      int
		patterns    []string
		collections []string
	)

	cmd := &cobra.Command{
		Use:   "validate [files-or-directories...]",
		Short: "Validate code refs and optionally fix safe nearby shifts",
		Long: `Validate code refs only. This command does not lint markdown links or heading anchors.

Examples:
  sift refs validate PLAN.md
  sift refs validate docs/ --fix
  sift refs validate --collection vault --path "repos/**/CLAUDE.md" --strict`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			files, roots, err := collectRefFiles(args, collections, patterns)
			if err != nil {
				return err
			}

			engine := coderef.NewEngine(coderef.Options{
				CollectionRoots: roots,
				Window:          window,
			})

			reports, _, err := runRefDocuments(files, fix, func(path string, content []byte) (coderef.DocumentResult, error) {
				return engine.ValidateDocument(path, content, fix)
			})
			if err != nil {
				return err
			}

			broken := countBrokenRefReports(reports)
			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"mode":   "validate",
					"fix":    fix,
					"strict": strict,
					"broken": broken,
					"files":  reports,
				})
			}

			printRefReports(cmd.OutOrStdout(), "validate", reports)
			if strict && broken > 0 {
				return fmt.Errorf("%d broken ref(s) found", broken)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON output")
	cmd.Flags().BoolVar(&fix, "fix", false, "Apply safe auto-fixes in place")
	cmd.Flags().BoolVar(&strict, "strict", false, "Exit non-zero on stale or broken refs")
	cmd.Flags().IntVar(&window, "window", 120, "Nearby search window for shifted refs")
	cmd.Flags().StringArrayVar(&patterns, "path", nil, "Glob(s) for collection scanning")
	cmd.Flags().StringArrayVar(&collections, "collection", nil, "Collection name(s) to scan")

	return cmd
}

func newRefsLintCmd() *cobra.Command {
	var (
		jsonOut     bool
		fix         bool
		window      int
		codeOnly    bool
		docOnly     bool
		patterns    []string
		collections []string
	)

	cmd := &cobra.Command{
		Use:   "lint [files-or-directories...]",
		Short: "Lint code refs, markdown links, wikilinks, and anchors",
		Long: `Run the CI-facing reference linter.

This command checks:
  - code refs created with sift refs stamp / validate
  - markdown links like [text](file.md#anchor)
  - wikilinks like [[PLAN#Overview]]
  - local anchors like [Jump](#overview)

It exits non-zero on any unresolved file, stale code ref, or missing anchor.

Examples:
  sift refs lint docs/
  sift refs lint docs/ --fix
  sift refs lint --collection vault --path "repos/**/CLAUDE.md"
  sift refs lint --doc-only docs/`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if codeOnly && docOnly {
				return fmt.Errorf("--code-only and --doc-only cannot be used together")
			}

			files, roots, err := collectRefFiles(args, collections, patterns)
			if err != nil {
				return err
			}

			engine := coderef.NewEngine(coderef.Options{
				CollectionRoots: roots,
				Window:          window,
			})

			reports, changed, issues, err := runRefLint(files, engine, fix, codeOnly, docOnly)
			if err != nil {
				return err
			}

			if jsonOut {
				return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]any{
					"mode":    "lint",
					"fix":     fix,
					"changed": changed,
					"issues":  issues,
					"files":   reports,
				})
			}

			printLintReports(cmd.OutOrStdout(), reports, issues)
			if issues > 0 {
				return fmt.Errorf("%d lint issue(s) found", issues)
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&jsonOut, "json", false, "JSON output")
	cmd.Flags().BoolVar(&fix, "fix", false, "Apply safe code-ref fixes in place before failing on remaining issues")
	cmd.Flags().IntVar(&window, "window", 120, "Nearby search window for shifted code refs")
	cmd.Flags().BoolVar(&codeOnly, "code-only", false, "Lint code refs only")
	cmd.Flags().BoolVar(&docOnly, "doc-only", false, "Lint markdown links and anchors only")
	cmd.Flags().StringArrayVar(&patterns, "path", nil, "Glob(s) for collection scanning")
	cmd.Flags().StringArrayVar(&collections, "collection", nil, "Collection name(s) to scan")

	return cmd
}

type refResultReport struct {
	Kind           string  `json:"kind,omitempty"`
	SourceLine     int     `json:"source_line"`
	Raw            string  `json:"raw"`
	Status         string  `json:"status"`
	Message        string  `json:"message,omitempty"`
	LinkType       string  `json:"link_type,omitempty"`
	TargetPath     string  `json:"target_path,omitempty"`
	ResolvedPath   string  `json:"resolved_path,omitempty"`
	TargetSection  string  `json:"target_section,omitempty"`
	SuggestedRaw   string  `json:"suggested_raw,omitempty"`
	SuggestedStart int     `json:"suggested_start,omitempty"`
	SuggestedEnd   int     `json:"suggested_end,omitempty"`
	Confidence     float64 `json:"confidence,omitempty"`
}

type refFileReport struct {
	Path    string            `json:"path"`
	Changed bool              `json:"changed"`
	Results []refResultReport `json:"results"`
}

func runRefDocuments(
	files []string,
	write bool,
	process func(path string, content []byte) (coderef.DocumentResult, error),
) ([]refFileReport, int, error) {
	reports := make([]refFileReport, 0, len(files))
	changed := 0

	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, 0, fmt.Errorf("read %s: %w", path, err)
		}

		result, err := process(path, content)
		if err != nil {
			return nil, 0, fmt.Errorf("process %s: %w", path, err)
		}

		if write && result.Changed {
			if err := writeRefFile(path, result.Content); err != nil {
				return nil, 0, fmt.Errorf("write %s: %w", path, err)
			}
		}
		if result.Changed {
			changed++
		}

		report := refFileReport{
			Path:    path,
			Changed: result.Changed,
			Results: make([]refResultReport, 0, len(result.Results)),
		}
		for _, item := range result.Results {
			report.Results = append(report.Results, refResultReport{
				Kind:           "code_ref",
				SourceLine:     item.Ref.SourceLine,
				Raw:            item.Ref.Raw,
				Status:         string(item.Status),
				Message:        item.Message,
				TargetPath:     item.Ref.RawPath,
				ResolvedPath:   item.Ref.ResolvedPath,
				SuggestedRaw:   item.SuggestedRaw,
				SuggestedStart: item.SuggestedStart,
				SuggestedEnd:   item.SuggestedEnd,
				Confidence:     item.Confidence,
			})
		}
		reports = append(reports, report)
	}

	return reports, changed, nil
}

func runRefLint(
	files []string,
	engine *coderef.Engine,
	fix bool,
	codeOnly bool,
	docOnly bool,
) ([]refFileReport, int, int, error) {
	reports := make([]refFileReport, 0, len(files))
	changed := 0
	issues := 0

	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("read %s: %w", path, err)
		}

		report := refFileReport{
			Path:    path,
			Results: make([]refResultReport, 0),
		}

		updated := content
		if !docOnly {
			codeResult, codeErr := engine.ValidateDocument(path, updated, fix)
			if codeErr != nil {
				return nil, 0, 0, fmt.Errorf("validate code refs in %s: %w", path, codeErr)
			}
			updated = codeResult.Content
			report.Changed = codeResult.Changed
			for _, item := range codeResult.Results {
				report.Results = append(report.Results, refResultReport{
					Kind:           "code_ref",
					SourceLine:     item.Ref.SourceLine,
					Raw:            item.Ref.Raw,
					Status:         string(item.Status),
					Message:        item.Message,
					TargetPath:     item.Ref.RawPath,
					ResolvedPath:   item.Ref.ResolvedPath,
					SuggestedRaw:   item.SuggestedRaw,
					SuggestedStart: item.SuggestedStart,
					SuggestedEnd:   item.SuggestedEnd,
					Confidence:     item.Confidence,
				})
				if !isValidLintStatus(string(item.Status)) {
					issues++
				}
			}
		}

		if !codeOnly {
			linkResults, linkErr := engine.ValidateDocumentLinks(path, updated)
			if linkErr != nil {
				return nil, 0, 0, fmt.Errorf("validate document links in %s: %w", path, linkErr)
			}
			for _, item := range linkResults {
				report.Results = append(report.Results, refResultReport{
					Kind:          "doc_link",
					SourceLine:    item.Link.SourceLine,
					Raw:           item.Link.Raw,
					Status:        string(item.Status),
					Message:       item.Message,
					LinkType:      item.Link.LinkType,
					TargetPath:    shortestPath(item.Link.TargetPath),
					ResolvedPath:  item.Link.ResolvedPath,
					TargetSection: item.Link.TargetSection,
				})
				if !isValidLintStatus(string(item.Status)) {
					issues++
				}
			}
		}

		if fix && report.Changed {
			if err := writeRefFile(path, updated); err != nil {
				return nil, 0, 0, fmt.Errorf("write %s: %w", path, err)
			}
			changed++
		}
		reports = append(reports, report)
	}

	return reports, changed, issues, nil
}

func printRefReports(w io.Writer, mode string, reports []refFileReport) {
	_ = mode
	for _, report := range reports {
		fmt.Fprintf(w, "%s (%d refs", shortestPath(report.Path), len(report.Results))
		if report.Changed {
			fmt.Fprint(w, ", updated")
		}
		fmt.Fprintln(w, ")")
		for _, item := range report.Results {
			fmt.Fprintf(w, "  %s line %d: %s\n", item.Status, item.SourceLine, item.Raw)
			if item.SuggestedRaw != "" && item.SuggestedRaw != item.Raw {
				fmt.Fprintf(w, "    -> %s\n", item.SuggestedRaw)
			}
		}
	}
}

func printLintReports(w io.Writer, reports []refFileReport, issues int) {
	if issues == 0 {
		fmt.Fprintf(w, "All links and refs valid across %d file(s).\n", len(reports))
		for _, report := range reports {
			if report.Changed {
				fmt.Fprintf(w, "  updated %s\n", shortestPath(report.Path))
			}
		}
		return
	}

	for _, report := range reports {
		fileIssues := 0
		for _, item := range report.Results {
			if !isValidLintStatus(item.Status) {
				fileIssues++
			}
		}
		if fileIssues == 0 && !report.Changed {
			continue
		}

		fmt.Fprintf(w, "%s (%d issue(s)", shortestPath(report.Path), fileIssues)
		if report.Changed {
			fmt.Fprint(w, ", updated")
		}
		fmt.Fprintln(w, ")")
		for _, item := range report.Results {
			if isValidLintStatus(item.Status) {
				continue
			}
			kind := item.Kind
			if kind == "" {
				kind = "ref"
			}
			fmt.Fprintf(w, "  %s line %d [%s]: %s\n", item.Status, item.SourceLine, kind, item.Raw)
			if item.Message != "" {
				fmt.Fprintf(w, "    %s\n", item.Message)
			}
			if item.SuggestedRaw != "" && item.SuggestedRaw != item.Raw {
				fmt.Fprintf(w, "    -> %s\n", item.SuggestedRaw)
			}
		}
	}
}

func collectRefFiles(args, collections, patterns []string) ([]string, []string, error) {
	roots, err := refCollectionRoots(collections)
	if err != nil {
		return nil, nil, err
	}
	if len(roots) == 0 {
		if cwd, cwdErr := os.Getwd(); cwdErr == nil {
			roots = append(roots, cwd)
		}
	}

	if len(args) > 0 {
		files := make([]string, 0, len(args))
		extraRoots := make([]string, 0, len(args))
		for _, arg := range args {
			abs, absErr := filepath.Abs(arg)
			if absErr != nil {
				return nil, nil, fmt.Errorf("resolve path %q: %w", arg, absErr)
			}
			info, statErr := os.Stat(abs)
			if statErr != nil {
				return nil, nil, fmt.Errorf("stat %q: %w", abs, statErr)
			}
			if info.IsDir() {
				scanned, scanErr := scanRefFiles(abs, patterns)
				if scanErr != nil {
					return nil, nil, scanErr
				}
				files = append(files, scanned...)
				extraRoots = append(extraRoots, abs)
				continue
			}
			files = append(files, abs)
		}
		files = uniquePaths(files)
		if len(files) == 0 {
			return nil, nil, fmt.Errorf("no matching documents found")
		}
		return files, uniquePaths(append(roots, extraRoots...)), nil
	}

	files := make([]string, 0)
	for _, root := range roots {
		scanned, scanErr := scanRefFiles(root, patterns)
		if scanErr != nil {
			return nil, nil, scanErr
		}
		files = append(files, scanned...)
	}
	files = uniquePaths(files)
	if len(files) == 0 {
		return nil, nil, fmt.Errorf("no matching documents found")
	}
	return files, uniquePaths(roots), nil
}

func refCollectionRoots(collections []string) ([]string, error) {
	if len(collections) == 0 {
		return nil, nil
	}

	database, err := openDB()
	if err != nil {
		return nil, err
	}
	defer database.Close()

	roots := make([]string, 0, len(collections))
	for _, name := range collections {
		col, getErr := database.GetCollection(name)
		if getErr != nil {
			return nil, fmt.Errorf("get collection %q: %w", name, getErr)
		}
		if col == nil {
			return nil, fmt.Errorf("collection %q not found", name)
		}
		roots = append(roots, col.Path)
	}
	return uniquePaths(roots), nil
}

func scanRefFiles(root string, patterns []string) ([]string, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve root %q: %w", root, err)
	}

	files := make([]string, 0)
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".sift":
				return filepath.SkipDir
			}
			return nil
		}

		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if len(patterns) > 0 {
			for _, pattern := range patterns {
				matched, matchErr := doublestar.Match(pattern, rel)
				if matchErr != nil {
					return fmt.Errorf("match %q: %w", pattern, matchErr)
				}
				if matched {
					files = append(files, path)
					return nil
				}
			}
			return nil
		}

		if isRefSourceFile(path) {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	sort.Strings(files)
	return files, nil
}

func isRefSourceFile(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".mdx", ".markdown", ".txt":
		return true
	default:
		return false
	}
}

func uniquePaths(paths []string) []string {
	if len(paths) == 0 {
		return nil
	}

	seen := make(map[string]struct{}, len(paths))
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		cleaned := filepath.Clean(path)
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	sort.Strings(out)
	return out
}

func countBrokenRefReports(reports []refFileReport) int {
	count := 0
	for _, report := range reports {
		for _, item := range report.Results {
			if !isValidCodeRefStatus(item.Status) {
				count++
			}
		}
	}
	return count
}

func isValidCodeRefStatus(status string) bool {
	switch status {
	case string(coderef.StatusValidExact), string(coderef.StatusValidShifted), string(coderef.StatusValidUnchecked):
		return true
	default:
		return false
	}
}

func isValidLintStatus(status string) bool {
	if isValidCodeRefStatus(status) {
		return true
	}
	return status == string(coderef.LinkStatusValid)
}

func writeRefFile(path string, content []byte) error {
	mode := fs.FileMode(0644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}
	return os.WriteFile(path, content, mode)
}
