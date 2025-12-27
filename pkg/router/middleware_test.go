package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// TestMiddlewareExecution tests that middleware executes correctly
func TestMiddlewareExecution(t *testing.T) {
	router := New()
	var executed []string
	var mu sync.Mutex

	// Create test middleware using new MiddlewareFunc signature
	middleware1 := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			mu.Lock()
			executed = append(executed, "middleware1-before")
			mu.Unlock()
			err := next(c)
			mu.Lock()
			executed = append(executed, "middleware1-after")
			mu.Unlock()
			return err
		}
	}

	middleware2 := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			mu.Lock()
			executed = append(executed, "middleware2-before")
			mu.Unlock()
			err := next(c)
			mu.Lock()
			executed = append(executed, "middleware2-after")
			mu.Unlock()
			return err
		}
	}

	// Apply middleware to router
	router.Use(middleware1, middleware2)

	// Add route
	router.Get("/test", func(c *Context) error {
		mu.Lock()
		executed = append(executed, "handler")
		mu.Unlock()
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	// Make request
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Verify execution order
	expected := []string{
		"middleware1-before",
		"middleware2-before",
		"handler",
		"middleware2-after",
		"middleware1-after",
	}

	if len(executed) != len(expected) {
		t.Fatalf("Expected %d executions, got %d", len(expected), len(executed))
	}

	for i, exp := range expected {
		if executed[i] != exp {
			t.Errorf("Expected execution[%d] to be %s, got %s", i, exp, executed[i])
		}
	}
}

// TestGroupMiddleware tests middleware inheritance in groups
func TestGroupMiddleware(t *testing.T) {
	router := New()
	var executed []string
	var mu sync.Mutex

	// Global middleware
	globalMiddleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			mu.Lock()
			executed = append(executed, "global")
			mu.Unlock()
			return next(c)
		}
	}

	// Group middleware
	groupMiddleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			mu.Lock()
			executed = append(executed, "group")
			mu.Unlock()
			return next(c)
		}
	}

	// Apply global middleware
	router.Use(globalMiddleware)

	// Create group with additional middleware
	api := router.Group("/api")
	api.Use(groupMiddleware)

	// Add route to group
	api.Get("/users", func(c *Context) error {
		mu.Lock()
		executed = append(executed, "handler")
		mu.Unlock()
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	// Make request
	req := httptest.NewRequest("GET", "/api/users", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Verify both middlewares executed
	expected := []string{"global", "group", "handler"}
	if len(executed) != len(expected) {
		t.Fatalf("Expected %d executions, got %d", len(expected), len(executed))
	}

	for i, exp := range expected {
		if executed[i] != exp {
			t.Errorf("Expected execution[%d] to be %s, got %s", i, exp, executed[i])
		}
	}
}

// TestRouteSpecificMiddleware tests middleware on individual routes
func TestRouteSpecificMiddleware(t *testing.T) {
	router := New()
	var executed []string
	var mu sync.Mutex

	// Route-specific middleware
	authMiddleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			mu.Lock()
			executed = append(executed, "auth")
			mu.Unlock()
			return next(c)
		}
	}

	cacheMiddleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			mu.Lock()
			executed = append(executed, "cache")
			mu.Unlock()
			return next(c)
		}
	}

	// Public route without middleware
	router.Get("/public", func(c *Context) error {
		mu.Lock()
		executed = append(executed, "public-handler")
		mu.Unlock()
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	// Protected route with middleware
	router.Get("/protected", func(c *Context) error {
		mu.Lock()
		executed = append(executed, "protected-handler")
		mu.Unlock()
		c.Response.WriteHeader(http.StatusOK)
		return nil
	}).Use(authMiddleware, cacheMiddleware)

	// Test public route
	executed = []string{}
	req := httptest.NewRequest("GET", "/public", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if len(executed) != 1 || executed[0] != "public-handler" {
		t.Errorf("Expected only public-handler, got %v", executed)
	}

	// Test protected route
	executed = []string{}
	req = httptest.NewRequest("GET", "/protected", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	expected := []string{"auth", "cache", "protected-handler"}
	if len(executed) != len(expected) {
		t.Fatalf("Expected %d executions, got %d", len(expected), len(executed))
	}

	for i, exp := range expected {
		if executed[i] != exp {
			t.Errorf("Expected execution[%d] to be %s, got %s", i, exp, executed[i])
		}
	}
}

// TestMiddlewareChaining tests chaining middleware calls
func TestMiddlewareChaining(t *testing.T) {
	router := New()
	var executed []string
	var mu sync.Mutex

	m1 := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			mu.Lock()
			executed = append(executed, "m1")
			mu.Unlock()
			return next(c)
		}
	}

	m2 := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			mu.Lock()
			executed = append(executed, "m2")
			mu.Unlock()
			return next(c)
		}
	}

	m3 := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			mu.Lock()
			executed = append(executed, "m3")
			mu.Unlock()
			return next(c)
		}
	}

	// Chain multiple Use calls on route
	router.Get("/chained", func(c *Context) error {
		mu.Lock()
		executed = append(executed, "handler")
		mu.Unlock()
		c.Response.WriteHeader(http.StatusOK)
		return nil
	}).Use(m1).Use(m2).Use(m3)

	// Make request
	req := httptest.NewRequest("GET", "/chained", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Verify execution order
	expected := []string{"m1", "m2", "m3", "handler"}
	if len(executed) != len(expected) {
		t.Fatalf("Expected %d executions, got %d", len(expected), len(executed))
	}

	for i, exp := range expected {
		if executed[i] != exp {
			t.Errorf("Expected execution[%d] to be %s, got %s", i, exp, executed[i])
		}
	}
}

