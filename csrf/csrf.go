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
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/csrf/stores"
	"github.com/velocitykode/velocity/router"
)

// ErrFormBodyTooLarge is returned by getTokenFromRequest when the
// x-www-form-urlencoded body exceeds Config.MaxFormBodyBytes. Callers
// surface this as 419 to the client; the request is NOT forwarded to
// the downstream handler so no truncated prefix is observable past the
// middleware boundary.
var ErrFormBodyTooLarge = errors.New("velocity/csrf: form body exceeds CSRF lookup limit")

// xsrfHeaderName is the request header axios/Angular-style clients use to
// echo the XSRF-TOKEN cookie value. It is intentionally not configurable:
// the cookie/header pair is a cross-client convention, and operators who
// need a custom header already have Config.HeaderName.
const xsrfHeaderName = "X-XSRF-TOKEN"

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
	config *Config
	// singleUseMu serializes validate+delete for single-use tokens when the
	// configured Store does NOT implement AtomicConsumer. With multiple
	// replicas behind a Redis-backed store, this per-process lock cannot
	// prevent replica A and replica B from both accepting the same token
	// simultaneously; the cross-process gate must come from the store's
	// own atomic compare-and-delete primitive (AtomicConsumer). When the
	// store lacks that primitive, the middleware logs a one-time warning
	// (singleUseDegradedLogged) so operators know the deployment is
	// single-use-best-effort rather than single-use-exact.
	singleUseMu             sync.Mutex
	singleUseDegradedLogged atomic.Bool

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
		// Attach the request-scoped token cache BEFORE any downstream
		// reader can observe the request. Both the safe-method
		// bootstrap path (which mints the XSRF-TOKEN cookie via
		// c.GetToken) and the unsafe-method validation path benefit:
		// any handler that calls csrf.TokenForRequest(r) downstream
		// gets a memoised token instead of re-paying Store.Get. See
		// request_token.go for the cache contract.
		//
		// We MUST replace the request pointer so the handler chain
		// inherits the augmented context. WithTokenState is cheap
		// (one allocation per request) and idempotent: nested
		// middleware stacks that re-enter Middleware will shadow the
		// outer state with their own, which is the desired behaviour
		// when a consumer wires a custom CSRF instance under a
		// sub-path mounted under the framework default.
		r = r.WithContext(withTokenState(r.Context(), c))

		// Skip safe methods (GET, HEAD, OPTIONS, TRACE).
		// Safe methods are also the bootstrap point for the XSRF-TOKEN
		// cookie: SPA clients (axios, fetch) expect a non-HttpOnly
		// cookie they can read and echo as X-XSRF-TOKEN on unsafe
		// requests. We write that cookie before delegating to the
		// handler so the body never sees it pre-empted by a Set-Cookie
		// race.
		if isSafeMethod(r.Method) {
			c.maybeWriteXSRFCookie(w, r)
			next.ServeHTTP(w, r)
			return
		}

		// Check exclusions
		if c.isExcluded(r) {
			next.ServeHTTP(w, r)
			return
		}

		// Testing-environment bypass, mirroring Laravel's
		// PreventRequestForgery.runningUnitTests() short-circuit: when this
		// instance's configured Env names a test profile, unsafe requests are
		// exempt from token validation so HTTP feature tests drive mutating
		// routes without a token round-trip. Keyed on c.config.Env (the app's
		// configured environment, captured at construction) - NOT a per-request
		// os.Getenv - so it is opt-in per instance: a Config built directly
		// (csrf's own unit tests, any caller that does not set Env) leaves Env
		// "" and still enforces, even under `APP_ENV=testing go test ./csrf`.
		// Fail-secure: contract.IsTestingEnv recognises only "test"/"testing";
		// every other value (unset, "dev", "local", "production", a typo)
		// enforces.
		if contract.IsTestingEnv(c.config.Env) {
			next.ServeHTTP(w, r)
			return
		}

		// Validate token. Pass w so getTokenFromRequest can wrap the
		// body with http.MaxBytesReader, which lets the standard
		// library handle oversize bodies cleanly (returns *MaxBytesError
		// once the cap is exceeded; we surface 419 without truncating
		// the request downstream).
		if err := c.validateToken(w, r); err != nil {
			c.handleError(w, r, err)
			return
		}

		next.ServeHTTP(w, r)
	})
}

