package eval

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// Case represents a single eval case.
type Case struct {
	ID     string       `json:"id"`
	Corpus string       `json:"corpus"`
	Query  string       `json:"query"`
	Gold   []GoldTarget `json:"gold"`
	Family string       `json:"family"`
}

// GoldTarget is an expected result.
type GoldTarget struct {
	Path      string `json:"path"`       // relative filename
	SectionID string `json:"section_id"` // slug of expected section heading
	StartLine int    `json:"start_line"` // 1-based line of the heading
}

// LoadCases reads a JSONL file and returns cases.
func LoadCases(path string) ([]Case, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open cases file: %w", err)
	}
	defer f.Close()

	var cases []Case
	sc := bufio.NewScanner(f)
	lineNum := 0
	for sc.Scan() {
		lineNum++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var c Case
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("parse case at line %d: %w", lineNum, err)
		}
		cases = append(cases, c)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read cases file: %w", err)
	}
	return cases, nil
}
