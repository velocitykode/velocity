package router

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// failedCapture counts RequestFailed events.
type failedCapture struct {
	mu sync.Mutex
	n  int
}

func (f *failedCapture) dispatch(_ context.Context, event interface{}) error {
	if _, ok := event.(*RequestFailed); ok {
		f.mu.Lock()
		f.n++
		f.mu.Unlock()
	}
	return nil
}

func (f *failedCapture) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.n
}

// An explicit StatusError wins over a deadline or an oversized body it
// wraps: the default path answers the explicit status with its headers,
// and only a 503 whose chain holds a deadline logs at warn.
func TestDefaultErrorPath_ExplicitStatusWinsOverFallbacks(t *testing.T) {
	tests := []struct {
		name       string
		handler    HandlerFunc
		wantStatus int
		wantRetry  string
		wantError  int
		wantWarn   int
		wantFailed int
	}{
		{
			name: "500 wrapping MaxBytesError",
			handler: func(*Context) error {
				return contract.NewHTTPError(http.StatusInternalServerError).WithCause(&http.MaxBytesError{Limit: 1024})
			},
			wantStatus: http.StatusInternalServerError, wantError: 1, wantFailed: 1,
		},
		{
			name: "503 with Retry-After wrapping DeadlineExceeded",
			handler: func(*Context) error {
				return contract.NewHTTPError(http.StatusServiceUnavailable).WithHeader("Retry-After", "7").WithCause(context.DeadlineExceeded)
			},
			wantStatus: http.StatusServiceUnavailable, wantRetry: "7", wantWarn: 1, wantFailed: 1,
		},
		{
			name: "502 wrapping DeadlineExceeded",
			handler: func(*Context) error {
				return contract.NewHTTPError(http.StatusBadGateway).WithCause(context.DeadlineExceeded)
			},
			wantStatus: http.StatusBadGateway, wantError: 1, wantFailed: 1,
		},
		{
			name: "404 wrapping DeadlineExceeded",
			handler: func(*Context) error {
				return contract.NewHTTPError(http.StatusNotFound).WithCause(context.DeadlineExceeded)
			},
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "bare MaxBytesError",
			handler:    func(*Context) error { return &http.MaxBytesError{Limit: 1024} },
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name:       "wrapped MaxBytesError",
			handler:    func(*Context) error { return fmt.Errorf("bind: %w", &http.MaxBytesError{Limit: 1024}) },
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name:       "bare DeadlineExceeded",
			handler:    func(*Context) error { return context.DeadlineExceeded },
			wantStatus: http.StatusServiceUnavailable, wantWarn: 1, wantFailed: 1,
		},
		{
			name:       "joined DeadlineExceeded",
			handler:    func(*Context) error { return errors.Join(errors.New("query"), context.DeadlineExceeded) },
			wantStatus: http.StatusServiceUnavailable, wantWarn: 1, wantFailed: 1,
		},
		{
			name:       "panic carrying a 404 HTTPError",
			handler:    func(*Context) error { panic(contract.NewHTTPError(http.StatusNotFound)) },
			wantStatus: http.StatusInternalServerError, wantError: 1, wantFailed: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewV2()
			errLog, warnLog := &logCapture{}, &logCapture{}
			failed := &failedCapture{}
			r.SetErrorLogger(errLog.fn)
			r.SetWarnLogger(warnLog.fn)
			r.SetEventDispatcher(failed.dispatch)
			r.Get("/x", tt.handler)

			w := serveErrLogReq(r, http.MethodGet, "/x")

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if got := w.Header().Get("Retry-After"); got != tt.wantRetry {
				t.Errorf("Retry-After = %q, want %q", got, tt.wantRetry)
			}
			if errLog.count() != tt.wantError || warnLog.count() != tt.wantWarn {
				t.Errorf("logs = error %d warn %d, want error %d warn %d", errLog.count(), warnLog.count(), tt.wantError, tt.wantWarn)
			}
			if failed.count() != tt.wantFailed {
				t.Errorf("RequestFailed = %d, want %d", failed.count(), tt.wantFailed)
			}
		})
	}
}

