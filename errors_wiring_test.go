package velocity

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
	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

// levelLogger is a log.Logger that records every entry with its level.
type levelLogger struct {
	mu      sync.Mutex
	entries []levelEntry
}

type levelEntry struct {
	level string
	msg   string
	kvs   []any
}

func (l *levelLogger) Debug(msg string, kvs ...any) { l.add("debug", msg, kvs) }
func (l *levelLogger) Info(msg string, kvs ...any)  { l.add("info", msg, kvs) }
func (l *levelLogger) Warn(msg string, kvs ...any)  { l.add("warn", msg, kvs) }
func (l *levelLogger) Error(msg string, kvs ...any) { l.add("error", msg, kvs) }
func (l *levelLogger) Fatal(msg string, kvs ...any) { l.add("fatal", msg, kvs) }

func (l *levelLogger) add(level, msg string, kvs []any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = append(l.entries, levelEntry{level: level, msg: msg, kvs: kvs})
}

func (l *levelLogger) count(level string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.entries {
		if e.level == level {
			n++
		}
	}
	return n
}

func (l *levelLogger) reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.entries = nil
}

// newPipelineApp builds a test app whose logger records every entry and
// whose error handler carries a recording reporter next to the default
// LogReporter. Boot-time log noise is cleared before it returns.
func newPipelineApp(t *testing.T, opts ...Option) (*App, *levelLogger, *recordingReporter) {
	t.Helper()
	capture := &levelLogger{}
	const driverName = "pipeline-capture"
	prev := log.Drivers().Override(driverName, func(_ context.Context, _ log.LogConfig) (log.Logger, error) {
		return capture, nil
	})
	t.Cleanup(func() { log.Drivers().Override(driverName, prev) })

	a, err := New(append([]Option{WithConfig(Config{
		Env:   "testing",
		Debug: true,
		Port:  "0",
		Cache: CacheConfig{Driver: "memory", Prefix: "test_cache"},
		Log:   log.LogConfig{Driver: driverName, Config: make(map[string]any)},
		Queue: QueueConfig{Driver: "memory"},
		Mail:  mail.MailConfig{Driver: "log"},
	})}, opts...)...)
	if err != nil {
		t.Fatalf("New() error: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	rec := &recordingReporter{}
	a.Services.Errors.AddReporter(rec)
	capture.reset()
	return a, capture, rec
}

// writeTracker records whether anything reached the response writer.
type writeTracker struct {
	*httptest.ResponseRecorder
	wrote bool
}

func (w *writeTracker) WriteHeader(code int) {
	w.wrote = true
	w.ResponseRecorder.WriteHeader(code)
}

func (w *writeTracker) Write(p []byte) (int, error) {
	w.wrote = true
	return w.ResponseRecorder.Write(p)
}

// bindTarget is the value the Bind cases decode into.
type bindTarget struct {
	Name string `json:"name"`
}

func bindHandler(c *router.Context) error {
	var v bindTarget
	return c.Bind(&v)
}

// TestErrorPipeline_DefaultMappings drives each default mapping through a
// real app: the router boundary, the bridge and the error handler built
// by New.
func TestErrorPipeline_DefaultMappings(t *testing.T) {
	// release unblocks the handler the Timeout case leaves running once
	// every case has finished.
	release := make(chan struct{})
	defer close(release)

	tests := []struct {
		name       string
		method     string
		body       string
		accept     string
		middleware []router.MiddlewareFunc
		handler    router.HandlerFunc
		configure  func(t *testing.T, a *App)
		// deadClient cancels the request context before serving.
		deadClient bool

		wantStatus      int // 0: nothing written
		wantHeader      map[string]string
		wantBody        string
		wantReports     int
		wantErrorLogs   int
		wantWarnLogs    int
		wantRecovered   bool
		wantContentType string
	}{
		{
			name:       "problem not found",
			handler:    func(*router.Context) error { return problem.NotFound() },
			wantStatus: http.StatusNotFound,
		},
		{
			name:            "problem not found as json",
			accept:          "application/json",
			handler:         func(*router.Context) error { return problem.NotFound() },
			wantStatus:      http.StatusNotFound,
			wantContentType: "application/problem+json",
		},
		{
			name:       "too many requests carries retry-after",
			handler:    func(*router.Context) error { return problem.TooManyRequests(30 * time.Second) },
			wantStatus: http.StatusTooManyRequests,
			wantHeader: map[string]string{"Retry-After": "30"},
		},
		{
			name:       "wrapped orm not found",
			handler:    func(*router.Context) error { return fmt.Errorf("load user: %w", orm.ErrNotFound) },
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "wrapped orm no rows",
			handler:    func(*router.Context) error { return fmt.Errorf("load user: %w", orm.ErrNoRows) },
			wantStatus: http.StatusNotFound,
		},
		{
			name:       "wrapped auth unauthorized",
			handler:    func(*router.Context) error { return fmt.Errorf("edit post: %w", auth.ErrUnauthorized) },
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "csrf token missing",
			handler:    func(*router.Context) error { return csrf.ErrTokenMissing },
			wantStatus: contract.StatusTokenMismatch,
		},
		{
			name:       "bind extra data",
			method:     http.MethodPost,
			body:       `{"name":"a"}{"name":"b"}`,
			handler:    bindHandler,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "oversized bind",
			method:     http.MethodPost,
			body:       `{"name":"far longer than the eight byte limit"}`,
			middleware: []router.MiddlewareFunc{router.BodyLimit(8)},
			handler:    bindHandler,
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name:          "live context canceled",
			handler:       func(*router.Context) error { return context.Canceled },
			wantStatus:    http.StatusInternalServerError,
			wantReports:   1,
			wantErrorLogs: 1,
		},
		{
			name:       "dead context canceled writes nothing",
			deadClient: true,
			handler:    func(*router.Context) error { return context.Canceled },
			wantStatus: 0,
		},
		{
			name:       "timeout with a dead client writes and reports nothing",
			deadClient: true,
			middleware: []router.MiddlewareFunc{router.Timeout(5 * time.Second)},
			handler: func(*router.Context) error {
				<-release
				return nil
			},
			wantStatus: 0,
		},
		{
			name:         "deadline exceeded",
			handler:      func(*router.Context) error { return fmt.Errorf("query: %w", context.DeadlineExceeded) },
			wantStatus:   http.StatusServiceUnavailable,
			wantReports:  1,
			wantWarnLogs: 1,
		},
		{
			name:          "panic with string",
			handler:       func(*router.Context) error { panic("handler exploded") },
			wantStatus:    http.StatusInternalServerError,
			wantReports:   1,
			wantErrorLogs: 1,
			wantRecovered: true,
		},
		{
			name:          "panic with error",
			handler:       func(*router.Context) error { panic(errors.New("handler exploded")) },
			wantStatus:    http.StatusInternalServerError,
			wantReports:   1,
			wantErrorLogs: 1,
			wantRecovered: true,
		},
		{
			name:          "panic with http error still 500",
			handler:       func(*router.Context) error { panic(problem.NotFound()) },
			wantStatus:    http.StatusInternalServerError,
			wantReports:   1,
			wantErrorLogs: 1,
			wantRecovered: true,
		},
		{
			name:          "plain error",
			handler:       func(*router.Context) error { return errors.New("db down") },
			wantStatus:    http.StatusInternalServerError,
			wantReports:   1,
			wantErrorLogs: 1,
		},
		{
			name:          "reported in handler then returned",
			handler:       func(c *router.Context) error { return c.Report(errors.New("db down")) },
			wantStatus:    http.StatusInternalServerError,
			wantReports:   1,
			wantErrorLogs: 1,
		},
		{
			name: "handled by middleware reports once",
			middleware: []router.MiddlewareFunc{func(next router.HandlerFunc) router.HandlerFunc {
				return func(c *router.Context) error {
					c.Response.WriteHeader(http.StatusBadGateway)
					return contract.Handled(errors.New("upstream failed"))
				}
			}},
			handler:       func(*router.Context) error { return nil },
			wantStatus:    http.StatusBadGateway,
			wantReports:   1,
			wantErrorLogs: 1,
		},
		{
			name: "committed response reports without rendering",
			handler: func(c *router.Context) error {
				c.Response.WriteHeader(http.StatusOK)
				_, _ = c.Response.Write([]byte("partial"))
				return errors.New("stream broke")
			},
			wantStatus:    http.StatusOK,
			wantBody:      "partial",
			wantReports:   1,
			wantErrorLogs: 1,
		},
		{
			name:       "custom render rule overrides a framework default",
			method:     http.MethodPost,
			body:       `{"name":"far longer than the eight byte limit"}`,
			middleware: []router.MiddlewareFunc{router.BodyLimit(8)},
			handler:    bindHandler,
			configure: func(t *testing.T, a *App) {
				a.Errors(func(h contract.ErrorHandler) {
					problem.RenderFor(h, func(rc problem.RenderContext, _ *http.MaxBytesError, _ *problem.ErrorContext) bool {
						rc.WriteHeader(http.StatusTeapot)
						_, _ = rc.Write([]byte("too big for the pot"))
						return true
					})
				})
				if err := a.Bootstrap(); err != nil {
					t.Fatalf("Bootstrap: %v", err)
				}
			},
			wantStatus: http.StatusTeapot,
			wantBody:   "too big for the pot",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, logs, rec := newPipelineApp(t)
			if tt.configure != nil {
				tt.configure(t, a)
			}
			method := tt.method
			if method == "" {
				method = http.MethodGet
			}
			route := a.Router.Get
			if method == http.MethodPost {
				route = a.Router.Post
			}
			route("/t", tt.handler).Use(tt.middleware...)

			req := httptest.NewRequest(method, "/t", strings.NewReader(tt.body))
			if tt.body != "" {
				req.Header.Set("Content-Type", "application/json")
			}
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}
			if tt.deadClient {
				ctx, cancel := context.WithCancel(req.Context())
				cancel()
				req = req.WithContext(ctx)
			}
			w := &writeTracker{ResponseRecorder: httptest.NewRecorder()}
			a.Router.ServeHTTP(w, req)

			if tt.wantStatus == 0 {
				if w.wrote {
					t.Fatalf("response written (status %d, body %q), want nothing", w.Code, w.Body.String())
				}
			} else if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
			for k, v := range tt.wantHeader {
				if got := w.Header().Get(k); got != v {
					t.Errorf("header %s = %q, want %q", k, got, v)
				}
			}
			if tt.wantBody != "" && w.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", w.Body.String(), tt.wantBody)
			}
			if tt.wantContentType != "" {
				if got := w.Header().Get("Content-Type"); !strings.HasPrefix(got, tt.wantContentType) {
					t.Errorf("Content-Type = %q, want %q", got, tt.wantContentType)
				}
			}
			if got := rec.count(); got != tt.wantReports {
				t.Errorf("reports = %d, want %d (%v)", got, tt.wantReports, rec.errs)
			}
			if got := logs.count("error"); got != tt.wantErrorLogs {
				t.Errorf("error log entries = %d, want %d (%+v)", got, tt.wantErrorLogs, logs.entries)
			}
			if got := logs.count("warn"); got != tt.wantWarnLogs {
				t.Errorf("warn log entries = %d, want %d (%+v)", got, tt.wantWarnLogs, logs.entries)
			}
			if tt.wantReports == 0 || rec.count() == 0 {
				return
			}
			ctx := rec.exCtx[0]
			if ctx.Method != method || ctx.URL != "/t" || ctx.IP == "" || ctx.RequestID == "" || ctx.TraceID == "" {
				t.Errorf("error context missing request facts: %+v", ctx)
			}
			if ctx.Recovered != tt.wantRecovered {
				t.Errorf("Recovered = %v, want %v", ctx.Recovered, tt.wantRecovered)
			}
			if tt.wantRecovered && (ctx.PanicStack == "" || ctx.StackTrace == nil || len(ctx.StackTrace.Frames) == 0) {
				t.Errorf("recovered panic reported without a stack: stack %q, trace %v", ctx.PanicStack, ctx.StackTrace)
			}
		})
	}
}

