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
// The func(any) error signature is the shared lowest common denominator: each
// subsystem emits its own concrete event types, so the dispatcher parameter
// is type-erased. Receivers type-assert back to the concrete event type.
type EventDispatcherAware interface {
	SetEventDispatcher(fn func(event any) error)
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

// ViewEngine defines the contract for the view/rendering layer.
// Implemented by *view.Engine.
type ViewEngine interface {
	Back(w http.ResponseWriter, r *http.Request)
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
