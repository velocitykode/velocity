// Package testsync provides synchronization helpers for tests that previously
// relied on fixed time.Sleep calls. Polling helpers here return as soon as a
// condition becomes true, so the common-case test runs fast while the timeout
// protects against indefinite hangs when the condition never fires.
//
// # time.Sleep policy (tests only)
//
// Adding a new time.Sleep to a *_test.go file requires a comment explaining
// why Eventually / channels / WaitGroups don't fit. Acceptable reasons:
//
//   - TTL MODELING: the feature under test is wall-clock based (cache
//     expiration, rate-limit windows, delayed-job visibility) and a real
//     duration must elapse for the assertion to mean anything.
//   - ORCHESTRATION: the sleep sits inside a closure that the helper under
//     test is supposed to run (the fake "work" for async.All, async.Race,
//     queue Job.Handle, scheduler callback, etc) — the sleep is test input,
//     not synchronization.
//   - STABILITY WINDOW: the test makes a NEGATIVE assertion ("this should NOT
//     have fired yet") and a small sleep gives any racing goroutine a chance
//     to violate the invariant before we sample.
//   - FIXTURE: benchmark timings, deliberate time-difference for UpdatedAt,
//     simulated slow HTTP handler feeding duration histograms.
//
// Not acceptable: "wait for the goroutine to finish", "let registration
// complete", "give the dispatcher a moment". Use Eventually or a channel.
//
// If a reviewer can't tell which bucket a sleep falls into from the
// surrounding comment, the sleep is probably wait-and-hope and should be
// replaced.
//
// # Naming debt (acknowledged, deferred to v1.0)
//
// This directory is testing/ but the package is testsync, a deliberate
// directory/package name mismatch. The sibling testing/http is package http,
// which collides with net/http and is imported under an alias such as
// velhttp. The consumer-facing entry point for the testing toolkit is the
// velocitytest package. Both non-matching names are recognized pre-1.0 naming
// debt; any rename is deferred to the v1.0 boundary.
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

// EventuallyEqual is Eventually specialized for comparing a value to an
// expected one — the most common use is an atomic counter that a worker
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
