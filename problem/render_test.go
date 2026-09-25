package problem

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

func TestRender_Order(t *testing.T) {
	var sawOriginal bool
	tests := []struct {
		name       string
		setup      func(h *Handler)
		err        error
		ctx        *ErrorContext
		wantStatus int
		wantBody   string
	}{
		{
			name: "RenderableWinsOverUserRule",
			setup: func(h *Handler) {
				RenderStatus[*renderableErr](h, http.StatusGone)
			},
			err:        &renderableErr{handled: true},
			wantStatus: http.StatusTeapot,
			wantBody:   "self rendered",
		},
		{
			name:       "RenderableDeclinesFallsThrough",
			err:        &renderableErr{handled: false},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "UserRuleOutranksFrameworkRule",
			setup: func(h *Handler) {
				h.AddFrameworkRenderRule(contract.RenderRule{Key: "fw", Match: matchIs(errSentinel), Status: http.StatusForbidden})
				RenderStatus[*contract.HTTPError](h, http.StatusGone)
				h.AddFrameworkPrepareRule(contract.MapRule{Match: matchIs(errSentinel), Map: func(err error) error {
					return NotFound().WithCause(err)
				}})
			},
			err:        errSentinel,
			wantStatus: http.StatusGone,
		},
		{
			name: "FrameworkRuleWhenNoUserRule",
			setup: func(h *Handler) {
				h.AddFrameworkRenderRule(contract.RenderRule{Key: "fw", Match: matchIs(errSentinel), Status: http.StatusForbidden})
			},
			err:        errSentinel,
			wantStatus: http.StatusForbidden,
		},
		{
			name: "UserRuleDeclinesFallsThrough",
			setup: func(h *Handler) {
				RenderFor[*contextualErr](h, func(RenderContext, *contextualErr, *ErrorContext) bool { return false })
			},
			err:        &contextualErr{},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "RenderForWrites",
			setup: func(h *Handler) {
				RenderFor[*contextualErr](h, func(rc RenderContext, err *contextualErr, _ *ErrorContext) bool {
					rc.WriteHeader(http.StatusAccepted)
					_, _ = rc.Write([]byte(err.Error()))
					return true
				})
			},
			err:        fmt.Errorf("wrap: %w", &contextualErr{}),
			wantStatus: http.StatusAccepted,
			wantBody:   "contextual",
		},
		{
			name: "PrepareMapsWrappedSentinelKeepsIs",
			setup: func(h *Handler) {
				h.AddFrameworkPrepareRule(contract.MapRule{Key: errSentinel, Match: matchIs(errSentinel), Map: func(err error) error {
					return NotFound().WithCause(err)
				}})
				h.BeforeRender(func(_ RenderContext, err error, status int) int {
					sawOriginal = errors.Is(err, errSentinel)
					return status
				})
			},
			err:        fmt.Errorf("load user: %w", errSentinel),
			wantStatus: http.StatusNotFound,
		},
		{name: "StdlibPrepareMaxBytes", err: fmt.Errorf("bind: %w", &http.MaxBytesError{Limit: 1}), wantStatus: http.StatusRequestEntityTooLarge},
		{name: "StdlibPrepareDeadline", err: fmt.Errorf("query: %w", context.DeadlineExceeded), wantStatus: http.StatusServiceUnavailable},
		{name: "StatusErrorStatus", err: &statusErr{code: http.StatusPaymentRequired}, wantStatus: http.StatusPaymentRequired},
		{
			name:       "RecoveredPanicAlways500",
			err:        panicerr.FromRecovered(NotFound()),
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:       "RecoveredContextAlways500",
			setup:      func(h *Handler) { RenderStatus[*renderableErr](h, http.StatusGone) },
			err:        &renderableErr{handled: true},
			ctx:        &ErrorContext{Recovered: true},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "RecoveredPanicReachesRenderFor",
			setup: func(h *Handler) {
				RenderFor[*panicerr.Error](h, func(rc RenderContext, _ *panicerr.Error, _ *ErrorContext) bool {
					rc.WriteHeader(http.StatusInternalServerError)
					_, _ = rc.Write([]byte("branded panic page"))
					return true
				})
			},
			err:        panicerr.FromRecovered("boom"),
			wantStatus: http.StatusInternalServerError,
			wantBody:   "branded panic page",
		},
		{
			name:       "StatusRuleOutsidePanic",
			setup:      func(h *Handler) { RenderStatus[*statusErr](h, http.StatusTeapot) },
			err:        &statusErr{code: http.StatusConflict},
			wantStatus: http.StatusTeapot,
		},
		{
			name:       "RecoveredPanicStatusRulePinnedTo500",
			setup:      func(h *Handler) { RenderStatus[*statusErr](h, http.StatusTeapot) },
			err:        &statusErr{code: http.StatusConflict},
			ctx:        &ErrorContext{Recovered: true},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "RecoveredPanicSkipsPrepare",
			setup: func(h *Handler) {
				h.AddFrameworkPrepareRule(contract.MapRule{Key: errSentinel, Match: matchIs(errSentinel), Map: func(err error) error {
					return NotFound().WithCause(err)
				}})
			},
			err:        errSentinel,
			ctx:        &ErrorContext{Recovered: true},
			wantStatus: http.StatusInternalServerError,
		},
		{
			name: "BeforeRenderChangesStatus",
			setup: func(h *Handler) {
				h.BeforeRender(func(rc RenderContext, _ error, status int) int {
					rc.SetHeader("X-Hook", fmt.Sprint(status))
					return http.StatusGone
				})
				h.BeforeRender(func(_ RenderContext, _ error, status int) int { return status + 1 })
			},
			err:        NotFound(),
			wantStatus: http.StatusGone + 1,
		},
		{
			name: "BeforeRenderInvalidStatusBecomes500",
			setup: func(h *Handler) {
				h.BeforeRender(func(RenderContext, error, int) int { return 42 })
			},
			err:        NotFound(),
			wantStatus: http.StatusInternalServerError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler()
			if tt.setup != nil {
				tt.setup(h)
			}
			rc, w := newRC(http.MethodGet, "/x")
			h.HandleRequest(rc, tt.err, tt.ctx)
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.wantBody != "" && w.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", w.Body.String(), tt.wantBody)
			}
		})
	}
	if !sawOriginal {
		t.Error("prepared error lost errors.Is to the original sentinel")
	}
}

