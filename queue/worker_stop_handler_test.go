package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// stopHooks maps a stopJob's ID to the code its run calls.
var stopHooks sync.Map

// stopJob runs the hook registered under its ID; with ctx set it is a
// HandleCtxer, so the worker runs it with the job's context.
type stopJob struct {
	ID  string `json:"id"`
	Ctx bool   `json:"ctx"`
}

func (j *stopJob) run() error {
	if fn, ok := stopHooks.Load(j.ID); ok {
		fn.(func())()
	}
	return nil
}

func (j *stopJob) Handle() error { return j.run() }
func (j *stopJob) Failed(error)  {}

// stopCtxJob is a stopJob the worker runs through HandleCtx.
type stopCtxJob struct{ stopJob }

func (j *stopCtxJob) HandleCtx(context.Context) error { return j.run() }

func init() {
	RegisterJob(func(data []byte) (*stopJob, error) {
		j := &stopJob{}
		return j, json.Unmarshal(data, j)
	})
	RegisterJob(func(data []byte) (*stopCtxJob, error) {
		j := &stopCtxJob{}
		return j, json.Unmarshal(data, &j.stopJob)
	})
}

// newStopJob returns a job whose run calls fn, a plain one or a HandleCtxer.
func newStopJob(t *testing.T, ctxAware bool, fn func()) Job {
	id := fmt.Sprintf("stop-handler-%d", logJobSeq.Add(1))
	stopHooks.Store(id, fn)
	t.Cleanup(func() { stopHooks.Delete(id) })
	if ctxAware {
		return &stopCtxJob{stopJob{ID: id}}
	}
	return &stopJob{ID: id}
}

// useLongKillCeiling makes the handler drain grace long enough that a stop
// waiting on it shows as a hang past hostile.Deadline.
func useLongKillCeiling(t *testing.T) {
	prev := defaultHandlerKillCeiling
	defaultHandlerKillCeiling = time.Minute
	t.Cleanup(func() { defaultHandlerKillCeiling = prev })
}

func startStopWorker(t *testing.T, d Driver, queueName string) *Worker {
	w := NewWorker(d, queueName, func(j Job) error { return j.Handle() },
		WithInterval(5*time.Millisecond), WithWorkerLogger(nullLogger{}))
	w.Start(context.Background())
	return w
}

// A Stop called from a job's handler, plain or ctx-aware, cannot wait for
// the handler it runs in: it signals the stop and returns an error
// wrapping contract.ErrStopFromOwnWork at once, and a Stop from outside
// then waits for the drain and returns nil.
func TestWorker_StopFromItsHandlerDoesNotWait(t *testing.T) {
	useLongKillCeiling(t)
	for _, ctxAware := range []bool{false, true} {
		t.Run(fmt.Sprintf("ctx-aware=%v", ctxAware), func(t *testing.T) {
			const queueName = "stop-from-handler"
			d := newStartedMemoryDriver(t)
			w := startStopWorker(t, d, queueName)
			var stopErr atomic.Pointer[error]
			returned := make(chan struct{})
			job := newStopJob(t, ctxAware, func() {
				err := w.Stop(context.Background())
				stopErr.Store(&err)
				close(returned)
			})
			if err := d.PushCtx(context.Background(), job, queueName); err != nil {
				t.Fatalf("push: %v", err)
			}
			hostile.Within(t, hostile.Deadline, func() { <-returned })
			if p := stopErr.Load(); p == nil || !errors.Is(*p, contract.ErrStopFromOwnWork) {
				t.Fatalf("Stop from the handler = %v, want an error wrapping contract.ErrStopFromOwnWork", p)
			}
			hostile.Within(t, hostile.Deadline, func() {
				if err := w.Stop(context.Background()); err != nil {
					t.Errorf("Stop from outside = %v, want nil once drained", err)
				}
			})
		})
	}
}

// A handler of one worker that stops another worker is not that worker's
// own work: the Stop waits for the other worker's drain and returns nil.
func TestWorker_StopFromAnotherWorkersHandlerWaits(t *testing.T) {
	d := newStartedMemoryDriver(t)
	other := startStopWorker(t, d, "stop-other")
	t.Cleanup(func() { _ = other.Stop(context.Background()) })
	code := hostile.New(t, hostile.Block, nil)
	var otherDone atomic.Bool
	if err := d.PushCtx(context.Background(), newStopJob(t, false, func() {
		code.Run()
		otherDone.Store(true)
	}), "stop-other"); err != nil {
		t.Fatalf("push: %v", err)
	}
	code.AwaitEntered(t)

	w := startStopWorker(t, d, "stop-caller")
	t.Cleanup(func() { _ = w.Stop(context.Background()) })
	stopping := make(chan struct{})
	var stopErr atomic.Pointer[error]
	var doneAtReturn atomic.Bool
	returned := make(chan struct{})
	if err := d.PushCtx(context.Background(), newStopJob(t, false, func() {
		close(stopping)
		err := other.Stop(context.Background())
		doneAtReturn.Store(otherDone.Load())
		stopErr.Store(&err)
		close(returned)
	}), "stop-caller"); err != nil {
		t.Fatalf("push: %v", err)
	}
	hostile.Within(t, hostile.Deadline, func() { <-stopping })
	code.Release()
	hostile.Within(t, hostile.Deadline, func() { <-returned })
	if err := *stopErr.Load(); err != nil {
		t.Errorf("Stop of another worker from a handler = %v, want nil after its drain", err)
	}
	if !doneAtReturn.Load() {
		t.Error("the Stop returned before the other worker's handler finished")
	}
}

// A Stop from outside whose ctx ends while a handler still runs returns
// ctx's error and the drain goes on; overlapping stops share that drain
// and return nil once the handler returns.
func TestWorker_StopAtItsDeadlineLeavesTheDrainRunning(t *testing.T) {
	useLongKillCeiling(t)
	const queueName = "stop-deadline"
	d := newStartedMemoryDriver(t)
	w := startStopWorker(t, d, queueName)
	code := hostile.New(t, hostile.Block, nil)
	if err := d.PushCtx(context.Background(), newStopJob(t, false, code.Run), queueName); err != nil {
		t.Fatalf("push: %v", err)
	}
	code.AwaitEntered(t)

	hostile.Within(t, hostile.Deadline, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		if err := w.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Stop while a handler blocks = %v, want its deadline", err)
		}
	})
	errs := make(chan error, 2)
	for range 2 {
		go func() { errs <- w.Stop(context.Background()) }()
	}
	code.Release()
	hostile.Within(t, hostile.Deadline, func() {
		for range 2 {
			if err := <-errs; err != nil {
				t.Errorf("overlapping Stop = %v, want nil once drained", err)
			}
		}
	})
}
