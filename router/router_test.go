package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
)

func TestNew(t *testing.T) {
	r := New()
	if r == nil {
		t.Fatal("New() returned nil")
	}
	if r.tree.Load() == nil {
		t.Fatal("New() router has nil tree")
	}
	if r.namedRoutes == nil {
		t.Fatal("New() router has nil namedRoutes map")
	}
}

func TestHTTPMethods(t *testing.T) {
	tests := []struct {
		name     string
		method   string
		register func(*VelocityRouterV2, string, HandlerFunc) RouteConfig
	}{
		{"GET", "GET", (*VelocityRouterV2).Get},
		{"POST", "POST", (*VelocityRouterV2).Post},
		{"PUT", "PUT", (*VelocityRouterV2).Put},
		{"DELETE", "DELETE", (*VelocityRouterV2).Delete},
		{"PATCH", "PATCH", (*VelocityRouterV2).Patch},
		{"OPTIONS", "OPTIONS", (*VelocityRouterV2).Options},
		{"HEAD", "HEAD", (*VelocityRouterV2).Head},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := New()
			called := false

			handler := func(c *Context) error {
				called = true
				c.Response.WriteHeader(http.StatusOK)
				return nil
			}

			route := tt.register(r, "/test", handler)
			if route == nil {
				t.Fatalf("%s returned nil route", tt.name)
			}

			// Test the route
			req := httptest.NewRequest(tt.method, "/test", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if !called {
				t.Errorf("%s handler was not called", tt.name)
			}

			// Test wrong method returns 404 (V2 returns 404 for method mismatch)
			wrongMethod := "GET"
			if tt.method == "GET" {
				wrongMethod = "POST"
			}

			called = false
			req = httptest.NewRequest(wrongMethod, "/test", nil)
			w = httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if called {
				t.Errorf("%s handler was called with wrong method", tt.name)
			}
		})
	}
}

