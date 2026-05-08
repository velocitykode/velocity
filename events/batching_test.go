package events

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	testsync "github.com/velocitykode/velocity/testing"
)

// Test Batching Dispatcher
func TestBatchingDispatcher(t *testing.T) {
	t.Run("batches events up to batch size", func(t *testing.T) {
		var count int32
		dispatcher := NewBatchingDispatcher(3, 100*time.Millisecond)
		dispatcher.Start()
		defer dispatcher.Stop()

		dispatcher.Listen("test.event", &countingListener{counter: &count})

		// Dispatch 2 events (under batch size)
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})

		time.Sleep(10 * time.Millisecond)
		if atomic.LoadInt32(&count) != 0 {
			t.Error("Events should not be dispatched before batch size reached")
		}

		// Dispatch 3rd event (reaches batch size)
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})

		testsync.EventuallyEqual(t, func() int32 { return atomic.LoadInt32(&count) }, int32(3), time.Second, "batch size reached")
	})

	t.Run("flushes events on interval", func(t *testing.T) {
		var count int32
		dispatcher := NewBatchingDispatcher(10, 50*time.Millisecond)
		dispatcher.Start()
		defer dispatcher.Stop()

		dispatcher.Listen("test.event", &countingListener{counter: &count})

		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})

		// Wait for flush interval (50ms) to fire
		testsync.EventuallyEqual(t, func() int32 { return atomic.LoadInt32(&count) }, int32(2), time.Second, "flush interval")
	})

	t.Run("manual flush dispatches all pending events", func(t *testing.T) {
		var count int32
		dispatcher := NewBatchingDispatcher(10, 1*time.Second)
		dispatcher.Start()
		defer dispatcher.Stop()

		dispatcher.Listen("test.event", &countingListener{counter: &count})

		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})

		if err := dispatcher.Flush(); err != nil {
			t.Errorf("Flush failed: %v", err)
		}

		if atomic.LoadInt32(&count) != 2 {
			t.Errorf("Expected 2 events after flush, got %d", atomic.LoadInt32(&count))
		}
	})

	t.Run("get batch size returns current batch count", func(t *testing.T) {
		dispatcher := NewBatchingDispatcher(10, 1*time.Second)
		dispatcher.Start()
		defer dispatcher.Stop()

		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})

		size := dispatcher.GetBatchSize()
		if size != 2 {
			t.Errorf("Expected batch size 2, got %d", size)
		}
	})
}

// Test Debouncing Dispatcher
func TestDebouncingDispatcher(t *testing.T) {
	t.Run("debounces rapid events", func(t *testing.T) {
		var count int32
		dispatcher := NewDebouncingDispatcher(50 * time.Millisecond)
		defer dispatcher.Stop()

		dispatcher.Listen("test.event", &countingListener{counter: &count})

		// Dispatch rapidly
		for i := 0; i < 10; i++ {
			dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
			time.Sleep(10 * time.Millisecond)
		}

		// Wait for debounce to fire
		testsync.EventuallyEqual(t, func() int32 { return atomic.LoadInt32(&count) }, int32(1), time.Second, "debounce flushes once")
	})

	t.Run("dispatch now bypasses debounce", func(t *testing.T) {
		var count int32
		dispatcher := NewDebouncingDispatcher(100 * time.Millisecond)
		defer dispatcher.Stop()

		dispatcher.Listen("test.event", &countingListener{counter: &count})

		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
		dispatcher.DispatchNow(context.Background(), &simpleEvent{name: "test.event"})

		testsync.EventuallyEqual(t, func() int32 { return atomic.LoadInt32(&count) }, int32(1), 500*time.Millisecond, "DispatchNow fires immediately")
	})

	t.Run("get pending count returns debounced events", func(t *testing.T) {
		dispatcher := NewDebouncingDispatcher(100 * time.Millisecond)
		defer dispatcher.Stop()

		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event1"})
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event2"})

		count := dispatcher.GetPendingCount()
		if count != 2 {
			t.Errorf("Expected 2 pending events, got %d", count)
		}
	})
}

