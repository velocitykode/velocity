package queue

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/hostile"
)

// The queue.job.failed dispatch is user code run by the worker after a job
// fails for good. A dispatcher that panics on it is contained like one that
// returns an error: the panic reaches the failure policy as a
// contract.RecoveredPanic, once, and the worker goes on to run the next job.
func TestWorker_PanickingFailedDispatchIsContained(t *testing.T) {
	const queueName = "failed-dispatch-panic"
	d := newStartedMemoryDriver(t)
	code := hostile.New(t, hostile.Panic, nil)
	var ran atomic.Int64
	w := NewWorker(d, queueName, func(j Job) error {
		ran.Add(1)
		return j.Handle()
	},
		WithInterval(5*time.Millisecond), WithMaxRetries(1),
		WithBackoff(func(int) time.Duration { return time.Millisecond }),
		WithWorkerLogger(&levelLogger{}))
	w.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		if _, ok := event.(*JobFailed); ok {
			code.Run()
		}
		return nil
	})
	failures := &eventemit.Failures{}
	var recorded atomic.Int64
	var lastErr atomic.Pointer[error]
	failures.SetHook(func(err error, _ any) {
		recorded.Add(1)
		lastErr.Store(&err)
	})
	w.events.Share(failures)

	failing := &plainFailingJob{ID: fmt.Sprintf("dispatch-panic-%d", logJobSeq.Add(1))}
	if err := d.PushCtx(context.Background(), failing, queueName); err != nil {
		t.Fatalf("push: %v", err)
	}
	w.Start(context.Background())
	t.Cleanup(func() { _ = w.Stop() })

	hostile.Eventually(t, hostile.Deadline, "the failed dispatch's panic recorded", func() bool {
		return recorded.Load() >= 1
	})
	var rp contract.RecoveredPanic
	if p := lastErr.Load(); p == nil || !errors.As(*p, &rp) {
		t.Errorf("recorded failure = %v, want a contract.RecoveredPanic", p)
	}

	before := ran.Load()
	if err := d.PushCtx(context.Background(), &plainFailingJob{ID: fmt.Sprintf("dispatch-next-%d", logJobSeq.Add(1))}, queueName); err != nil {
		t.Fatalf("push next: %v", err)
	}
	hostile.Eventually(t, hostile.Deadline, "the worker running the next job", func() bool {
		return ran.Load() > before && recorded.Load() >= 2
	})
	w.Stop()
	if n := recorded.Load(); n != 2 {
		t.Errorf("recorded failures = %d, want one per failed job", n)
	}
}
