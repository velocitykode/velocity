package async

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRun(t *testing.T) {
	t.Run("successful execution", func(t *testing.T) {
		result := Run(func() int {
			// orchestration: sleep is test input — the fake "work" Run wraps.
			time.Sleep(50 * time.Millisecond)
			return 42
		})

		if result.Ready() {
			t.Error("Result should not be ready immediately")
		}

		value, err := result.Get()
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if value != 42 {
			t.Errorf("Expected 42, got %d", value)
		}

		if !result.Ready() {
			t.Error("Result should be ready after Get()")
		}
	})

	t.Run("panic recovery", func(t *testing.T) {
		result := Run(func() int {
			panic("test panic")
		})

		value, err := result.Get()
		if err == nil {
			t.Error("Expected error from panic")
		}
		if value != 0 {
			t.Errorf("Expected zero value, got %d", value)
		}
	})
}

// VEL-88 regression: Ready must turn true once the producer completes,
// without any prior Get call, and Get must then return immediately.
func TestResultReady_ReflectsCompletionWithoutGet(t *testing.T) {
	release := make(chan struct{})
	result := Run(func() int {
		<-release
		return 42
	})

	if result.Ready() {
		t.Fatal("Ready() should be false while the producer is still running")
	}
	close(release)

	deadline := time.After(2 * time.Second)
	for !result.Ready() {
		select {
		case <-deadline:
			t.Fatal("Ready() never became true after the producer completed")
		case <-time.After(time.Millisecond):
		}
	}

	// Ready is true with no Get issued yet; Get must not block.
	done := make(chan struct{})
	go func() {
		defer close(done)
		v, err := result.Get()
		if err != nil || v != 42 {
			t.Errorf("Get after Ready: want (42,nil), got (%d,%v)", v, err)
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Get blocked even though Ready() reported true")
	}
}

func TestRunWithTimeout(t *testing.T) {
	t.Run("completes before timeout", func(t *testing.T) {
		result := RunWithTimeout(200*time.Millisecond, func() string {
			// orchestration: sleep is test input — work that finishes before timeout.
			time.Sleep(50 * time.Millisecond)
			return "success"
		})

		value, err := result.Get()
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if value != "success" {
			t.Errorf("Expected 'success', got %s", value)
		}
		if result.TimedOut() {
			t.Error("Should not have timed out")
		}
	})

	t.Run("times out", func(t *testing.T) {
		result := RunWithTimeout(50*time.Millisecond, func() string {
			// orchestration: sleep is test input — work that deliberately outlasts the timeout.
			time.Sleep(200 * time.Millisecond)
			return "too late"
		})

		_, err := result.Get()
		if err == nil {
			t.Error("Expected timeout error")
		}
		if !result.TimedOut() {
			t.Error("Should have timed out")
		}
	})
}

func TestRunWithContext(t *testing.T) {
	t.Run("completes before cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		result := RunWithContext(ctx, func() int {
			// orchestration: sleep is test input — work that completes before ctx cancel.
			time.Sleep(50 * time.Millisecond)
			return 100
		})

		value, err := result.Get()
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if value != 100 {
			t.Errorf("Expected 100, got %d", value)
		}
	})

	t.Run("cancelled before completion", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())

		result := RunWithContext(ctx, func() int {
			// orchestration: sleep is test input — long-running work meant to be cancelled.
			time.Sleep(200 * time.Millisecond)
			return 100
		})

		// Cancel after short delay
		go func() {
			// orchestration: sleep is timing input — deliberate delay before cancel.
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()

		_, err := result.Get()
		if err == nil {
			t.Error("Expected cancellation error")
		}
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Expected context.Canceled, got %v", err)
		}
	})
}

func TestAll(t *testing.T) {
	t.Run("parallel execution", func(t *testing.T) {
		start := time.Now()
		// orchestration: each closure's sleep is test input, the fake "work"
		// All must run concurrently. Elapsed time is the assertion.
		results, err := All(
			func() int { time.Sleep(100 * time.Millisecond); return 1 },
			func() int { time.Sleep(100 * time.Millisecond); return 2 },
			func() int { time.Sleep(100 * time.Millisecond); return 3 },
		)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("unexpected error from All: %v", err)
		}

		// Should complete in ~100ms, not 300ms
		if elapsed > 150*time.Millisecond {
			t.Errorf("All should run functions in parallel, took %v", elapsed)
		}

		if len(results) != 3 {
			t.Fatalf("Expected 3 results, got %d", len(results))
		}

		if results[0] != 1 || results[1] != 2 || results[2] != 3 {
			t.Errorf("Unexpected results: %v", results)
		}
	})

	t.Run("preserves order", func(t *testing.T) {
		// orchestration: staggered sleeps are test input so closures finish
		// out of submission order; assertion checks output preserves index order.
		results, err := All(
			func() string { time.Sleep(100 * time.Millisecond); return "first" },
			func() string { time.Sleep(50 * time.Millisecond); return "second" },
			func() string { time.Sleep(10 * time.Millisecond); return "third" },
		)
		if err != nil {
			t.Fatalf("unexpected error from All: %v", err)
		}

		// Despite different delays, order should be preserved
		if results[0] != "first" || results[1] != "second" || results[2] != "third" {
			t.Errorf("Order not preserved: %v", results)
		}
	})
}

