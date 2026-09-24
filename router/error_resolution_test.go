package router

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// httpErrorWrapper is a typed wrapper (not fmt.Errorf) around an error, so
// the table covers a custom Unwrap chain as well as %w.
type httpErrorWrapper struct {
	op  string
	err error
}

func (w *httpErrorWrapper) Error() string { return w.op + ": " + w.err.Error() }
func (w *httpErrorWrapper) Unwrap() error { return w.err }

// errorResolutionCases is shared by the router default path and Wrap so the
// two are held to one mapping.
var errorResolutionCases = []struct {
	name     string
	err      error
	wantCode int
	wantBody string
	wantLog  bool
}{
	{
		name:     "direct 4xx HTTPError echoes its message",
		err:      contract.NewHTTPError(http.StatusNotFound, "no such user"),
		wantCode: http.StatusNotFound,
		wantBody: "no such user",
	},
	{
		name:     "fmt wrapped 4xx HTTPError keeps status and message",
		err:      fmt.Errorf("load user: %w", contract.NewHTTPError(http.StatusNotFound, "no such user")),
		wantCode: http.StatusNotFound,
		wantBody: "no such user",
	},
	{
		name:     "double wrapped 4xx HTTPError keeps status and message",
		err:      fmt.Errorf("handler: %w", fmt.Errorf("service: %w", contract.NewHTTPError(http.StatusUnprocessableEntity, "name is required"))),
		wantCode: http.StatusUnprocessableEntity,
		wantBody: "name is required",
	},
	{
		name:     "typed wrapper around 4xx HTTPError keeps status",
		err:      &httpErrorWrapper{op: "authorize", err: contract.NewHTTPError(http.StatusForbidden)},
		wantCode: http.StatusForbidden,
		wantBody: http.StatusText(http.StatusForbidden),
	},
	{
		name:     "HTTPError carrying an internal cause keeps its own status",
		err:      &contract.HTTPError{Status: http.StatusConflict, Message: "version conflict", Cause: errors.New("row changed")},
		wantCode: http.StatusConflict,
		wantBody: "version conflict",
	},
	{
		name:     "wrapped 5xx HTTPError keeps status, hides message, logs",
		err:      fmt.Errorf("upstream: %w", contract.NewHTTPError(http.StatusServiceUnavailable, "db password rotation in flight")),
		wantCode: http.StatusServiceUnavailable,
		wantBody: http.StatusText(http.StatusServiceUnavailable),
		wantLog:  true,
	},
	{
		name:     "plain error is a generic 500 and logs",
		err:      errors.New("db exploded"),
		wantCode: http.StatusInternalServerError,
		wantBody: http.StatusText(http.StatusInternalServerError),
		wantLog:  true,
	},
	{
		name:     "wrapped plain error is a generic 500 and logs",
		err:      fmt.Errorf("query: %w", errors.New("db exploded")),
		wantCode: http.StatusInternalServerError,
		wantBody: http.StatusText(http.StatusInternalServerError),
		wantLog:  true,
	},
}

func TestHandleError_DefaultPathResolvesWithErrorsAs(t *testing.T) {
	for _, tt := range errorResolutionCases {
		t.Run(tt.name, func(t *testing.T) {
			r := NewV2()
			capture := &logCapture{}
			r.SetErrorLogger(capture.fn)
			r.Get("/err", func(c *Context) error { return tt.err })

			w := serveErrLogReq(r, "GET", "/err")

			if w.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tt.wantCode)
			}
			if got := strings.TrimSpace(w.Body.String()); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
			wantEntries := 0
			if tt.wantLog {
				wantEntries = 1
			}
			if capture.count() != wantEntries {
				t.Errorf("log entries = %d, want %d", capture.count(), wantEntries)
			}
		})
	}
}

func TestWrap_SharesDefaultErrorResolution(t *testing.T) {
	for _, tt := range errorResolutionCases {
		t.Run(tt.name, func(t *testing.T) {
			h := Wrap(func(c *Context) error { return tt.err })

			w := httptest.NewRecorder()
			h(w, httptest.NewRequest("GET", "/err", nil))

			if w.Code != tt.wantCode {
				t.Errorf("status = %d, want %d", w.Code, tt.wantCode)
			}
			if got := strings.TrimSpace(w.Body.String()); got != tt.wantBody {
				t.Errorf("body = %q, want %q", got, tt.wantBody)
			}
		})
	}
}

// A recovered panic is a bug, not a response: even a panic value that is
// an *HTTPError (reachable by errors.As through the recovered-panic
// wrapper) responds 500 and is logged with its stack.
func TestHandleError_PanicWithHTTPErrorValueIs500(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{"direct 4xx HTTPError", contract.NewHTTPError(http.StatusNotFound, "no such user")},
		{"wrapped 4xx HTTPError", fmt.Errorf("load: %w", contract.NewHTTPError(http.StatusForbidden, "denied"))},
		{"5xx HTTPError", contract.NewHTTPError(http.StatusServiceUnavailable, "down")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewV2()
			capture := &logCapture{}
			r.SetErrorLogger(capture.fn)
			r.Get("/panic", func(c *Context) error { panic(tt.value) })

			w := serveErrLogReq(r, "GET", "/panic")

			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", w.Code)
			}
			if got := strings.TrimSpace(w.Body.String()); got != http.StatusText(http.StatusInternalServerError) {
				t.Errorf("body = %q, want generic 500 text", got)
			}
			if capture.count() != 1 {
				t.Fatalf("log entries = %d, want 1", capture.count())
			}
			if v, ok := capture.kv(0, "stack"); !ok || v.(string) == "" {
				t.Error("panic entry must carry a non-empty stack kv")
			}
		})
	}
}
