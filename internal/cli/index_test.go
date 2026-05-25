package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sift/internal/index"
)

// runIndexCheck builds a fresh `index check` command and executes it
// with the given args. Returns stdout/stderr buffers and the resulting
// (possibly typed) error so tests can assert exit codes.
func runIndexCheck(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	cmd := newIndexCmd()
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	cmd.SetArgs(append([]string{"check"}, args...))
	cmd.SetContext(context.Background())
	err := cmd.Execute()
	return out.String(), errBuf.String(), err
}

// seedSiftToml mirrors the test helper from internal/index, but here
// because that helper is package-private. Hashes the on-disk files so
// the resulting toml is fresh.
func seedSiftToml(t *testing.T, folder string, files []string, summary string) {
	t.Helper()
	idx := &index.FolderIndex{
		SchemaVersion: index.SchemaVersion,
		Files:         map[string]index.FileEntry{},
	}
	for _, name := range files {
		sig, err := index.SigOf(filepath.Join(folder, name))
		if err != nil {
			t.Fatalf("sig %s: %v", name, err)
		}
		idx.Files[name] = index.FileEntry{
			ContentHash: sig.ContentHash,
			HeadHash:    sig.HeadHash,
			TailHash:    sig.TailHash,
			Words:       sig.Words,
			Summary:     summary,
		}
	}
	if err := index.Save(filepath.Join(folder, index.FilenameSiftToml), idx); err != nil {
		t.Fatalf("save: %v", err)
	}
}

func writeFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestIndexCheck_CleanTree_Exit0(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.md"), "alpha content for test")
	writeFile(t, filepath.Join(root, "b.md"), "beta content for test")
	seedSiftToml(t, root, []string{"a.md", "b.md"}, "ok")

	stdout, _, err := runIndexCheck(t, root, "--json")
	if err != nil {
		t.Fatalf("expected no error, got %v: %s", err, stdout)
	}
	if IndexCheckExitCode(err) != 0 {
		t.Fatalf("expected exit code 0, got %d", IndexCheckExitCode(err))
	}
	var rep index.CheckReport
	if jerr := json.Unmarshal([]byte(stdout), &rep); jerr != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", jerr, stdout)
	}
	if !rep.Clean() {
		t.Fatalf("expected clean: %+v", rep.Summary)
	}
}

func TestIndexCheck_DefectsExit2(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "lonely.md"), "no metadata here")

	stdout, _, err := runIndexCheck(t, root, "--json")
	if err == nil {
		t.Fatalf("expected error (defects), got nil. stdout=%s", stdout)
	}
	if code := IndexCheckExitCode(err); code != 2 {
		t.Fatalf("expected exit code 2, got %d (err=%v)", code, err)
	}
	var rep index.CheckReport
	if jerr := json.Unmarshal([]byte(stdout), &rep); jerr != nil {
		t.Fatalf("invalid JSON: %v\noutput: %s", jerr, stdout)
	}
	if rep.Summary.Missing != 1 {
		t.Fatalf("expected one missing defect: %+v", rep.Summary)
	}
}

func TestIndexCheck_BadRootExit1(t *testing.T) {
	_, _, err := runIndexCheck(t, "/this/does/not/exist/zzzqqq", "--json")
	if err == nil {
		t.Fatalf("expected error for bad root")
	}
	if code := IndexCheckExitCode(err); code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
}

func TestIndexCheck_JSONFlagAndMarkdownFlagMutuallyExclusive(t *testing.T) {
	root := t.TempDir()
	_, _, err := runIndexCheck(t, root, "--json", "--markdown")
	if err == nil {
		t.Fatalf("expected error for conflicting flags")
	}
	if code := IndexCheckExitCode(err); code != 1 {
		t.Fatalf("expected exit code 1, got %d (err=%v)", code, err)
	}
}

func TestIndexCheck_MarkdownOutput(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "f.md"), "x")

	stdout, _, err := runIndexCheck(t, root, "--markdown")
	// missing sift.toml → defects → exit 2
	if code := IndexCheckExitCode(err); code != 2 {
		t.Fatalf("expected code 2, got %d", code)
	}
	if !strings.Contains(stdout, "# sift index check") {
		t.Fatalf("markdown output missing header: %s", stdout)
	}
	if !strings.Contains(stdout, "missing") {
		t.Fatalf("markdown output should mention missing: %s", stdout)
	}
}

func TestIndexCheck_OrphanedDetected(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "real.md"), "real")
	seedSiftToml(t, root, []string{"real.md"}, "ok")
	// new file on disk not in toml
	writeFile(t, filepath.Join(root, "new.md"), "new")

	stdout, _, err := runIndexCheck(t, root, "--json")
	if code := IndexCheckExitCode(err); code != 2 {
		t.Fatalf("expected code 2, got %d", code)
	}
	var rep index.CheckReport
	if jerr := json.Unmarshal([]byte(stdout), &rep); jerr != nil {
		t.Fatalf("invalid JSON: %v", jerr)
	}
	if rep.Summary.Orphaned != 1 {
		t.Fatalf("expected one orphan, got %+v", rep.Summary)
	}
}

func TestIndexCheck_StaleDetected(t *testing.T) {
	root := t.TempDir()
	body := "the original content of this file is plenty long enough to give head and tail hashes a real signature to bite into."
	writeFile(t, filepath.Join(root, "drift.md"), body)
	seedSiftToml(t, root, []string{"drift.md"}, "ok")

	// mutate the file
	writeFile(t, filepath.Join(root, "drift.md"), "totally new content with new opening words and a wholly different ending phrase entirely.")

	stdout, _, err := runIndexCheck(t, root, "--json")
	if code := IndexCheckExitCode(err); code != 2 {
		t.Fatalf("expected code 2, got %d", code)
	}
	var rep index.CheckReport
	if jerr := json.Unmarshal([]byte(stdout), &rep); jerr != nil {
		t.Fatalf("invalid JSON: %v", jerr)
	}
	if rep.Summary.Stale != 1 {
		t.Fatalf("expected one stale, got %+v", rep.Summary)
	}
}

func TestIndexCheck_ParseError(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, index.FilenameSiftToml), "==garbage==[[[[")
	writeFile(t, filepath.Join(root, "f.md"), "x")

	stdout, _, err := runIndexCheck(t, root, "--json")
	if code := IndexCheckExitCode(err); code != 2 {
		t.Fatalf("expected code 2, got %d", code)
	}
	var rep index.CheckReport
	if jerr := json.Unmarshal([]byte(stdout), &rep); jerr != nil {
		t.Fatalf("invalid JSON: %v", jerr)
	}
	if rep.Summary.ParseError != 1 {
		t.Fatalf("expected one parse error, got %+v", rep.Summary)
	}
}

func TestIndexCheck_IndexCheckExitCodeUnwrap(t *testing.T) {
	// Pass through arbitrary error: should return -1.
	if code := IndexCheckExitCode(errors.New("plain error")); code != -1 {
		t.Fatalf("expected -1 for plain error, got %d", code)
	}
	if code := IndexCheckExitCode(nil); code != 0 {
		t.Fatalf("expected 0 for nil, got %d", code)
	}
}
