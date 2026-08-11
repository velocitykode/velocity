package events

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/internal/panicerr"
)

// untilPanicHandleListener panics from Handle. Used to exercise the
// non-HandleWithResult branch of Until's recovery path.
type untilPanicHandleListener struct {
	called atomic.Int32
}

func (l *untilPanicHandleListener) Handle(ctx context.Context, event interface{}) error {
	l.called.Add(1)
	panic("until handle boom")
}

func (l *untilPanicHandleListener) Async() bool { return false }

// untilPanicResultListener panics from HandleWithResult. Used to
// exercise the HandleWithResult branch of Until's recovery path so the
// recover block covers both shapes.
type untilPanicResultListener struct {
	called atomic.Int32
}

func (l *untilPanicResultListener) Handle(ctx context.Context, event interface{}) error {
	return nil
}

func (l *untilPanicResultListener) HandleWithResult(ctx context.Context, event interface{}) (interface{}, error) {
	l.called.Add(1)
	panic("until result boom")
}

func (l *untilPanicResultListener) Async() bool { return false }

// untilCountingListener records invocations and returns nil so Until
// proceeds to the next listener. Lets the Until-panic tests prove that
// downstream listeners still get a chance to run when a peer panics AND
// that the panic short-circuits the chain at the panicking listener
// (matching the contract of every other "first non-nil short-circuits"
// listener pipeline in the codebase).
type untilCountingListener struct {
	called atomic.Int32
}

func (l *untilCountingListener) Handle(ctx context.Context, event interface{}) error {
	l.called.Add(1)
	return nil
}

func (l *untilCountingListener) Async() bool { return false }

// TestDispatcher_Until_RecoversHandlePanic verifies the M-05 fix: a
// listener that panics from Handle while Until is iterating must not
// unwind through the dispatcher into the caller. The recover converts
// the panic to an error (typed *panicerr.Error so callers can use
// errors.As) and returns it so Until short-circuits the chain, with
// the same contract as a listener that returned a regular error.
func TestDispatcher_Until_RecoversHandlePanic(t *testing.T) {
	d := NewDispatcher()

	panicker := &untilPanicHandleListener{}
	d.Listen("until.boom", panicker)

	// Until is documented as short-circuiting on the first non-nil
	// error/result. We additionally register a downstream listener so a
	// regression that lets the panic skip recovery AND continue
	// iteration (the opposite of what processListener does) would also
	// surface; under correct behaviour this listener must NOT run.
	downstream := &untilCountingListener{}
	d.Listen("until.boom", downstream)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Until propagated panic into caller (recover triggered): %v", r)
		}
	}()

	result, err := d.Until(context.Background(), "until.boom")
	if err == nil {
		t.Fatal("Until returned nil err after a panicking listener; recover must convert panic to error")
	}
	if result != nil {
		t.Errorf("Until returned non-nil result on panic path: %v", result)
	}
	if panicker.called.Load() != 1 {
		t.Errorf("panicker.called = %d, want 1", panicker.called.Load())
	}
	if downstream.called.Load() != 0 {
		t.Errorf("downstream.called = %d, want 0 (Until must short-circuit on the panic-derived error)", downstream.called.Load())
	}

	// The error is the typed panicerr.Error so callers can inspect the
	// raw recovered value. This is the same shape processListener uses,
	// keeping the two recover paths aligned.
	var pe *panicerr.Error
	if !errors.As(err, &pe) {
		t.Fatalf("Until error is not *panicerr.Error: %T %v", err, err)
	}
	if !strings.Contains(pe.Error(), "until handle boom") {
		t.Errorf("recovered panic message lost: %q", pe.Error())
	}

	// Dispatcher remains usable after the panic. A follow-up Dispatch
	// must succeed, mirroring TestDispatcher_DispatchAsync_Fallback_RecoversPanic.
	var followup atomic.Int32
	d.Listen("until.followup", listenerFunc(func(ctx context.Context, event interface{}) error {
		followup.Add(1)
		return nil
	}))
	if err := d.Dispatch(context.Background(), "until.followup"); err != nil {
		t.Fatalf("Dispatch after Until-panic failed: %v", err)
	}
	if followup.Load() != 1 {
		t.Fatalf("follow-up listener did not run after Until-panic: got %d", followup.Load())
	}
}

// TestDispatcher_Until_RecoversHandleWithResultPanic covers the
// HandleWithResult branch of safeInvokeForUntil. Until consults the
// HandleWithResult interface first; a panic from that method follows
// the same recovery contract as Handle.
func TestDispatcher_Until_RecoversHandleWithResultPanic(t *testing.T) {
	d := NewDispatcher()

	panicker := &untilPanicResultListener{}
	d.Listen("until.result.boom", panicker)

	downstream := &untilCountingListener{}
	d.Listen("until.result.boom", downstream)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Until propagated HandleWithResult panic into caller: %v", r)
		}
	}()

	result, err := d.Until(context.Background(), "until.result.boom")
	if err == nil {
		t.Fatal("Until returned nil err after HandleWithResult panic")
	}
	if result != nil {
		t.Errorf("Until returned non-nil result on HandleWithResult panic path: %v", result)
	}
	if panicker.called.Load() != 1 {
		t.Errorf("panicker.called = %d, want 1", panicker.called.Load())
	}
	if downstream.called.Load() != 0 {
		t.Errorf("downstream.called = %d, want 0 (short-circuit on panic-derived error)", downstream.called.Load())
	}

	var pe *panicerr.Error
	if !errors.As(err, &pe) {
		t.Fatalf("Until error is not *panicerr.Error: %T %v", err, err)
	}
	if !strings.Contains(pe.Error(), "until result boom") {
		t.Errorf("recovered panic message lost: %q", pe.Error())
	}
}
