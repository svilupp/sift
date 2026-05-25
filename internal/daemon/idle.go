package daemon

import (
	"log/slog"
	"net/http"
	"sync"
	"time"
)

// idleTimer fires onIdle exactly once after timeout has elapsed without
// a Reset. A timeout of zero disables the timer entirely: Reset becomes a
// no-op and onIdle is never invoked.
//
// All access to fired/timer is serialised through mu. Using an atomic.Bool
// outside the mutex would let a late time.AfterFunc callback race with
// Reset and re-arm a timer that already fired its onIdle.
type idleTimer struct {
	timeout time.Duration
	onIdle  func()

	mu       sync.Mutex
	timer    *time.Timer
	fired    bool // guarded by mu
	stopOnce sync.Once
}

// newIdleTimer constructs an idle timer with the given timeout and callback.
// If timeout is zero, the returned timer never fires.
func newIdleTimer(timeout time.Duration, onIdle func()) *idleTimer {
	t := &idleTimer{
		timeout: timeout,
		onIdle:  onIdle,
	}
	if timeout > 0 {
		t.timer = time.AfterFunc(timeout, t.fire)
	}
	return t
}

// fire is invoked by the underlying time.Timer. It re-checks fired-state
// under the mutex so a callback that started before Stop()/Reset() gets
// observed cannot race past those calls and invoke onIdle a second time.
//
// A defer-recover guards onIdle: a panic in user-supplied shutdown logic
// must not crash the daemon.
func (t *idleTimer) fire() {
	t.mu.Lock()
	if t.fired {
		t.mu.Unlock()
		return
	}
	t.fired = true
	cb := t.onIdle
	t.mu.Unlock()

	if cb == nil {
		return
	}
	defer func() {
		if r := recover(); r != nil {
			slog.Default().Error("idle onIdle panic", slog.Any("recover", r))
		}
	}()
	cb()
}

// Reset extends the idle window. If the timer is disabled (timeout==0) or
// has already fired, Reset is a no-op.
func (t *idleTimer) Reset() {
	if t.timeout == 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.fired {
		return
	}
	if t.timer == nil {
		return
	}
	// Per Go docs, Reset on an active timer should be preceded by Stop+drain;
	// we don't drain because we don't read the channel (AfterFunc).
	t.timer.Stop()
	t.timer.Reset(t.timeout)
}

// Stop halts the timer permanently and prevents onIdle from firing if it
// has not already done so. Safe to call multiple times.
func (t *idleTimer) Stop() {
	t.stopOnce.Do(func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		// Mark as fired so any in-flight callback short-circuits.
		t.fired = true
		if t.timer != nil {
			t.timer.Stop()
		}
	})
}

// idleResetPaths is the allowlist of routes that count as "user activity"
// for keeping the daemon alive. /health is intentionally excluded so that
// `sift daemon status` polling doesn't keep the daemon alive forever.
var idleResetPaths = map[string]struct{}{
	"/search":  {},
	"/refresh": {},
}

// wrapWithIdleReset wraps next so that requests to user-activity routes
// (/search, /refresh) reset the idle timer. /health and /shutdown do NOT
// reset.
func wrapWithIdleReset(next http.Handler, t *idleTimer) http.Handler {
	if t == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, ok := idleResetPaths[r.URL.Path]; ok {
			t.Reset()
		}
		next.ServeHTTP(w, r)
	})
}
