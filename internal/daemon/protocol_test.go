package daemon

import (
	"encoding/json"
	"reflect"
	"testing"
)

// roundTrip marshals v to JSON, unmarshals into a fresh value of the same
// concrete type, and returns it. Failures are reported via t.Fatalf.
func roundTrip[T any](t *testing.T, v T) T {
	t.Helper()
	buf, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out T
	if err := json.Unmarshal(buf, &out); err != nil {
		t.Fatalf("unmarshal: %v\njson: %s", err, buf)
	}
	return out
}

func assertEqual[T any](t *testing.T, name string, want, got T) {
	t.Helper()
	if !reflect.DeepEqual(want, got) {
		w, _ := json.MarshalIndent(want, "", "  ")
		g, _ := json.MarshalIndent(got, "", "  ")
		t.Fatalf("%s round-trip mismatch:\nwant:\n%s\ngot:\n%s", name, w, g)
	}
}

func TestSearchRequestRoundTrip(t *testing.T) {
	req := SearchRequest{
		Query:        "hybrid retrieval pipeline",
		Collection:   "vault",
		Since:        "2w",
		TopK:         10,
		Threshold:    0.42,
		Adaptive:     true,
		File:         "docs/INFRA.md",
		OutputFormat: "json",
		SearchID:     "abc123",
	}
	assertEqual(t, "SearchRequest", req, roundTrip(t, req))
}

func TestSearchRequestOmitEmpty(t *testing.T) {
	req := SearchRequest{Query: "q"}
	buf, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Only "query" should appear; all other fields are omitempty.
	got := string(buf)
	want := `{"query":"q"}`
	if got != want {
		t.Fatalf("omitempty: want %s, got %s", want, got)
	}
}

func TestScoreComponentsRoundTrip(t *testing.T) {
	sc := ScoreComponents{BM25Rank: 1, VectorRank: 3, RRF: 0.0123, Rerank: 0.789, Final: 0.91}
	assertEqual(t, "ScoreComponents", sc, roundTrip(t, sc))
}

func TestDuplicateRefRoundTrip(t *testing.T) {
	d := DuplicateRef{File: "/a/b.md", StartLine: 10, EndLine: 20, Score: 0.55}
	assertEqual(t, "DuplicateRef", d, roundTrip(t, d))
}

func TestNeighborhoodRefRoundTrip(t *testing.T) {
	n := NeighborhoodRef{
		FilePath:   "/a/b.md",
		SectionID:  "intro",
		Heading:    "Intro",
		CharCount:  1234,
		HeadingLvl: 2,
	}
	assertEqual(t, "NeighborhoodRef", n, roundTrip(t, n))
}

func TestLinkRefRoundTrip(t *testing.T) {
	l := LinkRef{TargetPath: "/x/y.md", TargetSection: "auth", LinkType: "wiki"}
	assertEqual(t, "LinkRef", l, roundTrip(t, l))
}

func TestResultEntryRoundTrip(t *testing.T) {
	r := ResultEntry{
		Index:      "a",
		ChunkID:    42,
		File:       "/abs/path.md",
		Collection: "vault",
		StartLine:  10,
		EndLine:    25,
		Content:    "line 1\nline 2\n",
		Stale:      true,
		Score:      0.87,
		OpenCmd:    "code /abs/path.md:10",
		Highlights: []string{"<mark>auth</mark>"},
		Components: &ScoreComponents{BM25Rank: 1, VectorRank: 2, RRF: 0.1, Rerank: 0.5, Final: 0.7},
		Duplicates: []DuplicateRef{
			{File: "/abs/dup.md", StartLine: 1, EndLine: 5, Score: 0.5},
		},
		Section:         "auth-flow",
		Heading:         "Auth Flow",
		HeadingLevel:    2,
		SectionChars:    512,
		SubsectionCount: 3,
		Siblings: []NeighborhoodRef{
			{FilePath: "/abs/path.md", SectionID: "intro", Heading: "Intro", CharCount: 100, HeadingLvl: 2},
		},
		Related: []NeighborhoodRef{
			{FilePath: "/abs/other.md", SectionID: "auth-flow", Heading: "Auth Flow", CharCount: 250, HeadingLvl: 2},
		},
		Links: []LinkRef{
			{TargetPath: "/abs/x.md", TargetSection: "more", LinkType: "wiki"},
		},
	}
	assertEqual(t, "ResultEntry", r, roundTrip(t, r))
}

func TestResultEntryOmitEmpty(t *testing.T) {
	// Minimum-shaped entry: optional fields should drop out of the wire format.
	r := ResultEntry{
		Index:      "a",
		ChunkID:    1,
		File:       "f.md",
		Collection: "c",
		StartLine:  1,
		EndLine:    2,
		Content:    "x",
		Score:      0.1,
		OpenCmd:    "open f.md",
	}
	buf, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(buf)
	for _, mustNotContain := range []string{
		`"stale"`, `"highlights"`, `"components"`, `"duplicates"`,
		`"section"`, `"heading"`, `"heading_level"`, `"section_char_count"`,
		`"subsection_count"`, `"siblings"`, `"related"`, `"links"`,
	} {
		if contains(s, mustNotContain) {
			t.Errorf("unexpected key %s present in minimal payload: %s", mustNotContain, s)
		}
	}
}