// TestErrorPipeline_ConsumerErrorHandlerReplacesPipeline asserts that a
// consumer SetErrorHandler after New replaces reporting, rendering and the
// default logging, and that Bootstrap does not re-install the bridge.
func TestErrorPipeline_ConsumerErrorHandlerReplacesPipeline(t *testing.T) {
	tests := []struct {
		name      string
		bootstrap bool
	}{
		{name: "after new"},
		{name: "survives bootstrap", bootstrap: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, logs, rec := newPipelineApp(t)
			var seen []error
			a.Router.SetErrorHandler(func(c *router.Context, err error, _ router.ErrorInfo) {
				seen = append(seen, err)
				c.Response.WriteHeader(http.StatusBadGateway)
			})
			if tt.bootstrap {
				if err := a.Bootstrap(); err != nil {
					t.Fatalf("Bootstrap: %v", err)
				}
			}
			a.Router.Get("/boom", func(*router.Context) error { return errors.New("consumer owns this") })
			a.Router.Get("/panic", func(*router.Context) error { panic("consumer owns this too") })

			for _, path := range []string{"/boom", "/panic"} {
				w := httptest.NewRecorder()
				a.Router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
				if w.Code != http.StatusBadGateway {
					t.Errorf("%s: status = %d, want 502 from the consumer handler", path, w.Code)
				}
			}
			if len(seen) != 2 {
				t.Errorf("consumer handler saw %d errors, want 2", len(seen))
			}
			if rec.count() != 0 {
				t.Errorf("pipeline reported %d errors, want 0 once replaced", rec.count())
			}
			if n := logs.count("error") + logs.count("warn"); n != 0 {
				t.Errorf("default logging emitted %d entries, want 0 (%+v)", n, logs.entries)
			}
		})
	}
}

