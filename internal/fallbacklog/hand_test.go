package fallbacklog_test

import (
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// takesLogger records the logger handed to it; code runs first.
type takesLogger struct {
	code *hostile.Code
	got  contract.Logger
}

func (t *takesLogger) SetLogger(l contract.Logger) {
	t.code.Run()
	t.got = l
}

// Hand hands the forwarder itself.
func TestForwarderHand_HandsTheForwarder(t *testing.T) {
	var f fallbacklog.Forwarder
	la := &takesLogger{}
	f.Hand(la, "unused")
	if la.got != &f {
		t.Fatalf("handed %v, want the forwarder", la.got)
	}
	f.Hand(nil, "unused") // a nil LoggerAware is ignored
}

// A SetLogger that panics is contained and written once as a warning
// through the forwarder, with the raw panic value under "error".
func TestForwarderHand_ContainsAPanickingSetLogger(t *testing.T) {
	var f fallbacklog.Forwarder
	rec := hostile.NewLogger(nil)
	f.Set(rec)
	la := &takesLogger{code: hostile.New(t, hostile.Panic, nil)}
	if p := hostile.Within(t, hostile.Deadline, func() { f.Hand(la, "handoff failed") }); p != nil {
		t.Fatalf("Hand let the panic escape: %v", p)
	}
	if n := rec.Count(hostile.Warn, "handoff failed"); n != 1 {
		t.Fatalf("warnings = %d, want 1: %v", n, rec.Lines())
	}
	kvs := rec.Lines()[0].KVs
	if len(kvs) != 2 || kvs[0] != "error" {
		t.Fatalf("warning pairs = %v, want error=<panic>", kvs)
	}
	if pe := panicerr.AsTyped(kvs[1].(error)); pe == nil || pe.Recovered() != hostile.PanicValue {
		t.Errorf("warning error = %v, want the raw panic value", kvs[1])
	}
	if la.got != nil {
		t.Error("a SetLogger that panicked recorded a logger")
	}
}

// When the logger the warning goes to panics too, the warning lands on
// the fallback and Hand still returns.
func TestForwarderHand_PanickingLoggerForTheWarning(t *testing.T) {
	out := fallbacklogtest.Capture(t)
	var f fallbacklog.Forwarder
	f.Set(hostile.NewLogger(hostile.New(t, hostile.Panic, nil)))
	la := &takesLogger{code: hostile.New(t, hostile.Panic, nil)}
	if p := hostile.Within(t, hostile.Deadline, func() { f.Hand(la, "handoff failed") }); p != nil {
		t.Fatalf("Hand let a panic escape: %v", p)
	}
	if got := out.Lines(); len(got) != 1 {
		t.Fatalf("fallback lines = %v, want the one warning", got)
	}
}

// A SetLogger that calls back into the forwarder (re-entry) completes.
func TestForwarderHand_Reentry(t *testing.T) {
	var f fallbacklog.Forwarder
	la := &takesLogger{}
	la.code = hostile.New(t, hostile.Reenter, func() { f.Hand(&takesLogger{}, "inner"); f.Set(nil) })
	hostile.Within(t, hostile.Deadline, func() { f.Hand(la, "outer") })
	if la.got != &f {
		t.Fatalf("handed %v after re-entry, want the forwarder", la.got)
	}
}
