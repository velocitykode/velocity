package csrf

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/velocitykode/velocity/csrf/stores"
	"github.com/velocitykode/velocity/router"
)

var (
	ErrTokenMissing = errors.New("velocity/csrf: token missing")
	ErrTokenInvalid = errors.New("velocity/csrf: token invalid")
	ErrTokenExpired = errors.New("velocity/csrf: token expired")
	ErrNoStore      = errors.New("velocity/csrf: no token store configured")
)

// CSRF provides CSRF protection functionality
type CSRF struct {
	config      *Config
	singleUseMu sync.Mutex // serializes validate+delete for single-use tokens

	// eventDispatcher is optional; when set via SetEventDispatcher, the CSRF
	// instance emits events such as csrf.session_fallback.
	eventMu         sync.RWMutex
	eventDispatcher func(event interface{}) error
}

// New creates a new CSRF instance with the given configuration.
// Call Start() to begin any background goroutines required by the token store.
func New(config *Config) *CSRF {
	if config == nil {
		config = DefaultConfig()
	}

	// Set default store if none provided
	if config.Store == nil {
		store := stores.NewSessionStore(config.TokenLifetime)
		store.Start(context.Background())
		config.Store = store
	}

	return &CSRF{config: config}
}

// SetEventDispatcher wires an event dispatcher into the CSRF instance.
// Safe to call before or after requests are served; mutex-protected.
func (c *CSRF) SetEventDispatcher(fn func(event interface{}) error) {
	c.eventMu.Lock()
	c.eventDispatcher = fn
	c.eventMu.Unlock()
}

// dispatchEvent fires an event if a dispatcher is configured. Failures
// from the dispatcher are swallowed — CSRF validation must never fail
// because of an event sink.
func (c *CSRF) dispatchEvent(evt interface{}) {
	c.eventMu.RLock()
	fn := c.eventDispatcher
	c.eventMu.RUnlock()
	if fn == nil {
		return
	}
	_ = fn(evt)
}

// Middleware returns HTTP middleware that validates CSRF tokens
func (c *CSRF) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip safe methods (GET, HEAD, OPTIONS, TRACE)
		if isSafeMethod(r.Method) {
			next.ServeHTTP(w, r)
			return
		}

		// Check exclusions
		if c.isExcluded(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Validate token
		if err := c.validateToken(r); err != nil {
			c.handleError(w, r, err)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// RouterMiddleware returns a router.MiddlewareFunc that validates CSRF tokens.
// This is the instance-based alternative to the global Middleware() function.
func (c *CSRF) RouterMiddleware() router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(ctx *router.Context) error {
			// Track whether the inner handler was called (CSRF passed)
			var called bool
			var handlerErr error
			inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				called = true
				ctx.Response = w
				ctx.Request = r
				handlerErr = next(ctx)
			})
			c.Middleware(inner).ServeHTTP(ctx.Response, ctx.Request)
			if !called {
				log.Printf("velocity/csrf: request blocked for %s %s", ctx.Request.Method, ctx.Request.URL.Path)
				return fmt.Errorf("velocity/csrf: request rejected for %s %s", ctx.Request.Method, ctx.Request.URL.Path)
			}
			return handlerErr
		}
	}
}

// validateToken validates the CSRF token in the request
func (c *CSRF) validateToken(r *http.Request) error {
	// Get token from request
	requestToken := c.getTokenFromRequest(r)
	if requestToken == "" {
		return ErrTokenMissing
	}

	// Get expected token from store
	sessionID, err := c.getSessionID(r)
	if err != nil {
		return err
	}

	// For single-use tokens, serialize validate+delete to prevent race conditions
	// where concurrent requests could both validate the same token before deletion.
	if c.config.SingleUse {
		c.singleUseMu.Lock()
		defer c.singleUseMu.Unlock()
	}

	expectedToken, err := c.config.Store.Get(sessionID)
	if err != nil {
		return ErrTokenInvalid
	}

	// Validate tokens via constant-time comparison (ValidateToken uses
	// crypto/subtle internally).
	if !ValidateToken(requestToken, expectedToken) {
		return ErrTokenInvalid
	}

	// Handle single-use tokens
	if c.config.SingleUse {
		if err := c.config.Store.Delete(sessionID); err != nil {
			log.Printf("velocity/csrf: failed to delete single-use token for session %s: %v", sessionID, err)
		}
	}

	return nil
}