func TestRouteParameters(t *testing.T) {
	r := New()
	var capturedID string

	r.Get("/users/{id}", func(c *Context) error {
		capturedID = c.Param("id")
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	req := httptest.NewRequest("GET", "/users/123", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if capturedID != "123" {
		t.Errorf("Expected captured ID to be '123', got '%s'", capturedID)
	}
}

func TestRouteParametersWithRegex(t *testing.T) {
	r := New()
	called := false

	r.Get("/users/{id:[0-9]+}", func(c *Context) error {
		called = true
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	// Test with numeric ID (should match)
	req := httptest.NewRequest("GET", "/users/123", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !called {
		t.Error("Handler was not called for numeric ID")
	}

	// Test with non-numeric ID (should not match)
	called = false
	req = httptest.NewRequest("GET", "/users/abc", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if called {
		t.Error("Handler was called for non-numeric ID")
	}
}

func TestGroup(t *testing.T) {
	r := New()
	called := false

	api := r.Group("/api")
	api.Get("/users", func(c *Context) error {
		called = true
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	req := httptest.NewRequest("GET", "/api/users", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !called {
		t.Error("Grouped route handler was not called")
	}

	// Test that non-grouped path doesn't work
	called = false
	req = httptest.NewRequest("GET", "/users", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if called {
		t.Error("Handler was called for non-grouped path")
	}
}

func TestNestedGroups(t *testing.T) {
	r := New()
	called := false

	api := r.Group("/api")
	v1 := api.Group("/v1")
	v1.Get("/users", func(c *Context) error {
		called = true
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	req := httptest.NewRequest("GET", "/api/v1/users", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !called {
		t.Error("Nested group route handler was not called")
	}
}

func TestPrefix(t *testing.T) {
	r := New()
	r.Prefix("/api/v1")
	called := false

	r.Get("/users", func(c *Context) error {
		called = true
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	req := httptest.NewRequest("GET", "/api/v1/users", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !called {
		t.Error("Prefixed route handler was not called")
	}
}

func TestNamedRoutes(t *testing.T) {
	r := New()

	r.Get("/users/{id}", func(c *Context) error {
		c.Response.WriteHeader(http.StatusOK)
		return nil
	}).Name("user.show")

	// Trigger route commit
	req := httptest.NewRequest("GET", "/users/1", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Test URL generation
	url, err := r.RouteURL("user.show", map[string]string{"id": "123"})
	if err != nil {
		t.Fatalf("Failed to generate URL: %v", err)
	}

	if url != "/users/123" {
		t.Errorf("Expected URL '/users/123', got '%s'", url)
	}

	// Test non-existent route
	_, err = r.RouteURL("non.existent", nil)
	if err == nil {
		t.Error("Expected error for non-existent route")
	}
}

func TestMultipleParameters(t *testing.T) {
	r := New()
	var postID, commentID string

	r.Get("/posts/{post}/comments/{comment}", func(c *Context) error {
		postID = c.Param("post")
		commentID = c.Param("comment")
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	req := httptest.NewRequest("GET", "/posts/123/comments/456", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if postID != "123" {
		t.Errorf("Expected post ID '123', got '%s'", postID)
	}
	if commentID != "456" {
		t.Errorf("Expected comment ID '456', got '%s'", commentID)
	}
}

func TestHandle(t *testing.T) {
	r := New()
	handler := r.Handle()

	if handler == nil {
		t.Fatal("Handle() returned nil")
	}

	r.Get("/test", func(c *Context) error {
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

func TestConcurrentRouteAccess(t *testing.T) {
	r := New()

	// Register routes first
	for i := 0; i < 10; i++ {
		path := fmt.Sprintf("/route%d", i)
		r.Get(path, func(c *Context) error {
			c.Response.WriteHeader(http.StatusOK)
			return nil
		})
	}

	// Trigger commit
	req := httptest.NewRequest("GET", "/route0", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Concurrent access
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			path := fmt.Sprintf("/route%d", id)
			req := httptest.NewRequest("GET", path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("Route %s failed with status %d", path, w.Code)
			}
		}(i)
	}

	wg.Wait()
}

func TestCurrentRoute(t *testing.T) {
	r := New()
	var routeName string

	r.Get("/test", func(c *Context) error {
		routeName = CurrentRoute(c.Request)
		c.Response.WriteHeader(http.StatusOK)
		return nil
	}).Name("test.route")

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if routeName != "test.route" {
		t.Errorf("Expected route name 'test.route', got '%s'", routeName)
	}
}

func TestRouteNotFoundError(t *testing.T) {
	err := &RouteNotFoundError{Name: "test.route"}
	expected := "route not found: test.route"

	if err.Error() != expected {
		t.Errorf("Expected error message '%s', got '%s'", expected, err.Error())
	}
}

func TestResourceRouteOnly(t *testing.T) {
	r := New()
	rr := r.Resource("/posts", nil)

	// Test Only method
	rr.Only("index", "show")

	wrapper := rr.(*resourceWrapperV2)
	if wrapper.methods["index"] != true {
		t.Error("Expected 'index' to be enabled")
	}
	if wrapper.methods["show"] != true {
		t.Error("Expected 'show' to be enabled")
	}
	if wrapper.methods["create"] != false {
		t.Error("Expected 'create' to be disabled")
	}
}

func TestResourceRouteExcept(t *testing.T) {
	r := New()
	rr := r.Resource("/posts", nil)

	// Test Except method
	rr.Except("destroy")

	wrapper := rr.(*resourceWrapperV2)
	if wrapper.methods["index"] != true {
		t.Error("Expected 'index' to be enabled")
	}
	if wrapper.methods["destroy"] != false {
		t.Error("Expected 'destroy' to be disabled")
	}
}

func TestStaticFileServing(t *testing.T) {
	// Create temp directory with test files
	tmpDir := t.TempDir()
	testFile := tmpDir + "/test.txt"
	testContent := "Hello, World!"

	if err := os.WriteFile(testFile, []byte(testContent), 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	r := New()
	r.Static(tmpDir)

	// Test serving existing file
	req := httptest.NewRequest("GET", "/test.txt", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	if w.Body.String() != testContent {
		t.Errorf("Expected body '%s', got '%s'", testContent, w.Body.String())
	}
}

func TestStaticFileNotFound(t *testing.T) {
	tmpDir := t.TempDir()

	r := New()
	r.Static(tmpDir)

	// Add a route to test fallback
	called := false
	r.Get("/not-a-file", func(c *Context) error {
		called = true
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	// Test non-existent file falls through to routes
	req := httptest.NewRequest("GET", "/not-a-file", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !called {
		t.Error("Route handler was not called when file doesn't exist")
	}
}

func TestStaticWithRoutes(t *testing.T) {
	// Create temp directory with test file
	tmpDir := t.TempDir()
	testFile := tmpDir + "/logo.png"
	testContent := []byte{0x89, 0x50, 0x4E, 0x47} // PNG header

	if err := os.WriteFile(testFile, testContent, 0644); err != nil {
		t.Fatalf("Failed to create test file: %v", err)
	}

	r := New()
	r.Static(tmpDir)

	// Add a route
	routeCalled := false
	r.Get("/api/test", func(c *Context) error {
		routeCalled = true
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	// Test static file is served
	req := httptest.NewRequest("GET", "/logo.png", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 for static file, got %d", w.Code)
	}

	// Test route still works
	req = httptest.NewRequest("GET", "/api/test", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !routeCalled {
		t.Error("Route handler was not called")
	}
}

func TestStaticDisabled(t *testing.T) {
	r := New()
	// Don't call Static(), so it should be disabled

	routeCalled := false
	r.Get("/test", func(c *Context) error {
		routeCalled = true
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if !routeCalled {
		t.Error("Route handler was not called when static is disabled")
	}
}

// BenchmarkRouter_StaticHit exercises the O(1) compiled static-route
// fast path in ServeHTTP (velocity_router.go compiledRoutes).
// Protect this number: a refactor that silently drops the static path
// back to a tree walk will show up here as a large regression.
func BenchmarkRouter_StaticHit(b *testing.B) {
	r := New()
	r.Get("/users", func(c *Context) error {
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})
	r.Freeze()

	req := httptest.NewRequest("GET", "/users", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}
}

// BenchmarkRouter_DynamicHit exercises the tree walk for parameterized
// routes (no static-map shortcut possible). This is the common case for
// REST routes like /users/{id}.
func BenchmarkRouter_DynamicHit(b *testing.B) {
	r := New()
	r.Get("/users/{id}/posts/{postID}", func(c *Context) error {
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})
	r.Freeze()

	req := httptest.NewRequest("GET", "/users/42/posts/7", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}
}

// BenchmarkRouter_MissFallback exercises the 404 path: static-map miss,
// tree walk miss, default not-found handler. Regressions here signal
// a hot loop added to the miss path (e.g., allocating an error per miss).
func BenchmarkRouter_MissFallback(b *testing.B) {
	r := New()
	r.Get("/users", func(c *Context) error { return nil })
	r.Freeze()

	req := httptest.NewRequest("GET", "/does-not-exist", nil)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
	}
}

// BenchmarkClientIP_Concurrent drives parallel requests through the full
// proxy-resolution path (currentWiring -> trustedProxiesOrParse -> IP()).
// Before the atomic.Pointer migration, every request took r.mu inside
// currentWiring, so this benchmark serialized on that single mutex and
// ns/op rose with -cpu. With the lock-free read the per-request snapshot
// no longer contends, so throughput scales with GOMAXPROCS.
//
// Run: go test ./router -run '^$' -bench BenchmarkClientIP_Concurrent -cpu 1,4,8
func BenchmarkClientIP_Concurrent(b *testing.B) {
	r := New()
	r.TrustedProxies = []string{"10.0.0.0/8"}
	if err := r.ValidateConfig(); err != nil {
		b.Fatalf("ValidateConfig: %v", err)
	}
	r.Get("/ping", func(c *Context) error {
		_ = c.IP() // exercise trusted-proxy resolution
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})
	r.Freeze()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		req := httptest.NewRequest("GET", "/ping", nil)
		req.RemoteAddr = "10.0.0.1:443" // trusted LB
		req.Header.Set("X-Forwarded-For", "203.0.113.9")
		for pb.Next() {
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
		}
	})
}

// BenchmarkCurrentWiring_Concurrent isolates the contended call:
// currentWiring builds the per-request snapshot and is invoked once per
// request from ServeHTTP. The full-request benchmark above is dominated
// by httptest allocations, which mask the wiring cost; this one calls
// currentWiring directly so the mutex-vs-atomic delta is visible.
//
// Old (r.mu.Lock per call) serializes every parallel goroutine on one
// mutex, so ns/op stays flat or worsens with more cores. The lock-free
// atomic.Pointer load scales down as -cpu rises.
//
// Run: go test ./router -run '^$' -bench BenchmarkCurrentWiring_Concurrent -cpu 1,4,8
func BenchmarkCurrentWiring_Concurrent(b *testing.B) {
	r := New()
	r.TrustedProxies = []string{"10.0.0.0/8"}
	if err := r.ValidateConfig(); err != nil {
		b.Fatalf("ValidateConfig: %v", err)
	}
	r.Get("/ping", func(c *Context) error { return nil })
	r.Freeze()

	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_ = r.currentWiring()
		}
	})
}
