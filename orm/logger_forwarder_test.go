package orm

import (
	"strings"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/log"
	logdrivers "github.com/velocitykode/velocity/log/drivers"
)

// The zero forwarder, and one set back to nil, write through the fallback
// logger; set to a logger, every level reaches it.
func TestLoggerForwarder_ZeroValueAndNil(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	var f loggerForwarder
	f.Warn("zero warn")
	if !strings.Contains(fallback.String(), "zero warn") {
		t.Errorf("zero forwarder: fallback = %q, want the warn line", fallback.String())
	}
	l := &levelLog{}
	f.set(l)
	f.Debug("d")
	f.Info("i")
	f.Warn("w")
	f.Error("e")
	f.Fatal("f")
	for _, want := range []string{"DEBUG d", "INFO i", "WARN w", "ERROR e", "FATAL f"} {
		if l.count(want) != 1 {
			t.Errorf("target lines = %v, want %q once", l.entries, want)
		}
	}
	f.set(nil)
	f.Error("after nil")
	if !strings.Contains(fallback.String(), "after nil") {
		t.Errorf("forwarder set to nil: fallback = %q, want the error line", fallback.String())
	}
	if l.count("ERROR after nil") != 0 {
		t.Error("forwarder set to nil still wrote to the old target")
	}
}

// A logger bound from the forwarder keeps its fields and follows a later
// swap: a connection or helper that bound fields once never writes to a
// replaced logger.
func TestLoggerForwarder_WithFollowsASwap(t *testing.T) {
	var f loggerForwarder
	a, b := logdrivers.NewConsoleLoggerTo(&fallbacklogtest.Output{}, 0), &fallbacklogtest.Output{}
	f.set(a)
	bound := f.With("connection", "reports")
	f.set(logdrivers.NewConsoleLoggerTo(b, 0))
	bound.Warn("after swap", "query", "SELECT 1")
	out := b.String()
	if !strings.Contains(out, "after swap") || !strings.Contains(out, "connection=reports") || !strings.Contains(out, "query=SELECT 1") {
		t.Errorf("new target got %q, want the line with the bound and own fields", out)
	}
	if strings.Index(out, "connection=reports") > strings.Index(out, "query=SELECT 1") {
		t.Errorf("bound fields come after the line's own: %q", out)
	}
}

// A redacting logger behind the forwarder still redacts fields bound
// through the forwarder: they reach it as the line's first pairs, which it
// redacts on every write.
func TestLoggerForwarder_WithFieldsAreRedactedByARedactingTarget(t *testing.T) {
	out := &fallbacklogtest.Output{}
	mask := log.RedactorFunc(func(s string) string { return strings.ReplaceAll(s, "hunter2", "[REDACTED]") })
	var f loggerForwarder
	f.set(log.WithRedactors(logdrivers.NewConsoleLoggerTo(out, 0), mask))
	f.With("password", "hunter2").Warn("bound secret")
	f.Warn("line secret", "password", "hunter2")
	got := out.String()
	if strings.Contains(got, "hunter2") {
		t.Errorf("output carries the secret: %q", got)
	}
	if strings.Count(got, "[REDACTED]") != 2 {
		t.Errorf("output = %q, want both secrets masked", got)
	}
}

// Swapping the target while many goroutines write through the forwarder
// and loggers bound from it is race-free, and every line lands on one of
// the installed loggers.
func TestLoggerForwarder_ConcurrentSetAndWrite(t *testing.T) {
	fallbacklogtest.Capture(t)
	var f loggerForwarder
	targets := []*levelLog{{}, {}, {}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				f.set(targets[(i+j)%len(targets)])
			}
		}(i)
		go func() {
			defer wg.Done()
			bound := f.With("k", "v")
			for j := 0; j < 200; j++ {
				f.Warn("direct")
				bound.Warn("bound")
			}
		}()
	}
	wg.Wait()
	f.set(targets[0])
	total := 0
	for _, l := range targets {
		total += l.count("WARN ")
	}
	// The zero forwarder may have written a few lines to the fallback
	// before the first set; every later line hit a target.
	if total == 0 || total > 8*400 {
		t.Errorf("lines on the targets = %d, want between 1 and %d", total, 8*400)
	}
	var _ contract.Logger = &f
}