// getTokenFromRequest extracts the CSRF token from the request
func (c *CSRF) getTokenFromRequest(r *http.Request) string {
	// Try header first
	if token := r.Header.Get(c.config.HeaderName); token != "" {
		return token
	}

	// Try form field
	if err := r.ParseForm(); err == nil {
		if token := r.FormValue(c.config.FormField); token != "" {
			return token
		}
	}

	return ""
}

// getSessionID extracts the session ID from the request.
// When no session cookie is present, a one-shot ephemeral ID is generated
// and a csrf.session_fallback event is dispatched so operators can see that
// the session middleware is missing or misconfigured.
func (c *CSRF) getSessionID(r *http.Request) (string, error) {
	// Use configured session cookie name
	cookieName := c.config.SessionCookieName
	if cookieName == "" {
		cookieName = "session_id"
	}

	// Try to get session ID from cookie
	cookie, err := r.Cookie(cookieName)
	if err == nil {
		return cookie.Value, nil
	}

	// Emit an event so operators can detect missing session middleware
	// rather than silently relying on a new per-request ID.
	c.dispatchEvent(&SessionFallback{
		Context: r.Context(),
		Path:    r.URL.Path,
		Method:  r.Method,
		At:      time.Now(),
	})

	// Generate a random per-request identifier instead of using RemoteAddr.
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("velocity/csrf: failed to generate session id: %w", err)
	}
	return "temp-" + hex.EncodeToString(b), nil
}

// isExcluded checks if the request should be excluded from CSRF protection
func (c *CSRF) isExcluded(r *http.Request) bool {
	// Check path exclusions
	for _, path := range c.config.ExcludePaths {
		if matchPath(r.URL.Path, path) {
			return true
		}
	}

	// Check custom exclusion function
	if c.config.ExcludeFunc != nil {
		return c.config.ExcludeFunc(r)
	}

	return false
}

// handleError handles CSRF validation errors
func (c *CSRF) handleError(w http.ResponseWriter, r *http.Request, err error) {
	// Custom error handler
	if c.config.ErrorHandler != nil {
		c.config.ErrorHandler(w, r, err)
		return
	}

	// JSON API response
	if isJSONRequest(r) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(419)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"code":    419,
			"message": c.config.ErrorMessage,
		})
		return
	}

	// HTML response
	http.Error(w, c.config.ErrorMessage, 419)
}

// RefreshHandler returns a handler that generates and returns a new CSRF token
func (c *CSRF) RefreshHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sessionID, err := c.getSessionID(r)
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		// Generate new token
		token, err := GenerateToken()
		if err != nil {
			http.Error(w, "Failed to generate token", http.StatusInternalServerError)
			return
		}

		// Store token
		if err := c.config.Store.Set(sessionID, token); err != nil {
			http.Error(w, "Failed to store token", http.StatusInternalServerError)
			return
		}

		// Return token as JSON
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token": token,
		})
	}
}

// GetToken retrieves or generates a token for the given session ID
func (c *CSRF) GetToken(sessionID string) (string, error) {
	if c.config.Store == nil {
		return "", ErrNoStore
	}

	// Try to get existing token
	token, err := c.config.Store.Get(sessionID)
	if err == nil {
		return token, nil
	}

	// Generate new token
	token, err = GenerateToken()
	if err != nil {
		return "", err
	}

	// Store token
	if err := c.config.Store.Set(sessionID, token); err != nil {
		return "", err
	}

	return token, nil
}

// Helper functions

func isSafeMethod(method string) bool {
	return method == "GET" || method == "HEAD" || method == "OPTIONS"
}

func matchPath(path, pattern string) bool {
	// Support wildcards
	if strings.HasSuffix(pattern, "*") {
		prefix := strings.TrimSuffix(pattern, "*")
		return strings.HasPrefix(path, prefix)
	}
	return path == pattern
}

// FromContext extracts the *CSRF from a router.Context.
// Returns nil if CSRF is not configured.
func FromContext(ctx *router.Context) *CSRF {
	c, _ := ctx.CSRF().(*CSRF)
	return c
}

func isJSONRequest(r *http.Request) bool {
	contentType := r.Header.Get("Content-Type")
	accept := r.Header.Get("Accept")
	return strings.Contains(contentType, "application/json") ||
		strings.Contains(accept, "application/json")
}

// constantTimeEq is exposed here for package-local use; it simply wraps
// subtle.ConstantTimeCompare for clarity. Tests rely on the underlying
// behaviour directly via ValidateToken.
func constantTimeEq(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
