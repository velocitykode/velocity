package fallbacklog_test

import (
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// panicLog panics on every line.
type panicLog struct{}

func (panicLog) Debug(string, ...any)              { panic("logger broke") }
func (panicLog) Info(string, ...any)               { panic("logger broke") }
func (panicLog) Warn(string, ...any)               { panic("logger broke") }
func (panicLog) Error(string, ...any)              { panic("logger broke") }
func (panicLog) Fatal(string, ...any)              { panic("logger broke") }
func (p panicLog) With(kvs ...any) contract.Logger { return contract.BindFields(p, kvs...) }

// Write writes through the logger it is given; the fallback sees nothing
// and the fallback fields are not added to the line.
func TestWrite_ThroughTheLogger(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	l := &levelLog{}
	fallbacklog.Write(l, func(w contract.Logger) { w.Error("line", "k", "v") }, "task_name", "t")
	if l.count("ERROR line") != 1 {
		t.Errorf("logger lines = %v, want the line once", l.entries)
	}
	if out := fallback.String(); out != "" {
		t.Errorf("fallback = %q, want nothing", out)
	}
}

// A nil logger means the fallback, with the fallback fields bound.
func TestWrite_NilLoggerIsTheFallback(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	fallbacklog.Write(nil, func(w contract.Logger) { w.Error("nil line", "k", "v") }, "task_name", "t")
	out := fallback.String()
	if !strings.Contains(out, "nil line") || !strings.Contains(out, "task_name=t") || !strings.Contains(out, "k=v") {
		t.Errorf("fallback = %q, want the line with its fields", out)
	}
}

// A logger that panics does not take the caller down: the line is written
// once more, to the fallback, carrying the fallback fields (the pairs the
// panicking logger had bound).
func TestWrite_PanickingLoggerFallsBack(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	calls := 0
	fallbacklog.Write(panicLog{}.With("task_name", "t"), func(w contract.Logger) {
		calls++
		w.Error("contained", "error", "boom")
	}, "task_name", "t")
	out := fallback.String()
	if calls != 2 {
		t.Errorf("write ran %d times, want 2 (logger, then fallback)", calls)
	}
	if strings.Count(out, "contained") != 1 || !strings.Contains(out, "task_name=t") || !strings.Contains(out, "error=boom") {
		t.Errorf("fallback = %q, want the line once with the bound and own fields", out)
	}
}

// A write that panics on the fallback too is contained: Write never panics.
func TestWrite_SecondPanicIsContained(t *testing.T) {
	fallbacklogtest.Capture(t)
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Write panicked: %v", r)
		}
	}()
	calls := 0
	fallbacklog.Write(panicLog{}, func(w contract.Logger) {
		calls++
		panic("write broke")
	})
	if calls != 2 {
		t.Errorf("write ran %d times, want 2", calls)
	}
	fallbacklog.Write(nil, nil)
}
