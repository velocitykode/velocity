package csrf

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/pkg/csrf/stores"
)

func TestNew(t *testing.T) {
	// Test with nil config
	csrf := New(nil)
	if csrf == nil {
		t.Fatal("New returned nil")
	}

	// Test with custom config
	config := &Config{
		HeaderName: "X-Custom-Token",
		FormField:  "_custom_token",
	}
	csrf = New(config)
	if csrf.config.HeaderName != "X-Custom-Token" {
		t.Error("Config not applied correctly")
	}
}

func TestMiddleware_SafeMethods(t *testing.T) {
	csrf := New(nil)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Test safe methods (should pass without token)
	safeMethods := []string{"GET", "HEAD", "OPTIONS"}

	for _, method := range safeMethods {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/test", nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Errorf("Expected status 200 for %s, got %d", method, w.Code)
			}
		})
	}
}

func TestMiddleware_UnsafeMethods_NoToken(t *testing.T) {
	csrf := New(nil)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Test unsafe methods without token (should fail)
	unsafeMethods := []string{"POST", "PUT", "DELETE", "PATCH"}

	for _, method := range unsafeMethods {
		t.Run(method, func(t *testing.T) {
			req := httptest.NewRequest(method, "/test", nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != 419 {
				t.Errorf("Expected status 419 for %s without token, got %d", method, w.Code)
			}
		})
	}
}

func TestMiddleware_ValidToken_Header(t *testing.T) {
	config := DefaultConfig()
	config.Store = stores.NewSessionStore()
	csrf := New(config)

	// Generate and store token
	sessionID := "test-session"
	token, _ := GenerateToken()
	csrf.config.Store.Set(sessionID, token)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Create request with valid token in header
	req := httptest.NewRequest("POST", "/test", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})
	req.Header.Set("X-CSRF-Token", token)

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 with valid token, got %d", w.Code)
	}
}

func TestMiddleware_ValidToken_FormField(t *testing.T) {
	config := DefaultConfig()
	config.Store = stores.NewSessionStore()
	csrf := New(config)

	// Generate and store token
	sessionID := "test-session"
	token, _ := GenerateToken()
	csrf.config.Store.Set(sessionID, token)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Create request with valid token in form
	formData := url.Values{}
	formData.Set("_token", token)
	req := httptest.NewRequest("POST", "/test", strings.NewReader(formData.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200 with valid token, got %d", w.Code)
	}
}

func TestMiddleware_InvalidToken(t *testing.T) {
	config := DefaultConfig()
	config.Store = stores.NewSessionStore()
	csrf := New(config)

	// Generate and store token
	sessionID := "test-session"
	token, _ := GenerateToken()
	csrf.config.Store.Set(sessionID, token)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Create request with INVALID token
	req := httptest.NewRequest("POST", "/test", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})
	req.Header.Set("X-CSRF-Token", "invalid-token")

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 419 {
		t.Errorf("Expected status 419 with invalid token, got %d", w.Code)
	}
}

func TestMiddleware_ExcludedPaths(t *testing.T) {
	config := DefaultConfig()
	config.ExcludePaths = []string{"/api/webhooks/*", "/health"}
	csrf := New(config)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		path   string
		status int
	}{
		{"/api/webhooks/stripe", http.StatusOK},
		{"/api/webhooks/github", http.StatusOK},
		{"/health", http.StatusOK},
		{"/other", 419}, // Not excluded, should fail without token
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			req := httptest.NewRequest("POST", tt.path, nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			if w.Code != tt.status {
				t.Errorf("Expected status %d for %s, got %d", tt.status, tt.path, w.Code)
			}
		})
	}
}

func TestMiddleware_ExcludeFunc(t *testing.T) {
	config := DefaultConfig()
	// Exclude requests with API key
	config.ExcludeFunc = func(r *http.Request) bool {
		return r.Header.Get("X-API-Key") != ""
	}
	csrf := New(config)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Request with API key (should pass)
	req1 := httptest.NewRequest("POST", "/test", nil)
	req1.Header.Set("X-API-Key", "secret")
	w1 := httptest.NewRecorder()
	handler.ServeHTTP(w1, req1)

	if w1.Code != http.StatusOK {
		t.Errorf("Expected status 200 with API key, got %d", w1.Code)
	}

	// Request without API key (should fail)
	req2 := httptest.NewRequest("POST", "/test", nil)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)

	if w2.Code != 419 {
		t.Errorf("Expected status 419 without API key, got %d", w2.Code)
	}
}

func TestRefreshHandler(t *testing.T) {
	config := DefaultConfig()
	config.Store = stores.NewSessionStore()
	csrf := New(config)

	handler := csrf.RefreshHandler()

	req := httptest.NewRequest("POST", "/csrf/refresh", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: "test-session"})
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected status 200, got %d", w.Code)
	}

	// Check if response contains token
	if !strings.Contains(w.Body.String(), "token") {
		t.Error("Response should contain token field")
	}
}

func TestGetToken(t *testing.T) {
	config := DefaultConfig()
	config.Store = stores.NewSessionStore()
	csrf := New(config)

	sessionID := "test-session"

	// Get token (should generate new one)
	token1, err := csrf.GetToken(sessionID)
	if err != nil {
		t.Fatalf("Failed to get token: %v", err)
	}
	if token1 == "" {
		t.Error("Token should not be empty")
	}

	// Get token again (should return same token)
	token2, err := csrf.GetToken(sessionID)
	if err != nil {
		t.Fatalf("Failed to get token: %v", err)
	}

	if token1 != token2 {
		t.Error("Should return same token for same session")
	}
}

func TestMatchPath(t *testing.T) {
	tests := []struct {
		path    string
		pattern string
		want    bool
	}{
		{"/api/webhooks/stripe", "/api/webhooks/*", true},
		{"/api/webhooks/github", "/api/webhooks/*", true},
		{"/api/users", "/api/webhooks/*", false},
		{"/health", "/health", true},
		{"/health/check", "/health", false},
		{"/health", "/health/*", false},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			got := matchPath(tt.path, tt.pattern)
			if got != tt.want {
				t.Errorf("matchPath(%s, %s) = %v, want %v", tt.path, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestIsJSONRequest(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		accept      string
		want        bool
	}{
		{"JSON content type", "application/json", "", true},
		{"JSON accept", "", "application/json", true},
		{"Both JSON", "application/json", "application/json", true},
		{"Form content type", "application/x-www-form-urlencoded", "", false},
		{"HTML accept", "", "text/html", false},
		{"Empty headers", "", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest("POST", "/test", nil)
			if tt.contentType != "" {
				req.Header.Set("Content-Type", tt.contentType)
			}
			if tt.accept != "" {
				req.Header.Set("Accept", tt.accept)
			}

			got := isJSONRequest(req)
			if got != tt.want {
				t.Errorf("isJSONRequest() = %v, want %v", got, tt.want)
			}
		})
	}
}

func BenchmarkMiddleware_SafeMethod(b *testing.B) {
	csrf := New(nil)
	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest("GET", "/test", nil)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}
}

func BenchmarkMiddleware_ValidToken(b *testing.B) {
	config := DefaultConfig()
	config.Store = stores.NewSessionStore()
	csrf := New(config)

	sessionID := "test-session"
	token, _ := GenerateToken()
	csrf.config.Store.Set(sessionID, token)

	handler := csrf.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	req := httptest.NewRequest("POST", "/test", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})
	req.Header.Set("X-CSRF-Token", token)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
	}
}