// maybeWriteXSRFCookie writes a non-HttpOnly XSRF-TOKEN cookie carrying
// the per-session CSRF token for the request's session, IF a session is
// resolvable. Used by the safe-method bootstrap path inside Middleware.
//
// Secure is true when the request is HTTPS or Config.Secure is set.
// See writeXSRFCookieForSession for the cookie attribute details.
func (c *CSRF) maybeWriteXSRFCookie(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.config == nil {
		return
	}
	sessionID, err := c.getSessionIDQuiet(r)
	if err != nil || sessionID == "" {
		// No session bound to this request - nothing to write. The
		// quiet variant suppresses the SessionFallback event because
		// this is the safe-method bootstrap path, not an enforcement
		// boundary.
		return
	}
	// Route through the request-scoped token cache so the bond shared-
	// props function and any downstream TokenForRequest reader observe
	// the exact same token value that we URL-encode into the cookie.
	// Without this, the cookie and the page-prop could diverge if the
	// store mints the token twice (transient inconsistency, or a slow
	// race between Get and Set). See request_token.go.
	c.writeXSRFCookieForSession(w, r, sessionID, r.TLS != nil || c.config.Secure)
}

// WriteXSRFCookie writes the XSRF-TOKEN cookie for sessionID to w. It
// is the public hook session schemes call after RotateToken so the
// response that establishes the new session also carries the freshly
// minted CSRF token; without this the SPA's first POST after Login (or
// remember-cookie revival) returns 419 because the per-session token
// is in the store but the client has no way to read it.
//
// Returns silently when w is nil, when sessionID is empty, when the
// CSRF cookie write is disabled (WriteXSRFCookie=false), or when
// SingleUse is enabled (the cookie value would go stale on the next
// unsafe request that consumes the token).
//
// Secure: this is the post-rotation write so we cannot read r.TLS. The
// cookie is marked Secure=true unconditionally; HTTP-only dev
// deployments must either disable WriteXSRFCookie or rely on the
// safe-method bootstrap path (which has the request in hand and so can
// downgrade Secure appropriately).
func (c *CSRF) WriteXSRFCookie(w http.ResponseWriter, sessionID string) {
	if c == nil || c.config == nil || w == nil || sessionID == "" {
		return
	}
	// Post-rotation write: no request in hand, so we cannot route
	// through the request-scoped cache. This call site does not
	// observe the drift surface the cache exists to close (it runs
	// once after RotateToken, not paired with a sharePropsFunc read),
	// so a direct GetToken is fine.
	c.writeXSRFCookieForSession(w, nil, sessionID, true)
}

// writeXSRFCookieForSession is the shared body used by both
// maybeWriteXSRFCookie (safe-method bootstrap, knows request scheme) and
// WriteXSRFCookie (post-rotation, secure assumed). Both opt-out guards
// (WriteXSRFCookie=false, SingleUse=true) are applied here.
//
// When r is non-nil and the request carries a TokenForRequest cache
// (attached by the CSRF middleware), the token lookup goes through the
// cache so the cookie value and any downstream TokenForRequest reader
// (sharePropsFunc, template helper) agree byte-for-byte. When r is nil
// (post-rotation WriteXSRFCookie call site), fall back to direct
// Store.Get via GetToken.
func (c *CSRF) writeXSRFCookieForSession(w http.ResponseWriter, r *http.Request, sessionID string, secure bool) {
	if !c.config.WriteXSRFCookie {
		return
	}
	if c.config.SingleUse {
		// Single-use tokens cannot be safely echoed via cookie: they
		// would be consumed by the next unsafe request and the cookie
		// value would be stale for any subsequent JS-driven request.
		return
	}
	if c.config.Store == nil {
		return
	}
	var (
		token string
		err   error
	)
	if r != nil {
		// Route through the request-scoped cache. The session id
		// argument is implied (TokenForRequest resolves it from the
		// same SessionIDResolver), so the (sessionID, cached token)
		// pair stays internally consistent. The cache already holds
		// the per-response masked form, so no further masking here.
		token, err = TokenForRequest(r)
	} else {
		token, err = c.GetToken(sessionID)
		if err == nil && token != "" {
			// No request-scoped cache on this path; mask at the sink
			// so the cookie never carries the raw stored token.
			token, err = MaskToken(token)
		}
	}
	if err != nil || token == "" {
		return
	}
	cookieName := c.config.XSRFCookieName
	if cookieName == "" {
		cookieName = "XSRF-TOKEN"
	}
	// MaxAge in seconds; clamp to int range. TokenLifetime <= 0 means
	// session cookie (no MaxAge set).
	maxAge := 0
	if ttl := c.config.TokenLifetime; ttl > 0 {
		secs := int64(ttl / time.Second)
		if secs > 0 {
			if secs > int64(int(^uint(0)>>1)) {
				maxAge = int(^uint(0) >> 1)
			} else {
				maxAge = int(secs)
			}
		}
	}
	cookie := &http.Cookie{
		Name: cookieName,
		// URL-encode so axios-style clients can echo the value
		// verbatim in X-XSRF-TOKEN without double-encoding.
		Value:    url.QueryEscape(token),
		Path:     "/",
		HttpOnly: false, // SPAs must read this
		Secure:   secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   maxAge,
	}
	http.SetCookie(w, cookie)
}

