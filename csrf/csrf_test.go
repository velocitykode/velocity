package csrf

import (
	"bytes"
	"errors"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/csrf/stores"
	"github.com/velocitykode/velocity/router"
)

func TestNew(t *testing.T) {
	// Test with default-with-resolver config
	csrf := New(testConfig())
	if csrf == nil {
		t.Fatal("New returned nil")
	}

	// Test with custom config (resolver still required)
	config := &Config{
		HeaderName:        "X-Custom-Token",
		FormField:         "_custom_token",
		SessionIDResolver: testCookieResolver("session_id"),
	}
	csrf = New(config)
	if csrf.config.HeaderName != "X-Custom-Token" {
		t.Error("Config not applied correctly")
	}
}

func TestMiddleware_SafeMethods(t *testing.T) {
	csrf := New(testConfig())

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
	csrf := New(testConfig())

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
	config.SessionIDResolver = testCookieResolver("session_id")
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
	config.SessionIDResolver = testCookieResolver("session_id")
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
	config.SessionIDResolver = testCookieResolver("session_id")
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
	config.SessionIDResolver = testCookieResolver("session_id")
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
	config.SessionIDResolver = testCookieResolver("session_id")
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
	config.SessionIDResolver = testCookieResolver("session_id")
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
	config.SessionIDResolver = testCookieResolver("session_id")
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
	csrf := New(testConfig())
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
	config.SessionIDResolver = testCookieResolver("session_id")
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
	c := New(testConfig())

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

// TestNewE_RejectsNilResolver pins the regression for the legacy fallback
// where, when SessionIDResolver was nil, getSessionID returned the raw
// http.Cookie value as the binding key. That let an unauthenticated
// attacker mint a CSRF token against any self-chosen "session id" simply
// by sending Cookie: <name>=<arbitrary-string>; the cookie value never
// went through session-middleware authentication. NewE must refuse to
// construct a CSRF instance without an explicit resolver so the
// attacker-controlled-binding-key path cannot be reached.
func TestNewE_RejectsNilResolver(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SessionIDResolver = nil
	_, err := NewE(cfg)
	if err == nil {
		t.Fatal("expected error for nil SessionIDResolver")
	}
	if !errors.Is(err, ErrInsecureCSRFConfig) {
		t.Errorf("expected ErrInsecureCSRFConfig, got %v", err)
	}
}

// TestCSRF_RawCookieValueNeverTrustedAsSessionID pins the H-01 regression.
// A request that carries only a raw session_id cookie (no resolver wired)
// must NOT validate; pre-fix, the middleware's legacy fallback path read
// cookie.Value as the session ID and let an attacker bind a token to any
// self-chosen string by hitting RefreshHandler with the attacker's cookie.
// With NewE requiring a resolver, the only construction in this test must
// fail, and any synthetic construction (bypassing NewE) cannot reach the
// removed branch because getSessionID delegates unconditionally to the
// resolver.
func TestCSRF_RawCookieValueNeverTrustedAsSessionID(t *testing.T) {
	// Construct without resolver: NewE must reject.
	cfg := DefaultConfig()
	cfg.SessionIDResolver = nil
	if _, err := NewE(cfg); err == nil {
		t.Fatal("NewE must reject nil SessionIDResolver")
	}

	// Even when a resolver is wired, the middleware must NOT fall through
	// to reading the cookie value. Wire a resolver that rejects every
	// request; a raw session_id cookie must be ignored.
	cfg = DefaultConfig()
	cfg.Store = stores.NewSessionStore()
	cfg.SessionIDResolver = func(r *http.Request) (string, error) {
		return "", ErrNoSession
	}
	c, err := NewE(cfg)
	if err != nil {
		t.Fatalf("NewE: %v", err)
	}

	// Attacker pre-seeds a token in the store keyed by the literal cookie
	// value they intend to send. Pre-fix, the legacy fallback path would
	// have used this string as the binding key. The resolver now refuses
	// every request, so the seeded entry must NOT be reachable.
	const attackerKey = "attacker-chosen-session-id"
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if err := c.config.Store.Set(attackerKey, token); err != nil {
		t.Fatalf("seed token: %v", err)
	}

	handler := c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler must not run; raw cookie value MUST NOT be trusted as session ID")
	}))
	req := httptest.NewRequest("POST", "/submit", nil)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: attackerKey})
	req.Header.Set("X-CSRF-Token", token)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	if w.Code != 419 {
		t.Fatalf("expected 419 (resolver rejects), got %d", w.Code)
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
	c := New(testConfig())

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
	cfg.SessionIDResolver = testCookieResolver("session_id")
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
	c := New(testConfig())
	form := url.Values{}
	form.Set("padding", strings.Repeat("a", 2<<20))

	req := httptest.NewRequest("POST", "/submit", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	tok, err := c.getTokenFromRequest(req)
	if !errors.Is(err, ErrFormBodyTooLarge) {
		t.Fatalf("expected ErrFormBodyTooLarge, got token=%q err=%v", tok, err)
	}
}

// TestSessionIDResolver_PlaintextSessionIDAcrossEncryption is the
// regression test for two compounding bugs that caused 419 on the
// second state-changing request after a session-modifying response:
//
//  1. getSessionID used to return the raw Cookie value. When the
//     framework encrypts the session cookie, that value is the
//     ciphertext; every re-encrypt produces a fresh IV, so the CSRF
//     token store key rotated on every response and the next request
//     failed validation. The fix routes session-id lookup through an
//     injectable SessionIDResolver that returns the plaintext id.
//
//  2. session.Save() unconditionally re-encrypted on every response,
//     which made (1) trigger on every request — not just on session
//     modifications. Fixed in auth/drivers/session.
//
// This test exercises only the CSRF half (the session half has its
// own coverage). It simulates the encryption rotation by mutating the
// session cookie between requests while the resolver continues to
// return the stable plaintext id, and asserts both state-changing
// requests validate successfully.
func TestSessionIDResolver_PlaintextSessionIDAcrossEncryption(t *testing.T) {
	const plaintextID = "stable-plaintext-session-id"

	cfg := DefaultConfig()
	cfg.Store = stores.NewSessionStore()
	cfg.Secure = false // test env
	cfg.SessionIDResolver = func(r *http.Request) (string, error) {
		// Mimics auth.Manager.Session(r).ID(): plaintext id, stable
		// across cookie ciphertext rotations.
		if _, err := r.Cookie("session_id"); err != nil {
			return "", ErrNoSession
		}
		return plaintextID, nil
	}
	c := New(cfg)

	// Seed a valid token under the plaintext id.
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	if err := c.config.Store.Set(plaintextID, token); err != nil {
		t.Fatalf("store set: %v", err)
	}

	doRequest := func(cookieValue string) int {
		req := httptest.NewRequest("DELETE", "/servers/x", nil)
		req.Header.Set(c.config.HeaderName, token)
		req.AddCookie(&http.Cookie{Name: "session_id", Value: cookieValue})
		w := httptest.NewRecorder()
		c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(w, req)
		return w.Code
	}

	// Request 1: cookie carries the original ciphertext.
	if code := doRequest("v1:ciphertext-A"); code != http.StatusOK {
		t.Fatalf("request 1: expected 200, got %d", code)
	}

	// Request 2: cookie has rotated to a new ciphertext (different IV).
	// Pre-fix this caused a 419 because the token was keyed by the now-
	// stale ciphertext "v1:ciphertext-A". The resolver hides the rotation.
	if code := doRequest("v1:ciphertext-B-different-iv"); code != http.StatusOK {
		t.Fatalf("request 2 (post-rotation): expected 200, got %d", code)
	}
}

// TestSessionIDResolver_ErrorPropagation verifies a resolver returning
// ErrNoSession produces 419 and emits the SessionFallback event.
func TestSessionIDResolver_ErrorPropagation(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Store = stores.NewSessionStore()
	cfg.Secure = false
	cfg.SessionIDResolver = func(r *http.Request) (string, error) {
		return "", ErrNoSession
	}
	c := New(cfg)

	req := httptest.NewRequest("POST", "/x", nil)
	req.Header.Set(c.config.HeaderName, "anything")
	w := httptest.NewRecorder()
	c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler must not run when resolver returns ErrNoSession")
	})).ServeHTTP(w, req)
	if w.Code != 419 {
		t.Fatalf("expected 419, got %d", w.Code)
	}
}

