package grpc

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// TestServer_ImplementsShutdownAware documents intent at the test layer;
// the canonical enforcement is the compile-time assertion in server.go.
func TestServer_ImplementsShutdownAware(t *testing.T) {
	var _ contract.ShutdownAware = (*Server)(nil)
}

// TestServer_ImplementsEventDispatcherAware mirrors the ShutdownAware
// check for the event-dispatcher contract.
func TestServer_ImplementsEventDispatcherAware(t *testing.T) {
	var _ contract.EventDispatcherAware = (*Server)(nil)
}

// TestServer_SetEventDispatcher_SafeToRecall exercises the mutex guard:
// repeated SetEventDispatcher calls from multiple goroutines, interleaved
// with dispatchEvent calls, must not race. Run under `-race` to confirm.
func TestServer_SetEventDispatcher_SafeToRecall(t *testing.T) {
	s := NewServer()

	// Calling twice on a fresh server must not panic.
	s.SetEventDispatcher(func(_ context.Context, event any) error { return nil })
	s.SetEventDispatcher(nil)
	s.SetEventDispatcher(func(_ context.Context, event any) error { return nil })

	// dispatchEvent with nil-cleared dispatcher must be a no-op (no panic).
	s.SetEventDispatcher(nil)
	s.dispatchEvent(context.Background(), "noop")

	// Concurrent recalls + reads via dispatchEvent. The race detector
	// catches a missing lock on either side.
	var seen atomic.Int64
	dispatcher := func(_ context.Context, event any) error {
		seen.Add(1)
		return nil
	}

	const writers, readers, iters = 4, 4, 200
	var wg sync.WaitGroup
	wg.Add(writers + readers)

	for range writers {
		go func() {
			defer wg.Done()
			for range iters {
				s.SetEventDispatcher(dispatcher)
				s.SetEventDispatcher(nil)
			}
		}()
	}
	for range readers {
		go func() {
			defer wg.Done()
			for range iters {
				s.dispatchEvent(context.Background(), "probe")
			}
		}()
	}
	wg.Wait()

	// Final state: a non-nil dispatcher should still be able to fire.
	s.SetEventDispatcher(dispatcher)
	before := seen.Load()
	s.dispatchEvent(context.Background(), "final")
	if seen.Load() != before+1 {
		t.Fatalf("dispatchEvent did not invoke dispatcher: before=%d after=%d", before, seen.Load())
	}
}

// TestServer_LifecycleEvents locks the start/stop emission contract:
// StartAsync emits exactly one ServerStarted before serving, GracefulStop
// emits exactly one ServerStopped carrying the uptime, and a second stop
// path (Shutdown, which delegates to GracefulStop) emits nothing further.
// Run under -race: emission interleaves with the serve goroutine.
func TestServer_LifecycleEvents(t *testing.T) {
	s := NewServer(WithPort("0"))

	var mu sync.Mutex
	var events []any
	s.SetEventDispatcher(func(_ context.Context, event any) error {
		mu.Lock()
		defer mu.Unlock()
		events = append(events, event)
		return nil
	})

	if err := s.StartAsync(); err != nil {
		t.Fatalf("StartAsync: %v", err)
	}

	waitFor := func(cond func() bool, what string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !cond() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s", what)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	waitFor(s.IsRunning, "IsRunning")
	waitFor(func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(events) > 0
	}, "ServerStarted emission")

	s.GracefulStop()

	// Second stop path must not double-emit ServerStopped.
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 {
		t.Fatalf("expected exactly 2 lifecycle events, got %d: %#v", len(events), events)
	}
	started, ok := events[0].(*ServerStarted)
	if !ok {
		t.Fatalf("first event: got %T, want *ServerStarted", events[0])
	}
	if started.Port != s.Port() {
		t.Errorf("ServerStarted.Port: got %q want %q", started.Port, s.Port())
	}
	if started.StartTime.IsZero() {
		t.Error("ServerStarted.StartTime is zero")
	}
	stopped, ok := events[1].(*ServerStopped)
	if !ok {
		t.Fatalf("second event: got %T, want *ServerStopped", events[1])
	}
	if stopped.Duration <= 0 {
		t.Errorf("ServerStopped.Duration: got %v, want > 0", stopped.Duration)
	}
	if stopped.StopTime.IsZero() {
		t.Error("ServerStopped.StopTime is zero")
	}
}
