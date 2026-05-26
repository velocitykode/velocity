package csrf

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/csrf/stores"
	"github.com/velocitykode/velocity/router"
)

// maxFormBodyForCSRFLookup caps the amount of an x-www-form-urlencoded
// request body the CSRF middleware will buffer while looking for a token.
// An unauthenticated attacker can otherwise force the middleware to read
// (and ParseMultipartForm to write to disk / hold in memory) an arbitrarily
// large body before any auth check runs. 1 MiB is generous for hidden
// _token fields and still bounds resource use. Multipart bodies are never
// parsed here, the token MUST come from a header.
const maxFormBodyForCSRFLookup = 1 << 20 // 1 MiB

// ErrFormBodyTooLarge is returned by getTokenFromRequest when the
// x-www-form-urlencoded body exceeds maxFormBodyForCSRFLookup. Callers
// surface this as 419 to the client; the body is not drained further.
var ErrFormBodyTooLarge = errors.New("velocity/csrf: form body exceeds CSRF lookup limit")

var (
	ErrTokenMissing = errors.New("velocity/csrf: token missing")
	ErrTokenInvalid = errors.New("velocity/csrf: token invalid")
	ErrTokenExpired = errors.New("velocity/csrf: token expired")
	ErrNoStore      = errors.New("velocity/csrf: no token store configured")
	// ErrNoSession is returned when ModeSession is active but the request
	// carries no session cookie. Previously the middleware generated a
	// per-request ephemeral ID here, which let an attacker bind a CSRF
	// token to any self-chosen session ID and replay it. The middleware
	// now refuses to issue or validate tokens without a real session.
	ErrNoSession = errors.New("velocity/csrf: session cookie required for ModeSession")
)

// CSRF provides CSRF protection functionality
type CSRF struct {
	config      *Config
	singleUseMu sync.Mutex // serializes validate+delete for single-use tokens

	// eventDispatcher is optional; when set via SetEventDispatcher, the CSRF
	// instance emits events such as csrf.session_fallback.
	eventMu         sync.RWMutex
	eventDispatcher func(ctx context.Context, event interface{}) error
}

// New creates a new CSRF instance with the given configuration.
// Call Start() to begin any background goroutines required by the token store.
//
// New rejects Mode values other than ModeSession at construction time;
// ModeDoubleSubmit is reserved for a future implementation. Use NewE for
// fail-fast boot-time validation of the full Config.
func New(config *Config) *CSRF {
	c, err := NewE(config)
	if err != nil {
		// Preserve existing callers' expectation of a non-nil *CSRF;
		// panic on unsupported modes (unrecoverable startup config).
		panic(err)
	}
	return c
}

// NewE is the error-returning constructor. Prefer this in app bootstrap
// so mis-set Config.Mode surfaces as a return error rather than a panic.
//
// SessionIDResolver MUST be non-nil. The resolver is the binding-key
// boundary between an attacker-controlled cookie value and the CSRF token
// store. Allowing a nil resolver re-opened a legacy code path that keyed
// tokens by the raw cookie value, which let an unauthenticated attacker
// mint tokens against a self-chosen session ID. Bootstrap code in app.go
// installs an encrypted-session resolver by default; consumers wiring CSRF
// directly must supply one explicitly.
func NewE(config *Config) (*CSRF, error) {
	if config == nil {
		config = DefaultConfig()
	}

	// Only ModeSession is currently implemented. Silently accepting
	// ModeDoubleSubmit would be worse than rejecting it - apps would
	// believe they had CSRF protection that was never wired.
	if config.Mode != ModeSession {
		return nil, fmt.Errorf("%w: Mode=%s is not yet implemented; use ModeSession", ErrInsecureCSRFConfig, config.Mode)
	}

	if config.SessionIDResolver == nil {
		return nil, fmt.Errorf("%w: SessionIDResolver is required; raw cookie value MUST NOT be used as the CSRF binding key", ErrInsecureCSRFConfig)
	}

	// Set default store if none provided
	if config.Store == nil {
		store := stores.NewSessionStore(config.TokenLifetime)
		store.Start(context.Background())
		config.Store = store
	}

	return &CSRF{config: config}, nil
}

