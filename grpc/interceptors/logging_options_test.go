package interceptors_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/interceptors"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

// optionLine is one line an optionLogger recorded.
type optionLine struct {
	level string
	msg   string
	kvs   map[string]any
}

// optionLogger records the level, message and fields of every line.
type optionLogger struct {
	mu    *sync.Mutex
	lines *[]optionLine
	bound []any
}

func newOptionLogger() optionLogger {
	return optionLogger{mu: &sync.Mutex{}, lines: &[]optionLine{}}
}

func (l optionLogger) add(level, msg string, kvs []any) {
	all := append(append([]any(nil), l.bound...), kvs...)
	fields := map[string]any{}
	for i := 0; i+1 < len(all); i += 2 {
		if k, ok := all[i].(string); ok {
			fields[k] = all[i+1]
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	*l.lines = append(*l.lines, optionLine{level: level, msg: msg, kvs: fields})
}

func (l optionLogger) Debug(msg string, kvs ...any) { l.add("debug", msg, kvs) }
func (l optionLogger) Info(msg string, kvs ...any)  { l.add("info", msg, kvs) }
func (l optionLogger) Warn(msg string, kvs ...any)  { l.add("warn", msg, kvs) }
func (l optionLogger) Error(msg string, kvs ...any) { l.add("error", msg, kvs) }
func (l optionLogger) Fatal(msg string, kvs ...any) { l.add("fatal", msg, kvs) }

func (l optionLogger) With(kvs ...any) contract.Logger {
	return optionLogger{mu: l.mu, lines: l.lines, bound: append(append([]any(nil), l.bound...), kvs...)}
}

func (l optionLogger) all() []optionLine {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]optionLine(nil), *l.lines...)
}

// optionRun is what one unary call through the call lifecycle interceptor left
// behind: the lines of the logger it was given and the fallback's output.
type optionRun struct {
	lines    []optionLine
	fallback string
}

// runLoggingCall sends one unary call to method through CallLifecycle(WithRequestLine(), opts...),
// with a logger unless noLogger; the handler waits for delay and returns
// err.
func runLoggingCall(t *testing.T, method string, delay time.Duration, err error, noLogger bool, opts ...interceptors.CallOption) optionRun {
	t.Helper()
	fallback := fallbacklogtest.Capture(t)
	logger := newOptionLogger()
	if !noLogger {
		opts = append([]interceptors.CallOption{interceptors.WithLogger(logger)}, opts...)
	}
	pair := interceptors.CallLifecycle(append([]interceptors.CallOption{interceptors.WithRequestLine()}, opts...)...)
	handler := func(context.Context, interface{}) (interface{}, error) {
		time.Sleep(delay)
		return "ok", err
	}
	_, _ = pair.Unary(context.Background(), nil, mockUnaryServerInfo(method), handler)
	return optionRun{lines: logger.all(), fallback: fallback.String()}
}

// countingDispatcher counts the events it receives.
type countingDispatcher struct {
	mu sync.Mutex
	n  int
}

func (d *countingDispatcher) dispatch(context.Context, interface{}) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.n++
	return nil
}

func (d *countingDispatcher) count() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.n
}

