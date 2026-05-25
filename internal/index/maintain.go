package index

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// ObservedFile describes one file as observed on disk during a scan. It
// supplies the freshness tuple (`Sig`) plus optional layout hints used to
// decide whether the parent folder's `purpose`/`use_when` are stale.
type ObservedFile struct {
	// RelPath is the file's path relative to the folder owning the
	// `sift.toml`. Forward slashes only; this is the key under
	// `[files."<rel>"]`.
	RelPath string

	// Sig is the freshness signature (content_hash, head/tail hashes,
	// word count). Always required.
	Sig FileSig

	// Ignore optionally marks this file as "do not recommend". Defaults
	// to false; callers may pass through `sift.toml`'s prior value if
	// they want to honor existing user edits.
	Ignore bool
}

// ObservedChildFolder describes one direct child folder observed on disk
// — used to keep `[folders."<name>"]` entries in sync without touching
// the children's own `sift.toml` files.
type ObservedChildFolder struct {
	// Name is the child folder's basename (no path separators).
	Name string

	// Ignore optionally seeds the per-folder ignore flag for new
	// children. Existing children's flags are preserved.
	Ignore bool
}

// FileChange names the per-file outcome of a maintenance plan.
type FileChange struct {
	RelPath  string
	Decision Decision
	// Prev holds the previous entry, if one existed. Empty for new files.
	Prev FileEntry
	// Next holds the entry that will be written. Empty for deletions.
	Next FileEntry
}

// MaintainPlan captures every mutation a single Plan call would apply to
// one folder's `sift.toml`. It is read-only by construction; pass it to
// Apply to actually persist.
type MaintainPlan struct {
	// FolderPath is the absolute folder whose `sift.toml` this plan
	// targets. Apply writes `filepath.Join(FolderPath, FilenameSiftToml)`.
	FolderPath string

	// Existed is true if a `sift.toml` was present before planning.
	Existed bool

	// Created is true when there was no prior `sift.toml` and the new one
	// has any content worth writing.
	Created bool

	// Changed is true when Apply will write to disk (includes Created
	// and any field-level mutation). When false, Apply is a no-op.
	Changed bool

	// PurposeStale is set when the folder-level signature shifted enough
	// that an LLM should re-propose `purpose`. Phase-3 only flags it;
	// Phase-5 will consume the flag.
	PurposeStale bool

	// FileChanges lists per-file outcomes in stable RelPath order.
	FileChanges []FileChange

	// Prev is the FolderIndex that was on disk (or nil if none).
	Prev *FolderIndex

	// Next is the FolderIndex that Apply will write. Always non-nil
	// when Changed is true.
	Next *FolderIndex
}

// PlanInput bundles everything Plan needs about one folder. All slices
// must be the caller's responsibility; Plan does not mutate them.
type PlanInput struct {
	// FolderPath is the absolute path of the folder being maintained.
	FolderPath string

	// Prev is the previously loaded FolderIndex (nil when no `sift.toml`
	// exists). Plan never writes back to it — it copies the fields it
	// preserves.
	Prev *FolderIndex

	// Files is the set of observed indexable files in this folder.
	// RelPaths must be unique. Order is irrelevant.
	Files []ObservedFile

	// Folders is the set of observed direct child subfolders. Names must
	// be unique. Order is irrelevant.
	Folders []ObservedChildFolder
}

