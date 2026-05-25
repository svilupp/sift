package daemon

import (
	stdsync "sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestIdleTimerFiresOnce asserts that onIdle is invoked at most once
// even when a burst of Reset calls races with the timer expiration.
//
// Strategy: launch the timer with a 30ms timeout. Wait ~25ms (so we're
// inside the fire window), then fire a burst of concurrent Resets. With
// the idleTimer's mutex around the fired flag, the order resolves as
// either "timer fired first → all Resets become no-ops" or "Reset
// landed first → timer re-armed → fires once afterwards". Either way
// onIdle runs exactly once.
func TestIdleTimerFiresOnce(t *testing.T) {
	var fireCount atomic.Int64
	timer := newIdleTimer(30*time.Millisecond, func() {
		fireCount.Add(1)
	})
	defer timer.Stop()

	// Sleep most of the timeout, then race a burst of Resets against the
	// expiration. The exact ordering is non-deterministic — this is the
	// race window the mutex must close.
	time.Sleep(25 * time.Millisecond)

	var wg stdsync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			timer.Reset()
		}()
	}
	wg.Wait()

	// Wait long enough for any rearmed timer to fire. 30ms timeout +
	// generous slack covers slow CI runners.
	time.Sleep(200 * time.Millisecond)

	if got := fireCount.Load(); got != 1 {
		t.Errorf("fireCount = %d, want 1 (onIdle should run exactly once)", got)
	}
}

// TestIdleTimerResetBeforeFire asserts that calling Reset before the
// timeout elapses pushes the fire deadline forward — onIdle should not
// run as long as Resets keep arriving inside the timeout window.
func TestIdleTimerResetBeforeFire(t *testing.T) {
	var fired atomic.Bool
	timer := newIdleTimer(50*time.Millisecond, func() {
		fired.Store(true)
	})
	defer timer.Stop()

	// Reset every 10ms for 200ms. Each Reset extends the deadline by
	// 50ms, so onIdle must NOT fire in this window.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		timer.Reset()
		time.Sleep(10 * time.Millisecond)
	}
	if fired.Load() {
		t.Fatal("onIdle fired despite Reset extending the window")
	}

	// Now stop resetting; the timer should fire within timeout + a small
	// scheduling slack.
	time.Sleep(150 * time.Millisecond)
	if !fired.Load() {
		t.Fatal("onIdle did not fire after Resets stopped")
	}
}

// TestIdleTimerConcurrentResets asserts that many goroutines hammering
// Reset is data-race safe. Run with -race to surface any unsynchronised
// access to timer/fired.
func TestIdleTimerConcurrentResets(t *testing.T) {
	timer := newIdleTimer(500*time.Millisecond, func() {})
	defer timer.Stop()

	var wg stdsync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				timer.Reset()
			}
		}()
	}
	wg.Wait()
}

// TestIdleTimerStopPreventsFire asserts that Stop before the timeout
// elapses prevents onIdle from being called.
func TestIdleTimerStopPreventsFire(t *testing.T) {
	var fired atomic.Bool
	timer := newIdleTimer(50*time.Millisecond, func() {
		fired.Store(true)
	})
	timer.Stop()

	// Wait well past the timeout. The timer must not fire post-Stop.
	time.Sleep(150 * time.Millisecond)
	if fired.Load() {
		t.Error("onIdle fired despite Stop being called before timeout")
	}
}

// TestIdleTimerZeroTimeoutNeverFires asserts that a zero-timeout idle
// timer is effectively disabled — Reset is a no-op and onIdle never runs.
func TestIdleTimerZeroTimeoutNeverFires(t *testing.T) {
	var fired atomic.Bool
	timer := newIdleTimer(0, func() {
		fired.Store(true)
	})
	defer timer.Stop()

	for i := 0; i < 10; i++ {
		timer.Reset()
	}
	time.Sleep(100 * time.Millisecond)
	if fired.Load() {
		t.Error("zero-timeout idle timer fired onIdle; expected disabled")
	}
}
