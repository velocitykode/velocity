package orm

import (
	"context"
	"sync"
	"testing"
	"time"
)

// Shutdown racing the first SetEventDispatcher must never leave the pump's
// goroutines running: either the installation comes first and Shutdown
// stops the pump, or Shutdown comes first and no pump starts. Run under
// -race; the interleaving that leaked (Shutdown taking its pump snapshot,
// then the whole installation, then Shutdown marking the manager closed)
// is narrow, so it races many managers.
func TestShutdown_RacingTheFirstDispatcherLeavesNoPumpRunning(t *testing.T) {
	const rounds = 5000
	leaked := 0
	for range rounds {
		m := &Manager{}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Go(func() {
			<-start
			m.SetEventDispatcher(func(context.Context, any) error { return nil })
		})
		wg.Go(func() {
			<-start
			if err := m.Shutdown(context.Background()); err != nil {
				t.Errorf("Shutdown: %v", err)
			}
		})
		close(start)
		wg.Wait()

		p := m.pump.Load()
		if p == nil {
			continue
		}
		if !p.stopped.Load() {
			leaked++
			_ = p.stop(context.Background())
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		if err := p.awaitExit(ctx); err != nil {
			t.Errorf("a stopped pump's goroutines did not exit: %v", err)
		}
		cancel()
	}
	if leaked > 0 {
		t.Fatalf("%d of %d managers were shut down with their pump still running", leaked, rounds)
	}
}
