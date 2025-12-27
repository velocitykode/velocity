package auth

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// mockAuthChecker creates a mock auth checker for testing
func mockAuthChecker(authenticated bool) func(*http.Request) bool {
	return func(r *http.Request) bool {
		return authenticated
	}
}

func TestGuestMiddleware(t *testing.T) {
	// Save original auth checker
	originalChecker := authChecker
	defer func() { authChecker = originalChecker }()

	tests := []struct {
		name            string
		isAuthenticated bool
		expectedStatus  int
		expectedPath    string
		checkBody       bool
		expectedBody    string
	}{
		{
			name:            "allows unauthenticated users",
			isAuthenticated: false,
			expectedStatus:  http.StatusOK,
			expectedBody:    "Guest page",
			checkBody:       true,
		},
		{
			name:            "redirects authenticated users",
			isAuthenticated: true,
			expectedStatus:  http.StatusSeeOther,
			expectedPath:    "/dashboard",
			checkBody:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set mock auth checker
			authChecker = mockAuthChecker(tt.isAuthenticated)

			// Create a test handler
			handler := Guest("/dashboard")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("Guest page"))
			}))

			// Create request and response
			req := httptest.NewRequest("GET", "/login", nil)
			rec := httptest.NewRecorder()

			// Serve the request
			handler.ServeHTTP(rec, req)

			// Check status
			if rec.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rec.Code)
			}

			// Check redirect location if applicable
			if tt.expectedStatus == http.StatusSeeOther {
				location := rec.Header().Get("Location")
				if location != tt.expectedPath {
					t.Errorf("expected redirect to %s, got %s", tt.expectedPath, location)
				}
			}

			// Check body if applicable
			if tt.checkBody {
				body := rec.Body.String()
				if body != tt.expectedBody {
					t.Errorf("expected body %q, got %q", tt.expectedBody, body)
				}
			}
		})
	}
}

func TestRedirectIfAuthenticatedMiddleware(t *testing.T) {
	// Save original auth checker
	originalChecker := authChecker
	defer func() { authChecker = originalChecker }()

	// Test that RedirectIfAuthenticated behaves identically to Guest
	t.Run("behaves like Guest middleware", func(t *testing.T) {
		authChecker = mockAuthChecker(true) // User is authenticated

		handler1 := Guest("/dashboard")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("handler"))
		}))

		handler2 := RedirectIfAuthenticated("/dashboard")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("handler"))
		}))

		req := httptest.NewRequest("GET", "/login", nil)
		rec1 := httptest.NewRecorder()
		rec2 := httptest.NewRecorder()

		handler1.ServeHTTP(rec1, req)
		handler2.ServeHTTP(rec2, req)

		// Both should redirect
		if rec1.Code != http.StatusSeeOther || rec2.Code != http.StatusSeeOther {
			t.Errorf("Both should redirect: Guest=%d, RedirectIfAuthenticated=%d", rec1.Code, rec2.Code)
		}

		// Both should redirect to the same location
		if rec1.Header().Get("Location") != rec2.Header().Get("Location") {
			t.Errorf("Both should redirect to same location: Guest=%s, RedirectIfAuthenticated=%s",
				rec1.Header().Get("Location"), rec2.Header().Get("Location"))
		}
	})
}

func TestRequireAuthMiddleware(t *testing.T) {
	// Save original auth checker
	originalChecker := authChecker
	defer func() { authChecker = originalChecker }()

	tests := []struct {
		name            string
		isAuthenticated bool
		requestPath     string
		expectedStatus  int
		checkRedirect   bool
		expectedBody    string
	}{
		{
			name:            "allows authenticated users",
			isAuthenticated: true,
			requestPath:     "/dashboard",
			expectedStatus:  http.StatusOK,
			expectedBody:    "Protected content",
		},
		{
			name:            "redirects unauthenticated users",
			isAuthenticated: false,
			requestPath:     "/dashboard",
			expectedStatus:  http.StatusSeeOther,
			checkRedirect:   true,
		},
		{
			name:            "preserves query parameters in redirect",
			isAuthenticated: false,
			requestPath:     "/dashboard?filter=active&sort=name",
			expectedStatus:  http.StatusSeeOther,
			checkRedirect:   true,
		},
		{
			name:            "handles paths with special characters",
			isAuthenticated: false,
			requestPath:     "/dashboard/user@example.com/settings",
			expectedStatus:  http.StatusSeeOther,
			checkRedirect:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set mock auth checker
			authChecker = mockAuthChecker(tt.isAuthenticated)

			// Create a test handler
			handler := RequireAuth("/login")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
				w.Write([]byte("Protected content"))
			}))

			// Create request and response
			req := httptest.NewRequest("GET", tt.requestPath, nil)
			rec := httptest.NewRecorder()

			// Serve the request
			handler.ServeHTTP(rec, req)

			// Check status
			if rec.Code != tt.expectedStatus {
				t.Errorf("expected status %d, got %d", tt.expectedStatus, rec.Code)
			}

			// Check redirect location if redirected
			if tt.checkRedirect && rec.Code == http.StatusSeeOther {
				location := rec.Header().Get("Location")
				if !strings.HasPrefix(location, "/login?redirect=") {
					t.Errorf("expected redirect to start with '/login?redirect=', got %s", location)
				}

				// Parse the redirect URL to check the redirect parameter
				parsedURL, err := url.Parse(location)
				if err != nil {
					t.Errorf("failed to parse redirect URL: %v", err)
				}

				redirectParam := parsedURL.Query().Get("redirect")
				if redirectParam == "" {
					t.Errorf("redirect parameter is missing")
				}

				// Verify that the original path is preserved (URL decoded)
				decodedRedirect, err := url.QueryUnescape(redirectParam)
				if err != nil {
					t.Errorf("failed to decode redirect parameter: %v", err)
				}

				if decodedRedirect != tt.requestPath {
					t.Errorf("expected redirect to contain original path %s, got %s", tt.requestPath, decodedRedirect)
				}
			}

			// Check body if authenticated
			if tt.isAuthenticated {
				body := rec.Body.String()
				if body != tt.expectedBody {
					t.Errorf("expected body %q, got %q", tt.expectedBody, body)
				}
			}
		})
	}
}

