package router

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
)

// headerCountingRecorder counts WriteHeader calls reaching the wire, so a
// test can prove the router wrote nothing at all.
type headerCountingRecorder struct {
	*httptest.ResponseRecorder
	headerCalls int
}

func (w *headerCountingRecorder) WriteHeader(code int) {
	w.headerCalls++
	w.ResponseRecorder.WriteHeader(code)
}

func (w *headerCountingRecorder) Write(p []byte) (int, error) {
	if w.headerCalls == 0 {
		w.headerCalls++
	}
	return w.ResponseRecorder.Write(p)
}

func newHeaderCountingRecorder() *headerCountingRecorder {
	return &headerCountingRecorder{ResponseRecorder: httptest.NewRecorder()}
}

// deadRequest returns a GET request whose context is already cancelled.
func deadRequest(path string) *http.Request {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
}

// hasTestFrame reports whether trace holds a frame from this test file,
// i.e. the structured stack reaches the panic site.
func hasTestFrame(trace *contract.StackTrace) bool {
	if trace == nil {
		return false
	}
	for _, f := range trace.Frames {
		if strings.HasSuffix(f.File, "error_boundary_test.go") {
			return true
		}
	}
	return false
}

func TestDefaultErrorHandler_StandaloneMatrix(t *testing.T) {
	const secret = "db password rotation in flight"

	tests := []struct {
		name        string
		handler     HandlerFunc
		request     func() *http.Request
		wantStatus  int
		wantBody    string
		wantHeader  map[string]string
		wantNoWrite bool
		wantErrLogs int
		wantWarn    int
		wantStack   bool
		wantJSON    *problemBody
	}{
		{
			name:       "direct 404",
			handler:    func(c *Context) error { return contract.NewHTTPError(http.StatusNotFound) },
			wantStatus: http.StatusNotFound,
			wantBody:   "Not Found",
		},
		{
			name: "wrapped 404 echoes its message",
			handler: func(c *Context) error {
				return fmt.Errorf("load user: %w", contract.NewHTTPError(http.StatusNotFound, "no such user"))
			},
			wantStatus: http.StatusNotFound,
			wantBody:   "no such user",
		},
		{
			name: "429 keeps Retry-After",
			handler: func(c *Context) error {
				return contract.NewHTTPError(http.StatusTooManyRequests).WithHeader("Retry-After", "30")
			},
			wantStatus: http.StatusTooManyRequests,
			wantBody:   "Too Many Requests",
			wantHeader: map[string]string{"Retry-After": "30"},
		},
		{
			name: "header with CR or LF is dropped",
			handler: func(c *Context) error {
				return &contract.HTTPError{Status: http.StatusTooManyRequests, Header: http.Header{"Retry-After": {"1\r\nX-Evil: 1"}}}
			},
			wantStatus: http.StatusTooManyRequests,
			wantBody:   "Too Many Requests",
			wantHeader: map[string]string{"Retry-After": "", "X-Evil": ""},
		},
		{
			name:        "plain error is 500 with status text only",
			handler:     func(c *Context) error { return errors.New(secret) },
			wantStatus:  http.StatusInternalServerError,
			wantBody:    "Internal Server Error",
			wantErrLogs: 1,
		},
		{
			name:        "5xx HTTPError hides its message",
			handler:     func(c *Context) error { return contract.NewHTTPError(http.StatusBadGateway, secret) },
			wantStatus:  http.StatusBadGateway,
			wantBody:    "Bad Gateway",
			wantErrLogs: 1,
		},
		{
			name:        "panic is 500 with one error log carrying the stack",
			handler:     func(c *Context) error { panic(secret) },
			wantStatus:  http.StatusInternalServerError,
			wantBody:    "Internal Server Error",
			wantErrLogs: 1,
			wantStack:   true,
		},
		{
			name:        "panic with an HTTPError value is still 500",
			handler:     func(c *Context) error { panic(contract.NewHTTPError(http.StatusNotFound)) },
			wantStatus:  http.StatusInternalServerError,
			wantBody:    "Internal Server Error",
			wantErrLogs: 1,
			wantStack:   true,
		},
		{
			name:       "MaxBytesError is 413",
			handler:    func(c *Context) error { return fmt.Errorf("bind: %w", &http.MaxBytesError{Limit: 10}) },
			wantStatus: http.StatusRequestEntityTooLarge,
			wantBody:   "Request Entity Too Large",
		},
		{
			name:       "deadline is 503 logged at warn",
			handler:    func(c *Context) error { return fmt.Errorf("query: %w", context.DeadlineExceeded) },
			wantStatus: http.StatusServiceUnavailable,
			wantBody:   "Service Unavailable",
			wantWarn:   1,
		},
		{
			name:        "cancel with a dead request context writes nothing",
			handler:     func(c *Context) error { return c.Request.Context().Err() },
			request:     func() *http.Request { return deadRequest("/err") },
			wantNoWrite: true,
		},
		{
			name:        "cancel with a live request context is 500",
			handler:     func(c *Context) error { return fmt.Errorf("upstream: %w", context.Canceled) },
			wantStatus:  http.StatusInternalServerError,
			wantBody:    "Internal Server Error",
			wantErrLogs: 1,
		},
		{
			name:        "bare ErrResponseWritten writes nothing",
			handler:     func(c *Context) error { return contract.ErrResponseWritten },
			wantNoWrite: true,
		},
		{
			name: "committed partial write gets no second body",
			handler: func(c *Context) error {
				c.Response.WriteHeader(http.StatusCreated)
				_, _ = c.Response.Write([]byte("partial"))
				return errors.New(secret)
			},
			wantStatus:  http.StatusCreated,
			wantBody:    "partial",
			wantErrLogs: 1,
		},
		{
			name: "JSON client gets problem+json",
			handler: func(c *Context) error {
				return contract.NewHTTPError(http.StatusNotFound, "no such user")
			},
			request: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/err", nil)
				r.Header.Set("Accept", "application/json")
				return r
			},
			wantStatus: http.StatusNotFound,
			wantHeader: map[string]string{"Content-Type": "application/problem+json"},
			wantJSON:   &problemBody{Type: "about:blank", Title: "Not Found", Status: 404, Detail: "no such user", Instance: "/err"},
		},
		{
			name:    "JSON client 5xx shows status text only",
			handler: func(c *Context) error { return errors.New(secret) },
			request: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/err", nil)
				r.Header.Set("Accept", "application/problem+json")
				return r
			},
			wantStatus:  http.StatusInternalServerError,
			wantHeader:  map[string]string{"Content-Type": "application/problem+json"},
			wantJSON:    &problemBody{Type: "about:blank", Title: "Internal Server Error", Status: 500, Detail: "Internal Server Error", Instance: "/err"},
			wantErrLogs: 1,
		},
		{
			name: "Inertia client gets text",
			handler: func(c *Context) error {
				return contract.NewHTTPError(http.StatusForbidden)
			},
			request: func() *http.Request {
				r := httptest.NewRequest(http.MethodGet, "/err", nil)
				r.Header.Set("Accept", "application/json")
				r.Header.Set("X-Inertia", "true")
				return r
			},
			wantStatus: http.StatusForbidden,
			wantBody:   "Forbidden",
			wantHeader: map[string]string{"Content-Type": "text/plain; charset=utf-8"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewV2()
			errLog, warnLog := &logCapture{}, &logCapture{}
			r.SetErrorLogger(errLog.fn)
			r.SetWarnLogger(warnLog.fn)
			r.Get("/err", tt.handler)

			req := httptest.NewRequest(http.MethodGet, "/err", nil)
			if tt.request != nil {
				req = tt.request()
			}
			w := newHeaderCountingRecorder()
			r.ServeHTTP(w, req)

			if tt.wantNoWrite {
				if w.headerCalls != 0 || w.Body.Len() != 0 {
					t.Fatalf("expected nothing written, got %d header writes and body %q", w.headerCalls, w.Body.String())
				}
			} else {
				if w.Code != tt.wantStatus {
					t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
				}
				if strings.Contains(w.Body.String(), secret) {
					t.Errorf("server-side detail leaked: %q", w.Body.String())
				}
			}
			if tt.wantBody != "" {
				if got := strings.TrimSpace(w.Body.String()); got != tt.wantBody {
					t.Errorf("body = %q, want %q", got, tt.wantBody)
				}
			}
			for k, v := range tt.wantHeader {
				if got := w.Header().Get(k); got != v {
					t.Errorf("header %s = %q, want %q", k, got, v)
				}
			}
			if tt.wantJSON != nil {
				var got problemBody
				if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
					t.Fatalf("body is not JSON: %v (%q)", err, w.Body.String())
				}
				if got != *tt.wantJSON {
					t.Errorf("problem body = %+v, want %+v", got, *tt.wantJSON)
				}
			}
			if errLog.count() != tt.wantErrLogs {
				t.Errorf("error log entries = %d, want %d", errLog.count(), tt.wantErrLogs)
			}
			if warnLog.count() != tt.wantWarn {
				t.Errorf("warn log entries = %d, want %d", warnLog.count(), tt.wantWarn)
			}
			if tt.wantStack {
				v, ok := errLog.kv(0, "stack")
				if !ok || !strings.Contains(v.(string), "goroutine") {
					t.Errorf("panic log entry must carry the raw stack, got %v", v)
				}
				if v, _ := errLog.kv(0, "error"); !strings.Contains(fmt.Sprint(v), "panic") {
					t.Errorf("panic log entry error kv = %v", v)
				}
			}
		})
	}
}

