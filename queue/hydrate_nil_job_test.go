package queue

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
)

// nilFactoryJob's registered factory returns no job and no error, as a
// typed nil pointer.
type nilFactoryJob struct{ ID string }

func (*nilFactoryJob) Handle() error { return nil }
func (*nilFactoryJob) Failed(error)  {}

// untypedNilJob is registered through Register with a factory returning a
// nil Job interface and no error.
type untypedNilJob struct{ ID string }

func (untypedNilJob) Handle() error { return nil }
func (untypedNilJob) Failed(error)  {}

var registerNilFactories sync.Once

func registerNilJobFactories() {
	registerNilFactories.Do(func() {
		RegisterJob(func([]byte) (*nilFactoryJob, error) { return nil, nil })
		Register(normalizeJobType("queue.untypedNilJob"), func([]byte) (Job, error) { return nil, nil })
	})
}

// pushRebuiltOnly pushes job and drops the wrapper's live pointer, so the
// pop rebuilds it through the registry.
func pushRebuiltOnly(t *testing.T, d *MemoryDriver, q string, job Job) {
	t.Helper()
	if err := d.PushCtx(context.Background(), job, q); err != nil {
		t.Fatalf("push: %v", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for e := d.queues[q].Front(); e != nil; e = e.Next() {
		e.Value.(*jobWrapper).Job = nil
	}
}

// A factory that returns no job and no error cannot yield a runnable job:
// hydration reports an error naming the job type, the reserved pop leaves
// no reservation behind, and a worker reports it instead of taking the
// pop for an empty queue (which would leave the reservation unacked) or
// running a nil job.
func TestHydrate_FactoryReturningNoJobIsAnError(t *testing.T) {
	registerNilJobFactories()
	for _, c := range []struct {
		name string
		job  Job
		typ  string
	}{
		{"typed nil", &nilFactoryJob{ID: "n1"}, "nilFactoryJob"},
		{"untyped nil", untypedNilJob{ID: "n2"}, "untypedNilJob"},
	} {
		t.Run(c.name+"/PopCtxReserved", func(t *testing.T) {
			const q = "nil-factory"
			d := NewMemoryDriver()
			t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
			pushRebuiltOnly(t, d, q, c.job)

			job, token, _, err := d.PopCtxReserved(context.Background(), q)
			if err == nil || !strings.Contains(err.Error(), c.typ) || job != nil || !token.IsZero() {
				t.Fatalf("PopCtxReserved = %v, %+v, %v; want nil, zero token and an error naming %s", job, token, err, c.typ)
			}
			d.mu.Lock()
			defer d.mu.Unlock()
			if n := len(d.reservations); n != 0 {
				t.Errorf("reservations = %d, want 0", n)
			}
		})
		t.Run(c.name+"/worker", func(t *testing.T) {
			const q = "nil-factory-worker"
			d := newStartedMemoryDriver(t)
			logger := &levelLogger{}
			var ran sync.Map
			w := NewWorker(d, q, func(j Job) error {
				ran.Store(j, true)
				return nil
			}, WithInterval(5*time.Millisecond), WithWorkerLogger(logger))
			pushRebuiltOnly(t, d, q, c.job)
			w.Start(context.Background())
			t.Cleanup(func() { _ = w.Stop() })

			hostile.Eventually(t, hostile.Deadline, "the worker reporting the job it could not rebuild", func() bool {
				for _, line := range logger.errorLines() {
					if err, ok := line.field("error"); ok && strings.Contains(errString(err), c.typ) {
						return true
					}
				}
				return false
			})
			_ = w.Stop()
			ran.Range(func(k, _ any) bool {
				t.Errorf("the handler ran a job from a factory that returned none: %#v", k)
				return true
			})
			d.mu.Lock()
			defer d.mu.Unlock()
			if n := len(d.reservations); n != 0 {
				t.Errorf("reservations = %d, want 0", n)
			}
		})
	}
}

func errString(v any) string {
	if err, ok := v.(error); ok {
		return err.Error()
	}
	return ""
}
