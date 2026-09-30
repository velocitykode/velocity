package orm

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// doneSignalCtx is a context that reports, once, the first call to Done:
// the caller has started waiting on it.
type doneSignalCtx struct {
	context.Context
	once    sync.Once
	waiting chan struct{}
}

func newDoneSignalCtx() *doneSignalCtx {
	return &doneSignalCtx{Context: context.Background(), waiting: make(chan struct{})}
}

func (c *doneSignalCtx) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

// startBlockedRelay starts a relay whose job callback blocks until the
// returned release is called, and waits until a dispatch is inside it.
func startBlockedRelay(t *testing.T) (*Relay, func()) {
	t.Helper()
	m, _ := newOutboxFileManager(t)
	enqueueOne(t, m)
	entered := make(chan struct{})
	var once sync.Once
	gate := make(chan struct{})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(gate) }) }
	t.Cleanup(release)
	relay := NewRelay(m, RelayCallbacks{OnJob: func(context.Context, any, string, string) error {
		once.Do(func() { close(entered) })
		<-gate // ignores ctx
		return nil
	}}, RelayConfig{PollInterval: 5 * time.Millisecond, ShutdownGrace: time.Hour})
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { release(); _ = relay.Stop(context.Background()) })
	<-entered
	return relay, release
}

// A Stop after one that timed out waits for the dispatch the first one
// admitted: nil means every dispatch finished, never that a stop was
// already under way.
func TestRelay_SecondStopWaitsForTheAdmittedDispatch(t *testing.T) {
	relay, release := startBlockedRelay(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := relay.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Stop = %v, want the ctx error while a dispatch runs", err)
	}
	second := newDoneSignalCtx()
	got := make(chan error, 1)
	go func() { got <- relay.Stop(second) }()
	select {
	case err := <-got:
		t.Fatalf("second Stop = %v while the dispatch still runs; want it to wait", err)
	case <-second.waiting:
	}
	release()
	if err := <-got; err != nil {
		t.Fatalf("second Stop after the dispatch finished = %v, want nil", err)
	}
}

// A Start while the previous run still drains waits for it, bounded by its
// own ctx: it never runs beside the old run, whose goroutines would
// otherwise act on the new one.
func TestRelay_StartWaitsForThePreviousRun(t *testing.T) {
	relay, release := startBlockedRelay(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := relay.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Stop = %v, want the ctx error while a dispatch runs", err)
	}
	if err := relay.Start(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Start with a done ctx while the previous run drains = %v, want the ctx error", err)
	}
	started := make(chan error, 1)
	go func() { started <- relay.Start(context.Background()) }()
	release()
	if err := <-started; err != nil {
		t.Fatalf("Start once the previous run finished = %v", err)
	}
	if err := relay.Stop(context.Background()); err != nil {
		t.Fatalf("Stop of the new run = %v", err)
	}
}

// A Start called from a dispatch of the run that still drains would wait
// on itself: it is refused at once.
func TestRelay_StartFromADrainingRunsCallbackIsRefused(t *testing.T) {
	m, _ := newOutboxFileManager(t)
	enqueueOne(t, m)
	var relay *Relay
	entered := make(chan struct{})
	stopped := make(chan struct{})
	inner := make(chan error, 1)
	relay = NewRelay(m, RelayCallbacks{OnJob: func(context.Context, any, string, string) error {
		close(entered)
		<-stopped
		inner <- relay.Start(context.Background())
		return nil
	}}, RelayConfig{PollInterval: 5 * time.Millisecond, ShutdownGrace: time.Hour})
	if err := relay.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	<-entered
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_ = relay.Stop(ctx)
	close(stopped)
	var err error
	hostile.Within(t, hostile.Deadline, func() { err = <-inner })
	if !errors.Is(err, contract.ErrStopFromOwnWork) {
		t.Fatalf("Start from the draining run's callback = %v, want ErrStopFromOwnWork", err)
	}
	if err := relay.Stop(context.Background()); err != nil {
		t.Fatalf("Stop = %v", err)
	}
}

// reentrantCtx calls fn from its first Done, as a context that calls back
// into its owner does.
type reentrantCtx struct {
	context.Context
	once sync.Once
	fn   func()
}

func (c *reentrantCtx) Done() <-chan struct{} {
	c.once.Do(c.fn)
	return c.Context.Done()
}

// Start derives its context outside the relay's lock: a ctx whose Done
// stops the relay neither deadlocks Start nor is lost.
func TestRelay_StartWithACtxThatStopsTheRelay(t *testing.T) {
	m, _ := newOutboxFileManager(t)
	relay := fastRelay(m, RelayCallbacks{})
	var stopErr error
	ctx := &reentrantCtx{Context: context.Background(), fn: func() { stopErr = relay.Stop(context.Background()) }}
	var startErr error
	hostile.Within(t, hostile.Deadline, func() { startErr = relay.Start(ctx) })
	if startErr != nil || stopErr != nil {
		t.Fatalf("Start = %v, Stop from its ctx = %v", startErr, stopErr)
	}
	if err := relay.Stop(context.Background()); err != nil {
		t.Fatalf("Stop = %v", err)
	}
}
