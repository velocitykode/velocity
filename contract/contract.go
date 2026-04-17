package contract

import (
	"context"
	"net/http"
)

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
