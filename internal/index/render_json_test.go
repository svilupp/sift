package index

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestRenderJSON_Deterministic(t *testing.T) {
	root := t.TempDir()
	writeFileTH(t, filepath.Join(root, "a.md"), "alpha")
	writeFileTH(t, filepath.Join(root, "sub", "b.md"), "beta")
	writeIndexHelper(t, root, "Root.", []string{"a.md"})
	writeIndexHelper(t, filepath.Join(root, "sub"), "Sub.", []string{"b.md"})

	fm, err := LoadTree(context.Background(), root, "", DefaultLoadOptions())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	out1, err := RenderTreeJSON(fm, DefaultJSONOptions())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out2, err := RenderTreeJSON(fm, DefaultJSONOptions())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !bytes.Equal(out1, out2) {
		t.Fatalf("RenderJSON not deterministic:\n--- a ---\n%s\n--- b ---\n%s", out1, out2)
	}
}

func TestRenderJSON_Shape(t *testing.T) {
	root := t.TempDir()
	writeFileTH(t, filepath.Join(root, "a.md"), "alpha")
	writeIndexHelper(t, root, "Root purpose. More text.", []string{"a.md"})

	fm, _ := LoadTree(context.Background(), root, "", DefaultLoadOptions())
	out, err := RenderTreeJSON(fm, DefaultJSONOptions())
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var env map[string]interface{}
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", err, out)
	}
	if env["schema_version"].(float64) != float64(SchemaVersion) {
		t.Fatalf("schema_version missing/wrong: %v", env["schema_version"])
	}
	if _, ok := env["digest"]; !ok {
		t.Fatalf("missing digest")
	}
	stats, ok := env["stats"].(map[string]interface{})
	if !ok {
		t.Fatalf("missing stats: %v", env)
	}
	if stats["folder_count"].(float64) < 1 {
		t.Fatalf("expected folder_count>=1, got %v", stats["folder_count"])
	}
	folders, ok := env["folders"].([]interface{})
	if !ok || len(folders) == 0 {
		t.Fatalf("missing folders: %v", env)
	}
	folder := folders[0].(map[string]interface{})
	if folder["purpose"].(string) == "" {
		t.Fatalf("expected non-empty purpose")
	}
}

func TestRenderJSON_NoSummariesStripsFields(t *testing.T) {
	root := t.TempDir()
	writeFileTH(t, filepath.Join(root, "a.md"), "alpha")
	writeIndexHelper(t, root, "Root purpose.", []string{"a.md"})

	fm, _ := LoadTree(context.Background(), root, "", DefaultLoadOptions())
	opts := RenderOptions{Sections: false, Summaries: false}
	out, err := RenderTreeJSON(fm, opts)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var env map[string]interface{}
	_ = json.Unmarshal(out, &env)
	folders := env["folders"].([]interface{})
	folder := folders[0].(map[string]interface{})
	if _, ok := folder["purpose"]; ok {
		t.Fatalf("expected no purpose with --no-summaries")
	}
	files := folder["files"].([]interface{})
	if len(files) > 0 {
		f := files[0].(map[string]interface{})
		if _, ok := f["summary"]; ok {
			t.Fatalf("expected no summary with --no-summaries")
		}
	}
}

func TestComputeDigest(t *testing.T) {
	fm := &FolderMap{
		Folders: []FolderNode{
			{Path: ".", Purpose: "Root area. Extra detail."},
			{Path: "a", Purpose: "Apples."},
			{Path: "b", Purpose: ""},
		},
	}
	got := computeDigest(fm)
	want := "Root area. Apples."
	if got != want {
		t.Fatalf("digest = %q, want %q", got, want)
	}
}

func TestFirstSentence(t *testing.T) {
	cases := map[string]string{
		"Hello.":               "Hello.",
		"  spaced.":            "spaced.",
		"a\nb":                 "a b",
		"first. second.":       "first.",
		"no terminator at all": "no terminator at all",
	}
	for in, want := range cases {
		if got := firstSentence(in); got != want {
			t.Errorf("firstSentence(%q) = %q, want %q", in, got, want)
		}
	}
}
