package index

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"sift/internal/fileutil"
	"sift/internal/ignore"
)

// CheckOptions controls a Check tree walk.
type CheckOptions struct {
	// IncludeIgnored, when true, descends into folders that have
	// `ignore = true` in their `sift.toml` and reports child files even
	// when the per-file `ignore` flag is set. By default such folders and
	// files are skipped (their entries appear under FolderCheck.IgnoredFiles).
	IncludeIgnored bool

	// MaxFileBytes caps the size of a file we will hash for staleness
	// checks. Files larger than this are reported as stale only when their
	// stored content_hash is empty; otherwise they are skipped to keep the
	// lint pass cheap. Zero disables the cap (always hash).
	MaxFileBytes int64

	// IncludeAll, when true, treats every regular file on disk as a
	// candidate for orphan-detection regardless of extension. By default
	// (false), only files matching the indexable-text allowlist (see
	// fileutil.IsIndexableText) participate in orphan checks; binary and
	// source-code files are silently skipped so a mixed code+docs tree
	// does not produce a sea of false orphan defects. Files explicitly
	// listed in `sift.toml` are always checked regardless of this flag.
	IncludeAll bool

	// RequireSummary makes an empty per-file summary a lint defect. It is
	// opt-in because mechanical, fully local indexes intentionally leave the
	// editorial summary field empty.
	RequireSummary bool
}

// Defect categories. Stable strings; both human and JSON output use them.
const (
	DefectMissingSiftToml = "missing_sift_toml"
	DefectParseError      = "parse_error"
	DefectStale           = "stale"
	DefectMissingSummary  = "missing_summary"
	DefectOrphaned        = "orphaned"
	DefectSchemaVersion   = "schema_version"
)

