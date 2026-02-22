package log

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Logger writes JSONL log files with weekly rotation.
type Logger struct {
	logDir   string
	maxWeeks int
}

// NewLogger creates a Logger that writes to logDir and retains files for maxWeeks.
func NewLogger(logDir string, maxWeeks int) *Logger {
	if maxWeeks <= 0 {
		maxWeeks = 12
	}
	return &Logger{
		logDir:   logDir,
		maxWeeks: maxWeeks,
	}
}

// Log marshals entry as JSON and appends it as a single line to the appropriate
// weekly log file for the given logType.
func (l *Logger) Log(logType string, entry any) error {
	if err := os.MkdirAll(l.logDir, 0755); err != nil {
		return fmt.Errorf("create log dir: %w", err)
	}

	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("marshal log entry: %w", err)
	}

	filename := l.filename(logType, time.Now())
	path := filepath.Join(l.logDir, filename)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	data = append(data, '\n')
	if _, writeErr := f.Write(data); writeErr != nil {
		f.Close()
		return fmt.Errorf("write log entry: %w", writeErr)
	}

	if err := f.Close(); err != nil {
		return fmt.Errorf("close log file: %w", err)
	}

	return nil
}

// Cleanup deletes log files older than maxWeeks.
func (l *Logger) Cleanup() error {
	entries, err := os.ReadDir(l.logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read log dir: %w", err)
	}

	cutoff := mondayOf(time.Now()).AddDate(0, 0, -7*l.maxWeeks)

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".jsonl") {
			continue
		}

		fileDate, err := parseDateFromFilename(name)
		if err != nil {
			continue // skip files we can't parse
		}

		if fileDate.Before(cutoff) {
			path := filepath.Join(l.logDir, name)
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("remove old log %s: %w", name, err)
			}
		}
	}

	return nil
}

// ReadRecent reads the last n entries from the most recent log files of the
// given type, returned newest-first.
func (l *Logger) ReadRecent(logType string, n int) ([]json.RawMessage, error) {
	if n <= 0 {
		n = 20
	}

	files, err := l.logFiles(logType)
	if err != nil {
		return nil, err
	}

	// files are sorted oldest-first; process newest-first.
	var entries []json.RawMessage
	for i := len(files) - 1; i >= 0 && len(entries) < n; i-- {
		lines, err := readAllLines(files[i])
		if err != nil {
			return nil, fmt.Errorf("read log file %s: %w", files[i], err)
		}

		// Lines are chronological; reverse to get newest-first.
		for j := len(lines) - 1; j >= 0 && len(entries) < n; j-- {
			entries = append(entries, json.RawMessage(lines[j]))
		}
	}

	return entries, nil
}

// filename returns the log file name for a given type and time.
// Format: {type}-{monday-date}.jsonl
func (l *Logger) filename(logType string, t time.Time) string {
	monday := mondayOf(t)
	return fmt.Sprintf("%s-%s.jsonl", logType, monday.Format("2006-01-02"))
}

// mondayOf returns the Monday of the week containing t (at midnight UTC).
func mondayOf(t time.Time) time.Time {
	t = t.UTC()
	weekday := int(t.Weekday())
	if weekday == 0 {
		weekday = 7 // Sunday = 7
	}
	monday := t.AddDate(0, 0, -(weekday - 1))
	return time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, time.UTC)
}

// parseDateFromFilename extracts the date from a filename like "searches-2026-02-03.jsonl".
func parseDateFromFilename(name string) (time.Time, error) {
	// Strip extension.
	name = strings.TrimSuffix(name, ".jsonl")
	// Find the date part (last 10 characters: YYYY-MM-DD).
	parts := strings.SplitN(name, "-", 2)
	if len(parts) < 2 {
		return time.Time{}, fmt.Errorf("no date in filename: %s", name)
	}
	// The date portion is everything after the first dash, but log types
	// can contain dashes too. The date is the last 10 chars.
	datePart := parts[1]
	if len(datePart) < 10 {
		return time.Time{}, fmt.Errorf("date too short in filename: %s", name)
	}
	datePart = datePart[len(datePart)-10:]
	return time.Parse("2006-01-02", datePart)
}

// logFiles returns sorted paths to log files for the given type (oldest first).
func (l *Logger) logFiles(logType string) ([]string, error) {
	entries, err := os.ReadDir(l.logDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read log dir: %w", err)
	}

	prefix := logType + "-"
	var paths []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".jsonl") {
			paths = append(paths, filepath.Join(l.logDir, name))
		}
	}

	sort.Strings(paths) // lexicographic = chronological for YYYY-MM-DD
	return paths, nil
}

// readAllLines reads all non-empty lines from a file.
func readAllLines(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var lines []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines, scanner.Err()
}
