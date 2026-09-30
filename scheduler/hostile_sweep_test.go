package scheduler

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// armedLogger passes each line to a hostile logger while armed and drops
// it otherwise.
type armedLogger struct {
	armed *atomic.Bool
	h     *hostile.Logger
}

func (l armedLogger) pass(line func(contract.Logger)) {
	if l.armed.Load() {
		line(l.h)
	}
}

func (l armedLogger) Debug(msg string, kvs ...any) {
	l.pass(func(h contract.Logger) { h.Debug(msg, kvs...) })
}
func (l armedLogger) Info(msg string, kvs ...any) {
	l.pass(func(h contract.Logger) { h.Info(msg, kvs...) })
}
func (l armedLogger) Warn(msg string, kvs ...any) {
	l.pass(func(h contract.Logger) { h.Warn(msg, kvs...) })
}
func (l armedLogger) Error(msg string, kvs ...any) {
	l.pass(func(h contract.Logger) { h.Error(msg, kvs...) })
}
func (l armedLogger) Fatal(msg string, kvs ...any) {
	l.pass(func(h contract.Logger) { h.Fatal(msg, kvs...) })
}
func (l armedLogger) With(kvs ...any) contract.Logger {
	l.pass(func(h contract.Logger) { h.With(kvs...) })
	return l
}

// The user code a scheduler calls: its logger, its event dispatcher, a
// task, and the Before and After callbacks.
var schedulerSites = []string{"logger", "dispatcher", "task", "before", "after"}

// sweepScheduler is a scheduler whose user code at one site turns hostile
// while armed, with a task that counts its runs.
type sweepScheduler struct {
	s     *Scheduler
	armed atomic.Bool
	runs  atomic.Int32
	code  *hostile.Code
	// reentered closes once a Reenter code's call into the scheduler has
	// returned.
	reentered chan struct{}
}

func newSweepScheduler(t *testing.T, mode hostile.Mode, site string) *sweepScheduler {
	w := &sweepScheduler{s: New(), reentered: make(chan struct{})}
	w.code = hostile.New(t, mode, func() {
		defer close(w.reentered)
		_ = w.s.Jobs()
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		_ = w.s.Run(ctx)
		_ = w.s.Shutdown(ctx)
	})
	hostileAt := func(at string) func() {
		return func() {
			if site == at && w.armed.Load() {
				w.code.Run()
			}
		}
	}
	switch site {
	case "logger":
		w.s.SetLogger(armedLogger{armed: &w.armed, h: hostile.NewLogger(w.code)})
	case "dispatcher":
		d := hostile.NewDispatcher(w.code)
		w.s.SetEventDispatcher(func(ctx context.Context, ev any) error {
			if w.armed.Load() {
				return d.Dispatch(ctx, ev)
			}
			return nil
		})
	}
	task := hostileAt("task")
	w.s.Named("sweep.task", func() {
		w.runs.Add(1)
		task()
	}).Cron("* * * * *")
	w.s.Before(hostileAt("before"))
	w.s.After(hostileAt("after"))
	return w
}

// start runs the scheduler on a goroutine of its own and returns its
// result channel.
func (w *sweepScheduler) start() <-chan error {
	done := make(chan error, 1)
	go func() { done <- w.s.Run(context.Background()) }()
	return done
}

// ranOnce fails the test unless the task has run within the harness
// deadline.
func (w *sweepScheduler) ranOnce(t *testing.T) {
	t.Helper()
	deadline := time.Now().Add(hostile.Deadline)
	for w.runs.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the task did not run")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// shutdown runs Shutdown with d as its deadline, failing the test when it
// panics or outlives d by much.
func (w *sweepScheduler) shutdown(t *testing.T, d time.Duration) error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()
	var err error
	if p := hostile.Within(t, d+2*time.Second, func() { err = w.s.Shutdown(ctx) }); p != nil {
		t.Fatalf("Shutdown panicked: %v", p)
	}
	return err
}

// awaitRun fails the test unless Run returns nil within the deadline.
func awaitRun(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run = %v, want nil", err)
		}
	case <-time.After(hostile.Deadline):
		t.Fatal("Run did not return after Shutdown")
	}
}

// Run with its first tick, and Shutdown, survive a logger, event
// dispatcher, task or Before/After callback that panics, blocks or calls
// back into the scheduler: no panic escapes, a bounded Shutdown returns
// on time while the code blocks, Run returns once shut down, and the
// scheduler runs its task again afterwards.
func TestScheduler_HostileUserCodeSweep(t *testing.T) {
	for _, entry := range []string{"Run", "Shutdown"} {
		t.Run(entry, func(t *testing.T) {
			for _, mode := range hostile.Modes() {
				for _, site := range schedulerSites {
					t.Run(mode.String()+"/"+site, func(t *testing.T) {
						body := func() { schedulerSweepCase(t, entry, mode, site) }
						if mode == hostile.Panic {
							hostile.Isolated(t, body)
							return
						}
						body()
					})
				}
			}
		})
	}
}

func schedulerSweepCase(t *testing.T, entry string, mode hostile.Mode, site string) {
	w := newSweepScheduler(t, mode, site)
	var done <-chan error
	switch entry {
	case "Run":
		w.armed.Store(true)
		done = w.start()
		// Every site runs in the first run's start or first tick: wait for
		// it, so the case proves the code ran, and the Shutdowns below come
		// after Run began (one before it would stop the scheduler for good).
		select {
		case <-w.code.Entered():
		case <-time.After(hostile.Deadline):
			t.Fatal("the hostile code never ran")
		}
	case "Shutdown":
		done = w.start()
		w.ranOnce(t)
		w.armed.Store(true)
	}

	var err error
	if entry == "Shutdown" || mode == hostile.Block {
		if mode == hostile.Block {
			select {
			case <-w.code.Entered():
			case <-time.After(time.Second):
			}
		}
		err = w.shutdown(t, 100*time.Millisecond)
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("Shutdown = %v", err)
		}
	}
	if mode == hostile.Reenter {
		select {
		case <-w.code.Entered():
			select {
			case <-w.reentered:
			case <-time.After(hostile.Deadline):
				t.Fatal("the re-entry into the scheduler did not return")
			}
		case <-time.After(200 * time.Millisecond):
		}
	}
	w.code.Release()
	w.code.Disarm()
	w.armed.Store(false)

	if err := w.shutdown(t, 2*time.Second); err != nil {
		t.Errorf("the closing Shutdown = %v, want nil", err)
	}
	awaitRun(t, done)

	// The scheduler runs again. A job fires once per wall-clock minute,
	// so the rerun proves itself with a job that has not fired yet.
	var reran atomic.Int32
	w.s.Named("sweep.rerun", func() { reran.Add(1) }).Cron("* * * * *")
	done = w.start()
	deadline := time.Now().Add(hostile.Deadline)
	for reran.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the scheduler did not run a job after it was shut down")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := w.shutdown(t, 2*time.Second); err != nil {
		t.Errorf("Shutdown after the rerun = %v, want nil", err)
	}
	awaitRun(t, done)
}