// seamCall records one invocation of the SetErrorHandler seam.
type seamCall struct {
	err  error
	info ErrorInfo
}

type seamRecorder struct {
	mu    sync.Mutex
	calls []seamCall
	// body, when non-empty, is written unless the response is committed.
	body string
}

func (s *seamRecorder) fn(c *Context, err error, info ErrorInfo) {
	s.mu.Lock()
	s.calls = append(s.calls, seamCall{err: err, info: info})
	s.mu.Unlock()
	if s.body != "" && !info.Committed && !errors.Is(err, contract.ErrResponseWritten) {
		c.Response.WriteHeader(http.StatusInternalServerError)
		_, _ = c.Response.Write([]byte(s.body))
	}
}

func (s *seamRecorder) get() []seamCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]seamCall(nil), s.calls...)
}

func TestSetErrorHandler_SeamMatrix(t *testing.T) {
	cause := errors.New("handler failed")

	tests := []struct {
		name          string
		use           []MiddlewareFunc
		handler       HandlerFunc
		wantCalls     int
		wantRecovered bool
		wantCommitted bool
		wantPanicErr  bool
		wantHandled   bool
		wantBody      string
		wantStatus    int
	}{
		{
			name:          "panic arrives recovered with a structured trace",
			handler:       func(c *Context) error { panic("kaboom") },
			wantCalls:     1,
			wantRecovered: true,
			wantPanicErr:  true,
			wantBody:      "seam",
			wantStatus:    http.StatusInternalServerError,
		},
		{
			name: "partial write arrives committed and gets no second body",
			handler: func(c *Context) error {
				c.Response.WriteHeader(http.StatusAccepted)
				_, _ = c.Response.Write([]byte("partial"))
				return cause
			},
			wantCalls:     1,
			wantCommitted: true,
			wantBody:      "partial",
			wantStatus:    http.StatusAccepted,
		},
		{
			name: "panic after a partial write is recovered and committed",
			handler: func(c *Context) error {
				_, _ = c.Response.Write([]byte("partial"))
				panic("late")
			},
			wantCalls:     1,
			wantRecovered: true,
			wantCommitted: true,
			wantPanicErr:  true,
			wantBody:      "partial",
			wantStatus:    http.StatusOK,
		},
		{
			name:          "Timeout panic arrives as *PanicError",
			use:           []MiddlewareFunc{Timeout(2 * time.Second)},
			handler:       func(c *Context) error { panic("timeout goroutine boom") },
			wantCalls:     1,
			wantRecovered: true,
			wantPanicErr:  true,
			wantBody:      "seam",
			wantStatus:    http.StatusInternalServerError,
		},
		{
			name:       "plain error arrives uncommitted",
			handler:    func(c *Context) error { return cause },
			wantCalls:  1,
			wantBody:   "seam",
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "bare ErrResponseWritten never reaches the seam",
			handler: func(c *Context) error {
				c.Response.WriteHeader(http.StatusSeeOther)
				return contract.ErrResponseWritten
			},
			wantCalls:  0,
			wantStatus: http.StatusSeeOther,
		},
		{
			name: "middleware-handled error reaches the seam once, committed",
			use: []MiddlewareFunc{ErrorHandlerMiddleware(func(c *Context, err error) bool {
				_ = c.String(http.StatusTeapot, "mw")
				return true
			})},
			handler:       func(c *Context) error { return cause },
			wantCalls:     1,
			wantCommitted: true,
			wantHandled:   true,
			wantBody:      "mw",
			wantStatus:    http.StatusTeapot,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewV2()
			errLog := &logCapture{}
			r.SetErrorLogger(errLog.fn)
			seam := &seamRecorder{body: "seam"}
			r.SetErrorHandler(seam.fn)
			for _, mw := range tt.use {
				r.Use(mw)
			}
			r.Get("/err", tt.handler)

			w := httptest.NewRecorder()
			r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/err", nil))

			calls := seam.get()
			if len(calls) != tt.wantCalls {
				t.Fatalf("seam calls = %d, want %d", len(calls), tt.wantCalls)
			}
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && w.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want exactly %q (no second body)", w.Body.String(), tt.wantBody)
			}
			if errLog.count() != 0 {
				t.Errorf("router logged %d entries with a seam installed, want 0", errLog.count())
			}
			if tt.wantCalls == 0 {
				return
			}
			got := calls[0]
			if got.info.Recovered != tt.wantRecovered {
				t.Errorf("Recovered = %v, want %v", got.info.Recovered, tt.wantRecovered)
			}
			if got.info.Committed != tt.wantCommitted {
				t.Errorf("Committed = %v, want %v", got.info.Committed, tt.wantCommitted)
			}
			if got.info.RequestID == "" || got.info.TraceID == "" || got.info.SpanID == "" {
				t.Errorf("IDs missing: %+v", got.info)
			}
			var pe *PanicError
			if isPanic := errors.As(got.err, &pe); isPanic != tt.wantPanicErr {
				t.Fatalf("errors.As(*PanicError) = %v, want %v (err %v)", isPanic, tt.wantPanicErr, got.err)
			}
			if tt.wantRecovered {
				if got.info.StackTrace == nil || len(got.info.StackTrace.Frames) == 0 {
					t.Fatal("recovered error must carry a non-nil structured StackTrace")
				}
				if !hasTestFrame(got.info.StackTrace) {
					t.Errorf("StackTrace does not reach the panic site:\n%s", got.info.StackTrace)
				}
				if !strings.Contains(got.info.Stack, "goroutine") {
					t.Errorf("raw Stack missing: %q", got.info.Stack)
				}
				if pe.Trace != got.info.StackTrace || pe.Stack != got.info.Stack {
					t.Error("ErrorInfo stacks must be the ones the PanicError carries")
				}
			}
			if handled := errors.Is(got.err, contract.ErrResponseWritten); handled != tt.wantHandled {
				t.Errorf("handled = %v, want %v", handled, tt.wantHandled)
			}
			if tt.wantHandled && contract.HandledCause(got.err) != cause {
				t.Errorf("HandledCause = %v, want %v", contract.HandledCause(got.err), cause)
			}
		})
	}
}

