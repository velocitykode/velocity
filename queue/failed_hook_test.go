package queue

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
	testsync "github.com/velocitykode/velocity/testing"
)

// panickingHookJob fails its run and, when PanicInFailed is set, panics in
// its Failed hook. Its state is serializable so a driver that rehydrates
// jobs (database) runs the same hook. Handle records the IDs it ran for
// the jobs that do not fail.
type panickingHookJob struct {
	ID            string `json:"id"`
	PanicInFailed bool   `json:"panic_in_failed"`
}

var errPanickingHookJob = errors.New("panicking hook job exploded")

// panickingHookHandled holds the IDs of panickingHookJob runs that
// succeeded, across rehydration.
var panickingHookHandled sync.Map

func (j *panickingHookJob) Handle() error {
	if j.PanicInFailed {
		return errPanickingHookJob
	}
	panickingHookHandled.Store(j.ID, true)
	return nil
}

func (j *panickingHookJob) Failed(error) {
	if j.PanicInFailed {
		panic("hook exploded for " + j.ID)
	}
}

func (j *panickingHookJob) MaxAttempts() int { return 1 }

func init() {
	RegisterJob(func(data []byte) (*panickingHookJob, error) {
		j := &panickingHookJob{}
		return j, json.Unmarshal(data, j)
	})
}

// TestRunFailedHook asserts RunFailedHook returns nil when the hook returns
// and ErrFailedHookPanicked wrapping the recovered value when it panics,
// whether the panic value is a string or an error.
func TestRunFailedHook(t *testing.T) {
	boom := errors.New("boom")
	if err := RunFailedHook(context.Background(), &panickingHookJob{ID: "quiet"}, boom); err != nil {
		t.Fatalf("hook that returns: err = %v, want nil", err)
	}

	err := RunFailedHook(context.Background(), &panickingHookJob{ID: "loud", PanicInFailed: true}, boom)
	if !errors.Is(err, ErrFailedHookPanicked) {
		t.Fatalf("hook that panics: err = %v, want ErrFailedHookPanicked", err)
	}
	pe := panicerr.AsTyped(err)
	if pe == nil || pe.Recovered() != "hook exploded for loud" {
		t.Errorf("recovered value not carried: %v", err)
	}

	errValue := errors.New("hook error value")
	err = RunFailedHook(context.Background(), &TestJob{OnFail: func(error) { panic(errValue) }}, boom)
	if !errors.Is(err, ErrFailedHookPanicked) || !errors.Is(err, errValue) {
		t.Errorf("hook that panics with an error: err = %v, want ErrFailedHookPanicked wrapping the value", err)
	}
}

// ctxHookJob records the context its FailedCtx hook got, and whether its
// plain Failed hook ran; PanicInFailed makes FailedCtx panic.
type ctxHookJob struct {
	PanicInFailed bool
	gotCtx        context.Context
	plainFailed   bool
}

func (j *ctxHookJob) Handle() error { return nil }
func (j *ctxHookJob) Failed(error)  { j.plainFailed = true }
func (j *ctxHookJob) FailedCtx(ctx context.Context, _ error) {
	j.gotCtx = ctx
	if j.PanicInFailed {
		panic("ctx hook exploded")
	}
}

type hookCtxKey struct{}

// RunFailedHook hands a FailedCtxer job the driver's context, never
// calling its plain Failed, contains a panic in FailedCtx, and turns a nil
// context into context.Background().
func TestRunFailedHook_FailedCtx(t *testing.T) {
	ctx := context.WithValue(context.Background(), hookCtxKey{}, "job-ctx")
	job := &ctxHookJob{}
	if err := RunFailedHook(ctx, job, errors.New("boom")); err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if job.plainFailed || job.gotCtx == nil || job.gotCtx.Value(hookCtxKey{}) != "job-ctx" {
		t.Errorf("FailedCtx got %v (plain Failed ran: %v), want the driver's context", job.gotCtx, job.plainFailed)
	}

	loud := &ctxHookJob{PanicInFailed: true}
	if err := RunFailedHook(ctx, loud, errors.New("boom")); !errors.Is(err, ErrFailedHookPanicked) {
		t.Errorf("panicking FailedCtx: err = %v, want ErrFailedHookPanicked", err)
	}

	bare := &ctxHookJob{}
	//lint:ignore SA1012 a nil context is the case under test
	_ = RunFailedHook(nil, bare, errors.New("boom"))
	if bare.gotCtx == nil {
		t.Error("nil context reached FailedCtx, want context.Background()")
	}
}

