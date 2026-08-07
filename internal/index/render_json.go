package index

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// RenderOptions controls JSON / Markdown emit.
type RenderOptions struct {
	// Sections, when true, emits per-file `sections` arrays. Off by
	// default in markdown, on by default in JSON (the CLI flips this).
	Sections bool

	// Summaries, when false, strips folder Purpose and file Summary.
	Summaries bool

	// Collection is an optional label for the JSON envelope.
	Collection string

	// SubPath is the relative subtree the load was scoped to (or empty
	// when the entire root was loaded). Reflected in the JSON envelope.
	SubPath string
}

// DefaultJSONOptions returns the agent-first defaults: sections on,
// summaries on.
func DefaultJSONOptions() RenderOptions {
	return RenderOptions{Sections: true, Summaries: true}
}

// DefaultMarkdownOptions returns the human defaults: sections off,
// summaries on.
func DefaultMarkdownOptions() RenderOptions {
	return RenderOptions{Sections: false, Summaries: true}
}

// jsonEnvelope is the top-level JSON shape. Field order is fixed by
// struct order; encoding/json preserves it.
type jsonEnvelope struct {
	SchemaVersion int          `json:"schema_version"`
	Collection    string       `json:"collection,omitempty"`
	Root          string       `json:"root"`
	SubPath       string       `json:"sub_path,omitempty"`
	Digest        string       `json:"digest"`
	Stats         jsonStats    `json:"stats"`
	Folders       []jsonFolder `json:"folders"`
	Errors        []string     `json:"errors,omitempty"`
}

type jsonStats struct {
	FolderCount int    `json:"folder_count"`
	FileCount   int    `json:"file_count"`
	TotalBytes  int64  `json:"total_bytes"`
	TotalWords  int    `json:"total_words"`
	NewestMtime string `json:"newest_mtime,omitempty"`
}

type jsonFolder struct {
	Path         string     `json:"path"`
	Depth        int        `json:"depth"`
	HasIndex     bool       `json:"has_index"`
	Ignore       bool       `json:"ignore,omitempty"`
	ParseError   string     `json:"parse_error,omitempty"`
	Purpose      string     `json:"purpose,omitempty"`
	UseWhen      []string   `json:"use_when,omitempty"`
	FileCount    int        `json:"file_count"`
	WordCount    int        `json:"word_count"`
	LastModified string     `json:"last_modified,omitempty"`
	Files        []jsonFile `json:"files"`
	Children     []string   `json:"children,omitempty"`
}

type jsonFile struct {
	Path        string        `json:"path"`
	Kind        string        `json:"kind,omitempty"`
	Ignore      bool          `json:"ignore,omitempty"`
	Bytes       int64         `json:"bytes"`
	Words       int           `json:"words"`
	Mtime       string        `json:"mtime,omitempty"`
	ContentHash string        `json:"content_hash,omitempty"`
	Summary     string        `json:"summary,omitempty"`
	Title       string        `json:"title,omitempty"`
	Excerpt     string        `json:"excerpt,omitempty"`
	Exists      bool          `json:"exists"`
	Sections    []SectionNode `json:"sections,omitempty"`
}