// TestRotateToken_DeletesOldAndMintsNew pins the contract that the H-02
// session-guard hook depends on: after RotateToken(old, new), the old
// session id has no entry in the store, and the new id has a fresh,
// non-empty token. Without this, an orphan token bound to the pre-login
// session id would survive Session.Regenerate.
func TestRotateToken_DeletesOldAndMintsNew(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SessionIDResolver = testCookieResolver("session_id")
	cfg.Store = stores.NewSessionStore()
	c := New(cfg)

	const oldID = "pre-login-id"
	const newID = "post-regenerate-id"

	seed, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if err := c.config.Store.Set(oldID, seed); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := c.RotateToken(oldID, newID); err != nil {
		t.Fatalf("RotateToken: %v", err)
	}

	if _, err := c.config.Store.Get(oldID); err == nil {
		t.Error("RotateToken did not delete the old session's token; orphan remains in the store")
	}

	got, err := c.config.Store.Get(newID)
	if err != nil {
		t.Fatalf("RotateToken did not mint a new token under newID: %v", err)
	}
	if got == "" {
		t.Error("RotateToken stored empty token for newID")
	}
	if got == seed {
		t.Error("RotateToken reused the old token under newID; fresh token expected")
	}
}

// nonAtomicStore is a Store that intentionally does NOT implement
// AtomicConsumer. M-01 regression tests use it to drive the degraded
// fallback path and assert the operator-warning is emitted exactly once.
type nonAtomicStore struct {
	mu     sync.Mutex
	tokens map[string]string
}

