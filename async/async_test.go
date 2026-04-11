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

func TestRunWithTimeout(t *testing.T) {
	t.Run("completes before timeout", func(t *testing.T) {
		result := RunWithTimeout(200*time.Millisecond, func() string {
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
			time.Sleep(200 * time.Millisecond)
			return 100
		})

		// Cancel after short delay
		go func() {
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
		results := All(
			func() int { time.Sleep(100 * time.Millisecond); return 1 },
			func() int { time.Sleep(100 * time.Millisecond); return 2 },
			func() int { time.Sleep(100 * time.Millisecond); return 3 },
		)
		elapsed := time.Since(start)

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
		results := All(
			func() string { time.Sleep(100 * time.Millisecond); return "first" },
			func() string { time.Sleep(50 * time.Millisecond); return "second" },
			func() string { time.Sleep(10 * time.Millisecond); return "third" },
		)

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

func TestRaceWithTimeout(t *testing.T) {
	t.Run("completes before timeout", func(t *testing.T) {
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
}

func TestGo(t *testing.T) {
	t.Run("executes without waiting", func(t *testing.T) {
		done := make(chan bool, 1)
		Go(func() {
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
		// Should not crash the test
		Go(func() {
			panic("test panic")
		})
		time.Sleep(10 * time.Millisecond) // Give time for panic to occur
		// Test passes if we reach here
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
		results := Map(items, func(i int) int {
			return i * i
		})

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

		Map(items, func(i int) int {
			time.Sleep(100 * time.Millisecond)
			return i
		})

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
