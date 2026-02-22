package search

import (
	"os"
	"path/filepath"
	"testing"

	"sift/internal/db"
)

func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

func TestDedupIdenticalContent_TwoDuplicates(t *testing.T) {
	dir := t.TempDir()
	content := "identical line one\nidentical line two\n"
	pathA := writeTempFile(t, dir, "a.md", content)
	pathB := writeTempFile(t, dir, "b.md", content)

	candidates := []scoredCandidate{
		{
			fused:       FusedResult{ChunkID: 1},
			chunkRec:    &db.ChunkRecord{ID: 1, StartLine: 1, EndLine: 2},
			fileRec:     &db.FileRecord{ID: 10, Path: pathA},
			rerankScore: 0.8,
		},
		{
			fused:       FusedResult{ChunkID: 2},
			chunkRec:    &db.ChunkRecord{ID: 2, StartLine: 1, EndLine: 2},
			fileRec:     &db.FileRecord{ID: 20, Path: pathB},
			rerankScore: 0.5,
		},
	}

	filtered, dedupMap := DedupIdenticalContent(candidates)

	if len(filtered) != 1 {
		t.Fatalf("expected 1 survivor, got %d", len(filtered))
	}
	if filtered[0].fused.ChunkID != 1 {
		t.Errorf("expected chunk 1 (higher score) to survive, got %d", filtered[0].fused.ChunkID)
	}

	dups, ok := dedupMap[1]
	if !ok {
		t.Fatal("expected dedup map entry for chunk 1")
	}
	if len(dups) != 1 {
		t.Fatalf("expected 1 duplicate ref, got %d", len(dups))
	}
	if dups[0].File != pathB {
		t.Errorf("duplicate file = %q, want %q", dups[0].File, pathB)
	}
	if dups[0].Score != 0.5 {
		t.Errorf("duplicate score = %f, want 0.5", dups[0].Score)
	}
}

func TestDedupIdenticalContent_DifferentContent(t *testing.T) {
	dir := t.TempDir()
	pathA := writeTempFile(t, dir, "a.md", "content A\n")
	pathB := writeTempFile(t, dir, "b.md", "content B\n")

	candidates := []scoredCandidate{
		{
			fused:       FusedResult{ChunkID: 1},
			chunkRec:    &db.ChunkRecord{ID: 1, StartLine: 1, EndLine: 1},
			fileRec:     &db.FileRecord{ID: 10, Path: pathA},
			rerankScore: 0.9,
		},
		{
			fused:       FusedResult{ChunkID: 2},
			chunkRec:    &db.ChunkRecord{ID: 2, StartLine: 1, EndLine: 1},
			fileRec:     &db.FileRecord{ID: 20, Path: pathB},
			rerankScore: 0.7,
		},
	}

	filtered, dedupMap := DedupIdenticalContent(candidates)

	if len(filtered) != 2 {
		t.Fatalf("expected 2 survivors, got %d", len(filtered))
	}
	if len(dedupMap) != 0 {
		t.Errorf("expected empty dedup map, got %d entries", len(dedupMap))
	}
}

func TestDedupIdenticalContent_Empty(t *testing.T) {
	filtered, dedupMap := DedupIdenticalContent(nil)

	if len(filtered) != 0 {
		t.Errorf("expected 0 results, got %d", len(filtered))
	}
	if dedupMap != nil {
		t.Errorf("expected nil dedup map, got %v", dedupMap)
	}
}

func TestDedupIdenticalContent_Single(t *testing.T) {
	dir := t.TempDir()
	path := writeTempFile(t, dir, "a.md", "single file content\n")

	candidates := []scoredCandidate{
		{
			fused:       FusedResult{ChunkID: 1},
			chunkRec:    &db.ChunkRecord{ID: 1, StartLine: 1, EndLine: 1},
			fileRec:     &db.FileRecord{ID: 10, Path: path},
			rerankScore: 0.95,
		},
	}

	filtered, dedupMap := DedupIdenticalContent(candidates)

	if len(filtered) != 1 {
		t.Fatalf("expected 1 survivor, got %d", len(filtered))
	}
	if filtered[0].fused.ChunkID != 1 {
		t.Errorf("expected chunk 1, got %d", filtered[0].fused.ChunkID)
	}
	if len(dedupMap) != 0 {
		t.Errorf("expected empty dedup map for single candidate, got %d entries", len(dedupMap))
	}
}

func TestDedupIdenticalContent_HigherScoreWins(t *testing.T) {
	dir := t.TempDir()
	content := "shared content line\n"
	pathA := writeTempFile(t, dir, "a.md", content)
	pathB := writeTempFile(t, dir, "b.md", content)

	// Candidate B has the higher score but comes second.
	candidates := []scoredCandidate{
		{
			fused:       FusedResult{ChunkID: 1},
			chunkRec:    &db.ChunkRecord{ID: 1, StartLine: 1, EndLine: 1},
			fileRec:     &db.FileRecord{ID: 10, Path: pathA},
			rerankScore: 0.3,
		},
		{
			fused:       FusedResult{ChunkID: 2},
			chunkRec:    &db.ChunkRecord{ID: 2, StartLine: 1, EndLine: 1},
			fileRec:     &db.FileRecord{ID: 20, Path: pathB},
			rerankScore: 0.9,
		},
	}

	filtered, dedupMap := DedupIdenticalContent(candidates)

	if len(filtered) != 1 {
		t.Fatalf("expected 1 survivor, got %d", len(filtered))
	}
	if filtered[0].fused.ChunkID != 2 {
		t.Errorf("expected chunk 2 (higher score) to survive, got %d", filtered[0].fused.ChunkID)
	}

	dups := dedupMap[2]
	if len(dups) != 1 {
		t.Fatalf("expected 1 duplicate ref, got %d", len(dups))
	}
	if dups[0].File != pathA {
		t.Errorf("duplicate file = %q, want %q", dups[0].File, pathA)
	}
}
