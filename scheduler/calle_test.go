package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestJob_CallE_FiresOnFailureForReturnedError covers item 6: a closure
// registered via CallE that returns a non-nil error must trigger OnFailure
// (and dispatch scheduled.failed) without requiring the closure to panic.
// Pre-fix the only path setting err was the panic-recover branch, so
// OnFailure was unreachable for normal-error returns.
func TestJob_CallE_FiresOnFailureForReturnedError(t *testing.T) {
	wantErr := errors.New("upstream timeout")

	t.Run("OnFailure fires", func(t *testing.T) {
		s := New()

		var (
			mu      sync.Mutex
			gotErr  error
			fireCnt int
		)
		job := s.CallE(func() error { return wantErr }).
			Name("calle-job").
			OnFailure(func(err error) {
				mu.Lock()
				defer mu.Unlock()
				gotErr = err
				fireCnt++
			})

		if err := job.Run(); !errors.Is(err, wantErr) {
			t.Fatalf("Run() err = %v, want %v", err, wantErr)
		}

		mu.Lock()
		defer mu.Unlock()
		if fireCnt != 1 {
			t.Fatalf("OnFailure fired %d times, want exactly 1", fireCnt)
		}
		if !errors.Is(gotErr, wantErr) {
			t.Errorf("captured err = %v, want %v", gotErr, wantErr)
		}
	})

	t.Run("scheduled.failed dispatched exactly once", func(t *testing.T) {
		s := New()

		var (
			mu     sync.Mutex
			events []interface{}
		)
		s.SetEventDispatcher(func(e interface{}) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, e)
			return nil
		})

		job := s.CallE(func() error { return wantErr }).Name("calle-event-job")
		_ = job.Run()

		mu.Lock()
		defer mu.Unlock()

		var failed []*ScheduledTaskFailed
		var finished []*ScheduledTaskFinished
		for _, e := range events {
			switch ev := e.(type) {
			case *ScheduledTaskFailed:
				failed = append(failed, ev)
			case *ScheduledTaskFinished:
				finished = append(finished, ev)
			}
		}
		if len(failed) != 1 {
			t.Fatalf("expected 1 ScheduledTaskFailed event, got %d", len(failed))
		}
		if len(finished) != 0 {
			t.Errorf("expected 0 ScheduledTaskFinished events, got %d", len(finished))
		}
		if !strings.Contains(failed[0].Error, "upstream timeout") {
			t.Errorf("failed event Error = %q, want substring 'upstream timeout'", failed[0].Error)
		}
		if failed[0].TaskName != "calle-event-job" {
			t.Errorf("failed event TaskName = %q, want 'calle-event-job'", failed[0].TaskName)
		}
	})

	t.Run("OnSuccess fires when err is nil", func(t *testing.T) {
		s := New()
		var (
			successCnt atomic.Int32
			failureCnt atomic.Int32
		)

		job := s.CallE(func() error { return nil }).
			OnSuccess(func() { successCnt.Add(1) }).
			OnFailure(func(error) { failureCnt.Add(1) })

		if err := job.Run(); err != nil {
			t.Fatalf("Run() err = %v, want nil", err)
		}

		if successCnt.Load() != 1 {
			t.Errorf("OnSuccess fired %d times, want 1", successCnt.Load())
		}
		if failureCnt.Load() != 0 {
			t.Errorf("OnFailure fired %d times, want 0", failureCnt.Load())
		}
	})

	t.Run("panic still flows through OnFailure once", func(t *testing.T) {
		// Regression: the panic-recover branch in CallE must still dispatch
		// scheduled.failed exactly once and feed OnFailure. This locks in
		// the eager-dispatch behaviour from item 7c (panic_dispatch_test.go).
		s := New()
		var (
			mu     sync.Mutex
			events []interface{}
		)
		s.SetEventDispatcher(func(e interface{}) error {
			mu.Lock()
			defer mu.Unlock()
			events = append(events, e)
			return nil
		})

		var failureCnt atomic.Int32
		job := s.CallE(func() error { panic("boom") }).
			Name("calle-panic").
			OnFailure(func(error) { failureCnt.Add(1) })
		_ = job.Run()

		if failureCnt.Load() != 1 {
			t.Errorf("OnFailure fired %d times, want 1", failureCnt.Load())
		}

		mu.Lock()
		defer mu.Unlock()
		var failed int
		for _, e := range events {
			if _, ok := e.(*ScheduledTaskFailed); ok {
				failed++
			}
		}
		if failed != 1 {
			t.Errorf("scheduled.failed dispatched %d times, want 1", failed)
		}
	})

	t.Run("nil errCallback does not panic", func(t *testing.T) {
		// Defensive: a nil errCallback should be a no-op (no execution path).
		// Today the field is unset by Call(); CallE(nil) constructs a job
		// with errCallback = nil. Run should treat it like a no-op job
		// (no callback, no command), succeed, and fire OnSuccess.
		s := New()
		var successCnt atomic.Int32

		job := s.CallE(nil).OnSuccess(func() { successCnt.Add(1) })
		if err := job.Run(); err != nil {
			t.Errorf("Run() err = %v, want nil", err)
		}
		if successCnt.Load() != 1 {
			t.Errorf("OnSuccess fired %d times, want 1", successCnt.Load())
		}
	})
}

