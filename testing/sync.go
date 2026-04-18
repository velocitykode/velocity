// Package testsync provides synchronization helpers for tests that previously
// relied on fixed time.Sleep calls. Polling helpers here return as soon as a
// condition becomes true, so the common-case test runs fast while the timeout
// protects against indefinite hangs when the condition never fires.
package testsync

import (
	"testing"
	"time"
)

// Eventually polls cond every 10ms until it returns true or timeout elapses.
// On timeout, the test fails with msg. Prefer this over a fixed sleep when a
// test waits on a state change (queue drained, goroutine finished writing to
// a counter, file appeared, etc).
func Eventually(t testing.TB, cond func() bool, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !cond() {
		t.Fatalf("Eventually: %s (timeout after %s)", msg, timeout)
	}
}

// EventuallyEqual is Eventually specialized for comparing an int32 to an
// expected value — the most common use is an atomic counter that a worker
// pool increments.
func EventuallyEqual[T comparable](t testing.TB, get func() T, want T, timeout time.Duration, msg string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if get() == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := get()
	if got != want {
		t.Fatalf("EventuallyEqual: %s (got %v, want %v after %s)", msg, got, want, timeout)
	}
}
