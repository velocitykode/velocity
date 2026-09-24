package routerbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/csrf/stores"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/validation"
)

// spyHandler records what HandleRequest received and then behaves like the
// fake handler (writes the resolved status when nothing was written).
type spyHandler struct {
	*problem.FakeHandler

	mu     sync.Mutex
	calls  int
	err    error
	ctx    *contract.ErrorContext
	rcWasW bool
}

func newSpy() *spyHandler {
	return &spyHandler{FakeHandler: problem.NewFakeHandler()}
}

func (s *spyHandler) HandleRequest(rc contract.RenderContext, err error, ctx *contract.ErrorContext) {
	s.mu.Lock()
	s.calls++
	s.err = err
	s.ctx = ctx
	s.rcWasW = rc.Written()
	s.mu.Unlock()
	s.FakeHandler.HandleRequest(rc, err, ctx)
}

// staticUser is a RequestUserIdentifier naming one user.
type staticUser string

func (u staticUser) RequestUserID(*http.Request) string { return string(u) }

// panicUser is a RequestUserIdentifier that panics.
type panicUser struct{}

func (panicUser) RequestUserID(*http.Request) string { panic("identifier exploded") }

// serve registers handler on a fresh router, installs the bridge with
// opts, serves one GET and returns the recorder.
func serve(t *testing.T, handler router.HandlerFunc, opts ...Option) *httptest.ResponseRecorder {
	t.Helper()
	r := router.New()
	Install(r, opts...)
	r.Get("/x", handler)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x?token=secret", nil)
	req.Header.Set("User-Agent", "bridge-test")
	r.ServeHTTP(w, req)
	return w
}

func TestInstall_HandlerErrors(t *testing.T) {
	tests := []struct {
		name          string
		handler       router.HandlerFunc
		userID        contract.RequestUserIdentifier
		wantStatus    int
		wantRecovered bool
		wantUser      string
	}{
		{
			name:       "status error",
			handler:    func(*router.Context) error { return problem.NotFound() },
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "plain error with user facet",
			handler:    func(*router.Context) error { return errors.New("boom") },
			userID:     staticUser("user-7"),
			wantStatus: http.StatusInternalServerError,
			wantUser:   "user-7",
		},
		{
			name:       "panicking user facet leaves the user empty",
			handler:    func(*router.Context) error { return errors.New("boom") },
			userID:     panicUser{},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:          "recovered panic",
			handler:       func(*router.Context) error { panic("handler exploded") },
			wantStatus:    http.StatusInternalServerError,
			wantRecovered: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := newSpy()
			opts := []Option{WithHandler(func() contract.ErrorHandler { return spy })}
			if tt.userID != nil {
				opts = append(opts, WithUserID(tt.userID))
			}
			w := serve(t, tt.handler, opts...)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if spy.calls != 1 {
				t.Fatalf("HandleRequest calls = %d, want 1", spy.calls)
			}
			ctx := spy.ctx
			if ctx.Method != http.MethodGet || ctx.URL != "/x" || ctx.IP != "192.0.2.1" || ctx.UserAgent != "bridge-test" {
				t.Errorf("request facts = %q %q %q %q", ctx.Method, ctx.URL, ctx.IP, ctx.UserAgent)
			}
			if ctx.RequestID == "" || ctx.TraceID == "" || ctx.SpanID == "" {
				t.Errorf("ids = %q %q %q, want all set", ctx.RequestID, ctx.TraceID, ctx.SpanID)
			}
			if ctx.Timestamp.IsZero() || ctx.Extra == nil {
				t.Error("error context not initialised")
			}
			if ctx.UserID != tt.wantUser {
				t.Errorf("UserID = %q, want %q", ctx.UserID, tt.wantUser)
			}
			if ctx.Recovered != tt.wantRecovered {
				t.Errorf("Recovered = %v, want %v", ctx.Recovered, tt.wantRecovered)
			}
			if tt.wantRecovered {
				if ctx.PanicStack == "" || ctx.StackTrace == nil || len(ctx.StackTrace.Frames) == 0 {
					t.Error("recovered panic without a stack")
				}
				var pe *router.PanicError
				if !errors.As(spy.err, &pe) {
					t.Errorf("error = %v, want a *router.PanicError", spy.err)
				}
			}
			if spy.rcWasW {
				t.Error("render context reported written before rendering")
			}
		})
	}
}

