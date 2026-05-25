// Package index defines the per-folder `sift.toml` metadata format and the
// helpers SIFT uses to read, write, and compare those files.
//
// `sift.toml` is a committed, human-editable, per-folder index of the files
// inside that folder. It stores deterministic content hashes, word counts,
// optional file summaries, and folder-level purpose/use_when text. SIFT
// maintains the mechanical fields (hashes, counts) on every refresh and may
// regenerate summaries via an LLM when the freshness tuple says they are
// stale.
//
// This package is the single source of truth for the file format. mem and
// other consumers must call `sift index` instead of parsing `sift.toml`
// directly.
package index

// SchemaVersion is the current `sift.toml` schema version. Files with a
// higher version are rejected by the parser; lower or missing versions
// default to this value.
const SchemaVersion = 1

// FilenameSiftToml is the canonical filename for a folder index.
const FilenameSiftToml = "sift.toml"

// FolderIndex is the in-memory representation of a single `sift.toml` file.
// Field order in this struct is also the canonical emit order for the
// writer; do not reorder casually.
type FolderIndex struct {
	// SchemaVersion is the on-disk schema version (currently always 1).
	SchemaVersion int

	// Ignore signals "do not recommend this folder for metadata-aware
	// routing." It does not exclude content from search; that is
	// `.siftignore`'s job.
	Ignore bool

	// Purpose is a sticky, LLM-or-human written paragraph describing what
	// the folder is for. Multiline.
	Purpose string

	// UseWhen lists short cues for when an agent should look here. Hand
	// edited; never auto-generated.
	UseWhen []string

	// Refresh holds machine-managed counts from the last refresh.
	Refresh RefreshStats

	// Files maps relative path (from this folder) to the per-file entry.
	// The writer emits keys in alphabetical order.
	Files map[string]FileEntry

	// Folders maps direct child folder name to its child-folder entry.
	// The writer emits keys in alphabetical order.
	Folders map[string]ChildFolder
}

// RefreshStats holds folder-wide counts from the last refresh pass.
type RefreshStats struct {
	// FileCount is the number of indexed files in the folder (after
	// `.siftignore` and `ignore = true` filters).
	FileCount int

	// WordCount is the sum of `words` across indexed files.
	WordCount int
}

// FileEntry is the per-file record stored under `[files."<rel>"]`.
type FileEntry struct {
	// Ignore marks this file as "do not recommend." Content may still be
	// indexed; this is metadata-only.
	Ignore bool

	// ContentHash is xxhash64 of the full file content, hex-encoded.
	ContentHash string

	// HeadHash is xxhash64 of the joined first 500 words.
	HeadHash string

	// TailHash is xxhash64 of the joined last 500 words.
	TailHash string

	// Words is the file's word count (whitespace-split).
	Words int

	// Summary is a short LLM-or-human written description (≤2 sentences,
	// ≤200 chars by convention). Empty until generation runs.
	Summary string
}

// ChildFolder is the entry under `[folders."<name>"]`. Currently only
// carries the ignore flag; richer metadata lives in the child's own
// `sift.toml`.
type ChildFolder struct {
	// Ignore signals "do not descend or recommend this child folder."
	Ignore bool
}

// IsZero reports whether the folder index has only default values. Useful
// for deciding whether to emit a file at all.
func (f *FolderIndex) IsZero() bool {
	if f == nil {
		return true
	}
	return f.SchemaVersion == 0 &&
		!f.Ignore &&
		f.Purpose == "" &&
		len(f.UseWhen) == 0 &&
		f.Refresh == (RefreshStats{}) &&
		len(f.Files) == 0 &&
		len(f.Folders) == 0
}
