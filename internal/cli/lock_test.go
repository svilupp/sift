package cli

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gofrs/flock"
)

// TestAcquireLockFailsFastWhenHeld guards against the regression where
// the in-process refresh path waited indefinitely for the lock. The
// helper must use TryLock semantics and surface errLockHeld so callers
// can decorate the error.
func TestAcquireLockFailsFastWhenHeld(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SIFT_DIR", dir)

	// Pre-acquire the lock from this process to simulate a daemon (or
	// another sift invocation) holding it. The path must match what
	// config.LockPath() returns for SIFT_DIR=dir.
	holder := flock.New(filepath.Join(dir, ".lock"))
	if ok, err := holder.TryLock(); err != nil || !ok {
		t.Fatalf("setup: holder TryLock returned ok=%v err=%v", ok, err)
	}
	t.Cleanup(func() { _ = holder.Unlock() })

	type result struct {
		err error
	}
	done := make(chan result, 1)
	go func() {
		fl, err := acquireLock()
		if fl != nil {
			_ = fl.Unlock()
		}
		done <- result{err: err}
	}()

	select {
	case res := <-done:
		if res.err == nil {
			t.Fatal("acquireLock succeeded; expected error because lock is held")
		}
		if !errors.Is(res.err, errLockHeld) {
			t.Fatalf("acquireLock error = %v; want errors.Is(errLockHeld)", res.err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("acquireLock blocked for >2s; expected fail-fast (TryLock)")
	}
}

// TestDecorateLockErrorPassesThroughNonHeld confirms unrelated errors
// are returned unchanged.
func TestDecorateLockErrorPassesThroughNonHeld(t *testing.T) {
	in := errors.New("disk full")
	got := decorateLockError(in, &refreshFlags{indexOnly: true})
	if got != in {
		t.Fatalf("decorateLockError mutated unrelated error: got %v want %v", got, in)
	}
}

// TestDecorateLockErrorWithoutDaemonReturnsOriginal: when no daemon is
// running, the wrapped errLockHeld should still pass through (the
// generic message is fine).
func TestDecorateLockErrorWithoutDaemonReturnsOriginal(t *testing.T) {
	t.Setenv("SIFT_DIR", t.TempDir())
	in := errors.Join(errLockHeld, errors.New("another sift operation is running"))
	got := decorateLockError(in, &refreshFlags{indexOnly: true})
	if got != in {
		t.Fatalf("decorateLockError replaced error when no daemon was running: got %v", got)
	}
}

// TestDecorateLockErrorRespectsSiftNoDaemon: with SIFT_NO_DAEMON set,
// we never probe the daemon and always return the original error.
func TestDecorateLockErrorRespectsSiftNoDaemon(t *testing.T) {
	t.Setenv("SIFT_NO_DAEMON", "1")
	in := errors.Join(errLockHeld, errors.New("held"))
	got := decorateLockError(in, &refreshFlags{generate: string(genStale)})
	if got != in {
		t.Fatalf("SIFT_NO_DAEMON should bypass daemon decoration: got %v want %v", got, in)
	}
}

// TestInProcessReason maps flag combinations to user-visible labels.
func TestInProcessReason(t *testing.T) {
	cases := []struct {
		name  string
		flags refreshFlags
		want  string
	}{
		{"indexOnly", refreshFlags{indexOnly: true}, "--index-only"},
		{"noIndex", refreshFlags{noIndex: true}, "--no-index"},
		{"generateStale", refreshFlags{generate: "stale"}, "--generate=stale"},
		{"generateNoneFallsThrough", refreshFlags{generate: "none"}, "this command"},
		{"empty", refreshFlags{}, "this command"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := inProcessReason(&tc.flags)
			if got != tc.want {
				t.Fatalf("inProcessReason(%+v) = %q, want %q", tc.flags, got, tc.want)
			}
		})
	}
}
