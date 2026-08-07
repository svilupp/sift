package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sift/internal/config"
	"sift/internal/db"
)

func setupIndexedCLIEnv(t *testing.T) (string, string) {
	t.Helper()

	siftHome, collDir := setupTestEnv(t)

	cmd := NewRootCmd("test")
	cmd.SetArgs([]string{"config", "init"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("config init: %v", err)
	}

	cmd = NewRootCmd("test")
	cmd.SetArgs([]string{"collections", "add", "vault", collDir})
	buf.Reset()
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("collections add: %v", err)
	}

	cmd = NewRootCmd("test")
	cmd.SetArgs([]string{"refresh"})
	buf.Reset()
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("refresh: %v", err)
	}

	return siftHome, collDir
}

func TestSearchAgentOutput(t *testing.T) {
	_, _ = setupIndexedCLIEnv(t)

	cmd := NewRootCmd("test")
	cmd.SetArgs([]string{"search", "authentication token", "--agent"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("search --agent: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, `Search: "authentication token"`) {
		t.Fatalf("missing header: %s", output)
	}
	if !strings.Contains(output, `--section "authentication-flow"`) {
		t.Fatalf("missing section flag: %s", output)
	}
	if !strings.Contains(output, "HINT: Scope search with --collection NAME, then use sift read") {
		t.Fatalf("missing hint footer: %s", output)
	}
	if !strings.Contains(output, "Feedback: sift feedback") {
		t.Fatalf("missing feedback footer: %s", output)
	}
	if strings.Contains(output, "(bm25") || strings.Contains(output, "(reranked") {
		t.Fatalf("agent output should not expose ranking mode: %s", output)
	}
}

func TestSearchAgentOutputNativeReadHint(t *testing.T) {
	_, _ = setupIndexedCLIEnv(t)

	cmd := NewRootCmd("test")
	cmd.SetArgs([]string{"search", "authentication token", "--collection", "vault", "--agent"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("search --agent: %v", err)
	}
	if !strings.Contains(buf.String(), `HINT: sift read <file> --collection "vault" --section "<section>"`) {
		t.Fatalf("missing directly reusable sift read hint: %s", buf.String())
	}
}

func TestSearchAgentOutputReadCommandOverride(t *testing.T) {
	_, _ = setupIndexedCLIEnv(t)

	cmd := NewRootCmd("test")
	cmd.SetArgs([]string{"search", "authentication token", "--agent", "--read-command", "mem read"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("search --agent --read-command: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, `HINT: mem read <file> --section "<section>" to read a section.`) {
		t.Fatalf("missing mem override hint: %s", output)
	}
}

func TestSearchCompactOutput(t *testing.T) {
	_, _ = setupIndexedCLIEnv(t)

	cmd := NewRootCmd("test")
	cmd.SetArgs([]string{"search", "database query", "--compact"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("search --compact: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "[a]") {
		t.Fatalf("missing grouped label: %s", output)
	}
	if !strings.Contains(output, "[database-design]") {
		t.Fatalf("missing compact section slug: %s", output)
	}
	if strings.Contains(output, "Feedback:") {
		t.Fatalf("compact output should omit feedback footer: %s", output)
	}
}

func TestKeywordsCommand(t *testing.T) {
	_, _ = setupIndexedCLIEnv(t)

	cmd := NewRootCmd("test")
	cmd.SetArgs([]string{"keywords"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("keywords: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "./") {
		t.Fatalf("expected root directory group: %s", output)
	}
	if !strings.Contains(output, "keywords:") || !strings.Contains(output, "notes: 3") {
		t.Fatalf("unexpected keywords output: %s", output)
	}
}

func TestRefreshImportsReadSignals(t *testing.T) {
	_, collDir := setupIndexedCLIEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}
	cfg.Scoring.ReadSignalPath = "reads.tsv"
	if err := cfg.Save(); err != nil {
		t.Fatalf("config.Save: %v", err)
	}

	readSignalData := "doc_path\ttotal_reads\tunique_days\tlast_read\n" +
		"auth.md\t7\t3\t" + time.Now().Format("2006-01-02") + "\n"
	if err := os.WriteFile(filepath.Join(collDir, "reads.tsv"), []byte(readSignalData), 0o644); err != nil {
		t.Fatalf("WriteFile reads.tsv: %v", err)
	}

	cmd := NewRootCmd("test")
	cmd.SetArgs([]string{"refresh"})
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("refresh with read signals: %v", err)
	}
	if !strings.Contains(buf.String(), "Read signals: 1 docs") {
		t.Fatalf("missing read-signal status line: %s", buf.String())
	}

	dbPath, _ := config.DBPath()
	database, err := db.Open(dbPath)
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer database.Close()

	counts, err := database.GetReadCounts()
	if err != nil {
		t.Fatalf("GetReadCounts: %v", err)
	}
	if counts[filepath.Join(collDir, "auth.md")].TotalReads != 7 {
		t.Fatalf("auth.md reads = %d, want 7", counts[filepath.Join(collDir, "auth.md")].TotalReads)
	}
}
