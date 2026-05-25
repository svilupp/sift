package search

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"sift/internal/bm25"
	"sift/internal/chunk"
	"sift/internal/config"
	"sift/internal/db"
	"sift/internal/sync"
)

func setupSectionSearchEnv(t *testing.T, files map[string]string) (*Engine, *db.DB, func()) {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	if err := database.Init(); err != nil {
		t.Fatalf("db.Init: %v", err)
	}

	blevePath := filepath.Join(t.TempDir(), "test.bleve")
	bleveIdx, err := bm25.OpenBleve(blevePath, "standard")
	if err != nil {
		t.Fatalf("OpenBleve: %v", err)
	}

	colDir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(colDir, name)
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	if _, err := database.AddCollection("vault", colDir, nil); err != nil {
		t.Fatalf("AddCollection: %v", err)
	}

	var buf bytes.Buffer
	if _, err := sync.Refresh(context.Background(), database, bleveIdx, nil, sync.RefreshOptions{
		BatchSize: 128,
		ChunkMode: "section",
		ChunkOpts: chunk.Options{
			RowsPerChunk:  10,
			OverlapRows:   2,
			MinChunkChars: 5,
			SkipEmptyRows: true,
		},
		SectionOpts: chunk.SectionOptions{
			MaxSectionChars: 5000,
			MinSectionChars: 20,
			OverlapLines:    1,
		},
	}, &buf); err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	cfg := config.Default()
	cfg.Search.DedupIdenticalContent = false

	engine := NewEngine(database, bleveIdx, nil, cfg)
	cleanup := func() {
		_ = bleveIdx.Close()
		_ = database.Close()
	}
	return engine, database, cleanup
}

func TestSearchSectionMetadataAndNeighborhoods(t *testing.T) {
	engine, _, cleanup := setupSectionSearchEnv(t, map[string]string{
		"a.md": `# Alpha

## Overview
Workflow orchestration for batch processing with database-backed state.

### Details
Operational caveats for workers and workflows.

## Setup
Collection bootstrapping and sync steps.
`,
		"b.md": `# Beta

## Overview
General architecture summary for a different project.
`,
	})
	defer cleanup()

	result, err := engine.Search(context.Background(), "workflow orchestration batch", SearchOptions{
		TopK:             1,
		SectionAggregate: true,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Results))
	}

	got := result.Results[0]
	if filepath.Base(got.FilePath) != "a.md" {
		t.Fatalf("top result file = %s, want a.md", got.FilePath)
	}
	if got.SectionID != "overview" {
		t.Fatalf("section id = %q, want overview", got.SectionID)
	}
	if got.SectionCharCount <= 0 {
		t.Fatal("expected section char count to be populated")
	}
	if got.SubsectionCount != 1 {
		t.Fatalf("subsection count = %d, want 1", got.SubsectionCount)
	}
	if len(got.Siblings) == 0 || got.Siblings[0].Heading != "Setup" {
		t.Fatalf("siblings = %+v, want Setup", got.Siblings)
	}
	if len(got.Related) == 0 || filepath.Base(got.Related[0].FilePath) != "b.md" {
		t.Fatalf("related = %+v, want b.md overview", got.Related)
	}
}

func TestSearchAppliesBacklinkAndReadBoosts(t *testing.T) {
	engine, database, cleanup := setupSectionSearchEnv(t, map[string]string{
		"plain.md": `# Plain

Storage limit handling for indexed documents.
`,
		"boosted.md": `# Boosted

Storage limit handling for indexed documents.
`,
		"source.md": `# Source

Reference links for other files.
`,
	})
	defer cleanup()

	col, err := database.GetCollection("vault")
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	if col == nil {
		t.Fatal("vault collection missing")
	}

	sourceFile, err := database.GetFileByPath(filepath.Join(col.Path, "source.md"))
	if err != nil {
		t.Fatalf("GetFileByPath source: %v", err)
	}
	if sourceFile == nil {
		t.Fatal("source.md not found")
	}
	boostedPath := filepath.Join(col.Path, "boosted.md")
	if err := database.InsertLink(sourceFile.ID, "", 1, boostedPath, "", "markdown", "[boosted](boosted.md)"); err != nil {
		t.Fatalf("InsertLink: %v", err)
	}
	if _, err := database.RefreshBacklinkCounts(); err != nil {
		t.Fatalf("RefreshBacklinkCounts: %v", err)
	}
	if err := database.ReplaceReadCounts([]db.ReadCountRecord{
		{DocPath: boostedPath, TotalReads: 16, UniqueDays: 4, LastRead: "2026-03-17"},
	}); err != nil {
		t.Fatalf("ReplaceReadCounts: %v", err)
	}

	engine.Cfg.Scoring.BacklinkWeight = 0.2
	engine.Cfg.Scoring.ReadSignalEnabled = true
	engine.Cfg.Scoring.ReadSignalWeight = 0.1

	result, err := engine.Search(context.Background(), "storage limit handling", SearchOptions{
		TopK: 2,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Results) < 2 {
		t.Fatalf("expected 2 results, got %d", len(result.Results))
	}
	if filepath.Base(result.Results[0].FilePath) != "boosted.md" {
		t.Fatalf("top result = %s, want boosted.md", result.Results[0].FilePath)
	}
}

func TestSearchSectionRankingDoesNotBleedAcrossSections(t *testing.T) {
	engine, database, cleanup := setupSectionSearchEnv(t, map[string]string{
		"architecture.md": `# Architecture

## Overview
Workflow orchestration for batch processing with database-backed storage.

### Details
Operational caveats for workers and workflows.

## Setup
Collection bootstrapping and sync steps.
`,
		"ops.md": `# Operations

## Overview
Runbook summary for a different service.
`,
		"source.md": `# Source

See [Architecture](architecture.md#overview) for the main system overview.
`,
	})
	defer cleanup()

	col, err := database.GetCollection("vault")
	if err != nil {
		t.Fatalf("GetCollection: %v", err)
	}
	sourceFile, err := database.GetFileByPath(filepath.Join(col.Path, "source.md"))
	if err != nil {
		t.Fatalf("GetFileByPath source: %v", err)
	}
	if sourceFile == nil {
		t.Fatal("source.md not found")
	}
	targetPath := filepath.Join(col.Path, "architecture.md")
	if err := database.InsertLink(sourceFile.ID, "", 1, targetPath, "overview", "markdown", "[overview](architecture.md#overview)"); err != nil {
		t.Fatalf("InsertLink: %v", err)
	}
	if _, err := database.RefreshBacklinkCounts(); err != nil {
		t.Fatalf("RefreshBacklinkCounts: %v", err)
	}
	if err := database.ReplaceReadCounts([]db.ReadCountRecord{
		{DocPath: targetPath, TotalReads: 9, UniqueDays: 3, LastRead: "2026-03-17"},
	}); err != nil {
		t.Fatalf("ReplaceReadCounts: %v", err)
	}

	engine.Cfg.Scoring.BacklinkWeight = 0.1
	engine.Cfg.Scoring.ReadSignalEnabled = true
	engine.Cfg.Scoring.ReadSignalWeight = 0.05

	result, err := engine.Search(context.Background(), "workflow orchestration batch", SearchOptions{
		TopK:             3,
		SectionAggregate: true,
	})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(result.Results) == 0 {
		t.Fatal("expected results")
	}
	if got := result.Results[0].SectionID; got != "overview" {
		t.Fatalf("top section = %q, want overview", got)
	}
}
