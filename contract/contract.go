package contract

import (
	"context"
	"database/sql"
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

	// ClearXSRFCookie writes a delete-Set-Cookie (Max-Age=-1) for the
	// XSRF-TOKEN cookie so the browser drops any value bound to a
	// just-revoked session. The cookie attributes (Name, Path,
	// SameSite, Secure) MUST match those written by the safe-method
	// bootstrap so the user agent considers it the same cookie and
	// removes it. Implementations derive Secure from the incoming
	// request (r.TLS != nil) so a plain-HTTP dev cookie is matched by
	// a plain-HTTP delete and the browser actually honours it (browsers
	// ignore Secure Set-Cookie received over HTTP, leaving the stale
	// value in place).
	//
	// Session guards MUST call this from Logout right after RevokeToken
	// and before session teardown. Without it the browser keeps the
	// stale XSRF-TOKEN value, and the next POST after logout (e.g. the
	// follow-up login) echoes it as X-XSRF-TOKEN; the server has no
	// matching token in the store for the post-logout session id, and
	// the request 419s with no useful signal to the SPA.
	//
	// No-op when w or r is nil, or when the rotator's configuration
	// disables cookie writes (WriteXSRFCookie=false).
	ClearXSRFCookie(w http.ResponseWriter, r *http.Request)
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

// Logger is the logging contract shared across the framework. It mirrors
// the log package's Logger interface exactly (Debug/Info/Warn/Error/Fatal),
// so *log.StackLogger, log.NullLogger, and the redacting logger all satisfy
// it with no adapter, and a Logger value is interchangeable with a
// log.Logger anywhere a concrete logger is expected.
//
// app.Services types its Log field as this contract so the app leaf need
// not import log; the router only emits warnings (dropped-event and
// async-listener diagnostics in event_dispatcher.go / velocity_router.go)
// but the full set keeps the field usable by callers that log at every
// level (e.g. serve.go startup/shutdown Info/Error). Every signature uses
// only stdlib types, so the contract leaf stays stdlib-only.
//
// kvs carries alternating key/value pairs (structured fields):
// Warn("msg", "error", err.Error()).
type Logger interface {
	Debug(msg string, kvs ...any)
	Info(msg string, kvs ...any)
	Warn(msg string, kvs ...any)
	Error(msg string, kvs ...any)
	Fatal(msg string, kvs ...any)
}

// Encryptor is the contract for symmetric encryption. Implemented by the
// concrete crypto driver (*crypto/drivers.AESDriver) and the crypto.Encryptor
// facade.
//
// Signatures mirror the concrete crypto.Encryptor interface exactly so it
// satisfies this contract with no adapter, and an Encryptor value is
// interchangeable with a crypto.Encryptor anywhere the concrete interface
// is expected (router.Context.Crypto, the bond flash encryptor, the CSRF
// session-id resolver). The Encrypt* methods return the base64 envelope
// string and the Decrypt* methods take that envelope string back; the
// *WithAAD variants bind additional authenticated data to the ciphertext
// (GCM via the AEAD tag, CBC via the HMAC framing). Every signature
// uses only stdlib types, so the contract leaf stays stdlib-only.
type Encryptor interface {
	Encrypt(plaintext string) (string, error)
	EncryptBytes(plaintext []byte) (string, error)
	Decrypt(payload string) (string, error)
	DecryptBytes(payload string) ([]byte, error)
	EncryptBytesWithAAD(plaintext, aad []byte) (string, error)
	DecryptBytesWithAAD(payload string, aad []byte) ([]byte, error)
	GenerateKey() (string, error)
}

// Database is the contract for the ORM database value held by app.Services
// and surfaced through router.Context.DB(). It is the stdlib-only subset of
// the *orm.Manager method set (orm.Database): every method the app leaf and
// other contract consumers need, with no driver-typed methods. The concrete
// manager satisfies it with no adapter.
//
// app.Services types its DB field as this contract so the app leaf need not
// import orm. The driver-facing methods on orm.Database (DefaultDriver,
// Connection, AddConnection) expose orm/drivers.Driver and are deliberately
// omitted here so the contract leaf stays stdlib-only. Code that genuinely
// needs them (the router's DB() accessor) type-asserts the stored value back
// to orm.Database, which is always the concrete *orm.Manager.
//
// Transaction takes a closure that receives the per-tx context. The
// returned ctx carries a *sql.Tx that any ORM terminal observing it
// automatically participates in; callers needing the raw *sql.Tx extract
// it via the orm package's TxFromContext.
type Database interface {
	DB() *sql.DB
	Raw(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
	Transaction(ctx context.Context, fn func(ctx context.Context) error) error
	Begin(ctx context.Context) (*sql.Tx, error)
	Shutdown(ctx context.Context) error
	Ping() error
	DriverName() string
	DatabaseName() string
	Stats() sql.DBStats
	// SetEventDispatcher wires the event dispatcher used by ORM internals
	// to surface query and transaction lifecycle events. The fn receives
	// ctx so listeners observe request- / tx-scoped values.
	SetEventDispatcher(fn func(ctx context.Context, event any) error)
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
//
// Implementations must pass authtest.RunLoginThrottlerContractTests. See
// authtest for the executable specification.
type LoginThrottler interface {
	Allow(r *http.Request, key string) bool
	RecordFailure(r *http.Request, key string)
	RecordSuccess(r *http.Request, key string)
}
