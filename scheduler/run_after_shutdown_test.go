package scheduler

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// Regression: Shutdown before Run was a no-op (!running early return),
// so a Run that lost the start race (the in-process scheduler goroutine
// spawned by Serve, with ListenAndServe failing fast and the app
// tearing down) started fresh AFTER teardown and ticked forever against
// closed services. Shutdown now marks the scheduler terminated even
// when it never started, and Run refuses to start once terminated.
func TestScheduler_RunAfterShutdown_DoesNotStart(t *testing.T) {
	s := New()

	var fired atomic.Bool
	s.Call(func() { fired.Store(true) }).EveryMinute()

	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown before Run: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- s.Run(context.Background()) }()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run after Shutdown returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after Shutdown; scheduler started post-teardown")
	}

	// Run's start path dispatches due jobs immediately; give a stray
	// dispatch a moment to surface before asserting nothing fired.
	time.Sleep(50 * time.Millisecond)
	if fired.Load() {
		t.Fatal("job fired from a Run that started after Shutdown")
	}

	s.mu.RLock()
	running := s.running
	s.mu.RUnlock()
	if running {
		t.Fatal("scheduler reports running after a post-Shutdown Run")
	}
}

// A second Shutdown (or a Shutdown racing Run's own ctx-cancel Shutdown)
// must stay a clean no-op.
func TestScheduler_ShutdownTwice_NoOp(t *testing.T) {
	s := New()
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}
