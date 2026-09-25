package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// TestBoundary_PanicCarryingWrittenMarker asserts a panic whose value is
// the response-written sentinel or a contract.Handled value is a recovered
// 500 like any other panic, directly and under Timeout: the default path
// logs it, writes 500 and dispatches RequestFailed with Recovered set, and
// an installed error handler receives it flagged as recovered.
func TestBoundary_PanicCarryingWrittenMarker(t *testing.T) {
	tests := []struct {
		name      string
		value     error
		timeout   bool
		installed bool
	}{
		{name: "sentinel direct", value: contract.ErrResponseWritten},
		{name: "handled direct", value: contract.Handled(errors.New("rendered elsewhere"))},
		{name: "sentinel under timeout", value: contract.ErrResponseWritten, timeout: true},
		{name: "handled under timeout", value: contract.Handled(errors.New("rendered elsewhere")), timeout: true},
		{name: "sentinel direct installed handler", value: contract.ErrResponseWritten, installed: true},
		{name: "handled under timeout installed handler", value: contract.Handled(errors.New("rendered elsewhere")), timeout: true, installed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewV2()
			errLog := &logCapture{}
			r.SetErrorLogger(errLog.fn)
			var (
				mu     sync.Mutex
				failed []*RequestFailed
			)
			r.SetEventDispatcher(func(_ context.Context, event interface{}) error {
				if rf, ok := event.(*RequestFailed); ok {
					mu.Lock()
					failed = append(failed, rf)
					mu.Unlock()
				}
				return nil
			})
			var (
				calls int
				got   ErrorInfo
			)
			if tt.installed {
				r.SetErrorHandler(func(c *Context, _ error, info ErrorInfo) {
					calls++
					got = info
					c.Response.WriteHeader(http.StatusInternalServerError)
				})
			}
			if tt.timeout {
				r.Use(Timeout(time.Minute))
			}
			r.Get("/boom", func(*Context) error { panic(tt.value) })

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))

			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", w.Code)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(failed) != 1 || !failed[0].Recovered {
				t.Errorf("RequestFailed = %+v, want one with Recovered set", failed)
			}
			if tt.installed {
				if calls != 1 || !got.Recovered {
					t.Errorf("error handler calls = %d, Recovered = %v, want 1 and true", calls, got.Recovered)
				}
				return
			}
			if errLog.count() != 1 {
				t.Errorf("error log entries = %d, want 1", errLog.count())
			}
		})
	}
}

// TestBoundary_ReturnedWrittenMarkerStillEndsTheRequest pins the returned
// forms the panic fix must keep: a bare sentinel writes and dispatches
// nothing, and a Handled value around a panic the Timeout middleware
// forwarded (a middleware rendered it) dispatches the panic and writes
// nothing more.
func TestBoundary_ReturnedWrittenMarkerStillEndsTheRequest(t *testing.T) {
	tests := []struct {
		name       string
		mw         MiddlewareFunc
		handler    HandlerFunc
		wantFailed int
	}{
		{
			name:    "bare sentinel",
			handler: func(*Context) error { return contract.ErrResponseWritten },
		},
		{
			name: "handled forwarded panic",
			mw: func(next HandlerFunc) HandlerFunc {
				guarded := Timeout(time.Minute)(next)
				return func(c *Context) error {
					err := guarded(c)
					var pe *PanicError
					if errors.As(err, &pe) {
						c.Response.WriteHeader(http.StatusTeapot)
						return contract.Handled(err)
					}
					return err
				}
			},
			handler:    func(*Context) error { panic("boom") },
			wantFailed: 1,
		},
		{
			name: "handled forwarded panic whose value is handled",
			mw: func(next HandlerFunc) HandlerFunc {
				guarded := Timeout(time.Minute)(next)
				return func(c *Context) error {
					err := guarded(c)
					var pe *PanicError
					if errors.As(err, &pe) {
						c.Response.WriteHeader(http.StatusTeapot)
						return contract.Handled(err)
					}
					return err
				}
			},
			handler:    func(*Context) error { panic(contract.Handled(errors.New("inner"))) },
			wantFailed: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewV2()
			var (
				mu     sync.Mutex
				failed []*RequestFailed
			)
			r.SetEventDispatcher(func(_ context.Context, event interface{}) error {
				if rf, ok := event.(*RequestFailed); ok {
					mu.Lock()
					failed = append(failed, rf)
					mu.Unlock()
				}
				return nil
			})
			if tt.mw != nil {
				r.Use(tt.mw)
			}
			r.Get("/x", tt.handler)

			w := newHeaderCountingRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

			if tt.wantFailed == 0 && (w.headerCalls != 0 || w.Body.Len() != 0) {
				t.Errorf("wrote %d status lines and body %q, want nothing", w.headerCalls, w.Body.String())
			}
			if tt.wantFailed == 1 && (w.Code != http.StatusTeapot || w.Body.Len() != 0) {
				t.Errorf("response = %d %q, want the middleware's 418 alone", w.Code, w.Body.String())
			}
			mu.Lock()
			defer mu.Unlock()
			if len(failed) != tt.wantFailed {
				t.Fatalf("RequestFailed dispatched %d times, want %d", len(failed), tt.wantFailed)
			}
			if tt.wantFailed == 1 {
				var pe *PanicError
				if !failed[0].Recovered || !errors.As(failed[0].Error, &pe) || contract.IsResponseWritten(failed[0].Error) {
					t.Errorf("RequestFailed = %+v, want the recovered panic without the handled marker", failed[0])
				}
			}
		})
	}
}