func TestInstall_CommittedReportsOnly(t *testing.T) {
	spy := newSpy()
	w := serve(t, func(c *router.Context) error {
		c.Response.WriteHeader(http.StatusAccepted)
		_, _ = c.Response.Write([]byte("partial"))
		return errors.New("late failure")
	}, WithHandler(func() contract.ErrorHandler { return spy }))

	if w.Code != http.StatusAccepted || w.Body.String() != "partial" {
		t.Fatalf("response = %d %q, want the handler's own 202 partial", w.Code, w.Body.String())
	}
	if spy.calls != 1 || len(spy.ReportedErrors()) != 1 {
		t.Fatalf("calls = %d, reports = %d; want 1 and 1", spy.calls, len(spy.ReportedErrors()))
	}
	if !spy.rcWasW {
		t.Error("render context for a committed response must report written")
	}
}

func TestInstall_FallsBackToRouterDefault(t *testing.T) {
	tests := []struct {
		name string
		opts []Option
	}{
		{name: "no resolver"},
		{name: "resolver returns nil", opts: []Option{WithHandler(func() contract.ErrorHandler { return nil })}},
		{name: "nil option ignored", opts: []Option{nil}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var lines []string
			var kvs []any
			opts := append([]Option{WithLogger(func(msg string, kv ...any) {
				lines = append(lines, msg)
				kvs = kv
			})}, tt.opts...)
			w := serve(t, func(*router.Context) error { return problem.NotFound("gone missing") }, opts...)
			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", w.Code)
			}
			if !strings.Contains(w.Body.String(), "gone missing") {
				t.Errorf("body = %q, want the router default rendering", w.Body.String())
			}
			if len(lines) != 1 {
				t.Fatalf("logged %d lines, want 1 (%v)", len(lines), lines)
			}
			if got := fmt.Sprint(kvs...); !strings.Contains(got, "gone missing") || !strings.Contains(got, "/x") {
				t.Errorf("log kvs = %v, want the error and the path", kvs)
			}
		})
	}
}

func TestInstall_LoggerOnlyWithoutHandler(t *testing.T) {
	logged := 0
	w := serve(t, func(*router.Context) error { return errors.New("boom") },
		WithHandler(func() contract.ErrorHandler { return newSpy() }),
		WithLogger(func(string, ...any) { logged++ }),
	)
	if logged != 0 {
		t.Errorf("logged %d lines with a handler, want 0", logged)
	}
	if w.Code == 0 {
		t.Error("handler path wrote nothing")
	}

	panicky := serve(t, func(*router.Context) error { return problem.NotFound() },
		WithLogger(func(string, ...any) { panic("logger down") }),
	)
	if panicky.Code != http.StatusNotFound {
		t.Errorf("status with a panicking logger = %d, want 404", panicky.Code)
	}
}

func TestInstall_ResolvedPerRequest(t *testing.T) {
	first, second := newSpy(), newSpy()
	current := first
	r := router.New()
	Install(r, WithHandler(func() contract.ErrorHandler { return current }))
	r.Get("/x", func(*router.Context) error { return errors.New("boom") })

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
	current = second
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	if first.calls != 1 || second.calls != 1 {
		t.Errorf("calls = %d and %d, want 1 each", first.calls, second.calls)
	}
}

func TestInstall_NilRouter(t *testing.T) {
	Install(nil, WithHandler(func() contract.ErrorHandler { return newSpy() }))
}

func TestInstall_ThroughProblemHandler(t *testing.T) {
	rec := &recordingReporter{}
	h := problem.NewHandler(problem.WithReporters(rec))
	r := router.New()
	Install(r, WithHandler(func() contract.ErrorHandler { return h }))
	r.Get("/boom", func(*router.Context) error { return errors.New("db down") })
	r.Get("/panic", func(*router.Context) error { panic("exploded") })
	r.Get("/missing", func(*router.Context) error { return problem.NotFound() })

	for _, tt := range []struct {
		path       string
		wantStatus int
	}{
		{path: "/boom", wantStatus: http.StatusInternalServerError},
		{path: "/panic", wantStatus: http.StatusInternalServerError},
		{path: "/missing", wantStatus: http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tt.path, nil))
		if w.Code != tt.wantStatus {
			t.Errorf("%s: status = %d, want %d", tt.path, w.Code, tt.wantStatus)
		}
	}
	if rec.count() != 2 {
		t.Errorf("reports = %d, want 2 (the 500 and the panic)", rec.count())
	}
}