func TestSetErrorHandler_NilRestoresDefault(t *testing.T) {
	r := NewV2()
	r.SetErrorHandler(func(c *Context, err error, info ErrorInfo) { t.Error("seam must not run") })
	r.SetErrorHandler(nil)
	r.Get("/err", func(c *Context) error { return contract.NewHTTPError(http.StatusGone) })

	w := serveErrLogReq(r, http.MethodGet, "/err")
	if w.Code != http.StatusGone {
		t.Fatalf("status = %d, want 410", w.Code)
	}
}

func TestErrorHandlerMiddleware_HandledPathThroughRouter(t *testing.T) {
	cause := errors.New("render me")

	tests := []struct {
		name        string
		seam        bool
		wantErrLogs int
	}{
		{name: "seam installed", seam: true},
		{name: "default path", seam: false, wantErrLogs: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := newTestEventCollector()
			r := NewV2()
			r.SetEventDispatcher(collector.dispatch)
			errLog := &logCapture{}
			r.SetErrorLogger(errLog.fn)
			seam := &seamRecorder{}
			if tt.seam {
				r.SetErrorHandler(seam.fn)
			}
			r.Use(ErrorHandlerMiddleware(func(c *Context, err error) bool {
				_ = c.String(http.StatusBadGateway, "rendered by middleware")
				return true
			}))
			r.Get("/err", func(c *Context) error { return cause })

			w := serveErrLogReq(r, http.MethodGet, "/err")

			if w.Code != http.StatusBadGateway || w.Body.String() != "rendered by middleware" {
				t.Fatalf("response = %d %q, want the middleware's only", w.Code, w.Body.String())
			}
			var failed []*RequestFailed
			for _, e := range collector.getEvents() {
				if ev, ok := e.(*RequestFailed); ok {
					failed = append(failed, ev)
				}
			}
			if len(failed) != 1 {
				t.Fatalf("RequestFailed fired %d times, want 1", len(failed))
			}
			if failed[0].Error != cause {
				t.Errorf("RequestFailed.Error = %v, want the cause %v", failed[0].Error, cause)
			}
			if tt.seam && len(seam.get()) != 1 {
				t.Errorf("seam calls = %d, want 1", len(seam.get()))
			}
			if errLog.count() != tt.wantErrLogs {
				t.Errorf("error log entries = %d, want %d", errLog.count(), tt.wantErrLogs)
			}
		})
	}
}