// TestMarkedWritten_OutsidePanicParity pins where the response-written
// marker counts relative to a recovered panic, as the pipeline's
// outsidePanic does: never inside the panic value, always around it or on
// a sibling branch of a join, and never when recovered is set but the
// error carries no panic node.
func TestMarkedWritten_OutsidePanicParity(t *testing.T) {
	x := errors.New("x")
	p := func(v any) error { return newPanicError(panicerr.FromRecovered(v), 0) }
	around := p("boom")
	both := p(contract.Handled(x))
	sibling := p("boom")
	handBuilt := &PanicError{Err: x}
	tests := []struct {
		name      string
		err       error
		recovered bool
		want      bool
		wantCause error
	}{
		{name: "NoPanicSentinel", err: contract.ErrResponseWritten, want: true},
		{name: "NoPanicHandled", err: contract.Handled(x), want: true, wantCause: x},
		{name: "SentinelInsidePanicOnly", err: p(contract.ErrResponseWritten), recovered: true},
		{name: "HandledInsidePanicOnly", err: p(contract.Handled(x)), recovered: true},
		{name: "HandledAroundPanic", err: contract.Handled(around), recovered: true, want: true, wantCause: around},
		{name: "HandledInsideAndAround", err: contract.Handled(both), recovered: true, want: true, wantCause: both},
		{name: "RecoveredNoPanicNode", err: contract.Handled(x), recovered: true},
		{name: "JoinSentinelSibling", err: errors.Join(sibling, contract.ErrResponseWritten), recovered: true, want: true},
		{name: "JoinHandledSibling", err: errors.Join(sibling, contract.Handled(x)), recovered: true, want: true, wantCause: x},
		{name: "Unmarked", err: x},
		{name: "HandBuiltHandledInside", err: &PanicError{Err: contract.Handled(x)}, recovered: true},
		{name: "HandBuiltHandledInsideNotFlagged", err: &PanicError{Err: contract.Handled(x)}},
		{name: "HandBuiltSentinelInside", err: &PanicError{Err: contract.ErrResponseWritten}, recovered: true},
		{name: "HandBuiltHandledAround", err: contract.Handled(handBuilt), recovered: true, want: true, wantCause: handBuilt},
		{name: "HandBuiltHandledAroundNotFlagged", err: contract.Handled(handBuilt), want: true, wantCause: handBuilt},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := markedWritten(tt.err, tt.recovered); got != tt.want {
				t.Errorf("markedWritten = %v, want %v", got, tt.want)
			}
			if !tt.want {
				return
			}
			if got := contract.HandledCause(tt.err); got != tt.wantCause {
				t.Errorf("HandledCause = %v, want %v", got, tt.wantCause)
			}
		})
	}
}

