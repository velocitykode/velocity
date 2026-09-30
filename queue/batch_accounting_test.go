package queue

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// batchEventLog records the batch events the process-wide dispatcher
// receives.
type batchEventLog struct {
	mu     sync.Mutex
	events []any
}

func (l *batchEventLog) dispatch(_ context.Context, event any) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
	return nil
}

func (l *batchEventLog) completed(id BatchID) []*BatchCompleted {
	l.mu.Lock()
	defer l.mu.Unlock()
	var out []*BatchCompleted
	for _, e := range l.events {
		if c, ok := e.(*BatchCompleted); ok && c.BatchID == string(id) {
			out = append(out, c)
		}
	}
	return out
}

func (l *batchEventLog) has(match func(any) bool) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, e := range l.events {
		if match(e) {
			return true
		}
	}
	return false
}

// globalBatchLog installs a recording process-wide batch dispatcher for
// the test.
func globalBatchLog(t *testing.T) *batchEventLog {
	t.Helper()
	log := &batchEventLog{}
	SetGlobalEventDispatcher(log.dispatch, nil, nil)
	t.Cleanup(func() { SetGlobalEventDispatcher(nil, nil, nil) })
	return log
}

// A batch's own dispatcher (WithEventDispatcher) that panics on
// BatchJobFailed is contained: the failure is recorded once in the batch
// events' failure policy, and the process-wide delivery, the batch's
// terminal callbacks and BatchCompleted still happen, with nothing
// unwinding into the worker that recorded the failure.
func TestBatch_PanickingLocalDispatcherIsContained(t *testing.T) {
	resetBatchStoreForTest(t)
	global := globalBatchLog(t)
	finally := make(chan struct{})
	code := hostile.New(t, hostile.Panic, nil)
	batch, err := NewBatch(&testBatchJob{}).
		AllowFailures().
		Finally(func(*Batch) { close(finally) }).
		WithEventDispatcher(func(_ context.Context, event interface{}) {
			if _, ok := event.(*BatchJobFailed); ok {
				code.Run()
			}
		}).
		Dispatch(context.Background(), newMemoryDriver())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	before := globalBatchEvents.FailureCount()

	if p := hostile.Within(t, hostile.Deadline, func() {
		batch.recordFailure(context.Background(), errors.New("job broke"))
	}); p != nil {
		t.Fatalf("the local dispatcher's panic escaped recordFailure: %v", p)
	}
	if code.Calls() != 1 {
		t.Fatalf("the local dispatcher ran %d times on BatchJobFailed, want 1", code.Calls())
	}
	if !global.has(func(e any) bool { _, ok := e.(*BatchJobFailed); return ok }) {
		t.Error("the process-wide dispatcher did not receive BatchJobFailed")
	}
	if got := len(global.completed(batch.ID())); got != 1 {
		t.Errorf("BatchCompleted dispatched %d times, want 1", got)
	}
	hostile.Within(t, hostile.Deadline, func() { <-finally })
	if got := globalBatchEvents.FailureCount() - before; got != 1 {
		t.Errorf("failures recorded = %d, want 1 (the local dispatcher's panic)", got)
	}
}

// A batch's own dispatcher that blocks or calls back into the batch never
// holds the batch's state: while it runs, another job of the same batch
// records its outcome and the batch's accessors answer.
func TestBatch_LocalDispatcherBlockingOrReentering(t *testing.T) {
	for _, mode := range []hostile.Mode{hostile.Block, hostile.Reenter} {
		t.Run(mode.String(), func(t *testing.T) {
			resetBatchStoreForTest(t)
			var batch *Batch
			code := hostile.New(t, mode, func() {
				batch.Cancel()
				_ = batch.Progress()
				_ = batch.FailedJobs()
			})
			var err error
			batch, err = NewBatch(&testBatchJob{}, &testBatchJob{}).
				AllowFailures().
				WithEventDispatcher(func(_ context.Context, event interface{}) {
					if _, ok := event.(*BatchJobFailed); ok {
						code.Run()
					}
				}).
				Dispatch(context.Background(), newMemoryDriver())
			if err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			done := make(chan struct{})
			go func() {
				defer close(done)
				batch.recordFailure(context.Background(), errors.New("job broke"))
			}()
			code.AwaitEntered(t)
			hostile.Within(t, hostile.Deadline, func() {
				batch.recordSuccess(context.Background())
				_ = batch.Progress()
				_ = batch.Finished()
			})
			code.Release()
			hostile.Within(t, hostile.Deadline, func() { <-done })
			if got, want := batch.CompletedJobs()+batch.FailedJobs(), 2; got != want {
				t.Errorf("processed = %d, want %d", got, want)
			}
		})
	}
}
