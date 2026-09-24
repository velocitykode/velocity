package router

import (
	"context"
	"errors"
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