func TestRender_WrittenNeverOverwritten(t *testing.T) {
	tests := []struct {
		name       string
		preWrite   bool
		err        error
		wantStatus int
		wantReport int
	}{
		{"AlreadyWritten", true, errors.New("late"), http.StatusCreated, 1},
		{"HandledCauseReportedNotRendered", false, contract.Handled(errors.New("rendered by middleware")), http.StatusOK, 1},
		{"HandledMarkedNotReported", false, contract.Handled(contract.MarkReported(errors.New("done"))), http.StatusOK, 0},
		{"MarkedHandledNotReported", false, contract.MarkReported(contract.Handled(errors.New("done"))), http.StatusOK, 0},
		{"BareSentinelNothing", false, contract.ErrResponseWritten, http.StatusOK, 0},
		{"HandledClientErrorNotReported", false, contract.Handled(NotFound()), http.StatusOK, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, _ := newTestHandler()
			rc, w := newRC(http.MethodGet, "/x")
			if tt.preWrite {
				rc.WriteHeader(http.StatusCreated)
			}
			h.HandleRequest(rc, tt.err, nil)
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if !tt.preWrite && w.Body.Len() != 0 {
				t.Errorf("body = %q, want nothing written", w.Body.String())
			}
			if rep.count() != tt.wantReport {
				t.Errorf("reports = %d, want %d", rep.count(), tt.wantReport)
			}
		})
	}
}

// TestRender_LastResort asserts a renderer that panics or fails leaves the
// plain-text 500: a failure is logged, and a panic is reported as a
// recovered panic after the error itself (see assertRenderPanicReported).
func TestRender_LastResort(t *testing.T) {
	tests := []struct {
		name      string
		renderer  Renderer
		wantLog   string
		wantPanic bool
	}{
		{name: "PanickingRenderer", renderer: panicRenderer{}, wantPanic: true},
		{name: "FailingRenderer", renderer: failRenderer{}, wantLog: "problem: rendering failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, logger := newTestHandler()
			h.AddRenderer("html", tt.renderer)
			rc, w := newRC(http.MethodGet, "/x")
			h.HandleRequest(rc, errors.New("boom"), nil)
			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500", w.Code)
			}
			if got := w.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
				t.Errorf("Content-Type = %q, want text/plain", got)
			}
			if w.Body.String() != "Internal Server Error" {
				t.Errorf("body = %q", w.Body.String())
			}
			if tt.wantLog != "" && !logger.has("error", tt.wantLog) {
				t.Errorf("missing log %q in %v", tt.wantLog, logger.all())
			}
			if tt.wantPanic {
				assertRenderPanicReported(t, rep, 2)
			}
		})
	}
}

// assertRenderPanicReported asserts rep holds want reports, the last one a
// recovered panic: its error a contract.RecoveredPanic and its context
// flagged Recovered with the panic stack.
func assertRenderPanicReported(t *testing.T, rep *recReporter, want int) {
	t.Helper()
	if n := rep.count(); n != want {
		t.Fatalf("reports = %d, want %d", n, want)
	}
	ctx, err := rep.last()
	var rp contract.RecoveredPanic
	if !errors.As(err, &rp) {
		t.Errorf("last report = %v (%T), want a contract.RecoveredPanic", err, err)
	}
	if ctx == nil || !ctx.Recovered || ctx.PanicStack == "" || ctx.StackTrace == nil {
		t.Errorf("last report context = %+v, want Recovered with both stacks", ctx)
	}
}

// panicWriter is a ResponseWriter whose every method panics.
type panicWriter struct{ header http.Header }

func (p *panicWriter) Header() http.Header       { return p.header }
func (p *panicWriter) Write([]byte) (int, error) { panic("write exploded") }
func (p *panicWriter) WriteHeader(int)           { panic("write header exploded") }

func TestRender_LastResortNeverRepanics(t *testing.T) {
	h, _, logger := newTestHandler()
	h.AddRenderer("html", panicRenderer{})
	rc := contract.NewRenderContext(&panicWriter{header: http.Header{}}, httptest.NewRequest(http.MethodGet, "/x", nil))
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("HandleRequest re-panicked: %v", p)
			}
		}()
		h.HandleRequest(rc, errors.New("boom"), nil)
	}()
	if !logger.has("error", "problem: last-resort response failed") {
		t.Errorf("missing last-resort log in %v", logger.all())
	}
}

func TestRender_SafeLogSwallowsPanickingLogger(t *testing.T) {
	safeLog(panicLogger{}, "msg")
	safeLog(nil, "msg")
}

type panicLogger struct{}

func (panicLogger) Debug(string, ...any) { panic("log") }
func (panicLogger) Info(string, ...any)  { panic("log") }
func (panicLogger) Warn(string, ...any)  { panic("log") }
func (panicLogger) Error(string, ...any) { panic("log") }
func (panicLogger) Fatal(string, ...any) { panic("log") }

func TestRender_ClientGoneWritesNothing(t *testing.T) {
	h, rep, _ := newTestHandler()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	rc, w := newRCWithContext(ctx, http.MethodGet, "/x")
	h.HandleRequest(rc, fmt.Errorf("read: %w", context.Canceled), nil)
	if rc.Written() || w.Body.Len() != 0 {
		t.Errorf("client-gone cancel wrote a response: %d %q", w.Code, w.Body.String())
	}
	if rep.count() != 0 {
		t.Errorf("client-gone cancel reported %d times", rep.count())
	}
}

