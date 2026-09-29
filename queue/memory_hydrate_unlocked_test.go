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

// A reserved pop whose job cannot be rebuilt, because the factory returns
// an error or panics, drops the job and leaves no reservation behind (a
// leftover one would pin the job's dedupe key until Clear). The error is
// returned, and the panic reaches the caller.
func TestMemoryDriver_PopCtxReservedHydrateFailureLeavesNoReservation(t *testing.T) {
	registerHydrateHostileJob()
	boom := errors.New("factory failed")
	for _, c := range []struct {
		name string
		hook func() error
	}{
		{"error", func() error { return boom }},
		{"panic", func() error { panic(hostile.PanicValue) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			const q = "hydrate-failure"
			d := NewMemoryDriver()
			t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
			hook := c.hook
			hydrateHook.Store(&hook)
			t.Cleanup(func() { hydrateHook.Store(nil) })
			pushBytesOnly(t, d, q)

			var job Job
			var token ReservationToken
			var err error
			p := hostile.Within(t, hostile.Deadline, func() {
				job, token, _, err = d.PopCtxReserved(context.Background(), q)
			})
			if c.name == "panic" {
				if p != hostile.PanicValue {
					t.Fatalf("PopCtxReserved panic = %v, want the factory's panic", p)
				}
			} else if !errors.Is(err, boom) || job != nil || !token.IsZero() {
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
		})
	}
}
