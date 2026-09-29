package queue

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// A Stop called from a worker's pump (a listener of a job event, which
// the pump runs after the job) cannot wait for the pump it runs on: it
// signals the stop and returns an error wrapping contract.ErrStopFromOwnWork
// at once, and a Stop from outside then waits for the drain and returns nil.
func TestWorker_StopFromItsOwnPumpDoesNotWait(t *testing.T) {
	const queueName = "stop-from-pump"
	d := newStartedMemoryDriver(t)
	w := NewWorker(d, queueName, func(j Job) error { return nil },
		WithInterval(5*time.Millisecond), WithWorkerLogger(nullLogger{}))
	var stopErr atomic.Pointer[error]
	returned := make(chan struct{})
	var once atomic.Bool
	w.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		if _, ok := event.(*JobProcessed); ok && once.CompareAndSwap(false, true) {
			err := w.Stop()
			stopErr.Store(&err)
			close(returned)
		}
		return nil
	})
	if err := d.PushCtx(context.Background(), &plainFailingJob{ID: fmt.Sprintf("stop-%d", logJobSeq.Add(1))}, queueName); err != nil {
		t.Fatalf("push: %v", err)
	}
	w.Start(context.Background())

	hostile.Within(t, hostile.Deadline, func() { <-returned })
	if p := stopErr.Load(); p == nil || !errors.Is(*p, contract.ErrStopFromOwnWork) {
		t.Fatalf("Stop from the pump = %v, want an error wrapping contract.ErrStopFromOwnWork", p)
	}
	hostile.Within(t, hostile.Deadline, func() {
		if err := w.Stop(); err != nil {
			t.Errorf("Stop from outside = %v, want nil once drained", err)
		}
	})
}
