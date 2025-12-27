package router

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestParam(t *testing.T) {
	r := New()
	var capturedValue string

	r.Get("/test/{param}", func(c *Context) error {
		capturedValue = Param(c.Request, "param")
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	req := httptest.NewRequest("GET", "/test/value123", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if capturedValue != "value123" {
		t.Errorf("Expected param 'value123', got '%s'", capturedValue)
	}

	// Test non-existent parameter
	r.Get("/other", func(c *Context) error {
		capturedValue = Param(c.Request, "nonexistent")
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	req = httptest.NewRequest("GET", "/other", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if capturedValue != "" {
		t.Errorf("Expected empty string for non-existent param, got '%s'", capturedValue)
	}
}

func TestParams(t *testing.T) {
	r := New()
	var capturedParams map[string]string

	r.Get("/posts/{post}/comments/{comment}", func(c *Context) error {
		capturedParams = Params(c.Request)
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	req := httptest.NewRequest("GET", "/posts/123/comments/456", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if len(capturedParams) != 2 {
		t.Fatalf("Expected 2 params, got %d", len(capturedParams))
	}

	if capturedParams["post"] != "123" {
		t.Errorf("Expected post='123', got '%s'", capturedParams["post"])
	}

	if capturedParams["comment"] != "456" {
		t.Errorf("Expected comment='456', got '%s'", capturedParams["comment"])
	}
}

func TestRouteGeneration(t *testing.T) {
	// Reset global router for clean test
	globalRouter = nil
	once = sync.Once{}

	r := Get()

	// Register named routes
	r.Get("/users/{id}", func(c *Context) error {
		c.Response.WriteHeader(http.StatusOK)
		return nil
	}).Name("user.show")

	r.Get("/posts/{post}/comments/{comment}", func(c *Context) error {
		c.Response.WriteHeader(http.StatusOK)
		return nil
	}).Name("comment.show")

	// Test single parameter route
	url, err := Route("user.show", map[string]string{"id": "123"})
	if err != nil {
		t.Fatalf("Failed to generate URL: %v", err)
	}
	if url != "/users/123" {
		t.Errorf("Expected '/users/123', got '%s'", url)
	}

	// Test multiple parameter route
	url, err = Route("comment.show", map[string]string{
		"post":    "456",
		"comment": "789",
	})
	if err != nil {
		t.Fatalf("Failed to generate URL: %v", err)
	}
	if url != "/posts/456/comments/789" {
		t.Errorf("Expected '/posts/456/comments/789', got '%s'", url)
	}

	// Test non-existent route
	_, err = Route("nonexistent", nil)
	if err == nil {
		t.Error("Expected error for non-existent route")
	}

	routeErr, ok := err.(*RouteNotFoundError)
	if !ok {
		t.Error("Expected RouteNotFoundError type")
	}
	if routeErr.Name != "nonexistent" {
		t.Errorf("Expected error name 'nonexistent', got '%s'", routeErr.Name)
	}
}

func TestCurrentRouteName(t *testing.T) {
	r := New()
	var capturedName string

	// Named route
	r.Get("/named", func(c *Context) error {
		capturedName = CurrentRoute(c.Request)
		c.Response.WriteHeader(http.StatusOK)
		return nil
	}).Name("test.named")

	req := httptest.NewRequest("GET", "/named", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if capturedName != "test.named" {
		t.Errorf("Expected route name 'test.named', got '%s'", capturedName)
	}

	// Unnamed route
	r.Get("/unnamed", func(c *Context) error {
		capturedName = CurrentRoute(c.Request)
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	req = httptest.NewRequest("GET", "/unnamed", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if capturedName != "" {
		t.Errorf("Expected empty route name for unnamed route, got '%s'", capturedName)
	}
}

func TestRouteGenerationWithSpecialCharacters(t *testing.T) {
	// Reset global router
	globalRouter = nil
	once = sync.Once{}

	r := Get()

	r.Get("/search/{query}", func(c *Context) error {
		c.Response.WriteHeader(http.StatusOK)
		return nil
	}).Name("search")

	// Test with special characters
	url, err := Route("search", map[string]string{"query": "hello world"})
	if err != nil {
		t.Fatalf("Failed to generate URL: %v", err)
	}

	// URL should be properly encoded
	expected := "/search/hello%20world"
	if url != expected {
		t.Errorf("Expected '%s', got '%s'", expected, url)
	}
}

func TestConcurrentParamAccess(t *testing.T) {
	r := New()
	done := make(chan bool, 10)

	r.Get("/concurrent/{id}", func(c *Context) error {
		// Simulate concurrent param access
		go func() {
			_ = Param(c.Request, "id")
			done <- true
		}()
		go func() {
			_ = Params(c.Request)
			done <- true
		}()
		c.Response.WriteHeader(http.StatusOK)
		return nil
	})

	req := httptest.NewRequest("GET", "/concurrent/123", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Wait for concurrent accesses
	for i := 0; i < 2; i++ {
		<-done
	}

	// If we get here without deadlock, test passes
}

func TestRouteGenerationMissingParams(t *testing.T) {
	// Reset global router
	globalRouter = nil
	once = sync.Once{}

	r := Get()

	r.Get("/users/{id}/posts/{post_id}", func(c *Context) error {
		c.Response.WriteHeader(http.StatusOK)
		return nil
	}).Name("user.post")

	// Test with missing parameter - should return error
	_, err := Route("user.post", map[string]string{"id": "123"})
	if err == nil {
		t.Error("Expected error for missing parameter")
	}
}

func TestCurrentRouteNilRoute(t *testing.T) {
	// Test with a request that has no route associated
	req := httptest.NewRequest("GET", "/nonexistent", nil)

	name := CurrentRoute(req)
	if name != "" {
		t.Errorf("Expected empty string for nil route, got '%s'", name)
	}
}
