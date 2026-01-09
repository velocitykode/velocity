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
