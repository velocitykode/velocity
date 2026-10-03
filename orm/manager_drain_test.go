package orm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/drain/draintest"
)

// TestManager_DrainContract runs the drain owner contract: the manager's
// work in flight is the statement-event delivery, a query arriving once
// Shutdown began is refused, and a Shutdown from a listener is refused.
func TestManager_DrainContract(t *testing.T) {
	draintest.Run(t, func(t *testing.T) draintest.Owner {
		m := newTestManager(t)
		return draintest.Owner{
			Hold: func(t *testing.T) func() { return blockPump(t, m, 0) },
			Stop: m.Shutdown,
			Refused: func(t *testing.T) bool {
				_, err := m.Exec(context.Background(), "SELECT 1")
				return errors.Is(err, ErrManagerShutdown)
			},
			StopFromOwnWork: func(t *testing.T) error {
				var stopErr error
				var once sync.Once
				done := make(chan struct{})
				m.SetEventDispatcher(func(context.Context, any) error {
					once.Do(func() {
						stopErr = m.Shutdown(context.Background())
						close(done)
					})
					return nil
				})
				if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
					t.Fatalf("exec: %v", err)
				}
				<-done
				return stopErr
			},
		}
	})
}

// The drivers close only once the queued statement events were delivered:
// a Shutdown whose ctx ends while a listener is held returns ctx's error
// and leaves the connections to the drain, which closes them after the
// listener returned.
func TestManagerShutdown_ClosesTheDriversAfterTheDelivery(t *testing.T) {
	m := newTestManager(t)
	release := blockPump(t, m, 2)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := m.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown with a listener held = %v, want context.DeadlineExceeded", err)
	}
	m.mu.RLock()
	open := m.defaultDriver != nil
	m.mu.RUnlock()
	if !open {
		t.Fatal("the drivers were taken for closing while the delivery still ran")
	}
	release()
	within(t, 2*time.Second, "Shutdown after the listener returned", func() {
		if err := m.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown = %v, want nil", err)
		}
	})
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.defaultDriver != nil {
		t.Error("the default driver was not closed by the finished Shutdown")
	}
	// A finished Shutdown means the pump's goroutines returned.
	p := m.pump.Load()
	for _, exited := range []chan struct{}{p.delivered, p.reported} {
		select {
		case <-exited:
		default:
			t.Error("Shutdown finished while a statement-event pump goroutine still ran")
		}
	}
}
