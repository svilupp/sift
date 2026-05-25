package ref

import (
	"os"
	"path/filepath"
	"testing"
)

func TestEngineValidateDocumentLinks(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	docPath := filepath.Join(root, "README.md")
	targetPath := filepath.Join(root, "ARCHITECTURE.md")

	if err := os.WriteFile(targetPath, []byte("# Trust Zones\n\n## Runtime Model\n"), 0644); err != nil {
		t.Fatal(err)
	}

	content := []byte(
		"See [runtime](ARCHITECTURE.md#runtime-model).\n" +
			"See [missing](ARCHITECTURE.md#missing-anchor).\n" +
			"See [[MISSING]].\n" +
			"Jump [local](#overview).\n" +
			"# Overview\n",
	)

	engine := NewEngine(Options{CollectionRoots: []string{root}})
	results, err := engine.ValidateDocumentLinks(docPath, content)
	if err != nil {
		t.Fatalf("ValidateDocumentLinks: %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("len(results) = %d, want 4", len(results))
	}

	byRaw := make(map[string]LinkValidationResult, len(results))
	for _, result := range results {
		byRaw[result.Link.Raw] = result
	}

	if got := byRaw["[runtime](ARCHITECTURE.md#runtime-model)"].Status; got != LinkStatusValid {
		t.Fatalf("runtime link status = %s, want %s", got, LinkStatusValid)
	}
	if got := byRaw["[missing](ARCHITECTURE.md#missing-anchor)"].Status; got != LinkStatusMissingAnchor {
		t.Fatalf("missing anchor status = %s, want %s", got, LinkStatusMissingAnchor)
	}
	if got := byRaw["[[MISSING]]"].Status; got != LinkStatusMissingFile {
		t.Fatalf("missing file status = %s, want %s", got, LinkStatusMissingFile)
	}
	local := byRaw["[local](#overview)"]
	if local.Status != LinkStatusValid {
		t.Fatalf("local anchor status = %s, want %s", local.Status, LinkStatusValid)
	}
	if local.Link.TargetPath != docPath {
		t.Fatalf("local anchor TargetPath = %q, want %q", local.Link.TargetPath, docPath)
	}
}

func TestEngineValidateDocumentLinksIgnoresCodeExamples(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	docPath := filepath.Join(root, "README.md")

	content := []byte(
		"Inline code `[[INLINE]]` should not lint.\n" +
			"```md\n[[FENCED]]\n[bad](missing.md)\n```\n",
	)

	engine := NewEngine(Options{CollectionRoots: []string{root}})
	results, err := engine.ValidateDocumentLinks(docPath, content)
	if err != nil {
		t.Fatalf("ValidateDocumentLinks: %v", err)
	}
	if len(results) != 0 {
		t.Fatalf("len(results) = %d, want 0", len(results))
	}
}