func TestSearchMetaRoundTrip(t *testing.T) {
	m := SearchMeta{
		TotalTimeMs:     123,
		BM25TimeMs:      10,
		VectorTimeMs:    20,
		RerankTimeMs:    30,
		Reranked:        true,
		ResultCount:     5,
		BM25Results:     50,
		VectorResults:   60,
		TotalCandidates: 80,
		FilteredCount:   3,
		Cached:          false,
		WallParallelMs:  25,
	}
	assertEqual(t, "SearchMeta", m, roundTrip(t, m))
}

func TestSearchResponseRoundTrip(t *testing.T) {
	resp := SearchResponse{
		SearchID: "abc123",
		Query:    "auth flow",
		Results: []ResultEntry{
			{
				Index: "a", ChunkID: 1, File: "f.md", Collection: "c",
				StartLine: 1, EndLine: 2, Content: "hello",
				Score: 0.5, OpenCmd: "code f.md:1",
			},
		},
		Meta: SearchMeta{
			TotalTimeMs: 99, ResultCount: 1, Reranked: true,
		},
		FeedbackCmd: "sift feedback abc123 --positive a --negative ",
	}
	assertEqual(t, "SearchResponse", resp, roundTrip(t, resp))
}

func TestRefreshRequestRoundTrip(t *testing.T) {
	r := RefreshRequest{
		Collection: "vault",
		Full:       true,
		DryRun:     false,
		Files:      []string{"/a/b.md", "/a/c.md"},
	}
	assertEqual(t, "RefreshRequest", r, roundTrip(t, r))
}

func TestRefreshRequestOmitEmpty(t *testing.T) {
	r := RefreshRequest{}
	buf, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(buf) != `{}` {
		t.Fatalf("empty RefreshRequest should marshal to {}; got %s", buf)
	}
}

func TestProgressEventRoundTrip(t *testing.T) {
	e := ProgressEvent{
		Ts:         "2026-05-03T12:00:00Z",
		Phase:      "embed",
		FilesDone:  10,
		FilesTotal: 100,
		Message:    "embedding batch 2/20",
		Error:      "",
	}
	assertEqual(t, "ProgressEvent", e, roundTrip(t, e))
}

func TestProgressEventPhases(t *testing.T) {
	// Documented phase set — round-trip each.
	for _, phase := range []string{"scan", "chunk", "embed", "store", "done"} {
		e := ProgressEvent{Ts: "2026-05-03T12:00:00Z", Phase: phase}
		got := roundTrip(t, e)
		if got.Phase != phase {
			t.Errorf("phase %q lost in round-trip: got %q", phase, got.Phase)
		}
	}
}

func TestHealthResponseRoundTrip(t *testing.T) {
	h := HealthResponse{
		Ok:            true,
		Version:       "0.1.0",
		UptimeS:       3600,
		PID:           12345,
		RequestCount:  42,
		TLSDialsTotal: 7,
		Goroutines:    25,
		StartedAt:     "2026-05-03T11:00:00Z",
	}
	assertEqual(t, "HealthResponse", h, roundTrip(t, h))
}

func TestErrorResponseRoundTrip(t *testing.T) {
	e := ErrorResponse{
		Code:    "INVALID_QUERY",
		Message: "query must not be empty",
		Details: "request body: query: required",
	}
	assertEqual(t, "ErrorResponse", e, roundTrip(t, e))
}

func TestErrorResponseOmitEmpty(t *testing.T) {
	e := ErrorResponse{Code: "X", Message: "m"}
	buf, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(buf) != `{"code":"X","message":"m"}` {
		t.Fatalf("ErrorResponse omitempty: got %s", buf)
	}
}

// Verify the wire shape of SearchResponse matches the CLI envelope
// (internal/cli/search.go:402-425). This is the byte-equivalence guarantee:
// daemon callers see the same top-level keys as `sift search --json`.
func TestSearchResponseTopLevelKeys(t *testing.T) {
	resp := SearchResponse{
		SearchID:    "id",
		Query:       "q",
		Results:     []ResultEntry{},
		Meta:        SearchMeta{},
		FeedbackCmd: "fb",
	}
	buf, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(buf, &generic); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantKeys := []string{"search_id", "query", "results", "meta", "feedback_cmd"}
	for _, k := range wantKeys {
		if _, ok := generic[k]; !ok {
			t.Errorf("missing top-level key %q in %s", k, buf)
		}
	}
	if len(generic) != len(wantKeys) {
		t.Errorf("unexpected extra keys: got %d, want %d (%s)", len(generic), len(wantKeys), buf)
	}
}

// contains is a small helper to avoid pulling in strings just for a substring
// check inside the test file.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
