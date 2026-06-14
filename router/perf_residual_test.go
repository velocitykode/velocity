package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
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

// TestRequestIDKey_ResolvesToString guards the exported RequestIDKey value
// type: a consumer reading the key directly must still get a string (not the
// internal lazy holder). Regression test for the deferred-request-ID change.
func TestRequestIDKey_ResolvesToString(t *testing.T) {
	var rawType string
	var rawVal string
	var matchesAccessor bool

	r := NewV2()
	r.Get("/x", func(c *Context) error {
		v := c.Request.Context().Value(RequestIDKey)
		if s, ok := v.(string); ok {
			rawType = "string"
			rawVal = s
			matchesAccessor = s == GetRequestID(c.Request)
		}
		return c.String(http.StatusOK, "ok")
	})

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/x", nil))

	if rawType != "string" {
		t.Fatalf("RequestIDKey value is not a string (got %q)", rawType)
	}
	if rawVal == "" {
		t.Error("RequestIDKey resolved to empty string")
	}
	if !matchesAccessor {
		t.Error("raw RequestIDKey value differs from GetRequestID accessor")
	}
}
