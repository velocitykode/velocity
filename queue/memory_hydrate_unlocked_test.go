package queue

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// hydrateHostileJob is a job whose registered factory runs hydrateHook,
// standing in for a factory or UnmarshalJSON that is user code.
type hydrateHostileJob struct {
	ID string `json:"id"`
}

func (j *hydrateHostileJob) Handle() error { return nil }
func (j *hydrateHostileJob) Failed(error)  {}

var (
	hydrateHook     atomic.Pointer[func() error]
	hydrateRegister sync.Once
)

func registerHydrateHostileJob() {
	hydrateRegister.Do(func() {
		RegisterJob(func(data []byte) (*hydrateHostileJob, error) {
			if h := hydrateHook.Load(); h != nil {
				if err := (*h)(); err != nil {
					return nil, err
				}
			}
			j := &hydrateHostileJob{}
			return j, json.Unmarshal(data, j)
		})
	})
}

// pushBytesOnly pushes a job and drops the wrapper's live pointer, so a pop
// rebuilds the job from its payload bytes through the registry, as a job
// read back from storage is.
func pushBytesOnly(t *testing.T, d *MemoryDriver, queueName string) {
	t.Helper()
	if err := d.PushCtx(context.Background(), &hydrateHostileJob{ID: "h1"}, queueName); err != nil {
		t.Fatalf("push: %v", err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	for e := d.queues[queueName].Front(); e != nil; e = e.Next() {
		e.Value.(*jobWrapper).Job = nil
	}
}

// Rebuilding a popped job runs the registered factory, user code, so the
// memory driver runs it after releasing its lock: a factory that calls back
// into the driver returns, and one that blocks holds only its own pop while
// the driver's other calls answer.
func TestMemoryDriver_HydratesPoppedJobsWithoutTheLock(t *testing.T) {
	registerHydrateHostileJob()
	pops := []struct {
		name string
		pop  func(d *MemoryDriver, q string) (Job, error)
	}{
		{"PopCtxWithTrace", func(d *MemoryDriver, q string) (Job, error) {
			j, _, err := d.PopCtxWithTrace(context.Background(), q)
			return j, err
		}},
		{"PopCtxReserved", func(d *MemoryDriver, q string) (Job, error) {
			j, _, _, err := d.PopCtxReserved(context.Background(), q)
			return j, err
		}},
	}
	for _, mode := range []hostile.Mode{hostile.Reenter, hostile.Block} {
		for _, p := range pops {
			t.Run(mode.String()+"/"+p.name, func(t *testing.T) {
				const q = "hydrate-unlocked"
				d := NewMemoryDriver()
				t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
				code := hostile.New(t, mode, func() {
					_, _ = d.Size(q)
					_ = d.PushCtx(context.Background(), fallbackProbeJob{}, "other")
				})
				hook := func() error { code.Run(); return nil }
				hydrateHook.Store(&hook)
				t.Cleanup(func() { hydrateHook.Store(nil) })
				pushBytesOnly(t, d, q)

				type result struct {
					job Job
					err error
				}
				popped := make(chan result, 1)
				go func() { //safe-goroutine: the test releases a blocked factory below and waits for the pop
					job, err := p.pop(d, q)
					popped <- result{job, err}
				}()
				if mode == hostile.Block {
					if !code.AwaitEntered(t) {
						return
					}
					hostile.Within(t, hostile.Deadline, func() {
						if _, sizeErr := d.Size(q); sizeErr != nil {
							t.Errorf("Size while the factory blocks: %v", sizeErr)
						}
					})
					code.Release()
				}
				var r result
				hostile.Within(t, hostile.Deadline, func() { r = <-popped })
				if r.err != nil || r.job == nil {
					t.Fatalf("pop = %v, %v; want the rebuilt job", r.job, r.err)
				}
				if _, ok := r.job.(*hydrateHostileJob); !ok {
					t.Errorf("pop returned %T, want *hydrateHostileJob", r.job)
				}
			})
		}
	}
}

// A reserved pop whose job cannot be rebuilt drops the job and returns the
// error, and leaves no reservation behind.
func TestMemoryDriver_PopCtxReservedHydrateErrorLeavesNoReservation(t *testing.T) {
	registerHydrateHostileJob()
	const q = "hydrate-error"
	d := NewMemoryDriver()
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	boom := errors.New("factory failed")
	hook := func() error { return boom }
	hydrateHook.Store(&hook)
	t.Cleanup(func() { hydrateHook.Store(nil) })
	pushBytesOnly(t, d, q)

	job, token, _, err := d.PopCtxReserved(context.Background(), q)
	if !errors.Is(err, boom) || job != nil || !token.IsZero() {
		t.Fatalf("PopCtxReserved = %v, %+v, %v; want nil, zero token, the factory's error", job, token, err)
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if n := len(d.reservations); n != 0 {
		t.Errorf("reservations = %d, want 0", n)
	}
	if n := d.queues[q].Len(); n != 0 {
		t.Errorf("queued jobs = %d, want the job dropped", n)
	}
}
