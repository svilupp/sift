package index

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalCanonical is the exact canonical bytes the writer is expected to
// emit for a default FolderIndex (just schema + ignore).
const minimalCanonical = `# SIFT folder index. Maintained by SIFT. Human edits preserved.

schema_version = 1
ignore = false
`

const fullCanonical = `# SIFT folder index. Maintained by SIFT. Human edits preserved.

schema_version = 1
ignore = false

purpose = """
Architecture planning material for the next agent system.
Use it for design tradeoffs, rollout planning, and historical decisions.
"""

use_when = [
  "Researching agent-next architecture decisions",
  "Finding rollout plans or design tradeoffs",
]

[refresh]
file_count = 17
word_count = 48210

[files."PROPOSAL.md"]
ignore = false
content_hash = "a91c22"
head_hash = "71b0ef"
tail_hash = "09cc31"
words = 4200
summary = """
Canonical proposal for the agent-next architecture workstream. Main design rationale,
rollout phases, and decision history.
"""

[files."logs/run-2026-04-28.md"]
ignore = false
content_hash = "22ab18"
head_hash = "b71931"
tail_hash = "d201aa"
words = 1800
summary = "Execution log."

[folders."logs"]
ignore = true
`

const childlessFolders = `# SIFT folder index. Maintained by SIFT. Human edits preserved.

schema_version = 1
ignore = false

[folders."archive"]
ignore = false

[folders."logs"]
ignore = true
`

const unicodeKey = `# SIFT folder index. Maintained by SIFT. Human edits preserved.

schema_version = 1
ignore = false

[files."nóte.md"]
ignore = false
content_hash = "a"
head_hash = "b"
tail_hash = "c"
words = 1
`

func TestRoundTrip_TableDriven(t *testing.T) {
	cases := []struct {
		name string
		in   string
	}{
		{"minimal", minimalCanonical},
		{"full", fullCanonical},
		{"folders only", childlessFolders},
		{"unicode key", unicodeKey},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			idx, err := LoadBytes([]byte(c.in))
			if err != nil {
				t.Fatalf("LoadBytes: %v", err)
			}
			out, err := Marshal(idx)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if !bytes.Equal(out, []byte(c.in)) {
				t.Fatalf("round-trip not byte-identical:\n--- want ---\n%s\n--- got ---\n%s", c.in, out)
			}
		})
	}
}

func TestMarshal_Deterministic(t *testing.T) {
	idx, err := LoadBytes([]byte(fullCanonical))
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	a, err := Marshal(idx)
	if err != nil {
		t.Fatalf("Marshal a: %v", err)
	}
	b, err := Marshal(idx)
	if err != nil {
		t.Fatalf("Marshal b: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("two Marshal calls disagree:\nA=%q\nB=%q", a, b)
	}
}

func TestMarshal_EmptyMapsAreElided(t *testing.T) {
	idx := &FolderIndex{
		SchemaVersion: 1,
		Files:         map[string]FileEntry{},
		Folders:       map[string]ChildFolder{},
	}
	out, err := Marshal(idx)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(out)
	if strings.Contains(got, "[files") || strings.Contains(got, "[folders") {
		t.Fatalf("empty maps must not emit tables; got:\n%s", got)
	}
	if strings.Contains(got, "[refresh") {
		t.Fatalf("empty refresh must not emit table; got:\n%s", got)
	}
}

func TestMarshal_FilesSortedAlphabetically(t *testing.T) {
	idx := &FolderIndex{
		SchemaVersion: 1,
		Files: map[string]FileEntry{
			"zeta.md":   {ContentHash: "1", HeadHash: "2", TailHash: "3", Words: 1},
			"alpha.md":  {ContentHash: "4", HeadHash: "5", TailHash: "6", Words: 2},
			"middle.md": {ContentHash: "7", HeadHash: "8", TailHash: "9", Words: 3},
		},
	}
	out, err := Marshal(idx)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	got := string(out)
	a := strings.Index(got, `[files."alpha.md"]`)
	m := strings.Index(got, `[files."middle.md"]`)
	z := strings.Index(got, `[files."zeta.md"]`)
	if !(a < m && m < z) {
		t.Fatalf("files not alphabetical: a=%d m=%d z=%d\n%s", a, m, z, got)
	}
}

func TestSave_AtomicAndReadable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sift.toml")
	idx, _ := LoadBytes([]byte(fullCanonical))
	if err := Save(path, idx); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, []byte(fullCanonical)) {
		t.Fatalf("Save then read disagrees with input:\n--- want ---\n%s\n--- got ---\n%s", fullCanonical, got)
	}
	// No tmp file leaked.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp.") {
			t.Fatalf("leftover tmp file: %s", e.Name())
		}
	}
}

func TestMarshal_MultilinePicksOnNewlineOrLength(t *testing.T) {
	short := &FolderIndex{SchemaVersion: 1, Purpose: "short purpose"}
	out, _ := Marshal(short)
	if strings.Contains(string(out), `"""`) {
		t.Fatalf("short single-line purpose should be inline:\n%s", out)
	}
	long := &FolderIndex{SchemaVersion: 1, Purpose: strings.Repeat("a ", 80)}
	out, _ = Marshal(long)
	if !strings.Contains(string(out), `"""`) {
		t.Fatalf("long purpose should be multiline:\n%s", out)
	}
	withNewline := &FolderIndex{SchemaVersion: 1, Purpose: "one\ntwo"}
	out, _ = Marshal(withNewline)
	if !strings.Contains(string(out), `"""`) {
		t.Fatalf("multiline purpose should use triple quotes:\n%s", out)
	}
}

func TestMarshal_ModifyOneFieldChangesOnlyThatLine(t *testing.T) {
	idx, _ := LoadBytes([]byte(fullCanonical))
	idx.Refresh.WordCount = 12345
	out, _ := Marshal(idx)
	got := string(out)
	if !strings.Contains(got, "word_count = 12345") {
		t.Fatalf("expected updated word_count in output:\n%s", got)
	}
	// Diff the canonical input vs new — only word_count line should differ.
	wantLines := strings.Split(fullCanonical, "\n")
	gotLines := strings.Split(got, "\n")
	if len(wantLines) != len(gotLines) {
		t.Fatalf("line count differs: %d vs %d", len(wantLines), len(gotLines))
	}
	diffs := 0
	for i := range wantLines {
		if wantLines[i] != gotLines[i] {
			diffs++
			if !strings.HasPrefix(strings.TrimSpace(gotLines[i]), "word_count") {
				t.Fatalf("unexpected line %d differs:\nwant: %q\n got: %q", i, wantLines[i], gotLines[i])
			}
		}
	}
	if diffs != 1 {
		t.Fatalf("expected exactly one line diff, got %d", diffs)
	}
}
