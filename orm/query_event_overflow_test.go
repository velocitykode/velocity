package orm

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventemit"
)

// A statement whose event overflows the full delivery queue returns at
// once, with a one-connection pool and a failure hook that itself runs a
// statement on that pool: the drop is counted inside the driver callback,
// and its line and hook run later, after the callback released the
// connection. The hook runs once per drop, and a drop its own statement
// causes is counted but not handed to it.
func TestQueryEventPump_OverflowHookMayUseTheDatabase(t *testing.T) {
	m := newTestManager(t)
	m.DB().SetMaxOpenConns(1)
	m.SetLogger(&fakeLogger{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer func() {
		unblock()
		_ = m.Shutdown(context.Background())
	}()

	shared := &eventemit.Failures{}
	var hookCalls atomic.Int32
	hookExec := make(chan error, 1)
	shared.SetHook(func(err error, _ any) {
		if !errors.Is(err, ErrQueryEventQueueFull) {
			return
		}
		if hookCalls.Add(1) == 1 {
			ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
			defer cancel()
			_, execErr := m.Exec(ctx, "SELECT 1")
			hookExec <- execErr
		}
	})
	m.ShareEventFailures(shared)

	var first sync.Once
	entered := make(chan struct{})
	m.SetEventDispatcher(func(context.Context, any) error {
		first.Do(func() { close(entered) })
		<-release
		return nil
	})
	if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	<-entered // the pump is blocked in the listener; the queue now fills

	const overflow = 10
	done := make(chan error, 1)
	go func() {
		for i := 0; i < queryEventQueueSize+overflow; i++ {
			if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("exec: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a statement whose event overflowed the queue did not return: the failure hook ran inside the driver callback, which holds the pool's one connection")
	}

	select {
	case err := <-hookExec:
		if err != nil {
			t.Fatalf("the hook's statement failed: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the hook's statement did not complete")
	}
	for deadline := time.Now().Add(3 * time.Second); hookCalls.Load() < overflow && time.Now().Before(deadline); time.Sleep(2 * time.Millisecond) {
	}
	if got := hookCalls.Load(); got != overflow {
		t.Errorf("hook calls = %d, want %d (one per drop, none for the drop the hook's statement caused)", got, overflow)
	}
	if got := shared.Count(); got != overflow+1 {
		t.Errorf("failure count = %d, want %d (the drops plus the hook statement's drop)", got, overflow+1)
	}
}

// FlushQueryEvents returns once the drops recorded before it have been
// handed to the hook, and Shutdown hands over the ones still pending.
func TestQueryEventPump_FlushAndShutdownDeliverDropReports(t *testing.T) {
	for _, name := range []string{"flush", "shutdown"} {
		t.Run(name, func(t *testing.T) {
			m := newTestManager(t)
			m.SetLogger(&fakeLogger{})
			shared := &eventemit.Failures{}
			var hooked atomic.Int32
			shared.SetHook(func(error, any) { hooked.Add(1) })
			m.ShareEventFailures(shared)
			release := make(chan struct{})
			var first sync.Once
			entered := make(chan struct{})
			m.SetEventDispatcher(func(context.Context, any) error {
				first.Do(func() { close(entered) })
				<-release
				return nil
			})
			if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
				t.Fatalf("exec: %v", err)
			}
			<-entered
			for i := 0; i < queryEventQueueSize+5; i++ {
				if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
					t.Fatalf("exec: %v", err)
				}
			}
			close(release)
			if name == "flush" {
				if err := m.FlushQueryEvents(context.Background()); err != nil {
					t.Fatalf("flush: %v", err)
				}
				defer m.Shutdown(context.Background())
			} else if err := m.Shutdown(context.Background()); err != nil {
				t.Fatalf("shutdown: %v", err)
			}
			if got, want := hooked.Load(), int32(shared.Count()); got != want || got != 5 {
				t.Errorf("hook calls = %d, count = %d, want 5 and 5", got, want)
			}
		})
	}
}

// Overflowing statements from many goroutines race with FlushQueryEvents
// (run under -race): every drop is counted once inside its callback and
// handed to the hook once by the reporter, and Shutdown runs the reports
// still pending.
func TestQueryEventPump_ConcurrentOverflowAndFlush(t *testing.T) {
	m := newTestManager(t)
	m.SetLogger(&fakeLogger{})
	shared := &eventemit.Failures{}
	var hooked atomic.Int64
	shared.SetHook(func(error, any) { hooked.Add(1) })
	m.ShareEventFailures(shared)
	release := make(chan struct{})
	var first sync.Once
	entered := make(chan struct{})
	m.SetEventDispatcher(func(context.Context, any) error {
		first.Do(func() { close(entered) })
		<-release
		return nil
	})
	if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	<-entered

	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
					t.Errorf("exec: %v", err)
					return
				}
			}
		}()
	}
	flushCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = m.FlushQueryEvents(flushCtx) // blocked behind the listener until ctx ends
		}()
	}
	wg.Wait()
	close(release)
	if err := m.Shutdown(context.Background()); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	if shared.Count() == 0 {
		t.Fatal("no statement event was dropped")
	}
	if got, want := hooked.Load(), int64(shared.Count()); got != want {
		t.Errorf("hook calls = %d, want %d (one per drop)", got, want)
	}
}

// The pump's reporter goroutine reads the manager's logger (Manager.log,
// under mu) while SetLogger and AddConnection hand loggers off under the
// wiring mutex, and a hook that itself calls SetLogger runs on it; flushes
// and Shutdown race all of them (run under -race). Nothing deadlocks, and
// every drop is counted and hooked once.
func TestQueryEventPump_ReporterRacesTheLoggerHandoff(t *testing.T) {
	m := newTestManager(t)
	loggers := []contract.Logger{&fakeLogger{}, &fakeLogger{}, nil}
	shared := &eventemit.Failures{}
	var hooked atomic.Int64
	shared.SetHook(func(error, any) {
		if hooked.Add(1)%7 == 0 {
			m.SetLogger(loggers[0])
		}
	})
	m.ShareEventFailures(shared)
	release := make(chan struct{})
	var first sync.Once
	entered := make(chan struct{})
	m.SetEventDispatcher(func(context.Context, any) error {
		first.Do(func() { close(entered) })
		<-release
		return nil
	})
	if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	<-entered

	var wg sync.WaitGroup
	for g := 0; g < 4; g++ {
		wg.Add(3)
		go func() {
			defer wg.Done()
			for i := 0; i < 400; i++ {
				if _, err := m.Exec(context.Background(), "SELECT 1"); err != nil {
					t.Errorf("exec: %v", err)
					return
				}
			}
		}()
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				m.SetLogger(loggers[(g+i)%len(loggers)])
				m.AddConnection(fmt.Sprintf("seam-%d-%d", g, i), &gateDriver{})
			}
		}(g)
		go func() {
			defer wg.Done()
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
			defer cancel()
			_ = m.FlushQueryEvents(ctx)
		}()
	}
	wg.Wait()
	close(release)

	done := make(chan error, 1)
	go func() {
		if err := m.FlushQueryEvents(context.Background()); err != nil {
			done <- err
			return
		}
		done <- m.Shutdown(context.Background())
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("flush/shutdown: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("FlushQueryEvents or Shutdown deadlocked against the logger handoff")
	}
	if shared.Count() == 0 {
		t.Fatal("no statement event was dropped")
	}
	if got, want := hooked.Load(), int64(shared.Count()); got != want {
		t.Errorf("hook calls = %d, want %d (one per drop)", got, want)
	}
}
