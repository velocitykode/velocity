package csrf

import (
	"bytes"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/csrf/stores"
	"github.com/velocitykode/velocity/router"
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

// TestCSRF_RefusesEphemeralSession pins the regression for the bug where,
// in the absence of a session cookie, the middleware silently generated
// an ephemeral session ID and bound a fresh CSRF token to it. That
// allowed an attacker to mint their own session+token pair and replay
// the token against the victim's session (or a cookie-less victim).
// The middleware must now refuse to issue or validate tokens without a
// real session cookie.
func TestCSRF_RefusesEphemeralSession(t *testing.T) {
	c := New(DefaultConfig())

	t.Run("getSessionID returns ErrNoSession", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/submit", nil)
		id, err := c.getSessionID(r)
		if err == nil {
			t.Fatalf("expected error, got id=%q", id)
		}
		if !errors.Is(err, ErrNoSession) {
			t.Errorf("expected ErrNoSession, got %v", err)
		}
		if strings.HasPrefix(id, "temp-") {
			t.Errorf("ephemeral ID leaked: %q", id)
		}
	})

	t.Run("middleware blocks unsafe request without session", func(t *testing.T) {
		handler := c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("inner handler must not be called")
		}))
		req := httptest.NewRequest("POST", "/submit", nil)
		req.Header.Set("X-CSRF-Token", "anything")
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != 419 {
			t.Errorf("expected 419, got %d", w.Code)
		}
	})

	t.Run("refresh handler returns 400 without session", func(t *testing.T) {
		h := c.RefreshHandler()
		req := httptest.NewRequest("GET", "/csrf-token", nil)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", w.Code)
		}
	})
}

// TestNewE_RejectsUnsupportedMode confirms NewE returns an error rather
// than silently accepting ModeDoubleSubmit (reserved, not yet implemented).
// Without the rejection, an operator setting Mode=ModeDoubleSubmit would
// believe they have CSRF protection when in fact the double-submit path
// is not wired.
func TestNewE_RejectsUnsupportedMode(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Mode = ModeDoubleSubmit
	_, err := NewE(cfg)
	if err == nil {
		t.Fatal("expected error for ModeDoubleSubmit")
	}
	if !errors.Is(err, ErrInsecureCSRFConfig) {
		t.Errorf("expected ErrInsecureCSRFConfig, got %v", err)
	}
}

// TestRouterMiddleware_RejectionDoesNotAppendInternalServerError pins the
// regression for the bug where RouterMiddleware returned a non-nil error
// after the inner CSRF middleware had already written a 419 response.
// The router would then call its ErrorHandler, which invokes http.Error
// and appends "Internal Server Error\n" to the body (the status code is
// guarded by responseWriter, but the body is not). The fix is to return
// nil when the inner handler was not called, since the CSRF middleware
// has already fully written the rejection response.
func TestRouterMiddleware_RejectionDoesNotAppendInternalServerError(t *testing.T) {
	c := New(DefaultConfig())

	r := router.New()
	// Install a sentinel error handler so we can detect if the router's
	// error path fires (it must not, because RouterMiddleware should
	// return nil after the 419 has been written).
	errorHandlerFired := false
	r.ErrorHandler = func(ctx *router.Context, err error) {
		errorHandlerFired = true
		http.Error(ctx.Response, "Internal Server Error", http.StatusInternalServerError)
	}
	r.Use(c.RouterMiddleware())
	r.Post("/submit", func(ctx *router.Context) error {
		t.Fatal("inner handler must not be called on CSRF rejection")
		return nil
	})

	req := httptest.NewRequest("POST", "/submit", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != 419 {
		t.Fatalf("expected status 419, got %d", w.Code)
	}

	body := w.Body.String()
	if strings.Contains(body, "Internal Server Error") {
		t.Errorf("response body must not contain appended 'Internal Server Error' marker; got body=%q", body)
	}

	if errorHandlerFired {
		t.Error("router ErrorHandler must not fire after CSRF middleware writes 419")
	}

	// Body must be exactly the configured CSRF error message followed by a
	// single newline (the format http.Error writes). No trailing garbage.
	want := c.config.ErrorMessage + "\n"
	if body != want {
		t.Errorf("expected body %q, got %q", want, body)
	}
}

// newCSRFWithToken returns a CSRF instance pre-seeded with a token for
// "test-session". Returned values: instance, sessionID, valid token.
func newCSRFWithToken(t *testing.T) (*CSRF, string, string) {
	t.Helper()
	cfg := DefaultConfig()
	cfg.Store = stores.NewSessionStore()
	c := New(cfg)

	sessionID := "test-session"
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if err := c.config.Store.Set(sessionID, token); err != nil {
		t.Fatalf("store set: %v", err)
	}
	return c, sessionID, token
}

// TestGetTokenFromRequest_HeaderOnlyForMultipart pins the security
// regression: a multipart body must NOT be parsed by the CSRF
// middleware. Previously r.ParseForm would call ParseMultipartForm with
// a 32 MiB default memory limit, letting an unauthenticated attacker
// spike memory pre-validation. After the fix, token lookup for
// multipart MUST fall back to the header only; a token in the form
// part is ignored and the request is rejected.
func TestGetTokenFromRequest_HeaderOnlyForMultipart(t *testing.T) {
	c, sessionID, token := newCSRFWithToken(t)

	// Build a small multipart body that carries the valid token in the
	// _token form field. The server must NOT find it: multipart bodies
	// are off-limits for the CSRF lookup.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("_token", token); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close mw: %v", err)
	}

	req := httptest.NewRequest("POST", "/submit", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})

	handler := c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler must not be called: token in multipart form should be ignored")
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 419 {
		t.Fatalf("expected 419 for multipart with token in form, got %d", w.Code)
	}
}

