package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRefsStampCheckAndWrite(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	docPath := filepath.Join(root, "PLAN.md")
	codePath := filepath.Join(root, "internal", "search", "search.go")

	if err := os.MkdirAll(filepath.Dir(codePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codePath, []byte("package search\nfunc main() {\nreturn err\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(docPath, []byte("- Ref: `internal/search/search.go:2-3`\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCmd("test")
	cmd.SetArgs([]string{"refs", "stamp", "--check", docPath})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err == nil {
		t.Fatalf("stamp --check should fail when a rewrite is needed")
	}

	cmd = NewRootCmd("test")
	cmd.SetArgs([]string{"refs", "stamp", "--write", docPath})
	out.Reset()
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("stamp --write: %v", err)
	}

	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "internal/search/search.go:2@") {
		t.Fatalf("stamped file = %q", string(data))
	}
}

func TestRefsValidateJSONAndFix(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	docPath := filepath.Join(root, "PLAN.md")
	codePath := filepath.Join(root, "shifted.go")

	if err := os.WriteFile(codePath, []byte("package sample\n\nfunc shifted() {\nhelper()\nif err != nil {\nreturn fmt.Errorf(\"wrap: %w\", err)\n}\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}

	startToken := "abc"
	endToken := "def"
	if err := os.WriteFile(docPath, []byte("- Ref: `shifted.go:3@"+startToken+"-4@"+endToken+"`\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCmd("test")
	cmd.SetArgs([]string{"refs", "validate", "--json", docPath})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("validate --json: %v", err)
	}

	var payload struct {
		Files []struct {
			Path    string `json:"path"`
			Changed bool   `json:"changed"`
			Results []struct {
				Status string `json:"status"`
			} `json:"results"`
		} `json:"files"`
	}
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("unmarshal validate --json output: %v\n%s", err, out.String())
	}
	if len(payload.Files) != 1 || len(payload.Files[0].Results) != 1 {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if payload.Files[0].Results[0].Status != "stale" {
		t.Fatalf("status = %q, want stale", payload.Files[0].Results[0].Status)
	}
}

func TestRefsValidateStrictFailsOnBrokenRef(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	docPath := filepath.Join(root, "PLAN.md")
	if err := os.WriteFile(docPath, []byte("- Ref: `missing.go:3@abc`\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCmd("test")
	cmd.SetArgs([]string{"refs", "validate", "--strict", docPath})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err == nil {
		t.Fatalf("validate --strict should fail on broken refs")
	}
}

func TestRefsLintDirectoryFixesCodeRefsAndFailsOnBrokenAnchors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	docsDir := filepath.Join(root, "docs")
	codePath := filepath.Join(root, "internal", "search.go")
	docWithCode := filepath.Join(docsDir, "PLAN.md")
	docWithBadLink := filepath.Join(docsDir, "README.md")

	if err := os.MkdirAll(filepath.Dir(codePath), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(docsDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codePath, []byte("package sample\nreturn err\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(docWithCode, []byte("- Ref: `../internal/search.go:2`\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(docWithBadLink, []byte("# Intro\n\nSee [missing](PLAN.md#missing-anchor).\n"), 0644); err != nil {
		t.Fatal(err)
	}

	cmd := NewRootCmd("test")
	cmd.SetArgs([]string{"refs", "lint", "--fix", docsDir})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := cmd.Execute(); err == nil {
		t.Fatalf("lint should fail on broken anchors")
	}

	data, err := os.ReadFile(docWithCode)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "../internal/search.go:2@") {
		t.Fatalf("expected code ref to be stamped during lint --fix, got %q", string(data))
	}
	if !strings.Contains(out.String(), "missing_anchor") {
		t.Fatalf("expected missing_anchor in output, got %q", out.String())
	}
}