func TestRequestFailed_Policy(t *testing.T) {
	boom := errors.New("boom")

	tests := []struct {
		name          string
		use           []MiddlewareFunc
		handler       HandlerFunc
		wantFired     bool
		wantRecovered bool
	}{
		{"4xx does not fire", nil, func(c *Context) error { return contract.NewHTTPError(http.StatusNotFound) }, false, false},
		{"429 does not fire", nil, func(c *Context) error {
			return contract.NewHTTPError(http.StatusTooManyRequests).WithHeader("Retry-After", "1")
		}, false, false},
		{"413 does not fire", nil, func(c *Context) error { return &http.MaxBytesError{Limit: 1} }, false, false},
		{"plain error fires", nil, func(c *Context) error { return boom }, true, false},
		{"5xx HTTPError fires", nil, func(c *Context) error { return contract.NewHTTPError(http.StatusServiceUnavailable) }, true, false},
		{"deadline fires", nil, func(c *Context) error { return context.DeadlineExceeded }, true, false},
		{"panic fires recovered", nil, func(c *Context) error { panic("boom") }, true, true},
		{"Timeout panic fires recovered", []MiddlewareFunc{Timeout(2 * time.Second)}, func(c *Context) error { panic("boom") }, true, true},
		{"bare ErrResponseWritten does not fire", nil, func(c *Context) error { return contract.ErrResponseWritten }, false, false},
		{"Handled 4xx cause does not fire", nil, func(c *Context) error {
			return contract.Handled(contract.NewHTTPError(http.StatusBadRequest))
		}, false, false},
		{"Handled plain cause fires", nil, func(c *Context) error { return contract.Handled(boom) }, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := newTestEventCollector()
			r := NewV2()
			r.SetEventDispatcher(collector.dispatch)
			for _, mw := range tt.use {
				r.Use(mw)
			}
			r.Get("/err", tt.handler)
			serveErrLogReq(r, http.MethodGet, "/err")

			var failed []*RequestFailed
			for _, e := range collector.getEvents() {
				if ev, ok := e.(*RequestFailed); ok {
					failed = append(failed, ev)
				}
			}
			if (len(failed) == 1) != tt.wantFired || len(failed) > 1 {
				t.Fatalf("RequestFailed fired %d times, wantFired %v", len(failed), tt.wantFired)
			}
			if !tt.wantFired {
				return
			}
			ev := failed[0]
			if ev.Recovered != tt.wantRecovered {
				t.Errorf("Recovered = %v, want %v", ev.Recovered, tt.wantRecovered)
			}
			if tt.wantRecovered && !strings.Contains(ev.Stack, "goroutine") {
				t.Errorf("recovered event must carry the stack, got %q", ev.Stack)
			}
			if errors.Is(ev.Error, contract.ErrResponseWritten) {
				t.Error("RequestFailed must carry the cause, not the Handled wrapper")
			}
		})
	}
}

