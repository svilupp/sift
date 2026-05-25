package index

import (
	"strings"
	"testing"
)

// buildBody synthesizes a deterministic body of N words with a stable
// intro and conclusion. Words 0..499 are intro, 500..N-501 are middle,
// last 500 are conclusion. The returned string can be mutated word-wise
// to simulate real edit patterns for the freshness tuple.
func buildBody(t *testing.T, total int) []string {
	t.Helper()
	if total < 1100 {
		t.Fatalf("buildBody: total=%d too short for head/tail/middle exercise", total)
	}
	out := make([]string, total)
	for i := 0; i < total; i++ {
		switch {
		case i < 500:
			out[i] = "intro" + itoa(i)
		case i >= total-500:
			out[i] = "tail" + itoa(i)
		default:
			out[i] = "mid" + itoa(i)
		}
	}
	return out
}

func itoa(n int) string {
	// Tiny helper to avoid importing strconv (keeps test compile clean).
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	digits := []byte{}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		digits = append([]byte{'-'}, digits...)
	}
	return string(digits)
}

func TestDecide_SevenScenarios(t *testing.T) {
	const total = 2000

	// Common base: an unmutated body of 2000 words.
	baseWords := buildBody(t, total)
	baseSig := ComputeSig([]byte(strings.Join(baseWords, " ")))
	prev := FileEntry{
		ContentHash: baseSig.ContentHash,
		HeadHash:    baseSig.HeadHash,
		TailHash:    baseSig.TailHash,
		Words:       baseSig.Words,
	}

	type scenario struct {
		name   string
		mutate func(words []string) []string
		want   Decision
	}

	scenarios := []scenario{
		{
			name: "typo (1-byte change in middle)",
			mutate: func(w []string) []string {
				out := append([]string(nil), w...)
				// Mutate one word in the middle, way outside head/tail.
				out[1000] = out[1000] + "x"
				return out
			},
			want: DecisionUpdateMechanical,
		},
		{
			name: "intro rewrite",
			mutate: func(w []string) []string {
				out := append([]string(nil), w...)
				for i := 0; i < 100; i++ {
					out[i] = "rewriteIntro" + itoa(i)
				}
				return out
			},
			want: DecisionRegenerate,
		},
		{
			name: "conclusion rewrite",
			mutate: func(w []string) []string {
				out := append([]string(nil), w...)
				for i := total - 100; i < total; i++ {
					out[i] = "rewriteConclusion" + itoa(i)
				}
				return out
			},
			want: DecisionRegenerate,
		},
		{
			name: "middle 5% change (no head/tail/word delta)",
			mutate: func(w []string) []string {
				out := append([]string(nil), w...)
				// 5% = 100 words. Change 100 of the middle words.
				for i := 800; i < 900; i++ {
					out[i] = out[i] + "_x"
				}
				return out
			},
			want: DecisionUpdateMechanical,
		},
		{
			name: "middle 15% change in word count",
			mutate: func(w []string) []string {
				out := append([]string(nil), w...)
				// Insert 300 extra words (~15%) into the middle. Head/tail
				// 500-word windows stay stable.
				extra := make([]string, 0, 300)
				for i := 0; i < 300; i++ {
					extra = append(extra, "added"+itoa(i))
				}
				return append(append(append([]string{}, out[:1000]...), extra...), out[1000:]...)
			},
			want: DecisionRegenerate,
		},
		{
			name: "append-only (tail hash flips)",
			mutate: func(w []string) []string {
				out := append([]string(nil), w...)
				// Append 50 new words at end — tail window now contains them.
				for i := 0; i < 50; i++ {
					out = append(out, "newend"+itoa(i))
				}
				return out
			},
			want: DecisionRegenerate,
		},
	}

	for _, sc := range scenarios {
		t.Run(sc.name, func(t *testing.T) {
			mutWords := sc.mutate(baseWords)
			mutSig := ComputeSig([]byte(strings.Join(mutWords, " ")))
			if mutSig.ContentHash == baseSig.ContentHash {
				t.Fatalf("mutation produced identical content hash; mutate is a no-op")
			}
			got := Decide(&prev, &mutSig)
			if got != sc.want {
				t.Fatalf("scenario %q: Decide=%s want=%s\n  prev=%+v\n  cur=%+v",
					sc.name, got, sc.want, prev, mutSig)
			}
		})
	}

	// Seventh scenario: rename — delete + new with matching content_hash.
	t.Run("rename detected via content hash match", func(t *testing.T) {
		deleted := map[string]FileEntry{
			"old/path.md": {ContentHash: baseSig.ContentHash, Summary: "Original summary."},
		}
		newSig := baseSig // identical content at new path
		matchedPath, matchedEntry, ok := MatchRename(newSig, deleted)
		if !ok {
			t.Fatalf("MatchRename should detect the rename")
		}
		if matchedPath != "old/path.md" {
			t.Fatalf("matched path = %q", matchedPath)
		}
		if matchedEntry.Summary != "Original summary." {
			t.Fatalf("did not preserve summary for porting")
		}
	})
}

func TestDecide_NoopWhenContentHashUnchanged(t *testing.T) {
	prev := &FileEntry{ContentHash: "abc"}
	cur := &FileSig{ContentHash: "abc"}
	if got := Decide(prev, cur); got != DecisionNoop {
		t.Fatalf("Decide=%s want noop", got)
	}
}

func TestDecide_NewAndDeleted(t *testing.T) {
	if got := Decide(nil, &FileSig{}); got != DecisionNew {
		t.Fatalf("nil prev should be new, got %s", got)
	}
	if got := Decide(&FileEntry{}, nil); got != DecisionDeleted {
		t.Fatalf("nil cur should be deleted, got %s", got)
	}
}

func TestDecisionString(t *testing.T) {
	cases := []struct {
		d    Decision
		want string
	}{
		{DecisionNoop, "noop"},
		{DecisionUpdateMechanical, "update_mechanical"},
		{DecisionRegenerate, "regenerate"},
		{DecisionNew, "new"},
		{DecisionDeleted, "deleted"},
		{DecisionRename, "rename"},
		{Decision(99), "unknown"},
	}
	for _, c := range cases {
		if c.d.String() != c.want {
			t.Fatalf("%d.String() = %q want %q", c.d, c.d.String(), c.want)
		}
	}
}
