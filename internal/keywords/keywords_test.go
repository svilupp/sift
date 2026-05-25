package keywords

import (
	"strings"
	"testing"
)

func TestExtract(t *testing.T) {
	docs := []Document{
		{
			RelativePath: "memory/knowledge/projects.md",
			Content: `# Projects

## Workflow Engine
Database-backed batch processing and workflow orchestration.
`,
		},
		{
			RelativePath: "memory/knowledge/preferences.md",
			Content: `# Preferences

## Coding Style
Prefer explicit errors and small helpers.
`,
		},
		{
			RelativePath: "repos/sift/docs/architecture.md",
			Content: `# Architecture

## Storage Layer
Bleve BM25 search with vector embeddings and scoring.
`,
		},
	}

	groups := Extract(docs, Options{Depth: 2, TopN: 4})
	if len(groups) != 2 {
		t.Fatalf("Extract returned %d groups, want 2", len(groups))
	}

	if groups[0].Directory != "memory/knowledge/" {
		t.Fatalf("first group directory = %q, want memory/knowledge/", groups[0].Directory)
	}
	if groups[0].Notes != 2 {
		t.Fatalf("memory/knowledge notes = %d, want 2", groups[0].Notes)
	}
	if len(groups[0].Keywords) == 0 {
		t.Fatal("memory/knowledge keywords should not be empty")
	}

	foundPhrase := false
	for _, kw := range groups[1].Keywords {
		if strings.Contains(kw, " ") {
			foundPhrase = true
			break
		}
	}
	if !foundPhrase {
		t.Fatalf("expected a bigram keyword in %v", groups[1].Keywords)
	}
}

func TestDirectoryKey(t *testing.T) {
	tests := []struct {
		path  string
		depth int
		want  string
	}{
		{path: "memory/knowledge/projects.md", depth: 2, want: "memory/knowledge/"},
		{path: "repos/mem/main/PLAN.md", depth: 2, want: "repos/mem/"},
		{path: "README.md", depth: 2, want: "./"},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := directoryKey(tt.path, tt.depth); got != tt.want {
				t.Fatalf("directoryKey(%q, %d) = %q, want %q", tt.path, tt.depth, got, tt.want)
			}
		})
	}
}
