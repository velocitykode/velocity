package eventemit

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/trace"
)

// panickingLogger panics in the method named by panicIn ("with", "warn" or
// "error") and discards every other line.
type panickingLogger struct{ panicIn string }

func (l panickingLogger) Debug(string, ...any) {}
func (l panickingLogger) Info(string, ...any)  {}
func (l panickingLogger) Warn(string, ...any) {
	if l.panicIn == "warn" {
		panic("warn broke")
	}
}
func (l panickingLogger) Error(string, ...any) {
	if l.panicIn == "error" {
		panic("error broke")
	}
}
func (l panickingLogger) Fatal(string, ...any) {}
func (l panickingLogger) With(...any) contract.Logger {
	if l.panicIn == "with" {
		panic("with broke")
	}
	return l
}

// A policy logger that panics while writing a failure's line (in With or
// in the write itself) never skips the accounting or the hook: the failure
// is counted once and handed to the hook once, the logger's panic is not
// counted as another failure, and the same line, with the context's ids,
// reaches the fallback logger. The same holds for the line of a hook's own
// panic.
func TestFailures_PanickingLoggerNeitherSkipsTheHookNorLosesTheLine(t *testing.T) {
	cases := []struct {
		name      string
		panicIn   string
		hookPanic bool
		wantLevel string
		wantMsg   string
		wantCount uint64
	}{
		{name: "warn panics", panicIn: "warn", wantLevel: "WARN", wantMsg: FailureMessage, wantCount: 1},
		{name: "with panics", panicIn: "with", wantLevel: "WARN", wantMsg: FailureMessage, wantCount: 1},
		{name: "error panics on the hook's panic line", panicIn: "error", hookPanic: true, wantLevel: "ERROR", wantMsg: HookPanicMessage, wantCount: 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := fallbacklogtest.Capture(t)
			var f Failures
			var calls atomic.Int32
			f.SetHook(func(error, any) {
				calls.Add(1)
				if tc.hookPanic {
					panic("hook broke")
				}
			})
			ctx := trace.WithTrace(trace.WithRequestID(context.Background(), "req-1"), "trace-1", "span-1")
			func() {
				defer func() {
					if p := recover(); p != nil {
						t.Fatalf("the logger's panic escaped Record: %v", p)
					}
				}()
				f.Record(ctx, panickingLogger{panicIn: tc.panicIn}, errListener, namedEvent{"cache.hit"})
			}()
			if got := calls.Load(); got != 1 {
				t.Errorf("hook calls = %d, want 1", got)
			}
			if got := f.Count(); got != tc.wantCount {
				t.Errorf("Count = %d, want %d", got, tc.wantCount)
			}
			if n := out.Count(tc.wantLevel, tc.wantMsg); n != 1 {
				t.Fatalf("fallback lines %q = %d, want 1; got %q", tc.wantMsg, n, out.String())
			}
			for _, want := range []string{"event=cache.hit", "request_id=req-1", "trace_id=trace-1"} {
				if !strings.Contains(out.String(), want) {
					t.Errorf("fallback line %q lacks %s", out.String(), want)
				}
			}
		})
	}
}