// Test Throttling Dispatcher
func TestThrottlingDispatcher(t *testing.T) {
	t.Run("throttles events to max rate", func(t *testing.T) {
		var count int32
		dispatcher := NewThrottlingDispatcher(50 * time.Millisecond)

		dispatcher.Listen("test.event", &countingListener{counter: &count})

		// Dispatch rapidly
		for i := 0; i < 5; i++ {
			dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
			time.Sleep(10 * time.Millisecond)
		}

		// Should only dispatch once (first one within throttle window)
		if atomic.LoadInt32(&count) != 1 {
			t.Errorf("Expected 1 event during throttle window, got %d", atomic.LoadInt32(&count))
		}

		// Wait past throttle window (50ms), then the next dispatch should fire
		time.Sleep(60 * time.Millisecond)

		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
		testsync.EventuallyEqual(t, func() int32 { return atomic.LoadInt32(&count) }, int32(2), 500*time.Millisecond, "throttle releases")
	})

	t.Run("can dispatch checks throttle state", func(t *testing.T) {
		dispatcher := NewThrottlingDispatcher(50 * time.Millisecond)
		event := &simpleEvent{name: "test.event"}

		if !dispatcher.CanDispatch(event) {
			t.Error("Should be able to dispatch initially")
		}

		dispatcher.Dispatch(context.Background(), event)

		if dispatcher.CanDispatch(event) {
			t.Error("Should not be able to dispatch immediately after")
		}

		time.Sleep(60 * time.Millisecond)

		if !dispatcher.CanDispatch(event) {
			t.Error("Should be able to dispatch after throttle expires")
		}
	})

	t.Run("reset clears throttle state", func(t *testing.T) {
		dispatcher := NewThrottlingDispatcher(50 * time.Millisecond)
		event := &simpleEvent{name: "test.event"}

		dispatcher.Dispatch(context.Background(), event)

		if dispatcher.CanDispatch(event) {
			t.Error("Should not be able to dispatch immediately after")
		}

		dispatcher.Reset("test.event")

		if !dispatcher.CanDispatch(event) {
			t.Error("Should be able to dispatch after reset")
		}
	})
}

// Test Rate Limited Dispatcher
func TestRateLimitedDispatcher(t *testing.T) {
	t.Run("enforces rate limit", func(t *testing.T) {
		var count int32
		dispatcher := NewRateLimitedDispatcher(3, 100*time.Millisecond)

		dispatcher.Listen("test.event", &countingListener{counter: &count})

		// Dispatch up to limit
		for i := 0; i < 3; i++ {
			if err := dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"}); err != nil {
				t.Errorf("Unexpected error: %v", err)
			}
		}

		if atomic.LoadInt32(&count) != 3 {
			t.Errorf("Expected 3 events, got %d", atomic.LoadInt32(&count))
		}

		// Next dispatch should fail
		err := dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
		if err != ErrRateLimitExceeded {
			t.Error("Expected rate limit exceeded error")
		}
	})

	t.Run("rate limit resets after window", func(t *testing.T) {
		var count int32
		dispatcher := NewRateLimitedDispatcher(2, 50*time.Millisecond)

		dispatcher.Listen("test.event", &countingListener{counter: &count})

		// Fill the limit
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})

		// Should fail
		err := dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
		if err != ErrRateLimitExceeded {
			t.Error("Expected rate limit exceeded error")
		}

		// Wait for window to expire
		time.Sleep(60 * time.Millisecond)

		// Should succeed now
		err = dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
		if err != nil {
			t.Errorf("Unexpected error after window: %v", err)
		}

		if atomic.LoadInt32(&count) != 3 {
			t.Errorf("Expected 3 events total, got %d", atomic.LoadInt32(&count))
		}
	})

	t.Run("get remaining events returns correct count", func(t *testing.T) {
		dispatcher := NewRateLimitedDispatcher(5, 100*time.Millisecond)

		remaining := dispatcher.GetRemainingEvents("test.event")
		if remaining != 5 {
			t.Errorf("Expected 5 remaining, got %d", remaining)
		}

		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})

		remaining = dispatcher.GetRemainingEvents("test.event")
		if remaining != 3 {
			t.Errorf("Expected 3 remaining, got %d", remaining)
		}
	})
}