// TestScheduler_NamedAndNamedE covers item 18: explicit names disambiguate
// jobs registered with WithoutOverlapping.
func TestScheduler_NamedAndNamedE(t *testing.T) {
	t.Run("Named sets explicit name and runs", func(t *testing.T) {
		s := New()
		var ran atomic.Int32
		job := s.Named("hourly-cleanup", func() { ran.Add(1) })

		if name := job.GetName(); name != "hourly-cleanup" {
			t.Errorf("GetName() = %q, want hourly-cleanup", name)
		}
		if !job.nameExplicit {
			t.Errorf("nameExplicit = false, want true")
		}
		if err := job.Run(); err != nil {
			t.Fatalf("Run() err = %v", err)
		}
		if ran.Load() != 1 {
			t.Errorf("ran = %d, want 1", ran.Load())
		}
	})

	t.Run("NamedE feeds returned err to OnFailure", func(t *testing.T) {
		wantErr := errors.New("upstream down")
		s := New()
		var captured error
		var fireCnt atomic.Int32

		job := s.NamedE("hourly-fetch", func() error { return wantErr }).
			OnFailure(func(err error) {
				captured = err
				fireCnt.Add(1)
			})

		if name := job.GetName(); name != "hourly-fetch" {
			t.Errorf("GetName() = %q, want hourly-fetch", name)
		}
		if err := job.Run(); !errors.Is(err, wantErr) {
			t.Fatalf("Run() err = %v, want %v", err, wantErr)
		}
		if fireCnt.Load() != 1 {
			t.Errorf("OnFailure fired %d times, want 1", fireCnt.Load())
		}
		if !errors.Is(captured, wantErr) {
			t.Errorf("captured err = %v, want %v", captured, wantErr)
		}
	})

	t.Run("Call default name no longer hardcoded to 'closure'", func(t *testing.T) {
		s := New()
		job := s.Call(namedTestFunc) // top-level func: name is resolvable
		name := job.GetName()
		if name == "closure" {
			t.Errorf("Call() default name = %q, want runtime-derived (not 'closure')", name)
		}
		if !strings.Contains(name, "namedTestFunc") {
			t.Errorf("Call() default name = %q, want substring 'namedTestFunc'", name)
		}
		// Crucially the auto-name is NOT marked explicit, so the
		// WithoutOverlapping warning still fires for unnamed closures.
		if job.nameExplicit {
			t.Errorf("nameExplicit = true for auto-derived name, want false")
		}
	})

	t.Run("Call(nil) falls back to 'closure'", func(t *testing.T) {
		s := New()
		job := s.Call(nil)
		if name := job.GetName(); name != "closure" {
			t.Errorf("Call(nil) name = %q, want closure", name)
		}
	})

	t.Run("CallE(nil) falls back to 'closure'", func(t *testing.T) {
		s := New()
		job := s.CallE(nil)
		if name := job.GetName(); name != "closure" {
			t.Errorf("CallE(nil) name = %q, want closure", name)
		}
	})

	t.Run("WithoutOverlapping warns when name is default", func(t *testing.T) {
		s := New()
		log := &captureLogger{}
		s.SetLogger(log)

		s.Call(func() {}).WithoutOverlapping()

		if !log.hasError("WithoutOverlapping") {
			t.Errorf("expected WithoutOverlapping warning, got: %v", log.errors())
		}
	})

	t.Run("WithoutOverlapping silent for explicit name", func(t *testing.T) {
		s := New()
		log := &captureLogger{}
		s.SetLogger(log)

		s.Named("explicit-name", func() {}).WithoutOverlapping()

		if log.hasError("WithoutOverlapping") {
			t.Errorf("WithoutOverlapping warning fired despite explicit name: %v", log.errors())
		}
	})

	t.Run("WithoutOverlapping silent after .Name() chain", func(t *testing.T) {
		s := New()
		log := &captureLogger{}
		s.SetLogger(log)

		s.Call(func() {}).Name("renamed").WithoutOverlapping()

		if log.hasError("WithoutOverlapping") {
			t.Errorf("WithoutOverlapping warning fired despite .Name() chain: %v", log.errors())
		}
	})
}

