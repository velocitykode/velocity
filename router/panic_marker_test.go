package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
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

// TestFinalize_PanickingHookIsRecoveredAndLogged asserts a BeforeFirstWrite
// hook that panics when the router fires it after a handler that wrote
// nothing never escapes ServeHTTP: the client gets its response, the
// router's error logger records the panic with its stack, and
// RequestHandled still fires.
func TestFinalize_PanickingHookIsRecoveredAndLogged(t *testing.T) {
	r := NewV2()
	errLog := &logCapture{}
	r.SetErrorLogger(errLog.fn)
	var (
		mu      sync.Mutex
		handled int
	)
	r.SetEventDispatcher(func(_ context.Context, event interface{}) error {
		if _, ok := event.(*RequestHandled); ok {
			mu.Lock()
			handled++
			mu.Unlock()
		}
		return nil
	})
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			if h, ok := c.Response.(interface{ BeforeFirstWrite(func()) }); ok {
				h.BeforeFirstWrite(func() { panic("hook exploded") })
			}
			return next(c)
		}
	})
	r.Get("/quiet", func(*Context) error { return nil })

	srv := httptest.NewServer(r)
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/quiet")
	if err != nil {
		t.Fatalf("GET: %v (the hook panic escaped the router)", err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want the implicit 200", resp.StatusCode)
	}
	if errLog.count() != 1 {
		t.Fatalf("error log entries = %d, want 1", errLog.count())
	}
	if v, _ := errLog.kv(0, "panic"); v != "hook exploded" {
		t.Errorf("logged panic = %v, want hook exploded", v)
	}
	if v, _ := errLog.kv(0, "stack"); v == nil || v == "" {
		t.Error("logged no stack")
	}
	if v, _ := errLog.kv(0, "path"); v != "/quiet" {
		t.Errorf("logged path = %v, want /quiet", v)
	}
	mu.Lock()
	defer mu.Unlock()
	if handled != 1 {
		t.Errorf("RequestHandled dispatched %d times, want 1", handled)
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
