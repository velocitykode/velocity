package bond_test

import (
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

// TestMiddlewareFunc_ErrorPageKeepsPrivacyPolicySetInsideBond: the app is
// wired the way the starter templates wire it: the error pipeline (a
// problem.Handler) sits on the router's error boundary through routerbridge
// with the view engine as its error page renderer, bond is the last global
// middleware, and a site-wide middleware registered before bond gives
// public pages "Cache-Control: public, max-age=3600". The account area
// marks its personalized pages "Cache-Control: private, no-store" with
// middleware attached at group or route level. The router builds a route's
// chain global, then group, then route, so that account middleware runs
// inside bond and, on an Inertia request, writes into bond's buffered
// header clone.
//
// A route in the account area fails with problem.NotFound(), or panics.
// The error pipeline answers with the configured error component, and the
// page carries the per-user shared prop auth.user. That personalized page
// must go out private and unstored however it is requested: with the
// site-wide "public, max-age=3600" instead (or with no policy at all, which
// leaves a 404 heuristically cacheable, RFC 9111 section 4.2.2) and only
// "Vary: X-Inertia", a shared cache may store one user's error page and
// serve it to another. The panic cases never commit bond's clone: the
// pipeline's own page policy covers them.
//
// The two Control cases are a successful Inertia render of an account page
// (bond flushes its buffer, clone included) and a full-page visit to the
// failing route (bond does not buffer a non-Inertia request).
func TestMiddlewareFunc_ErrorPageKeepsPrivacyPolicySetInsideBond(t *testing.T) {
	const (
		publicPolicy  = "public, max-age=3600"
		accountPolicy = "private, no-store"
		user          = "alice@example.com"
	)

	// newApp builds the app's router. sitePolicy is the Cache-Control the
	// site-wide middleware sets before bond ("" registers no such
	// middleware); routeLevel attaches the account middleware to each
	// account route instead of to the /account group.
	newApp := func(t *testing.T, sitePolicy string, routeLevel bool) *router.VelocityRouterV2 {
		t.Helper()
		engine, err := view.NewEngine(view.Config{Version: "v1", ErrorPage: "Error"})
		if err != nil {
			t.Fatalf("view.NewEngine: %v", err)
		}
		// A per-request, user-specific shared prop, as apps share auth.user.
		engine.ShareFunc("auth", func(*http.Request) (interface{}, error) {
			return map[string]any{"user": user}, nil
		})

		errorHandler := problem.NewHandler(problem.WithReporters())
		errorHandler.SetDebug(false) // production: the error component, not the debug page
		errorHandler.SetErrorPageRenderer(engine)

		rt := router.NewV2()
		routerbridge.Install(rt, routerbridge.WithHandler(func() contract.ErrorHandler { return errorHandler }))
		if sitePolicy != "" {
			// The site-wide policy for public pages, set before bond.
			rt.Use(func(next router.HandlerFunc) router.HandlerFunc {
				return func(c *router.Context) error {
					c.Response.Header().Set("Cache-Control", sitePolicy)
					return next(c)
				}
			})
		}
		rt.Use(engine.Middleware())

		// The account area's policy for its personalized pages. It runs
		// after (inside) bond: group and route middleware wrap inside the
		// global chain.
		account := func(next router.HandlerFunc) router.HandlerFunc {
			return func(c *router.Context) error {
				c.Response.Header().Set("Cache-Control", accountPolicy)
				return next(c)
			}
		}
		profile := func(c *router.Context) error {
			return engine.Render(c.Response, c.Request, "Account")
		}
		order := func(*router.Context) error {
			return problem.NotFound()
		}
		invoice := func(*router.Context) error {
			panic("invoice renderer crashed")
		}
		if routeLevel {
			rt.Get("/account/profile", profile).Use(account)
			rt.Get("/account/orders/42", order).Use(account)
			rt.Get("/account/invoices/7", invoice).Use(account)
		} else {
			rt.Group("/account", func(g router.Router) {
				g.Use(account)
				g.Get("/profile", profile)
				g.Get("/orders/42", order)
				g.Get("/invoices/7", invoice)
			})
		}
		return rt
	}

	inertia := map[string]string{"X-Inertia": "true", "X-Inertia-Version": "v1"}
	fullPage := map[string]string{"Accept": "text/html"}
	tests := []struct {
		name          string
		sitePolicy    string
		routeLevel    bool
		path          string
		headers       map[string]string
		xhr           bool
		wantStatus    int
		wantComponent string
	}{
		{name: "Control/InertiaSuccess", sitePolicy: publicPolicy, path: "/account/profile", headers: inertia, xhr: true, wantStatus: http.StatusOK, wantComponent: "Account"},
		{name: "Control/FullPageError", sitePolicy: publicPolicy, path: "/account/orders/42", headers: fullPage, wantStatus: http.StatusNotFound, wantComponent: "Error"},
		{name: "InertiaError/GroupLevel", sitePolicy: publicPolicy, path: "/account/orders/42", headers: inertia, xhr: true, wantStatus: http.StatusNotFound, wantComponent: "Error"},
		{name: "InertiaError/RouteLevel", sitePolicy: publicPolicy, routeLevel: true, path: "/account/orders/42", headers: inertia, xhr: true, wantStatus: http.StatusNotFound, wantComponent: "Error"},
		{name: "InertiaError/NoSitePolicy", path: "/account/orders/42", headers: inertia, xhr: true, wantStatus: http.StatusNotFound, wantComponent: "Error"},
		{name: "InertiaPanic/GroupLevel", sitePolicy: publicPolicy, path: "/account/invoices/7", headers: inertia, xhr: true, wantStatus: http.StatusInternalServerError, wantComponent: "Error"},
		{name: "InertiaPanic/RouteLevel", sitePolicy: publicPolicy, routeLevel: true, path: "/account/invoices/7", headers: inertia, xhr: true, wantStatus: http.StatusInternalServerError, wantComponent: "Error"},
		{name: "InertiaPanic/NoSitePolicy", path: "/account/invoices/7", headers: inertia, xhr: true, wantStatus: http.StatusInternalServerError, wantComponent: "Error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := newApp(t, tt.sitePolicy, tt.routeLevel)
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

			// The page goes out private and unstored, and nothing on the
			// response lets a shared cache store it.
			cacheControl := strings.Join(res.Header.Values("Cache-Control"), ", ")
			var violations []string
			for _, d := range []string{"private", "no-store"} {
				if !hasCacheDirective(cacheControl, d) {
					violations = append(violations, "missing "+d)
				}
			}
			for _, d := range []string{"public", "max-age"} {
				if hasCacheDirective(cacheControl, d) {
					violations = append(violations, "lists "+d)
				}
			}
			if len(violations) > 0 {
				t.Errorf("Cache-Control = %q (Vary %q) on the %d %q page carrying props.auth.user %q, want %q (%s)",
					cacheControl, strings.Join(res.Header.Values("Vary"), ", "), res.StatusCode, page.Component, user, accountPolicy, strings.Join(violations, "; "))
			}
		})
	}
}
