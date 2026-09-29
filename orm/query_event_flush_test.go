package orm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A flush reports success only once every statement event admitted before
// it has been delivered, even when a Shutdown that gave up on its own drain
// stopped the pump meanwhile: it keeps waiting for the delivery goroutine
// to finish the events already queued, or returns its ctx's error.
func TestFlushQueryEvents_AfterAnAbandonedShutdownWaitsForDelivery(t *testing.T) {
	for _, order := range []string{"flush after the Shutdown", "flush during the Shutdown"} {
		t.Run(order, func(t *testing.T) {
			m := newTestManager(t)
			var delivered atomic.Int32
			gate := make(chan struct{})
			var once, first sync.Once
			release := func() { once.Do(func() { close(gate) }) }
			defer release()
			entered := make(chan struct{})
			m.SetEventDispatcher(func(context.Context, any) error {
				first.Do(func() { close(entered) })
				<-gate
				delivered.Add(1)
				return nil
			})
			const queued = 4
			for i := 0; i < queued; i++ {
				if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
					t.Fatalf("exec: %v", err)
				}
			}
			<-entered

			flushErr := make(chan error, 1)
			flush := func() {
				ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
				defer cancel()
				flushErr <- m.FlushQueryEvents(ctx)
			}
			shortCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
			defer cancel()
			if order == "flush during the Shutdown" {
				go flush()
				time.Sleep(10 * time.Millisecond)
				_ = m.Shutdown(shortCtx)
			} else {
				_ = m.Shutdown(shortCtx)
				go flush()
			}
			err := <-flushErr
			if err == nil {
				t.Fatalf("flush = nil with %d of %d events delivered: it reported a drain that had not happened", delivered.Load(), queued)
			}
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Errorf("flush = %v, want its ctx's context.DeadlineExceeded", err)
			}

			release()
			within(t, 5*time.Second, "FlushQueryEvents", func() {
				if err := m.FlushQueryEvents(context.Background()); err != nil {
					t.Errorf("flush after release = %v, want nil", err)
				}
			})
			if got := delivered.Load(); got != queued {
				t.Errorf("delivered = %d after a successful flush, want %d", got, queued)
			}
		})
	}
}

// Flush and Shutdown on a manager whose pump never started, and a flush
// after a completed Shutdown, return nil at once.
func TestFlushQueryEvents_WithoutAPumpAndAfterShutdown(t *testing.T) {
	m := newTestManager(t)
	if err := m.FlushQueryEvents(context.Background()); err != nil {
		t.Errorf("flush without a pump = %v, want nil", err)
	}
	m.SetEventDispatcher(func(context.Context, any) error { return nil })
	if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown = %v", err)
	}
	within(t, time.Second, "FlushQueryEvents", func() {
		if err := m.FlushQueryEvents(context.Background()); err != nil {
			t.Errorf("flush after Shutdown = %v, want nil", err)
		}
	})
}
