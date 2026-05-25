package search

import (
	"math"
	"testing"
	"time"

	"sift/internal/config"
)

func TestComputeRecency(t *testing.T) {
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)
	halfLife := 30.0

	tests := []struct {
		name     string
		ageDays  float64
		halfLife float64
		wantMin  float64
		wantMax  float64
	}{
		{
			name:     "0 days old",
			ageDays:  0,
			halfLife: halfLife,
			wantMin:  0.99,
			wantMax:  1.01,
		},
		{
			name:     "30 days old (half_life=30)",
			ageDays:  30,
			halfLife: halfLife,
			wantMin:  0.49,
			wantMax:  0.51,
		},
		{
			name:     "60 days old",
			ageDays:  60,
			halfLife: halfLife,
			wantMin:  0.24,
			wantMax:  0.26,
		},
		{
			name:     "1 day old",
			ageDays:  1,
			halfLife: halfLife,
			wantMin:  0.97,
			wantMax:  0.98,
		},
		{
			name:     "zero half life returns 1.0",
			ageDays:  30,
			halfLife: 0,
			wantMin:  0.99,
			wantMax:  1.01,
		},
		{
			name:     "future mtime clamped to 0",
			ageDays:  -5,
			halfLife: halfLife,
			wantMin:  0.99,
			wantMax:  1.01,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mtime := now.Add(-time.Duration(tt.ageDays*24) * time.Hour)
			got := ComputeRecency(mtime, now, tt.halfLife)
			if got < tt.wantMin || got > tt.wantMax {
				t.Errorf("ComputeRecency(age=%.0f days, halfLife=%.0f) = %.6f, want [%.2f, %.2f]",
					tt.ageDays, tt.halfLife, got, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestFeedbackBoost(t *testing.T) {
	tests := []struct {
		name string
		good int
		bad  int
		want float64
	}{
		{
			name: "no signals returns 1.0",
			good: 0,
			bad:  0,
			want: 1.0,
		},
		{
			name: "1 good 0 bad",
			good: 1,
			bad:  0,
			want: 0.7 + 0.6*(2.0/3.0), // 1.1
		},
		{
			name: "3 good 0 bad",
			good: 3,
			bad:  0,
			want: 0.7 + 0.6*(4.0/5.0), // 1.18
		},
		{
			name: "0 good 3 bad",
			good: 0,
			bad:  3,
			want: 0.7 + 0.6*(1.0/5.0), // 0.82
		},
		{
			name: "10 good 1 bad",
			good: 10,
			bad:  1,
			want: 0.7 + 0.6*(11.0/13.0), // ~1.2077
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FeedbackBoost(tt.good, tt.bad)
			if math.Abs(got-tt.want) > 0.001 {
				t.Errorf("FeedbackBoost(%d, %d) = %.6f, want %.6f", tt.good, tt.bad, got, tt.want)
			}
		})
	}
}

func TestBacklinkBoost(t *testing.T) {
	tests := []struct {
		name   string
		count  int
		weight float64
		want   float64
	}{
		{name: "zero backlinks", count: 0, weight: 0.1, want: 1.0},
		{name: "one backlink", count: 1, weight: 0.1, want: 1.1},
		{name: "three backlinks", count: 3, weight: 0.1, want: 1.2},
		{name: "disabled weight", count: 7, weight: 0, want: 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := BacklinkBoost(tt.count, tt.weight)
			if math.Abs(got-tt.want) > 0.001 {
				t.Errorf("BacklinkBoost(%d, %.2f) = %.6f, want %.6f", tt.count, tt.weight, got, tt.want)
			}
		})
	}
}

func TestReadBoost(t *testing.T) {
	tests := []struct {
		name   string
		count  int
		weight float64
		want   float64
	}{
		{name: "zero reads", count: 0, weight: 0.05, want: 1.0},
		{name: "one read", count: 1, weight: 0.05, want: 1.05},
		{name: "nine reads", count: 9, weight: 0.05, want: 1.15},
		{name: "disabled weight", count: 25, weight: 0, want: 1.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ReadBoost(tt.count, tt.weight)
			if math.Abs(got-tt.want) > 0.001 {
				t.Errorf("ReadBoost(%d, %.2f) = %.6f, want %.6f", tt.count, tt.weight, got, tt.want)
			}
		})
	}
}

func TestComputeFinalScore(t *testing.T) {
	now := time.Date(2026, 2, 7, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name          string
		baseScore     float64
		ageDays       float64
		recencyWeight float64
		halfLifeDays  float64
		feedbackBoost float64
		want          float64
		tolerance     float64
	}{
		{
			name:          "basic: rerank=0.9 recency=0.5 weight=0.2 feedback=1.0",
			baseScore:     0.9,
			ageDays:       30,
			recencyWeight: 0.2,
			halfLifeDays:  30,
			feedbackBoost: 1.0,
			want:          0.9 * (1.0 + 0.5*0.2) * 1.0, // 0.99
			tolerance:     0.01,
		},
		{
			name:          "with feedback boost",
			baseScore:     0.9,
			ageDays:       30,
			recencyWeight: 0.2,
			halfLifeDays:  30,
			feedbackBoost: 1.1,
			want:          0.9 * (1.0 + 0.5*0.2) * 1.1, // 1.089
			tolerance:     0.01,
		},
		{
			name:          "recent file scores higher than old file",
			baseScore:     0.8,
			ageDays:       0,
			recencyWeight: 0.2,
			halfLifeDays:  30,
			feedbackBoost: 1.0,
			want:          0.8 * (1.0 + 1.0*0.2) * 1.0, // 0.96
			tolerance:     0.001,
		},
		{
			name:          "old file with same base score",
			baseScore:     0.8,
			ageDays:       90,
			recencyWeight: 0.2,
			halfLifeDays:  30,
			feedbackBoost: 1.0,
			want:          0.8 * (1.0 + 0.125*0.2) * 1.0, // ~0.82
			tolerance:     0.01,
		},
		{
			name:          "zero recency weight means no boost",
			baseScore:     0.5,
			ageDays:       0,
			recencyWeight: 0.0,
			halfLifeDays:  30,
			feedbackBoost: 1.0,
			want:          0.5,
			tolerance:     0.001,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mtime := now.Add(-time.Duration(tt.ageDays*24) * time.Hour)
			got := ComputeFinalScore(tt.baseScore, mtime, now, tt.recencyWeight, tt.halfLifeDays, tt.feedbackBoost)
			if math.Abs(got-tt.want) > tt.tolerance {
				t.Errorf("ComputeFinalScore(base=%.2f, age=%.0f, weight=%.2f, halfLife=%.0f, fb=%.2f) = %.6f, want %.6f (tolerance %.4f)",
					tt.baseScore, tt.ageDays, tt.recencyWeight, tt.halfLifeDays, tt.feedbackBoost, got, tt.want, tt.tolerance)
			}
		})
	}

	// Verify that a recent file scores higher than an old file with the same base score.
	t.Run("recent beats old", func(t *testing.T) {
		recentMtime := now
		oldMtime := now.Add(-90 * 24 * time.Hour)

		recentScore := ComputeFinalScore(0.8, recentMtime, now, 0.2, 30, 1.0)
		oldScore := ComputeFinalScore(0.8, oldMtime, now, 0.2, 30, 1.0)

		if recentScore <= oldScore {
			t.Errorf("recent score (%.6f) should be greater than old score (%.6f)", recentScore, oldScore)
		}
	})
}

