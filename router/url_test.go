package router

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestRouteV2_BasicURLGeneration(t *testing.T) {
	t.Run("generates URL for static route", func(t *testing.T) {
		router := NewV2()
		router.Get("/about", dummyHandler).Name("about")

		// Trigger route commit
		req := httptest.NewRequest("GET", "/about", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		url, err := router.RouteURL("about", nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if url != "/about" {
			t.Errorf("expected '/about', got %q", url)
		}
	})

	t.Run("generates URL with single parameter", func(t *testing.T) {
		router := NewV2()
		router.Get("/users/{id}", dummyHandler).Name("users.show")

		// Trigger route commit
		req := httptest.NewRequest("GET", "/users/1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		url, err := router.RouteURL("users.show", map[string]string{"id": "42"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if url != "/users/42" {
			t.Errorf("expected '/users/42', got %q", url)
		}
	})

	t.Run("generates URL with multiple parameters", func(t *testing.T) {
		router := NewV2()
		router.Get("/users/{userId}/posts/{postId}", dummyHandler).Name("users.posts.show")

		// Trigger route commit
		req := httptest.NewRequest("GET", "/users/1/posts/1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		url, err := router.RouteURL("users.posts.show", map[string]string{
			"userId": "5",
			"postId": "99",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if url != "/users/5/posts/99" {
			t.Errorf("expected '/users/5/posts/99', got %q", url)
		}
	})
}

func TestRouteV2_RegexConstraints(t *testing.T) {
	t.Run("generates URL for regex-constrained route", func(t *testing.T) {
		router := NewV2()
		router.Get("/posts/{id:[0-9]+}", dummyHandler).Name("posts.show")

		// Trigger route commit
		req := httptest.NewRequest("GET", "/posts/1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		url, err := router.RouteURL("posts.show", map[string]string{"id": "123"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if url != "/posts/123" {
			t.Errorf("expected '/posts/123', got %q", url)
		}
	})
}

func TestRouteV2_GroupedRoutes(t *testing.T) {
	t.Run("generates URL for grouped route", func(t *testing.T) {
		router := NewV2()
		router.Group("/api", func(g Router) {
			g.Group("/v1", func(g Router) {
				g.Get("/users/{id}", dummyHandler).Name("api.users.show")
			})
		})

		// Trigger route commit
		req := httptest.NewRequest("GET", "/api/v1/users/1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		url, err := router.RouteURL("api.users.show", map[string]string{"id": "42"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if url != "/api/v1/users/42" {
			t.Errorf("expected '/api/v1/users/42', got %q", url)
		}
	})
}

func TestRouteV2_Errors(t *testing.T) {
	t.Run("returns error for non-existent route", func(t *testing.T) {
		router := NewV2()
		router.Get("/users", dummyHandler).Name("users.index")

		// Trigger route commit
		req := httptest.NewRequest("GET", "/users", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		_, err := router.RouteURL("nonexistent", nil)
		if err == nil {
			t.Error("expected error for non-existent route")
		}

		routeErr, ok := err.(*RouteNotFoundError)
		if !ok {
			t.Errorf("expected RouteNotFoundError, got %T", err)
		}
		if routeErr.Name != "nonexistent" {
			t.Errorf("expected route name 'nonexistent', got %q", routeErr.Name)
		}
	})

	t.Run("returns error for missing required parameter", func(t *testing.T) {
		router := NewV2()
		router.Get("/users/{id}", dummyHandler).Name("users.show")

		// Trigger route commit
		req := httptest.NewRequest("GET", "/users/1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		_, err := router.RouteURL("users.show", nil) // Missing "id" param
		if err == nil {
			t.Error("expected error for missing parameter")
		}
	})

	t.Run("returns error for missing one of multiple parameters", func(t *testing.T) {
		router := NewV2()
		router.Get("/users/{userId}/posts/{postId}", dummyHandler).Name("users.posts.show")

		// Trigger route commit
		req := httptest.NewRequest("GET", "/users/1/posts/1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		_, err := router.RouteURL("users.posts.show", map[string]string{"userId": "5"}) // Missing postId
		if err == nil {
			t.Error("expected error for missing parameter")
		}
	})
}

func TestRouteV2_WildcardRoutes(t *testing.T) {
	t.Run("generates URL with wildcard parameter", func(t *testing.T) {
		router := NewV2()
		router.Get("/files/{path:.*}", dummyHandler).Name("files.show")

		// Trigger route commit
		req := httptest.NewRequest("GET", "/files/docs/readme.md", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		url, err := router.RouteURL("files.show", map[string]string{"path": "images/logo.png"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if url != "/files/images/logo.png" {
			t.Errorf("expected '/files/images/logo.png', got %q", url)
		}
	})
}

func TestRouteV2_EmptyParams(t *testing.T) {
	t.Run("generates static URL with empty params map", func(t *testing.T) {
		router := NewV2()
		router.Get("/health", dummyHandler).Name("health")

		// Trigger route commit
		req := httptest.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		url, err := router.RouteURL("health", map[string]string{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if url != "/health" {
			t.Errorf("expected '/health', got %q", url)
		}
	})
}

func TestRouteV2_BeforeCommit(t *testing.T) {
	t.Run("returns error if routes not committed", func(t *testing.T) {
		router := NewV2()
		router.Get("/users/{id}", dummyHandler).Name("users.show")
		// Not triggering ServeHTTP, routes not committed

		_, err := router.RouteURL("users.show", map[string]string{"id": "42"})
		if err == nil {
			t.Error("expected error before routes committed")
		}
	})
}

func TestCurrentRouteV2(t *testing.T) {
	t.Run("returns route name from request context", func(t *testing.T) {
		router := NewV2()
		var capturedName string
		router.Get("/users/{id}", func(ctx *Context) error {
			capturedName = GetRouteName(ctx.Request)
			return ctx.String(http.StatusOK, "ok")
		}).Name("users.show")

		req := httptest.NewRequest("GET", "/users/42", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if capturedName != "users.show" {
			t.Errorf("expected route name 'users.show', got %q", capturedName)
		}
	})

	t.Run("returns empty string for unnamed route", func(t *testing.T) {
		router := NewV2()
		var capturedName string
		router.Get("/health", func(ctx *Context) error {
			capturedName = GetRouteName(ctx.Request)
			return ctx.String(http.StatusOK, "ok")
		})

		req := httptest.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if capturedName != "" {
			t.Errorf("expected empty route name, got %q", capturedName)
		}
	})
}
