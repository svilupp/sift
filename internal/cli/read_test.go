package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestReadCLI_CollectionSectionJSON(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "docs", "architecture.md"), "# Architecture\n\nIntro.\n\n## 2. Data Flow\n\nFirst line.\nSecond line.\n\n## Other\n\nDone.\n")
	registerIndexCollection(t, "vault", root)

	cmd := newReadCmd()
	cmd.SetArgs([]string{"docs/architecture.md", "--collection", "vault", "--section", "data-flow", "--json"})
	var out bytes.Buffer
	cmd.SetOut(&out)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("read: %v", err)
	}
	var env readEnvelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if env.File != "docs/architecture.md" || env.Section != "2. Data Flow" || env.StartLine != 5 || env.EndLine != 9 {
		t.Fatalf("unexpected read envelope: %+v", env)
	}
	if env.Content != "## 2. Data Flow\n\nFirst line.\nSecond line.\n" {
		t.Fatalf("unexpected content: %q", env.Content)
	}
}

func TestReadCLI_RejectsCollectionEscape(t *testing.T) {
	registerIndexCollection(t, "vault", t.TempDir())
	cmd := newReadCmd()
	cmd.SetArgs([]string{"../secret.md", "--collection", "vault"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected collection path escape error")
	}
}
