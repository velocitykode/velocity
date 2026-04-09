package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestVelocityRouterV2_BasicRouting(t *testing.T) {
	t.Run("handles GET request", func(t *testing.T) {
		router := NewV2()
		called := false

		router.Get("/users", func(c *Context) error {
			called = true
			return c.String(http.StatusOK, "users list")
		})

		req := httptest.NewRequest("GET", "/users", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if !called {
			t.Error("handler was not called")
		}
		if w.Code != http.StatusOK {
			t.Errorf("expected status 200, got %d", w.Code)
		}
	})

	t.Run("handles POST request", func(t *testing.T) {
		router := NewV2()
		var receivedMethod string

		router.Post("/users", func(c *Context) error {
			receivedMethod = c.Method()
			return c.NoContent()
		})

		req := httptest.NewRequest("POST", "/users", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if receivedMethod != "POST" {
			t.Errorf("expected POST, got %s", receivedMethod)
		}
	})

	t.Run("returns 404 for unregistered route", func(t *testing.T) {
		router := NewV2()
		router.Get("/users", func(c *Context) error { return nil })

		req := httptest.NewRequest("GET", "/posts", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d", w.Code)
		}
	})

	t.Run("returns 404 for wrong method", func(t *testing.T) {
		router := NewV2()
		router.Get("/users", func(c *Context) error { return nil })

		req := httptest.NewRequest("POST", "/users", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404, got %d", w.Code)
		}
	})
}

func TestVelocityRouterV2_RouteParameters(t *testing.T) {
	t.Run("extracts single parameter", func(t *testing.T) {
		router := NewV2()
		var capturedID string

		router.Get("/users/{id}", func(c *Context) error {
			capturedID = c.Param("id")
			return nil
		})

		req := httptest.NewRequest("GET", "/users/123", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if capturedID != "123" {
			t.Errorf("expected id=123, got %q", capturedID)
		}
	})

	t.Run("extracts multiple parameters", func(t *testing.T) {
		router := NewV2()
		var capturedUserID, capturedPostID string

		router.Get("/users/{userId}/posts/{postId}", func(c *Context) error {
			capturedUserID = c.Param("userId")
			capturedPostID = c.Param("postId")
			return nil
		})

		req := httptest.NewRequest("GET", "/users/1/posts/99", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if capturedUserID != "1" {
			t.Errorf("expected userId=1, got %q", capturedUserID)
		}
		if capturedPostID != "99" {
			t.Errorf("expected postId=99, got %q", capturedPostID)
		}
	})

	t.Run("handles regex constrained parameter", func(t *testing.T) {
		router := NewV2()
		var capturedID string

		router.Get("/users/{id:[0-9]+}", func(c *Context) error {
			capturedID = c.Param("id")
			return nil
		})

		// Should match numeric
		req := httptest.NewRequest("GET", "/users/456", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if capturedID != "456" {
			t.Errorf("expected id=456, got %q", capturedID)
		}

		// Should not match non-numeric
		req = httptest.NewRequest("GET", "/users/abc", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404 for non-numeric id, got %d", w.Code)
		}
	})
}

func TestVelocityRouterV2_Groups(t *testing.T) {
	t.Run("groups routes with prefix", func(t *testing.T) {
		router := NewV2()
		called := false

		router.Group("/api", func(api Router) {
			api.Get("/users", func(c *Context) error {
				called = true
				return nil
			})
		})

		req := httptest.NewRequest("GET", "/api/users", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if !called {
			t.Error("handler was not called")
		}
	})

	t.Run("nested groups", func(t *testing.T) {
		router := NewV2()
		called := false

		router.Group("/api", func(api Router) {
			api.Group("/v1", func(v1 Router) {
				v1.Get("/users", func(c *Context) error {
					called = true
					return nil
				})
			})
		})

		req := httptest.NewRequest("GET", "/api/v1/users", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if !called {
			t.Error("handler was not called")
		}
	})

	t.Run("fluent Group().Use() applies middleware", func(t *testing.T) {
		router := NewV2()
		var executionOrder []string

		authMw := func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				executionOrder = append(executionOrder, "auth")
				return next(c)
			}
		}

		router.Group("/admin", func(admin Router) {
			admin.Get("/dashboard", func(c *Context) error {
				executionOrder = append(executionOrder, "handler")
				return nil
			})
		}).Use(authMw)

		req := httptest.NewRequest("GET", "/admin/dashboard", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		expected := []string{"auth", "handler"}
		if len(executionOrder) != len(expected) {
			t.Fatalf("expected %d executions, got %d: %v", len(expected), len(executionOrder), executionOrder)
		}

		for i, exp := range expected {
			if executionOrder[i] != exp {
				t.Errorf("execution[%d]: expected %q, got %q", i, exp, executionOrder[i])
			}
		}
	})
}

func TestVelocityRouterV2_Middleware(t *testing.T) {
	t.Run("global middleware runs on all routes", func(t *testing.T) {
		router := NewV2()
		var middlewareCalled bool

		router.Use(func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				middlewareCalled = true
				return next(c)
			}
		})

		router.Get("/test", func(c *Context) error { return nil })

		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if !middlewareCalled {
			t.Error("middleware was not called")
		}
	})

	t.Run("route-specific middleware", func(t *testing.T) {
		router := NewV2()
		var executionOrder []string

		routeMw := func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				executionOrder = append(executionOrder, "route-mw")
				return next(c)
			}
		}

		router.Get("/protected", func(c *Context) error {
			executionOrder = append(executionOrder, "handler")
			return nil
		}).Use(routeMw)

		req := httptest.NewRequest("GET", "/protected", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		expected := []string{"route-mw", "handler"}
		if len(executionOrder) != len(expected) {
			t.Fatalf("expected %d executions, got %d", len(expected), len(executionOrder))
		}
	})

	t.Run("middleware execution order: global -> group -> route", func(t *testing.T) {
		router := NewV2()
		var executionOrder []string

		globalMw := func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				executionOrder = append(executionOrder, "global")
				return next(c)
			}
		}
		groupMw := func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				executionOrder = append(executionOrder, "group")
				return next(c)
			}
		}
		routeMw := func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				executionOrder = append(executionOrder, "route")
				return next(c)
			}
		}

		router.Use(globalMw)
		router.Group("/api", func(api Router) {
			api.Get("/test", func(c *Context) error {
				executionOrder = append(executionOrder, "handler")
				return nil
			}).Use(routeMw)
		}).Use(groupMw)

		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		expected := []string{"global", "group", "route", "handler"}
		if len(executionOrder) != len(expected) {
			t.Fatalf("expected %d executions, got %d: %v", len(expected), len(executionOrder), executionOrder)
		}
	})
}

