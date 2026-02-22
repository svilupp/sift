package log

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLogger_LogAndRead(t *testing.T) {
	dir := t.TempDir()
	logger := NewLogger(dir, 12)

	type entry struct {
		Query   string `json:"query"`
		Results int    `json:"results"`
	}

	// Write a few entries.
	for i := 0; i < 5; i++ {
		err := logger.Log("searches", entry{Query: "test", Results: i})
		if err != nil {
			t.Fatalf("Log() error: %v", err)
		}
	}

	// Verify the file exists with correct naming.
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 log file, got %d", len(files))
	}

	name := files[0].Name()
	if name[:9] != "searches-" {
		t.Fatalf("unexpected file prefix: %s", name)
	}
	if filepath.Ext(name) != ".jsonl" {
		t.Fatalf("unexpected extension: %s", name)
	}

	// Read entries back.
	entries, err := logger.ReadRecent("searches", 10)
	if err != nil {
		t.Fatalf("ReadRecent() error: %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(entries))
	}

	// Newest first — last written entry has Results=4.
	var first entry
	if err := json.Unmarshal(entries[0], &first); err != nil {
		t.Fatalf("unmarshal first entry: %v", err)
	}
	if first.Results != 4 {
		t.Fatalf("expected Results=4 in newest entry, got %d", first.Results)
	}
}

func TestLogger_ReadRecent_LimitedCount(t *testing.T) {
	dir := t.TempDir()
	logger := NewLogger(dir, 12)

	for i := 0; i < 10; i++ {
		err := logger.Log("sql", map[string]int{"n": i})
		if err != nil {
			t.Fatal(err)
		}
	}

	entries, err := logger.ReadRecent("sql", 3)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(entries))
	}
}

func TestLogger_ReadRecent_NoFiles(t *testing.T) {
	dir := t.TempDir()
	logger := NewLogger(dir, 12)

	entries, err := logger.ReadRecent("searches", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}
}

func TestLogger_LogTypes(t *testing.T) {
	dir := t.TempDir()
	logger := NewLogger(dir, 12)

	err := logger.Log("searches", map[string]string{"q": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	err = logger.Log("feedback", map[string]string{"signal": "positive"})
	if err != nil {
		t.Fatal(err)
	}
	err = logger.Log("sql", map[string]string{"query": "SELECT 1"})
	if err != nil {
		t.Fatal(err)
	}

	for _, logType := range []string{"searches", "feedback", "sql"} {
		entries, err := logger.ReadRecent(logType, 10)
		if err != nil {
			t.Fatalf("ReadRecent(%q) error: %v", logType, err)
		}
		if len(entries) != 1 {
			t.Fatalf("expected 1 entry for %q, got %d", logType, len(entries))
		}
	}
}

func TestLogger_Cleanup(t *testing.T) {
	dir := t.TempDir()
	logger := NewLogger(dir, 2) // Keep only 2 weeks.

	// Create files simulating old weekly logs.
	now := time.Now().UTC()
	monday := mondayOf(now)

	// Current week file (should be kept).
	current := filepath.Join(dir, "searches-"+monday.Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(current, []byte(`{"q":"now"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// 3 weeks ago (should be deleted, older than 2 weeks).
	old := monday.AddDate(0, 0, -21)
	oldFile := filepath.Join(dir, "searches-"+old.Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(oldFile, []byte(`{"q":"old"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	// 1 week ago (should be kept).
	recent := monday.AddDate(0, 0, -7)
	recentFile := filepath.Join(dir, "searches-"+recent.Format("2006-01-02")+".jsonl")
	if err := os.WriteFile(recentFile, []byte(`{"q":"recent"}`+"\n"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := logger.Cleanup(); err != nil {
		t.Fatalf("Cleanup() error: %v", err)
	}

	// Verify old file removed.
	if _, err := os.Stat(oldFile); !os.IsNotExist(err) {
		t.Fatal("expected old file to be deleted")
	}

	// Current and recent files should still exist.
	if _, err := os.Stat(current); err != nil {
		t.Fatalf("current file should exist: %v", err)
	}
	if _, err := os.Stat(recentFile); err != nil {
		t.Fatalf("recent file should exist: %v", err)
	}
}

func TestLogger_CleanupEmptyDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "nonexistent")
	logger := NewLogger(dir, 12)

	// Should not error on nonexistent dir.
	if err := logger.Cleanup(); err != nil {
		t.Fatalf("Cleanup() on nonexistent dir: %v", err)
	}
}

func TestMondayOf(t *testing.T) {
	tests := []struct {
		input time.Time
		want  string
	}{
		{time.Date(2026, 2, 7, 15, 0, 0, 0, time.UTC), "2026-02-02"},  // Saturday -> Monday
		{time.Date(2026, 2, 2, 0, 0, 0, 0, time.UTC), "2026-02-02"},   // Monday -> Monday
		{time.Date(2026, 2, 8, 0, 0, 0, 0, time.UTC), "2026-02-02"},   // Sunday -> same Monday
		{time.Date(2026, 2, 9, 0, 0, 0, 0, time.UTC), "2026-02-09"},   // Next Monday
		{time.Date(2026, 2, 4, 10, 30, 0, 0, time.UTC), "2026-02-02"}, // Wednesday -> Monday
	}

	for _, tt := range tests {
		got := mondayOf(tt.input).Format("2006-01-02")
		if got != tt.want {
			t.Errorf("mondayOf(%v) = %s, want %s", tt.input, got, tt.want)
		}
	}
}

func TestParseDateFromFilename(t *testing.T) {
	tests := []struct {
		name    string
		want    string
		wantErr bool
	}{
		{"searches-2026-02-03.jsonl", "2026-02-03", false},
		{"sql-2026-01-27.jsonl", "2026-01-27", false},
		{"feedback-2026-02-10.jsonl", "2026-02-10", false},
		{"bad.jsonl", "", true},
		{"x-.jsonl", "", true},
	}

	for _, tt := range tests {
		got, err := parseDateFromFilename(tt.name)
		if (err != nil) != tt.wantErr {
			t.Errorf("parseDateFromFilename(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			continue
		}
		if !tt.wantErr && got.Format("2006-01-02") != tt.want {
			t.Errorf("parseDateFromFilename(%q) = %s, want %s", tt.name, got.Format("2006-01-02"), tt.want)
		}
	}
}

func TestLogger_JSONLFormat(t *testing.T) {
	dir := t.TempDir()
	logger := NewLogger(dir, 12)

	type entry struct {
		Msg string `json:"msg"`
	}

	if err := logger.Log("searches", entry{Msg: "hello"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Log("searches", entry{Msg: "world"}); err != nil {
		t.Fatal(err)
	}

	// Read raw file and verify each line is valid JSON.
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %d", len(files))
	}

	lines, err := readAllLines(filepath.Join(dir, files[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}

	for i, line := range lines {
		var m map[string]string
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("line %d is not valid JSON: %v", i, err)
		}
	}
}

func TestLogger_DefaultMaxWeeks(t *testing.T) {
	logger := NewLogger(t.TempDir(), 0) // 0 should default to 12
	if logger.maxWeeks != 12 {
		t.Fatalf("expected maxWeeks=12, got %d", logger.maxWeeks)
	}
}