// TestGetTokenFromRequest_MultipartTokenInHeaderPasses confirms that
// when a client sends a multipart body but puts the CSRF token in the
// header (as required), the request is accepted. The body is NOT
// parsed by the middleware.
func TestGetTokenFromRequest_MultipartTokenInHeaderPasses(t *testing.T) {
	c, sessionID, token := newCSRFWithToken(t)

	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	if err := mw.WriteField("payload", "hello"); err != nil {
		t.Fatalf("write field: %v", err)
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close mw: %v", err)
	}

	req := httptest.NewRequest("POST", "/submit", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", token)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})

	var called bool
	handler := c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Fatalf("expected handler to be called, got status %d", w.Code)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
}

// TestGetTokenFromRequest_UrlencodedSmallBodyPasses verifies the
// normal HTML-form flow still works: a small x-www-form-urlencoded
// body carrying the token in _token is accepted and that the body is
// restored so downstream handlers can re-read it.
func TestGetTokenFromRequest_UrlencodedSmallBodyPasses(t *testing.T) {
	c, sessionID, token := newCSRFWithToken(t)

	form := url.Values{}
	form.Set("_token", token)
	form.Set("name", "alice")

	req := httptest.NewRequest("POST", "/submit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})

	var called bool
	var bodySeen string
	handler := c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		b, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("downstream read body: %v", err)
		}
		bodySeen = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called {
		t.Fatalf("expected handler to be called, got status %d", w.Code)
	}
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	// Body must be restored verbatim so downstream handlers can re-read.
	if bodySeen != form.Encode() {
		t.Fatalf("body not restored for downstream: got %q want %q", bodySeen, form.Encode())
	}
}

// TestGetTokenFromRequest_HeaderWinsOverUrlencodedBody verifies the
// header is consulted first and the body is not even read when the
// header carries the token. This is the cheap path for XHR clients.
func TestGetTokenFromRequest_HeaderWinsOverUrlencodedBody(t *testing.T) {
	c, sessionID, token := newCSRFWithToken(t)

	// Body has garbage; header carries the real token. Request must pass
	// and the body must still be available downstream untouched.
	rawBody := "_token=NOT_A_TOKEN&name=alice"
	req := httptest.NewRequest("POST", "/submit", strings.NewReader(rawBody))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-CSRF-Token", token)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})

	var bodySeen string
	var called bool
	handler := c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
		b, _ := io.ReadAll(r.Body)
		bodySeen = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if !called || w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if bodySeen != rawBody {
		t.Fatalf("body must be untouched when header wins: got %q want %q", bodySeen, rawBody)
	}
}

// TestGetTokenFromRequest_UrlencodedOversizeRejected ensures an
// oversize x-www-form-urlencoded body returns 419 instead of being
// buffered into memory. The cap is 1 MiB; we send 2 MiB.
func TestGetTokenFromRequest_UrlencodedOversizeRejected(t *testing.T) {
	c, sessionID, _ := newCSRFWithToken(t)

	// Build a 2 MiB urlencoded body. The actual content does not matter;
	// the middleware must refuse to read past the cap.
	padding := strings.Repeat("a", 2<<20)
	form := url.Values{}
	form.Set("padding", padding)

	req := httptest.NewRequest("POST", "/submit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})

	handler := c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler must not be called: oversize body must be rejected")
	}))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != 419 {
		t.Fatalf("expected 419 for oversize urlencoded body, got %d", w.Code)
	}
}

// TestGetTokenFromRequest_DirectReturnsErrFormBodyTooLarge exercises
// the unit boundary: ErrFormBodyTooLarge bubbles out of
// getTokenFromRequest for oversize urlencoded bodies. This guards
// future refactors that might silently swallow the error.
func TestGetTokenFromRequest_DirectReturnsErrFormBodyTooLarge(t *testing.T) {
	c := New(DefaultConfig())
	form := url.Values{}
	form.Set("padding", strings.Repeat("a", 2<<20))

	req := httptest.NewRequest("POST", "/submit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	tok, err := c.getTokenFromRequest(req)
	if !errors.Is(err, ErrFormBodyTooLarge) {
		t.Fatalf("expected ErrFormBodyTooLarge, got token=%q err=%v", tok, err)
	}
}
