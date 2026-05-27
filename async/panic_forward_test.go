package async

import (
	"context"
	"strings"
	"testing"
	"time"
)

// These tests cover the X-04 follow-up: panics inside the inner goroutine
// of RunWithTimeout / RunWithContext must be forwarded to the outer select
// via a panic channel, otherwise:
//   - RunWithContext hangs forever (no `done` send, ctx may never cancel).
//   - RunWithTimeout returns a misleading "operation timed out" error.
//
// Each test guards r.Get() with a select on time.After so a regression manifests
// as a fast t.Fatal rather than a 60s test-suite hang.

// getWithDeadline drains r in a worker and returns (value, err) or fatals if
// the result doesn't arrive within d. The worker has cap=1 channels so it
// can't leak past test completion.
func getWithDeadline[T any](t *testing.T, r *Result[T], d time.Duration) (T, error) {
	t.Helper()
	type pair struct {
		v T
		e error
	}
	ch := make(chan pair, 1)
	go func() {
		v, e := r.Get()
		ch <- pair{v, e}
	}()
	select {
	case p := <-ch:
		return p.v, p.e
	case <-time.After(d):
		var zero T
		t.Fatalf("Result.Get did not return within %v (regression: panic not forwarded?)", d)
		return zero, nil
	}
}

func TestRunWithContext_PanicReturnsError(t *testing.T) {
	// Pre-fix: this test would hang at r.Get() because RunWithContext's
	// inner goroutine recovered the panic but never forwarded it to the
	// outer select. The outer goroutine then blocked waiting on `done`
	// (never sent) or ctx.Done() (Background never cancels).
	_ = withLogger(t) // silence the panic log; we only care about the error path.

	r := RunWithContext(context.Background(), func() int {
		panic("forward-context-panic")
	})

	_, err := getWithDeadline(t, r, time.Second)
	if err == nil {
		t.Fatal("expected non-nil error from panicking RunWithContext")
	}
	if !strings.Contains(err.Error(), "forward-context-panic") {
		t.Fatalf("error %q does not mention panic marker", err.Error())
	}
}

func TestRunWithTimeout_PanicReturnsPanicErrorNotTimeout(t *testing.T) {
	// Pre-fix: this returned "operation timed out after 5s" after the full
	// 5-second wait, because the inner recover dropped the panic on the
	// floor and the outer select had no panic branch.
	_ = withLogger(t)

	r := RunWithTimeout(5*time.Second, func() int {
		panic("forward-timeout-panic")
	})

	_, err := getWithDeadline(t, r, time.Second)
	if err == nil {
		t.Fatal("expected non-nil error from panicking RunWithTimeout")
	}
	if strings.Contains(err.Error(), "operation timed out") {
		t.Fatalf("got misleading timeout error %q, expected panic error", err.Error())
	}
	if !strings.Contains(err.Error(), "forward-timeout-panic") {
		t.Fatalf("error %q does not mention panic marker", err.Error())
	}
}

func TestRunWithContext_TimeoutStillFires(t *testing.T) {
	// Regression guard: adding panicCh must not break the ctx-cancel path.
	// The fn-internal sleep is short enough that the worker goroutine has
	// definitely exited by the time goleak runs at process teardown (Go
	// offers no goroutine preemption, so unfinished sleeps would leak).
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	r := RunWithContext(ctx, func() int {
		time.Sleep(150 * time.Millisecond)
		return 42
	})

	_, err := getWithDeadline(t, r, 2*time.Second)
	if err == nil {
		t.Fatal("expected ctx error, got nil")
	}
	if !strings.Contains(err.Error(), "deadline") && !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("expected ctx.Err()-shaped error, got %q", err.Error())
	}
	// Wait for the worker goroutine's sleep to finish so goleak doesn't
	// see it at process exit. Go has no preemption; we must outwait it.
	time.Sleep(200 * time.Millisecond)
}

func TestRunWithTimeout_TimeoutStillFires(t *testing.T) {
	// Regression guard: panicCh must not pre-empt the timeout branch for
	// non-panicking slow work.
	r := RunWithTimeout(50*time.Millisecond, func() int {
		time.Sleep(150 * time.Millisecond)
		return 42
	})

	_, err := getWithDeadline(t, r, 2*time.Second)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "operation timed out") {
		t.Fatalf("expected timeout-shaped error, got %q", err.Error())
	}
	// See note in sibling test: outwait the un-preemptible worker.
	time.Sleep(200 * time.Millisecond)
}

// TestRunWithTimeout_PanicVsTimeoutRace exercises the race: whichever branch
// fires first must win, the other must not block test completion. We can't
// pin which path is taken (it's a real race), only that the test returns
// quickly with SOME error.
func TestRunWithTimeout_PanicVsTimeoutRace(t *testing.T) {
	_ = withLogger(t)

	for i := 0; i < 20; i++ {
		r := RunWithTimeout(time.Millisecond, func() int {
			// Tiny stagger so sometimes timeout wins, sometimes panic wins.
			time.Sleep(time.Millisecond)
			panic("race-panic")
		})
		_, err := getWithDeadline(t, r, time.Second)
		if err == nil {
			t.Fatalf("iter %d: expected non-nil error", i)
		}
	}
}