// Test Coalescing Dispatcher
func TestCoalescingDispatcher(t *testing.T) {
	t.Run("coalesces rapid identical events", func(t *testing.T) {
		var count int32
		dispatcher := NewCoalescingDispatcher(50 * time.Millisecond)
		defer dispatcher.Stop()

		dispatcher.Listen("test.event", &countingListener{counter: &count})

		// Dispatch same event rapidly
		for i := 0; i < 10; i++ {
			dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
			time.Sleep(10 * time.Millisecond)
		}

		// Wait for coalesce to fire
		testsync.EventuallyEqual(t, func() int32 { return atomic.LoadInt32(&count) }, int32(1), time.Second, "coalesce fires once")
	})

	t.Run("different events not coalesced", func(t *testing.T) {
		var count int32
		dispatcher := NewCoalescingDispatcher(50 * time.Millisecond)
		defer dispatcher.Stop()

		dispatcher.Listen("test.event1", &countingListener{counter: &count})
		dispatcher.Listen("test.event2", &countingListener{counter: &count})

		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event1"})
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event2"})

		testsync.EventuallyEqual(t, func() int32 { return atomic.LoadInt32(&count) }, int32(2), time.Second, "different events both fire")
	})

	t.Run("get coalesced count tracks coalescence", func(t *testing.T) {
		dispatcher := NewCoalescingDispatcher(100 * time.Millisecond)
		defer dispatcher.Stop()

		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})

		time.Sleep(10 * time.Millisecond)

		count := dispatcher.GetCoalescedCount("test.event")
		if count != 3 {
			t.Errorf("Expected coalesced count 3, got %d", count)
		}
	})
}

// Test Concurrent Operations
func TestBatchingConcurrency(t *testing.T) {
	t.Run("concurrent batching dispatches", func(t *testing.T) {
		var count int32
		dispatcher := NewBatchingDispatcher(10, 100*time.Millisecond)
		dispatcher.Start()
		defer dispatcher.Stop()

		dispatcher.Listen("test.event", &countingListener{counter: &count})

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
			}()
		}

		wg.Wait()
		dispatcher.Flush()

		if atomic.LoadInt32(&count) != 100 {
			t.Errorf("Expected 100 events, got %d", atomic.LoadInt32(&count))
		}
	})

	t.Run("concurrent rate limited dispatches", func(t *testing.T) {
		var count int32
		dispatcher := NewRateLimitedDispatcher(50, 1*time.Second)

		dispatcher.Listen("test.event", &countingListener{counter: &count})

		var wg sync.WaitGroup
		for i := 0; i < 100; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
			}()
		}

		wg.Wait()

		// Should be limited to 50
		dispatched := atomic.LoadInt32(&count)
		if dispatched > 50 {
			t.Errorf("Expected at most 50 events, got %d", dispatched)
		}
	})
}

// Test Helpers
type countingListener struct {
	counter *int32
}

func (l *countingListener) Handle(ctx context.Context, event interface{}) error {
	atomic.AddInt32(l.counter, 1)
	return nil
}

func (l *countingListener) ShouldQueue() bool {
	return false
}

type simpleEvent struct {
	name string
}

func (e *simpleEvent) Name() string {
	return e.name
}

// Benchmarks
func BenchmarkBatchingDispatcher(b *testing.B) {
	dispatcher := NewBatchingDispatcher(100, 100*time.Millisecond)
	dispatcher.Start()
	defer dispatcher.Stop()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
	}
	dispatcher.Flush()
}

func BenchmarkDebouncingDispatcher(b *testing.B) {
	dispatcher := NewDebouncingDispatcher(10 * time.Millisecond)
	defer dispatcher.Stop()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
	}
}

func BenchmarkThrottlingDispatcher(b *testing.B) {
	dispatcher := NewThrottlingDispatcher(1 * time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
	}
}

func BenchmarkRateLimitedDispatcher(b *testing.B) {
	dispatcher := NewRateLimitedDispatcher(1000000, 1*time.Second)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
	}
}

func BenchmarkCoalescingDispatcher(b *testing.B) {
	dispatcher := NewCoalescingDispatcher(10 * time.Millisecond)
	defer dispatcher.Stop()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		dispatcher.Dispatch(context.Background(), &simpleEvent{name: "test.event"})
	}
}
