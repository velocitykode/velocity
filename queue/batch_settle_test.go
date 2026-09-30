package queue

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// When jobs of a batch fail while others succeed at the same time, the
// batch's terminal state always counts every failure: Then never fires,
// and BatchCompleted carries the complete counters. Run under -race.
func TestBatch_ConcurrentSuccessAndFailureCountTheFailureBeforeTheEnd(t *testing.T) {
	resetBatchStoreForTest(t)
	global := globalBatchLog(t)
	const rounds, perKind = 200, 32
	var thenFired atomic.Int32
	var finallies sync.WaitGroup
	batches := make([]*Batch, rounds)
	for i := range rounds {
		finallies.Add(1)
		jobs := make([]Job, 2*perKind)
		for j := range jobs {
			jobs[j] = &testBatchJob{}
		}
		b, err := NewBatch(jobs...).
			AllowFailures().
			Then(func(*Batch) { thenFired.Add(1) }).
			Finally(func(*Batch) { finallies.Done() }).
			Dispatch(context.Background(), newMemoryDriver())
		if err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		batches[i] = b
		start := make(chan struct{})
		var wg sync.WaitGroup
		for j := range 2 * perKind {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				if j%2 == 0 {
					b.recordSuccess(context.Background())
				} else {
					b.recordFailure(context.Background(), errors.New("job broke"))
				}
			}()
		}
		close(start)
		wg.Wait()
	}
	hostile.Within(t, hostile.Deadline, finallies.Wait)
	for _, b := range batches {
		done := global.completed(b.ID())
		if len(done) != 1 {
			t.Fatalf("batch %s: BatchCompleted dispatched %d times, want 1", b.ID(), len(done))
		}
		c := done[0]
		if c.CompletedJobs != perKind || c.FailedJobs != perKind || !c.HasFailures {
			t.Fatalf("batch %s: BatchCompleted{Completed: %d, Failed: %d, HasFailures: %v}, want %d, %d, true",
				b.ID(), c.CompletedJobs, c.FailedJobs, c.HasFailures, perKind, perKind)
		}
	}
	if got := thenFired.Load(); got != 0 {
		t.Errorf("Then fired for %d batches with failures", got)
	}
}