// TestErrorPipeline_RemovedPipelineLogsThroughRouterDefault asserts that a
// consumer removing the pipeline with SetErrorHandler(nil) gets the router's
// default path, logged through the app logger at error and warn level.
func TestErrorPipeline_RemovedPipelineLogsThroughRouterDefault(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantError  int
		wantWarn   int
	}{
		{name: "server error", err: errors.New("db down"), wantStatus: http.StatusInternalServerError, wantError: 1},
		{name: "deadline", err: context.DeadlineExceeded, wantStatus: http.StatusServiceUnavailable, wantWarn: 1},
		{name: "client error", err: problem.NotFound(), wantStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, logs, rec := newPipelineApp(t)
			a.Router.SetErrorHandler(nil)
			a.Router.Get("/t", func(*router.Context) error { return tt.err })

			w := httptest.NewRecorder()
			a.Router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/t", nil))

			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if logs.count("error") != tt.wantError || logs.count("warn") != tt.wantWarn {
				t.Errorf("error/warn entries = %d/%d, want %d/%d (%+v)", logs.count("error"), logs.count("warn"), tt.wantError, tt.wantWarn, logs.entries)
			}
			if rec.count() != 0 {
				t.Errorf("pipeline reported %d errors, want 0 once removed", rec.count())
			}
		})
	}
}

// TestErrorPipeline_HandlerResolvedPerRequest asserts that an error handler
// swapped in after New receives request errors.
func TestErrorPipeline_HandlerResolvedPerRequest(t *testing.T) {
	a, _, rec := newPipelineApp(t)
	fake := problem.NewFakeHandler()
	a.Services.Errors = fake
	a.Router.Get("/boom", func(*router.Context) error { return errors.New("boom") })

	w := httptest.NewRecorder()
	a.Router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))

	if got := len(fake.ReportedErrors()); got != 1 {
		t.Errorf("swapped handler reported %d errors, want 1", got)
	}
	if rec.count() != 0 {
		t.Errorf("original handler reported %d errors, want 0", rec.count())
	}
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", w.Code)
	}
}

// TestErrorPipeline_NilHandlerFallsBackToRouterDefault asserts that with
// no error handler the bridge logs one error line through the app logger
// and answers through router.DefaultErrorHandler.
func TestErrorPipeline_NilHandlerFallsBackToRouterDefault(t *testing.T) {
	a, logs, _ := newPipelineApp(t)
	a.Services.Errors = nil
	a.Router.Get("/missing", func(*router.Context) error { return problem.NotFound("no such thing") })

	w := httptest.NewRecorder()
	a.Router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/missing", nil))

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
	if !strings.Contains(w.Body.String(), "no such thing") {
		t.Errorf("body = %q, want the router default rendering", w.Body.String())
	}
	if n := logs.count("error"); n != 1 {
		t.Errorf("error log entries = %d, want 1 (%+v)", n, logs.entries)
	}
}

