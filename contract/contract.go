package contract

import "net/http"

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