// TestFinalize_PanickingHookIsARecoveredPanic asserts a BeforeFirstWrite
// hook that panics when the router fires it after the boundary (nothing
// wrote a response) is a recovered panic like one in the handler, on the
// matched, unmatched and static paths: the client gets a 500, one
// RequestFailed fires with Recovered set and a *PanicError whose stack
// holds the panicking frame, RequestHandled records the 500, and the
// failure reaches the boundary once: one default-path log line on a
// standalone router, one installed-handler call flagged recovered (and no
// router log line) otherwise.
func TestFinalize_PanickingHookIsARecoveredPanic(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "asset.txt"), []byte("asset"), 0o644); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name      string
		path      string
		installed bool
	}{
		{name: "MatchedDefault", path: "/quiet"},
		{name: "MatchedInstalled", path: "/quiet", installed: true},
		{name: "UnmatchedDefault", path: "/nowhere"},
		{name: "UnmatchedInstalled", path: "/nowhere", installed: true},
		{name: "StaticDefault", path: "/asset.txt"},
		{name: "StaticInstalled", path: "/asset.txt", installed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewV2()
			errLog := &logCapture{}
			r.SetErrorLogger(errLog.fn)
			var (
				mu        sync.Mutex
				failed    []*RequestFailed
				handled   []int
				calls     int
				callInfo  ErrorInfo
				callPanic bool
			)
			if tt.installed {
				r.SetErrorHandler(func(c *Context, err error, info ErrorInfo) {
					var pe *PanicError
					mu.Lock()
					calls++
					callInfo = info
					callPanic = errors.As(err, &pe)
					mu.Unlock()
					c.Response.WriteHeader(http.StatusInternalServerError)
				})
			}
			r.SetEventDispatcher(func(_ context.Context, event interface{}) error {
				mu.Lock()
				defer mu.Unlock()
				switch ev := event.(type) {
				case *RequestFailed:
					failed = append(failed, ev)
				case *RequestHandled:
					handled = append(handled, ev.StatusCode)
				}
				return nil
			})
			r.Static(dir)
			r.Use(func(next HandlerFunc) HandlerFunc {
				return func(c *Context) error {
					if h, ok := c.Response.(interface{ BeforeFirstWrite(func()) }); ok {
						h.BeforeFirstWrite(func() { panic("hook exploded") })
					}
					if c.Request.URL.Path != "/quiet" {
						// Answer nothing on the unmatched and static paths
						// either, so only the router fires the hook.
						return nil
					}
					return next(c)
				}
			})
			r.Get("/quiet", func(*Context) error { return nil })

			srv := httptest.NewServer(r)
			defer srv.Close()
			resp, err := srv.Client().Get(srv.URL + tt.path)
			if err != nil {
				t.Fatalf("GET: %v (the hook panic escaped the router)", err)
			}
			_ = resp.Body.Close()

			if resp.StatusCode != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", resp.StatusCode)
			}
			mu.Lock()
			defer mu.Unlock()
			if len(failed) != 1 {
				t.Fatalf("RequestFailed dispatched %d times, want 1", len(failed))
			}
			var pe *PanicError
			if !failed[0].Recovered || !errors.As(failed[0].Error, &pe) {
				t.Errorf("RequestFailed Recovered = %v, Error = %T; want true and a *PanicError", failed[0].Recovered, failed[0].Error)
			}
			if !strings.Contains(failed[0].Stack, "panic_marker_test.go") {
				t.Errorf("RequestFailed stack does not hold the panicking frame:\n%s", failed[0].Stack)
			}
			if len(handled) != 1 || handled[0] != http.StatusInternalServerError {
				t.Errorf("RequestHandled statuses = %v, want [500]", handled)
			}
			if tt.installed {
				if calls != 1 || !callInfo.Recovered || !callPanic || callInfo.Stack == "" {
					t.Errorf("installed handler: calls = %d, Recovered = %v, *PanicError = %v, stack set = %v; want 1, true, true, true", calls, callInfo.Recovered, callPanic, callInfo.Stack != "")
				}
				if errLog.count() != 0 {
					t.Errorf("router error log entries = %d with a handler installed, want 0", errLog.count())
				}
				return
			}
			if errLog.count() != 1 {
				t.Fatalf("error log entries = %d, want 1 (the boundary's)", errLog.count())
			}
			if v, _ := errLog.kv(0, "error"); !strings.Contains(fmt.Sprint(v), "hook exploded") {
				t.Errorf("logged error = %v, want the hook panic", v)
			}
			if v, _ := errLog.kv(0, "stack"); v == nil || v == "" {
				t.Error("logged no stack")
			}
			if v, _ := errLog.kv(0, "path"); v != tt.path {
				t.Errorf("logged path = %v, want %s", v, tt.path)
			}
		})
	}
}

