package aigen

import (
	_ "embed"
	"fmt"
	"sort"
	"strings"
)

//go:embed prompts/folder_v5.txt
var folderPromptV5 string

// PromptVersion identifies the active prompt revision. Bump in lockstep
// with the embedded prompt file to keep telemetry aligned with the
// system text in flight.
const PromptVersion = "v5"

// PromptInput is the deterministic shape used to render a folder
// prompt. The renderer never reads from anywhere else, so the same
// PromptInput always produces the same bytes.
type PromptInput struct {
	Folder FolderContext
	Files  []FileBatch
}

// SystemPrompt returns the embedded LT-D winning prompt text. It is
// intentionally a value, not a constructor — callers must not mutate.
func SystemPrompt() string {
	return folderPromptV5
}

// BuildFolderPrompt renders the user-message body for a folder batch.
// Output is deterministic: files are sorted by path; trailing
// whitespace is trimmed.
//
// Format (versioned implicitly by PromptVersion):
//
//	Folder: <relpath>
//	Parent purpose: <text>      // omitted when empty
//	Existing purpose: <text>    // omitted when empty
//	Existing use_when:          // omitted when empty
//	  - cue1
//	  - cue2
//
//	Files:
//	[1] path: <rel>
//	    words: N
//	    title: <title>          // omitted when empty
//	    type: <type>            // omitted when empty
//	    tags: a, b, c           // omitted when empty
//	    current_summary: <text> // omitted when empty
//	    head_500w: |
//	      ...
//	    tail_500w: |
//	      ...
//	[2] ...
//
//	Respond with one JSON object: {"purpose": "...", "use_when": [...],
//	  "files": [{"path": "...", "summary": "..."}]}.
//	Cover EXACTLY the file paths listed above. Do not add or omit any.
func BuildFolderPrompt(in PromptInput) string {
	files := append([]FileBatch(nil), in.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })

	var b strings.Builder

	if rp := promptFolderName(in.Folder); rp != "" {
		fmt.Fprintf(&b, "Folder: %s\n", rp)
	} else {
		b.WriteString("Folder: <unspecified>\n")
	}

	if t := strings.TrimSpace(in.Folder.ParentPurpose); t != "" {
		fmt.Fprintf(&b, "Parent purpose: %s\n", oneLine(t))
	}
	if t := strings.TrimSpace(in.Folder.ExistingPurpose); t != "" {
		fmt.Fprintf(&b, "Existing purpose: %s\n", oneLine(t))
	}
	if cues := nonEmptyTrim(in.Folder.ExistingUseWhen); len(cues) > 0 {
		b.WriteString("Existing use_when:\n")
		for _, c := range cues {
			fmt.Fprintf(&b, "  - %s\n", c)
		}
	}

	b.WriteString("\nFiles:\n")
	for i, f := range files {
		fmt.Fprintf(&b, "[%d] path: %s\n", i+1, f.Path)
		fmt.Fprintf(&b, "    words: %d\n", f.Words)
		if t := strings.TrimSpace(f.FrontmatterTitle); t != "" {
			fmt.Fprintf(&b, "    title: %s\n", oneLine(t))
		}
		if t := strings.TrimSpace(f.FrontmatterType); t != "" {
			fmt.Fprintf(&b, "    type: %s\n", oneLine(t))
		}
		if tags := nonEmptyTrim(f.FrontmatterTags); len(tags) > 0 {
			fmt.Fprintf(&b, "    tags: %s\n", strings.Join(tags, ", "))
		}
		if cs := strings.TrimSpace(f.CurrentSummary); cs != "" {
			fmt.Fprintf(&b, "    current_summary: %s\n", oneLine(cs))
		}
		if h := strings.TrimSpace(f.HeadWords); h != "" {
			b.WriteString("    head_500w: |\n")
			writeIndented(&b, h, "      ")
		}
		if t := strings.TrimSpace(f.TailWords); t != "" && t != strings.TrimSpace(f.HeadWords) {
			b.WriteString("    tail_500w: |\n")
			writeIndented(&b, t, "      ")
		}
	}

	b.WriteString("\nRespond with one JSON object: ")
	b.WriteString(`{"purpose": "...", "use_when": [...], `)
	b.WriteString(`"files": [{"path": "...", "summary": "..."}]}.` + "\n")
	b.WriteString("Cover EXACTLY the file paths listed above. Do not add or omit any.\n")

	return b.String()
}

func promptFolderName(f FolderContext) string {
	if rp := strings.TrimSpace(f.RelPath); rp != "" {
		return rp
	}
	return strings.TrimSpace(f.Path)
}

// oneLine collapses any internal newlines so the prompt stays
// single-line per field. The model sees one tidy block of context.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return strings.TrimSpace(s)
}

func writeIndented(b *strings.Builder, s, prefix string) {
	for _, line := range strings.Split(s, "\n") {
		b.WriteString(prefix)
		b.WriteString(line)
		b.WriteString("\n")
	}
}

func nonEmptyTrim(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		t := strings.TrimSpace(s)
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}