// TestNestedGroupsMiddleware tests middleware in nested groups
func TestNestedGroupsMiddleware(t *testing.T) {
	router := New()
	var executed []string
	var mu sync.Mutex

	// Create middleware
	rootMiddleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			mu.Lock()
			executed = append(executed, "root")
			mu.Unlock()
			return next(c)
		}
	}

	apiMiddleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			mu.Lock()
			executed = append(executed, "api")
			mu.Unlock()
			return next(c)
		}
	}

	v1Middleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			mu.Lock()
			executed = append(executed, "v1")
			mu.Unlock()
			return next(c)
		}
	}

	// Apply root middleware
	router.Use(rootMiddleware)

	// Create nested groups
	api := router.Group("/api").Use(apiMiddleware)
	v1 := api.Group("/v1").Use(v1Middleware)

	// Add route to nested group
	v1.Get("/users", func(c *Context) error {
		mu.Lock()
		executed = append(executed, "handler")
		mu.Unlock()
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	// Make request
	req := httptest.NewRequest("GET", "/api/v1/users", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Verify all middleware executed in correct order
	expected := []string{"root", "api", "v1", "handler"}
	if len(executed) != len(expected) {
		t.Fatalf("Expected %d executions, got %d", len(expected), len(executed))
	}

	for i, exp := range expected {
		if executed[i] != exp {
			t.Errorf("Expected execution[%d] to be %s, got %s", i, exp, executed[i])
		}
	}
}

// TestMiddlewareContext tests context passing through middleware
func TestMiddlewareContext(t *testing.T) {
	router := New()

	// Middleware that sets context value
	authMiddleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			// Set user ID in context
			c.Set("userID", "123")
			return next(c)
		}
	}

	// Apply middleware
	router.Use(authMiddleware)

	// Handler that reads context
	router.Get("/profile", func(c *Context) error {
		userID := c.GetString("userID")
		if userID != "123" {
			t.Errorf("Expected user ID 123, got %s", userID)
		}
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	// Make request
	req := httptest.NewRequest("GET", "/profile", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

// TestMiddlewareAbort tests middleware that aborts the chain
func TestMiddlewareAbort(t *testing.T) {
	router := New()
	var handlerCalled bool

	// Middleware that aborts
	abortMiddleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.Response.WriteHeader(http.StatusUnauthorized)
			c.Response.Write([]byte("Unauthorized"))
			// Don't call next - abort chain
			return nil
		}
	}

	// Apply abort middleware to specific route
	router.Get("/protected", func(c *Context) error {
		handlerCalled = true
		c.Response.WriteHeader(http.StatusOK)
		return nil
	}).Use(abortMiddleware)

	// Make request
	req := httptest.NewRequest("GET", "/protected", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected status 401, got %d", w.Code)
	}

	if handlerCalled {
		t.Error("Handler should not have been called")
	}

	body := w.Body.String()
	if body != "Unauthorized" {
		t.Errorf("Expected body 'Unauthorized', got %s", body)
	}
}

// TestMiddlewareModifyResponse tests middleware that modifies response
func TestMiddlewareModifyResponse(t *testing.T) {
	router := New()

	// Middleware that adds header
	headerMiddleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			c.Response.Header().Set("X-Custom-Header", "test-value")
			return next(c)
		}
	}

	router.Use(headerMiddleware)

	router.Get("/test", func(c *Context) error {
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	// Make request
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Header().Get("X-Custom-Header") != "test-value" {
		t.Errorf("Expected X-Custom-Header to be test-value, got %s", w.Header().Get("X-Custom-Header"))
	}
}

// TestMiddlewarePanic tests panic recovery in middleware
func TestMiddlewarePanic(t *testing.T) {
	router := New()
	var recovered bool

	// Recovery middleware
	recoveryMiddleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			defer func() {
				if r := recover(); r != nil {
					recovered = true
					c.Response.WriteHeader(http.StatusInternalServerError)
					c.Response.Write([]byte("Internal Server Error"))
				}
			}()
			return next(c)
		}
	}

	router.Use(recoveryMiddleware)

	// Handler that panics
	router.Get("/panic", func(c *Context) error {
		panic("test panic")
	})

	// Make request
	req := httptest.NewRequest("GET", "/panic", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if !recovered {
		t.Error("Panic was not recovered")
	}

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected status 500, got %d", w.Code)
	}

	if !strings.Contains(w.Body.String(), "Internal Server Error") {
		t.Errorf("Expected error message in body, got %s", w.Body.String())
	}
}