// recordingReporter counts reports.
type recordingReporter struct {
	mu sync.Mutex
	n  int
}

func (r *recordingReporter) Report(error, *contract.ErrorContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
}

func (r *recordingReporter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

func TestHandle(t *testing.T) {
	tests := []struct {
		name       string
		ctx        func() *router.Context
		err        error
		info       router.ErrorInfo
		wantCalls  int
		wantStatus int
	}{
		{
			name:      "nil context",
			ctx:       func() *router.Context { return nil },
			err:       errors.New("boom"),
			wantCalls: 0,
		},
		{
			name: "nil error",
			ctx: func() *router.Context {
				c, _ := router.NewTestContext(http.MethodGet, "/x")
				return c
			},
			wantCalls: 0,
		},
		{
			name: "no request",
			ctx: func() *router.Context {
				return &router.Context{Response: httptest.NewRecorder()}
			},
			err:        errors.New("boom"),
			wantCalls:  1,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "info carries the ids and stack",
			ctx: func() *router.Context {
				c, _ := router.NewTestContext(http.MethodPost, "/y")
				return c
			},
			err:        errors.New("boom"),
			info:       router.ErrorInfo{RequestID: "req-1", TraceID: "trace-1", SpanID: "span-1", Recovered: true, Stack: "goroutine 1", StackTrace: contract.CaptureStackTrace(0)},
			wantCalls:  1,
			wantStatus: http.StatusInternalServerError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spy := newSpy()
			c := tt.ctx()
			Handle(c, tt.err, tt.info, spy)
			if spy.calls != tt.wantCalls {
				t.Fatalf("calls = %d, want %d", spy.calls, tt.wantCalls)
			}
			if tt.wantCalls == 0 {
				return
			}
			if rec, ok := c.Response.(*httptest.ResponseRecorder); ok && rec.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			ctx := spy.ctx
			if ctx.RequestID != tt.info.RequestID || ctx.TraceID != tt.info.TraceID || ctx.SpanID != tt.info.SpanID {
				t.Errorf("ids = %q %q %q", ctx.RequestID, ctx.TraceID, ctx.SpanID)
			}
			if ctx.Recovered != tt.info.Recovered || ctx.PanicStack != tt.info.Stack || ctx.StackTrace != tt.info.StackTrace {
				t.Errorf("panic facts not carried: %+v", ctx)
			}
			if ctx.UserID != "" {
				t.Errorf("UserID = %q, want empty from Handle", ctx.UserID)
			}
		})
	}
}

func TestHandle_NilHandlerUsesRouterDefault(t *testing.T) {
	c, w := router.NewTestContext(http.MethodGet, "/x")
	Handle(c, problem.Forbidden("not yours"), router.ErrorInfo{}, nil)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "not yours") {
		t.Errorf("response = %d %q, want the router default 403", w.Code, w.Body.String())
	}
}

func TestCommittedRenderContext(t *testing.T) {
	c, w := router.NewTestContext(http.MethodGet, "/x")
	rc := committedRenderContext{RenderContext: c.RenderContext()}

	if !rc.Written() {
		t.Error("Written = false, want true")
	}
	rc.SetHeader("X-Late", "1")
	rc.WriteHeader(http.StatusTeapot)
	n, err := rc.Write([]byte("late body"))
	if n != 0 || !errors.Is(err, contract.ErrResponseWritten) {
		t.Errorf("Write = %d, %v; want 0 and ErrResponseWritten", n, err)
	}
	if err := rc.Redirect(http.StatusFound, "/elsewhere"); !errors.Is(err, contract.ErrInvalidRedirect) {
		t.Errorf("Redirect error = %v, want ErrInvalidRedirect", err)
	}
	if w.Code != http.StatusOK || w.Body.Len() != 0 || w.Header().Get("X-Late") != "" || w.Header().Get("Location") != "" {
		t.Errorf("committed render context wrote: %d %q %v", w.Code, w.Body.String(), w.Header())
	}
	if rc.Request() != c.Request || rc.Writer() != c.Response {
		t.Error("request or writer not passed through")
	}
}