// TestWorker_FailedHookPanicKeepsWorkerRunning asserts a job whose Failed
// hook panics after the driver recorded its failure does not stop the
// worker: the queue.job.failed event for it is still dispatched, the driver
// holds the failed job, and the next job on the queue runs. Covers the
// memory and database drivers with one worker goroutine.
func TestWorker_FailedHookPanicKeepsWorkerRunning(t *testing.T) {
	const queueName = "hook-panic"
	drivers := []struct {
		name        string
		open        func(t *testing.T) Driver
		failedCount func(t *testing.T, d Driver) int
	}{
		{
			name: "memory",
			open: func(*testing.T) Driver { return NewMemoryDriver() },
			failedCount: func(t *testing.T, d Driver) int {
				failed, err := d.(*MemoryDriver).GetFailed(queueName)
				if err != nil {
					t.Fatalf("GetFailed: %v", err)
				}
				return len(failed)
			},
		},
		{
			name: "database",
			open: func(t *testing.T) Driver {
				d, cleanup := newSQLiteQueueDB(t)
				t.Cleanup(cleanup)
				return d
			},
			failedCount: func(t *testing.T, d Driver) int {
				var n int
				if err := d.(*DatabaseDriver).db.QueryRow("SELECT COUNT(*) FROM failed_jobs").Scan(&n); err != nil {
					t.Fatalf("count failed_jobs: %v", err)
				}
				return n
			},
		},
	}
	for _, tt := range drivers {
		t.Run(tt.name, func(t *testing.T) {
			d := tt.open(t)
			first, second := nextHookJobID("panic-"+tt.name), nextHookJobID("after-"+tt.name)
			t.Cleanup(func() { panickingHookHandled.Delete(second) })
			for _, job := range []*panickingHookJob{{ID: first, PanicInFailed: true}, {ID: second}} {
				if err := d.PushCtx(context.Background(), job, queueName); err != nil {
					t.Fatalf("push %s: %v", job.ID, err)
				}
			}

			var (
				mu     sync.Mutex
				failed []*JobFailed
				logged atomic.Int32
			)
			w := NewWorker(d, queueName, func(j Job) error { return j.Handle() },
				WithConcurrency(1), WithInterval(5*time.Millisecond), WithMaxRetries(1),
				WithWorkerLogger(hookPanicLogger{errors: &logged}))
			w.SetEventDispatcher(func(_ context.Context, event interface{}) error {
				if e, ok := event.(*JobFailed); ok {
					mu.Lock()
					failed = append(failed, e)
					mu.Unlock()
				}
				return nil
			})
			w.Start(context.Background())
			defer w.Stop()
			testsync.Eventually(t, func() bool {
				_, ok := panickingHookHandled.Load(second)
				return ok
			}, 5*time.Second, "the job after the panicking hook runs")
			w.Stop()

			mu.Lock()
			defer mu.Unlock()
			if len(failed) != 1 || failed[0].Error != errPanickingHookJob.Error() {
				t.Errorf("queue.job.failed dispatched %d times (%v), want once for the panicking job", len(failed), failed)
			}
			if n := tt.failedCount(t, d); n != 1 {
				t.Errorf("driver holds %d failed jobs, want 1", n)
			}
			if logged.Load() != 1 {
				t.Errorf("worker logged the hook panic %d times, want 1", logged.Load())
			}
		})
	}
}

// hookPanicLogger counts the worker's Error lines that report a panicking
// Failed hook and drops everything else.
type hookPanicLogger struct{ errors *atomic.Int32 }

func (hookPanicLogger) Info(string, ...any) {}
func (hookPanicLogger) Warn(string, ...any) {}
func (l hookPanicLogger) Error(msg string, _ ...any) {
	if msg == "Job Failed hook panicked after the failure was recorded" {
		l.errors.Add(1)
	}
}

func (hookPanicLogger) Debug(string, ...any) {}
func (hookPanicLogger) Fatal(string, ...any) {}

func (l hookPanicLogger) With(kvs ...any) contract.Logger { return contract.BindFields(l, kvs...) }
