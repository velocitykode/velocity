package router

import (
	"net/http"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/trace"
)

// withCountingLogger counts With calls and records the pairs bound last.
type withCountingLogger struct {
	withs int
	bound []any
}

func (*withCountingLogger) Debug(string, ...any) {}
func (*withCountingLogger) Info(string, ...any)  {}
func (*withCountingLogger) Warn(string, ...any)  {}
func (*withCountingLogger) Error(string, ...any) {}
func (*withCountingLogger) Fatal(string, ...any) {}
func (l *withCountingLogger) With(kvs ...any) contract.Logger {
	l.withs++
	l.bound = kvs
	return contract.BindFields(l, kvs...)
}

// c.Log binds Services.Log once per request and hands back the same
// logger on every later call; a pooled context starts over.
func TestContextLog_BuiltOncePerRequest(t *testing.T) {
	c, _ := NewTestContext(http.MethodGet, "/orders")
	ctx := trace.WithRequestID(trace.WithTrace(c.Request.Context(), "t1", "s1"), "r1")
	c.Request = c.Request.WithContext(ctx)
	logger := &withCountingLogger{}
	c.SetServices(&app.Services{Log: logger})

	first, second := c.Log(), c.Log()

	if logger.withs != 1 || first != second {
		t.Fatalf("With called %d times, same logger %v; want 1 and the same", logger.withs, first == second)
	}
	want := []any{"request_id", "r1", "trace_id", "t1", "span_id", "s1", "method", http.MethodGet}
	if len(logger.bound) != len(want) {
		t.Fatalf("bound %v, want %v", logger.bound, want)
	}
	for i := range want {
		if logger.bound[i] != want[i] {
			t.Fatalf("bound %v, want %v", logger.bound, want)
		}
	}

	c.reset()
	if c.logger != nil {
		t.Error("reset kept the request's logger")
	}
}