// TestInstall_PanicCarryingMarkerIsAReported500 asserts a panic whose
// value is the response-written sentinel, a contract.Handled value or a
// report-once marked error is a reported 500 through the pipeline,
// directly and under Timeout: a marker the panic value carries counts for
// nothing.
func TestInstall_PanicCarryingMarkerIsAReported500(t *testing.T) {
	values := []struct {
		name  string
		value error
	}{
		{name: "sentinel", value: contract.ErrResponseWritten},
		{name: "handled", value: contract.Handled(errors.New("rendered elsewhere"))},
		{name: "reported", value: contract.MarkReported(errors.New("reported elsewhere"))},
	}
	for _, v := range values {
		for _, timeout := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s timeout=%v", v.name, timeout), func(t *testing.T) {
				rec := &recordingReporter{}
				h := problem.NewHandler(problem.WithReporters(rec))
				r := router.New()
				Install(r, WithHandler(func() contract.ErrorHandler { return h }))
				if timeout {
					r.Use(router.Timeout(time.Minute))
				}
				r.Get("/boom", func(*router.Context) error { panic(v.value) })

				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/boom", nil)
				req.Header.Set("Accept", "application/json")
				r.ServeHTTP(w, req)

				if w.Code != http.StatusInternalServerError {
					t.Errorf("status = %d, want 500 (body %q)", w.Code, w.Body.String())
				}
				if rec.count() != 1 {
					t.Errorf("reports = %d, want 1", rec.count())
				}
			})
		}
	}
}

// TestInstall_MiddlewareReportsAMarkedPanicOnce asserts the report-once
// flow for a recovered panic whose value was marked: the handler under
// Timeout panics with a marked error, an outer middleware reports the
// forwarded panic through c.Errors().Report and returns it marked, and
// the boundary reports nothing more.
func TestInstall_MiddlewareReportsAMarkedPanicOnce(t *testing.T) {
	rec := &recordingReporter{}
	h := problem.NewHandler(problem.WithReporters(rec))
	r := router.New()
	r.SetServices(&app.Services{Errors: h})
	Install(r, WithHandler(func() contract.ErrorHandler { return h }))
	r.Use(func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			err := next(c)
			var pe *router.PanicError
			if errors.As(err, &pe) {
				c.Errors().Report(err, problem.NewErrorContext())
				return contract.MarkReported(err)
			}
			return err
		}
	})
	r.Use(router.Timeout(time.Minute))
	r.Get("/boom", func(*router.Context) error {
		panic(contract.MarkReported(errors.New("reported inside the handler")))
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	req.Header.Set("Accept", "application/json")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500 (body %q)", w.Code, w.Body.String())
	}
	if rec.count() != 1 {
		t.Errorf("reports = %d, want exactly 1", rec.count())
	}
}

// timeoutHandoffKey is the context key inner middleware sets in the
// Timeout handoff tests.
type timeoutHandoffKey struct{}

// handoffErr is the error the Timeout CSRF handoff test renders itself.
type handoffErr struct{}

func (handoffErr) Error() string { return "handoff" }

// TestInstall_TimeoutHandsTheInnerRequestToThePipeline asserts a JSONWhen
// predicate sees a context value middleware inside Timeout added when the
// handler returns an error in time.
func TestInstall_TimeoutHandsTheInnerRequestToThePipeline(t *testing.T) {
	h := problem.NewHandler(problem.WithReporters())
	h.SetDebug(false)
	h.JSONWhen(func(r *http.Request, _ error) bool {
		return r.Context().Value(timeoutHandoffKey{}) == "api"
	})
	r := router.New()
	Install(r, WithHandler(func() contract.ErrorHandler { return h }))
	r.Use(router.Timeout(time.Minute))
	r.Use(func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), timeoutHandoffKey{}, "api"))
			return next(c)
		}
	})
	r.Get("/x", func(*router.Context) error { return errors.New("db down") })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.Header.Set("Accept", "text/html")
	r.ServeHTTP(w, req)

	if w.Code != http.StatusInternalServerError || !strings.HasPrefix(w.Header().Get("Content-Type"), problem.ProblemTypeContent) {
		t.Errorf("response = %d %q, want 500 %s (body %q)", w.Code, w.Header().Get("Content-Type"), problem.ProblemTypeContent, w.Body.String())
	}
}