func TestGlobMatchPath(t *testing.T) {
	tests := []struct {
		path    string
		pattern string
		want    bool
	}{
		{"/vault/memory/pinned/work.md", "**/pinned/*", true},
		{"/vault/memory/pinned/core.md", "**/pinned/*", true},
		{"/vault/repos/org/repo/file.go", "**/pinned/*", false},
		{"/vault/memory/index/components/topics.md", "**/index/components/*", true},
		{"/vault/memory/index/all.md", "**/index/components/*", false},
		{"/vault/docs/INFRA.md", "**/docs/*", true},
		{"/vault/repos/org/repo/main/file.go", "**/repos/**", true},
		{"/vault/memory/templates/work.md", "**/templates/*", true},
		{"pinned/work.md", "pinned/*", true},
		{"/foo/pinned/work.md", "pinned/*", false}, // no leading **, must start with "pinned/"
		{"/vault/memory/daily/2026-02-20.md", "**/daily/*", true},
		{"/vault/memory/daily/2026-02-20.md", "**/pinned/*", false},
	}

	for _, tt := range tests {
		t.Run(tt.path+"_"+tt.pattern, func(t *testing.T) {
			got := globMatchPath(tt.path, tt.pattern)
			if got != tt.want {
				t.Errorf("globMatchPath(%q, %q) = %v, want %v", tt.path, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestPathBoost(t *testing.T) {
	boosts := []config.PathBoost{
		{Pattern: "**/pinned/*", Boost: 3.0},
		{Pattern: "**/index/components/*", Boost: 2.5},
		{Pattern: "**/docs/*", Boost: 1.5},
		{Pattern: "**/repos/**", Boost: 0.8},
	}

	tests := []struct {
		path string
		want float64
	}{
		{"/vault/memory/pinned/work.md", 3.0},
		{"/vault/memory/index/components/topics.md", 2.5},
		{"/vault/docs/INFRASTRUCTURE.md", 1.5},
		{"/vault/repos/org/repo/file.go", 0.8},
		{"/vault/memory/daily/2026-02-20.md", 1.0}, // no match = 1.0
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := PathBoost(tt.path, boosts)
			if got != tt.want {
				t.Errorf("PathBoost(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}

	// nil boosts = no boost
	t.Run("nil boosts", func(t *testing.T) {
		got := PathBoost("/any/path", nil)
		if got != 1.0 {
			t.Errorf("PathBoost with nil boosts = %v, want 1.0", got)
		}
	})
}

func TestSplitCamelCase(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"swapId", []string{"swap", "Id"}},
		{"HTMLParser", []string{"HTML", "Parser"}},
		{"getHTTPResponse", []string{"get", "HTTP", "Response"}},
		{"hello", nil}, // no splits
		{"ID", nil},    // all upper, no boundary
		{"", nil},      // empty
		{"camelCaseWord", []string{"camel", "Case", "Word"}},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := splitCamelCase(tt.input)
			if len(got) != len(tt.want) {
				t.Fatalf("splitCamelCase(%q) = %v, want %v", tt.input, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("splitCamelCase(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestExpandCodeIdentifiers(t *testing.T) {
	tests := []struct {
		query string
		want  string
	}{
		{"swapId", "(swapId OR swap_id OR swap id)"},
		{"hello world", "hello world"}, // no camelCase words
		{"swapId eleven loves", "(swapId OR swap_id OR swap id) eleven loves"},
		{"HTMLParser", "(HTMLParser OR html_parser OR html parser)"},
	}

	for _, tt := range tests {
		t.Run(tt.query, func(t *testing.T) {
			got := ExpandCodeIdentifiers(tt.query)
			if got != tt.want {
				t.Errorf("ExpandCodeIdentifiers(%q) = %q, want %q", tt.query, got, tt.want)
			}
		})
	}
}