func TestRender_Negotiation(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(h *Handler)
		path        string
		headers     []string
		wantContent string
	}{
		{name: "HTMLDefault", path: "/x", wantContent: "text/html; charset=utf-8"},
		{name: "APIPathNoLongerImpliesJSON", path: "/api/x", wantContent: "text/html; charset=utf-8"},
		{name: "AcceptJSON", path: "/x", headers: []string{"Accept", "application/json"}, wantContent: ProblemTypeContent},
		{name: "AcceptProblemJSON", path: "/x", headers: []string{"Accept", "application/problem+json"}, wantContent: ProblemTypeContent},
		{name: "XHR", path: "/x", headers: []string{"X-Requested-With", "XMLHttpRequest"}, wantContent: ProblemTypeContent},
		{name: "APIMode", setup: func(h *Handler) { h.SetAPIMode(true) }, path: "/x", wantContent: ProblemTypeContent},
		{name: "APIPrefix", setup: func(h *Handler) { h.SetAPIPrefixes("/v1/") }, path: "/v1/users", wantContent: ProblemTypeContent},
		{name: "APIPrefixMiss", setup: func(h *Handler) { h.SetAPIPrefixes("/v1/") }, path: "/v2/users", wantContent: "text/html; charset=utf-8"},
		{
			name:        "JSONWhenDecidesTrue",
			setup:       func(h *Handler) { h.JSONWhen(func(r *http.Request, _ error) bool { return r.URL.Path == "/x" }) },
			path:        "/x",
			wantContent: ProblemTypeContent,
		},
		{
			name: "JSONWhenOverridesAPIMode",
			setup: func(h *Handler) {
				h.SetAPIMode(true)
				h.JSONWhen(func(*http.Request, error) bool { return false })
			},
			path:        "/x",
			headers:     []string{"Accept", "application/json"},
			wantContent: "text/html; charset=utf-8",
		},
		{
			name: "JSONWhenNilRestoresDefault",
			setup: func(h *Handler) {
				h.JSONWhen(func(*http.Request, error) bool { return false })
				h.JSONWhen(nil)
			},
			path:        "/x",
			headers:     []string{"Accept", "application/json"},
			wantContent: ProblemTypeContent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler()
			if tt.setup != nil {
				tt.setup(h)
			}
			rc, w := newRC(http.MethodGet, tt.path, tt.headers...)
			h.HandleRequest(rc, NotFound(), nil)
			if got := w.Header().Get("Content-Type"); got != tt.wantContent {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantContent)
			}
			if w.Code != http.StatusNotFound {
				t.Errorf("status = %d, want 404", w.Code)
			}
		})
	}
}

func TestRender_ErrorHeadersCopied(t *testing.T) {
	multi := NotFound()
	multi.Header = http.Header{"Link": {"</a>", "</b>"}, "X-Bad": {"ok", "no\r\nsplit"}}
	tests := []struct {
		name   string
		err    error
		header string
		want   []string
	}{
		{"Allow", MethodNotAllowed("GET", "HEAD"), "Allow", []string{"GET, HEAD"}},
		{"RetryAfter", TooManyRequests(3 * time.Second), "Retry-After", []string{"3"}},
		{"WrappedKeepsHeaders", fmt.Errorf("limit: %w", TooManyRequests(time.Second)), "Retry-After", []string{"1"}},
		{"MultiValue", multi, "Link", []string{"</a>", "</b>"}},
		{"MultiValueCRLFDropped", multi, "X-Bad", []string{"ok"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler()
			rc, w := newRC(http.MethodGet, "/x")
			h.HandleRequest(rc, tt.err, nil)
			if got := w.Header().Values(tt.header); fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Errorf("%s = %v, want %v", tt.header, got, tt.want)
			}
		})
	}
}

// fakeErrorPage records RenderErrorPage calls.
type fakeErrorPage struct {
	ok      bool
	err     error
	write   bool
	status  int
	message string
}

func (p *fakeErrorPage) RenderErrorPage(rc RenderContext, status int, message string) (bool, error) {
	p.status, p.message = status, message
	if p.write {
		rc.WriteHeader(status)
	}
	return p.ok, p.err
}

func TestRender_Inertia(t *testing.T) {
	tests := []struct {
		name         string
		method       string
		path         string
		headers      []string
		page         *fakeErrorPage
		debug        bool
		err          error
		wantStatus   int
		wantLocation string
		wantPage     int
		wantContent  string
	}{
		{
			name: "ErrorPageAtRealStatus", method: http.MethodGet, path: "/p",
			page: &fakeErrorPage{ok: true, write: true}, err: Forbidden("members only"),
			wantStatus: http.StatusForbidden, wantPage: http.StatusForbidden,
		},
		{
			name: "NoPageGETReloadsCurrentURL", method: http.MethodGet, path: "/posts/1?tab=a",
			err: NotFound(), wantStatus: http.StatusConflict, wantLocation: "/posts/1?tab=a",
		},
		{
			name: "PageDeclinesFallsTo409", method: http.MethodGet, path: "/p",
			page: &fakeErrorPage{ok: false}, err: NotFound(),
			wantStatus: http.StatusConflict, wantLocation: "/p", wantPage: http.StatusNotFound,
		},
		{
			name: "PageErrorFallsTo409", method: http.MethodGet, path: "/p",
			page: &fakeErrorPage{ok: false, err: errors.New("no component")}, err: NotFound(),
			wantStatus: http.StatusConflict, wantLocation: "/p", wantPage: http.StatusNotFound,
		},
		{
			name: "POSTSameOriginReferer", method: http.MethodPost, path: "/posts",
			headers: []string{"Referer", "http://example.com/posts/new?draft=1"},
			err:     errors.New("boom"), wantStatus: http.StatusConflict, wantLocation: "/posts/new?draft=1",
		},
		{
			name: "POSTCrossOriginReferer", method: http.MethodPost, path: "/posts",
			headers: []string{"Referer", "https://evil.test/phish"},
			err:     errors.New("boom"), wantStatus: http.StatusConflict, wantLocation: "/",
		},
		{
			name: "POSTRelativeReferer", method: http.MethodPut, path: "/posts",
			headers: []string{"Referer", "/posts/2/edit"},
			err:     errors.New("boom"), wantStatus: http.StatusConflict, wantLocation: "/posts/2/edit",
		},
		{
			name: "POSTProtocolRelativeReferer", method: http.MethodPost, path: "/posts",
			headers: []string{"Referer", "//evil.test/x"},
			err:     errors.New("boom"), wantStatus: http.StatusConflict, wantLocation: "/",
		},
		{
			name: "POSTNoReferer", method: http.MethodDelete, path: "/posts/1",
			err: errors.New("boom"), wantStatus: http.StatusConflict, wantLocation: "/",
		},
		{
			name: "DebugRendersDebugPageAtRealStatus", method: http.MethodGet, path: "/p",
			debug: true, err: errors.New("boom"),
			wantStatus: http.StatusInternalServerError, wantContent: "text/html; charset=utf-8",
		},
		{
			name: "DebugSkipsErrorPage", method: http.MethodGet, path: "/p",
			page: &fakeErrorPage{ok: true, write: true}, debug: true, err: NotFound(),
			wantStatus: http.StatusNotFound, wantContent: "text/html; charset=utf-8",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler(WithDebug(tt.debug))
			if tt.page != nil {
				h.SetErrorPageRenderer(tt.page)
			}
			headers := append([]string{"X-Inertia", "true"}, tt.headers...)
			rc, w := newRC(tt.method, tt.path, headers...)
			h.HandleRequest(rc, tt.err, nil)
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if got := w.Header().Get("X-Inertia-Location"); got != tt.wantLocation {
				t.Errorf("X-Inertia-Location = %q, want %q", got, tt.wantLocation)
			}
			if tt.page != nil && tt.page.status != tt.wantPage {
				t.Errorf("page status = %d, want %d", tt.page.status, tt.wantPage)
			}
			if tt.wantContent != "" && w.Header().Get("Content-Type") != tt.wantContent {
				t.Errorf("Content-Type = %q, want %q", w.Header().Get("Content-Type"), tt.wantContent)
			}
		})
	}
}

