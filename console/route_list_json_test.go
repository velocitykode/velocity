package console

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/router"
)

func namedHandler(c *router.Context) error { return nil }

func namedMiddleware(next router.HandlerFunc) router.HandlerFunc {
	return next
}

func TestRouteListJSON_EmitsStableShape(t *testing.T) {
	r := router.NewV2()
	r.Get("/users", namedHandler).Name("users.index").Use(namedMiddleware)

	var buf bytes.Buffer
	if err := RouteListJSON(r, &buf); err != nil {
		t.Fatalf("RouteListJSON: %v", err)
	}

	var routes []map[string]any
	if err := json.Unmarshal(buf.Bytes(), &routes); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, buf.String())
	}
	if len(routes) != 1 {
		t.Fatalf("got %d routes, want 1", len(routes))
	}

	route := routes[0]
	for _, key := range []string{"method", "path", "handler", "middleware", "name"} {
		if _, ok := route[key]; !ok {
			t.Errorf("missing %q key in %v", key, route)
		}
	}
	if route["method"] != "GET" {
		t.Errorf("method: got %v want GET", route["method"])
	}
	if route["path"] != "/users" {
		t.Errorf("path: got %v want /users", route["path"])
	}
	if route["name"] != "users.index" {
		t.Errorf("name: got %v want users.index", route["name"])
	}
	if h, _ := route["handler"].(string); !strings.Contains(h, "namedHandler") {
		t.Errorf("handler: got %v want name containing namedHandler", route["handler"])
	}
	mw, _ := route["middleware"].([]any)
	if len(mw) != 1 {
		t.Fatalf("middleware: got %v want one entry", route["middleware"])
	}
	if s, _ := mw[0].(string); !strings.Contains(s, "namedMiddleware") {
		t.Errorf("middleware[0]: got %v want name containing namedMiddleware", mw[0])
	}
}

func TestRouteListJSON_EmptyIsEmptyArray(t *testing.T) {
	var buf bytes.Buffer
	if err := RouteListJSON(router.NewV2(), &buf); err != nil {
		t.Fatalf("RouteListJSON: %v", err)
	}
	if got := strings.TrimSpace(buf.String()); got != "[]" {
		t.Errorf("empty router: got %q want []", got)
	}
}
