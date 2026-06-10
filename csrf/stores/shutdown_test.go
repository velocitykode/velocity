package stores

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestSessionStore_Shutdown_RespectsContextDeadline(t *testing.T) {
	store := NewSessionStore()
	store.Start(context.Background())

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := store.Shutdown(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context.Canceled, got %v", err)
	}
}

func TestSessionStore_Shutdown_Idempotent(t *testing.T) {
	store := NewSessionStore()
	store.Start(context.Background())

	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatalf("first Shutdown: %v", err)
	}
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatalf("second Shutdown: %v", err)
	}
}

func TestSessionStore_Shutdown_BeforeStart(t *testing.T) {
	store := NewSessionStore()
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown before Start: %v", err)
	}
}

// TestSessionStore_DoubleStart_NoLeak pins the regression where Start
// overwrote s.cancel without cancelling the previous cleanup goroutine,
// leaking one goroutine per extra Start call.
func TestSessionStore_DoubleStart_NoLeak(t *testing.T) {
	before := runtime.NumGoroutine()

	store := NewSessionStore()
	store.Start(context.Background())
	store.Start(context.Background())

	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	// Cleanup goroutines exit on ctx.Done; poll until the count returns
	// to the pre-test baseline.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if runtime.NumGoroutine() <= before {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("goroutine leak after double Start + Shutdown: before=%d after=%d", before, runtime.NumGoroutine())
}

func TestSessionStore_ConcurrentStartShutdown(t *testing.T) {
	store := NewSessionStore()

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			store.Start(context.Background())
		}()
		go func() {
			defer wg.Done()
			_ = store.Shutdown(context.Background())
		}()
	}
	wg.Wait()

	// A Start may have raced in last; final Shutdown reaps it.
	if err := store.Shutdown(context.Background()); err != nil {
		t.Fatalf("final Shutdown: %v", err)
	}
}
