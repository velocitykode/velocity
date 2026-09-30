package queuetest

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/queue"
)

// poisonJob is a job whose registered factory cannot rebuild it: it panics
// or returns errPoisonFactory, as its Mode says. The factory is user code,
// so a driver that rebuilds jobs from their payload must treat either
// outcome as a poison job.
type poisonJob struct {
	Mode string `json:"mode"`
}

func (*poisonJob) Handle() error   { return nil }
func (*poisonJob) Failed(error)    {}
func (j *poisonJob) JobID() string { return "poison-" + j.Mode }

const (
	poisonModePanic = "panic"
	poisonModeError = "error"
)

// errPoisonFactory is the error the poison job's factory returns.
var errPoisonFactory = errors.New("queuetest: the factory cannot rebuild this job")

// poisonPanicValue is the value the poison job's factory panics with.
type poisonPanicValue struct{}

func init() {
	queue.RegisterJob(func(data []byte) (*poisonJob, error) {
		j := &poisonJob{}
		if err := json.Unmarshal(data, j); err != nil {
			return nil, err
		}
		if j.Mode == poisonModePanic {
			panic(poisonPanicValue{})
		}
		return nil, errPoisonFactory
	})
}

// failedEventRecorder records the names of the events a driver dispatches.
type failedEventRecorder struct {
	mu    sync.Mutex
	names []string
}

func (r *failedEventRecorder) dispatch(_ context.Context, event interface{}) error {
	if n, ok := event.(interface{ Name() string }); ok {
		r.mu.Lock()
		r.names = append(r.names, n.Name())
		r.mu.Unlock()
	}
	return nil
}

func (r *failedEventRecorder) count(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, got := range r.names {
		if got == name {
			n++
		}
	}
	return n
}

// poisonPop pops one job; token is the zero token for a driver's
// non-reserving pop.
type poisonPop func(d queue.Driver, queueName string) (queue.Job, queue.ReservationToken, error)

// runPoisonContract is the poison contract of a driver's pop, shared by
// the plain and the reserving pop. A job whose factory panics or fails
// never runs, so the pop:
//
//   - never lets the factory's panic reach the caller;
//   - returns no job, a zero token and an error wrapping queue.ErrPoisonJob
//     together with the cause (the factory's error, or the recovered panic
//     as a *panicerr.Error);
//   - dispatches no queue.job.failed event, since the job never ran;
//   - leaves the next job poppable, so one poison job does not stall the
//     queue.
//
// A driver that hands back the pushed job itself (an in-process driver's
// same-process pop) runs no factory and loses nothing, and satisfies the
// contract that way.
func runPoisonContract(t *testing.T, factory DriverFactory, pop poisonPop) {
	t.Helper()
	for _, mode := range []string{poisonModePanic, poisonModeError} {
		t.Run(mode, func(t *testing.T) {
			d := factory(t)
			events := &failedEventRecorder{}
			if aware, ok := d.(contract.EventDispatcherAware); ok {
				aware.SetEventDispatcher(events.dispatch)
			}
			const q = "q-poison"
			ctx := context.Background()
			poison := &poisonJob{Mode: mode}
			if err := d.PushCtx(ctx, poison, q); err != nil {
				t.Fatalf("push poison: %v", err)
			}
			if err := d.PushCtx(ctx, &ContractJob{IDValue: "after-poison"}, q); err != nil {
				t.Fatalf("push next: %v", err)
			}

			var (
				job   queue.Job
				token queue.ReservationToken
				err   error
			)
			escaped := func() (p any) {
				defer func() { p = recover() }()
				job, token, err = pop(d, q)
				return nil
			}()
			if escaped != nil {
				t.Fatalf("pop let the factory's panic reach the caller: %v", escaped)
			}
			if pj, ok := job.(*poisonJob); ok && pj == poison && err == nil {
				// Same-process pop: the pushed job itself, no factory run.
			} else {
				if job != nil || !token.IsZero() {
					t.Fatalf("pop = %T, %+v, %v; want no job and a zero token", job, token, err)
				}
				if !errors.Is(err, queue.ErrPoisonJob) {
					t.Fatalf("pop error = %v; want it to wrap queue.ErrPoisonJob", err)
				}
				switch mode {
				case poisonModeError:
					if !errors.Is(err, errPoisonFactory) {
						t.Errorf("pop error = %v; want it to wrap the factory's error", err)
					}
				case poisonModePanic:
					pe := panicerr.AsTyped(err)
					if pe == nil {
						t.Errorf("pop error = %v; want the recovered panic reachable as *panicerr.Error", err)
					} else if _, ok := pe.Recovered().(poisonPanicValue); !ok {
						t.Errorf("recovered panic = %#v; want the factory's panic value", pe.Recovered())
					}
				}
			}
			if n := events.count("queue.job.failed"); n != 0 {
				t.Errorf("queue.job.failed dispatched %d times for a job that never ran; want 0", n)
			}

			next, _, err := pop(d, q)
			if err != nil {
				t.Fatalf("pop after the poison job: %v", err)
			}
			cj, ok := next.(*ContractJob)
			if !ok || cj.IDValue != "after-poison" {
				t.Fatalf("pop after the poison job = %#v; want the next job", next)
			}
		})
	}
}
