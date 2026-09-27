package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/trace"
)

// TestMatchedRoute_OverridesWin guards the last-writer-wins contract for the
// bundled per-route context: a handler/middleware that overrides route
// metadata AFTER the route matched must win over the bundled routeData,
// exactly as on the unbundled path. Regression test for the adversarial
// review finding that the bundle was consulted before the override layer.
func TestMatchedRoute_OverridesWin(t *testing.T) {
	var gotName, gotPattern string
	var gotParams map[string]string

	r := NewV2()
	r.Use(func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			// Layer overrides above the matched routeData.
			c.Request = SetRouteName(c.Request, "overridden.name")
			c.Request = SetParams(c.Request, map[string]string{"id": "override"})
			c.Request = c.Request.WithContext(
				context.WithValue(c.Request.Context(), RoutePatternKey, "/override/pattern"),
			)
			return next(c)
		}
	})
	r.Get("/u/{id}", func(c *Context) error {
		gotName = GetRouteName(c.Request)
		gotParams = GetParams(c.Request)
		gotPattern = GetRoutePattern(c.Request)
		return c.String(http.StatusOK, "ok")
	}).Name("original.name")

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/u/42", nil))

	if gotName != "overridden.name" {
		t.Errorf("route name override dropped: got %q want %q", gotName, "overridden.name")
	}
	if gotParams["id"] != "override" {
		t.Errorf("params override dropped: got %q want %q", gotParams["id"], "override")
	}
	if gotPattern != "/override/pattern" {
		t.Errorf("route pattern override dropped: got %q want %q", gotPattern, "/override/pattern")
	}
}

// TestMatchedRoute_BundleWhenNoOverride confirms the common no-override path
// still resolves the bundled match metadata.
func TestMatchedRoute_BundleWhenNoOverride(t *testing.T) {
	var gotName, gotPattern string
	var gotParams map[string]string

	r := NewV2()
	r.Get("/u/{id}", func(c *Context) error {
		gotName = GetRouteName(c.Request)
		gotParams = GetParams(c.Request)
		gotPattern = GetRoutePattern(c.Request)
		return c.String(http.StatusOK, "ok")
	}).Name("original.name")

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/u/42", nil))

	if gotName != "original.name" {
		t.Errorf("bundled route name: got %q want %q", gotName, "original.name")
	}
	if gotParams["id"] != "42" {
		t.Errorf("bundled params: got %q want %q", gotParams["id"], "42")
	}
	if gotPattern != "/u/{id}" {
		t.Errorf("bundled route pattern: got %q want %q", gotPattern, "/u/{id}")
	}
}

// TestRequestID_ReadableThroughTrace guards the one request id key: code
// that holds only the request context (httpclient, the gRPC client
// interceptors) reads the router's request id through trace.GetRequestID,
// the same id GetRequestID returns.
func TestRequestID_ReadableThroughTrace(t *testing.T) {
	var fromTrace, fromRouter string

	r := NewV2()
	r.Get("/x", func(c *Context) error {
		fromTrace = trace.GetRequestID(c.Request.Context())
		fromRouter = GetRequestID(c.Request)
		return c.String(http.StatusOK, "ok")
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	if fromTrace == "" {
		t.Fatal("trace.GetRequestID returned empty inside a handler")
	}
	if fromTrace != fromRouter {
		t.Errorf("trace.GetRequestID = %q, GetRequestID = %q, want one id", fromTrace, fromRouter)
	}
}
