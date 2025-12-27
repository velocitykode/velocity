package router

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestRegister(t *testing.T) {
	// Clear registrations for test
	registrations = nil

	called := false
	Register(func(r Router) {
		called = true
		r.Get("/test", func(c *Context) error {
			c.Response.WriteHeader(http.StatusOK)
			return nil
		})
	})

	if len(registrations) != 1 {
		t.Errorf("Expected 1 registration, got %d", len(registrations))
	}

	// Apply registrations
	router := New()
	applyRegistrations(router)

	if !called {
		t.Error("Registration function was not called")
	}

	// Test the registered route works
	req := httptest.NewRequest("GET", "/test", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

func TestRegisterWithPrefix(t *testing.T) {
	// Clear registrations for test
	registrations = nil

	called := false
	RegisterWithPrefix("/api", func(r Router) {
		called = true
		r.Get("/users", func(c *Context) error {
			c.Response.WriteHeader(http.StatusOK)
			return nil
		})
	})

	if len(registrations) != 1 {
		t.Errorf("Expected 1 registration, got %d", len(registrations))
	}

	// Apply registrations
	router := New()
	applyRegistrations(router)

	if !called {
		t.Error("Registration function was not called")
	}

	// Test the prefixed route works
	req := httptest.NewRequest("GET", "/api/users", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Test non-prefixed path doesn't work
	req = httptest.NewRequest("GET", "/users", nil)
	w = httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Error("Non-prefixed path should not work")
	}
}

func TestMultipleRegistrations(t *testing.T) {
	// Clear registrations for test
	registrations = nil

	// Register multiple route files
	Register(func(r Router) {
		r.Get("/route1", func(c *Context) error {
			c.Response.Write([]byte("route1"))
			return nil
		})
	})

	Register(func(r Router) {
		r.Get("/route2", func(c *Context) error {
			c.Response.Write([]byte("route2"))
			return nil
		})
	})

	RegisterWithPrefix("/api", func(r Router) {
		r.Get("/route3", func(c *Context) error {
			c.Response.Write([]byte("route3"))
			return nil
		})
	})

	if len(registrations) != 3 {
		t.Errorf("Expected 3 registrations, got %d", len(registrations))
	}

	// Apply all registrations
	router := New()
	applyRegistrations(router)

	// Test all routes work
	tests := []struct {
		path     string
		expected string
	}{
		{"/route1", "route1"},
		{"/route2", "route2"},
		{"/api/route3", "route3"},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("GET", tt.path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Path %s: expected status 200, got %d", tt.path, w.Code)
		}

		if w.Body.String() != tt.expected {
			t.Errorf("Path %s: expected body '%s', got '%s'", tt.path, tt.expected, w.Body.String())
		}
	}
}

func TestLoadRoutes(t *testing.T) {
	// Clear registrations and reset global router
	registrations = nil
	globalRouter = nil
	once = sync.Once{}

	// Register a test route
	Register(func(r Router) {
		r.Get("/load-test", func(c *Context) error {
			c.Response.WriteHeader(http.StatusOK)
			return nil
		})
	})

	// Load routes to global router
	LoadRoutes()

	// Test using global router
	req := httptest.NewRequest("GET", "/load-test", nil)
	w := httptest.NewRecorder()
	Get().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}
}

func TestConcurrentRegistration(t *testing.T) {
	// Clear registrations for test
	registrations = nil

	done := make(chan bool, 10)

	// Register routes concurrently
	for i := 0; i < 10; i++ {
		go func(id int) {
			Register(func(r Router) {
				path := fmt.Sprintf("/concurrent%d", id)
				r.Get(path, func(c *Context) error {
					c.Response.WriteHeader(http.StatusOK)
					return nil
				})
			})
			done <- true
		}(i)
	}

	// Wait for all registrations
	for i := 0; i < 10; i++ {
		<-done
	}

	if len(registrations) != 10 {
		t.Errorf("Expected 10 registrations, got %d", len(registrations))
	}

	// Apply and test all routes
	router := New()
	applyRegistrations(router)

	for i := 0; i < 10; i++ {
		path := fmt.Sprintf("/concurrent%d", i)
		req := httptest.NewRequest("GET", path, nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Path %s: expected status 200, got %d", path, w.Code)
		}
	}
}
