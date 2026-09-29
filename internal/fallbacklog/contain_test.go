package fallbacklog_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/internal/hostile"
)

// Every method writes through the logger it contains.
func TestContain_WritesThroughTheLogger(t *testing.T) {
	l := hostile.NewLogger(nil)
	c := fallbacklog.Contain(l)
	c.Debug("d")
	c.Info("i")
	c.Warn("w")
	c.Error("e")
	c.Fatal("f")
	c.With("k", "v").Warn("bound", "own", 1)
	if n := len(l.Lines()); n != 6 {
		t.Fatalf("lines = %+v, want 6", l.Lines())
	}
	last := l.Lines()[5]
	if fmt.Sprint(last.KVs) != "[k v own 1]" {
		t.Fatalf("bound line pairs = %v, want the bound pair first", last.KVs)
	}
}

// A panicking logger never reaches the caller: each line goes to the
// fallback, bound pairs included.
func TestContain_PanickingLoggerFallsBack(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	c := fallbacklog.Contain(hostile.NewLogger(hostile.New(t, hostile.Panic, nil)))
	if p := hostile.Within(t, hostile.Deadline, func() {
		c.Warn("warn line")
		c.Error("error line")
		c.Fatal("fatal line")
		c.With("task", "t").Error("bound line", "k", "v")
	}); p != nil {
		t.Fatalf("a panic escaped: %v", p)
	}
	for _, want := range []struct{ level, msg string }{
		{"WARN", "warn line"}, {"ERROR", "error line"}, {"ERROR", "fatal line"}, {"ERROR", "bound line"},
	} {
		if fallback.Count(want.level, want.msg) != 1 {
			t.Errorf("fallback lacks %s %s:\n%s", want.level, want.msg, fallback.String())
		}
	}
	if s := fallback.String(); !strings.Contains(s, "task=t") || !strings.Contains(s, "k=v") {
		t.Errorf("fallback lost the pairs:\n%s", s)
	}
}

// With never calls the contained logger's With, so a panicking With
// cannot escape either.
func TestContain_WithDoesNotCallTheLoggersWith(t *testing.T) {
	fallbacklogtest.Capture(t)
	code := hostile.New(t, hostile.Panic, nil)
	l := hostile.NewLogger(code, hostile.With)
	c := fallbacklog.Contain(l)
	if p := hostile.Within(t, hostile.Deadline, func() { c.With("k", 1).Info("line") }); p != nil {
		t.Fatalf("a panic escaped: %v", p)
	}
	if code.Calls() != 0 || l.Count(hostile.Info, "line") != 1 {
		t.Fatalf("With ran the logger's With (%d calls) or lost the line: %+v", code.Calls(), l.Lines())
	}
}

func TestContain_NilIsTheFallback(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	fallbacklog.Contain(nil).Warn("nil line")
	if fallback.Count("WARN", "nil line") != 1 {
		t.Fatalf("fallback = %q", fallback.String())
	}
}

func TestContain_Idempotent(t *testing.T) {
	c := fallbacklog.Contain(nil)
	if fallbacklog.Contain(c) != c {
		t.Fatal("Contain wrapped a contained logger again")
	}
}
