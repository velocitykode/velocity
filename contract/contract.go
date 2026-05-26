package contract

import (
	"context"
	"net/http"
)

// EventDispatcherAware is the uniform interface for types that surface events
// to the framework-level dispatcher. Every subsystem (cache, queue, scheduler,
// router, ORM, auth, view, mail, crypto) that emits events implements this so
// bootstrap wiring can thread a single dispatcher through every subsystem.
//
// The fn parameter receives a context.Context so listeners can observe
// request-scoped values (transactions, trace IDs, deadlines). Subsystems that
// emit events MUST pass the most-relevant ctx in scope (request ctx, tx ctx,
// scheduler-job ctx) instead of dropping to context.Background(). The event
// parameter is type-erased so each subsystem can emit its own concrete event
// types; receivers type-assert back to the concrete type.
type EventDispatcherAware interface {
	SetEventDispatcher(fn func(ctx context.Context, event any) error)
}

// ShutdownAware is the uniform interface for types that hold background
// resources (goroutines, connections, file handles) and need to release
// them during application shutdown.
//
// Implementations MUST:
//   - honour the context deadline and return promptly when ctx is cancelled;
//   - be safe to call more than once (idempotent);
//   - return a non-nil error only when cleanup actually failed — a
//     no-op Shutdown returns nil.
//
// The provider registry and App.Shutdown call Shutdown in reverse
// registration order; see serve.go and app/provider.go.
type ShutdownAware interface {
	Shutdown(ctx context.Context) error
}

// AuthManager defines the contract for authorization checks.
// Implemented by *auth.Manager.
type AuthManager interface {
	GateAllows(r *http.Request, ability string, args ...interface{}) bool
	GateAuthorize(r *http.Request, ability string, args ...interface{}) error
}

// CSRFProtector defines the contract for CSRF protection middleware.
// Implemented by *csrf.CSRF.
type CSRFProtector interface {
	Middleware(next http.Handler) http.Handler
}

// CSRFTokenRotator is the contract the auth subsystem uses to keep CSRF
// tokens aligned with the session lifecycle. Implemented by *csrf.CSRF.
//
// Session guards MUST call RotateToken after Session.Regenerate (Login,
// privilege change, remember-cookie revival) so the token bound to the
// old session id is gone and the new id has a fresh token. They MUST
// call RevokeToken before Session.Invalidate (Logout) so the token does
// not survive the session. Without this, a captured cookie+token pair
// would remain valid for the store TTL (default 24h) even after the
// user logs out, and a token minted under a pre-login session id would
// persist as an orphan after regenerate.
//
// Implementations MUST be safe for concurrent use. A best-effort
// implementation is acceptable: a transient store failure should not
// abort Login/Logout, but it should be observable (logged or eventful)
// because each surviving token weakens the invariant.
type CSRFTokenRotator interface {
	// RotateToken deletes any token bound to oldID and mints a fresh one
	// bound to newID. oldID may be empty (first login after a fresh
	// session). newID MUST be non-empty.
	RotateToken(oldID, newID string) error
	// RevokeToken deletes the token bound to id. A missing entry is not
	// an error.
	RevokeToken(id string) error
	// WriteXSRFCookie writes the non-HttpOnly XSRF-TOKEN cookie carrying
	// the token currently bound to sessionID in the CSRF store, so SPA
	// clients can read it via document.cookie and echo it back as
	// X-XSRF-TOKEN on subsequent unsafe requests.
	//
	// Session guards MUST call this after RotateToken on Login and on the
	// remember-cookie revival path: without it the response carries the
	// new session cookie but no fresh XSRF-TOKEN, and the very next POST
	// from the SPA returns 419 (the new per-session token is in the store
	// but the client has no way to retrieve it). Safe to call any number
	// of times.
	//
	// No-op when:
	//   - w is nil (e.g. tests),
	//   - sessionID is empty,
	//   - the rotator's configuration disables cookie writes
	//     (WriteXSRFCookie=false), or
	//   - SingleUse is enabled (the cookie would carry a value about to
	//     be consumed on the next unsafe request).
	WriteXSRFCookie(w http.ResponseWriter, sessionID string)
}

// ViewEngine defines the contract for the view/rendering layer.
// Implemented by *view.Engine.
type ViewEngine interface {
	Back(w http.ResponseWriter, r *http.Request)
}

// RedirectAllowlist is the operator-configured allowlist of cross-origin
// hosts that redirect helpers may treat as "same-origin equivalent".
// Implemented by *router.VelocityRouterV2 (the Router.RedirectAllowedHosts
// field).
//
// Redirect helpers (router.Context.Redirect, bond.RedirectWithStatus,
// bond.Back) MUST consult this contract instead of trusting the incoming
// Host header. A misconfigured fronting proxy that copies an attacker-
// supplied X-Forwarded-Host into r.Host would otherwise convert
// "same-origin" into an attacker-controlled origin and let cross-host
// targets pass the sanitizer.
//
// Implementations MUST be safe for concurrent use and SHOULD return a
// stable snapshot (callers MUST NOT mutate the returned slice).
//
// An implementation MAY return an empty slice. Callers that receive an
// empty slice should fail closed (reject every cross-host target) or,
// where backwards compatibility forbids that, fall back to r.Host with
// a one-time logged warning so operators see the gap.
type RedirectAllowlist interface {
	AllowedRedirectHosts() []string
}

// LoginThrottler rate-limits login attempts. Implementations should be safe
// for concurrent use. The default no-op implementation lives in the auth
// package as auth.NoopLoginThrottler.
//
// Contract:
//   - Allow(r, key) is called before the credential check. Returning false
//     short-circuits the attempt with an ErrLoginThrottled.
//   - RecordFailure(r, key) is called when credential validation fails.
//   - RecordSuccess(r, key) is called after a successful login; a good
//     implementation clears any failure counters for the key.
//
// The key is typically a composite such as "<username>|<remote-ip>".
type LoginThrottler interface {
	Allow(r *http.Request, key string) bool
	RecordFailure(r *http.Request, key string)
	RecordSuccess(r *http.Request, key string)
}