func newNonAtomicStore() *nonAtomicStore { return &nonAtomicStore{tokens: make(map[string]string)} }

func (s *nonAtomicStore) Get(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.tokens[id]; ok {
		return v, nil
	}
	return "", errors.New("not found")
}
func (s *nonAtomicStore) Set(id string, token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tokens[id] = token
	return nil
}
func (s *nonAtomicStore) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.tokens, id)
	return nil
}
func (s *nonAtomicStore) Exists(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.tokens[id]
	return ok
}

// TestSingleUse_AtomicStore_ConcurrentValidate pins M-01: with an
// AtomicConsumer-capable store, exactly one of N concurrent unsafe
// requests carrying the same single-use token may pass. Pre-fix the
// per-process singleUseMu serialised within a single process but
// could not stop two replicas from both accepting the token; an
// atomic compare-and-delete store closes that hole. We model both
// replicas inside one process by sharing the same store across two
// CSRF instances.
func TestSingleUse_AtomicStore_ConcurrentValidate(t *testing.T) {
	const sessionID = "shared-session"
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	sharedStore := stores.NewSessionStore()
	if err := sharedStore.Set(sessionID, token); err != nil {
		t.Fatalf("seed: %v", err)
	}

	newReplica := func() *CSRF {
		cfg := DefaultConfig()
		cfg.SessionIDResolver = testCookieResolver("session_id")
		cfg.SingleUse = true
		cfg.Store = sharedStore // same store across both replicas
		return New(cfg)
	}
	replicaA := newReplica()
	replicaB := newReplica()

	const goroutines = 32
	var (
		wg          sync.WaitGroup
		successCnt  atomic.Int64
		rejectedCnt atomic.Int64
	)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest("POST", "/submit", nil)
			req.Header.Set("X-CSRF-Token", token)
			req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})
			w := httptest.NewRecorder()

			target := replicaA
			if i%2 == 1 {
				target = replicaB
			}
			handler := target.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				successCnt.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			handler.ServeHTTP(w, req)
			if w.Code == 419 {
				rejectedCnt.Add(1)
			}
		}(i)
	}
	wg.Wait()

	if successCnt.Load() != 1 {
		t.Errorf("expected exactly 1 success across %d goroutines, got %d", goroutines, successCnt.Load())
	}
	if rejectedCnt.Load() != goroutines-1 {
		t.Errorf("expected %d rejections, got %d", goroutines-1, rejectedCnt.Load())
	}

	// Token must be gone from the shared store after consumption.
	if _, err := sharedStore.Get(sessionID); err == nil {
		t.Error("single-use token must be removed from shared store after consume")
	}
}

