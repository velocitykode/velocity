package grpc

import (
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// TestServer_ImplementsShutdownAware documents intent at the test layer;
// the canonical enforcement is the compile-time assertion in
// event_dispatcher_aware.go at the repo root.
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
	s.SetEventDispatcher(func(event any) error { return nil })
	s.SetEventDispatcher(nil)
	s.SetEventDispatcher(func(event any) error { return nil })

	// dispatchEvent with nil-cleared dispatcher must be a no-op (no panic).
	s.SetEventDispatcher(nil)
	s.dispatchEvent("noop")

	// Concurrent recalls + reads via dispatchEvent. The race detector
	// catches a missing lock on either side.
	var seen atomic.Int64
	dispatcher := func(event any) error {
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
				s.dispatchEvent("probe")
			}
		}()
	}
	wg.Wait()

	// Final state: a non-nil dispatcher should still be able to fire.
	s.SetEventDispatcher(dispatcher)
	before := seen.Load()
	s.dispatchEvent("final")
	if seen.Load() != before+1 {
		t.Fatalf("dispatchEvent did not invoke dispatcher: before=%d after=%d", before, seen.Load())
	}
}
