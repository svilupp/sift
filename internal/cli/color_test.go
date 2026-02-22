package cli

import (
	"strings"
	"testing"
)

func TestStyleColorsOff(t *testing.T) {
	colorMode = 0
	defer func() { colorMode = -1 }()

	got := style("hello", ansiBold, ansiCyan)
	if got != "hello" {
		t.Errorf("style() with colors off = %q, want %q", got, "hello")
	}
}

func TestStyleColorsOn(t *testing.T) {
	colorMode = 1
	defer func() { colorMode = -1 }()

	got := style("hello", ansiBold, ansiCyan)
	want := ansiBold + ansiCyan + "hello" + ansiReset
	if got != want {
		t.Errorf("style() = %q, want %q", got, want)
	}
}

func TestStyleMultipleCodes(t *testing.T) {
	colorMode = 1
	defer func() { colorMode = -1 }()

	got := style("warn", ansiBold, ansiRed)
	if !strings.HasPrefix(got, ansiBold+ansiRed) {
		t.Errorf("style() missing prefix codes: %q", got)
	}
	if !strings.HasSuffix(got, ansiReset) {
		t.Errorf("style() missing reset suffix: %q", got)
	}
	if !strings.Contains(got, "warn") {
		t.Errorf("style() missing content: %q", got)
	}
}

func TestStyleNoCodes(t *testing.T) {
	colorMode = 1
	defer func() { colorMode = -1 }()

	got := style("hello")
	want := "hello" + ansiReset
	if got != want {
		t.Errorf("style() no codes = %q, want %q", got, want)
	}
}

func TestColorsOnNoColor(t *testing.T) {
	colorMode = -1
	t.Setenv("NO_COLOR", "1")
	t.Setenv("CLICOLOR_FORCE", "")

	if colorsOn() {
		t.Error("colorsOn() = true with NO_COLOR set")
	}
}

func TestColorsOnCLIColorForce(t *testing.T) {
	colorMode = -1
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "1")

	if !colorsOn() {
		t.Error("colorsOn() = false with CLICOLOR_FORCE set")
	}
}

func TestColorsOnModeOverride(t *testing.T) {
	tests := []struct {
		mode int
		want bool
	}{
		{0, false},
		{1, true},
	}
	for _, tt := range tests {
		colorMode = tt.mode
		got := colorsOn()
		colorMode = -1
		if got != tt.want {
			t.Errorf("colorsOn() with mode=%d = %v, want %v", tt.mode, got, tt.want)
		}
	}
}

func TestColorsOnAutoDetectNotTTY(t *testing.T) {
	colorMode = -1
	t.Setenv("NO_COLOR", "")
	t.Setenv("CLICOLOR_FORCE", "")

	// In tests, stdout is not a terminal, so auto-detect should return false.
	if colorsOn() {
		t.Error("colorsOn() = true in non-TTY test environment")
	}
}