// getSessionIDQuiet resolves the session id without dispatching a
// SessionFallback event. Used by the XSRF-TOKEN cookie bootstrap path
// where the absence of a session is normal (anonymous GET) rather than
// a CSRF policy violation.
func (c *CSRF) getSessionIDQuiet(r *http.Request) (string, error) {
	if c.config.SessionIDResolver == nil {
		return "", ErrNoSession
	}
	id, err := c.config.SessionIDResolver(r)
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", ErrNoSession
	}
	return id, nil
}

// RouterMiddleware returns a router.MiddlewareFunc that validates CSRF tokens.
// This is the instance-based alternative to the global Middleware() function.
//
// It delegates to router.CSRFMiddleware so the two adapters cannot drift:
// both reuse the ORIGINAL *router.Context (preserving validateFn, fileRoot,
// intendedFn) and capture the request as csrf.Middleware augmented it.
// Middleware passes the ResponseWriter through unwrapped, so the Context's
// Response field needs no reassignment.
func (c *CSRF) RouterMiddleware() router.MiddlewareFunc {
	return router.CSRFMiddleware(c)
}

// validateToken validates the CSRF token in the request.
//
// Single-use semantics: when SingleUse is enabled and the configured Store
// implements AtomicConsumer, validation and deletion happen as one atomic
// cross-process operation. This is the only path that closes the multi-
// replica race where two replicas could each accept the same token in the
// same instant. When the store lacks AtomicConsumer, the middleware falls
// back to per-process serialization (singleUseMu) and logs a one-time
// warning so operators know their deployment is single-use-best-effort.
func (c *CSRF) validateToken(w http.ResponseWriter, r *http.Request) error {
	// Get token from request
	requestToken, err := c.getTokenFromRequest(w, r)
	if err != nil {
		return err
	}
	if requestToken == "" {
		return ErrTokenMissing
	}

	// Emission points wrap the token in a per-response mask (MaskToken)
	// so identical bytes never repeat across responses. Clients echo
	// whatever they were given, so the request may carry either form:
	// masked (decodes to exactly 2x the token length, unwrapped here) or
	// raw framework-length (legacy clients / values captured before
	// masking shipped, passed through unchanged). Anything else - bad
	// base64, truncated or wrong-length masked encodings - is malformed
	// and treated exactly like an absent token, before any store lookup,
	// so a custom ErrorHandler cannot distinguish a garbled submission
	// from no submission. For the two accepted forms, decoding decides
	// nothing; the terminal accept/reject check remains the constant-
	// time comparison below (ValidateToken / the store's ConsumeIfMatch).
	requestToken, encoding := decodeRequestToken(requestToken)
	if encoding == encodingMalformed {
		return ErrTokenMissing
	}

	// Get expected token from store
	sessionID, err := c.getSessionID(r)
	if err != nil {
		return err
	}

	// Fast path for single-use tokens when the store supports an atomic
	// compare-and-delete. This is the only primitive that prevents two
	// replicas from accepting the same token simultaneously.
	if c.config.SingleUse {
		if consumer, ok := c.config.Store.(AtomicConsumer); ok {
			consumed, err := consumer.ConsumeIfMatch(sessionID, requestToken)
			if err != nil {
				log.Printf("velocity/csrf: ConsumeIfMatch failed for session %s: %v", sessionID, err)
				return ErrTokenInvalid
			}
			if !consumed {
				return ErrTokenInvalid
			}
			return nil
		}
		// Store cannot enforce cross-process single-use. Warn once,
		// then fall through to per-process serialize+validate+delete.
		// Multi-replica deployments with this code path MUST migrate to
		// an AtomicConsumer-capable store; the per-process mutex below
		// only protects within a single process.
		if c.singleUseDegradedLogged.CompareAndSwap(false, true) {
			log.Printf("velocity/csrf: WARNING SingleUse enabled but Store does not implement AtomicConsumer; cross-process single-use is best-effort only and a token may be accepted by multiple replicas concurrently")
		}
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

	// Handle single-use tokens (degraded path - store lacks AtomicConsumer).
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
//  1. Configured header (Config.HeaderName, always checked first, no
//     body read).
//  2. X-XSRF-TOKEN header (axios/Angular convention). These clients echo
//     the XSRF-TOKEN cookie value verbatim, and the cookie is written
//     URL-encoded (see writeXSRFCookieForSession), so the value is
//     url.QueryUnescape'd here. A value that fails to unescape is treated
//     as absent (fall through to the form field), never as an error.
//  3. application/x-www-form-urlencoded body, capped at
//     Config.MaxFormBodyBytes (default 1 MiB). The body is wrapped with
//     http.MaxBytesReader so an oversize body errors cleanly at read
//     time without ever buffering the prefix downstream. On successful
//     read the body is restored as a bytes.NewReader so the handler can
//     re-read it.
//
// Multipart bodies are NEVER parsed by the CSRF middleware. The standard
// library's r.ParseForm / r.FormValue would call ParseMultipartForm with
// a 32 MiB default memory limit, which lets an unauthenticated attacker
// spike memory (or fill the temp directory) before any CSRF check runs.
// Clients submitting multipart forms must send the token in the configured
// header.
//
// Returns ErrFormBodyTooLarge when the urlencoded body exceeds
// Config.MaxFormBodyBytes. The middleware translates that into a 419
// response and does NOT call the downstream handler, so no truncated
// prefix is observable past the middleware boundary.
func (c *CSRF) getTokenFromRequest(w http.ResponseWriter, r *http.Request) (string, error) {
	// Try header first; this is always safe and never reads the body.
	if token := r.Header.Get(c.config.HeaderName); token != "" {
		return token, nil
	}

	// X-XSRF-TOKEN: clients like axios echo the XSRF-TOKEN cookie value
	// verbatim. The cookie is written URL-encoded, so unescape before
	// validation; an unescape failure means the value cannot be a token
	// the framework issued, so treat it as absent rather than erroring.
	if raw := r.Header.Get(xsrfHeaderName); raw != "" {
		if token, err := url.QueryUnescape(raw); err == nil && token != "" {
			return token, nil
		}
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

	cap := c.config.MaxFormBodyBytes
	if cap <= 0 {
		cap = DefaultMaxFormBodyBytes
	}

	// Wrap with http.MaxBytesReader so reads beyond the cap return a
	// *http.MaxBytesError rather than silently truncating. We do NOT
	// install the wrapped body on r yet; that happens only on a clean
	// read so a downstream handler never observes a partial prefix as a
	// "complete" body.
	origBody := r.Body
	limited := http.MaxBytesReader(w, origBody, cap)
	buf, readErr := io.ReadAll(limited) //nolint:forbidigo // bounded by http.MaxBytesReader on `limited` above
	if readErr != nil {
		// MaxBytesReader returns *http.MaxBytesError on overflow.
		var maxErr *http.MaxBytesError
		if errors.As(readErr, &maxErr) {
			// Do NOT install the truncated buffer on r. Leave the
			// original body in place; the middleware will write 419
			// and not call the handler, so the body is never read
			// past this point.
			r.Body = origBody
			return "", ErrFormBodyTooLarge
		}
		// Other read errors (client disconnect, mid-stream EOF, etc.):
		// restore the original body and treat as "no token"; the
		// downstream handler, if reached, will see the same error.
		r.Body = origBody
		return "", nil
	}

	// Read succeeded and stayed within the cap. Restore r.Body as a
	// re-readable wrapper around the buffered bytes so handlers can
	// re-parse the form.
	r.Body = struct {
		io.Reader
		io.Closer
	}{
		Reader: bytes.NewReader(buf),
		Closer: origBody,
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

		// Emit the per-response masked form, never the raw stored
		// token (see MaskToken). The client echoes it verbatim and
		// validation unmasks before comparing.
		masked, err := MaskToken(token)
		if err != nil {
			http.Error(w, "Failed to generate token", http.StatusInternalServerError)
			return
		}

		// Return token as JSON
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"token": masked,
		})
	}
}

// RotateToken implements contract.CSRFTokenRotator. It deletes any token
// bound to oldID and mints a fresh token bound to newID. Session schemes
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

// ClearXSRFCookie implements contract.CSRFTokenRotator. Writes a
// delete-Set-Cookie (Max-Age=-1) for XSRF-TOKEN so the browser drops
// the value tied to a just-revoked session.
//
// The cookie attributes (Name, Path, SameSite) MUST match those written
// by writeXSRFCookieForSession so the user agent treats this as the
// same cookie and actually removes it. Domain is intentionally left at
// the default (host-only) to match the write path, which also does not
// set Domain.
//
// Secure mirrors the safe-method bootstrap path: Secure=true when the
// request is HTTPS or Config.Secure is true. This keeps production
// proxy-terminated TLS deployments secure even though r.TLS is nil in
// the Go process, while still allowing explicit Secure=false dev/test
// configs to delete a plain-HTTP cookie.
//
// Called from SessionScheme.Logout right after RevokeToken.
func (c *CSRF) ClearXSRFCookie(w http.ResponseWriter, r *http.Request) {
	if c == nil || c.config == nil || w == nil || r == nil {
		return
	}
	if !c.config.WriteXSRFCookie {
		return
	}
	cookieName := c.config.XSRFCookieName
	if cookieName == "" {
		cookieName = "XSRF-TOKEN"
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: false,
		Secure:   r.TLS != nil || c.config.Secure,
		SameSite: http.SameSiteLaxMode,
	})
}

