package events

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
)

// asyncPanicListener runs its hostile code when asked whether it is queued.
type asyncPanicListener struct{ c *hostile.Code }

func (asyncPanicListener) Handle(context.Context, interface{}) error { return nil }
func (l asyncPanicListener) Async() bool                             { l.c.Run(); return false }

// afterCommitPanicListener runs its hostile code when asked whether it
// waits for commit.
type afterCommitPanicListener struct{ c *hostile.Code }

func (afterCommitPanicListener) Handle(context.Context, interface{}) error { return nil }
func (afterCommitPanicListener) Async() bool                               { return false }
func (l afterCommitPanicListener) ShouldDispatchAfterCommit() bool {
	l.c.Run()
	return false
}

// tallyListener counts the events it handles.
type tallyListener struct{ n *atomic.Int32 }

func (l tallyListener) Handle(context.Context, interface{}) error { l.n.Add(1); return nil }
func (tallyListener) Async() bool                                 { return false }

// panickingNameEvent is an event whose Name runs its hostile code.
type panickingNameEvent struct{ c *hostile.Code }

func (e panickingNameEvent) Name() string { e.c.Run(); return "evt" }

// selectorScenario delivers one event detached, the way name says, to a
// listener whose selector (Async, ShouldDispatchAfterCommit) panics beside
// a healthy listener, or as an event whose Name panics, and returns how
// many deliveries were recorded and how many events the healthy listener
// handled.
func selectorScenario(t *testing.T, name string) (recorded, handled int32) {
	var calls, seen atomic.Int32
	panics := hostile.New(t, hostile.Panic, nil)
	parts := strings.SplitN(name, "/", 2)
	wire := func(d *DefaultDispatcher) {
		d.SetDetachedFailureRecorder(func(context.Context, error, any) { calls.Add(1) })
		switch parts[0] {
		case "async":
			d.Listen("evt", asyncPanicListener{c: panics})
		case "after-commit":
			d.Listen("evt", afterCommitPanicListener{c: panics})
		}
		d.Listen("evt", tallyListener{n: &seen})
	}
	var event any = "evt"
	if parts[0] == "name" {
		event = panickingNameEvent{c: panics}
	}
	ctx := context.Background()
	var stop func()
	switch parts[1] {
	case "after":
		d := NewDispatcher()
		wire(d)
		_ = d.DispatchAfter(ctx, event, time.Millisecond)
	case "async":
		d := NewDispatcher()
		wire(d)
		_ = d.DispatchAsync(ctx, event)
	case "debounce":
		d := NewDebouncingDispatcher(time.Millisecond)
		wire(d.DefaultDispatcher)
		_ = d.Dispatch(ctx, event)
		stop = d.Stop
	case "coalesce":
		d := NewCoalescingDispatcher(time.Millisecond)
		wire(d.DefaultDispatcher)
		_ = d.Dispatch(ctx, event)
		stop = d.Stop
	}
	waitFor(func() bool { return calls.Load() > 0 })
	// Give an escaped panic time to end the process; a slow machine can
	// only make this pass falsely.
	time.Sleep(50 * time.Millisecond)
	if stop != nil {
		stop()
	}
	return calls.Load(), seen.Load()
}

// A listener whose Async or ShouldDispatchAfterCommit panics, or an event
// whose Name panics, on a detached delivery (a DispatchAfter or
// DispatchAsync without a queue, a debounced or coalesced delivery) has no
// caller left to receive the panic: it is contained, the other listeners
// still run, and the delivery is recorded once. Each scenario runs in a
// child process, so a panic that escapes fails that scenario only.
func TestDetachedDelivery_PanickingSelectorIsContained(t *testing.T) {
	for _, name := range []string{
		// DispatchAsync without a queue delivers every listener inline and
		// never asks it Async or ShouldDispatchAfterCommit, so only a
		// panicking Name reaches its goroutine. A debounced or coalesced
		// dispatch keys by name, so a panicking Name fails its caller.
		"async/after", "async/debounce", "async/coalesce",
		"after-commit/after", "after-commit/debounce", "after-commit/coalesce",
		"name/after", "name/async",
	} {
		t.Run(name, func(t *testing.T) {
			hostile.Isolated(t, func() {
				recorded, handled := selectorScenario(t, name)
				wantHandled := int32(1)
				if strings.HasPrefix(name, "name/") {
					wantHandled = 0
				}
				if recorded != 1 || handled != wantHandled {
					t.Errorf("recorded=%d handled=%d, want recorded=1 handled=%d", recorded, handled, wantHandled)
				}
			})
		})
	}
}

// panickingQueue is a QueueDispatcher whose Push runs its hostile code.
type panickingQueue struct{ c *hostile.Code }

func (q panickingQueue) Push(context.Context, interface{}, Listener, time.Duration) error {
	q.c.Run()
	return nil
}