func TestRender_InertiaPageMessagePolicy(t *testing.T) {
	tests := []struct {
		name  string
		err   error
		debug bool
		want  string
	}{
		{"ClientErrorMessage", Forbidden("members only"), false, "members only"},
		{"ServerErrorHidden", Internal("db password wrong"), false, "Internal Server Error"},
		{"PlainErrorHidden", errors.New("secret"), false, "Internal Server Error"},
		{"NotFoundTitle", NotFound(), false, "Not Found"},
		{"MessageErrorFacet", &messageErr{status: http.StatusConflict, msg: "already taken"}, false, "already taken"},
		{"MessageErrorEmptyUsesTitle", &messageErr{status: http.StatusConflict}, false, "Conflict"},
		{"MessageErrorServerHidden", &messageErr{status: http.StatusBadGateway, msg: "upstream secret"}, false, "Bad Gateway"},
		{"OuterMessageErrorWins", Conflict("outer").WithCause(&messageErr{status: http.StatusConflict, msg: "inner"}), false, "outer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler(WithDebug(tt.debug))
			page := &fakeErrorPage{ok: true, write: true}
			h.SetErrorPageRenderer(page)
			rc, _ := newRC(http.MethodGet, "/p", "X-Inertia", "true")
			h.HandleRequest(rc, tt.err, nil)
			if page.message != tt.want {
				t.Errorf("message = %q, want %q", page.message, tt.want)
			}
		})
	}
}

// messageErr is a contract.MessageError that is not an HTTPError.
type messageErr struct {
	status int
	msg    string
}

func (e *messageErr) Error() string         { return "message error" }
func (e *messageErr) StatusCode() int       { return e.status }
func (e *messageErr) ClientMessage() string { return e.msg }

// fakeLocatorPage is an error page renderer with the ReloadLocator facet
// that declines every page.
type fakeLocatorPage struct {
	location string
	asked    int
}

func (p *fakeLocatorPage) RenderErrorPage(RenderContext, int, string) (bool, error) {
	return false, nil
}

func (p *fakeLocatorPage) ReloadLocation(*http.Request) string {
	p.asked++
	return p.location
}

func TestRender_InertiaReloadLocator(t *testing.T) {
	tests := []struct {
		name         string
		method       string
		path         string
		referer      string
		location     string
		wantLocation string
	}{
		{name: "LocatorAnswers", method: http.MethodPost, path: "/posts", referer: "http://example.com/posts/new", location: "https://app.example.com/posts/new", wantLocation: "https://app.example.com/posts/new"},
		{name: "LocatorEmptyFallsBackGET", method: http.MethodGet, path: "/posts/1?tab=a", wantLocation: "/posts/1?tab=a"},
		{name: "LocatorEmptyFallsBackPOST", method: http.MethodPost, path: "/posts", referer: "http://example.com/posts/new", wantLocation: "/posts/new"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler()
			page := &fakeLocatorPage{location: tt.location}
			h.SetErrorPageRenderer(page)
			headers := []string{"X-Inertia", "true"}
			if tt.referer != "" {
				headers = append(headers, "Referer", tt.referer)
			}
			rc, w := newRC(tt.method, tt.path, headers...)
			h.HandleRequest(rc, NotFound(), nil)
			if w.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409", w.Code)
			}
			if got := w.Header().Get("X-Inertia-Location"); got != tt.wantLocation {
				t.Errorf("X-Inertia-Location = %q, want %q", got, tt.wantLocation)
			}
			if page.asked != 1 {
				t.Errorf("locator asked %d times, want 1", page.asked)
			}
		})
	}
}

func TestRender_FullPageErrorPage(t *testing.T) {
	tests := []struct {
		name        string
		page        *fakeErrorPage
		debug       bool
		accept      string
		err         error
		wantStatus  int
		wantPage    int
		wantContent string
		wantLog     bool
	}{
		{
			name: "ErrorPageAtRealStatus", page: &fakeErrorPage{ok: true, write: true},
			err: NotFound(), wantStatus: http.StatusNotFound, wantPage: http.StatusNotFound,
		},
		{
			name: "PageDeclinesHTMLRenderer", page: &fakeErrorPage{ok: false},
			err: NotFound(), wantStatus: http.StatusNotFound, wantPage: http.StatusNotFound,
			wantContent: "text/html; charset=utf-8",
		},
		{
			name: "PageErrorHTMLRendererAndLog", page: &fakeErrorPage{ok: false, err: errors.New("template broke")},
			err: errors.New("boom"), wantStatus: http.StatusInternalServerError, wantPage: http.StatusInternalServerError,
			wantContent: "text/html; charset=utf-8", wantLog: true,
		},
		{
			name: "DebugSkipsErrorPage", page: &fakeErrorPage{ok: true, write: true}, debug: true,
			err: NotFound(), wantStatus: http.StatusNotFound, wantContent: "text/html; charset=utf-8",
		},
		{
			name: "JSONSkipsErrorPage", page: &fakeErrorPage{ok: true, write: true}, accept: "application/json",
			err: NotFound(), wantStatus: http.StatusNotFound, wantContent: ProblemTypeContent,
		},
		{
			name: "NoPageHTMLRenderer", err: NotFound(), wantStatus: http.StatusNotFound,
			wantContent: "text/html; charset=utf-8",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, logger := newTestHandler(WithDebug(tt.debug))
			if tt.page != nil {
				h.SetErrorPageRenderer(tt.page)
			}
			accept := tt.accept
			if accept == "" {
				accept = "text/html"
			}
			rc, w := newRC(http.MethodGet, "/p", "Accept", accept)
			h.HandleRequest(rc, tt.err, nil)
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if tt.page != nil && tt.page.status != tt.wantPage {
				t.Errorf("page status = %d, want %d", tt.page.status, tt.wantPage)
			}
			if tt.wantContent != "" && w.Header().Get("Content-Type") != tt.wantContent {
				t.Errorf("Content-Type = %q, want %q", w.Header().Get("Content-Type"), tt.wantContent)
			}
			logged := false
			for _, e := range logger.all() {
				if e.msg == "problem: error page failed" {
					logged = true
				}
			}
			if logged != tt.wantLog {
				t.Errorf("error page failure logged = %v, want %v", logged, tt.wantLog)
			}
		})
	}
}