// stubUserAuth is a contract.AuthManager with the RequestUserIdentifier
// facet.
type stubUserAuth struct{ id string }

func (stubUserAuth) Allows(*http.Request, string, ...interface{}) bool     { return true }
func (stubUserAuth) Authorize(*http.Request, string, ...interface{}) error { return nil }
func (s stubUserAuth) RequestUserID(*http.Request) string                  { return s.id }

// TestErrorPipeline_UserIDFromAuthFacet asserts that the auth manager's
// RequestUserIdentifier facet, read per request, names the user in the
// report.
func TestErrorPipeline_UserIDFromAuthFacet(t *testing.T) {
	tests := []struct {
		name string
		auth contract.AuthManager
		want string
	}{
		{name: "facet names the user", auth: stubUserAuth{id: "user-42"}, want: "user-42"},
		{name: "no auth manager", auth: nil, want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _, rec := newPipelineApp(t)
			a.Services.Auth = tt.auth
			a.Router.Get("/boom", func(*router.Context) error { return errors.New("boom") })

			a.Router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))

			if rec.count() != 1 {
				t.Fatalf("reports = %d, want 1", rec.count())
			}
			if got := rec.exCtx[0].UserID; got != tt.want {
				t.Errorf("UserID = %q, want %q", got, tt.want)
			}
		})
	}
}

// stubErrorPageView is a contract.ViewEngine with the ErrorPageRenderer
// facet.
type stubErrorPageView struct {
	mu       sync.Mutex
	statuses []int
}

func (*stubErrorPageView) Back(http.ResponseWriter, *http.Request) {}

func (v *stubErrorPageView) RenderErrorPage(rc contract.RenderContext, status int, message string) (bool, error) {
	v.mu.Lock()
	v.statuses = append(v.statuses, status)
	v.mu.Unlock()
	rc.WriteHeader(status)
	_, err := rc.Write([]byte("error page: " + message))
	return true, err
}

// stubPlainView is a contract.ViewEngine without the facet.
type stubPlainView struct{}

func (stubPlainView) Back(http.ResponseWriter, *http.Request) {}

// swapViewModule replaces the view engine during Start, the way a module
// may after New has run.
type swapViewModule struct{ view contract.ViewEngine }

func (m swapViewModule) Init(*app.Services) error { return nil }
func (m swapViewModule) Start(s *app.Services) error {
	s.View = m.view
	return nil
}
func (swapViewModule) Shutdown(context.Context) error { return nil }

// TestErrorPipeline_ErrorPageFacet asserts that the view engine's
// ErrorPageRenderer facet answers a failed Inertia request, resolved per
// request after a module replaced the view engine: a chain module during
// Bootstrap, or a WithModules module during New with no Bootstrap at all.
func TestErrorPipeline_ErrorPageFacet(t *testing.T) {
	tests := []struct {
		name        string
		view        contract.ViewEngine
		withModules bool
		wantStatus  int
		wantPage    bool
	}{
		{name: "facet renders the error page at the real status", view: &stubErrorPageView{}, wantStatus: http.StatusNotFound, wantPage: true},
		{name: "no facet reloads with 409", view: stubPlainView{}, wantStatus: http.StatusConflict},
		{name: "with modules facet renders without bootstrap", view: &stubErrorPageView{}, withModules: true, wantStatus: http.StatusNotFound, wantPage: true},
		{name: "with modules no facet reloads with 409", view: stubPlainView{}, withModules: true, wantStatus: http.StatusConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var opts []Option
			if tt.withModules {
				opts = append(opts, WithModules(swapViewModule{view: tt.view}))
			}
			a, _, _ := newPipelineApp(t, opts...)
			a.Services.Errors.SetDebug(false)
			if !tt.withModules {
				a.Modules(func(r *chain.ModuleRegistry) { r.Add(swapViewModule{view: tt.view}) })
				if err := a.Bootstrap(); err != nil {
					t.Fatalf("Bootstrap: %v", err)
				}
			}
			a.Router.Get("/page", func(*router.Context) error { return problem.NotFound() })

			req := httptest.NewRequest(http.MethodGet, "/page", nil)
			req.Header.Set("X-Inertia", "true")
			w := httptest.NewRecorder()
			a.Router.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
			page, _ := tt.view.(*stubErrorPageView)
			if tt.wantPage && (page == nil || len(page.statuses) != 1) {
				t.Errorf("error page facet not used")
			}
			if !tt.wantPage && w.Header().Get("X-Inertia-Location") != "/page" {
				t.Errorf("X-Inertia-Location = %q, want /page", w.Header().Get("X-Inertia-Location"))
			}
		})
	}
}

// TestInstallErrorPageRenderer asserts the installed adapter: the view's
// facet answers when it has one, the 409 reload otherwise, and install is
// a no-op with no handler.
func TestInstallErrorPageRenderer(t *testing.T) {
	tests := []struct {
		name     string
		view     contract.ViewEngine
		noErrors bool
		wantPage bool
	}{
		{name: "view with facet", view: &stubErrorPageView{}, wantPage: true},
		{name: "view without facet", view: stubPlainView{}},
		{name: "no view", view: nil},
		{name: "no error handler", view: &stubErrorPageView{}, noErrors: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := &App{Services: &app.Services{View: tt.view}}
			h := problem.NewHandler(problem.WithReporters())
			if !tt.noErrors {
				a.Services.Errors = h
			}
			installErrorPageRenderer(a)

			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header.Set("X-Inertia", "true")
			w := httptest.NewRecorder()
			h.HandleRequest(contract.NewRenderContext(w, req), problem.NotFound(), nil)

			gotPage := w.Code == http.StatusNotFound
			if gotPage != tt.wantPage {
				t.Errorf("error page used = %v (status %d), want %v", gotPage, w.Code, tt.wantPage)
			}
		})
	}
}