// Plan diffs a folder's previous `sift.toml` (if any) against the
// freshly observed disk state and produces a MaintainPlan describing the
// mechanical updates needed. It does NOT touch disk and does NOT call
// any LLM. Sticky fields — file `summary`, folder `purpose`, `use_when`,
// per-file `ignore`, per-folder `ignore` — are preserved unless the
// freshness rules say to drop them.
func Plan(in PlanInput) (*MaintainPlan, error) {
	if in.FolderPath == "" {
		return nil, fmt.Errorf("plan: empty folder path")
	}

	plan := &MaintainPlan{
		FolderPath: in.FolderPath,
		Prev:       in.Prev,
		Existed:    in.Prev != nil,
	}

	// Build the next FolderIndex from a clone of Prev so sticky fields
	// (Purpose, UseWhen, Ignore) carry across by default.
	next := cloneOrEmpty(in.Prev)
	next.SchemaVersion = SchemaVersion

	// Collect prev files for matching; consume entries as they get used
	// so the leftovers represent deletions (eligible for rename detect).
	prevFiles := map[string]FileEntry{}
	if in.Prev != nil {
		for k, v := range in.Prev.Files {
			prevFiles[k] = v
		}
	}

	// Stable iteration on observed files for deterministic plans.
	observed := append([]ObservedFile(nil), in.Files...)
	sort.Slice(observed, func(i, j int) bool {
		return observed[i].RelPath < observed[j].RelPath
	})

	nextFiles := map[string]FileEntry{}
	totalWords := 0
	var changes []FileChange

	for _, of := range observed {
		if of.RelPath == "" {
			continue
		}
		prev, hadPrev := prevFiles[of.RelPath]
		var prevPtr *FileEntry
		if hadPrev {
			p := prev
			prevPtr = &p
			delete(prevFiles, of.RelPath)
		}
		curSig := of.Sig
		dec := Decide(prevPtr, &curSig)

		entry := FileEntry{
			Ignore: of.Ignore || prev.Ignore,
		}
		switch dec {
		case DecisionNoop:
			// Preserve everything from prev, including summary and
			// any prior ignore flag. Words/hashes already match.
			entry = prev
			// Refresh flags only flip on if the observation says so.
			if of.Ignore {
				entry.Ignore = true
			}
		case DecisionUpdateMechanical:
			// Update hashes + words; keep summary.
			entry = curSig.AsEntry(prev)
			if of.Ignore {
				entry.Ignore = true
			}
		case DecisionRegenerate:
			// Update hashes + words; drop summary so Phase-5 regenerates.
			entry = curSig.AsEntry(FileEntry{Ignore: prev.Ignore})
			if of.Ignore {
				entry.Ignore = true
			}
		case DecisionNew:
			entry = curSig.AsEntry(FileEntry{Ignore: of.Ignore})
		}

		nextFiles[of.RelPath] = entry
		totalWords += entry.Words
		changes = append(changes, FileChange{
			RelPath:  of.RelPath,
			Decision: dec,
			Prev:     prev,
			Next:     entry,
		})
	}

	// Whatever's still in prevFiles is a deletion.
	deletedNames := make([]string, 0, len(prevFiles))
	for name := range prevFiles {
		deletedNames = append(deletedNames, name)
	}
	sort.Strings(deletedNames)
	for _, name := range deletedNames {
		changes = append(changes, FileChange{
			RelPath:  name,
			Decision: DecisionDeleted,
			Prev:     prevFiles[name],
		})
	}

	next.Files = nextFiles
	next.Folders = mergeFolderEntries(in.Prev, in.Folders)
	next.Refresh.FileCount = len(nextFiles)
	next.Refresh.WordCount = totalWords

	plan.FileChanges = changes
	plan.Next = next
	plan.PurposeStale = computePurposeStale(in.Prev, changes, totalWords)

	plan.Created = !plan.Existed && !next.IsZero()
	plan.Changed = plan.diffNeedsWrite()

	return plan, nil
}

// Apply persists the plan's Next FolderIndex when plan.Changed is true.
// When Changed is false, Apply is a no-op and returns nil — re-running
// Plan/Apply on an unchanged tree produces zero writes.
func Apply(plan *MaintainPlan) error {
	if plan == nil {
		return fmt.Errorf("apply: nil plan")
	}
	if !plan.Changed {
		return nil
	}
	if plan.Next == nil {
		return fmt.Errorf("apply: nil next index for %s", plan.FolderPath)
	}
	path := filepath.Join(plan.FolderPath, FilenameSiftToml)
	return Save(path, plan.Next)
}

// PlanAndApply is a convenience wrapper for callers who want both.
func PlanAndApply(in PlanInput) (*MaintainPlan, error) {
	plan, err := Plan(in)
	if err != nil {
		return nil, err
	}
	if err := Apply(plan); err != nil {
		return plan, err
	}
	return plan, nil
}

