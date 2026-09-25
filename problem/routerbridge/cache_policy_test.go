package routerbridge

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/router"
)

// cachePolicyPage is an error page renderer that answers with a page, or
// declines having written nothing.
type cachePolicyPage struct{ decline bool }

func (p cachePolicyPage) RenderErrorPage(rc contract.RenderContext, status int, message string) (bool, error) {
	if p.decline {
		return false, nil
	}
	rc.SetHeader("Content-Type", "text/html; charset=utf-8")
	rc.WriteHeader(status)
	_, err := rc.Write([]byte("<p>" + message + "</p>"))
	return true, err
}

// failingHTMLRenderer fails every render having written nothing.
type failingHTMLRenderer struct{}

func (failingHTMLRenderer) Render(problem.RenderContext, error, *problem.ErrorContext, int, bool) error {
	return errors.New("template broke")
}

func (failingHTMLRenderer) ContentType() string { return "text/html" }

// TestInstall_ErrorAnswerCachePolicy asserts the pipeline owns the cache
// policy of the error answers that embed application or request content:
// the HTML page, the error page renderer's page and the debug page (for a
// full-page and an Inertia request) go out "private, no-store" whatever
// policy middleware left on the response, unless the error's own headers
// name a Cache-Control or a BeforeRender hook replaces it. The
// problem+json body, the Inertia 409 reload and the plain-text last resort
// set no policy and keep the one the response held.
func TestInstall_ErrorAnswerCachePolicy(t *testing.T) {
	const (
		public = "public, max-age=3600"
		page   = "private, no-store"
	)
	fail := func(*router.Context) error { return errors.New("db down") }
	inertia := map[string]string{"X-Inertia": "true"}
	html := map[string]string{"Accept": "text/html"}
	jsonAccept := map[string]string{"Accept": "application/json"}
	tests := []struct {
		name       string
		outer      string // Cache-Control set by middleware before the handler
		request    map[string]string
		handler    router.HandlerFunc
		debug      bool
		errorPage  contract.ErrorPageRenderer
		renderers  map[string]problem.Renderer
		hook       func(rc problem.RenderContext, err error, status int) int
		wantStatus int
		want       []string // Cache-Control values on the answer
	}{
		{name: "HTMLPage/OuterPublic", outer: public, request: html, handler: fail, wantStatus: http.StatusInternalServerError, want: []string{page}},
		{name: "HTMLPage/NoOuter", request: html, handler: func(*router.Context) error { return problem.NotFound() }, wantStatus: http.StatusNotFound, want: []string{page}},
		{name: "DebugPage/FullPage", outer: public, request: html, handler: fail, debug: true, wantStatus: http.StatusInternalServerError, want: []string{page}},
		{name: "DebugPage/Inertia", outer: public, request: inertia, handler: fail, debug: true, wantStatus: http.StatusInternalServerError, want: []string{page}},
		{name: "ErrorPage/FullPage", outer: public, request: html, handler: fail, errorPage: cachePolicyPage{}, wantStatus: http.StatusInternalServerError, want: []string{page}},
		{name: "ErrorPage/Inertia", outer: public, request: inertia, handler: fail, errorPage: cachePolicyPage{}, wantStatus: http.StatusInternalServerError, want: []string{page}},
		{name: "JSON/OuterNoStoreKept", outer: "no-store", request: jsonAccept, handler: fail, wantStatus: http.StatusInternalServerError, want: []string{"no-store"}},
		{name: "JSON/NoOuter", request: jsonAccept, handler: fail, wantStatus: http.StatusInternalServerError},
		{
			name: "ErrorCacheControlWins", outer: public, request: html, errorPage: cachePolicyPage{},
			handler: func(*router.Context) error {
				return contract.NewHTTPError(http.StatusServiceUnavailable).WithHeader("Cache-Control", "no-cache")
			},
			wantStatus: http.StatusServiceUnavailable, want: []string{"no-cache"},
		},
		{
			name: "HookReplacesPolicy", outer: public, request: inertia, handler: fail, errorPage: cachePolicyPage{},
			hook: func(rc problem.RenderContext, _ error, status int) int {
				rc.SetHeader("Cache-Control", "private, max-age=0")
				return status
			},
			wantStatus: http.StatusInternalServerError, want: []string{"private, max-age=0"},
		},
		{name: "InertiaReload/OuterNoStoreKept", outer: "no-store", request: inertia, handler: fail, wantStatus: http.StatusConflict, want: []string{"no-store"}},
		{name: "InertiaReload/NoOuter", request: inertia, handler: fail, wantStatus: http.StatusConflict},
		{name: "InertiaReload/ErrorPageDeclines", outer: "no-store", request: inertia, handler: fail, errorPage: cachePolicyPage{decline: true}, wantStatus: http.StatusConflict, want: []string{"no-store"}},
		{
			name: "InertiaReload/HookPolicyKept", request: inertia, handler: fail,
			hook: func(rc problem.RenderContext, _ error, status int) int {
				rc.SetHeader("Cache-Control", "no-cache")
				return status
			},
			wantStatus: http.StatusConflict, want: []string{"no-cache"},
		},
		{
			name: "LastResort/RendererFails", outer: "no-store", request: html, handler: fail,
			renderers:  map[string]problem.Renderer{"html": failingHTMLRenderer{}},
			wantStatus: http.StatusInternalServerError, want: []string{"no-store"},
		},
		{
			name: "LastResort/HookPanics", outer: public, request: html, handler: fail,
			hook:       func(problem.RenderContext, error, int) int { panic("hook broke") },
			wantStatus: http.StatusInternalServerError, want: []string{public},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := problem.NewHandler(problem.WithReporters(), problem.WithRenderers(tt.renderers), problem.WithEnvironment("testing"))
			h.SetDebug(tt.debug)
			if h.IsDebug() != tt.debug {
				t.Fatalf("IsDebug = %v, want %v", h.IsDebug(), tt.debug)
			}
			if tt.errorPage != nil {
				h.SetErrorPageRenderer(tt.errorPage)
			}
			if tt.hook != nil {
				h.BeforeRender(tt.hook)
			}
			r := router.New()
			Install(r, WithHandler(func() contract.ErrorHandler { return h }))
			if tt.outer != "" {
				r.Use(func(next router.HandlerFunc) router.HandlerFunc {
					return func(c *router.Context) error {
						c.Response.Header().Set("Cache-Control", tt.outer)
						return next(c)
					}
				})
			}
			r.Get("/account/orders/42", tt.handler)

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/account/orders/42", nil)
			for k, v := range tt.request {
				req.Header.Set(k, v)
			}
			r.ServeHTTP(w, req)
			res := w.Result()
			defer res.Body.Close()

			if res.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %.120q)", res.StatusCode, tt.wantStatus, w.Body.String())
			}
			if got := res.Header.Values("Cache-Control"); !slices.Equal(got, tt.want) {
				t.Errorf("Cache-Control = %q on the %d %s answer, want %q", got, res.StatusCode, res.Header.Get("Content-Type"), tt.want)
			}
		})
	}
}