// Shutdown releases resources held by the CSRF token store. If the
// underlying store implements contract.ShutdownAware (Shutdown(ctx) error)
// it is invoked; otherwise Shutdown is a no-op. The context deadline is
// honoured.
func (c *CSRF) Shutdown(ctx context.Context) error {
	if c == nil || c.config == nil || c.config.Store == nil {
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
	if sd, ok := c.config.Store.(contract.ShutdownAware); ok {
		return sd.Shutdown(ctx)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

// SetEventDispatcher wires an event dispatcher into the CSRF instance.
// Safe to call before or after requests are served; mutex-protected.
func (c *CSRF) SetEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	c.eventMu.Lock()
	c.eventDispatcher = fn
	c.eventMu.Unlock()
}

// dispatchEvent fires an event if a dispatcher is configured. The
// caller-supplied ctx is propagated so listeners observe request-scoped
// values. Failures from the dispatcher are swallowed: CSRF validation
// must never fail because of an event sink.
func (c *CSRF) dispatchEvent(ctx context.Context, evt interface{}) {
	c.eventMu.RLock()
	fn := c.eventDispatcher
	c.eventMu.RUnlock()
	if fn == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	_ = fn(ctx, evt)
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
				// CSRF middleware already wrote the 419 response (via
				// handleError). Returning a non-nil error here would
				// trigger the router's ErrorHandler, which calls
				// http.Error and appends "Internal Server Error\n" to
				// the body. The status is guarded by responseWriter
				// but the body is not. Return nil so the router skips
				// its error path; the rejection is already fully sent.
				log.Printf("velocity/csrf: request blocked for %s %s", ctx.Request.Method, ctx.Request.URL.Path)
				return nil
			}
			return handlerErr
		}
	}
}

