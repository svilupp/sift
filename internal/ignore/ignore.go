package ignore

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

// LoadPatterns reads a .siftignore file from collectionPath/.siftignore.
// Returns nil patterns and nil error if the file doesn't exist.
// Each line is a glob pattern. '#' comments and blank lines are ignored.
func LoadPatterns(collectionPath string) ([]string, error) {
	path := filepath.Join(collectionPath, ".siftignore")
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("open .siftignore: %w", err)
	}
	defer f.Close()

	var patterns []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read .siftignore: %w", err)
	}
	return patterns, nil
}

// ShouldIgnore checks if a path matches any of the ignore patterns.
// The path can be absolute or relative; it is converted to a path relative
// to collectionRoot before matching against patterns using doublestar.Match.
//
// For directories, also checks if the pattern matches with a trailing "/"
// or as a prefix.
func ShouldIgnore(path string, collectionRoot string, patterns []string) bool {
	if len(patterns) == 0 {
		return false
	}

	rel, err := filepath.Rel(collectionRoot, path)
	if err != nil {
		return false
	}
	// Normalise to forward slashes for consistent matching.
	rel = filepath.ToSlash(rel)

	for _, pattern := range patterns {
		pattern = filepath.ToSlash(pattern)

		// Direct match against the relative path.
		if matched, _ := doublestar.Match(pattern, rel); matched {
			return true
		}

		// Like .gitignore: if the pattern has no slash, it can match
		// in any subdirectory. Prepend **/ so "*.json" matches "a/b/c.json".
		if !strings.Contains(pattern, "/") {
			if matched, _ := doublestar.Match("**/"+pattern, rel); matched {
				return true
			}
		}

		// Check if this is a directory by seeing if path + "/" matches.
		if matched, _ := doublestar.Match(pattern+"/", rel+"/"); matched {
			return true
		}

		// Check if pattern matches as a prefix (e.g., pattern "vendor"
		// should match "vendor/foo/bar.go").
		if matched, _ := doublestar.Match(pattern+"/**", rel); matched {
			return true
		}

		// Combine: pattern without slash can also be a directory at any depth.
		if !strings.Contains(pattern, "/") {
			if matched, _ := doublestar.Match("**/"+pattern+"/**", rel); matched {
				return true
			}
		}
	}
	return false
}