// The Timeout middleware's explicit 503 wraps the deadline and keeps
// logging at warn.
func TestDefaultErrorPath_TimeoutLogsAtWarn(t *testing.T) {
	r := NewV2()
	errLog, warnLog := &logCapture{}, &logCapture{}
	r.SetErrorLogger(errLog.fn)
	r.SetWarnLogger(warnLog.fn)
	r.Use(Timeout(1))
	r.Get("/slow", func(c *Context) error {
		<-c.Request.Context().Done()
		return nil
	})

	w := serveErrLogReq(r, http.MethodGet, "/slow")

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
	if warnLog.count() != 1 || errLog.count() != 0 {
		t.Errorf("logs = error %d warn %d, want warn 1 only", errLog.count(), warnLog.count())
	}
}

// classifyError must agree with errors.As and errors.Is for every chain
// shape the walk handles by hand or hands off.
func TestClassifyError_ParityWithErrorsPackage(t *testing.T) {
	deep := error(contract.NewHTTPError(http.StatusTeapot).WithCause(context.DeadlineExceeded))
	for i := 0; i < walkLimit+5; i++ {
		deep = fmt.Errorf("layer %d: %w", i, deep)
	}
	pe := &PanicError{Err: errors.New("boom"), Stack: "stack"}
	tests := []struct {
		name string
		err  error
	}{
		{"plain", errors.New("x")},
		{"http error", contract.NewHTTPError(http.StatusConflict)},
		{"wrapped deadline", fmt.Errorf("w: %w", context.DeadlineExceeded)},
		{"joined canceled and maxbytes", errors.Join(context.Canceled, &http.MaxBytesError{})},
		{"handled", contract.Handled(contract.NewHTTPError(http.StatusBadRequest))},
		{"handled panic", contract.Handled(pe)},
		{"panic carrying marker", &PanicError{Err: contract.ErrResponseWritten}},
		{"as method", &asOnlyError{target: contract.NewHTTPError(http.StatusGone), inner: context.Canceled}},
		{"beyond walk limit", deep},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := classifyError(tt.err)
			var se contract.StatusError
			var he contract.HeaderError
			var me contract.MessageError
			var gotPE *PanicError
			var mbe *http.MaxBytesError
			if got, want := f.haveStatus, errors.As(tt.err, &se); got != want || (want && f.status != se) {
				t.Errorf("status = %v %v, errors.As gives %v %v", got, f.status, want, se)
			}
			if got, want := f.haveHeader, errors.As(tt.err, &he); got != want {
				t.Errorf("header = %v, errors.As gives %v", got, want)
			}
			if got, want := f.haveMessage, errors.As(tt.err, &me); got != want || (want && f.message != me) {
				t.Errorf("message = %v, errors.As gives %v", got, want)
			}
			if got, want := f.panicked, errors.As(tt.err, &gotPE); got != want || f.panicErr != gotPE {
				t.Errorf("panic = %v %p, errors.As gives %v %p", got, f.panicErr, want, gotPE)
			}
			if got, want := f.maxBytes, errors.As(tt.err, &mbe); got != want {
				t.Errorf("maxBytes = %v, errors.As gives %v", got, want)
			}
			if got, want := f.canceled, errors.Is(tt.err, context.Canceled); got != want {
				t.Errorf("canceled = %v, errors.Is gives %v", got, want)
			}
			if got, want := f.deadline, errors.Is(tt.err, context.DeadlineExceeded); got != want {
				t.Errorf("deadline = %v, errors.Is gives %v", got, want)
			}
			if got, want := f.written, errors.Is(tt.err, contract.ErrResponseWritten); got != want {
				t.Errorf("written = %v, errors.Is gives %v", got, want)
			}
		})
	}
}

// asOnlyError answers a *contract.StatusError target through its As
// method and unwraps to inner.
type asOnlyError struct {
	target contract.StatusError
	inner  error
}

func (e *asOnlyError) Error() string { return "as only" }
func (e *asOnlyError) Unwrap() error { return e.inner }
func (e *asOnlyError) As(target any) bool {
	if p, ok := target.(*contract.StatusError); ok {
		*p = e.target
		return true
	}
	return false
}

// The unmatched path classifies once and allocates nothing for it.
func TestClassifyError_Allocations(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{"404", unmatchedHTTPError(nil)},
		{"405", unmatchedHTTPError([]string{http.MethodGet})},
		{"wrapped", fmt.Errorf("w: %w", contract.NewHTTPError(http.StatusForbidden))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			allocs := testing.AllocsPerRun(100, func() {
				f := classifyError(tt.err)
				_, _, _ = f.answer()
			})
			if allocs != 0 {
				t.Errorf("classifyError allocated %.0f times, want 0", allocs)
			}
		})
	}
}