func TestContext_RenderContext(t *testing.T) {
	tests := []struct {
		name        string
		viaRouter   bool
		prewrite    bool
		status      int
		target      string
		wantErr     bool
		wantBadReq  bool
		wantStatus  int
		wantLoc     string
		wantWritten bool
	}{
		{name: "safe redirect through router", viaRouter: true, status: http.StatusSeeOther, target: "/login", wantStatus: http.StatusSeeOther, wantLoc: "/login", wantWritten: true},
		{name: "safe redirect bare writer", status: http.StatusFound, target: "/home", wantStatus: http.StatusFound, wantLoc: "/home", wantWritten: true},
		{name: "protocol relative target refused", viaRouter: true, status: http.StatusFound, target: "//evil.example", wantErr: true, wantBadReq: true},
		{name: "absolute foreign host refused", status: http.StatusFound, target: "https://evil.example/", wantErr: true, wantBadReq: true},
		{name: "non 3xx refused", viaRouter: true, status: http.StatusOK, target: "/x", wantErr: true},
		{name: "written response refused", viaRouter: true, prewrite: true, status: http.StatusFound, target: "/x", wantErr: true, wantWritten: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				redirectErr error
				written     bool
			)
			run := func(c *Context) error {
				if tt.prewrite {
					c.Response.WriteHeader(http.StatusAccepted)
				}
				rc := c.RenderContext()
				redirectErr = rc.Redirect(tt.status, tt.target)
				written = rc.Written()
				return nil
			}
			w := httptest.NewRecorder()
			if tt.viaRouter {
				r := NewV2()
				r.Get("/p", run)
				r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/p", nil))
			} else {
				_ = run(NewContext(w, httptest.NewRequest(http.MethodGet, "/p", nil)))
			}

			if (redirectErr != nil) != tt.wantErr {
				t.Fatalf("Redirect error = %v, wantErr %v", redirectErr, tt.wantErr)
			}
			if written != tt.wantWritten {
				t.Errorf("Written = %v, want %v", written, tt.wantWritten)
			}
			if tt.wantErr {
				if !errors.Is(redirectErr, contract.ErrInvalidRedirect) {
					t.Errorf("error %v does not match ErrInvalidRedirect", redirectErr)
				}
				status, _, _ := contract.StatusOf(redirectErr)
				if tt.wantBadReq != (status == http.StatusBadRequest) {
					t.Errorf("error status = %d, wantBadReq %v", status, tt.wantBadReq)
				}
				if w.Header().Get("Location") != "" {
					t.Error("a refused redirect must write no Location")
				}
				return
			}
			if w.Code != tt.wantStatus || w.Header().Get("Location") != tt.wantLoc {
				t.Errorf("got %d Location %q, want %d %q", w.Code, w.Header().Get("Location"), tt.wantStatus, tt.wantLoc)
			}
		})
	}
}

