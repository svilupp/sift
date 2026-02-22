package cli

import "testing"

func TestIsMeaningfulLine(t *testing.T) {
	tests := []struct {
		line string
		want bool
	}{
		{"", false},                         // empty
		{"   ", false},                      // whitespace only
		{"---", false},                      // markdown separator
		{"===", false},                      // markdown separator
		{"|---|---|---|", false},             // table separator
		{"***", false},                      // horizontal rule
		{"| | | |", false},                  // empty table row (only pipes and spaces)
		{"abc", false},                      // too short (3 chars)
		{"abcd", true},                      // exactly 4
		{"Hello world", true},               // normal text
		{"## GCP Projects", true},           // markdown heading
		{"| Store | Swap ID | URL |", true}, // table header with content
		{"`ePxLjCpjSHhzCPYjapUs`", true},   // code with alphanumeric
		{"- item", true},                    // list item
		{"1. First", true},                  // numbered list
	}

	for _, tt := range tests {
		t.Run(tt.line, func(t *testing.T) {
			got := isMeaningfulLine(tt.line)
			if got != tt.want {
				t.Errorf("isMeaningfulLine(%q) = %v, want %v", tt.line, got, tt.want)
			}
		})
	}
}
