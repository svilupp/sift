package cli

import (
	"os"
	"testing"
)

// TestMain disables the daemon auto-spawn path by default for all tests
// in this package. Tests that intentionally exercise the spawn path
// (search_test.go's orchestrator tests, daemon_test.go's lifecycle
// tests) override this via t.Setenv("SIFT_NO_DAEMON", "") or
// t.Setenv("SIFT_DAEMON_BINARY", ...).
//
// Without this guard, any test that runs `sift search ...` would call
// SpawnDetached via os.Executable(), which resolves to the test binary
// — leaking a long-lived child per test invocation.
//
// The production check in runSearchOrchestrated uses
// `os.Getenv("SIFT_NO_DAEMON") != ""`, so an empty string is treated as
// "daemon enabled". Tests that opt back in via t.Setenv("SIFT_NO_DAEMON", "")
// therefore get the daemon path. We capture the original value (if any)
// and restore it after the run so we don't pollute the process env for
// other test packages running in the same `go test` invocation.
func TestMain(m *testing.M) {
	orig, hadOrig := os.LookupEnv("SIFT_NO_DAEMON")
	if err := os.Setenv("SIFT_NO_DAEMON", "1"); err != nil {
		panic(err)
	}
	code := m.Run()
	if hadOrig {
		_ = os.Setenv("SIFT_NO_DAEMON", orig)
	} else {
		_ = os.Unsetenv("SIFT_NO_DAEMON")
	}
	os.Exit(code)
}