func TestAllWithError(t *testing.T) {
	t.Run("all succeed", func(t *testing.T) {
		results, err := AllWithError(
			func() (int, error) { return 1, nil },
			func() (int, error) { return 2, nil },
			func() (int, error) { return 3, nil },
		)

		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if len(results) != 3 || results[0] != 1 || results[1] != 2 || results[2] != 3 {
			t.Errorf("Unexpected results: %v", results)
		}
	})

	t.Run("one fails", func(t *testing.T) {
		_, err := AllWithError(
			func() (int, error) { return 1, nil },
			func() (int, error) { return 0, errors.New("failed") },
			func() (int, error) { return 3, nil },
		)

		if err == nil {
			t.Error("Expected error from failed function")
		}
	})
}

func TestRace(t *testing.T) {
	t.Run("fastest wins", func(t *testing.T) {
		// orchestration: staggered sleeps are test input so Race sees a clear
		// ordering and the assertion checks the fastest closure's value wins.
		result := Race(
			func() string { time.Sleep(200 * time.Millisecond); return "slow" },
			func() string { time.Sleep(50 * time.Millisecond); return "fast" },
			func() string { time.Sleep(100 * time.Millisecond); return "medium" },
		)

		value, err := result.Get()
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if value != "fast" {
			t.Errorf("Expected 'fast', got %s", value)
		}
	})
}

// getWithin drains result.Get() but fails the test (instead of hanging the
// whole run) if Get does not return within d. Used by the all-panic / zero-fn
// Race tests, which would block Get forever before the B38 fix.
func getWithin[T any](t *testing.T, result *Result[T], d time.Duration) (T, error) {
	t.Helper()
	type outcome struct {
		v   T
		err error
	}
	ch := make(chan outcome, 1)
	go func() {
		v, err := result.Get()
		ch <- outcome{v, err}
	}()
	select {
	case o := <-ch:
		return o.v, o.err
	case <-time.After(d):
		var zero T
		t.Fatalf("Get did not return within %v (deadlock)", d)
		return zero, nil
	}
}

