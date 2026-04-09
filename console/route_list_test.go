package console

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/router"
)

func captureStdout(fn func()) string {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	fn()

	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	io.Copy(&buf, r)
	return buf.String()
}

func TestRouteList_DisplaysRoutes(t *testing.T) {
	r := router.NewV2()
	r.Get("/", func(c *router.Context) error { return nil })
	r.Get("/users", func(c *router.Context) error { return nil }).Name("users.index")
	r.Post("/users", func(c *router.Context) error { return nil }).Name("users.store")

	output := captureStdout(func() {
		RouteList(r)
	})

	if !strings.Contains(output, "GET") {
		t.Error("expected GET in output")
	}
	if !strings.Contains(output, "POST") {
		t.Error("expected POST in output")
	}
	if !strings.Contains(output, "/users") {
		t.Error("expected /users in output")
	}
	if !strings.Contains(output, "users.index") {
		t.Error("expected users.index in output")
	}
	if !strings.Contains(output, "users.store") {
		t.Error("expected users.store in output")
	}
	if !strings.Contains(output, "Showing 3 routes") {
		t.Errorf("expected 'Showing 3 routes', got: %s", output)
	}
}

func TestRouteList_GroupedRoutes(t *testing.T) {
	r := router.NewV2()
	r.Group("/api/v1", func(api router.Router) {
		api.Get("/health", func(c *router.Context) error { return nil }).Name("api.health")
		api.Get("/users", func(c *router.Context) error { return nil }).Name("api.users")
	})

	output := captureStdout(func() {
		RouteList(r)
	})

	if !strings.Contains(output, "/api/v1/health") {
		t.Error("expected /api/v1/health in output")
	}
	if !strings.Contains(output, "/api/v1/users") {
		t.Error("expected /api/v1/users in output")
	}
	if !strings.Contains(output, "Showing 2 routes") {
		t.Errorf("expected 'Showing 2 routes', got: %s", output)
	}
}

func TestRouteList_Empty(t *testing.T) {
	r := router.NewV2()

	output := captureStdout(func() {
		RouteList(r)
	})

	if !strings.Contains(output, "No routes registered") {
		t.Errorf("expected 'No routes registered', got: %s", output)
	}
}
