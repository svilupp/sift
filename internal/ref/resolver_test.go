package ref

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolverResolveRelativeAndCollectionFallback(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	docDir := filepath.Join(root, "docs")
	codeDir := filepath.Join(root, "internal", "search")
	if err := os.MkdirAll(docDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(codeDir, 0755); err != nil {
		t.Fatal(err)
	}

	docPath := filepath.Join(docDir, "PLAN.md")
	codePath := filepath.Join(codeDir, "search.go")
	if err := os.WriteFile(docPath, []byte("# plan\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(codePath, []byte("package search\n"), 0644); err != nil {
		t.Fatal(err)
	}

	resolver := NewResolver([]string{root})
	res := resolver.Resolve(docPath, "internal/search/search.go")

	if !res.Exists {
		t.Fatalf("Exists = false, want true")
	}
	if res.ResolvedPath != codePath {
		t.Fatalf("ResolvedPath = %q, want %q", res.ResolvedPath, codePath)
	}
}

func TestResolverResolveAbsoluteMissingFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	resolver := NewResolver(nil)
	target := filepath.Join(root, "missing.go")

	res := resolver.Resolve(filepath.Join(root, "PLAN.md"), target)
	if !filepath.IsAbs(res.ResolvedPath) {
		t.Fatalf("ResolvedPath = %q, want absolute", res.ResolvedPath)
	}
	if res.Exists {
		t.Fatalf("Exists = true, want false")
	}
}
