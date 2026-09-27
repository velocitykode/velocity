package async

import (
	"sync/atomic"
	"testing"
	"time"
)

// recoveredPanicLogs counts the "async: panic recovered" entries cap holds.
func recoveredPanicLogs(cap *captureLogger) int {
	n := 0
	for _, e := range cap.snapshot() {
		if e.msg == "async: panic recovered" {
			n++
		}
	}
	return n
}

// TestSetPanicHook_TakesOverThePanic asserts a panic hook that returns
// normally takes a recovered panic over, so the package does not also log
// it, while a panic with no hook installed, or with a hook that itself
// panics, is logged.
func TestSetPanicHook_TakesOverThePanic(t *testing.T) {
	cap := withLogger(t)
	t.Cleanup(func() { SetPanicHook(nil) })

	var hooked atomic.Int32
	SetPanicHook(func(any) { hooked.Add(1) })
	done := make(chan struct{})
	Go(func() { defer close(done); panic("taken over") })
	<-done
	deadline := time.Now().Add(2 * time.Second)
	for hooked.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if hooked.Load() != 1 {
		t.Fatalf("hook fired %d times, want 1", hooked.Load())
	}
	time.Sleep(20 * time.Millisecond)
	if n := recoveredPanicLogs(cap); n != 0 {
		t.Errorf("panic the hook took over was logged %d times, want 0", n)
	}

	SetPanicHook(func(any) { panic("hook itself panics") })
	done = make(chan struct{})
	Go(func() { defer close(done); panic("hook failed") })
	<-done
	deadline = time.Now().Add(2 * time.Second)
	for recoveredPanicLogs(cap) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := recoveredPanicLogs(cap); n != 1 {
		t.Errorf("panic whose hook panicked logged %d times, want 1", n)
	}

	SetPanicHook(nil)
	done = make(chan struct{})
	Go(func() { defer close(done); panic("no hook") })
	<-done
	deadline = time.Now().Add(2 * time.Second)
	for recoveredPanicLogs(cap) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := recoveredPanicLogs(cap); n != 2 {
		t.Errorf("panic with no hook: %d panic logs in all, want 2", n)
	}
}