func TestContext_RenderContextWrites(t *testing.T) {
	r := NewV2()
	r.Get("/p", func(c *Context) error {
		rc := c.RenderContext()
		if rc.Request() != c.Request || rc.Writer() != c.Response {
			t.Error("adapter must expose the context's request and writer")
		}
		rc.SetHeader("X-Ok", "1")
		rc.SetHeader("X-Bad", "a\r\nX-Evil: 1")
		rc.SetHeader("", "x")
		rc.WriteHeader(42)
		rc.WriteHeader(http.StatusTeapot)
		_, _ = rc.Write([]byte("body"))
		if !rc.Written() {
			t.Error("Written must be true after a write")
		}
		return nil
	})
	w := serveErrLogReq(r, http.MethodGet, "/p")
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 for an invalid first status", w.Code)
	}
	if w.Header().Get("X-Ok") != "1" || w.Header().Get("X-Bad") != "" || w.Header().Get("X-Evil") != "" {
		t.Errorf("headers = %v", w.Header())
	}
	if w.Body.String() != "body" {
		t.Errorf("body = %q", w.Body.String())
	}

	// Write without WriteHeader commits 200 once.
	c, rec := NewTestContext(http.MethodGet, "/p")
	rc := c.RenderContext()
	_, _ = rc.Write([]byte("a"))
	rc.WriteHeader(http.StatusTeapot)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestContext_NegotiationPredicates(t *testing.T) {
	tests := []struct {
		name        string
		accept      string
		inertia     string
		wantJSON    bool
		wantInertia bool
	}{
		{"json", "application/json", "", true, false},
		{"inertia", "application/json", "true", false, true},
		{"html", "text/html", "", false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewTestContext(http.MethodGet, "/")
			c.Request.Header.Set("Accept", tt.accept)
			if tt.inertia != "" {
				c.Request.Header.Set("X-Inertia", tt.inertia)
			}
			if c.WantsJSON() != tt.wantJSON || c.IsInertia() != tt.wantInertia {
				t.Errorf("WantsJSON %v IsInertia %v, want %v %v", c.WantsJSON(), c.IsInertia(), tt.wantJSON, tt.wantInertia)
			}
			rc := c.RenderContext()
			if rc.WantsJSON() != tt.wantJSON || rc.IsInertia() != tt.wantInertia {
				t.Error("RenderContext predicates must match the Context's")
			}
		})
	}
}