func TestInertiaLocation_Edges(t *testing.T) {
	tests := []struct {
		name string
		req  func() *http.Request
		want string
	}{
		{"NilRequest", func() *http.Request { return nil }, "/"},
		{"GETUnsafeURI", func() *http.Request {
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			r.URL.Path = "//evil.test"
			return r
		}, "/"},
		{"RefererWithUserinfo", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/x", nil)
			r.Header.Set("Referer", "http://user@example.com/a")
			return r
		}, "/"},
		{"RefererBadScheme", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/x", nil)
			r.Header.Set("Referer", "javascript://example.com/a")
			return r
		}, "/"},
		{"RefererUnparseable", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/x", nil)
			r.Header.Set("Referer", "http://[::1")
			return r
		}, "/"},
		{"RefererHostOnly", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/x", nil)
			r.Header.Set("Referer", "http://example.com")
			return r
		}, "/"},
		{"RefererBackslashEscaped", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/x", nil)
			r.Header.Set("Referer", "/\\evil.test")
			return r
		}, "/%5Cevil.test"},
		{"RefererNotRooted", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/x", nil)
			r.Header.Set("Referer", "posts")
			return r
		}, "/"},
		{"RefererInteriorSpace", func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/x", nil)
			r.Header.Set("Referer", "http://example.com/search?q=a b")
			return r
		}, "/search?q=a b"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := inertiaLocation(tt.req()); got != tt.want {
				t.Errorf("inertiaLocation = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestIsLocalPath asserts isLocalPath accepts exactly the paths starting
// with "/" that contract.SanitizeRedirect keeps unchanged: an interior
// space is fine, an edge space, a control byte, a backslash, a slash
// lookalike or a leading "//" is not.
func TestIsLocalPath(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"/", true},
		{"/a?b=c", true},
		{"", false},
		{"a", false},
		{"//x", false},
		{"/a b", true},
		{"/a ", false},
		{"/\t/evil", false},
		{"/a\x00", false},
		{"/a\x7f", false},
		{"/a／b", false},
		{"/a\\b", false},
	}
	for _, tt := range tests {
		if got := isLocalPath(tt.in); got != tt.want {
			t.Errorf("isLocalPath(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestHTMLRenderer_TemplateLookup(t *testing.T) {
	r := NewHTMLRenderer()
	mustTpl := func(s string) *template.Template { return template.Must(template.New("t").Parse(s)) }
	if err := r.RegisterStatusTemplate(http.StatusNotFound, mustTpl("status {{.StatusCode}} {{.Message}}")); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterClassTemplate(4, mustTpl("class4 {{.StatusCode}}")); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterClassTemplate(5, mustTpl("class5 {{.StatusCode}}")); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		err    error
		debug  bool
		prefix string
	}{
		{"ExactStatus", NotFound("no post"), false, "status 404 no post"},
		{"ClassFour", Forbidden(), false, "class4 403"},
		{"ClassFive", errors.New("boom"), false, "class5 500"},
		{"DebugIgnoresRegistrations", NotFound(), true, "<!DOCTYPE html>"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler(WithDebug(tt.debug), WithRenderers(map[string]Renderer{"html": r}))
			rc, w := newRC(http.MethodGet, "/x")
			h.HandleRequest(rc, tt.err, nil)
			if !strings.HasPrefix(w.Body.String(), tt.prefix) {
				t.Errorf("body = %q, want prefix %q", w.Body.String(), tt.prefix)
			}
		})
	}
}

func TestHTMLRenderer_RegistrationErrors(t *testing.T) {
	r := NewHTMLRenderer()
	tpl := template.Must(template.New("t").Parse("x"))
	tests := []struct {
		name string
		err  error
	}{
		{"StatusNilTemplate", r.RegisterStatusTemplate(404, nil)},
		{"StatusTooLow", r.RegisterStatusTemplate(99, tpl)},
		{"StatusTooHigh", r.RegisterStatusTemplate(1000, tpl)},
		{"ClassNilTemplate", r.RegisterClassTemplate(4, nil)},
		{"ClassThree", r.RegisterClassTemplate(3, tpl)},
	}
	for _, tt := range tests {
		if !errors.Is(tt.err, ErrInvalidTemplate) {
			t.Errorf("%s: err = %v, want ErrInvalidTemplate", tt.name, tt.err)
		}
	}
}

func TestHTMLRenderer_TemplateFailureLeavesResponseUntouched(t *testing.T) {
	r := NewHTMLRenderer()
	if err := r.RegisterStatusTemplate(404, template.Must(template.New("t").Parse("{{.Missing.Field}}"))); err != nil {
		t.Fatal(err)
	}
	h, _, logger := newTestHandler(WithRenderers(map[string]Renderer{"html": r}))
	rc, w := newRC(http.MethodGet, "/x")
	h.HandleRequest(rc, NotFound(), nil)
	if w.Code != http.StatusInternalServerError || w.Body.String() != "Internal Server Error" {
		t.Errorf("got %d %q, want the plain-text 500", w.Code, w.Body.String())
	}
	if !logger.has("error", "problem: rendering failed") {
		t.Error("template failure not logged")
	}
}

func TestHTMLRenderer_DebugPage(t *testing.T) {
	h, _, _ := newTestHandler(WithDebug(true))
	ctx := NewErrorContext()
	ctx.RequestID = "req-1"
	ctx.StackTrace = contract.CaptureStackTrace(0)
	ctx.WithExtra("tenant", "acme")
	rc, w := newRC(http.MethodGet, "/x")
	h.HandleRequest(rc, Internal("db down").WithCause(errors.New("dial tcp: refused")), ctx)
	body := w.Body.String()
	for _, want := range []string{"500 Internal Server Error", "*contract.HTTPError", "render_test.go", "req-1", "tenant", "acme", "dial tcp: refused", "Stack Trace"} {
		if !strings.Contains(body, want) {
			t.Errorf("debug page missing %q", want)
		}
	}
}

func TestHTMLRenderer_ProductionPageHidesInternals(t *testing.T) {
	h, _, _ := newTestHandler()
	rc, w := newRC(http.MethodGet, "/x")
	h.HandleRequest(rc, Internal("db password is hunter2").WithCause(errors.New("dial")), nil)
	body := w.Body.String()
	for _, leak := range []string{"hunter2", "dial", "contract.HTTPError"} {
		if strings.Contains(body, leak) {
			t.Errorf("production page leaks %q", leak)
		}
	}
	if !strings.Contains(body, "Internal Server Error") {
		t.Error("production page missing status text")
	}
}

func TestHTMLRenderer_ContentTypes(t *testing.T) {
	if NewHTMLRenderer().ContentType() != "text/html" || NewJSONRenderer().ContentType() != ProblemTypeContent {
		t.Error("unexpected renderer content types")
	}
	custom := NewHTMLRendererWithTemplates(template.Must(template.New("d").Parse("D")), template.Must(template.New("e").Parse("E")))
	h, _, _ := newTestHandler(WithRenderers(map[string]Renderer{"html": custom}))
	rc, w := newRC(http.MethodGet, "/x")
	h.HandleRequest(rc, NotFound(), nil)
	if w.Body.String() != "E" {
		t.Errorf("custom error template not used: %q", w.Body.String())
	}
}

func TestRendererFor_FallsBackToBuiltins(t *testing.T) {
	s := &snapshot{renderers: map[string]Renderer{}}
	if _, ok := rendererFor(s, "json").(*JSONRenderer); !ok {
		t.Error("json fallback is not the JSONRenderer")
	}
	if _, ok := rendererFor(s, "html").(*HTMLRenderer); !ok {
		t.Error("html fallback is not the HTMLRenderer")
	}
}

func TestRender_PublicRenderAppliesMapWithoutReporting(t *testing.T) {
	h, rep, _ := newTestHandler()
	MapIs(h, errSentinel, func(err error) error { return Conflict().WithCause(err) })
	rc, w := newRC(http.MethodGet, "/x")
	h.Render(rc, errSentinel, nil)
	h.Render(nil, errSentinel, nil)
	h.Render(rc, nil, nil)
	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", w.Code)
	}
	if rep.count() != 0 {
		t.Errorf("Render reported %d times", rep.count())
	}
}

