package aigen

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

// mockChatHandler returns a handler that emits canned chat responses
// for each request, and counts calls.
func mockChatHandler(resps []string, hits *int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(hits, 1)
		idx := int(n) - 1
		if idx >= len(resps) {
			idx = len(resps) - 1
		}
		writeJSON(w, 200, okBody(resps[idx]))
	}
}

func TestGenerateFolderSingleBatch(t *testing.T) {
	var hits int32
	resp := `{"purpose":"Docs root.","use_when":["debugging docs build"],"files":[{"path":"a.md","summary":"S1"},{"path":"b.md","summary":"S2"}]}`
	c, _ := newTestClient(t, mockChatHandler([]string{resp}, &hits))
	g := NewFolderGenerator(c)

	res, err := g.GenerateFolder(context.Background(), GenerateInput{
		Folder: FolderContext{RelPath: "docs"},
		Files: []FileBatch{
			{Path: "a.md", Words: 10, Bytes: 1000},
			{Path: "b.md", Words: 10, Bytes: 1000},
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if hits != 1 {
		t.Errorf("expected 1 LLM call, got %d", hits)
	}
	if res.Folder.Purpose != "Docs root." {
		t.Errorf("got purpose %q", res.Folder.Purpose)
	}
	if len(res.Folder.Files) != 2 {
		t.Errorf("got %d files", len(res.Folder.Files))
	}
}

func TestGenerateFolderStickyZeroCalls(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, mockChatHandler([]string{`{}`}, &hits))
	g := NewFolderGenerator(c)

	res, err := g.GenerateFolder(context.Background(), GenerateInput{
		Folder: FolderContext{RelPath: "x", ExistingPurpose: "Stays."},
		Files: []FileBatch{
			{Path: "a.md", CurrentSummary: "Existing S1"},
			{Path: "b.md", CurrentSummary: "Existing S2"},
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if hits != 0 {
		t.Errorf("expected 0 LLM calls (sticky), got %d", hits)
	}
	if res.Folder.Purpose != "Stays." {
		t.Errorf("expected sticky purpose; got %q", res.Folder.Purpose)
	}
	if len(res.Folder.Files) != 2 {
		t.Errorf("got %d files", len(res.Folder.Files))
	}
}

func TestGenerateFolderPartitioned(t *testing.T) {
	// 25 files each 1KB → triggers partition (file_count >20).
	files := []FileBatch{}
	want := []string{}
	for i := 0; i < 25; i++ {
		p := fmt.Sprintf("f%02d.md", i)
		files = append(files, FileBatch{Path: p, Words: 5, Bytes: 1024})
		want = append(want, p)
	}

	// Hits: 3 sub-batches (10, 10, 5) + 1 synthesis = 4 calls.
	var hits int32
	respFor := func(batchPaths []string) string {
		summaries := []map[string]string{}
		for _, p := range batchPaths {
			summaries = append(summaries, map[string]string{"path": p, "summary": "ok"})
		}
		body, _ := json.Marshal(map[string]any{
			"purpose":  "batch purpose",
			"use_when": []string{"finding test fixtures"},
			"files":    summaries,
		})
		return string(body)
	}

	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		// Read the full body to know which paths the prompt mentions.
		// Simpler: just respond with what we know about partition boundaries.
		switch n {
		case 1:
			// First sub-batch: top 10 by authority (alphabetic since equal scores).
			writeJSON(w, 200, okBody(respFor(want[:10])))
		case 2:
			writeJSON(w, 200, okBody(respFor(want[10:20])))
		case 3:
			writeJSON(w, 200, okBody(respFor(want[20:25])))
		case 4:
			// Synthesis: free-form purpose text.
			writeJSON(w, 200, okBody("Folder of test fixtures."))
		default:
			writeJSON(w, 500, map[string]string{"error": "too many calls"})
		}
	})

	g := NewFolderGenerator(c)
	res, err := g.GenerateFolder(context.Background(), GenerateInput{
		Folder: FolderContext{RelPath: "huge"},
		Files:  files,
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if hits != 4 {
		t.Errorf("expected 4 calls (3 sub + 1 synth), got %d", hits)
	}
	if res.Folder.Purpose != "Folder of test fixtures." {
		t.Errorf("expected synth purpose; got %q", res.Folder.Purpose)
	}
	if len(res.Folder.Files) != 25 {
		t.Errorf("expected 25 files; got %d", len(res.Folder.Files))
	}
}

func TestGenerateFolderPathMismatchRetry(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		switch n {
		case 1:
			// Mismatch: missing b.md.
			writeJSON(w, 200, okBody(`{"purpose":"P","use_when":["x"],"files":[{"path":"a.md","summary":"S1"}]}`))
		default:
			// Fixed second attempt.
			writeJSON(w, 200, okBody(`{"purpose":"P","use_when":["x"],"files":[{"path":"a.md","summary":"S1"},{"path":"b.md","summary":"S2"}]}`))
		}
	})
	g := NewFolderGenerator(c)
	res, err := g.GenerateFolder(context.Background(), GenerateInput{
		Folder: FolderContext{RelPath: "docs"},
		Files: []FileBatch{
			{Path: "a.md", Bytes: 100},
			{Path: "b.md", Bytes: 100},
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if hits != 2 {
		t.Errorf("expected 2 calls (mismatch + retry); got %d", hits)
	}
	if len(res.Folder.Files) != 2 {
		t.Errorf("expected 2 files; got %d", len(res.Folder.Files))
	}
}

func TestGenerateFolderPerFileFallback(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		switch n {
		case 1, 2:
			// Both attempts mismatch (missing b.md).
			writeJSON(w, 200, okBody(`{"purpose":"P","files":[{"path":"a.md","summary":"S1"}]}`))
		case 3:
			writeJSON(w, 200, okBody(`{"purpose":"only","files":[{"path":"a.md","summary":"per-a"}]}`))
		case 4:
			writeJSON(w, 200, okBody(`{"purpose":"only","files":[{"path":"b.md","summary":"per-b"}]}`))
		default:
			writeJSON(w, 500, map[string]string{"err": "too many"})
		}
	})
	g := NewFolderGenerator(c)
	res, err := g.GenerateFolder(context.Background(), GenerateInput{
		Folder: FolderContext{RelPath: "docs"},
		Files: []FileBatch{
			{Path: "a.md", Bytes: 100},
			{Path: "b.md", Bytes: 100},
		},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// 2 attempts at batch + 2 per-file = 4 calls.
	if hits != 4 {
		t.Errorf("expected 4 calls; got %d", hits)
	}
	if len(res.Folder.Files) != 2 {
		t.Errorf("got %d files", len(res.Folder.Files))
	}
	for _, f := range res.Folder.Files {
		if !strings.HasPrefix(f.Summary, "per-") {
			t.Errorf("expected per-file summary; got %q", f.Summary)
		}
	}
}

func TestGenerateFolderUseWhenRetryRecovers(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		switch n {
		case 1:
			// Missing use_when: should trigger retry.
			writeJSON(w, 200, okBody(`{"purpose":"P","files":[{"path":"a.md","summary":"S1"}]}`))
		default:
			writeJSON(w, 200, okBody(`{"purpose":"P","use_when":["x"],"files":[{"path":"a.md","summary":"S1"}]}`))
		}
	})
	g := NewFolderGenerator(c)
	res, err := g.GenerateFolder(context.Background(), GenerateInput{
		Folder: FolderContext{RelPath: "docs"},
		Files:  []FileBatch{{Path: "a.md", Bytes: 100}},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if hits != 2 {
		t.Errorf("expected 2 calls (missing use_when + retry); got %d", hits)
	}
	if len(res.Folder.UseWhen) == 0 {
		t.Errorf("expected non-empty use_when after retry; got %v", res.Folder.UseWhen)
	}
}

func TestGenerateFolderUseWhenRetryExhausted(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		// Both attempts return without use_when.
		writeJSON(w, 200, okBody(`{"purpose":"P","files":[{"path":"a.md","summary":"S1"}]}`))
	})
	g := NewFolderGenerator(c)
	_, err := g.GenerateFolder(context.Background(), GenerateInput{
		Folder: FolderContext{RelPath: "docs"},
		Files:  []FileBatch{{Path: "a.md", Bytes: 100}},
	})
	if err == nil {
		t.Fatal("expected error after retry exhausted")
	}
	if !strings.Contains(err.Error(), "use_when") {
		t.Errorf("expected error to mention use_when; got %v", err)
	}
	if hits != 2 {
		t.Errorf("expected exactly 2 calls (1 + retry); got %d", hits)
	}
}

func TestGenerateFolderEmptyFiles(t *testing.T) {
	var hits int32
	c, _ := newTestClient(t, mockChatHandler([]string{`{}`}, &hits))
	g := NewFolderGenerator(c)
	res, err := g.GenerateFolder(context.Background(), GenerateInput{
		Folder: FolderContext{ExistingPurpose: "Empty."},
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if hits != 0 {
		t.Errorf("expected 0 calls; got %d", hits)
	}
	if res.Folder.Purpose != "Empty." {
		t.Errorf("got %q", res.Folder.Purpose)
	}
}
