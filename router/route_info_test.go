package router

import (
	"strings"
	"testing"
)

func routeInfoHandler(c *Context) error { return nil }

func groupMW(next HandlerFunc) HandlerFunc  { return next }
func routeMW(next HandlerFunc) HandlerFunc  { return next }
func nestedMW(next HandlerFunc) HandlerFunc { return next }

func TestAllRoutes_HandlerAndMiddlewareNames(t *testing.T) {
	r := NewV2()
	r.Group("/api", func(api Router) {
		api.Use(groupMW)
		api.Get("/users", routeInfoHandler).Use(routeMW)
		api.Group("/v2", func(v2 Router) {
			v2.Use(nestedMW)
			v2.Get("/users", routeInfoHandler)
		})
	})

	byPath := map[string]RouteInfo{}
	for _, info := range r.AllRoutes() {
		byPath[info.Path] = info
	}

	users, ok := byPath["/api/users"]
	if !ok {
		t.Fatalf("missing /api/users in %v", byPath)
	}
	if !strings.Contains(users.Handler, "routeInfoHandler") {
		t.Errorf("handler: got %q want name containing routeInfoHandler", users.Handler)
	}
	if len(users.Middleware) != 2 ||
		!strings.Contains(users.Middleware[0], "groupMW") ||
		!strings.Contains(users.Middleware[1], "routeMW") {
		t.Errorf("middleware: got %v want [groupMW routeMW]", users.Middleware)
	}

	nested, ok := byPath["/api/v2/users"]
	if !ok {
		t.Fatalf("missing /api/v2/users in %v", byPath)
	}
	if len(nested.Middleware) != 2 ||
		!strings.Contains(nested.Middleware[0], "groupMW") ||
		!strings.Contains(nested.Middleware[1], "nestedMW") {
		t.Errorf("nested middleware: got %v want [groupMW nestedMW]", nested.Middleware)
	}
}