// RevokeToken implements contract.CSRFTokenRotator. It deletes the token
// bound to id. Session schemes call this from Logout (before
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

// GetToken retrieves or generates a token for the given session ID.
//
// The return value is the RAW stored token. Do not write it into a
// response verbatim: every emission sink must wrap it with MaskToken so
// the same bytes never repeat across responses (the built-in sinks -
// XSRF-TOKEN cookie writes, TokenForRequest, RefreshHandler - already
// do). Raw values remain valid on submission, so existing callers that
// compare or replay the raw token keep working.
func (c *CSRF) GetToken(sessionID string) (string, error) {
	if c.config.Store == nil {
		return "", ErrNoStore
	}

	// Try to get existing token
	token, err := c.config.Store.Get(sessionID)
	if err == nil {
		return token, nil
	}
	if !errors.Is(err, stores.ErrTokenNotFound) {
		// Transient store failure (network, timeout). Minting a fresh
		// token here would overwrite the stored one and silently
		// invalidate the token every other tab/client already holds
		// (their next POST 419s). Surface the error instead; only a
		// genuine miss mints.
		return "", err
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
// Returns nil if CSRF is not configured, including when the context has
// no service container at all (e.g. a bare test context), so callers
// can rely on the documented nil contract instead of a panic.
func FromContext(ctx *router.Context) *CSRF {
	s := ctx.ServicesIfSet()
	if s == nil || s.CSRF == nil {
		return nil
	}
	c, _ := s.CSRF.(*CSRF)
	return c
}

func isJSONRequest(r *http.Request) bool {
	contentType := r.Header.Get("Content-Type")
	accept := r.Header.Get("Accept")
	return strings.Contains(contentType, "application/json") ||
		strings.Contains(accept, "application/json")
}
