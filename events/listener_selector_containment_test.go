package events

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// selectorChildEnv names the scenario a re-executed test binary runs.
const selectorChildEnv = "VELOCITY_SELECTOR_PANIC_CHILD"

// asyncPanicListener panics when asked whether it is queued.
type asyncPanicListener struct{}

func (asyncPanicListener) Handle(context.Context, interface{}) error { return nil }
func (asyncPanicListener) Async() bool                               { panic("Async broke") }

// afterCommitPanicListener panics when asked whether it waits for commit.
type afterCommitPanicListener struct{}

func (afterCommitPanicListener) Handle(context.Context, interface{}) error { return nil }
func (afterCommitPanicListener) Async() bool                               { return false }
func (afterCommitPanicListener) ShouldDispatchAfterCommit() bool {
	panic("ShouldDispatchAfterCommit broke")
}

// tallyListener counts the events it handles.
type tallyListener struct{ n *atomic.Int32 }

func (l tallyListener) Handle(context.Context, interface{}) error { l.n.Add(1); return nil }
func (tallyListener) Async() bool                                 { return false }

// panickingNameEvent is an event whose Name panics.
type panickingNameEvent struct{}

func (panickingNameEvent) Name() string { panic("Name broke") }

// selectorScenario delivers one event detached, the way name says, to a
// listener whose selector (Async, ShouldDispatchAfterCommit) panics beside
// a healthy listener, or as an event whose Name panics, and returns how
// many deliveries were recorded and how many events the healthy listener
// handled.
func selectorScenario(name string) (recorded, handled int32) {
	var calls, seen atomic.Int32
	parts := strings.SplitN(name, "/", 2)
	wire := func(d *DefaultDispatcher) {
		d.SetDetachedFailureRecorder(func(context.Context, error, any) { calls.Add(1) })
		switch parts[0] {
		case "async":
			d.Listen("evt", asyncPanicListener{})
		case "after-commit":
			d.Listen("evt", afterCommitPanicListener{})
		}
		d.Listen("evt", tallyListener{n: &seen})
	}
	var event any = "evt"
	if parts[0] == "name" {
		event = panickingNameEvent{}
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
		if parts[0] == "name" {
			// Debounce keys by name; a panicking Name fails the caller.
			return 1, 0
		}
		_ = d.Dispatch(ctx, event)
		stop = d.Stop
	case "coalesce":
		d := NewCoalescingDispatcher(time.Millisecond)
		wire(d.DefaultDispatcher)
		if parts[0] == "name" {
			return 1, 0
		}
		_ = d.Dispatch(ctx, event)
		stop = d.Stop
	}
	waitFor(func() bool { return calls.Load() > 0 })
	// Give an escaped panic time to end the process.
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
// still run, and the delivery is recorded once.
func TestDetachedDelivery_PanickingSelectorIsContained(t *testing.T) {
	if name := os.Getenv(selectorChildEnv); name != "" {
		recorded, handled := selectorScenario(name)
		fmt.Printf("recorded=%d handled=%d\n", recorded, handled)
		return
	}
	for _, name := range []string{
		// DispatchAsync without a queue delivers every listener inline and
		// never asks it Async or ShouldDispatchAfterCommit, so only a
		// panicking Name reaches its goroutine.
		"async/after", "async/debounce", "async/coalesce",
		"after-commit/after", "after-commit/debounce", "after-commit/coalesce",
		"name/after", "name/async",
	} {
		t.Run(name, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run", "^TestDetachedDelivery_PanickingSelectorIsContained$", "-test.count=1")
			cmd.Env = append(os.Environ(), selectorChildEnv+"="+name)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("the panic ended the process: %v\n%s", err, out)
			}
			want := "recorded=1 handled=1\n"
			if strings.HasPrefix(name, "name/") {
				want = "recorded=1 handled=0\n"
			}
			if !strings.Contains(string(out), want) {
				t.Errorf("want %q in\n%s", want, out)
			}
		})
	}
}

// panickingQueue is a QueueDispatcher whose Push panics.
type panickingQueue struct{}

func (panickingQueue) Push(context.Context, interface{}, Listener, time.Duration) error {
	panic("Push broke")
}

// queuedCounting is a queued listener that counts the events it handles.
type queuedCounting struct{ tallyListener }

func (queuedCounting) Async() bool { return true }

// On a synchronous dispatch, a listener whose selector panics, or a queue
// whose Push panics, fails that listener only: the caller gets the
// recovered panic as an error and the other listeners still run.
func TestDispatch_PanickingSelectorOrQueueFailsOnlyThatListener(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		setup func(d *DefaultDispatcher)
		call  func(d *DefaultDispatcher) error
	}{
		{"Dispatch/Async", func(d *DefaultDispatcher) { d.Listen("evt", asyncPanicListener{}) },
			func(d *DefaultDispatcher) error { return d.Dispatch(ctx, "evt") }},
		{"Dispatch/ShouldDispatchAfterCommit", func(d *DefaultDispatcher) { d.Listen("evt", afterCommitPanicListener{}) },
			func(d *DefaultDispatcher) error { return d.Dispatch(ctx, "evt") }},
		{"Dispatch/Push", func(d *DefaultDispatcher) {
			d.SetQueueDispatcher(panickingQueue{})
			d.Listen("evt", queuedCounting{})
		}, func(d *DefaultDispatcher) error { return d.Dispatch(ctx, "evt") }},
		{"DispatchAsync/Push", func(d *DefaultDispatcher) {
			d.SetQueueDispatcher(panickingQueue{})
			d.Listen("evt", queuedCounting{})
		}, func(d *DefaultDispatcher) error { return d.DispatchAsync(ctx, "evt") }},
		{"DispatchAfter/Push", func(d *DefaultDispatcher) {
			d.SetQueueDispatcher(panickingQueue{})
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
	for _, tc := range []struct {
		name     string
		dispatch func(seen *atomic.Int32) error
	}{
		{"QueueIntegrated/Async", func(seen *atomic.Int32) error {
			d := NewQueueIntegratedDispatcher()
			d.Listen("evt", asyncPanicListener{})
			d.Listen("evt", tallyListener{n: seen})
			return d.Dispatch(ctx, "evt")
		}},
		{"QueueIntegrated/ShouldDispatchAfterCommit", func(seen *atomic.Int32) error {
			d := NewQueueIntegratedDispatcher()
			d.Listen("evt", afterCommitPanicListener{})
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
			d.Listen("evt", asyncPanicListener{})
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
