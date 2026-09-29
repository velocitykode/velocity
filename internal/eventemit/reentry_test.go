package eventemit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// A hook that synchronously causes another failing dispatch is not called
// again for it: the nested failure is counted and logged like any other,
// and the hook runs once.
func TestFailures_HookIsNotReenteredByTheFailuresItCauses(t *testing.T) {
	var f Failures
	logger := &recordingLogger{}
	dispatch := f.Recording(failing, logger)
	var calls atomic.Int32
	var nestedErr error
	f.SetHook(func(error, any) {
		if calls.Add(1) > 5 {
			return // bound the recursion the base let through
		}
		nestedErr = dispatch(context.Background(), namedEvent{name: "cache.hit"})
	})

	if err := dispatch(context.Background(), namedEvent{name: "cache.hit"}); !Recorded(err) {
		t.Fatalf("dispatch returned %v, want a recorded failure", err)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("hook calls = %d, want 1 (not re-entered by its own failure)", got)
	}
	if got := f.Count(); got != 2 {
		t.Errorf("Count = %d, want 2 (the failure and the one the hook caused)", got)
	}
	if !Recorded(nestedErr) || !errors.Is(nestedErr, errListener) {
		t.Errorf("the hook's dispatch returned %v, want the recorded listener failure", nestedErr)
	}
	if got := len(logger.at("warn")); got != 1 {
		t.Errorf("warn lines = %d, want 1 (first failure of the name)", got)
	}

	// The guard is released once the hook returns: a later failure on the
	// same goroutine is hooked again.
	_ = dispatch(context.Background(), namedEvent{name: "widget.synced"})
	if got := calls.Load(); got != 2 {
		t.Errorf("hook calls after a later failure = %d, want 2", got)
	}
}

// A hook panic still releases the guard.
func TestFailures_HookGuardReleasedAfterAPanic(t *testing.T) {
	var f Failures
	var calls atomic.Int32
	f.SetHook(func(error, any) {
		calls.Add(1)
		panic("hook broke")
	})
	f.Record(context.Background(), &recordingLogger{}, errListener, namedEvent{name: "a"})
	f.Record(context.Background(), &recordingLogger{}, errListener, namedEvent{name: "a"})
	if got := calls.Load(); got != 2 {
		t.Errorf("hook calls = %d, want 2", got)
	}
}

// The guard is per goroutine: while the hook runs for one failure, a
// failure on another goroutine is still handed to the hook.
func TestFailures_HookRunsForConcurrentFailures(t *testing.T) {
	var f Failures
	logger := &recordingLogger{}
	secondHooked := make(chan struct{})
	var calls atomic.Int32
	f.SetHook(func(error, any) {
		if calls.Add(1) != 1 {
			close(secondHooked)
			return
		}
		go f.Record(context.Background(), logger, errListener, namedEvent{name: "b"})
		select {
		case <-secondHooked:
		case <-time.After(5 * time.Second):
			t.Error("a failure on another goroutine was not hooked while the hook ran")
		}
	})
	f.Record(context.Background(), logger, errListener, namedEvent{name: "a"})
	if got := calls.Load(); got != 2 {
		t.Errorf("hook calls = %d, want 2", got)
	}
}

// The hook guard is safe under concurrent failures, hooks and SetHook.
func TestFailures_HookGuardConcurrent(t *testing.T) {
	var f Failures
	dispatch := f.Recording(failing, &recordingLogger{})
	var calls atomic.Int64
	hook := func(error, any) {
		calls.Add(1)
		_ = dispatch(context.Background(), namedEvent{name: "nested"})
	}
	f.SetHook(hook)
	const goroutines, per = 16, 200
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < per; i++ {
				if g == 0 && i%20 == 0 {
					f.SetHook(hook)
				}
				_ = dispatch(context.Background(), namedEvent{name: "outer"})
			}
		}(g)
	}
	wg.Wait()
	if got := calls.Load(); got != goroutines*per {
		t.Errorf("hook calls = %d, want %d (one per outer failure, none for nested)", got, goroutines*per)
	}
	if got := f.Count(); got != 2*goroutines*per {
		t.Errorf("Count = %d, want %d", got, 2*goroutines*per)
	}
}