// fakeErrorHandler implements contract.ErrorHandler for Report; any other
// method panics through the nil embedded interface.
type fakeErrorHandler struct {
	contract.ErrorHandler
	mu       sync.Mutex
	reported []error
	ctxs     []*contract.ErrorContext
}

func (f *fakeErrorHandler) Report(err error, ctx *contract.ErrorContext) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reported = append(f.reported, err)
	f.ctxs = append(f.ctxs, ctx)
}

func TestContext_Report(t *testing.T) {
	boom := errors.New("boom")

	tests := []struct {
		name         string
		services     func(h *fakeErrorHandler) *app.Services
		err          error
		wantReported int
		wantMarked   bool
	}{
		{"no services returns err unchanged", func(*fakeErrorHandler) *app.Services { return nil }, boom, 0, false},
		{"no handler returns err unchanged", func(*fakeErrorHandler) *app.Services { return &app.Services{} }, boom, 0, false},
		{"handler reports and marks", func(h *fakeErrorHandler) *app.Services { return &app.Services{Errors: h} }, boom, 1, true},
		{"nil err is nil", func(h *fakeErrorHandler) *app.Services { return &app.Services{Errors: h} }, nil, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := &fakeErrorHandler{}
			c, _ := NewTestContext(http.MethodPost, "/orders?id=1&signature=secret")
			c.Request.Header.Set("User-Agent", "probe")
			c.services = tt.services(h)

			got := c.Report(tt.err)

			if tt.err == nil {
				if got != nil {
					t.Fatalf("Report(nil) = %v", got)
				}
				return
			}
			if !errors.Is(got, tt.err) || got.Error() != tt.err.Error() {
				t.Errorf("Report returned %v, want a transparent %v", got, tt.err)
			}
			if contract.IsReported(got) != tt.wantMarked {
				t.Errorf("IsReported = %v, want %v", contract.IsReported(got), tt.wantMarked)
			}
			if len(h.reported) != tt.wantReported {
				t.Fatalf("reported %d, want %d", len(h.reported), tt.wantReported)
			}
			if tt.wantReported == 1 {
				ec := h.ctxs[0]
				if ec == nil || ec.Method != http.MethodPost || ec.URL != "/orders" || ec.UserAgent != "probe" || ec.Timestamp.IsZero() {
					t.Errorf("error context = %+v, want URL /orders with no query string", ec)
				}
			}
		})
	}
}

