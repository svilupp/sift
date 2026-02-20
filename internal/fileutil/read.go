package fileutil

import (
	"bufio"
	"os"
	"strings"
)

// ReadLines reads lines [startLine, endLine] from path.
// If maxChars > 0: trims lines, joins with spaces, truncates at maxChars.
// If maxChars == 0: preserves newlines and original formatting.
func ReadLines(path string, startLine, endLine, maxChars int) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var b strings.Builder
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		if lineNum < startLine {
			continue
		}
		if lineNum > endLine {
			break
		}
		if b.Len() > 0 {
			if maxChars == 0 {
				b.WriteByte('\n')
			} else {
				b.WriteByte(' ')
			}
		}
		if maxChars == 0 {
			b.WriteString(scanner.Text())
		} else {
			b.WriteString(strings.TrimSpace(scanner.Text()))
		}
		if maxChars > 0 && b.Len() >= maxChars {
			break
		}
	}
	return b.String()
}
