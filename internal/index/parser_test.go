package index

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadBytes_Empty(t *testing.T) {
	idx, err := LoadBytes(nil)
	if err != nil {
		t.Fatalf("LoadBytes(nil): %v", err)
	}
	if idx.SchemaVersion != SchemaVersion {
		t.Fatalf("default SchemaVersion = %d, want %d", idx.SchemaVersion, SchemaVersion)
	}
	if idx.Files == nil || idx.Folders == nil {
		t.Fatalf("Files/Folders should be non-nil empty maps")
	}
	if len(idx.Files) != 0 || len(idx.Folders) != 0 {
		t.Fatalf("Files/Folders should be empty")
	}
}

func TestLoadBytes_Minimal(t *testing.T) {
	in := []byte("schema_version = 1\nignore = false\n")
	idx, err := LoadBytes(in)
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if idx.SchemaVersion != 1 {
		t.Fatalf("SchemaVersion = %d", idx.SchemaVersion)
	}
	if idx.Ignore {
		t.Fatalf("Ignore should be false")
	}
}

func TestLoadBytes_Full(t *testing.T) {
	in := []byte(`schema_version = 1
ignore = false
purpose = "Architecture planning material."
use_when = ["Researching agent-next architecture decisions", "Finding rollout plans"]

[refresh]
file_count = 17
word_count = 48210

[files."PROPOSAL.md"]
ignore = false
content_hash = "a91c22"
head_hash = "71b0ef"
tail_hash = "09cc31"
words = 4200
summary = "Canonical proposal."

[folders."logs"]
ignore = true
`)
	idx, err := LoadBytes(in)
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if idx.Purpose != "Architecture planning material." {
		t.Fatalf("Purpose = %q", idx.Purpose)
	}
	if len(idx.UseWhen) != 2 {
		t.Fatalf("UseWhen len = %d", len(idx.UseWhen))
	}
	if idx.Refresh.FileCount != 17 || idx.Refresh.WordCount != 48210 {
		t.Fatalf("Refresh = %+v", idx.Refresh)
	}
	f, ok := idx.Files["PROPOSAL.md"]
	if !ok {
		t.Fatalf("missing PROPOSAL.md")
	}
	if f.ContentHash != "a91c22" || f.Words != 4200 || f.Summary != "Canonical proposal." {
		t.Fatalf("file entry = %+v", f)
	}
	logs, ok := idx.Folders["logs"]
	if !ok || !logs.Ignore {
		t.Fatalf("folders[logs] = %+v ok=%v", logs, ok)
	}
}

func TestLoadBytes_UnsupportedSchemaVersion(t *testing.T) {
	_, err := LoadBytes([]byte("schema_version = 99\n"))
	if err == nil {
		t.Fatalf("expected error for schema_version=99")
	}
	if !strings.Contains(err.Error(), "schema_version") {
		t.Fatalf("error %q should mention schema_version", err)
	}
}

func TestLoadBytes_Malformed(t *testing.T) {
	_, err := LoadBytes([]byte("schema_version = "))
	if err == nil {
		t.Fatalf("expected parse error")
	}
}

func TestLoadBytes_UnicodeInPaths(t *testing.T) {
	in := []byte(`[files."nóte-ünïcødé.md"]
ignore = false
content_hash = "a"
head_hash = "b"
tail_hash = "c"
words = 1
`)
	idx, err := LoadBytes(in)
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if _, ok := idx.Files["nóte-ünïcødé.md"]; !ok {
		t.Fatalf("missing unicode key; got %v", idx.Files)
	}
}

func TestLoad_MissingFile(t *testing.T) {
	tmp := t.TempDir()
	_, err := Load(filepath.Join(tmp, "nope.toml"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load missing should return os.ErrNotExist, got %v", err)
	}
}

func TestLoad_BadFileContext(t *testing.T) {
	tmp := t.TempDir()
	path := filepath.Join(tmp, "sift.toml")
	if err := os.WriteFile(path, []byte("schema_version = "), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatalf("expected parse error")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error %q should mention path", err)
	}
}
