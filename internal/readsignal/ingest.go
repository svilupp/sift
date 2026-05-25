package readsignal

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"sift/internal/db"
)

var timeNow = time.Now

// LoadReadSignals loads and merges TSV read-signal exports for the given collections.
func LoadReadSignals(collections []db.Collection, configuredPath string, maxAgeDays int) ([]db.ReadCountRecord, []string, error) {
	if strings.TrimSpace(configuredPath) == "" {
		return nil, nil, nil
	}

	records := make(map[string]db.ReadCountRecord)
	var sources []string
	for _, col := range collections {
		paths, err := resolveCollectionPaths(col.Path, configuredPath)
		if err != nil {
			return nil, nil, err
		}
		for _, path := range paths {
			fileRecords, err := loadTSV(path, col.Path, maxAgeDays)
			if err != nil {
				return nil, nil, err
			}
			if len(fileRecords) == 0 {
				continue
			}
			sources = append(sources, path)
			for _, record := range fileRecords {
				existing, ok := records[record.DocPath]
				if !ok {
					records[record.DocPath] = record
					continue
				}
				existing.TotalReads += record.TotalReads
				existing.UniqueDays += record.UniqueDays
				if record.LastRead > existing.LastRead {
					existing.LastRead = record.LastRead
				}
				records[record.DocPath] = existing
			}
		}
	}

	merged := make([]db.ReadCountRecord, 0, len(records))
	for _, record := range records {
		merged = append(merged, record)
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].DocPath < merged[j].DocPath
	})
	sort.Strings(sources)

	return merged, sources, nil
}

func resolveCollectionPaths(collectionRoot string, configuredPath string) ([]string, error) {
	pattern := configuredPath
	if strings.HasPrefix(pattern, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("resolve home dir for %s: %w", pattern, err)
		}
		pattern = filepath.Join(home, strings.TrimPrefix(pattern, "~/"))
	}
	if !filepath.IsAbs(pattern) {
		pattern = filepath.Join(collectionRoot, pattern)
	}

	if hasGlob(pattern) {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return nil, fmt.Errorf("expand read-signal glob %s: %w", pattern, err)
		}
		return matches, nil
	}

	if _, err := os.Stat(pattern); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat read-signal export %s: %w", pattern, err)
	}
	return []string{pattern}, nil
}

func loadTSV(path string, collectionRoot string, maxAgeDays int) ([]db.ReadCountRecord, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open read-signal export %s: %w", path, err)
	}
	defer file.Close()

	var cutoff time.Time
	if maxAgeDays > 0 {
		cutoff = timeNow().AddDate(0, 0, -maxAgeDays)
	}

	scanner := bufio.NewScanner(file)
	lineNo := 0
	var records []db.ReadCountRecord
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if lineNo == 1 && strings.HasPrefix(strings.ToLower(line), "doc_path\t") {
			continue
		}

		fields := strings.Split(line, "\t")
		if len(fields) < 4 {
			return nil, fmt.Errorf("parse read-signal export %s:%d: expected 4 columns", path, lineNo)
		}

		totalReads, err := strconv.Atoi(fields[1])
		if err != nil {
			return nil, fmt.Errorf("parse total_reads in %s:%d: %w", path, lineNo, err)
		}
		uniqueDays, err := strconv.Atoi(fields[2])
		if err != nil {
			return nil, fmt.Errorf("parse unique_days in %s:%d: %w", path, lineNo, err)
		}

		lastRead, err := parseLastRead(fields[3])
		if err != nil {
			return nil, fmt.Errorf("parse last_read in %s:%d: %w", path, lineNo, err)
		}
		if !cutoff.IsZero() && lastRead.Before(cutoff) {
			continue
		}

		docPath, err := resolveDocPath(collectionRoot, fields[0])
		if err != nil {
			return nil, fmt.Errorf("resolve doc_path in %s:%d: %w", path, lineNo, err)
		}

		records = append(records, db.ReadCountRecord{
			DocPath:    docPath,
			TotalReads: totalReads,
			UniqueDays: uniqueDays,
			LastRead:   lastRead.Format("2006-01-02"),
		})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan read-signal export %s: %w", path, err)
	}
	return records, nil
}

func resolveDocPath(collectionRoot string, docPath string) (string, error) {
	docPath = filepath.Clean(filepath.FromSlash(strings.TrimSpace(docPath)))
	absPath := filepath.Clean(filepath.Join(collectionRoot, docPath))

	rel, err := filepath.Rel(collectionRoot, absPath)
	if err != nil {
		return "", err
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path escapes collection root: %s", docPath)
	}
	return absPath, nil
}

func parseLastRead(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	layouts := []string{
		"2006-01-02",
		time.RFC3339,
		time.RFC3339Nano,
	}
	for _, layout := range layouts {
		if ts, err := time.Parse(layout, value); err == nil {
			return ts, nil
		}
	}
	return time.Time{}, fmt.Errorf("unsupported timestamp %q", value)
}

func hasGlob(path string) bool {
	return strings.ContainsAny(path, "*?[")
}
