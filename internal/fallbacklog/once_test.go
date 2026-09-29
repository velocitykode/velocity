package fallbacklog_test

import (
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

func warnOnce(o *fallbacklog.Once, l contract.Logger) {
	o.Write(l, func(w contract.Logger) { w.Warn("once line") })
}

func TestOnce_WritesOnce(t *testing.T) {
	var o fallbacklog.Once
	l := hostile.NewLogger(nil)
	warnOnce(&o, l)
	warnOnce(&o, l)
	if n := l.Count(hostile.Warn, "once line"); n != 1 {
		t.Fatalf("the line was written %d times, want 1", n)
	}
}

// Many goroutines racing the first write: exactly one line.
func TestOnce_Concurrent(t *testing.T) {
	var o fallbacklog.Once
	l := hostile.NewLogger(nil)
	var wg sync.WaitGroup
	for range 64 {
		wg.Go(func() { warnOnce(&o, l) })
	}
	wg.Wait()
	if n := l.Count(hostile.Warn, "once line"); n != 1 {
		t.Fatalf("the line was written %d times, want 1", n)
	}
}

// A logger that writes the same warning again from inside the line (it
// calls back into the component) returns at once; a sync.Once deadlocks.
func TestOnce_ReentryDoesNotWait(t *testing.T) {
	var o fallbacklog.Once
	var l *hostile.Logger
	code := hostile.New(t, hostile.Reenter, func() { warnOnce(&o, l) })
	l = hostile.NewLogger(code)
	hostile.Within(t, hostile.Deadline, func() { warnOnce(&o, l) })
}

// A logger blocked inside the line does not hold up other callers.
func TestOnce_BlockedLineDoesNotHoldCallers(t *testing.T) {
	var o fallbacklog.Once
	code := hostile.New(t, hostile.Block, nil)
	l := hostile.NewLogger(code)
	go warnOnce(&o, l)
	<-code.Entered()
	hostile.Within(t, hostile.Deadline, func() { warnOnce(&o, l) })
	code.Release()
}

// A panicking logger is contained: the line goes to the fallback, and the
// warning stays claimed.
func TestOnce_PanickingLoggerFallsBack(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	var o fallbacklog.Once
	l := hostile.NewLogger(hostile.New(t, hostile.Panic, nil))
	if p := hostile.Within(t, hostile.Deadline, func() { warnOnce(&o, l) }); p != nil {
		t.Fatalf("the panic escaped: %v", p)
	}
	warnOnce(&o, l)
	if n := fallback.Count("WARN", "once line"); n != 1 {
		t.Fatalf("fallback has %d lines, want 1:\n%s", n, fallback.String())
	}
}

func TestOnce_NilWriteClaimsNothing(t *testing.T) {
	var o fallbacklog.Once
	o.Write(nil, nil)
	l := hostile.NewLogger(nil)
	warnOnce(&o, l)
	if n := l.Count(hostile.Warn, "once line"); n != 1 {
		t.Fatalf("the line was written %d times after a nil write, want 1", n)
	}
}