// TestInstall_TimeoutKeepsCSRFTokenStateForTheErrorPage asserts a render
// rule reading csrf.TokenForRequest gets a token when CSRFMiddleware runs
// inside Timeout and the handler returns an error in time.
func TestInstall_TimeoutKeepsCSRFTokenStateForTheErrorPage(t *testing.T) {
	cfg := csrf.DefaultConfig()
	cfg.Store = stores.NewSessionStore()
	cfg.SessionIDResolver = func(r *http.Request) (string, error) {
		ck, err := r.Cookie("session_id")
		if err != nil || ck.Value == "" {
			return "", csrf.ErrNoSession
		}
		return ck.Value, nil
	}
	c, err := csrf.NewE(cfg)
	if err != nil {
		t.Fatalf("csrf.NewE: %v", err)
	}
	if err := c.RotateToken("", "s1"); err != nil {
		t.Fatalf("RotateToken: %v", err)
	}

	var tokenErr error
	h := problem.NewHandler(problem.WithReporters())
	problem.RenderFor(h, func(rc problem.RenderContext, _ handoffErr, _ *problem.ErrorContext) bool {
		token, err := csrf.TokenForRequest(rc.Request())
		tokenErr = err
		rc.WriteHeader(http.StatusTeapot)
		_, _ = rc.Write([]byte(token))
		return true
	})
	r := router.New()
	Install(r, WithHandler(func() contract.ErrorHandler { return h }))
	r.Use(router.Timeout(time.Minute))
	r.Use(router.CSRFMiddleware(c))
	r.Get("/x", func(*router.Context) error { return handoffErr{} })

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: "s1"})
	r.ServeHTTP(w, req)

	if w.Code != http.StatusTeapot {
		t.Fatalf("status = %d, want 418 from the render rule (body %q)", w.Code, w.Body.String())
	}
	if tokenErr != nil || w.Body.Len() == 0 {
		t.Errorf("csrf.TokenForRequest = %q, %v; want a token", w.Body.String(), tokenErr)
	}
}

// TestInstall_ErrorResponseDropsStaleContentLength asserts a Content-Length
// a handler staged before returning an error never reaches the pipeline's
// response through a real server.
func TestInstall_ErrorResponseDropsStaleContentLength(t *testing.T) {
	h := problem.NewHandler(problem.WithReporters())
	h.SetDebug(false)
	r := router.New()
	Install(r, WithHandler(func() contract.ErrorHandler { return h }))
	r.Get("/boom", func(c *router.Context) error {
		c.Response.Header().Set("Content-Length", "1")
		return errors.New("db down")
	})
	srv := httptest.NewServer(r)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/boom", nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	var doc map[string]any
	if resp.StatusCode != http.StatusInternalServerError || json.Unmarshal(raw, &doc) != nil || doc["status"] != float64(http.StatusInternalServerError) {
		t.Errorf("response = %d %q, want the full 500 problem body", resp.StatusCode, raw)
	}
}

// TestInstall_StatusResolutionMatchesStandalone asserts the standalone
// router default and the installed pipeline answer the same status and
// headers when an explicit status wraps a deadline or an oversized body,
// for the bare fallbacks, and for a panic carrying a 4xx value.
func TestInstall_StatusResolutionMatchesStandalone(t *testing.T) {
	tests := []struct {
		name       string
		handler    router.HandlerFunc
		wantStatus int
		wantRetry  string
	}{
		{
			name: "500 wrapping MaxBytesError",
			handler: func(*router.Context) error {
				return contract.NewHTTPError(http.StatusInternalServerError).WithCause(&http.MaxBytesError{Limit: 1024})
			},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "503 with Retry-After wrapping DeadlineExceeded",
			handler: func(*router.Context) error {
				return contract.NewHTTPError(http.StatusServiceUnavailable).WithHeader("Retry-After", "7").WithCause(context.DeadlineExceeded)
			},
			wantStatus: http.StatusServiceUnavailable,
			wantRetry:  "7",
		},
		{
			name:       "bare MaxBytesError",
			handler:    func(*router.Context) error { return &http.MaxBytesError{Limit: 1024} },
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name:       "bare DeadlineExceeded",
			handler:    func(*router.Context) error { return context.DeadlineExceeded },
			wantStatus: http.StatusServiceUnavailable,
		},
		{
			name:       "panic carrying a 404 HTTPError",
			handler:    func(*router.Context) error { panic(contract.NewHTTPError(http.StatusNotFound)) },
			wantStatus: http.StatusInternalServerError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			standalone := router.New()
			standalone.Get("/x", tt.handler)

			h := problem.NewHandler(problem.WithReporters())
			h.SetDebug(false)
			installed := router.New()
			Install(installed, WithHandler(func() contract.ErrorHandler { return h }))
			installed.Get("/x", tt.handler)

			for name, r := range map[string]*router.VelocityRouterV2{"standalone": standalone, "pipeline": installed} {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/x", nil)
				req.Header.Set("Accept", "application/json")
				r.ServeHTTP(w, req)
				if w.Code != tt.wantStatus {
					t.Errorf("%s: status = %d, want %d", name, w.Code, tt.wantStatus)
				}
				if got := w.Header().Get("Retry-After"); got != tt.wantRetry {
					t.Errorf("%s: Retry-After = %q, want %q", name, got, tt.wantRetry)
				}
			}
		})
	}
}