func TestContext_Errors(t *testing.T) {
	tests := []struct {
		name      string
		services  *app.Services
		wantPanic bool
	}{
		{name: "returns the error handler", services: &app.Services{Errors: &fakeErrorHandler{}}},
		{name: "panics when the error handler is unset", services: &app.Services{}, wantPanic: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := NewTestContext(http.MethodGet, "/")
			c.services = tt.services
			defer func() {
				if p := recover(); (p != nil) != tt.wantPanic {
					t.Fatalf("recover() = %v, want panic %v", p, tt.wantPanic)
				}
			}()
			if got := c.Errors(); got != tt.services.Errors {
				t.Errorf("Errors() = %v, want %v", got, tt.services.Errors)
			}
		})
	}
}

func TestWrap_CommittedAndWrittenSentinel(t *testing.T) {
	tests := []struct {
		name       string
		handler    HandlerFunc
		wantStatus int
		wantBody   string
	}{
		{
			name: "partial write gets no second body",
			handler: func(c *Context) error {
				c.Response.WriteHeader(http.StatusAccepted)
				_, _ = c.Response.Write([]byte("partial"))
				return errors.New("late")
			},
			wantStatus: http.StatusAccepted,
			wantBody:   "partial",
		},
		{
			name: "ErrResponseWritten writes nothing more",
			handler: func(c *Context) error {
				c.Response.WriteHeader(http.StatusSeeOther)
				return contract.ErrResponseWritten
			},
			wantStatus: http.StatusSeeOther,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			Wrap(tt.handler)(w, httptest.NewRequest(http.MethodGet, "/", nil))
			if w.Code != tt.wantStatus || w.Body.String() != tt.wantBody {
				t.Errorf("got %d %q, want %d %q", w.Code, w.Body.String(), tt.wantStatus, tt.wantBody)
			}
		})
	}
}

func TestDefaultErrorHandler_Guards(t *testing.T) {
	// nil inputs are no-ops.
	DefaultErrorHandler(nil, errors.New("x"), ErrorInfo{})
	c, w := NewTestContext(http.MethodGet, "/")
	DefaultErrorHandler(c, nil, ErrorInfo{})
	if w.Body.Len() != 0 {
		t.Fatalf("nil error wrote %q", w.Body.String())
	}

	// An out-of-range status resolves to 500.
	c, w = NewTestContext(http.MethodGet, "/")
	DefaultErrorHandler(c, &contract.HTTPError{Status: 42}, ErrorInfo{})
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}

	// Recovered always answers 500, whatever the error names.
	c, w = NewTestContext(http.MethodGet, "/")
	DefaultErrorHandler(c, contract.NewHTTPError(http.StatusNotFound), ErrorInfo{Recovered: true})
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}

	// A status net/http does not name gets a synthesized title.
	c, w = NewTestContext(http.MethodGet, "/")
	c.Request.Header.Set("Accept", "application/json")
	DefaultErrorHandler(c, contract.NewHTTPError(599, ""), ErrorInfo{})
	var body problemBody
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil || body.Title != "status 599" || body.Status != 599 {
		t.Errorf("body = %+v (%v)", body, err)
	}
}