func TestHandleRequest_NilErrAndNilRC(t *testing.T) {
	h, rep, _ := newTestHandler()
	rc, w := newRC(http.MethodGet, "/x")
	h.HandleRequest(rc, nil, nil)
	if rc.Written() || rep.count() != 0 {
		t.Error("nil error must do nothing")
	}
	h.HandleRequest(nil, errors.New("console-ish"), nil)
	if rep.count() != 1 {
		t.Errorf("nil rc still reports: got %d", rep.count())
	}
	_ = w
}

func TestHandleRequest_FillsRequestContext(t *testing.T) {
	h, rep, _ := newTestHandler()
	rc, _ := newRC(http.MethodPost, "/orders?id=1", "User-Agent", "probe/1", "X-Forwarded-For", "6.6.6.6")
	h.HandleRequest(rc, errors.New("boom"), &ErrorContext{RequestID: "r1"})
	ctx, _ := rep.last()
	if ctx.Method != http.MethodPost || ctx.URL != "/orders" || ctx.UserAgent != "probe/1" || ctx.RequestID != "r1" {
		t.Errorf("context not filled: %+v", ctx)
	}
	if ctx.IP != "192.0.2.1" {
		t.Errorf("IP = %q, want RemoteAddr (untrusted forwarded header ignored)", ctx.IP)
	}
	if ctx.Timestamp.IsZero() {
		t.Error("timestamp not set")
	}
}

// TestRender_FullPageErrorPagePrecedence asserts the order a full-page HTML
// request is answered in outside debug mode: a page the application
// supplied (status, class or fallback template, custom html renderer),
// then the configured error page, then the built-in template. An Inertia
// request keeps the error page first.
func TestRender_FullPageErrorPagePrecedence(t *testing.T) {
	tpl := func(body string) *template.Template {
		return template.Must(template.New("t").Parse(body))
	}
	tests := []struct {
		name      string
		setup     func(h *Handler)
		inertia   bool
		wantPage  bool
		wantBody  string
		wantBuilt bool
	}{
		{name: "NoAppPageErrorPageWins", wantPage: true},
		{
			name: "StatusTemplateWins",
			setup: func(h *Handler) {
				r := NewHTMLRenderer()
				_ = r.RegisterStatusTemplate(http.StatusNotFound, tpl("status page"))
				h.AddRenderer("html", r)
			},
			wantBody: "status page",
		},
		{
			name: "ClassTemplateWins",
			setup: func(h *Handler) {
				r := NewHTMLRenderer()
				_ = r.RegisterClassTemplate(4, tpl("class page"))
				h.AddRenderer("html", r)
			},
			wantBody: "class page",
		},
		{
			name: "OtherStatusTemplateLeavesErrorPage",
			setup: func(h *Handler) {
				r := NewHTMLRenderer()
				_ = r.RegisterStatusTemplate(http.StatusGone, tpl("gone page"))
				h.AddRenderer("html", r)
			},
			wantPage: true,
		},
		{
			name:     "FallbackTemplateWins",
			setup:    func(h *Handler) { h.AddRenderer("html", NewHTMLRendererWithTemplates(nil, tpl("fallback page"))) },
			wantBody: "fallback page",
		},
		{
			name:     "CustomHTMLRendererWins",
			setup:    func(h *Handler) { h.AddRenderer("html", jsonStamp{}) },
			wantBody: `{"stamp":true}`,
		},
		{
			name: "InertiaKeepsErrorPageFirst",
			setup: func(h *Handler) {
				r := NewHTMLRenderer()
				_ = r.RegisterStatusTemplate(http.StatusNotFound, tpl("status page"))
				h.AddRenderer("html", r)
			},
			inertia:  true,
			wantPage: true,
		},
		{name: "DeclinedErrorPageFallsToBuiltin", wantBuilt: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler()
			page := &fakeErrorPage{ok: !tt.wantBuilt, write: !tt.wantBuilt}
			h.SetErrorPageRenderer(page)
			if tt.setup != nil {
				tt.setup(h)
			}
			headers := []string{"Accept", "text/html"}
			if tt.inertia {
				headers = append(headers, "X-Inertia", "true")
			}
			rc, w := newRC(http.MethodGet, "/p", headers...)
			h.HandleRequest(rc, NotFound(), nil)

			if w.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404 (body %q)", w.Code, w.Body.String())
			}
			if called := page.status != 0; called != (tt.wantPage || tt.wantBuilt) {
				t.Errorf("error page asked = %v, want %v", called, tt.wantPage || tt.wantBuilt)
			}
			if tt.wantBody != "" && w.Body.String() != tt.wantBody {
				t.Errorf("body = %q, want %q", w.Body.String(), tt.wantBody)
			}
			if tt.wantBuilt && !strings.Contains(w.Body.String(), "Not Found") {
				t.Errorf("body = %q, want the built-in page", w.Body.String())
			}
		})
	}
}