// TestPanicError_Recovered asserts *PanicError is a contract.RecoveredPanic
// whose Recovered answers the recovered value of the panic error it wraps,
// or its Err when it wraps none.
func TestPanicError_Recovered(t *testing.T) {
	plain := errors.New("plain")
	valueErr := errors.New("value")
	tests := []struct {
		name string
		err  *PanicError
		want any
	}{
		{name: "RouterBuilt", err: newPanicError(panicerr.FromRecovered("boom"), 0), want: "boom"},
		{name: "TimeoutWrapped", err: newPanicError(fmt.Errorf("timeout handler panic: %w", panicerr.FromRecovered(valueErr)), 0), want: valueErr},
		{name: "HandBuilt", err: &PanicError{Err: plain}, want: plain},
		{name: "Nil", err: nil, want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rp contract.RecoveredPanic = tt.err
			if got := rp.Recovered(); got != tt.want {
				t.Errorf("Recovered() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestBoundary_DeepChainMarkers asserts the response-written marker is
// found by position at any depth: a *PanicError carrying the sentinel under
// more wrappers than the classification walk's limit is still a logged,
// dispatched 500, a plain sentinel that deep still ends the request, and a
// sentinel past the marker walk's cap is not found, so the error is logged
// and answered like any other.
func TestBoundary_DeepChainMarkers(t *testing.T) {
	wrap := func(err error, n int) error {
		for i := 0; i < n; i++ {
			err = fmt.Errorf("layer %d: %w", i, err)
		}
		return err
	}
	tests := []struct {
		name          string
		err           error
		wantStatus    int // 0: nothing written
		wantRecovered bool
	}{
		{name: "PanicCarryingSentinelPastWalkLimit", err: wrap(&PanicError{Err: contract.ErrResponseWritten, Stack: "stack"}, walkLimit+1), wantStatus: http.StatusInternalServerError, wantRecovered: true},
		{name: "SentinelPastWalkLimit", err: wrap(contract.ErrResponseWritten, walkLimit+1)},
		{name: "SentinelPastMarkerCap", err: wrap(contract.ErrResponseWritten, 1025), wantStatus: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewV2()
			errLog := &logCapture{}
			r.SetErrorLogger(errLog.fn)
			var (
				mu     sync.Mutex
				failed []*RequestFailed
			)
			r.SetEventDispatcher(func(_ context.Context, event interface{}) error {
				if rf, ok := event.(*RequestFailed); ok {
					mu.Lock()
					failed = append(failed, rf)
					mu.Unlock()
				}
				return nil
			})
			r.Get("/x", func(*Context) error { return tt.err })

			w := newHeaderCountingRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))

			mu.Lock()
			defer mu.Unlock()
			if tt.wantStatus == 0 {
				if w.headerCalls != 0 || w.Body.Len() != 0 || errLog.count() != 0 || len(failed) != 0 {
					t.Errorf("status lines %d, body %q, logs %d, RequestFailed %d; want nothing", w.headerCalls, w.Body.String(), errLog.count(), len(failed))
				}
				return
			}
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if errLog.count() != 1 {
				t.Errorf("error log entries = %d, want 1", errLog.count())
			}
			if len(failed) != 1 || failed[0].Recovered != tt.wantRecovered {
				t.Errorf("RequestFailed = %+v, want one with Recovered %v", failed, tt.wantRecovered)
			}
		})
	}
}

// consumerRecovered stands for the recovered-panic error of a consumer's
// own recovery middleware: it implements contract.RecoveredPanic and
// unwraps to the error it carries, and is not a *PanicError.
type consumerRecovered struct{ err error }

func (e *consumerRecovered) Error() string  { return "recovered: " + e.err.Error() }
func (e *consumerRecovered) Recovered() any { return e.err }
func (e *consumerRecovered) Unwrap() error  { return e.err }

// TestBoundary_ConsumerRecoveredPanic asserts the router treats any
// contract.RecoveredPanic as a recovered panic: the default path answers
// 500 without echoing the value's message and logs it, RequestFailed fires
// once with Recovered set (and no raw stack, since there is no
// *PanicError), and an installed handler receives it flagged recovered,
// whatever the value would answer or be dropped as on its own.
func TestBoundary_ConsumerRecoveredPanic(t *testing.T) {
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name      string
		value     error
		reqCx     context.Context
		installed bool
	}{
		{name: "ClientError", value: contract.NewHTTPError(http.StatusNotFound, "payload")},
		{name: "CanceledLiveRequest", value: context.Canceled},
		{name: "CanceledDeadRequest", value: context.Canceled, reqCx: dead},
		{name: "ClientErrorInstalledHandler", value: contract.NewHTTPError(http.StatusNotFound, "payload"), installed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewV2()
			errLog := &logCapture{}
			r.SetErrorLogger(errLog.fn)
			var (
				mu     sync.Mutex
				failed []*RequestFailed
			)
			r.SetEventDispatcher(func(_ context.Context, event interface{}) error {
				if rf, ok := event.(*RequestFailed); ok {
					mu.Lock()
					failed = append(failed, rf)
					mu.Unlock()
				}
				return nil
			})
			var (
				calls int
				got   ErrorInfo
			)
			if tt.installed {
				r.SetErrorHandler(func(c *Context, _ error, info ErrorInfo) {
					calls++
					got = info
					c.Response.WriteHeader(http.StatusInternalServerError)
				})
			}
			r.Get("/x", func(*Context) error { return &consumerRecovered{err: tt.value} })

			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			if tt.reqCx != nil {
				req = req.WithContext(tt.reqCx)
			}
			req.Header.Set("Accept", "application/json")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", w.Code)
			}
			if strings.Contains(w.Body.String(), "payload") {
				t.Errorf("body leaks the panic value's message: %q", w.Body.String())
			}
			mu.Lock()
			defer mu.Unlock()
			if len(failed) != 1 || !failed[0].Recovered || failed[0].Stack != "" {
				t.Errorf("RequestFailed = %+v, want one with Recovered set and no stack", failed)
			}
			if tt.installed {
				if calls != 1 || !got.Recovered {
					t.Errorf("error handler calls = %d, Recovered = %v, want 1 and true", calls, got.Recovered)
				}
				return
			}
			if errLog.count() != 1 {
				t.Errorf("error log entries = %d, want 1", errLog.count())
			}
		})
	}
}