// LoadOrZero reads `sift.toml` at folder if present and returns the
// parsed FolderIndex. A missing file returns (nil, false, nil) so the
// caller can drive PlanInput.Prev. Other errors are returned wrapped.
func LoadOrZero(folder string) (*FolderIndex, bool, error) {
	path := filepath.Join(folder, FilenameSiftToml)
	idx, err := Load(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return idx, true, nil
}

// diffNeedsWrite returns true when persisting Next would change the
// on-disk bytes. We compare via canonical Marshal output: that's the
// same form Save writes, so equal bytes mean equal file.
func (p *MaintainPlan) diffNeedsWrite() bool {
	if !p.Existed {
		// New file: write only when Next has any content (or any file
		// entries) — empty placeholders are not persisted.
		return p.Created
	}
	prevBytes, errPrev := Marshal(p.Prev)
	nextBytes, errNext := Marshal(p.Next)
	if errPrev != nil || errNext != nil {
		// On marshal error, fall back to writing — safer than silently
		// dropping an update. The error will surface from Apply's Save.
		return true
	}
	if len(prevBytes) != len(nextBytes) {
		return true
	}
	for i := range prevBytes {
		if prevBytes[i] != nextBytes[i] {
			return true
		}
	}
	return false
}

// cloneOrEmpty returns a deep-enough copy of prev so callers can mutate
// the result without touching the input. When prev is nil it returns a
// fresh zero FolderIndex.
func cloneOrEmpty(prev *FolderIndex) *FolderIndex {
	if prev == nil {
		return &FolderIndex{
			SchemaVersion: SchemaVersion,
			Files:         map[string]FileEntry{},
			Folders:       map[string]ChildFolder{},
		}
	}
	out := &FolderIndex{
		SchemaVersion: prev.SchemaVersion,
		Ignore:        prev.Ignore,
		Purpose:       prev.Purpose,
		Refresh:       prev.Refresh,
		Files:         map[string]FileEntry{},
		Folders:       map[string]ChildFolder{},
	}
	if len(prev.UseWhen) > 0 {
		out.UseWhen = append([]string(nil), prev.UseWhen...)
	}
	for k, v := range prev.Files {
		out.Files[k] = v
	}
	for k, v := range prev.Folders {
		out.Folders[k] = v
	}
	return out
}

// mergeFolderEntries reconciles previously-known [folders.X] entries
// with the current observation. Existing children keep their ignore
// flag; new children seed from the observation; vanished children are
// dropped.
func mergeFolderEntries(prev *FolderIndex, observed []ObservedChildFolder) map[string]ChildFolder {
	out := map[string]ChildFolder{}
	prevFolders := map[string]ChildFolder{}
	if prev != nil {
		for k, v := range prev.Folders {
			prevFolders[k] = v
		}
	}
	for _, of := range observed {
		if of.Name == "" {
			continue
		}
		if existing, ok := prevFolders[of.Name]; ok {
			out[of.Name] = existing
		} else {
			out[of.Name] = ChildFolder{Ignore: of.Ignore}
		}
	}
	return out
}

// computePurposeStale flags whether the folder's `purpose` should be
// regenerated. Heuristics drawn from PROPOSAL2.md / LT-C:
//
//   - new folder with content → stale (so Phase-5 generates one).
//   - ≥20% of files churned (new + regenerate + deleted) → stale.
//   - any single new file ≥20% of the folder's word count → stale.
//
// Phase-3 only flags; nothing here calls an LLM.
func computePurposeStale(prev *FolderIndex, changes []FileChange, totalWords int) bool {
	if prev == nil {
		// Brand-new folder. Purpose is empty by definition; do not flag
		// stale on its own — Phase-5 fills it in via the "missing"
		// generation mode. Flag only when there is any content at all.
		hasContent := false
		for _, ch := range changes {
			if ch.Decision != DecisionDeleted {
				hasContent = true
				break
			}
		}
		return hasContent
	}

	churn := 0
	keepers := 0
	for _, ch := range changes {
		switch ch.Decision {
		case DecisionNew, DecisionRegenerate, DecisionDeleted:
			churn++
		case DecisionNoop, DecisionUpdateMechanical:
			keepers++
		}
		if ch.Decision == DecisionNew && totalWords > 0 {
			share := float64(ch.Next.Words) / float64(totalWords)
			if share >= 0.20 {
				return true
			}
		}
	}
	denom := churn + keepers
	if denom == 0 {
		return false
	}
	return float64(churn)/float64(denom) >= 0.20
}
