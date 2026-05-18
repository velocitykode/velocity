package scheduler

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestEventDispatcher_RaceFree verifies that SetEventDispatcher (writer
// under s.mu.Lock) and dispatchEvent (reader that snapshots under
// s.mu.RLock) do not race under the -race detector.
//
// Pre-fix, dispatchEvent read s.eventDispatcher without holding any lock
// while SetEventDispatcher wrote under s.mu.Lock -- a data race the
// detector flagged immediately under concurrent load.
func TestEventDispatcher_RaceFree(t *testing.T) {
	s := New()

	// A no-op dispatcher and a counter-incrementing dispatcher; both are
	// valid shapes so the writer can swap between them freely without
	// changing test semantics.
	var calls atomic.Int64
	dispatchers := []func(ctx context.Context, event interface{}) error{
		nil,
		func(_ context.Context, _ interface{}) error { return nil },
		func(_ context.Context, _ interface{}) error {
			calls.Add(1)
			return nil
		},
	}

	var wg sync.WaitGroup
	stop := make(chan struct{})

	// Writer: rotates the dispatcher aggressively (including nil) so the
	// reader sees mid-swap states if any unsynchronised access remains.
	wg.Add(1)
	go func() {
		defer wg.Done()
		i := 0
		for {
			select {
			case <-stop:
				return
			default:
				s.SetEventDispatcher(dispatchers[i%len(dispatchers)])
				i++
			}
		}
	}()

	// Internal event firing path: dispatchEvent is the same entry point
	// used by job.go (Run -> dispatchScheduledTaskStarting etc.), so
	// hammering it directly here exercises the exact field that races.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.dispatchEvent(context.Background(), &ScheduledTaskStarting{TaskName: "race"})
			}
		}
	}()

	// Second reader: confirms multiple concurrent readers are fine since
	// the fix uses RLock (shared read), not exclusive Lock.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				s.dispatchEvent(context.Background(), &ScheduledTaskFinished{TaskName: "race"})
			}
		}
	}()

	time.Sleep(50 * time.Millisecond)
	close(stop)
	wg.Wait()

	// Sanity: at least one call should have landed when the counter
	// dispatcher was installed. Not strictly required for the race check
	// but guards against the test silently no-oping.
	if calls.Load() == 0 {
		t.Log("counter dispatcher never observed a call; race window may be too short on this machine (not a failure)")
	}
}
