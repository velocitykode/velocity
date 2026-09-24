package problem

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/internal/panicerr"
)

func TestMiddleware_RecoversPanic(t *testing.T) {
	tests := []struct {
		name  string
		serve func(h *Handler, w http.ResponseWriter, r *http.Request)
	}{
		{"Middleware", func(h *Handler, w http.ResponseWriter, r *http.Request) {
			Middleware(h)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic(NotFound()) })).ServeHTTP(w, r)
		}},
		{"MiddlewareFunc", func(h *Handler, w http.ResponseWriter, r *http.Request) {
			MiddlewareFunc(h)(func(http.ResponseWriter, *http.Request) { panic("boom") })(w, r)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, _ := newTestHandler()
			w := httptest.NewRecorder()
			tt.serve(h, w, httptest.NewRequest(http.MethodGet, "/x", nil))
			if w.Code != http.StatusInternalServerError {
				t.Errorf("status = %d, want 500 for a panic", w.Code)
			}
			ctx, err := rep.last()
			if err == nil || panicerr.AsTyped(err) == nil {
				t.Fatalf("reported %v, want the recovered panic", err)
			}
			if !ctx.Recovered || ctx.PanicStack == "" || ctx.StackTrace == nil {
				t.Errorf("panic context incomplete: recovered=%v stack=%d", ctx.Recovered, len(ctx.PanicStack))
			}
		})
	}
}

func TestMiddleware_PassesThroughWithoutPanic(t *testing.T) {
	h, rep, _ := newTestHandler()
	w := httptest.NewRecorder()
	Middleware(h)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusNoContent || rep.count() != 0 {
		t.Errorf("got %d with %d reports", w.Code, rep.count())
	}
}

func TestMiddleware_AbortHandlerPropagates(t *testing.T) {
	h, rep, _ := newTestHandler()
	defer func() {
		p := recover()
		err, ok := p.(error)
		if !ok || !errors.Is(err, http.ErrAbortHandler) {
			t.Fatalf("recovered %v, want http.ErrAbortHandler", p)
		}
		if rep.count() != 0 {
			t.Error("ErrAbortHandler must not be reported")
		}
	}()
	Middleware(h)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))
}

func TestErrorHandler_Adapter(t *testing.T) {
	tests := []struct {
		name        string
		setup       func(h *Handler)
		path        string
		headers     []string
		err         error
		wantStatus  int
		wantContent string
	}{
		{name: "HTMLForBrowser", path: "/api/users", err: NotFound(), wantStatus: 404, wantContent: "text/html; charset=utf-8"},
		{name: "JSONContentTypeRequestIsNotJSONAccept", path: "/x", headers: []string{"Content-Type", "application/json"}, err: NotFound(), wantStatus: 404, wantContent: "text/html; charset=utf-8"},
		{name: "AcceptJSON", path: "/x", headers: []string{"Accept", "application/json"}, err: BadRequest(), wantStatus: 400, wantContent: ProblemTypeContent},
		{name: "ConfiguredPrefix", setup: func(h *Handler) { h.SetAPIPrefixes("/api/") }, path: "/api/users", err: NotFound(), wantStatus: 404, wantContent: ProblemTypeContent},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _, _ := newTestHandler()
			if tt.setup != nil {
				tt.setup(h)
			}
			r := httptest.NewRequest(http.MethodGet, tt.path, nil)
			for i := 0; i+1 < len(tt.headers); i += 2 {
				r.Header.Set(tt.headers[i], tt.headers[i+1])
			}
			w := httptest.NewRecorder()
			ErrorHandler(h)(w, r, tt.err)
			if w.Code != tt.wantStatus {
				t.Errorf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if got := w.Header().Get("Content-Type"); got != tt.wantContent {
				t.Errorf("Content-Type = %q, want %q", got, tt.wantContent)
			}
		})
	}
}

func TestErrorHandler_ClientIPUsesTrustedProxies(t *testing.T) {
	_, trusted, _ := net.ParseCIDR("192.0.2.0/24")
	tests := []struct {
		name    string
		proxies []*net.IPNet
		want    string
	}{
		{"UntrustedIgnoresHeader", nil, "192.0.2.1"},
		{"TrustedHonoursHeader", []*net.IPNet{trusted}, "203.0.113.9"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, rep, _ := newTestHandler(WithTrustedProxies(tt.proxies))
			r := httptest.NewRequest(http.MethodGet, "/x", nil)
			r.Header.Set("X-Forwarded-For", "203.0.113.9")
			ErrorHandler(h)(httptest.NewRecorder(), r, errors.New("boom"))
			ctx, _ := rep.last()
			if ctx.IP != tt.want {
				t.Errorf("IP = %q, want %q", ctx.IP, tt.want)
			}
		})
	}
}

func TestErrorHandler_AcceptsInterface(t *testing.T) {
	fake := NewFakeHandler()
	w := httptest.NewRecorder()
	ErrorHandler(fake)(w, httptest.NewRequest(http.MethodGet, "/x", nil), Forbidden())
	if w.Code != http.StatusForbidden || len(fake.ReportedErrors()) != 1 || len(fake.RenderedErrors()) != 1 {
		t.Errorf("fake handler not driven: %d %v %v", w.Code, fake.Reported, fake.Rendered)
	}
}
