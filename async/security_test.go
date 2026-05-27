package async

import (
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Regression tests for the cross-cutting security audit findings X-03 and
// X-04 (docs/security-audit-2026-05/05-cross-cutting.md).
//
// X-03: Result.Get() double-closed `done`, panicking with
//       `close of closed channel` on the second call.
// X-04: All / Map silently dropped error returns from Result.Get(), masking
//       recovered panics inside fn closures (caller saw zero-value slice).

// X-03 regression: a second Get() on the same Result must not panic and must
// return the same value/error as the first call.
func TestResultGet_DoubleCallNoPanic(t *testing.T) {
	t.Run("success path", func(t *testing.T) {
		r := Run(func() int { return 42 })

		v1, err1 := r.Get()
		if err1 != nil {
			t.Fatalf("first Get: unexpected error %v", err1)
		}
		if v1 != 42 {
			t.Fatalf("first Get: want 42, got %d", v1)
		}

		// Second call MUST NOT panic. Catch panic explicitly so a regression
		// fails the test instead of crashing the runner.
		var v2 int
		var err2 error
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("second Get panicked: %v", p)
				}
			}()
			v2, err2 = r.Get()
		}()
		if err2 != nil {
			t.Fatalf("second Get: unexpected error %v", err2)
		}
		if v2 != v1 {
			t.Fatalf("second Get returned %d, expected same as first %d", v2, v1)
		}
	})

	t.Run("error (panic-converted) path", func(t *testing.T) {
		r := Run(func() int { panic("boom") })

		v1, err1 := r.Get()
		if err1 == nil {
			t.Fatalf("first Get: expected error from panicking fn, got nil")
		}
		if v1 != 0 {
			t.Fatalf("first Get: want zero value on error, got %d", v1)
		}

		var v2 int
		var err2 error
		func() {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("second Get panicked: %v", p)
				}
			}()
			v2, err2 = r.Get()
		}()
		if err2 == nil || err2.Error() != err1.Error() {
			t.Fatalf("second Get returned different error: first=%v second=%v", err1, err2)
		}
		if v2 != v1 {
			t.Fatalf("second Get returned %d, want %d", v2, v1)
		}
	})
}

// X-03 regression: many concurrent Get() callers must not race or panic on
// the close of `done`. sync.Once is the guarantee under test.
func TestResultGet_ConcurrentCallersNoPanic(t *testing.T) {
	const callers = 50
	r := Run(func() string { return "ok" })

	var (
		wg        sync.WaitGroup
		panicSeen atomic.Bool
		mismatch  atomic.Bool
	)
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			defer func() {
				if p := recover(); p != nil {
					panicSeen.Store(true)
				}
			}()
			v, err := r.Get()
			if err != nil || v != "ok" {
				mismatch.Store(true)
			}
		}()
	}
	wg.Wait()

	if panicSeen.Load() {
		t.Fatalf("concurrent Get() panicked (likely close of closed channel)")
	}
	if mismatch.Load() {
		t.Fatalf("concurrent Get() returned an unexpected value/error")
	}
}

// X-03 regression: the audit-flagged Ready -> Get -> Get pattern. Ready()
// reports completion only after Get() has consumed the channel (since
// Get is what closes `done`). The audit's concern is that once Ready
// returns true a misread/polling pattern leads to a second Get(), which
// historically panicked with `close of closed channel`. Confirm a stream
// of post-first-Get calls is safe.
func TestResultGet_AfterReadyThenDoubleGet(t *testing.T) {
	r := Run(func() int { return 7 })

	// First Get drains the channel and closes done. After this Ready must
	// be true and further Gets are the path under audit.
	v0, err0 := r.Get()
	if err0 != nil || v0 != 7 {
		t.Fatalf("priming Get: want (7,nil), got (%d,%v)", v0, err0)
	}
	if !r.Ready() {
		t.Fatal("Ready() should be true after a successful Get")
	}

	for i := 0; i < 3; i++ {
		func(i int) {
			defer func() {
				if p := recover(); p != nil {
					t.Fatalf("Get #%d panicked: %v", i, p)
				}
			}()
			v, err := r.Get()
			if err != nil || v != 7 {
				t.Fatalf("Get #%d: want (7,nil), got (%d,%v)", i, v, err)
			}
		}(i)
	}
}

// X-03 regression: after RunWithTimeout fires the timeout path (errorCh), a
// repeat Get() must still not double-close `done`.
func TestResultGet_TimeoutDoubleGetNoPanic(t *testing.T) {
	r := RunWithTimeout(20*time.Millisecond, func() int {
		time.Sleep(200 * time.Millisecond)
		return 99
	})

	_, err1 := r.Get()
	if err1 == nil {
		t.Fatalf("first Get: expected timeout error, got nil")
	}
	if !r.TimedOut() {
		t.Fatalf("expected TimedOut() to report true")
	}

	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("second Get panicked: %v", p)
			}
		}()
		_, err2 := r.Get()
		if err2 == nil || err2.Error() != err1.Error() {
			t.Fatalf("second Get returned different error: first=%v second=%v", err1, err2)
		}
	}()
}

// X-04 regression: All must surface a panic in any fn as a non-nil error
// rather than silently producing a zero-valued entry. The values slice may
// still be populated for the non-panicking fns.
func TestAll_PanickingFnSurfacesError(t *testing.T) {
	values, err := All(
		func() int { return 1 },
		func() int { panic("boom") },
		func() int { return 3 },
	)
	if err == nil {
		t.Fatalf("All swallowed the panic-converted error from a panicking fn")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("All error should mention the panic payload, got %q", err.Error())
	}
	if len(values) != 3 {
		t.Fatalf("All values slice should be len=3, got %d", len(values))
	}
	// Non-panicking fns still produced their values at the correct indices.
	if values[0] != 1 || values[2] != 3 {
		t.Fatalf("All did not preserve non-panicking values: %v", values)
	}
	// Panicking fn left a zero at its slot, which is fine; err is the signal.
	if values[1] != 0 {
		t.Fatalf("expected zero value at panicked index, got %d", values[1])
	}
}

