package index

import (
	"bytes"
	"crypto/rand"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestComputeSig_Deterministic(t *testing.T) {
	data := []byte("the quick brown fox jumps over the lazy dog\n")
	a := ComputeSig(data)
	b := ComputeSig(data)
	if a != b {
		t.Fatalf("ComputeSig non-deterministic: %+v vs %+v", a, b)
	}
}

func TestComputeSig_DifferentContentDifferentHash(t *testing.T) {
	a := ComputeSig([]byte("hello world"))
	b := ComputeSig([]byte("hello mars"))
	if a.ContentHash == b.ContentHash {
		t.Fatalf("distinct content should produce distinct hashes")
	}
}

func TestComputeSig_ShortFileHeadEqualsTail(t *testing.T) {
	// A file with fewer than HeadTailWindow words should have head and
	// tail collapsed onto the same hash (whole content covers both).
	data := []byte("only a few words here")
	sig := ComputeSig(data)
	if sig.Words != 5 {
		t.Fatalf("expected 5 words got %d", sig.Words)
	}
	if sig.HeadHash != sig.TailHash {
		t.Fatalf("for short files head and tail should match")
	}
}

func TestComputeSig_LongFileHeadDiffersFromTail(t *testing.T) {
	// 1500 words with distinct head and tail.
	words := make([]string, 1500)
	for i := 0; i < 500; i++ {
		words[i] = "intro" + itoa(i)
	}
	for i := 500; i < 1000; i++ {
		words[i] = "mid" + itoa(i)
	}
	for i := 1000; i < 1500; i++ {
		words[i] = "tail" + itoa(i)
	}
	sig := ComputeSig([]byte(strings.Join(words, " ")))
	if sig.HeadHash == sig.TailHash {
		t.Fatalf("for long files head and tail must differ")
	}
}

func TestComputeSig_HashFormatStable(t *testing.T) {
	sig := ComputeSig([]byte("hello"))
	if len(sig.ContentHash) != 16 {
		t.Fatalf("content hash should be 16 hex chars, got %d (%q)", len(sig.ContentHash), sig.ContentHash)
	}
}

func TestSigOf_FileMatchesComputeSig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.txt")
	body := []byte("the quick brown fox\nover the lazy dog\n")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := SigOf(path)
	if err != nil {
		t.Fatalf("SigOf: %v", err)
	}
	want := ComputeSig(body)
	if got != want {
		t.Fatalf("SigOf=%+v vs ComputeSig=%+v", got, want)
	}
}

func TestSigOf_LargeFileStreamingMatchesInMemory(t *testing.T) {
	// Construct a >1MB body so the streaming path triggers, but with
	// totally deterministic content. Then verify SigOf == ComputeSig.
	dir := t.TempDir()
	path := filepath.Join(dir, "big.txt")

	var buf bytes.Buffer
	for i := 0; buf.Len() < (1<<20)+1024; i++ {
		buf.WriteString("word")
		buf.WriteString(itoa(i))
		if i%17 == 0 {
			buf.WriteByte('\n')
		} else {
			buf.WriteByte(' ')
		}
	}
	body := buf.Bytes()
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := SigOf(path)
	if err != nil {
		t.Fatalf("SigOf: %v", err)
	}
	want := ComputeSig(body)
	if got != want {
		t.Fatalf("streaming sig diverges from in-memory:\n got=%+v\nwant=%+v", got, want)
	}
}

func TestSigOf_HugeRandomFileNoOOM(t *testing.T) {
	if testing.Short() {
		t.Skip("skip large file test in -short mode")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.bin")
	// 5MB random bytes — guards against unbounded memory in the streaming
	// path. We don't compare to in-memory here (also fine; this is a
	// resource probe).
	huge := make([]byte, 5<<20)
	if _, err := rand.Read(huge); err != nil {
		t.Fatalf("rand: %v", err)
	}
	if err := os.WriteFile(path, huge, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := SigOf(path); err != nil {
		t.Fatalf("SigOf huge: %v", err)
	}
}

func TestSplitWords(t *testing.T) {
	got := SplitWords("  hello\tworld\n\nthird ")
	if len(got) != 3 || got[0] != "hello" || got[1] != "world" || got[2] != "third" {
		t.Fatalf("SplitWords got %v", got)
	}
}