// validateToken validates the CSRF token in the request
func (c *CSRF) validateToken(r *http.Request) error {
	// Get token from request
	requestToken, err := c.getTokenFromRequest(r)
	if err != nil {
		return err
	}
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

// getTokenFromRequest extracts the CSRF token from the request.
//
// Token lookup order:
//  1. Header (always checked first, no body read).
//  2. application/x-www-form-urlencoded body, capped at
//     maxFormBodyForCSRFLookup. The body is buffered and restored so
//     downstream handlers can re-read it.
//
// Multipart bodies are NEVER parsed by the CSRF middleware. The standard
// library's r.ParseForm / r.FormValue would call ParseMultipartForm with
// a 32 MiB default memory limit, which lets an unauthenticated attacker
// spike memory (or fill the temp directory) before any CSRF check runs.
// Clients submitting multipart forms must send the token in the configured
// header.
//
// Returns ErrFormBodyTooLarge when the urlencoded body exceeds the cap.
// The middleware translates that into a 419 response without draining
// the rest of the body.
func (c *CSRF) getTokenFromRequest(r *http.Request) (string, error) {
	// Try header first; this is always safe and never reads the body.
	if token := r.Header.Get(c.config.HeaderName); token != "" {
		return token, nil
	}

	// No body, nothing else to inspect.
	if r.Body == nil || r.Body == http.NoBody {
		return "", nil
	}

	// Determine media type. parseMediaType handles parameters/charset.
	ct := r.Header.Get("Content-Type")
	if ct == "" {
		return "", nil
	}
	mediaType, _, err := mime.ParseMediaType(ct)
	if err != nil {
		// Malformed Content-Type. Refuse to parse a body for it.
		return "", nil
	}

	// Multipart bodies are off-limits. Token must come from the header.
	if mediaType != "application/x-www-form-urlencoded" {
		return "", nil
	}

	// Read up to the cap+1 so we can detect overflow distinctly from "fits
	// exactly". http.MaxBytesReader would set a 413 on the writer if we
	// passed one in, which is not what we want here; we just need a bound.
	limited := io.LimitReader(r.Body, maxFormBodyForCSRFLookup+1)
	buf, readErr := io.ReadAll(limited)
	// Always restore the body, even on error, so downstream handlers and
	// the deferred Close on the original body still behave.
	origBody := r.Body
	r.Body = struct {
		io.Reader
		io.Closer
	}{
		Reader: bytes.NewReader(buf),
		Closer: origBody,
	}
	if readErr != nil {
		// Treat read errors as "no token"; the handler will likely fail
		// downstream with its own body-read error.
		return "", nil
	}
	if len(buf) > maxFormBodyForCSRFLookup {
		return "", ErrFormBodyTooLarge
	}

	values, err := url.ParseQuery(string(buf))
	if err != nil {
		return "", nil
	}
	return values.Get(c.config.FormField), nil
}

// getSessionID extracts the session ID from the request for ModeSession.
// Returns ErrNoSession when no session cookie is present. NewE guarantees
// SessionIDResolver is non-nil; the legacy fallback that read the raw
// cookie value as the session ID was removed because it let an
// unauthenticated attacker mint tokens bound to any self-chosen string.
// A csrf.session_fallback event is dispatched on ErrNoSession so operators
// can detect requests arriving without a session.
func (c *CSRF) getSessionID(r *http.Request) (string, error) {
	id, err := c.config.SessionIDResolver(r)
	if err != nil {
		if errors.Is(err, ErrNoSession) {
			c.dispatchEvent(r.Context(), &SessionFallback{
				Context: r.Context(),
				Path:    r.URL.Path,
				Method:  r.Method,
				At:      time.Now(),
			})
		}
		return "", err
	}
	if id == "" {
		c.dispatchEvent(r.Context(), &SessionFallback{
			Context: r.Context(),
			Path:    r.URL.Path,
			Method:  r.Method,
			At:      time.Now(),
		})
		return "", ErrNoSession
	}
	return id, nil
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
			// ErrNoSession means the request has no session cookie, so no
			// token can be bound. Return 400 (client misconfiguration),
			// not 500 (server error) — the server is behaving correctly.
			if errors.Is(err, ErrNoSession) {
				http.Error(w, "session required to issue CSRF token", http.StatusBadRequest)
				return
			}
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

// RotateToken implements contract.CSRFTokenRotator. It deletes any token
// bound to oldID and mints a fresh token bound to newID. Session guards
// call this from Login (immediately after Session.Regenerate) and from
// the remember-cookie revival path so the token follows the session-id
// rotation, closing the orphan-token window described in the H-02 audit
// finding. oldID may be empty (first login on a fresh session); newID
// must be non-empty.
//
// A delete error on oldID is logged but does NOT abort the rotation: the
// new token must still be installed under newID so the post-login request
// has a valid token to validate against. A set error on newID IS returned
// so the caller can surface it (Login aborts on token-mint failure).
func (c *CSRF) RotateToken(oldID, newID string) error {
	if c == nil || c.config == nil || c.config.Store == nil {
		return ErrNoStore
	}
	if newID == "" {
		return fmt.Errorf("velocity/csrf: RotateToken: newID is required")
	}
	if oldID != "" && oldID != newID {
		if err := c.config.Store.Delete(oldID); err != nil {
			log.Printf("velocity/csrf: RotateToken: delete old token for session %s failed: %v", oldID, err)
		}
	}
	token, err := GenerateToken()
	if err != nil {
		return fmt.Errorf("velocity/csrf: RotateToken: generate: %w", err)
	}
	if err := c.config.Store.Set(newID, token); err != nil {
		return fmt.Errorf("velocity/csrf: RotateToken: store set: %w", err)
	}
	return nil
}

// RevokeToken implements contract.CSRFTokenRotator. It deletes the token
// bound to id. Session guards call this from Logout (before
// Session.Invalidate) so the token does not survive the session in the
// CSRF store; without this a captured cookie+token pair would remain
// valid for the store TTL (24h default) past logout.
//
// A delete on a missing entry is not an error (idempotent), matching the
// underlying Store.Delete contract.
func (c *CSRF) RevokeToken(id string) error {
	if c == nil || c.config == nil || c.config.Store == nil {
		return ErrNoStore
	}
	if id == "" {
		return nil
	}
	return c.config.Store.Delete(id)
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