// TestFrameworkRenderFor_AnswersOnlyTheStatusOwner asserts a framework
// render rule registered for T runs only while the T owns the status the
// error resolves to, and never for a recovered panic.
func TestFrameworkRenderFor_AnswersOnlyTheStatusOwner(t *testing.T) {
	conflict := &statusErr{code: http.StatusConflict}
	tests := []struct {
		name       string
		err        error
		recovered  bool
		wantStatus int
	}{
		{name: "OwnError", err: conflict, wantStatus: http.StatusTeapot},
		{name: "WrappedOwnError", err: fmt.Errorf("load: %w", conflict), wantStatus: http.StatusTeapot},
		{name: "OuterSameStatus", err: contract.NewHTTPError(http.StatusConflict).WithCause(conflict), wantStatus: http.StatusTeapot},
		{name: "OuterOtherStatus", err: Internal().WithCause(conflict), wantStatus: http.StatusInternalServerError},
		{name: "RecoveredPanic", err: conflict, recovered: true, wantStatus: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler()
			FrameworkRenderFor[*statusErr](h, func(rc RenderContext, _ error, _ *ErrorContext) bool {
				rc.WriteHeader(http.StatusTeapot)
				return true
			})
			ctx := NewErrorContext()
			ctx.Recovered = tt.recovered
			rc, w := newRC(http.MethodGet, "/x", "Accept", "application/json")
			h.HandleRequest(rc, tt.err, ctx)
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

// failingJSONRenderer declines every render without writing, so the
// pipeline falls back to the plain-text 500.
type failingJSONRenderer struct{}

func (failingJSONRenderer) ContentType() string { return ProblemTypeContent }

func (failingJSONRenderer) Render(RenderContext, error, *ErrorContext, int, bool) error {
	return errors.New("renderer down")
}

// TestRender_DropsStaleContentLengthOnARealServer asserts a Content-Length
// the handler staged before failing never reaches the error response:
// every renderer (JSON, HTML, the Inertia reload) and the last resort
// deliver their full body through a real server, which enforces the
// declared length.
func TestRender_DropsStaleContentLengthOnARealServer(t *testing.T) {
	tests := []struct {
		name       string
		opts       []Option
		err        error
		headers    map[string]string
		wantStatus int
		wantBody   func(body string) bool
	}{
		{
			name:       "JSON",
			err:        errors.New("db down"),
			headers:    map[string]string{"Accept": "application/json"},
			wantStatus: http.StatusInternalServerError,
			wantBody: func(body string) bool {
				var doc map[string]any
				return json.Unmarshal([]byte(body), &doc) == nil && doc["status"] == float64(http.StatusInternalServerError)
			},
		},
		{
			name:       "HTML",
			err:        NotFound(),
			headers:    map[string]string{"Accept": "text/html"},
			wantStatus: http.StatusNotFound,
			wantBody:   func(body string) bool { return strings.Contains(body, "</html>") },
		},
		{
			name:       "InertiaReload",
			err:        NotFound(),
			headers:    map[string]string{"X-Inertia": "true", "Accept": "text/html"},
			wantStatus: http.StatusConflict,
			wantBody:   func(body string) bool { return body == "" },
		},
		{
			name:       "LastResort",
			opts:       []Option{WithRenderers(map[string]Renderer{"json": failingJSONRenderer{}})},
			err:        errors.New("db down"),
			headers:    map[string]string{"Accept": "application/json"},
			wantStatus: http.StatusInternalServerError,
			wantBody:   func(body string) bool { return body == http.StatusText(http.StatusInternalServerError) },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler(tt.opts...)
			h.SetDebug(false)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Length", "1")
				h.HandleRequest(contract.NewRenderContext(w, r), tt.err, nil)
			}))
			defer srv.Close()

			req, err := http.NewRequest(http.MethodGet, srv.URL+"/x", nil)
			if err != nil {
				t.Fatalf("NewRequest: %v", err)
			}
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			resp, err := srv.Client().Do(req)
			if err != nil {
				t.Fatalf("GET: %v", err)
			}
			defer resp.Body.Close()
			raw, err := io.ReadAll(resp.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			if resp.StatusCode != tt.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, tt.wantStatus)
			}
			if !tt.wantBody(string(raw)) {
				t.Errorf("body = %q (Content-Length %d), want the full error body", raw, resp.ContentLength)
			}
		})
	}
}