// TestMiddlewareOrder tests that middleware order is preserved
func TestMiddlewareOrder(t *testing.T) {
	router := New()
	order := []int{}
	var mu sync.Mutex

	createMiddleware := func(id int) MiddlewareFunc {
		return func(next HandlerFunc) HandlerFunc {
			return func(c *Context) error {
				mu.Lock()
				order = append(order, id)
				mu.Unlock()
				return next(c)
			}
		}
	}

	// Apply multiple middleware at once
	router.Use(
		createMiddleware(1),
		createMiddleware(2),
		createMiddleware(3),
		createMiddleware(4),
		createMiddleware(5),
	)

	router.Get("/test", func(c *Context) error {
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	// Make request
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Verify order
	expected := []int{1, 2, 3, 4, 5}
	if len(order) != len(expected) {
		t.Fatalf("Expected %d middleware calls, got %d", len(expected), len(order))
	}

	for i, exp := range expected {
		if order[i] != exp {
			t.Errorf("Expected middleware %d at position %d, got %d", exp, i, order[i])
		}
	}
}

// BenchmarkMiddleware benchmarks middleware overhead
func BenchmarkMiddleware(b *testing.B) {
	router := New()

	// Simple middleware
	middleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			return next(c)
		}
	}

	// Apply 5 middleware
	for i := 0; i < 5; i++ {
		router.Use(middleware)
	}

	router.Get("/bench", func(c *Context) error {
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	req := httptest.NewRequest("GET", "/bench", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
}

// BenchmarkNoMiddleware benchmarks requests without middleware
func BenchmarkNoMiddleware(b *testing.B) {
	router := New()

	router.Get("/bench", func(c *Context) error {
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	req := httptest.NewRequest("GET", "/bench", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
	}
}

// TestRouteMiddlewareWithRouterMiddleware tests that route-specific middleware
// works together with router-level middleware (covers buildHandler lines 283-285)
func TestRouteMiddlewareWithRouterMiddleware(t *testing.T) {
	router := New()
	var order []string
	var mu sync.Mutex

	// Router-level middleware
	routerMiddleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			mu.Lock()
			order = append(order, "router")
			mu.Unlock()
			return next(c)
		}
	}

	// Route-specific middleware
	routeMiddleware := func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			mu.Lock()
			order = append(order, "route")
			mu.Unlock()
			return next(c)
		}
	}

	// Add router middleware
	router.Use(routerMiddleware)

	// Add route with route-specific middleware
	router.Get("/test", func(c *Context) error {
		mu.Lock()
		order = append(order, "handler")
		mu.Unlock()
		c.Response.WriteHeader(http.StatusOK)
		return nil
	}).Use(routeMiddleware)

	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	// Both router and route middleware should execute
	expected := []string{"router", "route", "handler"}
	if len(order) != len(expected) {
		t.Fatalf("Expected %d executions, got %d: %v", len(expected), len(order), order)
	}

	for i, exp := range expected {
		if order[i] != exp {
			t.Errorf("Expected order[%d] to be %s, got %s", i, exp, order[i])
		}
	}
}
