package main

import (
	"os"

	"sift/internal/cli"
)

// Build-time variables set via ldflags.
var (
	version   = "0.1.0"
	commit    = "unknown"
	buildTime = "unknown"
)

func versionString() string {
	return version + " (commit: " + commit + ", built: " + buildTime + ")"
}

func main() {
	if err := cli.NewRootCmd(versionString()).Execute(); err != nil {
		os.Exit(1)
	}
}