// Defect is a single issue surfaced by Check. Folder is the relative
// path of the folder (from the walked root, "." for the root). Path is
// the relative path of the offending file (from the same root) when the
// defect is file-scoped; empty when folder-scoped.
type Defect struct {
	Kind   string `json:"kind"`
	Folder string `json:"folder"`
	Path   string `json:"path,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// FolderCheck is the per-folder slice of the report. Lists are sorted
// alphabetically so output is deterministic.
type FolderCheck struct {
	// Folder is the relative path from the walk root ("." for root).
	Folder string `json:"folder"`

	// HasIndex is true when a sift.toml exists in this folder. If false
	// and the folder contains files, Missing reports the absence.
	HasIndex bool `json:"has_index"`

	// ParseError holds the human-readable error if sift.toml failed to
	// parse. When set, no file-level checks ran for this folder.
	ParseError string `json:"parse_error,omitempty"`

	Missing        bool     `json:"missing,omitempty"`
	Stale          []string `json:"stale,omitempty"`
	MissingSummary []string `json:"missing_summary,omitempty"`
	Orphaned       []string `json:"orphaned,omitempty"`
	IgnoredFiles   []string `json:"ignored_files,omitempty"`
}

// Summary holds totals across all folders.
type Summary struct {
	Folders        int `json:"folders"`
	Missing        int `json:"missing"`
	ParseError     int `json:"parse_error"`
	Stale          int `json:"stale"`
	MissingSummary int `json:"missing_summary"`
	Orphaned       int `json:"orphaned"`
	SchemaVersion  int `json:"schema_version"`
}

// CheckReport is the structured output of a Check pass. JSON layout is
// stable; the human renderer derives from the same data.
type CheckReport struct {
	SchemaVersion int           `json:"schema_version"`
	Root          string        `json:"root"`
	Folders       []FolderCheck `json:"folders"`
	Defects       []Defect      `json:"defects"`
	Summary       Summary       `json:"summary"`
	Errors        []string      `json:"errors,omitempty"`
}

// Clean reports whether the report has any defects at all.
func (r *CheckReport) Clean() bool {
	if r == nil {
		return true
	}
	s := r.Summary
	return s.Missing == 0 &&
		s.ParseError == 0 &&
		s.Stale == 0 &&
		s.MissingSummary == 0 &&
		s.Orphaned == 0 &&
		s.SchemaVersion == 0 &&
		len(r.Errors) == 0
}

// Check walks the directory tree at root, parses each `sift.toml` it
// finds, and produces a CheckReport describing every defect. The walk
// honors `.siftignore` at the root and per-folder `ignore = true` in
// `sift.toml` (unless opts.IncludeIgnored is set).
//
// Check never writes. It returns an error only on signal-level
// conditions: the root not existing, a context cancellation, or a
// catastrophic walk error. Per-file/folder problems are reported as
// defects.
func Check(ctx context.Context, root string, opts CheckOptions) (*CheckReport, error) {
	if root == "" {
		return nil, errors.New("check: empty root")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("check: resolve root: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("check: stat root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("check: root %q is not a directory", abs)
	}

	patterns, ierr := ignore.LoadPatterns(abs)
	if ierr != nil {
		return nil, fmt.Errorf("check: load .siftignore: %w", ierr)
	}

	report := &CheckReport{
		SchemaVersion: SchemaVersion,
		Root:          abs,
	}

	if err := walkTree(ctx, abs, patterns, opts, report); err != nil {
		return nil, err
	}

	// Deterministic output ordering.
	sort.Slice(report.Folders, func(i, j int) bool {
		return report.Folders[i].Folder < report.Folders[j].Folder
	})
	sort.Slice(report.Defects, func(i, j int) bool {
		a, b := report.Defects[i], report.Defects[j]
		if a.Folder != b.Folder {
			return a.Folder < b.Folder
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Path < b.Path
	})
	report.Summary.Folders = len(report.Folders)
	return report, nil
}

// walkTree performs the recursive descent. We do it manually rather than
// via filepath.WalkDir so per-folder `ignore = true` can prune subtrees
// before we even read them.
func walkTree(ctx context.Context, root string, patterns []string, opts CheckOptions, report *CheckReport) error {
	type item struct {
		abs string
		rel string
	}
	stack := []item{{abs: root, rel: "."}}

	for len(stack) > 0 {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("check: %w", err)
		}
		// Pop.
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]

		entries, err := os.ReadDir(cur.abs)
		if err != nil {
			report.Errors = append(report.Errors,
				fmt.Sprintf("read folder %s: %v", cur.rel, err))
			continue
		}

		var (
			subdirs      []os.DirEntry
			fileNames    []string
			allFileNames []string
			hasIndex     bool
		)
		for _, e := range entries {
			name := e.Name()
			if name == FilenameSiftToml {
				hasIndex = true
				continue
			}
			// .siftignore is the ignore-rules file itself — it is not
			// indexed content and should never be flagged as an orphan.
			if name == ".siftignore" {
				continue
			}
			abs := filepath.Join(cur.abs, name)
			if patterns != nil && ignore.ShouldIgnore(abs, root, patterns) {
				continue
			}
			if e.IsDir() {
				// Skip hidden and well-known noise dirs explicitly.
				if name == ".git" || name == ".sift" {
					continue
				}
				subdirs = append(subdirs, e)
				continue
			}
			// Regular files only.
			if !e.Type().IsRegular() {
				continue
			}
			// Track every regular file (used to honor explicit sift.toml
			// entries regardless of extension), but only treat
			// indexable-text files as orphan candidates by default.
			allFileNames = append(allFileNames, name)
			if opts.IncludeAll || fileutil.IsIndexableText(name) {
				fileNames = append(fileNames, name)
			}
		}

		fc := FolderCheck{
			Folder:   cur.rel,
			HasIndex: hasIndex,
		}

		var folderIdx *FolderIndex
		if hasIndex {
			tomlPath := filepath.Join(cur.abs, FilenameSiftToml)
			parsed, perr := Load(tomlPath)
			if perr != nil {
				fc.ParseError = perr.Error()
				report.Defects = append(report.Defects, Defect{
					Kind:   DefectParseError,
					Folder: cur.rel,
					Path:   FilenameSiftToml,
					Detail: perr.Error(),
				})
				report.Summary.ParseError++
				// Schema-version-only failures still count as parse_error
				// — we cannot rely on the folderIdx to do further checks.
				report.Folders = append(report.Folders, fc)
				// Still descend into subfolders even when parse fails.
				for _, sd := range subdirs {
					stack = append(stack, item{
						abs: filepath.Join(cur.abs, sd.Name()),
						rel: relJoin(cur.rel, sd.Name()),
					})
				}
				continue
			}
			folderIdx = parsed
			if parsed.SchemaVersion > SchemaVersion {
				detail := fmt.Sprintf("schema_version %d > supported %d",
					parsed.SchemaVersion, SchemaVersion)
				report.Defects = append(report.Defects, Defect{
					Kind:   DefectSchemaVersion,
					Folder: cur.rel,
					Path:   FilenameSiftToml,
					Detail: detail,
				})
				report.Summary.SchemaVersion++
			}
			if parsed.Ignore && !opts.IncludeIgnored {
				// Folder marked ignore — surface as a single ignored entry
				// and skip descent + per-file checks.
				fc.IgnoredFiles = []string{"<all>"}
				report.Folders = append(report.Folders, fc)
				continue
			}
		} else {
			// Missing sift.toml: only report when the folder has at least
			// one indexable file. An empty / pure-container folder is fine.
			// By default this only counts text-like files (the IsIndexableText
			// allowlist), so a code-only folder does not get flagged.
			if len(fileNames) > 0 {
				fc.Missing = true
				report.Defects = append(report.Defects, Defect{
					Kind:   DefectMissingSiftToml,
					Folder: cur.rel,
					Path:   FilenameSiftToml,
					Detail: fmt.Sprintf("%d file(s) present without sift.toml", len(fileNames)),
				})
				report.Summary.Missing++
			}
		}

		if folderIdx != nil {
			// Files explicitly listed in sift.toml are always checked for
			// existence regardless of extension; the orphan walk uses the
			// indexable-text-filtered set unless IncludeAll is set.
			checkFiles(cur.abs, cur.rel, fileNames, allFileNames, folderIdx, opts, &fc, report)
		}

		// Always record the folder, even when clean — useful for renderer.
		report.Folders = append(report.Folders, fc)

		// Push subdirectories for descent (depth-first, alphabetic).
		sort.Slice(subdirs, func(i, j int) bool {
			return subdirs[i].Name() > subdirs[j].Name() // reverse for stack
		})
		for _, sd := range subdirs {
			child := relJoin(cur.rel, sd.Name())
			// If parent's child folder entry is ignore=true and we are not
			// including ignored, skip the descent.
			if folderIdx != nil && !opts.IncludeIgnored {
				if cf, ok := folderIdx.Folders[sd.Name()]; ok && cf.Ignore {
					continue
				}
			}
			stack = append(stack, item{
				abs: filepath.Join(cur.abs, sd.Name()),
				rel: child,
			})
		}
	}

	return nil
}

// checkFiles compares a folder's on-disk files against its sift.toml
// entries. Three classes of defect emerge:
//   - file on disk but not in sift.toml (orphan_disk → reported as
//     orphaned with detail "missing entry"),
//   - entry in sift.toml but no file on disk (orphan_entry → reported
//     as orphaned with detail "missing file"),
//   - file present in both but stale per freshness.Decide,
//   - file present in both with empty summary → MissingSummary.
//
// Both orphan flavors share the "orphaned" defect kind to match the spec
// in PLAN.md (a single Orphaned bucket per FolderCheck).
func checkFiles(folderAbs, folderRel string, orphanCandidateNames, allDiskNames []string, idx *FolderIndex, opts CheckOptions, fc *FolderCheck, report *CheckReport) {
	// Build a quick lookup of every regular file on disk so explicit
	// sift.toml entries can verify existence regardless of extension.
	onDisk := make(map[string]struct{}, len(allDiskNames))
	for _, n := range allDiskNames {
		onDisk[n] = struct{}{}
	}

	// Pass 1: every entry in sift.toml.
	tomlNames := make([]string, 0, len(idx.Files))
	for name := range idx.Files {
		tomlNames = append(tomlNames, name)
	}
	sort.Strings(tomlNames)

	for _, name := range tomlNames {
		entry := idx.Files[name]
		if entry.Ignore && !opts.IncludeIgnored {
			fc.IgnoredFiles = append(fc.IgnoredFiles, name)
			continue
		}

		_, present := onDisk[name]
		if !present {
			fc.Orphaned = append(fc.Orphaned, name)
			report.Defects = append(report.Defects, Defect{
				Kind:   DefectOrphaned,
				Folder: folderRel,
				Path:   name,
				Detail: "entry references file missing on disk",
			})
			report.Summary.Orphaned++
			continue
		}

		// File present on both sides — check freshness.
		abs := filepath.Join(folderAbs, name)
		if opts.MaxFileBytes > 0 {
			if info, err := os.Stat(abs); err == nil && info.Size() > opts.MaxFileBytes {
				// Skip hashing for very large files; only flag as stale
				// when we have no stored hash to compare against.
				if entry.ContentHash == "" {
					fc.Stale = append(fc.Stale, name)
					report.Defects = append(report.Defects, Defect{
						Kind:   DefectStale,
						Folder: folderRel,
						Path:   name,
						Detail: "no stored content_hash; file too large to hash inline",
					})
					report.Summary.Stale++
				}
				if opts.RequireSummary && entry.Summary == "" {
					fc.MissingSummary = append(fc.MissingSummary, name)
					report.Defects = append(report.Defects, Defect{
						Kind:   DefectMissingSummary,
						Folder: folderRel,
						Path:   name,
						Detail: "summary is empty",
					})
					report.Summary.MissingSummary++
				}
				continue
			}
		}

		sig, sigErr := SigOf(abs)
		if sigErr != nil {
			report.Errors = append(report.Errors,
				fmt.Sprintf("hash %s: %v", filepath.Join(folderRel, name), sigErr))
			continue
		}
		entryCopy := entry
		decision := Decide(&entryCopy, &sig)
		switch decision {
		case DecisionRegenerate, DecisionUpdateMechanical:
			fc.Stale = append(fc.Stale, name)
			report.Defects = append(report.Defects, Defect{
				Kind:   DefectStale,
				Folder: folderRel,
				Path:   name,
				Detail: fmt.Sprintf("content drift: %s", decision),
			})
			report.Summary.Stale++
		}

		if opts.RequireSummary && entry.Summary == "" {
			fc.MissingSummary = append(fc.MissingSummary, name)
			report.Defects = append(report.Defects, Defect{
				Kind:   DefectMissingSummary,
				Folder: folderRel,
				Path:   name,
				Detail: "summary is empty",
			})
			report.Summary.MissingSummary++
		}
	}

	// Pass 2: every (orphan-candidate) file on disk that isn't in sift.toml.
	// orphanCandidateNames has already been filtered through IsIndexableText
	// (unless --all was passed), so binary/code files are silently skipped.
	disk := make([]string, 0, len(orphanCandidateNames))
	disk = append(disk, orphanCandidateNames...)
	sort.Strings(disk)
	for _, name := range disk {
		if _, ok := idx.Files[name]; ok {
			continue
		}
		fc.Orphaned = append(fc.Orphaned, name)
		report.Defects = append(report.Defects, Defect{
			Kind:   DefectOrphaned,
			Folder: folderRel,
			Path:   name,
			Detail: "file on disk has no entry in sift.toml",
		})
		report.Summary.Orphaned++
	}

	sort.Strings(fc.Stale)
	sort.Strings(fc.MissingSummary)
	sort.Strings(fc.Orphaned)
	sort.Strings(fc.IgnoredFiles)
}

func relJoin(base, name string) string {
	if base == "." || base == "" {
		return name
	}
	return filepath.ToSlash(filepath.Join(base, name))
}

// RenderJSON writes a stable JSON representation of the report to w.
func RenderJSON(w io.Writer, report *CheckReport) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report); err != nil {
		return fmt.Errorf("render json: %w", err)
	}
	return nil
}

// RenderHuman writes a colored, human-friendly summary to w. The color
// argument toggles ANSI escapes; callers should pass false when the
// destination is not a TTY.
func RenderHuman(w io.Writer, report *CheckReport, color bool) error {
	c := newPainter(color)
	if report == nil {
		_, err := fmt.Fprintln(w, "no report")
		return err
	}
	if _, err := fmt.Fprintf(w, "%s %s\n", c.bold("Root:"), report.Root); err != nil {
		return err
	}
	if report.Clean() {
		if _, err := fmt.Fprintln(w, c.green("clean: no defects")); err != nil {
			return err
		}
	}
	for _, fc := range report.Folders {
		if folderClean(fc) {
			continue
		}
		if _, err := fmt.Fprintf(w, "\n%s %s\n", c.bold("Folder:"), fc.Folder); err != nil {
			return err
		}
		if fc.Missing {
			if _, err := fmt.Fprintf(w, "  %s sift.toml missing\n", c.red("missing:")); err != nil {
				return err
			}
		}
		if fc.ParseError != "" {
			if _, err := fmt.Fprintf(w, "  %s %s\n", c.red("parse_error:"), fc.ParseError); err != nil {
				return err
			}
		}
		for _, p := range fc.Stale {
			if _, err := fmt.Fprintf(w, "  %s %s\n", c.yellow("stale:"), p); err != nil {
				return err
			}
		}
		for _, p := range fc.MissingSummary {
			if _, err := fmt.Fprintf(w, "  %s %s\n", c.yellow("missing_summary:"), p); err != nil {
				return err
			}
		}
		for _, p := range fc.Orphaned {
			if _, err := fmt.Fprintf(w, "  %s %s\n", c.red("orphaned:"), p); err != nil {
				return err
			}
		}
	}
	for _, e := range report.Errors {
		if _, err := fmt.Fprintf(w, "%s %s\n", c.red("error:"), e); err != nil {
			return err
		}
	}
	if _, err := fmt.Fprintf(w, "\n%s %d folders | %d missing | %d parse | %d stale | %d missing_summary | %d orphaned\n",
		c.bold("Summary:"),
		report.Summary.Folders,
		report.Summary.Missing,
		report.Summary.ParseError,
		report.Summary.Stale,
		report.Summary.MissingSummary,
		report.Summary.Orphaned,
	); err != nil {
		return err
	}
	return nil
}

// RenderMarkdown writes a markdown-formatted report — suitable for
// pasting into a PR description.
func RenderMarkdown(w io.Writer, report *CheckReport) error {
	if report == nil {
		_, err := fmt.Fprintln(w, "_no report_")
		return err
	}
	if _, err := fmt.Fprintf(w, "# sift index check\n\nRoot: `%s`\n\n", report.Root); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w,
		"| metric | count |\n|---|---|\n| folders | %d |\n| missing sift.toml | %d |\n| parse errors | %d |\n| stale | %d |\n| missing summary | %d |\n| orphaned | %d |\n\n",
		report.Summary.Folders,
		report.Summary.Missing,
		report.Summary.ParseError,
		report.Summary.Stale,
		report.Summary.MissingSummary,
		report.Summary.Orphaned,
	); err != nil {
		return err
	}
	if report.Clean() {
		_, err := fmt.Fprintln(w, "_No defects detected._")
		return err
	}
	for _, fc := range report.Folders {
		if folderClean(fc) {
			continue
		}
		if _, err := fmt.Fprintf(w, "## `%s`\n\n", fc.Folder); err != nil {
			return err
		}
		if fc.Missing {
			if _, err := fmt.Fprintln(w, "- [ ] **missing**: `sift.toml` not present"); err != nil {
				return err
			}
		}
		if fc.ParseError != "" {
			if _, err := fmt.Fprintf(w, "- [ ] **parse_error**: %s\n", fc.ParseError); err != nil {
				return err
			}
		}
		for _, p := range fc.Stale {
			if _, err := fmt.Fprintf(w, "- [ ] **stale**: `%s`\n", p); err != nil {
				return err
			}
		}
		for _, p := range fc.MissingSummary {
			if _, err := fmt.Fprintf(w, "- [ ] **missing_summary**: `%s`\n", p); err != nil {
				return err
			}
		}
		for _, p := range fc.Orphaned {
			if _, err := fmt.Fprintf(w, "- [ ] **orphaned**: `%s`\n", p); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	for _, e := range report.Errors {
		if _, err := fmt.Fprintf(w, "> error: %s\n", e); err != nil {
			return err
		}
	}
	return nil
}

func folderClean(fc FolderCheck) bool {
	return !fc.Missing &&
		fc.ParseError == "" &&
		len(fc.Stale) == 0 &&
		len(fc.MissingSummary) == 0 &&
		len(fc.Orphaned) == 0
}

// painter wraps text with ANSI codes when enabled.
type painter struct {
	on bool
}

func newPainter(on bool) painter { return painter{on: on} }

func (p painter) wrap(s, code string) string {
	if !p.on {
		return s
	}
	return code + s + "\033[0m"
}
func (p painter) bold(s string) string   { return p.wrap(s, "\033[1m") }
func (p painter) red(s string) string    { return p.wrap(s, "\033[31m") }
func (p painter) yellow(s string) string { return p.wrap(s, "\033[33m") }
func (p painter) green(s string) string  { return p.wrap(s, "\033[32m") }

// Avoid unused import errors if strings is not used; ensure compile.
var _ = strings.TrimSpace