// queuedCounting is a queued listener that counts the events it handles.
type queuedCounting struct{ tallyListener }

func (queuedCounting) Async() bool { return true }

// On a synchronous dispatch, a listener whose selector panics, or a queue
// whose Push panics, fails that listener only: the caller gets the
// recovered panic as an error and the other listeners still run.
func TestDispatch_PanickingSelectorOrQueueFailsOnlyThatListener(t *testing.T) {
	ctx := context.Background()
	panics := hostile.New(t, hostile.Panic, nil)
	for _, tc := range []struct {
		name  string
		setup func(d *DefaultDispatcher)
		call  func(d *DefaultDispatcher) error
	}{
		{"Dispatch/Async", func(d *DefaultDispatcher) { d.Listen("evt", asyncPanicListener{c: panics}) },
			func(d *DefaultDispatcher) error { return d.Dispatch(ctx, "evt") }},
		{"Dispatch/ShouldDispatchAfterCommit", func(d *DefaultDispatcher) { d.Listen("evt", afterCommitPanicListener{c: panics}) },
			func(d *DefaultDispatcher) error { return d.Dispatch(ctx, "evt") }},
		{"Dispatch/Push", func(d *DefaultDispatcher) {
			d.SetQueueDispatcher(panickingQueue{c: panics})
			d.Listen("evt", queuedCounting{})
		}, func(d *DefaultDispatcher) error { return d.Dispatch(ctx, "evt") }},
		{"DispatchAsync/Push", func(d *DefaultDispatcher) {
			d.SetQueueDispatcher(panickingQueue{c: panics})
			d.Listen("evt", queuedCounting{})
		}, func(d *DefaultDispatcher) error { return d.DispatchAsync(ctx, "evt") }},
		{"DispatchAfter/Push", func(d *DefaultDispatcher) {
			d.SetQueueDispatcher(panickingQueue{c: panics})
			d.Listen("evt", queuedCounting{})
		}, func(d *DefaultDispatcher) error { return d.DispatchAfter(ctx, "evt", time.Second) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen atomic.Int32
			d := NewDispatcher()
			tc.setup(d)
			d.Listen("evt", tallyListener{n: &seen})
			var err error
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("panic escaped the dispatch: %v", p)
					}
				}()
				err = tc.call(d)
			}()
			var rp contract.RecoveredPanic
			if !errors.As(err, &rp) {
				t.Errorf("dispatch returned %v, want the recovered panic", err)
			}
			if strings.HasPrefix(tc.name, "Dispatch/") && seen.Load() != 1 {
				t.Errorf("healthy listener handled %d events, want 1", seen.Load())
			}
		})
	}
}

// The queue-integrated and stop-propagation dispatchers contain a
// listener's whole delivery the same way: a panicking selector, queue push
// or handler fails that listener only.
func TestQueueIntegratedAndStoppable_PanickingListenerFailsOnlyThatListener(t *testing.T) {
	ctx := context.Background()
	panics := hostile.New(t, hostile.Panic, nil)
	for _, tc := range []struct {
		name     string
		dispatch func(seen *atomic.Int32) error
	}{
		{"QueueIntegrated/Async", func(seen *atomic.Int32) error {
			d := NewQueueIntegratedDispatcher()
			d.Listen("evt", asyncPanicListener{c: panics})
			d.Listen("evt", tallyListener{n: seen})
			return d.Dispatch(ctx, "evt")
		}},
		{"QueueIntegrated/ShouldDispatchAfterCommit", func(seen *atomic.Int32) error {
			d := NewQueueIntegratedDispatcher()
			d.Listen("evt", afterCommitPanicListener{c: panics})
			d.Listen("evt", tallyListener{n: seen})
			return d.Dispatch(ctx, "evt")
		}},
		{"Stoppable/Handle", func(seen *atomic.Int32) error {
			d := NewStoppablePropagationDispatcher()
			d.Listen("evt", panickingListener{})
			d.Listen("evt", tallyListener{n: seen})
			return d.Dispatch(ctx, "evt")
		}},
		{"Stoppable/Async", func(seen *atomic.Int32) error {
			d := NewStoppablePropagationDispatcher()
			d.Listen("evt", asyncPanicListener{c: panics})
			d.Listen("evt", tallyListener{n: seen})
			return d.Dispatch(ctx, "evt")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var seen atomic.Int32
			var err error
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("panic escaped the dispatch: %v", p)
					}
				}()
				err = tc.dispatch(&seen)
			}()
			var rp contract.RecoveredPanic
			if !errors.As(err, &rp) {
				t.Errorf("dispatch returned %v, want the recovered panic", err)
			}
			if seen.Load() != 1 {
				t.Errorf("healthy listener handled %d events, want 1", seen.Load())
			}
		})
	}
}
