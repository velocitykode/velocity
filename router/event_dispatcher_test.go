package router

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestAsyncEventDispatcher_DeliversAllEvents(t *testing.T) {
	r := NewV2()

	var mu sync.Mutex
	var received []string
	r.SetAsyncEventDispatcher(func(event interface{}) error {
		mu.Lock()
		received = append(received, event.(string))
		mu.Unlock()
		return nil
	}, 4, 64)

	for i := 0; i < 50; i++ {
		if err := r.eventDispatcher("evt"); err != nil {
			t.Fatalf("dispatch[%d] returned %v", i, err)
		}
	}

	if err := r.ShutdownEventDispatcher(context.Background()); err != nil {
		t.Fatalf("ShutdownEventDispatcher: %v", err)
	}

	mu.Lock()
	count := len(received)
	mu.Unlock()
	if count != 50 {
		t.Errorf("listener received %d events, want 50", count)
	}
}

func TestAsyncEventDispatcher_DoesNotBlockCaller(t *testing.T) {
	r := NewV2()

	// Listener blocks until released — guarantees the buffer fills.
	release := make(chan struct{})
	r.SetAsyncEventDispatcher(func(event interface{}) error {
		<-release
		return nil
	}, 1, 4)

	// Dispatch far more than the buffer can hold. Whether the worker has
	// pulled the first event or not, the buffer fills within a handful of
	// iterations and the rest must drop without blocking.
	const N = 100
	var dropped int
	start := time.Now()
	for i := 0; i < N; i++ {
		if err := r.eventDispatcher(i); errors.Is(err, ErrEventBufferFull) {
			dropped++
		}
	}
	elapsed := time.Since(start)

	if dropped == 0 {
		t.Errorf("expected some dispatches to drop when buffer full; got zero drops")
	}
	// 100 non-blocking sends should be effectively instant.
	if elapsed > 50*time.Millisecond {
		t.Errorf("100 dispatches took %v; async dispatcher blocked the caller", elapsed)
	}

	close(release)
	if err := r.ShutdownEventDispatcher(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestAsyncEventDispatcher_RecoversListenerPanics(t *testing.T) {
	r := NewV2()

	var processed int64
	r.SetAsyncEventDispatcher(func(event interface{}) error {
		atomic.AddInt64(&processed, 1)
		if event.(int)%2 == 0 {
			panic("boom")
		}
		return nil
	}, 2, 16)

	for i := 0; i < 10; i++ {
		_ = r.eventDispatcher(i)
	}

	if err := r.ShutdownEventDispatcher(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}

	// All events reached the listener; panics did not kill workers.
	if got := atomic.LoadInt64(&processed); got != 10 {
		t.Errorf("processed = %d, want 10 (panics must not kill workers)", got)
	}
}

func TestShutdownEventDispatcher_NoopWhenSync(t *testing.T) {
	r := NewV2()
	// Never set an async dispatcher.
	if err := r.ShutdownEventDispatcher(context.Background()); err != nil {
		t.Errorf("expected nil, got %v", err)
	}
}

func TestShutdownEventDispatcher_SecondCallIsNoop(t *testing.T) {
	r := NewV2()
	r.SetAsyncEventDispatcher(func(event interface{}) error { return nil }, 1, 4)

	if err := r.ShutdownEventDispatcher(context.Background()); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	// Second call must not panic on the closed channel.
	if err := r.ShutdownEventDispatcher(context.Background()); err != nil {
		t.Errorf("second shutdown: %v", err)
	}
}

// TestDispatchInstanceEvent_DropsCountedAndCallbackInvoked verifies the
// observability surface for dispatcher errors — the fix for the silent-drop
// failure mode introduced by SetAsyncEventDispatcher.
func TestDispatchInstanceEvent_DropsCountedAndCallbackInvoked(t *testing.T) {
	r := NewV2()

	// Install a dispatcher that always returns ErrEventBufferFull.
	r.SetEventDispatcher(func(event interface{}) error {
		return ErrEventBufferFull
	})

	var seen []interface{}
	var seenErr error
	r.OnEventDispatchError = func(err error, event interface{}) {
		seenErr = err
		seen = append(seen, event)
	}

	for i := 0; i < 7; i++ {
		r.dispatchInstanceEvent(i)
	}

	if got, want := r.DroppedEventCount(), uint64(7); got != want {
		t.Errorf("DroppedEventCount = %d, want %d", got, want)
	}
	if len(seen) != 7 {
		t.Errorf("OnEventDispatchError invoked %d times, want 7", len(seen))
	}
	if !errors.Is(seenErr, ErrEventBufferFull) {
		t.Errorf("callback err = %v, want ErrEventBufferFull", seenErr)
	}
}

// TestDispatchInstanceEvent_NoCallbackDoesNotPanic ensures the default
// (no OnEventDispatchError, no services.Log wired) still counts drops
// and exits cleanly without a nil dereference.
func TestDispatchInstanceEvent_NoCallbackDoesNotPanic(t *testing.T) {
	r := NewV2()
	r.SetEventDispatcher(func(event interface{}) error { return ErrEventBufferFull })

	r.dispatchInstanceEvent("a")
	r.dispatchInstanceEvent("b")

	if got := r.DroppedEventCount(); got != 2 {
		t.Errorf("DroppedEventCount = %d, want 2", got)
	}
}
