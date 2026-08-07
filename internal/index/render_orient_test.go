package index

import (
	"encoding/json"
	"testing"
)

func TestRenderOrientationJSON_PrefersEditorialAndFallsBackLocally(t *testing.T) {
	fm := &FolderMap{
		Root: "/vault",
		Folders: []FolderNode{{
			Path:     "docs",
			Purpose:  "Architecture guidance. More detail.",
			UseWhen:  []string{"changing the pipeline"},
			Children: []string{"decisions"},
			Files: []FileNode{
				{Path: "docs/editorial.md", Name: "editorial.md", Title: "Editorial", Summary: "Written summary."},
				{Path: "docs/local.md", Name: "local.md", Kind: "md", Title: "Local", Excerpt: "Verbatim opening prose.", Sections: []SectionNode{{Heading: "Local", Level: 1}, {Heading: "Examples", Level: 2}}},
				{Path: "docs/screenshot.png", Name: "screenshot.png", Kind: "png", Title: "screenshot"},
			},
		}},
	}
	out, err := RenderOrientationJSON(fm, OrientationOptions{Collection: "vault", SubPath: "docs"})
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatal(err)
	}
	folder := env["folders"].([]any)[0].(map[string]any)
	if folder["purpose_source"] != "editorial" {
		t.Fatalf("purpose source = %v", folder["purpose_source"])
	}
	files := folder["files"].([]any)
	if len(files) != 2 {
		t.Fatalf("binary noise should be omitted: %v", files)
	}
	if files[0].(map[string]any)["summary_source"] != "editorial" {
		t.Fatalf("editorial summary not preferred: %v", files[0])
	}
	if files[1].(map[string]any)["summary_source"] != "extractive" {
		t.Fatalf("local fallback not labeled: %v", files[1])
	}
	if _, ok := files[1].(map[string]any)["content_hash"]; ok {
		t.Fatal("orientation payload should omit mechanical detail")
	}
	if _, ok := env["errors"]; !ok {
		t.Fatal("stable orientation contract must include errors array")
	}
}

func TestRenderOrientationJSON_LocalOnlyIgnoresEditorialText(t *testing.T) {
	fm := &FolderMap{Root: "/vault", Folders: []FolderNode{{
		Path:    ".",
		Depth:   0,
		Purpose: "Generated folder purpose.",
		Files: []FileNode{{
			Path: "guide.md", Name: "guide.md", Kind: "md", Title: "Guide",
			Summary: "Generated file summary.", Excerpt: "Verbatim local opening.",
		}},
	}}}
	out, err := RenderOrientationJSON(fm, OrientationOptions{LocalOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if err := json.Unmarshal(out, &env); err != nil {
		t.Fatal(err)
	}
	folder := env["folders"].([]any)[0].(map[string]any)
	if folder["purpose_source"] != "extractive" {
		t.Fatalf("folder should use local fallback: %v", folder)
	}
	file := folder["files"].([]any)[0].(map[string]any)
	if file["summary"] != "Verbatim local opening." || file["summary_source"] != "extractive" {
		t.Fatalf("file should ignore editorial summary: %v", file)
	}
}
