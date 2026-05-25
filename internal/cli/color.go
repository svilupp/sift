package cli

import (
	"os"
	"strings"

	"github.com/mattn/go-isatty"
)

// ANSI style codes.
const (
	ansiReset  = "\033[0m"
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiCyan   = "\033[36m"
	ansiYellow = "\033[33m"
	ansiGreen  = "\033[32m"
	ansiRed    = "\033[31m"
)

// colorMode overrides auto-detection: -1 = auto, 0 = off, 1 = on.
var colorMode = -1

func colorsOn() bool {
	switch colorMode {
	case 0:
		return false
	case 1:
		return true
	default:
		if os.Getenv("NO_COLOR") != "" {
			return false
		}
		if os.Getenv("CLICOLOR_FORCE") != "" {
			return true
		}
		fd := os.Stdout.Fd()
		return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
	}
}

// style wraps s with ANSI codes if colors are enabled.
func style(s string, codes ...string) string {
	if !colorsOn() {
		return s
	}
	return strings.Join(codes, "") + s + ansiReset
}