// TestRender_ResponseWrittenMarkers exercises Render directly: an error
// marking the response written (the bare sentinel or a contract.Handled
// value) writes nothing, while a recovered panic carrying the marker in
// its value, or a ctx flagged recovered with no panic node, renders the
// 500. Render never reports.
func TestRender_ResponseWrittenMarkers(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		recovered  bool
		wantStatus int // 0: nothing written
	}{
		{name: "BareSentinel", err: contract.ErrResponseWritten},
		{name: "Handled", err: contract.Handled(errors.New("rendered by middleware"))},
		{name: "HandledClientError", err: contract.Handled(NotFound())},
		{name: "WrappedSentinel", err: fmt.Errorf("mw: %w", contract.ErrResponseWritten)},
		{name: "HandledAroundPanic", err: contract.Handled(panicerr.FromRecovered("boom")), recovered: true},
		{name: "PanicCarryingSentinel", err: panicerr.FromRecovered(contract.ErrResponseWritten), recovered: true, wantStatus: http.StatusInternalServerError},
		{name: "PanicCarryingHandled", err: panicerr.FromRecovered(contract.Handled(NotFound())), recovered: true, wantStatus: http.StatusInternalServerError},
		{name: "RecoveredNoPanicNode", err: contract.ErrResponseWritten, recovered: true, wantStatus: http.StatusInternalServerError},
		{name: "Unmarked", err: NotFound(), wantStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, _ := newTestHandler()
			rc, w := newRC(http.MethodGet, "/x", "Accept", "application/json")
			ctx := NewErrorContext()
			ctx.Recovered = tt.recovered
			h.Render(rc, tt.err, ctx)
			if rep.count() != 0 {
				t.Errorf("reports = %d, want 0", rep.count())
			}
			if tt.wantStatus == 0 {
				if rc.Written() || w.Body.Len() != 0 {
					t.Errorf("wrote %d %q, want nothing", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d (body %q)", w.Code, tt.wantStatus, w.Body.String())
			}
		})
	}
}

// TestRender_HandledAfterAPlainWriterAppendsNothing asserts a caller that
// wrote a deliberate response through a plain http.ResponseWriter (which
// does not expose its commitment) and passes contract.Handled(err) to
// Render through a fresh render context gets no appended body.
func TestRender_HandledAfterAPlainWriterAppendsNothing(t *testing.T) {
	h, _, _ := newTestHandler()
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("mine"))

	h.Render(contract.NewRenderContext(w, r), contract.Handled(errors.New("done")), nil)

	if w.Code != http.StatusAccepted || w.Body.String() != "mine" {
		t.Errorf("response = %d %q, want 202 %q", w.Code, w.Body.String(), "mine")
	}
}

// panicOnceHeaderWriter panics on its first WriteHeader, before anything
// reaches the wire, and writes through afterwards.
type panicOnceHeaderWriter struct {
	*httptest.ResponseRecorder
	panicked bool
}

func (w *panicOnceHeaderWriter) WriteHeader(code int) {
	if !w.panicked {
		w.panicked = true
		panic("hook exploded")
	}
	w.ResponseRecorder.WriteHeader(code)
}

// TestRender_PanickingRedirectFallbackHasNoLocation asserts a render rule
// whose redirect panics in the status write falls back to the plain-text
// 500 without the redirect's Location header.
func TestRender_PanickingRedirectFallbackHasNoLocation(t *testing.T) {
	h, rep, _ := newTestHandler()
	RenderFor(h, func(rc RenderContext, _ *HTTPError, _ *ErrorContext) bool {
		return rc.Redirect(http.StatusSeeOther, "/login") == nil
	})
	w := &panicOnceHeaderWriter{ResponseRecorder: httptest.NewRecorder()}
	rc := contract.NewRenderContext(w, httptest.NewRequest(http.MethodGet, "/x", nil))

	h.HandleRequest(rc, NotFound(), nil)

	if w.Code != http.StatusInternalServerError || w.Body.String() != http.StatusText(http.StatusInternalServerError) {
		t.Errorf("response = %d %q, want the plain-text 500", w.Code, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "" {
		t.Errorf("Location = %q, want none", loc)
	}
	// NotFound is not reported; the render panic is.
	assertRenderPanicReported(t, rep, 1)
}

// TestFakeHandler_RenderResponseWrittenMarkers mirrors
// TestRender_ResponseWrittenMarkers on the fake: it records every error it
// is asked to render, writes nothing for one marking the response written
// outside a recovered panic, and otherwise writes the error's resolved
// status (the fake applies no rules, so a recovered panic is not pinned at
// 500; the panic values below name no status).
func TestFakeHandler_RenderResponseWrittenMarkers(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		recovered  bool
		wantStatus int // 0: nothing written
	}{
		{name: "BareSentinel", err: contract.ErrResponseWritten},
		{name: "Handled", err: contract.Handled(errors.New("rendered by middleware"))},
		{name: "HandledClientError", err: contract.Handled(NotFound())},
		{name: "WrappedSentinel", err: fmt.Errorf("mw: %w", contract.ErrResponseWritten)},
		{name: "HandledAroundPanic", err: contract.Handled(panicerr.FromRecovered("boom")), recovered: true},
		{name: "PanicCarryingSentinel", err: panicerr.FromRecovered(contract.ErrResponseWritten), recovered: true, wantStatus: http.StatusInternalServerError},
		{name: "PanicCarryingHandled", err: panicerr.FromRecovered(contract.Handled(errors.New("rendered"))), recovered: true, wantStatus: http.StatusInternalServerError},
		{name: "RecoveredNoPanicNode", err: contract.ErrResponseWritten, recovered: true, wantStatus: http.StatusInternalServerError},
		{name: "Unmarked", err: NotFound(), wantStatus: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := NewFakeHandler()
			rc, w := newRC(http.MethodGet, "/x", "Accept", "application/json")
			ctx := NewErrorContext()
			ctx.Recovered = tt.recovered
			f.Render(rc, tt.err, ctx)
			if got := f.RenderedErrors(); len(got) != 1 || got[0] != tt.err {
				t.Errorf("Rendered = %v, want [%v]", got, tt.err)
			}
			if got := f.ReportedErrors(); len(got) != 0 {
				t.Errorf("Reported = %v, want none", got)
			}
			if tt.wantStatus == 0 {
				if rc.Written() || w.Body.Len() != 0 {
					t.Errorf("wrote %d %q, want nothing", w.Code, w.Body.String())
				}
				return
			}
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}