// loggingOptionCases holds, per exported CallOption constructor, a check
// that the option changes what the interceptor writes or dispatches.
var loggingOptionCases = map[string]func(t *testing.T){
	"WithLogger": func(t *testing.T) {
		failing := status.Error(codes.Internal, "boom")
		without := runLoggingCall(t, "/svc.S/M", 0, failing, true)
		with := runLoggingCall(t, "/svc.S/M", 0, failing, false)
		if !strings.Contains(without.fallback, "ERROR gRPC request") {
			t.Errorf("without: fallback = %q, want the error line", without.fallback)
		}
		if with.fallback != "" || len(with.lines) != 1 || with.lines[0].level != "error" {
			t.Errorf("with: fallback = %q, lines = %+v, want the one error line on the logger", with.fallback, with.lines)
		}
	},
	"WithSkipMethods": func(t *testing.T) {
		without := runLoggingCall(t, "/svc.S/Skip", 0, nil, false)
		with := runLoggingCall(t, "/svc.S/Skip", 0, nil, false, interceptors.WithSkipMethods("/svc.S/Skip"))
		if len(without.lines) != 1 || len(with.lines) != 0 {
			t.Errorf("lines without = %d, with = %d, want 1 and 0", len(without.lines), len(with.lines))
		}
	},
	"WithSkipHealthChecks": func(t *testing.T) {
		const health = "/grpc.health.v1.Health/Check"
		without := runLoggingCall(t, health, 0, nil, false)
		with := runLoggingCall(t, health, 0, nil, false, interceptors.WithSkipHealthChecks(false))
		if len(without.lines) != 0 || len(with.lines) != 1 {
			t.Errorf("lines without = %d, with = %d, want 0 (skipped by default) and 1", len(without.lines), len(with.lines))
		}
	},
	"WithSlowThreshold": func(t *testing.T) {
		const delay = 30 * time.Millisecond
		for _, tt := range []struct {
			name      string
			opts      []interceptors.CallOption
			wantLevel string
			wantMsg   string
		}{
			{name: "default 5s", wantLevel: "info", wantMsg: "gRPC request"},
			{name: "10ms", opts: []interceptors.CallOption{interceptors.WithSlowThreshold(10 * time.Millisecond)}, wantLevel: "warn", wantMsg: "gRPC request (slow)"},
			{name: "zero disables", opts: []interceptors.CallOption{interceptors.WithSlowThreshold(0)}, wantLevel: "info", wantMsg: "gRPC request"},
		} {
			run := runLoggingCall(t, "/svc.S/Slow", delay, nil, false, tt.opts...)
			if len(run.lines) != 1 {
				t.Fatalf("%s: lines = %+v, want 1", tt.name, run.lines)
			}
			line := run.lines[0]
			if line.level != tt.wantLevel || line.msg != tt.wantMsg {
				t.Errorf("%s: line = %s %q, want %s %q", tt.name, line.level, line.msg, tt.wantLevel, tt.wantMsg)
			}
			if ms, ok := line.kvs["duration_ms"].(int64); !ok || ms < delay.Milliseconds() {
				t.Errorf("%s: duration_ms = %#v, want an int64 of at least %d", tt.name, line.kvs["duration_ms"], delay.Milliseconds())
			}
		}
	},
	"WithExtraFields": func(t *testing.T) {
		without := runLoggingCall(t, "/svc.S/M", 0, nil, false)
		with := runLoggingCall(t, "/svc.S/M", 0, nil, false, interceptors.WithExtraFields(func(context.Context) []interface{} {
			return []interface{}{"tenant", "acme"}
		}))
		if _, ok := without.lines[0].kvs["tenant"]; ok {
			t.Errorf("without: line carries tenant: %v", without.lines[0].kvs)
		}
		if with.lines[0].kvs["tenant"] != "acme" {
			t.Errorf("with: tenant = %v, want acme", with.lines[0].kvs["tenant"])
		}
	},
	"WithRequestLine": func(t *testing.T) {
		handler := func(context.Context, interface{}) (interface{}, error) { return nil, nil }
		without, with := newOptionLogger(), newOptionLogger()
		_, _ = interceptors.CallLifecycle(interceptors.WithLogger(without)).Unary(context.Background(), nil, mockUnaryServerInfo("/svc.S/M"), handler)
		_, _ = interceptors.CallLifecycle(interceptors.WithLogger(with), interceptors.WithRequestLine()).Unary(context.Background(), nil, mockUnaryServerInfo("/svc.S/M"), handler)
		if len(without.all()) != 0 || len(with.all()) != 1 {
			t.Errorf("lines without = %d, with = %d, want 0 (the line is opt-in) and 1", len(without.all()), len(with.all()))
		}
	},
	"WithReporter": func(t *testing.T) {
		reports := &layerReports{}
		failing := func(context.Context, interface{}) (interface{}, error) {
			return nil, status.Error(codes.Internal, "boom")
		}
		_, _ = interceptors.CallLifecycle().Unary(context.Background(), nil, mockUnaryServerInfo("/svc.S/M"), failing)
		_, _ = interceptors.CallLifecycle(interceptors.WithReporter(reports)).Unary(context.Background(), nil, mockUnaryServerInfo("/svc.S/M"), failing)
		if reports.count() != 1 {
			t.Errorf("reports = %d, want 1", reports.count())
		}
	},
	"WithPanicHandler": func(t *testing.T) {
		panicking := func(context.Context, interface{}) (interface{}, error) { panic("boom") }
		_, without := interceptors.CallLifecycle(interceptors.WithStackTrace(false)).Unary(context.Background(), nil, mockUnaryServerInfo("/svc.S/M"), panicking)
		_, with := interceptors.CallLifecycle(interceptors.WithStackTrace(false), interceptors.WithPanicHandler(func(context.Context, interface{}) error {
			return status.Error(codes.Unavailable, "retry")
		})).Unary(context.Background(), nil, mockUnaryServerInfo("/svc.S/M"), panicking)
		if status.Code(without) != codes.Internal || status.Code(with) != codes.Unavailable {
			t.Errorf("codes without = %v, with = %v, want Internal and Unavailable", status.Code(without), status.Code(with))
		}
	},
	"WithStackTrace": func(t *testing.T) {
		panicking := func(context.Context, interface{}) (interface{}, error) { panic("boom") }
		for _, enabled := range []bool{true, false} {
			reports := &layerReports{}
			_, _ = interceptors.CallLifecycle(interceptors.WithReporter(reports), interceptors.WithStackTrace(enabled)).Unary(context.Background(), nil, mockUnaryServerInfo("/svc.S/M"), panicking)
			ecs := reports.contexts()
			if len(ecs) != 1 || (ecs[0].PanicStack != "") != enabled {
				t.Errorf("WithStackTrace(%v): reports = %d, stack captured = %v", enabled, len(ecs), len(ecs) == 1 && ecs[0].PanicStack != "")
			}
		}
	},
	"WithEventDispatcher": func(t *testing.T) {
		d := &countingDispatcher{}
		runLoggingCall(t, "/svc.S/M", 0, nil, false)
		if d.count() != 0 {
			t.Fatalf("events before the option = %d", d.count())
		}
		runLoggingCall(t, "/svc.S/M", 0, nil, false, interceptors.WithEventDispatcher(d.dispatch))
		if d.count() != 2 {
			t.Errorf("events = %d, want 2 (started, completed)", d.count())
		}
	},
}

// Every exported CallOption constructor changes what the call lifecycle interceptor
// writes, dispatches or reports, or what the call ends with; the table has a row for each one the
// package declares, so an option that does nothing cannot be added
// without a row that fails.
func TestCallOptions_EachChangesObservableOutput(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "call_lifecycle.go", nil, 0)
	if err != nil {
		t.Fatalf("parse call_lifecycle.go: %v", err)
	}
	var declared []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !fn.Name.IsExported() || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
			continue
		}
		if id, ok := fn.Type.Results.List[0].Type.(*ast.Ident); ok && id.Name == "CallOption" {
			declared = append(declared, fn.Name.Name)
		}
	}
	sort.Strings(declared)
	if len(declared) == 0 {
		t.Fatal("found no CallOption constructors in call_lifecycle.go")
	}
	for _, name := range declared {
		check, ok := loggingOptionCases[name]
		if !ok {
			t.Errorf("%s has no row showing it changes the interceptor's output", name)
			continue
		}
		t.Run(name, check)
	}
	for name := range loggingOptionCases {
		if !contains(declared, name) {
			t.Errorf("row %s names no CallOption constructor in call_lifecycle.go", name)
		}
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