// failingCommand is a chain command that fails with err.
type failingCommand struct{ err error }

func (failingCommand) Name() string                           { return "fail" }
func (failingCommand) Description() string                    { return "always fails" }
func (c failingCommand) Handle(*app.Services, []string) error { return c.err }

// exitCodeError is an error naming its process exit code.
type exitCodeError struct{ code int }

func (e exitCodeError) Error() string { return fmt.Sprintf("exit with %d", e.code) }
func (e exitCodeError) ExitCode() int { return e.code }

// TestRunConsole_CommandErrorReportsOnceAndExitsNonZero asserts that a
// failing command goes through HandleConsole: reported once, one stderr
// line, the exit code returned instead of exiting.
func TestRunConsole_CommandErrorReportsOnceAndExitsNonZero(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantCode   int
		wantStderr string
	}{
		{name: "plain error", err: errors.New("import failed"), wantCode: 1, wantStderr: "error: import failed\n"},
		{name: "exit coder", err: exitCodeError{code: 3}, wantCode: 3, wantStderr: "error: exit with 3\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, logs, rec := newPipelineApp(t)
			a.Commands(func(r *chain.Commands) { r.Add(failingCommand{err: tt.err}) })

			var stderr strings.Builder
			code, err := a.runConsole([]string{"run", "fail"}, &stderr)
			if err != nil {
				t.Fatalf("runConsole returned %v, want the error handled", err)
			}
			if code != tt.wantCode {
				t.Errorf("exit code = %d, want %d", code, tt.wantCode)
			}
			if stderr.String() != tt.wantStderr {
				t.Errorf("stderr = %q, want %q", stderr.String(), tt.wantStderr)
			}
			if rec.count() != 1 {
				t.Errorf("reports = %d, want 1", rec.count())
			}
			if logs.count("error") != 1 {
				t.Errorf("error log entries = %d, want 1", logs.count("error"))
			}
		})
	}
}

// TestRunConsole_Success asserts that a command that succeeds exits 0 and
// reports nothing.
func TestRunConsole_Success(t *testing.T) {
	a, _, rec := newPipelineApp(t)
	var stderr strings.Builder
	code, err := a.runConsole([]string{"help"}, &stderr)
	if err != nil || code != 0 {
		t.Fatalf("runConsole(help) = %d, %v; want 0, nil", code, err)
	}
	if rec.count() != 0 || stderr.Len() != 0 {
		t.Errorf("success reported %d errors and wrote %q", rec.count(), stderr.String())
	}
}

// TestRunConsole_NoErrorHandlerReturnsError asserts that with no error
// handler the command error comes back to the caller.
func TestRunConsole_NoErrorHandlerReturnsError(t *testing.T) {
	a, _, _ := newPipelineApp(t)
	a.Services.Errors = nil
	var stderr strings.Builder
	code, err := a.runConsole([]string{"nonexistent"}, &stderr)
	if err == nil || code != 1 {
		t.Fatalf("runConsole = %d, %v; want 1 and the command error", code, err)
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want empty", stderr.String())
	}
}

// TestInstallFrameworkErrorRules asserts each sentinel renders at its
// status, keeps the original as the cause, and is not reported, while a
// user rule for the same sentinel still wins.
func TestInstallFrameworkErrorRules(t *testing.T) {
	for _, s := range sentinelStatuses {
		t.Run(s.err.Error(), func(t *testing.T) {
			rec := &recordingReporter{}
			h := problem.NewHandler(problem.WithReporters(rec))
			installFrameworkErrorRules(h)

			err := fmt.Errorf("wrapped: %w", s.err)
			if h.ShouldReport(err) {
				t.Errorf("ShouldReport = true, want the sentinel ignored")
			}
			w := httptest.NewRecorder()
			h.HandleRequest(contract.NewRenderContext(w, httptest.NewRequest(http.MethodGet, "/x", nil)), err, nil)
			if w.Code != s.status {
				t.Errorf("status = %d, want %d", w.Code, s.status)
			}
			if rec.count() != 0 {
				t.Errorf("reports = %d, want 0", rec.count())
			}

			problem.RenderStatus[*contract.HTTPError](h, http.StatusTeapot)
			w = httptest.NewRecorder()
			h.HandleRequest(contract.NewRenderContext(w, httptest.NewRequest(http.MethodGet, "/x", nil)), err, nil)
			if w.Code != http.StatusTeapot {
				t.Errorf("status with user rule = %d, want 418", w.Code)
			}
		})
	}
}

