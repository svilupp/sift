package index

import (
	"errors"
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// rawIndex mirrors the on-disk TOML structure with TOML tags so we can let
// the library handle field mapping. We translate from this raw shape into
// FolderIndex (and back) so the in-memory type stays clean of TOML tags
// and the writer can emit a fully canonical form.
type rawIndex struct {
	SchemaVersion *int                 `toml:"schema_version"`
	Ignore        *bool                `toml:"ignore"`
	Purpose       *string              `toml:"purpose"`
	UseWhen       []string             `toml:"use_when"`
	Refresh       *rawRefresh          `toml:"refresh"`
	Files         map[string]rawFile   `toml:"files"`
	Folders       map[string]rawFolder `toml:"folders"`
}

type rawRefresh struct {
	FileCount *int `toml:"file_count"`
	WordCount *int `toml:"word_count"`
}

type rawFile struct {
	Ignore      *bool   `toml:"ignore"`
	ContentHash *string `toml:"content_hash"`
	HeadHash    *string `toml:"head_hash"`
	TailHash    *string `toml:"tail_hash"`
	Words       *int    `toml:"words"`
	Summary     *string `toml:"summary"`
}

type rawFolder struct {
	Ignore *bool `toml:"ignore"`
}

// Load reads `sift.toml` at path and returns the parsed FolderIndex. A
// missing file returns os.ErrNotExist (callers can treat as "no index
// yet"). Other errors are wrapped with the path for context.
func Load(path string) (*FolderIndex, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("sift.toml at %s: %w", path, err)
	}
	idx, err := LoadBytes(data)
	if err != nil {
		return nil, fmt.Errorf("sift.toml at %s: %w", path, err)
	}
	return idx, nil
}

// LoadBytes parses TOML bytes into a FolderIndex, applying defaults for
// missing fields and rejecting unknown schema versions.
func LoadBytes(data []byte) (*FolderIndex, error) {
	var raw rawIndex
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse toml: %w", err)
	}

	idx := &FolderIndex{
		SchemaVersion: SchemaVersion,
		Files:         map[string]FileEntry{},
		Folders:       map[string]ChildFolder{},
	}

	if raw.SchemaVersion != nil {
		idx.SchemaVersion = *raw.SchemaVersion
	}
	if idx.SchemaVersion <= 0 {
		idx.SchemaVersion = SchemaVersion
	}
	if idx.SchemaVersion > SchemaVersion {
		return nil, fmt.Errorf("unsupported schema_version %d (max %d)", idx.SchemaVersion, SchemaVersion)
	}

	if raw.Ignore != nil {
		idx.Ignore = *raw.Ignore
	}
	if raw.Purpose != nil {
		idx.Purpose = *raw.Purpose
	}
	if len(raw.UseWhen) > 0 {
		idx.UseWhen = append([]string(nil), raw.UseWhen...)
	}
	if raw.Refresh != nil {
		if raw.Refresh.FileCount != nil {
			idx.Refresh.FileCount = *raw.Refresh.FileCount
		}
		if raw.Refresh.WordCount != nil {
			idx.Refresh.WordCount = *raw.Refresh.WordCount
		}
	}

	for name, rf := range raw.Files {
		entry := FileEntry{}
		if rf.Ignore != nil {
			entry.Ignore = *rf.Ignore
		}
		if rf.ContentHash != nil {
			entry.ContentHash = *rf.ContentHash
		}
		if rf.HeadHash != nil {
			entry.HeadHash = *rf.HeadHash
		}
		if rf.TailHash != nil {
			entry.TailHash = *rf.TailHash
		}
		if rf.Words != nil {
			entry.Words = *rf.Words
		}
		if rf.Summary != nil {
			entry.Summary = *rf.Summary
		}
		idx.Files[name] = entry
	}

	for name, rf := range raw.Folders {
		entry := ChildFolder{}
		if rf.Ignore != nil {
			entry.Ignore = *rf.Ignore
		}
		idx.Folders[name] = entry
	}

	return idx, nil
}
