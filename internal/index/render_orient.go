package index

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// OrientationOptions controls the compact semantic routing view used by
// agents before they choose a subtree or issue a search.
type OrientationOptions struct {
	Collection string
	SubPath    string
	// LocalOnly ignores editorial purpose/use_when/summary fields and builds
	// routing context solely from on-disk titles, prose, and headings.
	LocalOnly bool
}

type orientationEnvelope struct {
	SchemaVersion int                 `json:"schema_version"`
	Collection    string              `json:"collection"`
	Root          string              `json:"root"`
	SubPath       string              `json:"sub_path"`
	Digest        string              `json:"digest"`
	Stats         jsonStats           `json:"stats"`
	Folders       []orientationFolder `json:"folders"`
	Errors        []string            `json:"errors"`
}

type orientationFolder struct {
	Path          string            `json:"path"`
	Purpose       string            `json:"purpose"`
	PurposeSource string            `json:"purpose_source"`
	UseWhen       []string          `json:"use_when"`
	Children      []string          `json:"children"`
	Files         []orientationFile `json:"files"`
}

type orientationFile struct {
	Path          string   `json:"path"`
	Title         string   `json:"title"`
	Summary       string   `json:"summary"`
	SummarySource string   `json:"summary_source"`
	Topics        []string `json:"topics"`
	Words         int      `json:"words"`
}

// RenderOrientationJSON emits a deliberately compact semantic map. It favors
// human/LLM-authored metadata and falls back to verbatim prose and headings,
// never generated text, so it remains useful in fully local mode.
func RenderOrientationJSON(fm *FolderMap, opts OrientationOptions) ([]byte, error) {
	if fm == nil {
		return nil, fmt.Errorf("render orientation: nil folder map")
	}

	fm = orientationView(fm, opts.LocalOnly)
	var newest = newestMtime(fm)
	env := orientationEnvelope{
		SchemaVersion: SchemaVersion,
		Collection:    opts.Collection,
		Root:          fm.Root,
		SubPath:       opts.SubPath,
		Digest:        computeDigest(fm),
		Stats:         computeStats(fm, newest),
		Folders:       make([]orientationFolder, 0, len(fm.Folders)),
		Errors:        append(make([]string, 0, len(fm.Errors)), fm.Errors...),
	}

	for _, folder := range fm.Folders {
		purpose := firstSentence(folder.Purpose)
		purposeSource := "editorial"
		if purpose == "" {
			purpose = fallbackFolderSummary(folder)
			purposeSource = "extractive"
			if purpose == "" {
				purposeSource = "none"
			}
		}
		of := orientationFolder{
			Path:          folder.Path,
			Purpose:       purpose,
			PurposeSource: purposeSource,
			UseWhen:       append(make([]string, 0, len(folder.UseWhen)), folder.UseWhen...),
			Children:      append(make([]string, 0, len(folder.Children)), folder.Children...),
			Files:         make([]orientationFile, 0, len(folder.Files)),
		}
		for _, file := range folder.Files {
			title := file.Title
			if title == "" {
				title = titleFromFilename(file.Name)
			}
			summary := strings.TrimSpace(file.Summary)
			source := "editorial"
			if summary == "" {
				summary = firstSentence(file.Excerpt)
				source = "extractive"
			}
			topics := sectionTopics(file, 6)
			if summary == "" && len(topics) > 0 {
				summary = "Topics: " + strings.Join(topics, ", ")
				source = "headings"
			}
			if summary == "" {
				summary = title
				source = "title"
			}
			of.Files = append(of.Files, orientationFile{
				Path:          file.Path,
				Title:         title,
				Summary:       truncate(summary, 280),
				SummarySource: source,
				Topics:        topics,
				Words:         file.Words,
			})
		}
		env.Folders = append(env.Folders, of)
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(env); err != nil {
		return nil, fmt.Errorf("render orientation: %w", err)
	}
	return buf.Bytes(), nil
}

// orientationView removes filesystem-integrity noise from the routing view.
// Detailed `--json` output still exposes every disk/index entry; orientation
// keeps only prose-like text or files that have an explicit editorial summary.
func orientationView(fm *FolderMap, localOnly bool) *FolderMap {
	view := &FolderMap{
		Root:   fm.Root,
		Errors: append([]string(nil), fm.Errors...),
	}
	keepPath := make(map[string]bool, len(fm.Folders))
	prepared := make([]FolderNode, 0, len(fm.Folders))
	for _, folder := range fm.Folders {
		copyFolder := folder
		if localOnly {
			copyFolder.Purpose = ""
			copyFolder.UseWhen = nil
		}
		copyFolder.Files = make([]FileNode, 0, len(folder.Files))
		for _, file := range folder.Files {
			if localOnly {
				file.Summary = ""
			}
			if strings.TrimSpace(file.Summary) == "" && !IsTextKind(file.Kind) {
				continue
			}
			copyFolder.Files = append(copyFolder.Files, file)
		}
		keep := folder.Depth == 0 || len(copyFolder.Files) > 0 || strings.TrimSpace(copyFolder.Purpose) != "" || len(copyFolder.UseWhen) > 0 || len(folder.Children) > 0
		keepPath[folder.Path] = keep
		prepared = append(prepared, copyFolder)
	}
	for _, folder := range prepared {
		if !keepPath[folder.Path] {
			continue
		}
		children := make([]string, 0, len(folder.Children))
		for _, child := range folder.Children {
			childPath := relJoin(folder.Path, child)
			if keep, loaded := keepPath[childPath]; loaded && !keep {
				continue
			}
			children = append(children, child)
		}
		folder.Children = children
		view.Folders = append(view.Folders, folder)
	}
	return view
}

func sectionTopics(file FileNode, limit int) []string {
	topics := make([]string, 0, min(limit, len(file.Sections)))
	for _, section := range file.Sections {
		if file.Title != "" && strings.EqualFold(section.Heading, file.Title) {
			continue
		}
		topics = append(topics, section.Heading)
		if len(topics) == limit {
			break
		}
	}
	return topics
}

func newestMtime(fm *FolderMap) (newest time.Time) {
	for _, folder := range fm.Folders {
		if folder.LastModified.After(newest) {
			newest = folder.LastModified
		}
	}
	return newest
}