// TestErrorPipeline_StatusBearingErrorKeepsItself asserts the framework
// prepare table never overrides an error that already names a status: an
// application HTTPError or a user map result whose cause holds a framework
// sentinel renders with its own status, message and headers.
func TestErrorPipeline_StatusBearingErrorKeepsItself(t *testing.T) {
	tests := []struct {
		name       string
		handler    router.HandlerFunc
		configure  func(h contract.ErrorHandler)
		wantStatus int
		wantDetail string
		wantHeader map[string]string
	}{
		{
			name: "http error wrapping orm not found",
			handler: func(*router.Context) error {
				return contract.NewHTTPError(http.StatusGone, "Post was removed").WithCause(orm.ErrNotFound)
			},
			wantStatus: http.StatusGone,
			wantDetail: "Post was removed",
		},
		{
			name: "http error wrapping auth unauthorized keeps headers",
			handler: func(*router.Context) error {
				return contract.NewHTTPError(http.StatusForbidden, "Upgrade your plan").
					WithHeader("X-Plan", "pro").
					WithCause(auth.ErrUnauthorized)
			},
			wantStatus: http.StatusForbidden,
			wantDetail: "Upgrade your plan",
			wantHeader: map[string]string{"X-Plan": "pro"},
		},
		{
			name:    "user map result wrapping orm not found",
			handler: func(*router.Context) error { return fmt.Errorf("load: %w", orm.ErrNotFound) },
			configure: func(h contract.ErrorHandler) {
				problem.MapIs(h, orm.ErrNotFound, func(err error) error {
					return contract.NewHTTPError(http.StatusNotFound, "No such record").WithCause(err)
				})
			},
			wantStatus: http.StatusNotFound,
			wantDetail: "No such record",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _, rec := newPipelineApp(t)
			a.Services.Errors.SetDebug(false)
			if tt.configure != nil {
				tt.configure(a.Services.Errors)
			}
			a.Router.Get("/t", tt.handler)

			req := httptest.NewRequest(http.MethodGet, "/t", nil)
			req.Header.Set("Accept", "application/json")
			w := httptest.NewRecorder()
			a.Router.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
			var body struct {
				Status int    `json:"status"`
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("problem body: %v (%q)", err, w.Body.String())
			}
			if body.Status != tt.wantStatus || body.Detail != tt.wantDetail {
				t.Errorf("problem body = %+v, want status %d detail %q", body, tt.wantStatus, tt.wantDetail)
			}
			for k, v := range tt.wantHeader {
				if got := w.Header().Get(k); got != v {
					t.Errorf("header %s = %q, want %q", k, got, v)
				}
			}
			if rec.count() != 0 {
				t.Errorf("reports = %d, want 0 (%v)", rec.count(), rec.errs)
			}
		})
	}
}

// TestErrorPipeline_PanicReachesRenderRules asserts a recovered panic
// reaches the application's render rules and still answers 500: a
// RenderFor rule for *router.PanicError writes the page, and a status rule
// for it is pinned to 500. The panic is reported once.
func TestErrorPipeline_PanicReachesRenderRules(t *testing.T) {
	t.Run("render for panic error", func(t *testing.T) {
		a, _, rec := newPipelineApp(t)
		fired := false
		problem.RenderFor(a.Services.Errors, func(rc problem.RenderContext, _ *router.PanicError, _ *problem.ErrorContext) bool {
			fired = true
			rc.WriteHeader(http.StatusInternalServerError)
			_, _ = rc.Write([]byte("branded panic page"))
			return true
		})
		a.Router.Get("/boom", func(*router.Context) error { panic("handler exploded") })

		w := httptest.NewRecorder()
		a.Router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))

		if !fired {
			t.Error("render rule for *router.PanicError did not fire")
		}
		if w.Code != http.StatusInternalServerError || w.Body.String() != "branded panic page" {
			t.Errorf("response = %d %q, want 500 branded panic page", w.Code, w.Body.String())
		}
		if rec.count() != 1 {
			t.Errorf("reports = %d, want 1", rec.count())
		}
	})
	t.Run("render status pinned to 500", func(t *testing.T) {
		a, _, rec := newPipelineApp(t)
		problem.RenderStatus[*router.PanicError](a.Services.Errors, http.StatusTeapot)
		a.Router.Get("/boom", func(*router.Context) error { panic("handler exploded") })

		w := httptest.NewRecorder()
		a.Router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/boom", nil))
		if w.Code != http.StatusInternalServerError {
			t.Errorf("panic status = %d, want 500 (body %q)", w.Code, w.Body.String())
		}
		if rec.count() != 1 {
			t.Errorf("reports = %d, want 1", rec.count())
		}
	})
}