// TestSingleUse_NonAtomicStore_EmitsWarningOnce pins the operator-warning
// half of M-01: when SingleUse is enabled and the Store does NOT
// implement AtomicConsumer, the middleware must emit a one-time warning
// so operators know their deployment is best-effort. The warning must
// NOT repeat on subsequent validations.
func TestSingleUse_NonAtomicStore_EmitsWarningOnce(t *testing.T) {
	const sessionID = "sess"
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	store := newNonAtomicStore()
	if err := store.Set(sessionID, token); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg := DefaultConfig()
	cfg.SessionIDResolver = testCookieResolver("session_id")
	cfg.SingleUse = true
	cfg.Store = store
	c := New(cfg)

	// Capture log output.
	var buf bytes.Buffer
	prevOut := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})

	do := func(tok string) int {
		req := httptest.NewRequest("POST", "/submit", nil)
		req.Header.Set("X-CSRF-Token", tok)
		req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})
		w := httptest.NewRecorder()
		c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})).ServeHTTP(w, req)
		return w.Code
	}

	// First validate: consumed (degraded path), warning emitted.
	if code := do(token); code != http.StatusOK {
		t.Fatalf("first validate: expected 200, got %d (log=%s)", code, buf.String())
	}
	if !strings.Contains(buf.String(), "Store does not implement AtomicConsumer") {
		t.Errorf("expected one-time warning on first single-use validate; log=%q", buf.String())
	}
	firstLog := buf.String()

	// Second validate: token is gone (deleted by first), so 419. Critical
	// assertion: warning must NOT repeat.
	if code := do(token); code != 419 {
		t.Fatalf("second validate: expected 419 (token consumed), got %d", code)
	}
	if buf.String() != firstLog {
		t.Errorf("warning must not repeat; first=%q second=%q", firstLog, buf.String())
	}
}

// TestSingleUse_WrongTokenLeavesEntry pins that an attacker submitting a
// wrong single-use token does NOT cause the legitimate token to be
// deleted via the AtomicConsumer path. Without this guarantee, an
// adversary who could observe POSTs could rapid-fire wrong tokens and
// either DoS the legitimate user or race the delete on the non-atomic
// fallback path.
func TestSingleUse_WrongTokenLeavesEntry(t *testing.T) {
	const sessionID = "sess"
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}

	store := stores.NewSessionStore()
	if err := store.Set(sessionID, token); err != nil {
		t.Fatalf("seed: %v", err)
	}

	cfg := DefaultConfig()
	cfg.SessionIDResolver = testCookieResolver("session_id")
	cfg.SingleUse = true
	cfg.Store = store
	c := New(cfg)

	// Wrong token rejected, store entry intact.
	req := httptest.NewRequest("POST", "/submit", nil)
	req.Header.Set("X-CSRF-Token", "bogus")
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})
	w := httptest.NewRecorder()
	c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("inner handler must not run for wrong token")
	})).ServeHTTP(w, req)
	if w.Code != 419 {
		t.Fatalf("expected 419 for wrong token, got %d", w.Code)
	}
	if _, err := store.Get(sessionID); err != nil {
		t.Fatalf("legitimate token must survive wrong-token attempt: %v", err)
	}

	// Right token now succeeds and consumes.
	req = httptest.NewRequest("POST", "/submit", nil)
	req.Header.Set("X-CSRF-Token", token)
	req.AddCookie(&http.Cookie{Name: "session_id", Value: sessionID})
	w = httptest.NewRecorder()
	c.Middleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 for right token, got %d", w.Code)
	}
}

// TestRevokeToken_DeletesEntry pins the contract that Logout depends on:
// after RevokeToken(id), the token bound to id is gone from the store.
// Without this, a captured cookie+token pair would remain valid for the
// store TTL (24h default) past logout.
func TestRevokeToken_DeletesEntry(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SessionIDResolver = testCookieResolver("session_id")
	cfg.Store = stores.NewSessionStore()
	c := New(cfg)

	const id = "live-session"
	token, err := GenerateToken()
	if err != nil {
		t.Fatalf("GenerateToken: %v", err)
	}
	if err := c.config.Store.Set(id, token); err != nil {
		t.Fatalf("seed: %v", err)
	}

	if err := c.RevokeToken(id); err != nil {
		t.Fatalf("RevokeToken: %v", err)
	}
	if _, err := c.config.Store.Get(id); err == nil {
		t.Error("RevokeToken did not delete the token; it must be unreachable after logout")
	}

	if err := c.RevokeToken("never-existed"); err != nil {
		t.Errorf("RevokeToken on missing id must be a no-op, got %v", err)
	}

	if err := c.RevokeToken(""); err != nil {
		t.Errorf("RevokeToken with empty id must be a no-op, got %v", err)
	}
}
