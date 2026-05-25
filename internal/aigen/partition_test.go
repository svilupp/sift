package aigen

import (
	"fmt"
	"testing"
)

func TestAuthorityScoreBonuses(t *testing.T) {
	cases := []struct {
		name string
		f    FileBatch
		min  float64
	}{
		{
			name: "title bonus",
			f:    FileBatch{Path: "x.md", Bytes: 1000, FrontmatterTitle: "T"},
			min:  4.0,
		},
		{
			name: "readme bonus",
			f:    FileBatch{Path: "README.md", Bytes: 1000},
			min:  3.5,
		},
		{
			name: "type=spec bonus",
			f:    FileBatch{Path: "spec.md", Bytes: 1000, FrontmatterType: "spec"},
			min:  3.5,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := AuthorityScore(tc.f)
			if s < tc.min {
				t.Errorf("score %.3f < min %.3f", s, tc.min)
			}
		})
	}
}

func TestAuthorityScorePenalties(t *testing.T) {
	base := AuthorityScore(FileBatch{Path: "a.md", Bytes: 1000})
	test := AuthorityScore(FileBatch{Path: "a_test.go", Bytes: 1000})
	dot := AuthorityScore(FileBatch{Path: ".eslintrc", Bytes: 1000})
	logs := AuthorityScore(FileBatch{Path: "x/logs/y.md", Bytes: 1000})
	gen := AuthorityScore(FileBatch{Path: "x.generated.go", Bytes: 1000})
	if test >= base {
		t.Errorf("test file should score lower; base=%.3f test=%.3f", base, test)
	}
	if dot >= base {
		t.Errorf("dotfile should score lower; base=%.3f dot=%.3f", base, dot)
	}
	if logs >= base {
		t.Errorf("logs path should score lower; base=%.3f logs=%.3f", base, logs)
	}
	if gen >= base {
		t.Errorf("generated should score lower; base=%.3f gen=%.3f", base, gen)
	}
}

func TestNeedsPartition(t *testing.T) {
	small := []FileBatch{}
	for i := 0; i < 15; i++ {
		small = append(small, FileBatch{Path: "x.md", Bytes: 1024})
	}
	if NeedsPartition(small) {
		t.Errorf("15 small files should fit single batch")
	}

	many := []FileBatch{}
	for i := 0; i < 30; i++ {
		many = append(many, FileBatch{Path: "x.md", Bytes: 1024})
	}
	if !NeedsPartition(many) {
		t.Errorf("30 files should require partition")
	}

	large := []FileBatch{{Path: "big.md", Bytes: 250 * 1024}}
	if !NeedsPartition(large) {
		t.Errorf("250KB single file should require partition")
	}
}

func TestPartitionThirty(t *testing.T) {
	files := []FileBatch{}
	for i := 0; i < 30; i++ {
		files = append(files, FileBatch{Path: fmt.Sprintf("f%02d.md", i), Bytes: 1024})
	}
	batches := Partition(files, 0, "")
	if len(batches) != 3 {
		t.Fatalf("expected 3 sub-batches, got %d", len(batches))
	}
	for i, b := range batches {
		if b.Index != i {
			t.Errorf("batch %d: Index=%d", i, b.Index)
		}
		if i < 2 && len(b.Files) != 10 {
			t.Errorf("batch %d size=%d, want 10", i, len(b.Files))
		}
	}
}

func TestPartitionAuthorityOrdered(t *testing.T) {
	files := []FileBatch{
		{Path: "z_test.go", Bytes: 1000},
		{Path: "README.md", Bytes: 1000},
		{Path: "middle.md", Bytes: 1000},
	}
	batches := Partition(files, 10, "")
	got := []string{}
	for _, f := range batches[0].Files {
		got = append(got, f.Path)
	}
	if got[0] != "README.md" {
		t.Errorf("expected README first; got %v", got)
	}
	if got[len(got)-1] != "z_test.go" {
		t.Errorf("expected test last; got %v", got)
	}
}

func TestPartitionTiebreakAlphabetical(t *testing.T) {
	files := []FileBatch{
		{Path: "b.md", Bytes: 1000},
		{Path: "a.md", Bytes: 1000},
		{Path: "c.md", Bytes: 1000},
	}
	batches := Partition(files, 10, "")
	want := []string{"a.md", "b.md", "c.md"}
	for i, f := range batches[0].Files {
		if f.Path != want[i] {
			t.Errorf("idx %d: got %q want %q", i, f.Path, want[i])
		}
	}
}

func TestExceedsHardCeiling(t *testing.T) {
	files := []FileBatch{}
	for i := 0; i < 26; i++ {
		files = append(files, FileBatch{Path: "x.md", Bytes: 100})
	}
	if !ExceedsHardCeiling(files) {
		t.Errorf("26 files should exceed ceiling")
	}
}