func TestVelocityRouterV2_AnyAndMatch(t *testing.T) {
	t.Run("Any matches all methods", func(t *testing.T) {
		router := NewV2()
		callCount := 0

		router.Any("/anything", func(c *Context) error {
			callCount++
			return nil
		})

		methods := []string{"GET", "POST", "PUT", "DELETE", "PATCH"}
		for _, method := range methods {
			req := httptest.NewRequest(method, "/anything", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("%s /anything: expected 200, got %d", method, w.Code)
			}
		}

		if callCount != len(methods) {
			t.Errorf("expected %d calls, got %d", len(methods), callCount)
		}
	})

	t.Run("Match specific methods", func(t *testing.T) {
		router := NewV2()

		router.Match([]string{"GET", "POST"}, "/resource", func(c *Context) error {
			return nil
		})

		// GET should work
		req := httptest.NewRequest("GET", "/resource", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("GET /resource: expected 200, got %d", w.Code)
		}

		// POST should work
		req = httptest.NewRequest("POST", "/resource", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("POST /resource: expected 200, got %d", w.Code)
		}

		// DELETE should not work
		req = httptest.NewRequest("DELETE", "/resource", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusNotFound {
			t.Errorf("DELETE /resource: expected 404, got %d", w.Code)
		}
	})
}

func TestVelocityRouterV2_NamedRoutes(t *testing.T) {
	t.Run("stores route name", func(t *testing.T) {
		router := NewV2()
		var capturedRouteName string

		router.Get("/users/{id}", func(c *Context) error {
			capturedRouteName = GetRouteName(c.Request)
			return nil
		}).Name("users.show")

		req := httptest.NewRequest("GET", "/users/123", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if capturedRouteName != "users.show" {
			t.Errorf("expected route name 'users.show', got %q", capturedRouteName)
		}
	})
}

func TestVelocityRouterV2_StaticFiles(t *testing.T) {
	t.Run("serves static files", func(t *testing.T) {
		// This test would require actual files, so we just verify Static() doesn't panic
		router := NewV2()
		router.Static("./testdata")

		if !router.staticEnabled {
			t.Error("static serving not enabled")
		}
	})
}

func TestVelocityRouterV2_ErrorHandling(t *testing.T) {
	t.Run("handler error returns 500", func(t *testing.T) {
		router := NewV2()

		router.Get("/error", func(c *Context) error {
			return http.ErrBodyNotAllowed // Any error
		})

		req := httptest.NewRequest("GET", "/error", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500, got %d", w.Code)
		}
	})
}

func TestVelocityRouterV2_AllHTTPMethods(t *testing.T) {
	// Test that all HTTP methods route correctly to their handlers
	// This is important for REST APIs where each method has semantic meaning

	t.Run("PUT updates a resource", func(t *testing.T) {
		router := NewV2()
		var receivedMethod string

		router.Put("/users/{id}", func(c *Context) error {
			receivedMethod = c.Method()
			return c.String(http.StatusOK, "updated")
		})

		req := httptest.NewRequest("PUT", "/users/123", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if receivedMethod != "PUT" {
			t.Errorf("expected PUT, got %s", receivedMethod)
		}
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})

	t.Run("DELETE removes a resource", func(t *testing.T) {
		router := NewV2()
		var capturedID string

		router.Delete("/users/{id}", func(c *Context) error {
			capturedID = c.Param("id")
			return c.NoContent()
		})

		req := httptest.NewRequest("DELETE", "/users/456", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if capturedID != "456" {
			t.Errorf("expected id=456, got %q", capturedID)
		}
		if w.Code != http.StatusNoContent {
			t.Errorf("expected 204, got %d", w.Code)
		}
	})

	t.Run("PATCH partially updates a resource", func(t *testing.T) {
		router := NewV2()
		called := false

		router.Patch("/users/{id}", func(c *Context) error {
			called = true
			return c.String(http.StatusOK, "patched")
		})

		req := httptest.NewRequest("PATCH", "/users/789", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if !called {
			t.Error("PATCH handler was not called")
		}
	})

	t.Run("OPTIONS returns allowed methods", func(t *testing.T) {
		router := NewV2()

		router.Options("/api/resource", func(c *Context) error {
			c.SetHeader("Allow", "GET, POST, OPTIONS")
			return c.NoContent()
		})

		req := httptest.NewRequest("OPTIONS", "/api/resource", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Header().Get("Allow") != "GET, POST, OPTIONS" {
			t.Errorf("expected Allow header, got %q", w.Header().Get("Allow"))
		}
	})

	t.Run("HEAD returns headers without body", func(t *testing.T) {
		router := NewV2()

		router.Head("/files/{name}", func(c *Context) error {
			c.SetHeader("Content-Length", "1024")
			c.SetHeader("Content-Type", "application/octet-stream")
			return c.NoContent()
		})

		req := httptest.NewRequest("HEAD", "/files/document.pdf", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Header().Get("Content-Type") != "application/octet-stream" {
			t.Error("HEAD should return headers")
		}
	})
}

func TestVelocityRouterV2_GroupHTTPMethods(t *testing.T) {
	// Test that groups correctly route all HTTP methods

	t.Run("group routes all methods correctly", func(t *testing.T) {
		router := NewV2()
		results := make(map[string]bool)

		router.Group("/api/v1", func(api Router) {
			api.Get("/items", func(c *Context) error {
				results["GET"] = true
				return nil
			})
			api.Post("/items", func(c *Context) error {
				results["POST"] = true
				return nil
			})
			api.Put("/items/{id}", func(c *Context) error {
				results["PUT"] = true
				return nil
			})
			api.Delete("/items/{id}", func(c *Context) error {
				results["DELETE"] = true
				return nil
			})
			api.Patch("/items/{id}", func(c *Context) error {
				results["PATCH"] = true
				return nil
			})
		})

		tests := []struct {
			method string
			path   string
		}{
			{"GET", "/api/v1/items"},
			{"POST", "/api/v1/items"},
			{"PUT", "/api/v1/items/1"},
			{"DELETE", "/api/v1/items/1"},
			{"PATCH", "/api/v1/items/1"},
		}

		for _, tt := range tests {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("%s %s: expected 200, got %d", tt.method, tt.path, w.Code)
			}
			if !results[tt.method] {
				t.Errorf("%s handler was not called", tt.method)
			}
		}
	})
}

func TestVelocityRouterV2_Handle(t *testing.T) {
	t.Run("Handle returns router as http.Handler", func(t *testing.T) {
		router := NewV2()
		router.Get("/test", func(c *Context) error {
			return c.String(http.StatusOK, "ok")
		})

		handler := router.Handle()
		if handler == nil {
			t.Fatal("Handle() returned nil")
		}

		// Verify it works as http.Handler
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})
}

func TestVelocityRouterV2_Prefix(t *testing.T) {
	t.Run("Prefix adds prefix to all routes", func(t *testing.T) {
		router := NewV2()
		router.Prefix("/api/v2")
		router.Get("/users", func(c *Context) error {
			return c.String(http.StatusOK, "users")
		})

		req := httptest.NewRequest("GET", "/api/v2/users", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})
}

func TestVelocityRouterV2_WildcardRoutes(t *testing.T) {
	t.Run("wildcard captures entire remaining path", func(t *testing.T) {
		router := NewV2()
		var capturedPath string

		router.Get("/files/{filepath:.*}", func(c *Context) error {
			capturedPath = c.Param("filepath")
			return nil
		})

		req := httptest.NewRequest("GET", "/files/documents/reports/2024/q1.pdf", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		expected := "documents/reports/2024/q1.pdf"
		if capturedPath != expected {
			t.Errorf("expected %q, got %q", expected, capturedPath)
		}
	})

	t.Run("wildcard matches empty path", func(t *testing.T) {
		router := NewV2()
		var capturedPath string

		router.Get("/proxy/{path:.*}", func(c *Context) error {
			capturedPath = c.Param("path")
			return c.String(http.StatusOK, "proxied")
		})

		req := httptest.NewRequest("GET", "/proxy/", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
		if capturedPath != "" {
			t.Errorf("expected empty path, got %q", capturedPath)
		}
	})
}

func TestVelocityRouterV2_GroupMethodsComplete(t *testing.T) {
	// Test all HTTP methods available on groups

	t.Run("group Options method", func(t *testing.T) {
		router := NewV2()
		called := false

		router.Group("/api", func(api Router) {
			api.Options("/cors", func(c *Context) error {
				called = true
				c.SetHeader("Access-Control-Allow-Origin", "*")
				return c.NoContent()
			})
		})

		req := httptest.NewRequest("OPTIONS", "/api/cors", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if !called {
			t.Error("OPTIONS handler was not called")
		}
	})

	t.Run("group Head method", func(t *testing.T) {
		router := NewV2()
		called := false

		router.Group("/api", func(api Router) {
			api.Head("/status", func(c *Context) error {
				called = true
				c.SetHeader("X-Status", "healthy")
				return c.NoContent()
			})
		})

		req := httptest.NewRequest("HEAD", "/api/status", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if !called {
			t.Error("HEAD handler was not called")
		}
	})

	t.Run("group Resource method", func(t *testing.T) {
		router := NewV2()
		controller := NewTestResourceController()

		router.Group("/api/v1", func(api Router) {
			api.Resource("/products", controller)
		})

		// Test Index
		req := httptest.NewRequest("GET", "/api/v1/products", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200 for index, got %d", w.Code)
		}
		if w.Body.String() != "products-index" {
			t.Errorf("expected 'products-index', got %q", w.Body.String())
		}
	})

	t.Run("group Handle returns router", func(t *testing.T) {
		router := NewV2()
		var groupHandler http.Handler

		router.Group("/api", func(api Router) {
			groupHandler = api.Handle()
		})

		if groupHandler == nil {
			t.Error("group Handle() should return router")
		}
	})

	t.Run("group ServeHTTP delegates to router", func(t *testing.T) {
		router := NewV2()
		called := false

		g := router.Group("/api", func(api Router) {
			api.Get("/test", func(c *Context) error {
				called = true
				return nil
			})
		})

		// ServeHTTP on group should work same as router
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		g.ServeHTTP(w, req)

		if !called {
			t.Error("handler should be called via group ServeHTTP")
		}
	})
}

func TestVelocityRouterV2_GroupPrefix(t *testing.T) {
	t.Run("Prefix on group is no-op by design", func(t *testing.T) {
		router := NewV2()
		called := false

		router.Group("/api", func(api Router) {
			// Calling Prefix on group does nothing - prefix is already set
			api.Prefix("/ignored")
			api.Get("/test", func(c *Context) error {
				called = true
				return nil
			})
		})

		// Route should still be at /api/test, not /api/ignored/test
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if !called {
			t.Error("handler should be called at /api/test")
		}
	})
}

// TestResourceController for group Resource tests
type TestResourceController struct{}

func NewTestResourceController() *TestResourceController {
	return &TestResourceController{}
}

func (c *TestResourceController) Index(ctx *Context) error {
	return ctx.String(http.StatusOK, "products-index")
}

func (c *TestResourceController) Show(ctx *Context) error {
	return ctx.String(http.StatusOK, "products-show")
}

func TestVelocityRouterV2_StaticFileServing(t *testing.T) {
	t.Run("static file serving with non-existent file", func(t *testing.T) {
		router := NewV2()
		router.Static("./testdata")
		router.Get("/api", func(c *Context) error {
			return c.String(http.StatusOK, "api")
		})

		// Should fall through to routes when file doesn't exist
		req := httptest.NewRequest("GET", "/api", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})

	t.Run("static file serving directory is skipped", func(t *testing.T) {
		router := NewV2()
		router.Static(".") // Current directory
		router.Get("/pkg", func(c *Context) error {
			return c.String(http.StatusOK, "pkg route")
		})

		// /pkg is a directory, should fall through to route
		req := httptest.NewRequest("GET", "/pkg", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Should match the route, not serve directory
		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})

	t.Run("serves actual static file", func(t *testing.T) {
		router := NewV2()
		router.Static("./testdata")

		// Request the actual test file
		req := httptest.NewRequest("GET", "/test.txt", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "test content") {
			t.Errorf("expected file content, got %q", w.Body.String())
		}
	})
}

func TestVelocityRouterV2_AnyFallback(t *testing.T) {
	t.Run("ANY route used as fallback when method not matched", func(t *testing.T) {
		router := NewV2()
		router.Any("/fallback", func(c *Context) error {
			return c.String(http.StatusOK, "any")
		})

		// TRACE method should match ANY
		req := httptest.NewRequest("TRACE", "/fallback", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})
}

func TestVelocityRouterV2_ContextPooling(t *testing.T) {
	t.Run("context is reused from pool", func(t *testing.T) {
		router := NewV2()
		var contexts []*Context

		router.Get("/pool", func(c *Context) error {
			contexts = append(contexts, c)
			return c.String(http.StatusOK, "ok")
		})

		// Make multiple requests
		for i := 0; i < 10; i++ {
			req := httptest.NewRequest("GET", "/pool", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("request %d: expected 200, got %d", i, w.Code)
			}
		}

		// After reset+return to pool, contexts should be reused
		// We can't guarantee exact reuse count, but verify no crash
		if len(contexts) != 10 {
			t.Errorf("expected 10 handler calls, got %d", len(contexts))
		}
	})

	t.Run("context values are cleared between requests", func(t *testing.T) {
		router := NewV2()
		callCount := 0

		router.Get("/clean", func(c *Context) error {
			callCount++
			if callCount == 1 {
				c.Set("key", "value")
			} else {
				if v := c.Get("key"); v != nil {
					t.Error("context value should be cleared between requests")
				}
			}
			return c.String(http.StatusOK, "ok")
		})

		for i := 0; i < 2; i++ {
			req := httptest.NewRequest("GET", "/clean", nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
		}
	})

	t.Run("params are cleared between requests", func(t *testing.T) {
		router := NewV2()
		callCount := 0

		router.Get("/users/{id}", func(c *Context) error {
			callCount++
			id := c.Param("id")
			if callCount == 1 && id != "123" {
				t.Errorf("expected id=123, got %q", id)
			}
			if callCount == 2 && id != "456" {
				t.Errorf("expected id=456, got %q", id)
			}
			return c.String(http.StatusOK, "ok")
		})

		req := httptest.NewRequest("GET", "/users/123", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		req = httptest.NewRequest("GET", "/users/456", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)
	})
}

func TestVelocityRouterV2_RouteFreeze(t *testing.T) {
	t.Run("routes registered before serve work", func(t *testing.T) {
		router := NewV2()
		router.Get("/test", func(c *Context) error {
			return c.String(http.StatusOK, "ok")
		})

		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
	})

	t.Run("frozen flag set after first request", func(t *testing.T) {
		router := NewV2()
		router.Get("/test", func(c *Context) error {
			return nil
		})

		if router.frozen {
			t.Error("router should not be frozen before first request")
		}

		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if !router.frozen {
			t.Error("router should be frozen after first request")
		}
	})
}

func TestVelocityRouterV2_CustomErrorHandler(t *testing.T) {
	t.Run("custom error handler called for handler error", func(t *testing.T) {
		router := NewV2()
		var capturedErr error

		router.ErrorHandler = func(ctx *Context, err error) {
			capturedErr = err
			ctx.Response.WriteHeader(http.StatusBadGateway)
			ctx.Response.Write([]byte("custom error"))
		}

		router.Get("/error", func(c *Context) error {
			return NewHTTPError(http.StatusBadRequest, "bad input")
		})

		req := httptest.NewRequest("GET", "/error", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if capturedErr == nil {
			t.Fatal("expected error handler to be called")
		}
		he, ok := capturedErr.(*HTTPError)
		if !ok {
			t.Fatal("expected *HTTPError")
		}
		if he.Code != http.StatusBadRequest {
			t.Errorf("expected code 400, got %d", he.Code)
		}
	})

	t.Run("default error handler returns 500", func(t *testing.T) {
		router := NewV2()

		router.Get("/error", func(c *Context) error {
			return http.ErrBodyNotAllowed
		})

		req := httptest.NewRequest("GET", "/error", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusInternalServerError {
			t.Errorf("expected 500, got %d", w.Code)
		}
	})

	t.Run("HTTPError uses its code when no custom handler", func(t *testing.T) {
		router := NewV2()

		router.Get("/forbidden", func(c *Context) error {
			return NewHTTPError(http.StatusForbidden, "not allowed")
		})

		req := httptest.NewRequest("GET", "/forbidden", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusForbidden {
			t.Errorf("expected 403, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "not allowed") {
			t.Errorf("expected error message in body, got %q", w.Body.String())
		}
	})

	t.Run("custom error handler called on panic", func(t *testing.T) {
		router := NewV2()
		var capturedErr error

		router.ErrorHandler = func(ctx *Context, err error) {
			capturedErr = err
			ctx.Response.WriteHeader(http.StatusInternalServerError)
			ctx.Response.Write([]byte("panic handled"))
		}

		router.Get("/panic", func(c *Context) error {
			panic("test panic")
		})

		req := httptest.NewRequest("GET", "/panic", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if capturedErr == nil {
			t.Fatal("expected error handler to be called on panic")
		}
	})
}

func TestVelocityRouterV2_TrustedProxies(t *testing.T) {
	t.Run("IP uses X-Forwarded-For when proxy is trusted", func(t *testing.T) {
		router := NewV2()
		router.TrustedProxies = []string{"10.0.0.1"}

		var capturedIP string
		router.Get("/ip", func(c *Context) error {
			capturedIP = c.IP()
			return nil
		})

		req := httptest.NewRequest("GET", "/ip", nil)
		req.RemoteAddr = "10.0.0.1:8080"
		req.Header.Set("X-Forwarded-For", "203.0.113.50, 10.0.0.1")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if capturedIP != "203.0.113.50" {
			t.Errorf("expected IP '203.0.113.50', got %q", capturedIP)
		}
	})

	t.Run("IP ignores X-Forwarded-For when proxy is not trusted", func(t *testing.T) {
		router := NewV2()
		// No trusted proxies configured

		var capturedIP string
		router.Get("/ip", func(c *Context) error {
			capturedIP = c.IP()
			return nil
		})

		req := httptest.NewRequest("GET", "/ip", nil)
		req.RemoteAddr = "192.168.1.1:8080"
		req.Header.Set("X-Forwarded-For", "203.0.113.50")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if capturedIP != "192.168.1.1" {
			t.Errorf("expected IP '192.168.1.1', got %q", capturedIP)
		}
	})

	t.Run("IP returns first non-trusted from chain", func(t *testing.T) {
		router := NewV2()
		router.TrustedProxies = []string{"10.0.0.1", "10.0.0.2"}

		var capturedIP string
		router.Get("/ip", func(c *Context) error {
			capturedIP = c.IP()
			return nil
		})

		req := httptest.NewRequest("GET", "/ip", nil)
		req.RemoteAddr = "10.0.0.1:8080"
		req.Header.Set("X-Forwarded-For", "1.1.1.1, 10.0.0.2, 10.0.0.1")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if capturedIP != "1.1.1.1" {
			t.Errorf("expected IP '1.1.1.1', got %q", capturedIP)
		}
	})

	t.Run("IP returns RemoteAddr when all XFF IPs are trusted", func(t *testing.T) {
		router := NewV2()
		router.TrustedProxies = []string{"10.0.0.1", "10.0.0.2"}

		var capturedIP string
		router.Get("/ip", func(c *Context) error {
			capturedIP = c.IP()
			return nil
		})

		req := httptest.NewRequest("GET", "/ip", nil)
		req.RemoteAddr = "10.0.0.1:8080"
		req.Header.Set("X-Forwarded-For", "10.0.0.2, 10.0.0.1")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if capturedIP != "10.0.0.1" {
			t.Errorf("expected IP '10.0.0.1', got %q", capturedIP)
		}
	})
}

func TestVelocityRouterV2_StaticFileOptimization(t *testing.T) {
	t.Run("static file served without double stat", func(t *testing.T) {
		router := NewV2()
		router.Static("./testdata")

		req := httptest.NewRequest("GET", "/test.txt", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "test content") {
			t.Errorf("expected test content, got %q", w.Body.String())
		}
	})

	t.Run("falls through to routes on 404 static", func(t *testing.T) {
		router := NewV2()
		router.Static("./testdata")
		router.Get("/api/data", func(c *Context) error {
			return c.String(http.StatusOK, "api response")
		})

		req := httptest.NewRequest("GET", "/api/data", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("expected 200, got %d", w.Code)
		}
		if w.Body.String() != "api response" {
			t.Errorf("expected 'api response', got %q", w.Body.String())
		}
	})
}

func TestContext_Reset(t *testing.T) {
	t.Run("reset clears all fields", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()

		ctx := &Context{
			Response:       w,
			Request:        req,
			params:         []RouteParam{{Key: "id", Value: "123"}},
			values:         map[string]interface{}{"key": "value"},
			sseStarted:     true,
			trustedProxies: []string{"10.0.0.1"},
		}

		ctx.reset()

		if ctx.Response != nil {
			t.Error("expected Response to be nil after reset")
		}
		if ctx.Request != nil {
			t.Error("expected Request to be nil after reset")
		}
		if len(ctx.params) != 0 {
			t.Error("expected params to be empty after reset")
		}
		if len(ctx.values) != 0 {
			t.Error("expected values to be empty after reset")
		}
		if ctx.services != nil {
			t.Error("expected services to be nil after reset")
		}
		if ctx.sseStarted {
			t.Error("expected sseStarted to be false after reset")
		}
		if ctx.trustedProxies != nil {
			t.Error("expected trustedProxies to be nil after reset")
		}
	})

	t.Run("reset preserves params slice capacity", func(t *testing.T) {
		ctx := &Context{
			params: make([]RouteParam, 0, 8),
			values: make(map[string]interface{}),
		}
		ctx.params = append(ctx.params, RouteParam{Key: "a", Value: "1"})
		ctx.params = append(ctx.params, RouteParam{Key: "b", Value: "2"})

		ctx.reset()

		if cap(ctx.params) < 8 {
			t.Errorf("expected params capacity preserved, got %d", cap(ctx.params))
		}
		if len(ctx.params) != 0 {
			t.Error("expected params length to be 0")
		}
	})
}

func TestContext_ParamSlice(t *testing.T) {
	t.Run("Param returns value for existing key", func(t *testing.T) {
		ctx := &Context{
			params: []RouteParam{
				{Key: "id", Value: "123"},
				{Key: "name", Value: "test"},
			},
			values: make(map[string]interface{}),
		}

		if ctx.Param("id") != "123" {
			t.Errorf("expected '123', got %q", ctx.Param("id"))
		}
		if ctx.Param("name") != "test" {
			t.Errorf("expected 'test', got %q", ctx.Param("name"))
		}
	})

	t.Run("Param returns empty for missing key", func(t *testing.T) {
		ctx := &Context{
			params: []RouteParam{{Key: "id", Value: "123"}},
			values: make(map[string]interface{}),
		}

		if ctx.Param("missing") != "" {
			t.Errorf("expected empty string, got %q", ctx.Param("missing"))
		}
	})

	t.Run("Param works with nil params", func(t *testing.T) {
		ctx := &Context{
			values: make(map[string]interface{}),
		}

		if ctx.Param("anything") != "" {
			t.Errorf("expected empty string, got %q", ctx.Param("anything"))
		}
	})
}

func TestVelocityRouterV2_CompiledRoutes(t *testing.T) {
	t.Run("static routes are compiled after first request", func(t *testing.T) {
		router := NewV2()
		router.Get("/users", func(c *Context) error {
			return c.String(http.StatusOK, "users")
		})
		router.Get("/posts", func(c *Context) error {
			return c.String(http.StatusOK, "posts")
		})

		// Before first request, compiled routes should be nil
		if router.compiledRoutes.Load() != nil {
			t.Error("compiled routes should be nil before first request")
		}

		req := httptest.NewRequest("GET", "/users", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// After first request, compiled routes should be populated
		compiled := router.compiledRoutes.Load()
		if compiled == nil {
			t.Fatal("compiled routes should be populated after first request")
		}

		if _, ok := (*compiled)["GET /users"]; !ok {
			t.Error("compiled routes should contain GET /users")
		}
		if _, ok := (*compiled)["GET /posts"]; !ok {
			t.Error("compiled routes should contain GET /posts")
		}
	})

	t.Run("compiled routes serve correct handlers", func(t *testing.T) {
		router := NewV2()
		router.Get("/health", func(c *Context) error {
			return c.String(http.StatusOK, "ok")
		})
		router.Post("/submit", func(c *Context) error {
			return c.String(http.StatusOK, "submitted")
		})

		// First request compiles routes
		req := httptest.NewRequest("GET", "/health", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK || w.Body.String() != "ok" {
			t.Errorf("expected 200/ok, got %d/%s", w.Code, w.Body.String())
		}

		// Second request should use compiled route
		req = httptest.NewRequest("POST", "/submit", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK || w.Body.String() != "submitted" {
			t.Errorf("expected 200/submitted, got %d/%s", w.Code, w.Body.String())
		}
	})

	t.Run("parameterized routes are not compiled", func(t *testing.T) {
		router := NewV2()
		router.Get("/users/{id}", func(c *Context) error {
			return c.String(http.StatusOK, c.Param("id"))
		})
		router.Get("/static-route", func(c *Context) error {
			return c.String(http.StatusOK, "static")
		})

		// Trigger compilation
		req := httptest.NewRequest("GET", "/static-route", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Static route should be compiled
		compiled := router.compiledRoutes.Load()
		if _, ok := (*compiled)["GET /static-route"]; !ok {
			t.Error("static route should be compiled")
		}

		// Parameterized route should NOT be in compiled map
		for key := range *compiled {
			if strings.Contains(key, "{id}") {
				t.Error("parameterized route should not be compiled")
			}
		}

		// But parameterized route should still work via tree
		req = httptest.NewRequest("GET", "/users/42", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK || w.Body.String() != "42" {
			t.Errorf("expected 200/42, got %d/%s", w.Code, w.Body.String())
		}
	})

	t.Run("grouped static routes are compiled", func(t *testing.T) {
		router := NewV2()
		router.Group("/api", func(api Router) {
			api.Group("/v1", func(v1 Router) {
				v1.Get("/status", func(c *Context) error {
					return c.String(http.StatusOK, "ok")
				})
			})
		})

		// Trigger compilation
		req := httptest.NewRequest("GET", "/api/v1/status", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		compiled := router.compiledRoutes.Load()
		if _, ok := (*compiled)["GET /api/v1/status"]; !ok {
			t.Error("grouped static route should be compiled")
		}
	})

	t.Run("ANY routes are compiled and served", func(t *testing.T) {
		router := NewV2()
		router.Any("/ping", func(c *Context) error {
			return c.String(http.StatusOK, "pong")
		})

		// Trigger compilation
		req := httptest.NewRequest("GET", "/ping", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		compiled := router.compiledRoutes.Load()
		if _, ok := (*compiled)["ANY /ping"]; !ok {
			t.Error("ANY route should be compiled")
		}

		// Subsequent request with different method should also work
		req = httptest.NewRequest("POST", "/ping", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK || w.Body.String() != "pong" {
			t.Errorf("expected 200/pong via compiled ANY, got %d/%s", w.Code, w.Body.String())
		}
	})
}

func TestVelocityRouterV2_ClearCompiledRoutes(t *testing.T) {
	t.Run("clears compiled cache", func(t *testing.T) {
		router := NewV2()
		router.Get("/test", func(c *Context) error {
			return c.String(http.StatusOK, "ok")
		})

		// Trigger compilation
		req := httptest.NewRequest("GET", "/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if router.compiledRoutes.Load() == nil {
			t.Fatal("compiled routes should exist")
		}

		// Clear compiled routes
		router.ClearCompiledRoutes()

		if router.compiledRoutes.Load() != nil {
			t.Error("compiled routes should be nil after clear")
		}

		// Route should still work (falls back to tree, then recompiled on next commitOnce)
		req = httptest.NewRequest("GET", "/test", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK || w.Body.String() != "ok" {
			t.Errorf("expected 200/ok after clear, got %d/%s", w.Code, w.Body.String())
		}
	})

	t.Run("does not affect route definitions", func(t *testing.T) {
		router := NewV2()
		router.Get("/a", func(c *Context) error { return c.String(http.StatusOK, "a") })
		router.Get("/b", func(c *Context) error { return c.String(http.StatusOK, "b") })

		// Trigger compilation
		req := httptest.NewRequest("GET", "/a", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		router.ClearCompiledRoutes()

		// Both routes should still work via tree
		for _, path := range []string{"/a", "/b"} {
			req = httptest.NewRequest("GET", path, nil)
			w = httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusOK {
				t.Errorf("GET %s: expected 200, got %d", path, w.Code)
			}
		}
	})
}

func TestVelocityRouterV2_ClearRoutes(t *testing.T) {
	t.Run("full reset allows re-registration", func(t *testing.T) {
		router := NewV2()
		router.Get("/old", func(c *Context) error {
			return c.String(http.StatusOK, "old")
		})

		// Trigger compilation
		req := httptest.NewRequest("GET", "/old", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Fatalf("expected 200 for /old, got %d", w.Code)
		}

		// Full reset
		router.ClearRoutes()

		if router.frozen {
			t.Error("router should not be frozen after ClearRoutes")
		}
		if router.committed {
			t.Error("router should not be committed after ClearRoutes")
		}
		if router.compiledRoutes.Load() != nil {
			t.Error("compiled routes should be nil after ClearRoutes")
		}

		// Register new routes
		router.Get("/new", func(c *Context) error {
			return c.String(http.StatusOK, "new")
		})

		// Old route should 404
		req = httptest.NewRequest("GET", "/old", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusNotFound {
			t.Errorf("expected 404 for /old after clear, got %d", w.Code)
		}

		// New route should work
		req = httptest.NewRequest("GET", "/new", nil)
		w = httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK || w.Body.String() != "new" {
			t.Errorf("expected 200/new, got %d/%s", w.Code, w.Body.String())
		}
	})

	t.Run("clears named routes", func(t *testing.T) {
		router := NewV2()
		router.Get("/users/{id}", func(c *Context) error { return nil }).Name("users.show")

		// Trigger compilation
		req := httptest.NewRequest("GET", "/users/1", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if len(router.namedRoutes) == 0 {
			t.Fatal("expected named routes to be populated")
		}

		router.ClearRoutes()

		if len(router.namedRoutes) != 0 {
			t.Error("expected named routes to be empty after ClearRoutes")
		}
	})

	t.Run("group ClearRoutes delegates to router", func(t *testing.T) {
		router := NewV2()
		var group Router

		router.Get("/root", func(c *Context) error { return nil })
		group = router.Group("/api", func(api Router) {
			api.Get("/test", func(c *Context) error { return nil })
		})

		// Trigger compilation
		req := httptest.NewRequest("GET", "/root", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		// Clear via group
		group.ClearRoutes()

		if router.committed {
			t.Error("router should not be committed after group ClearRoutes")
		}
	})

	t.Run("group ClearCompiledRoutes delegates to router", func(t *testing.T) {
		router := NewV2()
		var group Router

		group = router.Group("/api", func(api Router) {
			api.Get("/test", func(c *Context) error { return nil })
		})

		// Trigger compilation
		req := httptest.NewRequest("GET", "/api/test", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if router.compiledRoutes.Load() == nil {
			t.Fatal("compiled routes should exist")
		}

		// Clear compiled via group
		group.ClearCompiledRoutes()

		if router.compiledRoutes.Load() != nil {
			t.Error("compiled routes should be nil after group ClearCompiledRoutes")
		}
	})
}

func TestVelocityRouterV2_AllRoutes(t *testing.T) {
	t.Run("lists root-level routes", func(t *testing.T) {
		router := NewV2()
		router.Get("/", func(c *Context) error { return nil })
		router.Post("/users", func(c *Context) error { return nil }).Name("users.store")

		routes := router.AllRoutes()
		if len(routes) != 2 {
			t.Fatalf("expected 2 routes, got %d", len(routes))
		}
		if routes[0].Method != "GET" || routes[0].Path != "/" {
			t.Errorf("unexpected route[0]: %+v", routes[0])
		}
		if routes[1].Method != "POST" || routes[1].Path != "/users" || routes[1].Name != "users.store" {
			t.Errorf("unexpected route[1]: %+v", routes[1])
		}
	})

	t.Run("lists grouped routes with full path", func(t *testing.T) {
		router := NewV2()
		router.Group("/api/v1", func(api Router) {
			api.Get("/health", func(c *Context) error { return nil }).Name("api.health")
			api.Get("/users", func(c *Context) error { return nil }).Name("api.users")
		})

		routes := router.AllRoutes()
		if len(routes) != 2 {
			t.Fatalf("expected 2 routes, got %d", len(routes))
		}
		if routes[0].Path != "/api/v1/health" || routes[0].Name != "api.health" {
			t.Errorf("unexpected route[0]: %+v", routes[0])
		}
		if routes[1].Path != "/api/v1/users" || routes[1].Name != "api.users" {
			t.Errorf("unexpected route[1]: %+v", routes[1])
		}
	})

	t.Run("lists nested group routes", func(t *testing.T) {
		router := NewV2()
		router.Group("/api", func(api Router) {
			api.Group("/v1", func(v1 Router) {
				v1.Get("/posts", func(c *Context) error { return nil })
			})
		})

		routes := router.AllRoutes()
		if len(routes) != 1 {
			t.Fatalf("expected 1 route, got %d", len(routes))
		}
		if routes[0].Path != "/api/v1/posts" {
			t.Errorf("expected /api/v1/posts, got %s", routes[0].Path)
		}
	})

	t.Run("returns empty for no routes", func(t *testing.T) {
		router := NewV2()
		routes := router.AllRoutes()
		if len(routes) != 0 {
			t.Errorf("expected 0 routes, got %d", len(routes))
		}
	})
}

func TestContext_TrustedProxyIP(t *testing.T) {
	t.Run("returns remote addr when no trusted proxies", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.1:1234"
		ctx := &Context{
			Request: req,
			values:  make(map[string]interface{}),
		}

		if ip := ctx.IP(); ip != "192.168.1.1" {
			t.Errorf("expected '192.168.1.1', got %q", ip)
		}
	})

	t.Run("reads XFF when remote is trusted", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		req.Header.Set("X-Forwarded-For", "8.8.8.8")
		ctx := &Context{
			Request:        req,
			values:         make(map[string]interface{}),
			trustedProxies: []string{"10.0.0.1"},
		}

		if ip := ctx.IP(); ip != "8.8.8.8" {
			t.Errorf("expected '8.8.8.8', got %q", ip)
		}
	})

	t.Run("ignores XFF when remote is not trusted", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "192.168.1.1:1234"
		req.Header.Set("X-Forwarded-For", "8.8.8.8")
		ctx := &Context{
			Request:        req,
			values:         make(map[string]interface{}),
			trustedProxies: []string{"10.0.0.1"},
		}

		if ip := ctx.IP(); ip != "192.168.1.1" {
			t.Errorf("expected '192.168.1.1', got %q", ip)
		}
	})

	t.Run("skips trusted IPs in XFF chain", func(t *testing.T) {
		req := httptest.NewRequest("GET", "/test", nil)
		req.RemoteAddr = "10.0.0.1:1234"
		req.Header.Set("X-Forwarded-For", "203.0.113.1, 10.0.0.2")
		ctx := &Context{
			Request:        req,
			values:         make(map[string]interface{}),
			trustedProxies: []string{"10.0.0.1", "10.0.0.2"},
		}

		if ip := ctx.IP(); ip != "203.0.113.1" {
			t.Errorf("expected '203.0.113.1', got %q", ip)
		}
	})
}
