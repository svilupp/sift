package eval

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadCases(t *testing.T) {
	jsonl := `{"id":"c1","corpus":"notes","query":"how to deploy","gold":[{"path":"deploy.md","section_id":"deploy-steps","start_line":10}],"family":"direct"}
{"id":"c2","corpus":"notes","query":"auth flow","gold":[{"path":"auth.md","section_id":"oauth","start_line":1},{"path":"auth.md","section_id":"jwt","start_line":30}],"family":"multi_gold"}

{"id":"c3","corpus":"notes","query":"empty line above","gold":[{"path":"misc.md","section_id":"intro","start_line":1}],"family":"direct"}
`
	dir := t.TempDir()
	p := filepath.Join(dir, "cases.jsonl")
	if err := os.WriteFile(p, []byte(jsonl), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}

	cases, err := LoadCases(p)
	if err != nil {
		t.Fatalf("LoadCases: %v", err)
	}

	if got := len(cases); got != 3 {
		t.Fatalf("expected 3 cases, got %d", got)
	}

	// Verify first case.
	c := cases[0]
	if c.ID != "c1" {
		t.Errorf("case 0 ID = %q, want %q", c.ID, "c1")
	}
	if c.Query != "how to deploy" {
		t.Errorf("case 0 Query = %q, want %q", c.Query, "how to deploy")
	}
	if len(c.Gold) != 1 {
		t.Fatalf("case 0 Gold len = %d, want 1", len(c.Gold))
	}
	if c.Gold[0].SectionID != "deploy-steps" {
		t.Errorf("case 0 Gold[0].SectionID = %q, want %q", c.Gold[0].SectionID, "deploy-steps")
	}

	// Verify second case has two gold targets.
	if got := len(cases[1].Gold); got != 2 {
		t.Errorf("case 1 Gold len = %d, want 2", got)
	}

	// Verify third case (after blank line).
	if cases[2].ID != "c3" {
		t.Errorf("case 2 ID = %q, want %q", cases[2].ID, "c3")
	}
}

func TestLoadCases_FileNotFound(t *testing.T) {
	_, err := LoadCases("/nonexistent/cases.jsonl")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestLoadCases_InvalidJSON(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.jsonl")
	if err := os.WriteFile(p, []byte("{not json\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadCases(p)
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}