// TestInstall_ProblemBodyMatchesStandalone asserts the standalone router
// default and the installed pipeline write the same problem body members
// for a JSON request: the validation errors map, the 419 title, a 4xx
// message as detail and a title-only 5xx detail.
func TestInstall_ProblemBodyMatchesStandalone(t *testing.T) {
	result, err := validation.CheckData(map[string]interface{}{"email": "bad"}, validation.Rules{
		"email": {validation.Email()},
	})
	if err != nil {
		t.Fatalf("CheckData: %v", err)
	}
	failure := validation.NewFailure(result)
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantTitle  string
		wantDetail string
		wantErrors bool
	}{
		{name: "validation failure", err: failure, wantStatus: http.StatusUnprocessableEntity, wantTitle: "Unprocessable Entity", wantDetail: "Unprocessable Entity", wantErrors: true},
		{name: "csrf mismatch", err: &csrf.TokenMismatchError{Reason: csrf.ErrTokenInvalid}, wantStatus: contract.StatusTokenMismatch, wantTitle: "Page Expired", wantDetail: "Page Expired"},
		{name: "4xx message", err: contract.NewHTTPError(http.StatusConflict, "Version conflict"), wantStatus: http.StatusConflict, wantTitle: "Conflict", wantDetail: "Version conflict"},
		{name: "5xx title only", err: contract.NewHTTPError(http.StatusServiceUnavailable, "db down"), wantStatus: http.StatusServiceUnavailable, wantTitle: "Service Unavailable", wantDetail: "Service Unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			handler := func(*router.Context) error { return tt.err }
			standalone := router.New()
			standalone.Get("/x", handler)

			h := problem.NewHandler(problem.WithReporters())
			h.SetDebug(false)
			installed := router.New()
			Install(installed, WithHandler(func() contract.ErrorHandler { return h }))
			installed.Get("/x", handler)

			bodies := map[string]map[string]any{}
			for name, r := range map[string]*router.VelocityRouterV2{"standalone": standalone, "pipeline": installed} {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/x", nil)
				req.Header.Set("Accept", "application/json")
				r.ServeHTTP(w, req)
				if w.Code != tt.wantStatus {
					t.Errorf("%s: status = %d, want %d", name, w.Code, tt.wantStatus)
				}
				var body map[string]any
				if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
					t.Fatalf("%s: body is not JSON: %v (%q)", name, err, w.Body.String())
				}
				if body["title"] != tt.wantTitle || body["detail"] != tt.wantDetail {
					t.Errorf("%s: title %v detail %v, want %q %q", name, body["title"], body["detail"], tt.wantTitle, tt.wantDetail)
				}
				if _, ok := body["errors"]; ok != tt.wantErrors {
					t.Errorf("%s: errors member present = %v, want %v", name, ok, tt.wantErrors)
				}
				bodies[name] = body
			}
			for _, member := range []string{"type", "title", "status", "detail", "instance", "errors"} {
				if s, p := fmt.Sprint(bodies["standalone"][member]), fmt.Sprint(bodies["pipeline"][member]); s != p {
					t.Errorf("%s differs: standalone %s, pipeline %s", member, s, p)
				}
			}
		})
	}
}

