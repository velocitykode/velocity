package router

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

func TestErrorHandlerMiddleware_Paths(t *testing.T) {
	handlerErr := errors.New("boom")

	tests := []struct {
		name        string
		ret         error
		handles     bool
		wantCalled  bool
		wantHandled bool
		wantErr     error
		wantStatus  int
	}{
		{
			name:        "handled error returns Handled wrapping the cause",
			ret:         handlerErr,
			handles:     true,
			wantCalled:  true,
			wantHandled: true,
			wantErr:     handlerErr,
			wantStatus:  http.StatusTeapot,
		},
		{
			name:       "declined error passes through unchanged",
			ret:        handlerErr,
			handles:    false,
			wantCalled: true,
			wantErr:    handlerErr,
			wantStatus: http.StatusOK,
		},
		{
			name:       "success does not call fn",
			ret:        nil,
			wantStatus: http.StatusOK,
		},
		{
			name:        "bare ErrResponseWritten skips fn",
			ret:         contract.ErrResponseWritten,
			handles:     true,
			wantHandled: true,
			wantErr:     contract.ErrResponseWritten,
			wantStatus:  http.StatusOK,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seen error
			called := false
			mw := ErrorHandlerMiddleware(func(c *Context, err error) bool {
				called = true
				seen = err
				if tt.handles {
					c.Response.WriteHeader(http.StatusTeapot)
				}
				return tt.handles
			})
			final := mw(func(c *Context) error { return tt.ret })

			w := httptest.NewRecorder()
			got := final(NewContext(w, httptest.NewRequest("GET", "/", nil)))

			if called != tt.wantCalled {
				t.Fatalf("fn called = %v, want %v", called, tt.wantCalled)
			}
			if called && !errors.Is(seen, handlerErr) {
				t.Errorf("fn saw %v, want %v", seen, handlerErr)
			}
			if tt.wantErr == nil {
				if got != nil {
					t.Fatalf("middleware returned %v, want nil", got)
				}
			} else if !errors.Is(got, tt.wantErr) {
				t.Fatalf("middleware returned %v, want a chain holding %v", got, tt.wantErr)
			}
			if handled := errors.Is(got, contract.ErrResponseWritten); handled != tt.wantHandled {
				t.Errorf("errors.Is(ret, ErrResponseWritten) = %v, want %v", handled, tt.wantHandled)
			}
			if tt.wantHandled && tt.wantCalled && contract.HandledCause(got) != handlerErr {
				t.Errorf("HandledCause = %v, want %v", contract.HandledCause(got), handlerErr)
			}
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

func TestPanicError_Contract(t *testing.T) {
	sentinel := errors.New("inner")
	notFound := contract.NewHTTPError(http.StatusNotFound)

	tests := []struct {
		name     string
		err      *PanicError
		wantText string
		wantIs   error
	}{
		{"string panic", &PanicError{Err: panicerr.FromRecovered("kaboom")}, "panic: kaboom", nil},
		{"error panic unwraps", &PanicError{Err: panicerr.FromRecovered(sentinel)}, "panic: inner", sentinel},
		{"http error panic still 500", &PanicError{Err: panicerr.FromRecovered(notFound)}, "panic: Not Found", notFound},
		{"nil Err", &PanicError{}, "panic", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.wantText {
				t.Errorf("Error() = %q, want %q", got, tt.wantText)
			}
			if tt.wantIs != nil && !errors.Is(tt.err, tt.wantIs) {
				t.Errorf("errors.Is(%v, %v) = false", tt.err, tt.wantIs)
			}
			status, _, ok := contract.StatusOf(fmt.Errorf("wrapped: %w", tt.err))
			if !ok || status != http.StatusInternalServerError {
				t.Errorf("StatusOf = %d, %v, want 500, true", status, ok)
			}
			var rep contract.Reportable
			if !errors.As(tt.err, &rep) || !rep.ShouldReport() {
				t.Error("a PanicError must always report")
			}
		})
	}

	var nilErr *PanicError
	if nilErr.Unwrap() != nil {
		t.Error("nil PanicError Unwrap must be nil")
	}
}