// X-04 regression: All with no panics returns nil error.
func TestAll_NoErrorOnSuccess(t *testing.T) {
	values, err := All(
		func() int { return 10 },
		func() int { return 20 },
	)
	if err != nil {
		t.Fatalf("All returned unexpected error: %v", err)
	}
	if len(values) != 2 || values[0] != 10 || values[1] != 20 {
		t.Fatalf("All returned wrong values: %v", values)
	}
}

// X-04 regression: Map must surface a panic in any fn as a non-nil error.
func TestMap_PanickingFnSurfacesError(t *testing.T) {
	items := []int{1, 2, 3}
	values, err := Map(items, func(i int) int {
		if i == 2 {
			panic("kaboom")
		}
		return i * 10
	})
	if err == nil {
		t.Fatalf("Map swallowed the panic-converted error from a panicking fn")
	}
	if !strings.Contains(err.Error(), "kaboom") {
		t.Fatalf("Map error should mention the panic payload, got %q", err.Error())
	}
	if len(values) != 3 {
		t.Fatalf("Map values slice should be len=3, got %d", len(values))
	}
	if values[0] != 10 || values[2] != 30 {
		t.Fatalf("Map did not preserve non-panicking values: %v", values)
	}
	if values[1] != 0 {
		t.Fatalf("expected zero at panicked index, got %d", values[1])
	}
}

// X-04 regression: Map with no panics returns nil error.
func TestMap_NoErrorOnSuccess(t *testing.T) {
	items := []int{2, 4, 6}
	values, err := Map(items, func(i int) int { return i + 1 })
	if err != nil {
		t.Fatalf("Map returned unexpected error: %v", err)
	}
	if len(values) != 3 || values[0] != 3 || values[1] != 5 || values[2] != 7 {
		t.Fatalf("Map returned wrong values: %v", values)
	}
}

// Sanity check that AllWithError continues to work alongside the new All
// signature. The audit notes AllWithError already returned errors; this just
// guards against regressions in the same file.
func TestAllWithError_StillPropagates(t *testing.T) {
	_, err := AllWithError(
		func() (int, error) { return 1, nil },
		func() (int, error) { return 0, errors.New("nope") },
	)
	if err == nil || err.Error() != "nope" {
		t.Fatalf("AllWithError did not propagate error correctly: %v", err)
	}
}

// X-04 follow-up: AllWithError previously discarded the panic-converted
// error from Run on its Result.Get call. A panicking fn produced a
// zero-value resultPair (pair.err == nil) so the loop fell through to
// values[i] = pair.value with no error signal. Same security shape as the
// original X-04: panic in fn silently looked like success.
func TestAllWithError_PanicSurfacedAsError(t *testing.T) {
	values, err := AllWithError(
		func() (int, error) { return 1, nil },
		func() (int, error) { panic("boom") },
		func() (int, error) { return 3, nil },
	)
	if err == nil {
		t.Fatalf("AllWithError swallowed panic, expected non-nil error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("error should reference the panic value, got %q", err.Error())
	}
	if values != nil {
		t.Fatalf("on error AllWithError should return nil values, got %v", values)
	}
}

// X-04 follow-up regression: the normal-error path (fn returns an error)
// must continue to surface. This is the case TestAllWithError_StillPropagates
// also covers; included separately so the failure mode is unambiguous in
// the test output if AllWithError regresses on either flavour.
func TestAllWithError_NormalErrorStillSurfaced(t *testing.T) {
	values, err := AllWithError(
		func() (int, error) { return 1, nil },
		func() (int, error) { return 0, errors.New("normal-error") },
	)
	if err == nil || err.Error() != "normal-error" {
		t.Fatalf("expected normal-error, got %v", err)
	}
	if values != nil {
		t.Fatalf("on error AllWithError should return nil values, got %v", values)
	}
}

// X-04 follow-up: when one fn panics and another returns a normal error
// AllWithError must still surface SOME error. Submission order wins: the
// returned error is the first non-nil seen while iterating results in
// index order. This matches the documented "first error" semantics of
// AllWithError and the new All/Map. Documented in AllWithError's godoc.
func TestAllWithError_MixedPanicAndError(t *testing.T) {
	t.Run("panic first wins", func(t *testing.T) {
		values, err := AllWithError(
			func() (int, error) { panic("first-panic") },
			func() (int, error) { return 0, errors.New("second-error") },
		)
		if err == nil {
			t.Fatalf("expected non-nil error from mixed panic+error")
		}
		if !strings.Contains(err.Error(), "first-panic") {
			t.Fatalf("submission-order-first error should win; got %q", err.Error())
		}
		if values != nil {
			t.Fatalf("expected nil values on error, got %v", values)
		}
	})

	t.Run("normal-error first wins", func(t *testing.T) {
		values, err := AllWithError(
			func() (int, error) { return 0, errors.New("first-error") },
			func() (int, error) { panic("second-panic") },
		)
		if err == nil {
			t.Fatalf("expected non-nil error from mixed error+panic")
		}
		if err.Error() != "first-error" {
			t.Fatalf("submission-order-first error should win; got %q", err.Error())
		}
		if values != nil {
			t.Fatalf("expected nil values on error, got %v", values)
		}
	})
}
