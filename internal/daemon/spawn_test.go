package daemon

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// fakeDaemonScript writes a small shell script at scriptPath that:
//   - writes "spawned" plus the value of $1 to stdout (which goes to the
//     daemon log when invoked via SpawnDetached);
//   - touches the flag file so tests can prove the script ran;
//   - sleeps for sleepSecs to verify the parent does NOT block on the
//     child.
//
// Returns the absolute path to the script (it is created with mode 0o755).
func fakeDaemonScript(t *testing.T, scriptPath, flagPath string, sleepSecs int) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"echo \"spawned arg=$1\"\n" +
		"touch \"" + flagPath + "\"\n"
	if sleepSecs > 0 {
		script += "sleep " + itoa(sleepSecs) + "\n"
	}
	if err := os.WriteFile(scriptPath, []byte(script), 0o755); err != nil {
		t.Fatalf("write script: %v", err)
	}
}

// itoa avoids pulling in strconv at top level for one tiny use.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// withSpawnEnv redirects SIFT_DIR + the binary override to a sandboxed
// tempdir for the duration of t. Returns (siftDir, scriptPath).
func withSpawnEnv(t *testing.T) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("daemon spawn requires POSIX setsid + shell scripts")
	}

	siftDir := t.TempDir()
	t.Setenv("SIFT_DIR", siftDir)
	// Make sure no inherited override from the developer's shell leaks
	// in and points at a real socket.
	t.Setenv("SIFT_DAEMON_SOCKET", filepath.Join(siftDir, "sift.sock"))

	scriptPath := filepath.Join(siftDir, "fake-daemon.sh")
	t.Setenv("SIFT_DAEMON_BINARY", scriptPath)
	return siftDir, scriptPath
}

// waitForFile polls path with a short interval up to timeout.
func waitForFile(t *testing.T, path string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	_, err := os.Stat(path)
	return err == nil
}

func TestSpawnDetached_HonorsEnvOverride(t *testing.T) {
	siftDir, scriptPath := withSpawnEnv(t)
	flagPath := filepath.Join(siftDir, "spawn.flag")
	fakeDaemonScript(t, scriptPath, flagPath, 0)

	if err := SpawnDetached(); err != nil {
		t.Fatalf("SpawnDetached: %v", err)
	}

	if !waitForFile(t, flagPath, 2*time.Second) {
		t.Fatalf("flag file %s never appeared (binary override not honored)", flagPath)
	}
}

func TestSpawnDetached_LogFileCreated(t *testing.T) {
	siftDir, scriptPath := withSpawnEnv(t)
	flagPath := filepath.Join(siftDir, "log.flag")
	fakeDaemonScript(t, scriptPath, flagPath, 0)

	if err := SpawnDetached(); err != nil {
		t.Fatalf("SpawnDetached: %v", err)
	}

	// Wait for the child to finish writing.
	if !waitForFile(t, flagPath, 2*time.Second) {
		t.Fatalf("child never ran (no flag file)")
	}

	logPath := filepath.Join(siftDir, "logs", "daemon.log")
	// Even after the script exits, the log file may still be in the OS
	// page cache; poll until visible to stat.
	if !waitForFile(t, logPath, 2*time.Second) {
		t.Fatalf("daemon log %s not created", logPath)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if want := "spawned arg=__daemon"; !substringContains(string(data), want) {
		t.Fatalf("log missing expected output\nwant substring: %q\ngot: %q", want, string(data))
	}
}

func TestSpawnDetached_NoBlocking(t *testing.T) {
	_, scriptPath := withSpawnEnv(t)
	// A 5-second sleep would visibly block the parent if Spawn waited.
	fakeDaemonScript(t, scriptPath, filepath.Join(t.TempDir(), "ignored.flag"), 5)

	start := time.Now()
	if err := SpawnDetached(); err != nil {
		t.Fatalf("SpawnDetached: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > 200*time.Millisecond {
		t.Fatalf("SpawnDetached blocked for %v; expected to return quickly", elapsed)
	}
}

// substringContains is a tiny dependency-free substring check.
func substringContains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
