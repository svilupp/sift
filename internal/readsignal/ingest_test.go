package readsignal

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"sift/internal/db"
)

func TestLoadReadSignals(t *testing.T) {
	root := t.TempDir()
	exportDir := filepath.Join(root, "memory")
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	exportPath := filepath.Join(exportDir, ".read-signals.tsv")
	data := "doc_path\ttotal_reads\tunique_days\tlast_read\n" +
		"knowledge/projects.md\t12\t5\t2026-03-17\n" +
		"knowledge/old.md\t3\t2\t2026-01-01\n"
	if err := os.WriteFile(exportPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	now := time.Date(2026, 3, 17, 12, 0, 0, 0, time.UTC)
	t.Setenv("TZ", "UTC")
	restoreNow := timeNow
	timeNow = func() time.Time { return now }
	t.Cleanup(func() { timeNow = restoreNow })

	records, sources, err := LoadReadSignals([]db.Collection{{Path: root}}, "memory/.read-signals.tsv", 14)
	if err != nil {
		t.Fatalf("LoadReadSignals: %v", err)
	}
	if len(sources) != 1 || sources[0] != exportPath {
		t.Fatalf("sources = %v, want [%s]", sources, exportPath)
	}
	if len(records) != 1 {
		t.Fatalf("records = %d, want 1 after age filter", len(records))
	}
	if records[0].DocPath != filepath.Join(root, "knowledge/projects.md") {
		t.Fatalf("doc path = %q", records[0].DocPath)
	}
	if records[0].TotalReads != 12 {
		t.Fatalf("total reads = %d, want 12", records[0].TotalReads)
	}
}

func TestLoadReadSignalsRejectsEscapingPaths(t *testing.T) {
	root := t.TempDir()
	exportDir := filepath.Join(root, "memory")
	if err := os.MkdirAll(exportDir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	exportPath := filepath.Join(exportDir, ".read-signals.tsv")
	data := "doc_path\ttotal_reads\tunique_days\tlast_read\n" +
		"../escape.md\t1\t1\t2026-03-17\n"
	if err := os.WriteFile(exportPath, []byte(data), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	if _, _, err := LoadReadSignals([]db.Collection{{Path: root}}, "memory/.read-signals.tsv", 0); err == nil {
		t.Fatal("expected escaping path error, got nil")
	}
}
