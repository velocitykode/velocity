package scheduler

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// startLineGate is a logger that holds the first "Scheduler started" line
// until released, so a test can keep a run's loop between its start and
// its select; it counts the started lines.
type startLineGate struct {
	contract.Logger
	first   atomic.Bool
	entered chan struct{}
	release chan struct{}
	started atomic.Int32
}

func newStartLineGate() *startLineGate {
	return &startLineGate{Logger: nopLogger{}, entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *startLineGate) Info(msg string, kvs ...any) {
	if msg != "Scheduler started" {
		return
	}
	g.started.Add(1)
	if g.first.CompareAndSwap(false, true) {
		close(g.entered)
		<-g.release
	}
}

func (g *startLineGate) With(...any) contract.Logger { return g }

// nopLogger discards every line.
type nopLogger struct{}

func (nopLogger) Debug(string, ...any)        {}
func (nopLogger) Info(string, ...any)         {}
func (nopLogger) Warn(string, ...any)         {}
func (nopLogger) Error(string, ...any)        {}
func (nopLogger) Fatal(string, ...any)        {}
func (nopLogger) With(...any) contract.Logger { return nopLogger{} }

// An old run's loop never stops a newer run. The first run's loop is held
// before its select while Shutdown stops it and a second Run starts; then
// its ctx ends and it is released. Its select sees both its stop and its
// ctx: whichever it takes, the second run keeps running. The choice is
// random, so the scenario repeats.
func TestRun_OldLoopNeverStopsANewerRun(t *testing.T) {
	for i := range 20 {
		synctest.Test(t, func(t *testing.T) {
			g := newStartLineGate()
			s := New()
			s.SetLogger(g)
			ctx1, cancel1 := context.WithCancel(context.Background())
			ctx2, cancel2 := context.WithCancel(context.Background())
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(g.release) }) }
			// Every goroutine of the bubble must end, whatever failed.
			defer func() {
				release()
				cancel1()
				cancel2()
				_ = s.Shutdown(context.Background())
			}()
			first := make(chan error, 1)
			go func() { first <- s.Run(ctx1) }()
			<-g.entered

			shut := make(chan error, 1)
			go func() { shut <- s.Shutdown(context.Background()) }()
			// The first run is stopped (its loop is not part of its drain)
			// before the second one starts.
			if err := <-shut; err != nil {
				t.Fatalf("iteration %d: Shutdown = %v", i, err)
			}
			second := make(chan error, 1)
			go func() { second <- s.Run(ctx2) }()
			synctest.Wait()
			cancel1()
			release()
			<-first
			synctest.Wait()
			if g.started.Load() != 2 {
				t.Fatalf("iteration %d: %d runs started, want 2", i, g.started.Load())
			}
			select {
			case err := <-second:
				t.Fatalf("iteration %d: the second run ended (%v): the first run's loop stopped it", i, err)
			default:
			}
			if err := s.Shutdown(context.Background()); err != nil {
				t.Fatalf("iteration %d: Shutdown of the second run = %v", i, err)
			}
			if err := <-second; err != nil {
				t.Fatalf("iteration %d: second Run = %v", i, err)
			}
		})
	}
}

// reentrantCtx calls fn from its first Done or Value, as a context whose
// methods call back into their owner do.
type reentrantCtx struct {
	context.Context
	once sync.Once
	fn   func()
}

func (c *reentrantCtx) Done() <-chan struct{} {
	c.once.Do(c.fn)
	return c.Context.Done()
}

func (c *reentrantCtx) Value(key any) any {
	c.once.Do(c.fn)
	return c.Context.Value(key)
}

// Run derives its run context outside the scheduler's lock: a ctx whose
// Done shuts the scheduler down neither deadlocks Run nor is lost (a
// Shutdown before the scheduler ever ran keeps it from starting).
func TestRun_WithACtxThatShutsTheSchedulerDown(t *testing.T) {
	s := New()
	var shutErr error
	ctx := &reentrantCtx{Context: context.Background(), fn: func() { shutErr = s.Shutdown(context.Background()) }}
	var runErr error
	hostile.Within(t, hostile.Deadline, func() { runErr = s.Run(ctx) })
	if runErr != nil || shutErr != nil {
		t.Fatalf("Run = %v, Shutdown from its ctx = %v", runErr, shutErr)
	}
	s.mu.RLock()
	terminated := s.terminated
	s.mu.RUnlock()
	if !terminated {
		t.Fatal("a Shutdown before the first Run did not keep the scheduler from starting")
	}
}

// The same for a scheduler that ran before: the Shutdown lands on no
// running run, and the new run starts.
func TestRun_AgainWithACtxThatShutsTheSchedulerDown(t *testing.T) {
	s := New()
	first := make(chan error, 1)
	go func() { first <- s.Run(context.Background()) }()
	hostile.Eventually(t, hostile.Deadline, "the first run started", func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.running
	})
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	<-first
	var shutErr error
	ctx := &reentrantCtx{Context: context.Background(), fn: func() { shutErr = s.Shutdown(context.Background()) }}
	second := make(chan error, 1)
	go func() { second <- s.Run(ctx) }()
	hostile.Eventually(t, hostile.Deadline, "the second run started", func() bool {
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.running
	})
	if shutErr != nil {
		t.Fatalf("Shutdown from the ctx = %v", shutErr)
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	var err error
	hostile.Within(t, hostile.Deadline, func() { err = <-second })
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("second Run = %v", err)
	}
}