// TestInstall_DeepChainMarkers asserts the pipeline finds a marker by its
// position at any depth: a *router.PanicError carrying the response-written
// sentinel under more wrappers than a depth-limited walk visits is a
// reported 500, a plain sentinel that deep still ends the request, and a
// marker past the marker walk's cap is not found, so the error is
// reported and rendered.
func TestInstall_DeepChainMarkers(t *testing.T) {
	wrap := func(err error, n int) error {
		for i := 0; i < n; i++ {
			err = fmt.Errorf("layer %d: %w", i, err)
		}
		return err
	}
	tests := []struct {
		name        string
		err         error
		wantStatus  int // 0: nothing written
		wantReports int
	}{
		{name: "PanicCarryingSentinelPastWalkLimit", err: wrap(&router.PanicError{Err: contract.ErrResponseWritten, Stack: "stack"}, 65), wantStatus: http.StatusInternalServerError, wantReports: 1},
		{name: "PanicCarryingReportedPastWalkLimit", err: wrap(&router.PanicError{Err: contract.MarkReported(errors.New("x")), Stack: "stack"}, 65), wantStatus: http.StatusInternalServerError, wantReports: 1},
		{name: "SentinelPastWalkLimit", err: wrap(contract.ErrResponseWritten, 65)},
		{name: "SentinelPastMarkerCap", err: wrap(contract.ErrResponseWritten, 1025), wantStatus: http.StatusInternalServerError, wantReports: 1},
		{name: "ReportedPastMarkerCap", err: wrap(contract.MarkReported(errors.New("x")), 1025), wantStatus: http.StatusInternalServerError, wantReports: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &recordingReporter{}
			h := problem.NewHandler(problem.WithReporters(rec))
			r := router.New()
			Install(r, WithHandler(func() contract.ErrorHandler { return h }))
			r.Get("/x", func(*router.Context) error { return tt.err })

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header.Set("Accept", "application/json")
			r.ServeHTTP(w, req)

			if tt.wantStatus == 0 {
				if w.Body.Len() != 0 || w.Header().Get("Content-Type") != "" {
					t.Errorf("wrote %d %q, want nothing", w.Code, w.Body.String())
				}
			} else if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
			if rec.count() != tt.wantReports {
				t.Errorf("reports = %d, want %d", rec.count(), tt.wantReports)
			}
		})
	}
}

// consumerRecovered stands for the recovered-panic error of a consumer's
// own recovery middleware: it implements contract.RecoveredPanic and
// unwraps to the error it carries, and is no framework panic type.
type consumerRecovered struct{ err error }

func (e *consumerRecovered) Error() string  { return "recovered: " + e.err.Error() }
func (e *consumerRecovered) Recovered() any { return e.err }
func (e *consumerRecovered) Unwrap() error  { return e.err }

// ctxReporter records whether each report was flagged recovered.
type ctxReporter struct {
	mu        sync.Mutex
	recovered []bool
}

func (r *ctxReporter) Report(_ error, ctx *contract.ErrorContext) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recovered = append(r.recovered, ctx != nil && ctx.Recovered)
}

func (r *ctxReporter) all() []bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]bool(nil), r.recovered...)
}

// TestInstall_ConsumerRecoveredPanic asserts the installed pipeline treats
// any contract.RecoveredPanic a handler returns as a recovered panic: one
// report flagged recovered and a 500 that does not echo the value's
// message, for JSON and browser clients, whatever the value would answer
// or be dropped as on its own.
func TestInstall_ConsumerRecoveredPanic(t *testing.T) {
	dead, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name   string
		value  error
		reqCx  context.Context
		accept string
	}{
		{name: "ClientErrorJSON", value: problem.NotFound("payload"), accept: "application/json"},
		{name: "ClientErrorHTML", value: problem.NotFound("payload"), accept: "text/html"},
		{name: "CanceledLiveRequest", value: context.Canceled, accept: "application/json"},
		{name: "CanceledDeadRequest", value: context.Canceled, reqCx: dead, accept: "application/json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rep := &ctxReporter{}
			h := problem.NewHandler(problem.WithReporters(rep))
			r := router.New()
			Install(r, WithHandler(func() contract.ErrorHandler { return h }))
			r.Get("/x", func(*router.Context) error { return &consumerRecovered{err: tt.value} })

			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			if tt.reqCx != nil {
				req = req.WithContext(tt.reqCx)
			}
			req.Header.Set("Accept", tt.accept)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", w.Code)
			}
			if strings.Contains(w.Body.String(), "payload") {
				t.Errorf("body leaks the panic value's message: %q", w.Body.String())
			}
			if got := rep.all(); len(got) != 1 || !got[0] {
				t.Errorf("reports (recovered flags) = %v, want one flagged recovered", got)
			}
		})
	}
}
