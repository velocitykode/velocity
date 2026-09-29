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

// optionRun is what one unary call through the logging interceptor left
// behind: the lines of the logger it was given, the fallback's output
// and the events it dispatched.
type optionRun struct {
	lines    []optionLine
	fallback string
	events   int
}

// runLoggingCall sends one unary call to method through Logging(opts...),
// with a logger unless noLogger; the handler waits for delay and returns
// err.
func runLoggingCall(t *testing.T, method string, delay time.Duration, err error, noLogger bool, opts ...interceptors.LoggingOption) optionRun {
	t.Helper()
	fallback := fallbacklogtest.Capture(t)
	logger := newOptionLogger()
	if !noLogger {
		opts = append([]interceptors.LoggingOption{interceptors.WithLoggingLogger(logger)}, opts...)
	}
	pair := interceptors.Logging(opts...)
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

// loggingOptionCases holds, per exported LoggingOption constructor, a check
// that the option changes what the interceptor writes or dispatches.
var loggingOptionCases = map[string]func(t *testing.T){
	"WithLoggingLogger": func(t *testing.T) {
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
			opts      []interceptors.LoggingOption
			wantLevel string
			wantMsg   string
		}{
			{name: "default 5s", wantLevel: "info", wantMsg: "gRPC request"},
			{name: "10ms", opts: []interceptors.LoggingOption{interceptors.WithSlowThreshold(10 * time.Millisecond)}, wantLevel: "warn", wantMsg: "gRPC request (slow)"},
			{name: "zero disables", opts: []interceptors.LoggingOption{interceptors.WithSlowThreshold(0)}, wantLevel: "info", wantMsg: "gRPC request"},
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

// Every exported LoggingOption constructor changes what the logging
// interceptor writes or dispatches; the table has a row for each one the
// package declares, so an option that does nothing cannot be added
// without a row that fails.
func TestLoggingOptions_EachChangesObservableOutput(t *testing.T) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "logging.go", nil, 0)
	if err != nil {
		t.Fatalf("parse logging.go: %v", err)
	}
	var declared []string
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !fn.Name.IsExported() || fn.Type.Results == nil || len(fn.Type.Results.List) != 1 {
			continue
		}
		if id, ok := fn.Type.Results.List[0].Type.(*ast.Ident); ok && id.Name == "LoggingOption" {
			declared = append(declared, fn.Name.Name)
		}
	}
	sort.Strings(declared)
	if len(declared) == 0 {
		t.Fatal("found no LoggingOption constructors in logging.go")
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
			t.Errorf("row %s names no LoggingOption constructor in logging.go", name)
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
