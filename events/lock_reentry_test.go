package events

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
)

// selfDispatchingNameEvent is an event whose Name dispatches through the
// dispatcher it is registered or dispatched on.
type selfDispatchingNameEvent struct{ d Dispatcher }

func (e selfDispatchingNameEvent) Name() string {
	if e.d != nil {
		_ = e.d.Dispatch(context.Background(), "inner")
	}
	return "outer"
}

// within fails t when fn does not return within two seconds: the user code
// fn runs called back into the component that called it, under a lock
// that component held.
func within(t *testing.T, what string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		defer close(done)
		fn()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("%s deadlocked on user code calling back into it", what)
	}
}

// Listen resolves an event value's name before it takes the dispatcher's
// lock, so a Name that dispatches does not deadlock the registration.
func TestListen_EventValueNameMayDispatch(t *testing.T) {
	d := NewDispatcher()
	within(t, "Listen", func() {
		d.Listen(selfDispatchingNameEvent{d: d}, &BaseListener{})
	})
	if !d.HasListeners("outer") {
		t.Error("listener not registered under the event's name")
	}
}

// LoggingMiddleware resolves the event's name outside its lock, so a Name
// that dispatches through the same middleware does not deadlock.
func TestLoggingMiddleware_EventNameMayDispatch(t *testing.T) {
	d := NewMiddlewareDispatcher()
	mw := NewLoggingMiddleware()
	d.Use(mw)
	within(t, "LoggingMiddleware", func() {
		_ = d.Dispatch(context.Background(), selfDispatchingNameEvent{d: d})
	})
	// The outer event is logged once; each resolution of its name (the
	// middleware's and the dispatcher's) logs one inner dispatch.
	if got := len(mw.GetLog()); got != 3 {
		t.Errorf("log lines = %d, want 3", got)
	}
}

// The fake's assertions call the matcher and the callback without its
// lock, so a callback, or an event Name, that dispatches on the same fake
// does not deadlock.
func TestFakeDispatcher_AssertionsMayDispatch(t *testing.T) {
	f := NewFakeDispatcher()
	_ = f.Dispatch(context.Background(), selfDispatchingNameEvent{d: f})
	within(t, "AssertDispatched", func() {
		if err := f.AssertDispatched("outer", func(interface{}) bool {
			_ = f.Dispatch(context.Background(), "again")
			return true
		}); err != nil {
			t.Errorf("AssertDispatched = %v", err)
		}
	})
	within(t, "AssertDispatchedTimes", func() { _ = f.AssertDispatchedTimes("outer", 1) })
	within(t, "AssertNotDispatched", func() { _ = f.AssertNotDispatched("never") })
}

// stoppableFailure is a stoppable failure event.
type stoppableFailure struct{ BaseStoppableEvent }

func (*stoppableFailure) FailureError() error                 { return errListenerBroke }
func (*stoppableFailure) FailureSource() contract.ErrorSource { return contract.ErrorSourceJob }

// The stop-propagation dispatcher reports a failure event through the
// failure-report bridge at the point of dispatch, once, like every other
// dispatch path.
func TestStoppableDispatch_ReportsAFailureEvent(t *testing.T) {
	d := NewStoppablePropagationDispatcher()
	var reported atomic.Int32
	d.SetFailureReporter(func(context.Context, any, error) { reported.Add(1) })
	if err := d.Dispatch(context.Background(), &stoppableFailure{}); err != nil {
		t.Fatalf("Dispatch = %v", err)
	}
	if got := reported.Load(); got != 1 {
		t.Errorf("reports = %d, want 1", got)
	}
}
