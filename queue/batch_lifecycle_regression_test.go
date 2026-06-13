package queue

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestBatch_DispatchFirstPushFailure_FiresTerminalCallbacks is a regression
// for B39. When the FIRST push fails, every pending slot is drained inside
// Dispatch itself and no worker will ever observe completion. The drain
// loop previously called repo.DecrementPending with `_, _, _ =`, discarding
// the justFinished signal, so the batch reached its terminal state silently
// and Finally / BatchCompleted never fired. They must fire now.
//
// Callbacks fire in goroutines (async.Go), so we wait on a channel the
// Finally closure closes, with a timeout. Fails before the fix.
func TestBatch_DispatchFirstPushFailure_FiresTerminalCallbacks(t *testing.T) {
	resetBatchStoreForTest(t)
	failDriver := &failingDriver{failOnNth: 1} // fail the very first push

	finallyFired := make(chan struct{})
	var finallyOnce sync.Once

	var completedSeen atomic.Bool
	dispatcher := func(_ context.Context, event interface{}) {
		if _, ok := event.(*BatchCompleted); ok {
			completedSeen.Store(true)
		}
	}

	_, err := NewBatch(&testBatchJob{}, &testBatchJob{}).
		WithEventDispatcher(dispatcher).
		Finally(func(b *Batch) { finallyOnce.Do(func() { close(finallyFired) }) }).
		Dispatch(context.Background(), failDriver)
	if err == nil {
		t.Fatal("expected error when the first push fails")
	}

	select {
	case <-finallyFired:
	case <-time.After(2 * time.Second):
		t.Fatal("Finally never fired: the first-push failure drained the batch to terminal but terminal callbacks were not invoked")
	}

	// BatchCompleted is dispatched synchronously inside fireTerminalCallbacks
	// (which runs on the Dispatch goroutine before it returns), so it is
	// already set by the time Finally's goroutine has run.
	if !completedSeen.Load() {
		t.Error("expected BatchCompleted event to fire on terminal completion after first-push failure")
	}
}

// TestBatch_FinishedFieldAccess_Race is a regression for B40. finishedAt and
// lastError are written under b.mu by Increment*/DecrementPending but were
// read WITHOUT the lock by PruneStale (finishedAt) and FindUndispatchedCallbacks
// (lastError). One goroutine hammers IncrementFailure while another loops
// FindUndispatchedCallbacks and PruneStale on the same batch. Fails under
// -race before the snapshot accessors were introduced.
func TestBatch_FinishedFieldAccess_Race(t *testing.T) {
	resetBatchStoreForTest(t)
	repo := NewInMemoryBatchRepository()
	t.Cleanup(func() { _ = repo.Close() })

	b := &Batch{id: newBatchID(), totalJobs: 1000, catchName: "audit", finallyName: "audit"}
	b.pendingJobs.Store(1000)
	if err := repo.Save(context.Background(), b); err != nil {
		t.Fatalf("save: %v", err)
	}

	ctx := context.Background()
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(2)

	// Writer: drive failures so lastError and finishedAt are written under b.mu.
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-done:
				return
			default:
			}
			_, _, _ = repo.IncrementFailure(ctx, b.id, fmt.Errorf("boom %d", i))
		}
	}()

	// Reader: both readers now route through the snapshot accessors.
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			_, _ = repo.FindUndispatchedCallbacks(ctx, 10)
			_, _ = repo.PruneStale(ctx, time.Hour)
		}
	}()

	time.Sleep(100 * time.Millisecond)
	close(done)
	wg.Wait()
}