// RenderTreeJSON returns a deterministic JSON serialization of fm.
// Identical inputs always yield byte-identical output. Distinct from
// RenderJSON (which serialises a CheckReport for `index check`).
func RenderTreeJSON(fm *FolderMap, opts RenderOptions) ([]byte, error) {
	if fm == nil {
		return nil, fmt.Errorf("render json: nil folder map")
	}
	env := jsonEnvelope{
		SchemaVersion: SchemaVersion,
		Collection:    opts.Collection,
		Root:          fm.Root,
		SubPath:       opts.SubPath,
		Folders:       make([]jsonFolder, 0, len(fm.Folders)),
	}

	var newest time.Time
	for _, f := range fm.Folders {
		jf := jsonFolder{
			Path:       f.Path,
			Depth:      f.Depth,
			HasIndex:   f.HasIndex,
			Ignore:     f.Ignore,
			ParseError: f.ParseError,
			FileCount:  f.Refresh.FileCount,
			WordCount:  f.Refresh.WordCount,
			Children:   append([]string(nil), f.Children...),
		}
		if opts.Summaries {
			jf.Purpose = f.Purpose
			if len(f.UseWhen) > 0 {
				jf.UseWhen = append([]string(nil), f.UseWhen...)
			}
		}
		if !f.LastModified.IsZero() {
			jf.LastModified = f.LastModified.UTC().Format(time.RFC3339)
			if f.LastModified.After(newest) {
				newest = f.LastModified
			}
		}
		// Fall back: when no refresh stats populated, derive from files.
		if jf.FileCount == 0 {
			jf.FileCount = len(f.Files)
		}
		if jf.WordCount == 0 {
			sum := 0
			for _, fl := range f.Files {
				sum += fl.Words
			}
			jf.WordCount = sum
		}

		jf.Files = make([]jsonFile, 0, len(f.Files))
		for _, fl := range f.Files {
			jfl := jsonFile{
				Path:        fl.Path,
				Kind:        fl.Kind,
				Ignore:      fl.Ignore,
				Bytes:       fl.Bytes,
				Words:       fl.Words,
				ContentHash: fl.ContentHash,
				Exists:      fl.Exists,
				Title:       fl.Title,
				Excerpt:     fl.Excerpt,
			}
			if !fl.Mtime.IsZero() {
				jfl.Mtime = fl.Mtime.UTC().Format(time.RFC3339)
			}
			if opts.Summaries {
				jfl.Summary = fl.Summary
			}
			if opts.Sections && fl.Sections != nil {
				jfl.Sections = append([]SectionNode(nil), fl.Sections...)
			}
			jf.Files = append(jf.Files, jfl)
		}
		env.Folders = append(env.Folders, jf)
	}

	env.Stats = computeStats(fm, newest)
	env.Digest = computeDigest(fm)
	env.Errors = append([]string(nil), fm.Errors...)

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(env); err != nil {
		return nil, fmt.Errorf("render json: %w", err)
	}
	return buf.Bytes(), nil
}

// DigestMaxChars caps the rendered top-level digest. PROPOSAL2.md
// suggests ~500 chars.
const DigestMaxChars = 500

// computeDigest concatenates the best semantic description available for
// each folder. Editorial purposes win; otherwise deterministic titles and
// extractive file context keep local-only indexes useful.
func computeDigest(fm *FolderMap) string {
	var b strings.Builder
	for _, f := range fm.Folders {
		s := firstSentence(f.Purpose)
		if s == "" {
			s = fallbackFolderSummary(f)
		}
		if s == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(s)
		if b.Len() >= DigestMaxChars {
			break
		}
	}
	out := strings.TrimSpace(b.String())
	if len(out) > DigestMaxChars {
		out = strings.TrimSpace(out[:DigestMaxChars]) + "..."
	}
	return out
}

func fallbackFolderSummary(f FolderNode) string {
	label := f.Path
	if label == "." {
		label = "Root"
	}
	var descriptions []string
	for _, file := range f.Files {
		title := file.Title
		if title == "" {
			title = titleFromFilename(file.Name)
		}
		detail := firstSentence(file.Excerpt)
		if detail == "" && len(file.Sections) > 1 {
			detail = "Topics: " + joinSectionTopics(file, 3)
		}
		if detail != "" {
			descriptions = append(descriptions, title+": "+detail)
		} else if title != "" {
			descriptions = append(descriptions, title)
		}
		if len(descriptions) == 2 {
			break
		}
	}
	if len(descriptions) > 0 {
		return truncate(label+" — "+strings.Join(descriptions, "; "), 300)
	}
	if len(f.Children) > 0 {
		children := f.Children
		if len(children) > 4 {
			children = children[:4]
		}
		return fmt.Sprintf("%s — child areas: %s.", label, strings.Join(children, ", "))
	}
	return ""
}

func joinSectionTopics(file FileNode, limit int) string {
	var topics []string
	for _, section := range file.Sections {
		if file.Title != "" && strings.EqualFold(section.Heading, file.Title) {
			continue
		}
		topics = append(topics, section.Heading)
		if len(topics) == limit {
			break
		}
	}
	return strings.Join(topics, ", ")
}

// firstSentence pulls the first sentence-ish chunk out of a paragraph.
// Splits on '.', '!', '?' followed by space or newline; falls back to
// the first non-empty line.
func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Collapse newlines to spaces.
	s = strings.ReplaceAll(s, "\n", " ")
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == '.' || c == '!' || c == '?' {
			// Include the punctuation; trim trailing space.
			return strings.TrimSpace(s[:i+1])
		}
	}
	return s
}

func computeStats(fm *FolderMap, newest time.Time) jsonStats {
	stats := jsonStats{FolderCount: len(fm.Folders)}
	for _, f := range fm.Folders {
		for _, fl := range f.Files {
			stats.FileCount++
			stats.TotalBytes += fl.Bytes
			stats.TotalWords += fl.Words
		}
	}
	if !newest.IsZero() {
		stats.NewestMtime = newest.UTC().Format(time.RFC3339)
	}
	return stats
}
