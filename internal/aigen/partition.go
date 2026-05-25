package aigen

import (
	"math"
	"path/filepath"
	"sort"
	"strings"
)

// Partition policy constants from PROPOSAL2 §"Batching".
const (
	// FilesSingleBatchMax is the soft cap; below this AND below
	// BytesSingleBatchMax we send a single LLM call.
	FilesSingleBatchMax = 20
	// BytesSingleBatchMax is the soft byte budget per call (200 KB).
	BytesSingleBatchMax = 200 * 1024

	// FilesHardCeiling forces partitioning regardless of byte size.
	FilesHardCeiling = 25
	// BytesHardCeiling forces partitioning regardless of file count.
	BytesHardCeiling = 250 * 1024

	// SubBatchSizeDefault is N=10 per LT-C winner.
	SubBatchSizeDefault = 10
)

// SubBatch is one piece of an authority-ordered partition.
type SubBatch struct {
	Index int
	Files []FileBatch
}

// AuthorityScore computes the per-file authority score from
// PROPOSAL2.md / LT-C. Higher scores anchor the prompt; tiebreak is
// alphabetical (handled in Partition).
func AuthorityScore(f FileBatch) float64 {
	score := math.Log10(float64(f.Bytes) + 1)

	// Frontmatter signals (positive).
	if strings.TrimSpace(f.FrontmatterTitle) != "" {
		score += 1.0
	}
	if len(f.FrontmatterTags) > 0 {
		score += 0.5
	}
	switch strings.ToLower(strings.TrimSpace(f.FrontmatterType)) {
	case "proposal", "design", "spec", "doc":
		score += 0.5
	}

	base := strings.ToLower(filepath.Base(f.Path))
	switch base {
	case "readme.md", "index.md", "proposal.md", "spec.md", "design.md",
		"architecture.md":
		score += 0.5
	}

	// Negative signals.
	if isTestFile(base) {
		score -= 1.5
	}
	if pathHasSegment(f.Path, "logs") || pathHasSegment(f.Path, "scratch") ||
		pathHasSegment(f.Path, "tmp") || pathHasSegment(f.Path, ".cache") {
		score -= 1.5
	}
	if strings.Contains(base, ".generated.") || strings.Contains(base, ".gen.") ||
		strings.Contains(base, ".auto.") {
		score -= 0.5
	}
	if strings.HasPrefix(base, ".") {
		score -= 0.5
	}
	return score
}

func isTestFile(base string) bool {
	if strings.HasSuffix(base, "_test.go") {
		return true
	}
	if strings.HasSuffix(base, ".test.go") || strings.HasSuffix(base, ".test.ts") ||
		strings.HasSuffix(base, ".test.js") || strings.HasSuffix(base, ".test.py") {
		return true
	}
	if strings.HasPrefix(base, "test_") {
		return true
	}
	if strings.HasSuffix(base, ".spec.ts") || strings.HasSuffix(base, ".spec.js") {
		return true
	}
	return false
}

func pathHasSegment(p, seg string) bool {
	return strings.Contains("/"+strings.ReplaceAll(p, "\\", "/")+"/", "/"+seg+"/")
}

// NeedsPartition reports whether the folder must be partitioned.
//
// Triggers per PROPOSAL2:
//   - file_count > 20 OR prompt_bytes > 200 KB → partition
//   - hard ceiling: file_count > 25 OR bytes > 250 KB
func NeedsPartition(files []FileBatch) bool {
	if len(files) > FilesSingleBatchMax {
		return true
	}
	if totalBytes(files) > BytesSingleBatchMax {
		return true
	}
	return false
}

// ExceedsHardCeiling reports whether the folder hits the hard ceiling
// (always must partition; refuse single-call path even if user opts in).
func ExceedsHardCeiling(files []FileBatch) bool {
	if len(files) > FilesHardCeiling {
		return true
	}
	if totalBytes(files) > BytesHardCeiling {
		return true
	}
	return false
}

func totalBytes(files []FileBatch) int64 {
	var n int64
	for _, f := range files {
		// Approximate "prompt bytes" with file bytes; the prompt format
		// adds modest overhead but the constants already include slack.
		n += f.Bytes
	}
	return n
}

// Partition orders files by AuthorityScore desc (alphabetical
// tiebreak) and slices into N=batchSize sub-batches. When batchSize <= 0
// SubBatchSizeDefault is used. order="mtime" is currently a synonym
// for the default; mtime-aware ordering is a TODO once mtime is
// threaded through (caller can pre-sort and pass order="presorted").
func Partition(files []FileBatch, batchSize int, order string) []SubBatch {
	if batchSize <= 0 {
		batchSize = SubBatchSizeDefault
	}
	out := append([]FileBatch(nil), files...)
	if order != "presorted" {
		sort.SliceStable(out, func(i, j int) bool {
			si := AuthorityScore(out[i])
			sj := AuthorityScore(out[j])
			if si != sj {
				return si > sj
			}
			return out[i].Path < out[j].Path
		})
	}

	var batches []SubBatch
	for i := 0; i < len(out); i += batchSize {
		end := i + batchSize
		if end > len(out) {
			end = len(out)
		}
		batches = append(batches, SubBatch{
			Index: len(batches),
			Files: append([]FileBatch(nil), out[i:end]...),
		})
	}
	return batches
}
