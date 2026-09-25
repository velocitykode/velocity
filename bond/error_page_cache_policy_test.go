package bond_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/problem"
	"github.com/velocitykode/velocity/problem/routerbridge"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/view"
)

// This is an external test package (bond_test) so bond is wired the way an
// app wires it: through the view engine (which imports bond), with the
// error pipeline (a problem.Handler) installed on the router's error
// boundary by routerbridge and the view engine as its error page renderer.

// TestMiddlewareFunc_ErrorPageKeepsRestrictiveCacheControlSetBeforeBond:
// an app middleware that runs before bond marks every response
// "Cache-Control: private, no-store" because its pages carry per-user
// shared props. A route fails with problem.NotFound() and the error
// pipeline answers with the configured error component at 404, shared
// props included. That personalized 404 must keep the app's directive
// however the page is requested. Without it a 404 is heuristically
// cacheable (RFC 9111 section 4.2.2) and Vary: X-Inertia does not separate
// users, so a shared cache may store one user's error page and serve it to
// another.
//
// The first two cases are controls: a successful Inertia render and a
// full-page visit to the failing route. The third is the Inertia visit to
// the failing route: bond buffers it and the handler errors without
// writing, so bond commits the handler's headers for the error page to
// ride on, and a caching header set before bond must come through that
// commit as it was set.
func TestMiddlewareFunc_ErrorPageKeepsRestrictiveCacheControlSetBeforeBond(t *testing.T) {
	const (
		directive = "private, no-store"
		user      = "alice@example.com"
	)

	engine, err := view.NewEngine(view.Config{Version: "v1", ErrorPage: "Error"})
	if err != nil {
		t.Fatalf("view.NewEngine: %v", err)
	}
	// A per-request, user-specific shared prop, as apps share auth.user.
	engine.ShareFunc("auth", func(*http.Request) (interface{}, error) {
		return map[string]any{"user": user}, nil
	})

	errorHandler := problem.NewHandler()
	errorHandler.SetDebug(false) // production: the error component, not the debug page
	errorHandler.SetErrorPageRenderer(engine)

	rt := router.NewV2()
	routerbridge.Install(rt, routerbridge.WithHandler(func() contract.ErrorHandler { return errorHandler }))
	// The app's cache policy for its personalized pages, set before bond.
	rt.Use(func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			c.Response.Header().Set("Cache-Control", directive)
			return next(c)
		}
	})
	rt.Use(engine.Middleware())
	rt.Get("/account", func(c *router.Context) error {
		return engine.Render(c.Response, c.Request, "Account")
	})
	rt.Get("/account/orders/42", func(*router.Context) error {
		return problem.NotFound()
	})

	inertia := map[string]string{"X-Inertia": "true", "X-Inertia-Version": "v1"}
	fullPage := map[string]string{"Accept": "text/html"}
	tests := []struct {
		name          string
		path          string
		headers       map[string]string
		xhr           bool
		wantStatus    int
		wantComponent string
	}{
		{name: "InertiaSuccess", path: "/account", headers: inertia, xhr: true, wantStatus: http.StatusOK, wantComponent: "Account"},
		{name: "FullPageError", path: "/account/orders/42", headers: fullPage, wantStatus: http.StatusNotFound, wantComponent: "Error"},
		{name: "InertiaError", path: "/account/orders/42", headers: inertia, xhr: true, wantStatus: http.StatusNotFound, wantComponent: "Error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			for k, v := range tt.headers {
				req.Header.Set(k, v)
			}
			w := httptest.NewRecorder()
			rt.ServeHTTP(w, req)
			res := w.Result()
			defer res.Body.Close()

			// Preconditions: the answer is the personalized page.
			if res.StatusCode != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %q)", res.StatusCode, tt.wantStatus, w.Body.String())
			}
			page := decodeCachePolicyPage(t, w.Body.String(), tt.xhr)
			if page.Component != tt.wantComponent {
				t.Fatalf("component = %q, want %q (body %q)", page.Component, tt.wantComponent, w.Body.String())
			}
			if page.Props.Auth.User != user {
				t.Fatalf("props.auth.user = %q, want the shared %q", page.Props.Auth.User, user)
			}

			// The app's restrictive directive reaches the client.
			cacheControl := strings.Join(res.Header.Values("Cache-Control"), ", ")
			var missing []string
			for _, d := range []string{"private", "no-store"} {
				if !hasCacheDirective(cacheControl, d) {
					missing = append(missing, d)
				}
			}
			if len(missing) > 0 {
				t.Errorf("Cache-Control = %q on the %d %q page carrying props.auth.user %q, want the %q set before bond kept (missing %s); response headers: %v",
					cacheControl, res.StatusCode, page.Component, user, directive, strings.Join(missing, ", "), res.Header)
			}
		})
	}
}

// cachePolicyPage is the part of an Inertia page object the test reads.
type cachePolicyPage struct {
	Component string `json:"component"`
	Props     struct {
		Auth struct {
			User string `json:"user"`
		} `json:"auth"`
	} `json:"props"`
}

// decodeCachePolicyPage decodes the page object of an Inertia JSON body
// (xhr) or of the data-page script of a full-page HTML shell.
func decodeCachePolicyPage(t *testing.T, body string, xhr bool) cachePolicyPage {
	t.Helper()
	raw := body
	if !xhr {
		const marker = `type="application/json" data-page="app">`
		start := strings.Index(raw, marker)
		if start < 0 {
			t.Fatalf("no page script in body: %s", body)
		}
		raw = raw[start+len(marker):]
		end := strings.Index(raw, "</script>")
		if end < 0 {
			t.Fatalf("unterminated page script in body: %s", body)
		}
		raw = raw[:end]
	}
	var page cachePolicyPage
	if err := json.Unmarshal([]byte(raw), &page); err != nil {
		t.Fatalf("decode page: %v (%s)", err, raw)
	}
	return page
}

// hasCacheDirective reports whether the Cache-Control value lists the
// directive, case-insensitively and ignoring any argument.
func hasCacheDirective(cacheControl, directive string) bool {
	for _, part := range strings.Split(cacheControl, ",") {
		name, _, _ := strings.Cut(strings.TrimSpace(part), "=")
		if strings.EqualFold(name, directive) {
			return true
		}
	}
	return false
}
