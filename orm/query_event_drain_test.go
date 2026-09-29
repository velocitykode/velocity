package orm

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/eventemit"
)

// blockPump installs a dispatcher that blocks in its first delivery until
// the returned release runs, waits for that delivery to start, then runs
// extra statements whose events queue behind it (dropping those beyond
// the queue). release is safe to call more than once.
func blockPump(t *testing.T, m *Manager, extra int) (release func()) {
	t.Helper()
	gate := make(chan struct{})
	var once, first sync.Once
	entered := make(chan struct{})
	m.SetEventDispatcher(func(context.Context, any) error {
		first.Do(func() { close(entered) })
		<-gate
		return nil
	})
	if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	<-entered
	for i := 0; i < extra; i++ {
		if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	return func() { once.Do(func() { close(gate) }) }
}

// within runs fn and fails the test when it has not returned by d.
func within(t *testing.T, d time.Duration, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(d):
		t.Fatalf("%s did not return within %v", what, d)
	}
}

// A drop hook runs on the pump's reporter goroutine: a flush or Shutdown it
// asks for would wait on the goroutine running it. Both are refused at
// once with ErrQueryEventsFlushFromPump, before Shutdown changes anything:
// the manager stays open, and a later Shutdown from outside drains and
// closes it.
func TestQueryEventPump_FlushAndShutdownFromTheHookAreRefused(t *testing.T) {
	m := newTestManager(t)
	m.SetLogger(&fakeLogger{})
	shared := &eventemit.Failures{}
	type result struct{ flush, shutdown error }
	results := make(chan result, 1)
	var once sync.Once
	shared.SetHook(func(err error, _ any) {
		if !errors.Is(err, ErrQueryEventQueueFull) {
			return
		}
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			var r result
			r.flush = m.FlushQueryEvents(ctx)
			r.shutdown = m.Shutdown(ctx)
			results <- r
		})
	})
	m.ShareEventFailures(shared)
	release := blockPump(t, m, queryEventQueueSize+1)
	defer release()

	var r result
	select {
	case r = <-results:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("the hook's flush or Shutdown did not return: it waited on the reporter goroutine running the hook")
	}
	if !errors.Is(r.flush, ErrQueryEventsFlushFromPump) {
		t.Errorf("flush from the hook = %v, want ErrQueryEventsFlushFromPump", r.flush)
	}
	if !errors.Is(r.shutdown, ErrQueryEventsFlushFromPump) {
		t.Errorf("Shutdown from the hook = %v, want ErrQueryEventsFlushFromPump", r.shutdown)
	}
	if err := m.Ping(); err != nil {
		t.Errorf("after the refused Shutdown: Ping = %v, want the manager still open", err)
	}
	release()
	within(t, 5*time.Second, "Shutdown", func() {
		if err := m.Shutdown(context.Background()); err != nil {
			t.Errorf("Shutdown from outside = %v, want nil", err)
		}
	})
	if err := m.Ping(); !errors.Is(err, ErrManagerShutdown) {
		t.Errorf("after Shutdown: Ping = %v, want ErrManagerShutdown", err)
	}
}

// A listener runs on the pump's delivery goroutine: its flush and Shutdown
// are refused the same way.
func TestQueryEventPump_FlushAndShutdownFromAListenerAreRefused(t *testing.T) {
	m := newTestManager(t)
	var flushErr, shutdownErr atomic.Pointer[error]
	done := make(chan struct{})
	var once sync.Once
	m.SetEventDispatcher(func(context.Context, any) error {
		once.Do(func() {
			defer close(done)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			f := m.FlushQueryEvents(ctx)
			s := m.Shutdown(ctx)
			flushErr.Store(&f)
			shutdownErr.Store(&s)
		})
		return nil
	})
	if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("the listener's flush or Shutdown did not return: it waited on the delivery goroutine running it")
	}
	if err := *flushErr.Load(); !errors.Is(err, ErrQueryEventsFlushFromPump) {
		t.Errorf("flush from a listener = %v, want ErrQueryEventsFlushFromPump", err)
	}
	if err := *shutdownErr.Load(); !errors.Is(err, ErrQueryEventsFlushFromPump) {
		t.Errorf("Shutdown from a listener = %v, want ErrQueryEventsFlushFromPump", err)
	}
	if err := m.Ping(); err != nil {
		t.Errorf("after the refused Shutdown: Ping = %v, want the manager still open", err)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Errorf("Shutdown from outside = %v, want nil", err)
	}
}

// A Shutdown whose ctx ends before the queued statement events are
// delivered still closes the connections, and returns the drain's error
// instead of reporting success.
func TestManagerShutdown_ReturnsAnUnfinishedDrain(t *testing.T) {
	m := newTestManager(t)
	release := blockPump(t, m, 3)
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := m.Shutdown(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Shutdown = %v, want the drain's context.DeadlineExceeded", err)
	}
	if err := m.Ping(); !errors.Is(err, ErrManagerShutdown) {
		t.Errorf("after Shutdown: Ping = %v, want ErrManagerShutdown (teardown went on)", err)
	}
	if err := m.Shutdown(context.Background()); err != nil {
		t.Errorf("second Shutdown = %v, want nil", err)
	}
}

// Under overload the drop count stays exact and the hook is best-effort:
// with the hook stuck, drops beyond the reporter's backlog are counted but
// not handed to it; once it recovers, every drop it was handed runs, and
// never more than were counted.
func TestQueryEventPump_OverloadKeepsCountsExactAndTheHookBestEffort(t *testing.T) {
	m := newTestManager(t)
	m.SetLogger(&fakeLogger{})
	shared := &eventemit.Failures{}
	hookGate := make(chan struct{})
	var hookOnce sync.Once
	releaseHook := func() { hookOnce.Do(func() { close(hookGate) }) }
	defer releaseHook()
	var hooked atomic.Int64
	shared.SetHook(func(error, any) {
		<-hookGate
		hooked.Add(1)
	})
	m.ShareEventFailures(shared)
	const drops = 2*queryEventQueueSize + 50 // queue full, then the report backlog too
	release := blockPump(t, m, queryEventQueueSize+drops)
	defer release()

	if got := shared.Count(); got != drops {
		t.Fatalf("failure count = %d, want exactly %d drops", got, drops)
	}
	releaseHook()
	release()
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	got := hooked.Load()
	if got >= drops {
		t.Errorf("hook calls = %d, want fewer than the %d drops: drops past the full backlog skip the hook", got, drops)
	}
	if got < queryEventQueueSize {
		t.Errorf("hook calls = %d, want at least the %d the backlog holds", got, queryEventQueueSize)
	}
	if count := shared.Count(); count != drops {
		t.Errorf("failure count after the hook ran = %d, want still exactly %d", count, drops)
	}
}