func TestRace_FailureModes(t *testing.T) {
	t.Run("all panic returns error", func(t *testing.T) {
		result := Race(
			func() int { panic("boom-1") },
			func() int { panic("boom-2") },
			func() int { panic("boom-3") },
		)
		_, err := getWithin(t, result, 2*time.Second)
		if err == nil {
			t.Fatal("expected non-nil error when every fn panics")
		}
	})

	t.Run("zero fns errors", func(t *testing.T) {
		result := Race[int]()
		_, err := getWithin(t, result, 2*time.Second)
		if err == nil {
			t.Fatal("expected error when Race called with zero functions")
		}
	})

	t.Run("winner among panicking losers", func(t *testing.T) {
		result := Race(
			func() string { panic("loser-1") },
			func() string { time.Sleep(30 * time.Millisecond); return "winner" },
			func() string { panic("loser-2") },
		)
		value, err := getWithin(t, result, 2*time.Second)
		if err != nil {
			t.Fatalf("unexpected error with a live winner: %v", err)
		}
		if value != "winner" {
			t.Fatalf("expected 'winner', got %q", value)
		}
	})

	t.Run("all panic routes through handlePanic", func(t *testing.T) {
		cap := withLogger(t)
		result := Race(
			func() int { panic("logged-1") },
			func() int { panic("logged-2") },
		)
		if _, err := getWithin(t, result, 2*time.Second); err == nil {
			t.Fatal("expected error from all-panic Race")
		}
		// handlePanic logs "async: panic recovered"; confirm parity with Run.
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			for _, e := range cap.snapshot() {
				if e.msg == "async: panic recovered" {
					return
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("expected handlePanic log; got: %+v", cap.snapshot())
	})
}

func TestRaceWithTimeout(t *testing.T) {
	t.Run("completes before timeout", func(t *testing.T) {
		// orchestration: closure sleeps are test input — work that finishes
		// before the 200ms timeout so Race can pick a winner.
		result := RaceWithTimeout(200*time.Millisecond,
			func() int { time.Sleep(50 * time.Millisecond); return 1 },
			func() int { time.Sleep(100 * time.Millisecond); return 2 },
		)

		value, err := result.Get()
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
		if value != 1 {
			t.Errorf("Expected 1, got %d", value)
		}
		if result.TimedOut() {
			t.Error("Should not have timed out")
		}
	})

	t.Run("all timeout", func(t *testing.T) {
		// orchestration: closure sleeps are test input — both deliberately
		// outlast the 50ms timeout so Race must return the timeout error.
		result := RaceWithTimeout(50*time.Millisecond,
			func() int { time.Sleep(200 * time.Millisecond); return 1 },
			func() int { time.Sleep(300 * time.Millisecond); return 2 },
		)

		_, err := result.Get()
		if err == nil {
			t.Error("Expected timeout error")
		}
		if !result.TimedOut() {
			t.Error("Should have timed out")
		}
	})

	t.Run("all panic terminates at deadline", func(t *testing.T) {
		// RaceWithTimeout's ctx.Done branch already unblocks Get; confirm an
		// all-panic race still terminates (with DeadlineExceeded) rather than
		// hanging.
		result := RaceWithTimeout(50*time.Millisecond,
			func() int { panic("boom-1") },
			func() int { panic("boom-2") },
		)
		_, err := getWithin(t, result, 2*time.Second)
		if err == nil {
			t.Fatal("expected timeout error from all-panic RaceWithTimeout")
		}
		if !result.TimedOut() {
			t.Error("Should have timed out")
		}
	})
}

func TestGo(t *testing.T) {
	t.Run("executes without waiting", func(t *testing.T) {
		done := make(chan bool, 1)
		Go(func() {
			// orchestration: sleep is test input — fake work so Go can be
			// observed not to block the caller (checked via select below).
			time.Sleep(50 * time.Millisecond)
			done <- true
		})

		select {
		case <-done:
			t.Error("Should not complete immediately")
		case <-time.After(10 * time.Millisecond):
			// Expected - function is still running
		}

		// Wait for completion
		select {
		case <-done:
			// Expected
		case <-time.After(100 * time.Millisecond):
			t.Error("Function should have completed")
		}
	})

	t.Run("recovers from panic", func(t *testing.T) {
		// Test passes if this doesn't crash the process. The inner closure's
		// deferred close runs during panic unwind, before Go's own recover.
		done := make(chan struct{})
		Go(func() {
			defer close(done)
			panic("test panic")
		})
		select {
		case <-done:
		case <-time.After(500 * time.Millisecond):
			t.Fatal("panicking goroutine didn't run its defer")
		}
	})
}

func TestGoWithRecover(t *testing.T) {
	recovered := make(chan any, 1)

	GoWithRecover(func() {
		panic("custom panic")
	}, func(p any) {
		recovered <- p
	})

	select {
	case p := <-recovered:
		if p != "custom panic" {
			t.Errorf("Expected 'custom panic', got %v", p)
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("Panic handler not called")
	}
}

func TestForEach(t *testing.T) {
	t.Run("processes all items", func(t *testing.T) {
		items := []int{1, 2, 3, 4, 5}
		var sum atomic.Int64

		ForEach(items, 2, func(item int) {
			// orchestration: sleep is test input — fake per-item work.
			time.Sleep(10 * time.Millisecond)
			sum.Add(int64(item * 2))
		})

		expected := int64(2 + 4 + 6 + 8 + 10)
		if sum.Load() != expected {
			t.Errorf("Expected sum %d, got %d", expected, sum.Load())
		}
	})

	t.Run("respects concurrency limit", func(t *testing.T) {
		items := []int{1, 2, 3, 4}
		var active atomic.Int32
		var maxActive atomic.Int32

		ForEach(items, 2, func(item int) {
			cur := active.Add(1)
			for {
				old := maxActive.Load()
				if cur <= old || maxActive.CompareAndSwap(old, cur) {
					break
				}
			}
			// orchestration: sleep is test input — holds workers "active"
			// long enough for the maxActive CAS loop to observe concurrency.
			time.Sleep(50 * time.Millisecond)
			active.Add(-1)
		})

		if maxActive.Load() > 2 {
			t.Errorf("Concurrency limit exceeded: %d > 2", maxActive.Load())
		}
	})
}

func TestMap(t *testing.T) {
	t.Run("transforms items", func(t *testing.T) {
		items := []int{1, 2, 3, 4, 5}
		results, err := Map(items, func(i int) int {
			return i * i
		})
		if err != nil {
			t.Fatalf("unexpected error from Map: %v", err)
		}

		expected := []int{1, 4, 9, 16, 25}
		for i, v := range results {
			if v != expected[i] {
				t.Errorf("Position %d: expected %d, got %d", i, expected[i], v)
			}
		}
	})

	t.Run("parallel execution", func(t *testing.T) {
		items := []int{1, 2, 3}
		start := time.Now()

		_, err := Map(items, func(i int) int {
			// orchestration: sleep is test input, fake per-item work; total
			// elapsed is the assertion that Map runs closures in parallel.
			time.Sleep(100 * time.Millisecond)
			return i
		})
		if err != nil {
			t.Fatalf("unexpected error from Map: %v", err)
		}

		elapsed := time.Since(start)
		if elapsed > 150*time.Millisecond {
			t.Errorf("Map should run in parallel, took %v", elapsed)
		}
	})
}

func TestGetOrDefault(t *testing.T) {
	t.Run("returns value on success", func(t *testing.T) {
		result := Run(func() string {
			return "success"
		})

		value := result.GetOrDefault("default")
		if value != "success" {
			t.Errorf("Expected 'success', got %s", value)
		}
	})

	t.Run("returns default on error", func(t *testing.T) {
		result := Run(func() string {
			panic("error")
		})

		value := result.GetOrDefault("default")
		if value != "default" {
			t.Errorf("Expected 'default', got %s", value)
		}
	})
}