func TestMiddlewareAlias(t *testing.T) {
	// Save original auth checker
	originalChecker := authChecker
	defer func() { authChecker = originalChecker }()

	// Test that Middleware is an alias for RequireAuth
	t.Run("Middleware behaves like RequireAuth", func(t *testing.T) {
		authChecker = mockAuthChecker(false) // User is not authenticated

		handler1 := Middleware("/login")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("handler"))
		}))

		handler2 := RequireAuth("/login")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("handler"))
		}))

		req := httptest.NewRequest("GET", "/dashboard", nil)
		rec1 := httptest.NewRecorder()
		rec2 := httptest.NewRecorder()

		handler1.ServeHTTP(rec1, req)
		handler2.ServeHTTP(rec2, req)

		// Both should redirect
		if rec1.Code != http.StatusSeeOther || rec2.Code != http.StatusSeeOther {
			t.Errorf("Both should redirect: Middleware=%d, RequireAuth=%d", rec1.Code, rec2.Code)
		}

		// Both should redirect to the same location
		if rec1.Header().Get("Location") != rec2.Header().Get("Location") {
			t.Errorf("Both should redirect to same location: Middleware=%s, RequireAuth=%s",
				rec1.Header().Get("Location"), rec2.Header().Get("Location"))
		}
	})
}

func TestMiddlewareChaining(t *testing.T) {
	// Save original auth checker
	originalChecker := authChecker
	defer func() { authChecker = originalChecker }()

	// Test that middleware can be properly chained
	callOrder := []string{}

	middleware1 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			callOrder = append(callOrder, "middleware1-before")
			next.ServeHTTP(w, r)
			callOrder = append(callOrder, "middleware1-after")
		})
	}

	finalHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callOrder = append(callOrder, "handler")
		w.WriteHeader(http.StatusOK)
	})

	t.Run("Guest middleware chains properly", func(t *testing.T) {
		callOrder = []string{}               // Reset
		authChecker = mockAuthChecker(false) // User is not authenticated

		// Chain Guest middleware with custom middleware
		handler := Guest("/dashboard")(middleware1(finalHandler))

		req := httptest.NewRequest("GET", "/login", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		// Since user is not authenticated, should proceed through middleware
		expected := []string{"middleware1-before", "handler", "middleware1-after"}
		if len(callOrder) != len(expected) {
			t.Errorf("Expected call order length %d, got %d", len(expected), len(callOrder))
		}

		for i, call := range expected {
			if i >= len(callOrder) || callOrder[i] != call {
				t.Errorf("Expected call %d to be %s, got %s", i, call, callOrder[i])
			}
		}
	})

	t.Run("RequireAuth middleware chains properly", func(t *testing.T) {
		callOrder = []string{}              // Reset
		authChecker = mockAuthChecker(true) // User is authenticated

		// Chain RequireAuth middleware with custom middleware
		handler := RequireAuth("/login")(middleware1(finalHandler))

		req := httptest.NewRequest("GET", "/dashboard", nil)
		rec := httptest.NewRecorder()

		handler.ServeHTTP(rec, req)

		// Since user is authenticated, should proceed through middleware
		expected := []string{"middleware1-before", "handler", "middleware1-after"}
		if len(callOrder) != len(expected) {
			t.Errorf("Expected call order length %d, got %d", len(expected), len(callOrder))
		}

		for i, call := range expected {
			if i >= len(callOrder) || callOrder[i] != call {
				t.Errorf("Expected call %d to be %s, got %s", i, call, callOrder[i])
			}
		}
	})
}

func TestGuestMiddlewareBlocksAuthenticatedUsers(t *testing.T) {
	// Save original auth checker
	originalChecker := authChecker
	defer func() { authChecker = originalChecker }()

	authChecker = mockAuthChecker(true) // User IS authenticated

	handlerCalled := false
	handler := Guest("/dashboard")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/login", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// Handler should NOT be called when user is authenticated
	if handlerCalled {
		t.Error("Guest middleware should block authenticated users from reaching the handler")
	}

	// Should redirect to dashboard
	if rec.Code != http.StatusSeeOther {
		t.Errorf("Expected redirect status %d, got %d", http.StatusSeeOther, rec.Code)
	}

	if location := rec.Header().Get("Location"); location != "/dashboard" {
		t.Errorf("Expected redirect to /dashboard, got %s", location)
	}
}

func TestRequireAuthMiddlewareBlocksUnauthenticatedUsers(t *testing.T) {
	// Save original auth checker
	originalChecker := authChecker
	defer func() { authChecker = originalChecker }()

	authChecker = mockAuthChecker(false) // User is NOT authenticated

	handlerCalled := false
	handler := RequireAuth("/login")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlerCalled = true
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest("GET", "/dashboard", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	// Handler should NOT be called when user is not authenticated
	if handlerCalled {
		t.Error("RequireAuth middleware should block unauthenticated users from reaching the handler")
	}

	// Should redirect to login
	if rec.Code != http.StatusSeeOther {
		t.Errorf("Expected redirect status %d, got %d", http.StatusSeeOther, rec.Code)
	}

	location := rec.Header().Get("Location")
	if !strings.HasPrefix(location, "/login?redirect=") {
		t.Errorf("Expected redirect to start with '/login?redirect=', got %s", location)
	}
}
