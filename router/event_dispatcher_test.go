package router

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventemit"
)

func TestAsyncEventDispatcher_DeliversAllEvents(t *testing.T) {
	r := NewV2()

	var mu sync.Mutex
	var received []string
	r.SetAsyncEventDispatcher(func(_ context.Context, event interface{}) error {
		mu.Lock()
		received = append(received, event.(string))
		mu.Unlock()
		return nil
	}, 4, 64)

	for i := 0; i < 50; i++ {
		if err := r.events.Dispatcher()(context.Background(), "evt"); err != nil {
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
	r.SetAsyncEventDispatcher(func(_ context.Context, event interface{}) error {
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
		if err := r.events.Dispatcher()(context.Background(), i); errors.Is(err, ErrEventBufferFull) {
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
	r.SetAsyncEventDispatcher(func(_ context.Context, event interface{}) error {
		atomic.AddInt64(&processed, 1)
		if event.(int)%2 == 0 {
			panic("boom")
		}
		return nil
	}, 2, 16)

	for i := 0; i < 10; i++ {
		_ = r.events.Dispatcher()(context.Background(), i)
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
	r.SetAsyncEventDispatcher(func(_ context.Context, event interface{}) error { return nil }, 1, 4)

	if err := r.ShutdownEventDispatcher(context.Background()); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	// Second call must not panic on the closed channel.
	if err := r.ShutdownEventDispatcher(context.Background()); err != nil {
		t.Errorf("second shutdown: %v", err)
	}
}

// TestDispatchInstanceEvent_DropsCountedAndHookInvoked verifies the
// observability surface for dispatcher errors (the fix for the silent-drop
// failure mode introduced by SetAsyncEventDispatcher): every drop is
// counted in the Failures the app shares with the router and handed to its
// hook.
func TestDispatchInstanceEvent_DropsCountedAndHookInvoked(t *testing.T) {
	r := NewV2()

	// Install a dispatcher that always returns ErrEventBufferFull.
	r.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		return ErrEventBufferFull
	})

	var seen []any
	var seenErr error
	failures := &eventemit.Failures{}
	failures.SetHook(func(err error, event any) {
		seenErr = err
		seen = append(seen, event)
	})
	r.ShareEventFailures(failures)

	for i := 0; i < 7; i++ {
		emitEvent(r, context.Background(), &RequestRouted{RequestID: "id"})
	}

	if got, want := failures.Count(), uint64(7); got != want {
		t.Errorf("failed event count = %d, want %d", got, want)
	}
	if len(seen) != 7 {
		t.Errorf("failure hook invoked %d times, want 7", len(seen))
	}
	if !errors.Is(seenErr, ErrEventBufferFull) {
		t.Errorf("hook err = %v, want ErrEventBufferFull", seenErr)
	}
}

// TestDispatchInstanceEvent_StandaloneCountsItsOwnDrops ensures a router
// no app shares Failures with (no hook, no logger wired) still counts
// drops in its own count and exits cleanly without a nil dereference.
func TestDispatchInstanceEvent_StandaloneCountsItsOwnDrops(t *testing.T) {
	r := NewV2()
	r.SetEventDispatcher(func(_ context.Context, event interface{}) error { return ErrEventBufferFull })

	emitEvent(r, context.Background(), &RequestStarted{})
	emitEvent(r, context.Background(), &RequestHandled{})

	if got := r.events.FailureCount(); got != 2 {
		t.Errorf("failed event count = %d, want 2", got)
	}
}

// TestShareEventFailures_Nil asserts ShareEventFailures(nil) is safe and
// returns the router to its own count: drops recorded after it no longer
// reach the Failures shared before.
func TestShareEventFailures_Nil(t *testing.T) {
	r := NewV2()
	r.ShareEventFailures(nil)
	r.SetEventDispatcher(func(context.Context, interface{}) error { return ErrEventBufferFull })
	emitEvent(r, context.Background(), &RequestStarted{})
	if got := r.events.FailureCount(); got != 1 {
		t.Fatalf("own count after ShareEventFailures(nil) = %d, want 1", got)
	}

	shared := &eventemit.Failures{}
	r.ShareEventFailures(shared)
	emitEvent(r, context.Background(), &RequestStarted{})
	r.ShareEventFailures(nil)
	emitEvent(r, context.Background(), &RequestStarted{})
	if shared.Count() != 1 || r.events.FailureCount() != 2 {
		t.Errorf("shared = %d, own = %d; want 1 and 2", shared.Count(), r.events.FailureCount())
	}
}

// TestEventFailures_ConcurrentSetAndShareWhileDispatching races
// SetEventDispatcher, ShareEventFailures and the hook's installation
// against request events dispatched from many goroutines (run under
// -race): every failed dispatch is recorded exactly once, in whichever
// Failures the router held when the failure happened.
func TestEventFailures_ConcurrentSetAndShareWhileDispatching(t *testing.T) {
	r := NewV2()
	a, b := &eventemit.Failures{}, &eventemit.Failures{}
	var failed atomic.Uint64
	fail := func(context.Context, interface{}) error { failed.Add(1); return ErrEventBufferFull }
	r.SetEventDispatcher(fail)
	r.ShareEventFailures(a)

	stop := make(chan struct{})
	var configurer sync.WaitGroup
	configurer.Add(1)
	go func() {
		defer configurer.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			r.SetEventDispatcher(fail)
			if i%2 == 0 {
				r.ShareEventFailures(b)
				b.SetHook(func(error, any) {})
			} else {
				r.ShareEventFailures(a)
				b.SetHook(nil)
			}
		}
	}()

	const senders, perSender = 16, 300
	var wg sync.WaitGroup
	for g := 0; g < senders; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perSender; i++ {
				emitEvent(r, context.Background(), &RequestHandled{})
			}
		}()
	}
	wg.Wait()
	close(stop)
	configurer.Wait()

	if got, want := a.Count()+b.Count(), failed.Load(); got != want || want != senders*perSender {
		t.Errorf("recorded %d failures for %d failed dispatches (want %d, each exactly once)", got, want, senders*perSender)
	}
}

// TestAsyncEventDispatcher_RecordedFailureCountedOnce asserts a listener
// failure the app's dispatch function already recorded is not recorded a
// second time when the async pool hands it to the router's policy: the
// app's count grows by one per failed event, not two.
func TestAsyncEventDispatcher_RecordedFailureCountedOnce(t *testing.T) {
	app := &eventemit.Failures{}
	var hooked atomic.Int32
	app.SetHook(func(error, any) { hooked.Add(1) })
	r := NewV2()
	r.ShareEventFailures(app)
	r.SetAsyncEventDispatcher(app.Recording(func(context.Context, any) error {
		return errors.New("listener failed")
	}, nil), 2, 64)

	const n = 10
	for i := 0; i < n; i++ {
		emitEvent(r, context.Background(), &RequestHandled{})
	}
	if err := r.ShutdownEventDispatcher(context.Background()); err != nil {
		t.Fatalf("ShutdownEventDispatcher: %v", err)
	}
	if got := app.Count(); got != n {
		t.Errorf("app count = %d, want %d (one per failed event)", got, n)
	}
	if got := hooked.Load(); got != n {
		t.Errorf("hook calls = %d, want %d", got, n)
	}
}

// emitEvent hands the ready-built event to r's dispatcher as the request
// event helpers do.
func emitEvent(r *VelocityRouterV2, ctx context.Context, event contract.Event) {
	r.events.EmitBuilt(ctx, func() any { return event })
}
