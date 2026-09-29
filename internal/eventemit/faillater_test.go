package eventemit

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// FailLater counts the failure at once and returns the rest of the policy
// (the first-failure line and the hook) to run later; running it applies
// them once.
func TestEmitter_FailLater(t *testing.T) {
	var f Failures
	var e Emitter
	e.Share(&f)
	logger := &recordingLogger{}
	e.UseLogger(func() contract.Logger { return logger })
	var calls atomic.Int32
	f.SetHook(func(error, any) { calls.Add(1) })

	report := e.FailLater(context.Background(), errListener, namedEvent{name: "orm.query.completed"})
	if report == nil {
		t.Fatal("FailLater returned no report")
	}
	if got := f.Count(); got != 1 {
		t.Errorf("Count before the report = %d, want 1", got)
	}
	if calls.Load() != 0 || len(logger.at("warn")) != 0 {
		t.Errorf("hook calls = %d, warn lines = %d before the report, want 0 and 0", calls.Load(), len(logger.at("warn")))
	}
	report()
	if got := f.Count(); got != 1 {
		t.Errorf("Count after the report = %d, want 1 (counted once)", got)
	}
	if calls.Load() != 1 || len(logger.at("warn")) != 1 {
		t.Errorf("hook calls = %d, warn lines = %d after the report, want 1 and 1", calls.Load(), len(logger.at("warn")))
	}

	if e.FailLater(nilCtx, nil, namedEvent{name: "x"}) != nil {
		t.Error("FailLater(nil error) returned a report")
	}
	marked := f.Recording(failing, logger)(context.Background(), namedEvent{name: "y"})
	before := f.Count()
	if e.FailLater(context.Background(), marked, namedEvent{name: "y"}) != nil || f.Count() != before {
		t.Error("FailLater recorded a failure the dispatch function already recorded")
	}
	if r := (&Emitter{}).FailLater(nilCtx, errListener, plainEvent{}); r == nil {
		t.Error("zero Emitter FailLater returned no report")
	} else {
		r()
	}
}

// A failure the hook causes and hands to FailLater (a statement it runs
// whose event is dropped) is counted and logged when its report runs, but
// the hook is not called for it.
func TestEmitter_FailLaterFromTheHookSkipsTheHook(t *testing.T) {
	var f Failures
	var e Emitter
	e.Share(&f)
	var calls atomic.Int32
	var nested func()
	f.SetHook(func(error, any) {
		if calls.Add(1) == 1 {
			nested = e.FailLater(context.Background(), errListener, namedEvent{name: "orm.query.completed"})
		}
	})
	e.FailLater(context.Background(), errListener, namedEvent{name: "orm.query.completed"})()
	if nested == nil {
		t.Fatal("the hook's FailLater returned no report")
	}
	nested()
	if got := calls.Load(); got != 1 {
		t.Errorf("hook calls = %d, want 1", got)
	}
	if got := f.Count(); got != 2 {
		t.Errorf("Count = %d, want 2", got)
	}
}