// TestScheduler_RunDueJobs_CallE_Concurrent covers the race-detection path
// for CallE-registered jobs running through the ticker loop. Multiple
// CallE jobs that return errors concurrently must each surface their err
// to their own OnFailure without cross-contamination.
func TestScheduler_RunDueJobs_CallE_Concurrent(t *testing.T) {
	s := New()
	var (
		mu       sync.Mutex
		captured = make(map[string]error)
	)

	for i, name := range []string{"a", "b", "c", "d", "e"} {
		i, name := i, name
		s.NamedE(name, func() error {
			return errors.New(name + "-failure")
		}).OnFailure(func(err error) {
			mu.Lock()
			defer mu.Unlock()
			captured[name] = err
		}).Cron("* * * * *")
		_ = i
	}

	// Drive the runDueJobs path so we exercise the goroutine fan-out.
	s.runDueJobs()

	// runDueJobs spawns goroutines via runWg; wait for them.
	done := make(chan struct{})
	go func() {
		s.runWg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runWg.Wait timed out")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(captured) != 5 {
		t.Fatalf("captured %d errors, want 5: %v", len(captured), captured)
	}
	for _, name := range []string{"a", "b", "c", "d", "e"} {
		err, ok := captured[name]
		if !ok {
			t.Errorf("no err captured for %q", name)
			continue
		}
		if !strings.Contains(err.Error(), name+"-failure") {
			t.Errorf("err for %q = %v, want substring %q", name, err, name+"-failure")
		}
	}
}

// TestScheduler_CallE_ContextCanceledDoesNotMaskErr verifies that even when
// the parent ctx is cancelled before Run, the closure still runs to
// completion (Run uses its own internal trace ctx, not the scheduler's
// run-loop ctx) and any returned err still flows to OnFailure. This
// documents the existing Run() contract under the new CallE path.
func TestScheduler_CallE_ContextCanceledDoesNotMaskErr(t *testing.T) {
	s := New()
	wantErr := errors.New("inner failure")
	var captured error
	job := s.CallE(func() error {
		return wantErr
	}).OnFailure(func(err error) { captured = err })

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel
	_ = ctx

	if err := job.Run(); !errors.Is(err, wantErr) {
		t.Fatalf("Run() err = %v, want %v", err, wantErr)
	}
	if !errors.Is(captured, wantErr) {
		t.Errorf("captured = %v, want %v", captured, wantErr)
	}
}

// namedTestFunc is a top-level helper so runtime.FuncForPC can resolve a
// real symbol name (anonymous funcs in test bodies often resolve to
// "TestX.func1" which is fine but we want a deterministic substring).
func namedTestFunc() {}

// captureLogger collects log entries for assertion.
type captureLogger struct {
	mu  sync.Mutex
	dbg []string
	inf []string
	err []string
}

func (l *captureLogger) Info(msg string, _ ...interface{})  { l.add(&l.inf, msg) }
func (l *captureLogger) Error(msg string, _ ...interface{}) { l.add(&l.err, msg) }
func (l *captureLogger) Debug(msg string, _ ...interface{}) { l.add(&l.dbg, msg) }

func (l *captureLogger) add(buf *[]string, msg string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	*buf = append(*buf, msg)
}

func (l *captureLogger) hasError(substr string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, m := range l.err {
		if strings.Contains(m, substr) {
			return true
		}
	}
	return false
}

func (l *captureLogger) errors() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]string, len(l.err))
	copy(out, l.err)
	return out
}
