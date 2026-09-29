package scheduler

import (
	"context"
	"errors"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// reentry calls Shutdown once from inside the scheduler's own work and
// records what it returned.
type reentry struct {
	s    *Scheduler
	once sync.Once
	got  chan error
}

func newReentry(s *Scheduler) *reentry {
	return &reentry{s: s, got: make(chan error, 1)}
}

// shutdown calls Shutdown with a deadline, so a Shutdown that waits on
// the work calling it fails the test instead of hanging it.
func (r *reentry) shutdown() {
	r.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r.got <- r.s.Shutdown(ctx)
	})
}

// assertShutdownRefused runs s, waits for the Shutdown r makes from inside
// s's work, and requires it to have returned ErrShutdownFromTask with the
// scheduler left running. A Shutdown from outside then stops it, once
// settle (when not nil) has returned.
func assertShutdownRefused(t *testing.T, s *Scheduler, r *reentry, settle ...func()) {
	t.Helper()
	ran := make(chan error, 1)
	go func() { ran <- s.Run(context.Background()) }()

	select {
	case err := <-r.got:
		if !errors.Is(err, ErrShutdownFromTask) {
			t.Fatalf("Shutdown from inside the scheduler's work returned %v, want ErrShutdownFromTask", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the work never called Shutdown")
	}

	s.mu.RLock()
	running := s.running
	s.mu.RUnlock()
	if !running {
		t.Error("a refused Shutdown stopped the scheduler")
	}

	for _, f := range settle {
		f()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown from outside after the refusal: %v", err)
	}
	select {
	case <-ran:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after Shutdown")
	}
}

// reentrantLogger calls onError from Error and onWarn from Warn, and
// panics on With when bindPanics is set.
type reentrantLogger struct {
	onError    func()
	onWarn     func()
	bindPanics bool
}

func (*reentrantLogger) Debug(string, ...any) {}
func (*reentrantLogger) Info(string, ...any)  {}
func (l *reentrantLogger) Warn(string, ...any) {
	if l.onWarn != nil {
		l.onWarn()
	}
}
func (l *reentrantLogger) Error(string, ...any) {
	if l.onError != nil {
		l.onError()
	}
}
func (*reentrantLogger) Fatal(string, ...any) {}
func (l *reentrantLogger) With(...any) contract.Logger {
	if l.bindPanics {
		panic("with broke")
	}
	return l
}

// A logger whose Error calls Shutdown while writing a task's panic line
// gets an error back at once instead of waiting for the run it is inside.
func TestShutdown_FromThePanicDiagnosticIsRefused(t *testing.T) {
	s := New()
	r := newReentry(s)
	s.SetLogger(&reentrantLogger{onError: r.shutdown, bindPanics: true})
	s.Named("reentry.diagnostic", func() {}).Cron("* * * * *")
	assertShutdownRefused(t, s, r)
}

// A logger whose Warn calls Shutdown while writing a lock-skip warning
// gets an error back at once instead of waiting for the skipped task.
func TestShutdown_FromTheLockSkipWarningIsRefused(t *testing.T) {
	s := New()
	r := newReentry(s)
	s.SetLogger(&reentrantLogger{onWarn: r.shutdown})
	s.SetLocker(failingLocker{})
	s.Named("reentry.skip", func() {}).Cron("* * * * *").OnOneServer()
	assertShutdownRefused(t, s, r)
}

// A task that calls Shutdown from its own run gets an error back at once.
func TestShutdown_FromATasksOwnRunIsRefused(t *testing.T) {
	s := New()
	r := newReentry(s)
	s.Named("reentry.run", r.shutdown).Cron("* * * * *")
	assertShutdownRefused(t, s, r)
}

// A listener handed a task's started event on the run's goroutine that
// calls Shutdown gets an error back at once.
func TestShutdown_FromATaskEventListenerIsRefused(t *testing.T) {
	s := New()
	r := newReentry(s)
	s.SetEventDispatcher(func(context.Context, interface{}) error {
		r.shutdown()
		return nil
	})
	s.Named("reentry.listener", func() {}).Cron("* * * * *")
	assertShutdownRefused(t, s, r)
}

// A task's hooks run inside its run, including an OnSuccess hook that a
// RunInBackground task's completion runs on its own goroutine.
func TestShutdown_FromATaskHookIsRefused(t *testing.T) {
	t.Run("OnSuccess", func(t *testing.T) {
		s := New()
		r := newReentry(s)
		s.Named("reentry.hook", func() {}).Cron("* * * * *").OnSuccess(r.shutdown)
		assertShutdownRefused(t, s, r)
	})
	t.Run("RunInBackground", func(t *testing.T) {
		bin, err := exec.LookPath("true")
		if err != nil {
			t.Skip("no true binary on this system")
		}
		s := New()
		r := newReentry(s)
		s.Command(bin).Cron("* * * * *").RunInBackground().OnSuccess(r.shutdown)
		assertShutdownRefused(t, s, r)
	})
}

// reentrantLocker calls onAcquire from Acquire and hands out locks whose
// Release calls onRelease.
type reentrantLocker struct {
	onAcquire func()
	onRelease func()
}

func (l reentrantLocker) Acquire(_ context.Context, name string, _ time.Duration) (Lock, error) {
	if l.onAcquire != nil {
		l.onAcquire()
	}
	return reentrantLock{name: name, onRelease: l.onRelease}, nil
}

type reentrantLock struct {
	name      string
	onRelease func()
}

func (l reentrantLock) Name() string       { return l.name }
func (reentrantLock) FencingToken() uint64 { return 1 }
func (l reentrantLock) Release(context.Context) error {
	if l.onRelease != nil {
		l.onRelease()
	}
	return nil
}

// A Locker is user code the scheduler runs inside a task's accounting:
// Shutdown from its Acquire, or from the Release of a task's overlap lock,
// gets an error back at once.
func TestShutdown_FromTheLockerIsRefused(t *testing.T) {
	t.Run("Acquire", func(t *testing.T) {
		s := New()
		r := newReentry(s)
		s.SetLocker(reentrantLocker{onAcquire: r.shutdown})
		s.Named("reentry.acquire", func() {}).Cron("* * * * *").OnOneServer()
		assertShutdownRefused(t, s, r)
	})
	t.Run("Release", func(t *testing.T) {
		s := New()
		r := newReentry(s)
		s.SetLocker(reentrantLocker{onRelease: r.shutdown})
		s.Named("reentry.release", func() {}).Cron("* * * * *").WithoutOverlapping()
		assertShutdownRefused(t, s, r)
	})
}

// The scheduler-level Before and After callbacks run inside a tick:
// Shutdown from one gets an error back at once.
func TestShutdown_FromASchedulerCallbackIsRefused(t *testing.T) {
	for _, when := range []string{"Before", "After"} {
		t.Run(when, func(t *testing.T) {
			s := New()
			r := newReentry(s)
			if when == "Before" {
				s.Before(r.shutdown)
			} else {
				s.After(r.shutdown)
			}
			// The tick goes on to dispatch the task after a refused
			// Shutdown in Before; the outside Shutdown waits for that.
			ran := make(chan struct{})
			s.Named("reentry.callback."+when, func() { close(ran) }).Cron("* * * * *")
			assertShutdownRefused(t, s, r, func() { <-ran })
		})
	}
}

// A refused Shutdown changes nothing even when the scheduler never ran: a
// task run directly by a tick outside Run that calls Shutdown does not
// mark the scheduler terminated, so a later Run still starts.
func TestShutdown_RefusedBeforeRunChangesNothing(t *testing.T) {
	s := New()
	r := newReentry(s)
	s.Named("reentry.norun", r.shutdown).Cron("* * * * *")
	s.runDueJobs()
	select {
	case err := <-r.got:
		if !errors.Is(err, ErrShutdownFromTask) {
			t.Fatalf("Shutdown from a task run outside Run returned %v, want ErrShutdownFromTask", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the task never called Shutdown")
	}
	s.runWg.Wait()
	s.mu.RLock()
	terminated := s.terminated
	s.mu.RUnlock()
	if terminated {
		t.Fatal("a refused Shutdown marked the scheduler terminated")
	}
}

// Shutdown from a goroutine that is not the scheduler's work is not
// refused while a task runs, and waits for it.
func TestShutdown_FromOutsideWaitsForTheRun(t *testing.T) {
	s := New()
	started, finish := make(chan struct{}), make(chan struct{})
	s.Named("reentry.outside", func() {
		close(started)
		<-finish
	}).Cron("* * * * *")
	ran := make(chan error, 1)
	go func() { ran <- s.Run(context.Background()) }()
	<-started

	stopped := make(chan error, 1)
	go func() { stopped <- s.Shutdown(context.Background()) }()
	select {
	case err := <-stopped:
		t.Fatalf("Shutdown returned %v while the task was still running", err)
	case <-time.After(100 * time.Millisecond):
	}
	close(finish)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Shutdown never returned after the task finished")
	}
	<-ran
}