// TestErrorPipeline_NegotiatesOnceOnTheRenderedError asserts a JSONWhen
// predicate keyed on the error's status is asked once per failure, with
// the error as it will be rendered: a bare context.DeadlineExceeded as its
// 503, orm.ErrNotFound through the framework prepare table as its 404, a
// request cut off by shutdown as its 503. A user render rule, a
// RenderStatus rule and the final Content-Type all agree with that one
// answer.
func TestErrorPipeline_NegotiatesOnceOnTheRenderedError(t *testing.T) {
	shutdownCtx := func() context.Context {
		ctx, cancel := context.WithCancelCause(context.Background())
		cancel(contract.ErrServerShuttingDown)
		return ctx
	}
	tests := []struct {
		name       string
		handler    router.HandlerFunc
		ctx        func() context.Context
		jsonStatus int // the predicate answers true for this status only
		wantStatus int
	}{
		{name: "deadline", handler: func(*router.Context) error { return context.DeadlineExceeded }, ctx: context.Background, jsonStatus: http.StatusServiceUnavailable, wantStatus: http.StatusServiceUnavailable},
		{name: "orm not found", handler: func(*router.Context) error { return orm.ErrNotFound }, ctx: context.Background, jsonStatus: http.StatusNotFound, wantStatus: http.StatusNotFound},
		{name: "shutdown cancel", handler: func(c *router.Context) error { return c.Request.Context().Err() }, ctx: shutdownCtx, jsonStatus: http.StatusServiceUnavailable, wantStatus: http.StatusServiceUnavailable},
		{name: "predicate declines", handler: func(*router.Context) error { return context.DeadlineExceeded }, ctx: context.Background, jsonStatus: http.StatusTeapot, wantStatus: http.StatusServiceUnavailable},
	}
	for _, tt := range tests {
		for _, statusRule := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/statusRule=%v", tt.name, statusRule), func(t *testing.T) {
				a, _, _ := newPipelineApp(t)
				h := a.Services.Errors
				var (
					mu      sync.Mutex
					answers []bool
					ruleSaw []bool
				)
				h.JSONWhen(func(_ *http.Request, err error) bool {
					status, _, ok := contract.StatusOf(err)
					answer := ok && status == tt.jsonStatus
					mu.Lock()
					answers = append(answers, answer)
					mu.Unlock()
					return answer
				})
				h.AddRenderRule(contract.RenderRule{
					Match: func(error) bool { return true },
					Render: func(rc contract.RenderContext, _ error, _ *contract.ErrorContext) bool {
						mu.Lock()
						ruleSaw = append(ruleSaw, rc.WantsJSON())
						mu.Unlock()
						return false
					},
				})
				wantStatus := tt.wantStatus
				if statusRule {
					problem.RenderStatus[*contract.HTTPError](h, http.StatusTeapot)
					wantStatus = http.StatusTeapot
				}
				a.Router.Get("/x", tt.handler)

				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(tt.ctx())
				req.Header.Set("Accept", "text/html")
				a.Router.ServeHTTP(w, req)

				mu.Lock()
				defer mu.Unlock()
				if len(answers) != 1 {
					t.Fatalf("JSONWhen asked %d times (%v), want once", len(answers), answers)
				}
				wantJSON := tt.jsonStatus == tt.wantStatus
				if answers[0] != wantJSON {
					t.Errorf("JSONWhen answered %v, want %v for the rendered %d", answers[0], wantJSON, tt.wantStatus)
				}
				if len(ruleSaw) != 1 || ruleSaw[0] != answers[0] {
					t.Errorf("render rule saw rc.WantsJSON() = %v, want [%v]", ruleSaw, answers[0])
				}
				if w.Code != wantStatus {
					t.Errorf("status = %d, want %d", w.Code, wantStatus)
				}
				ct := w.Header().Get("Content-Type")
				if gotJSON := ct == "application/problem+json"; gotJSON != answers[0] {
					t.Errorf("Content-Type = %q, want JSON = %v", ct, answers[0])
				}
			})
		}
	}
}

// TestRun_FailedCommandShutsDownBeforeExit asserts that a failed command
// shuts the app down before the process exits (the exit skips every
// deferred cleanup), and that a successful one neither shuts down nor
// exits.
func TestRun_FailedCommandShutsDownBeforeExit(t *testing.T) {
	tests := []struct {
		name         string
		argv         []string
		wantExit     int
		wantShutdown bool
	}{
		{name: "failed command", argv: []string{"run", "fail"}, wantExit: 3, wantShutdown: true},
		{name: "successful command", argv: []string{"help"}, wantExit: -1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mod := &shutdownRecorder{}
			a, _, _ := newPipelineApp(t, WithModules(mod))
			a.Commands(func(r *chain.Commands) { r.Add(failingCommand{err: exitCodeError{code: 3}}) })

			exitCode, shutDownAtExit := -1, false
			var stderr strings.Builder
			err := a.run(tt.argv, &stderr, func(code int) {
				exitCode, shutDownAtExit = code, mod.shutdowns.Load() > 0
			})
			if err != nil {
				t.Fatalf("run returned %v, want the error handled", err)
			}
			if exitCode != tt.wantExit {
				t.Errorf("exit code = %d, want %d", exitCode, tt.wantExit)
			}
			if shutDownAtExit != tt.wantShutdown {
				t.Errorf("shut down before exit = %v, want %v", shutDownAtExit, tt.wantShutdown)
			}
			if !tt.wantShutdown && mod.shutdowns.Load() > 0 {
				t.Error("a successful command shut the app down")
			}
		})
	}
}