// TestServeHTTP_AbortPanicSkipsBoundaryAndHooks asserts a
// panic(http.ErrAbortHandler) leaves ServeHTTP as the same panic, with
// nothing written, no pending pre-commit hook run (nothing will be
// committed), no RequestFailed and no error handler call, while
// RequestHandled still fires once. Under Timeout the handler's buffered
// response is dropped rather than flushed.
func TestServeHTTP_AbortPanicSkipsBoundaryAndHooks(t *testing.T) {
	tests := []struct {
		name    string
		timeout bool
		handler HandlerFunc
	}{
		{name: "Direct", handler: func(*Context) error { panic(http.ErrAbortHandler) }},
		{name: "UnderTimeout", timeout: true, handler: func(c *Context) error {
			_, _ = c.Response.Write([]byte("buffered, never flushed"))
			panic(http.ErrAbortHandler)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				mu      sync.Mutex
				hooks   int
				failed  int
				handled int
				calls   int
			)
			r := NewV2()
			r.SetErrorHandler(func(*Context, error, ErrorInfo) { mu.Lock(); calls++; mu.Unlock() })
			r.SetEventDispatcher(func(_ context.Context, event interface{}) error {
				mu.Lock()
				defer mu.Unlock()
				switch event.(type) {
				case *RequestFailed:
					failed++
				case *RequestHandled:
					handled++
				}
				return nil
			})
			r.Use(func(next HandlerFunc) HandlerFunc {
				return func(c *Context) error {
					if h, ok := c.Response.(interface{ BeforeFirstWrite(func()) }); ok {
						h.BeforeFirstWrite(func() { mu.Lock(); hooks++; mu.Unlock() })
					}
					return next(c)
				}
			})
			if tt.timeout {
				r.Use(Timeout(time.Minute))
			}
			r.Get("/x", tt.handler)

			w := &writeSpy{ResponseRecorder: httptest.NewRecorder()}
			escaped := func() (p any) {
				defer func() { p = recover() }()
				r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
				return nil
			}()
			if err, ok := escaped.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
				t.Fatalf("ServeHTTP ended with %v, want the http.ErrAbortHandler panic", escaped)
			}
			mu.Lock()
			defer mu.Unlock()
			if w.wrote {
				t.Errorf("wrote %d %q for an aborted request", w.Code, w.Body.String())
			}
			if hooks != 0 || failed != 0 || calls != 0 {
				t.Errorf("hooks = %d, RequestFailed = %d, error handler calls = %d; want 0, 0, 0", hooks, failed, calls)
			}
			if handled != 1 {
				t.Errorf("RequestHandled fired %d times, want 1", handled)
			}
		})
	}
}
