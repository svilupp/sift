package index

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestPrototypeCorpus_RoundTrip loads every hand-written sift.toml in
// experiments/sift-toml-prototype/ and asserts the writer produces
// byte-identical output. Any new prototype file is automatically
// covered.
func TestPrototypeCorpus_RoundTrip(t *testing.T) {
	t.Helper()
	root := prototypeRoot(t)
	if _, err := os.Stat(root); os.IsNotExist(err) {
		t.Skipf("prototype tree not present at %s", root)
	}

	var found int
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() || filepath.Base(path) != FilenameSiftToml {
			return nil
		}
		found++
		t.Run(strings.TrimPrefix(path, root+string(filepath.Separator)), func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			idx, err := LoadBytes(data)
			if err != nil {
				t.Fatalf("LoadBytes %s: %v", path, err)
			}
			out, err := Marshal(idx)
			if err != nil {
				t.Fatalf("Marshal %s: %v", path, err)
			}
			if !bytes.Equal(out, data) {
				t.Fatalf("round-trip not byte-identical for %s\n--- input ---\n%s\n--- output ---\n%s",
					path, data, out)
			}
		})
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if found < 5 {
		t.Fatalf("expected at least 5 prototype sift.toml files, found %d", found)
	}
}

// prototypeRoot resolves the path to experiments/sift-toml-prototype/
// relative to this test file, regardless of the working directory.
func prototypeRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatalf("runtime.Caller failed")
	}
	// thisFile = .../sift/internal/index/parser_corpus_test.go
	repoRoot := filepath.Join(filepath.Dir(thisFile), "..", "..")
	return filepath.Clean(filepath.Join(repoRoot, "experiments", "sift-toml-prototype"))
}