// TestRequestFailed_DecidedByAnswer_ThroughApp drives failed requests
// through a real app's router boundary and error pipeline and counts the
// RequestFailed events the router dispatches and the reports: the event
// follows the status the pipeline answered with, so an error it maps to a
// 4xx (a framework not-found sentinel, an application MapIs rule)
// dispatches nothing, while a 500, a Handled cause after a written 500, a
// plain error after the handler committed a 200 itself and a recovered
// panic each dispatch once and are reported once.
func TestRequestFailed_DecidedByAnswer_ThroughApp(t *testing.T) {
	errGone := errors.New("record archived")
	writeThen := func(status int, err error) router.HandlerFunc {
		return func(c *router.Context) error {
			c.Response.WriteHeader(status)
			return err
		}
	}
	tests := []struct {
		name          string
		handler       router.HandlerFunc
		wantStatus    int
		wantFailed    int
		wantReports   int
		wantRecovered bool
	}{
		{name: "orm not found", handler: func(*router.Context) error { return fmt.Errorf("load user: %w", orm.ErrNotFound) }, wantStatus: http.StatusNotFound},
		{name: "map rule sentinel", handler: func(*router.Context) error { return fmt.Errorf("load post: %w", errGone) }, wantStatus: http.StatusGone},
		{name: "plain error", handler: func(*router.Context) error { return errors.New("db exploded") }, wantStatus: http.StatusInternalServerError, wantFailed: 1, wantReports: 1},
		{name: "handled not found after a written 404", handler: writeThen(http.StatusNotFound, contract.Handled(problem.NotFound())), wantStatus: http.StatusNotFound},
		{name: "handled plain error after a written 500", handler: writeThen(http.StatusInternalServerError, contract.Handled(errors.New("rendered by middleware"))), wantStatus: http.StatusInternalServerError, wantFailed: 1, wantReports: 1},
		{name: "plain error after a committed 200", handler: func(c *router.Context) error {
			if err := c.String(http.StatusOK, "partial"); err != nil {
				return err
			}
			return errors.New("stream broke")
		}, wantStatus: http.StatusOK, wantFailed: 1, wantReports: 1},
		{name: "recovered panic", handler: func(*router.Context) error { panic("boom") }, wantStatus: http.StatusInternalServerError, wantFailed: 1, wantReports: 1, wantRecovered: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := events.NewFakeDispatcher()
			a, _, rec := newPipelineApp(t, WithFakeEvents(fake))
			a.Services.Errors.SetDebug(false)
			problem.MapIs(a.Services.Errors, errGone, func(err error) error {
				return problem.Gone().WithCause(err)
			})
			a.Router.Get("/x", tt.handler)

			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header.Set("Accept", "application/json")
			w := httptest.NewRecorder()
			a.Router.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
			var failed []*router.RequestFailed
			for _, ev := range fake.GetDispatchedEvents() {
				if rf, ok := ev.(*router.RequestFailed); ok {
					failed = append(failed, rf)
				}
			}
			if len(failed) != tt.wantFailed {
				t.Fatalf("RequestFailed dispatched %d times, want %d", len(failed), tt.wantFailed)
			}
			if tt.wantFailed == 1 && failed[0].Recovered != tt.wantRecovered {
				t.Errorf("RequestFailed.Recovered = %v, want %v", failed[0].Recovered, tt.wantRecovered)
			}
			if rec.count() != tt.wantReports {
				t.Errorf("reports = %d, want %d", rec.count(), tt.wantReports)
			}
		})
	}
}

// TestBindClientErrors_ThroughApp drives bodies the client got wrong into
// c.Bind, c.BindXML and c.BindForm handlers through a real app's router
// boundary and error pipeline: each answers 400 problem+json with the
// client message (a form body over the limit 413), is not reported and
// dispatches no RequestFailed.
func TestBindClientErrors_ThroughApp(t *testing.T) {
	const (
		jsonType = "application/json"
		xmlType  = "application/xml"
		formType = "application/x-www-form-urlencoded"
	)
	tests := []struct {
		name        string
		path        string
		contentType string
		body        string
		wantStatus  int
		wantDetail  string
	}{
		{name: "json malformed", path: "/bind", contentType: jsonType, body: `{"name":`, wantStatus: http.StatusBadRequest, wantDetail: "malformed request body"},
		{name: "json syntax error", path: "/bind", contentType: jsonType, body: `{bad}`, wantStatus: http.StatusBadRequest, wantDetail: "malformed request body"},
		{name: "json wrong type", path: "/bind", contentType: jsonType, body: `{"name":5}`, wantStatus: http.StatusBadRequest, wantDetail: "malformed request body"},
		{name: "json empty body", path: "/bind", contentType: jsonType, body: ``, wantStatus: http.StatusBadRequest, wantDetail: "empty request body"},
		{name: "xml syntax error", path: "/bind-xml", contentType: xmlType, body: `<item><name>a</item>`, wantStatus: http.StatusBadRequest, wantDetail: "malformed request body"},
		{name: "xml empty body", path: "/bind-xml", contentType: xmlType, body: ``, wantStatus: http.StatusBadRequest, wantDetail: "empty request body"},
		{name: "form bad escape", path: "/bind-form", contentType: formType, body: "name=%zz", wantStatus: http.StatusBadRequest, wantDetail: "malformed request body"},
		{name: "form over the body limit", path: "/bind-form-small", contentType: formType, body: "name=" + strings.Repeat("a", 64), wantStatus: http.StatusRequestEntityTooLarge, wantDetail: "Request Entity Too Large"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := events.NewFakeDispatcher()
			a, logs, rec := newPipelineApp(t, WithFakeEvents(fake))
			a.Services.Errors.SetDebug(false)
			bindForm := func(c *router.Context) error {
				var v struct {
					Name string `form:"name"`
				}
				return c.BindForm(&v)
			}
			a.Router.Post("/bind", bindHandler)
			a.Router.Post("/bind-xml", func(c *router.Context) error {
				var v struct {
					Name string `xml:"name"`
				}
				return c.BindXML(&v)
			})
			a.Router.Post("/bind-form", bindForm)
			a.Router.Post("/bind-form-small", bindForm).Use(router.BodyLimit(8))

			req := httptest.NewRequest(http.MethodPost, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", tt.contentType)
			req.Header.Set("Accept", "application/json")
			w := httptest.NewRecorder()
			a.Router.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
			if got := w.Header().Get("Content-Type"); got != problem.ProblemTypeContent {
				t.Errorf("Content-Type = %q, want %q", got, problem.ProblemTypeContent)
			}
			var doc struct {
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
				t.Fatalf("body is not JSON: %v (%q)", err, w.Body.String())
			}
			if doc.Detail != tt.wantDetail {
				t.Errorf("detail = %q, want %q", doc.Detail, tt.wantDetail)
			}
			if rec.count() != 0 || logs.count("error") != 0 {
				t.Errorf("reports = %d, error logs = %d, want 0 and 0", rec.count(), logs.count("error"))
			}
			if err := fake.AssertNotDispatched(&router.RequestFailed{}); err != nil {
				t.Errorf("RequestFailed: %v", err)
			}
		})
	}
}
