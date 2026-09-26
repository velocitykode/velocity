package csrf

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/velocitykode/velocity/contract"
)

// Mode selects how CSRF tokens are bound to the requesting client.
// Binding matters because an attacker-controlled binding key is equivalent
// to no CSRF protection at all: the attacker simply issues their own token
// against their own key and replays it.
type Mode int

const (
	// ModeSession binds CSRF tokens to the session cookie. If the request
	// has no session cookie, validation fails — the middleware will NOT
	// generate an ephemeral session ID. This is the secure default.
	ModeSession Mode = iota

	// ModeDoubleSubmit binds CSRF tokens to a server-issued signed cookie
	// value, independent of the session. Use this when the app does not
	// run a session middleware (e.g., pure API with JWT). Reserved — the
	// current implementation only supports ModeSession; setting this mode
	// is rejected at New() time until the double-submit path is wired.
	ModeDoubleSubmit
)

// String returns the human-readable mode name for diagnostics.
func (m Mode) String() string {
	switch m {
	case ModeSession:
		return "session"
	case ModeDoubleSubmit:
		return "double-submit"
	default:
		return fmt.Sprintf("unknown(%d)", int(m))
	}
}

// ErrInsecureCSRFConfig is returned from Config.Validate and New when
// Mode requests an unsupported binding strategy, and from New when no
// SessionIDResolver is configured.
var ErrInsecureCSRFConfig = errors.New("velocity/csrf: insecure config")

// Config holds CSRF protection configuration
type Config struct {
	HeaderName        string
	FormField         string
	SessionCookieName string // Name of the session cookie to read session ID from

	// Env is the application environment (APP_ENV), set by the framework from
	// the app Config at construction (App.bootstrap copies a.config.Env here
	// before csrf.NewE). It drives env-aware behaviour WITHOUT a per-request
	// os.Getenv: when Env names a test profile (contract.IsTestingEnv:
	// "test"/"testing") the middleware bypasses token validation on unsafe
	// requests, so HTTP feature tests need no token round-trip. The zero
	// value "" enforces; because a Config built directly (as csrf's own
	// unit tests do) leaves Env
	// empty, the bypass is strictly opt-in per instance and never leaks into the
	// package's own test profile, even under `APP_ENV=testing go test ./csrf`.
	Env string

	// MaxFormBodyBytes bounds how many bytes of an
	// application/x-www-form-urlencoded request body the CSRF middleware
	// is allowed to buffer while looking for a token. Bodies larger than
	// this are rejected with 419 before any prefix is buffered; the
	// downstream handler is NOT called. The default (1 MiB) is generous
	// for a hidden _token field. Operators with legitimate >1 MiB urlencoded
	// payloads should send the token in the configured header instead.
	// A zero value falls back to the default.
	MaxFormBodyBytes int64

	// Mode selects how tokens are bound to the client. Default ModeSession.
	Mode Mode

	// SingleUse consumes a token on its first successful validation.
	SingleUse bool

	// CookiePolicy is the Path, Domain, Secure and SameSite of the XSRF
	// token cookie, its post-login rewrite and its logout deletion.
	// velocity.New sets it to the app's policy (derived from the session
	// config), so the token cookie follows SESSION_SAME_SITE,
	// SESSION_DOMAIN, SESSION_PATH and SESSION_SECURE. The zero value is
	// the secure default (Path "/", Secure, SameSite=Lax).
	CookiePolicy contract.CookiePolicy

	// WriteXSRFCookie controls whether the middleware writes a non-
	// HttpOnly cookie carrying the per-session CSRF token on safe
	// (idempotent) requests. The cookie name is "XSRF-TOKEN" by
	// convention (axios, angular, etc.) and its value is the URL-encoded
	// token so axios-style clients can echo it back as X-XSRF-TOKEN.
	// Default true. Set to false to opt out (the operator must then
	// hand-roll cookie wiring or rely solely on RefreshHandler).
	//
	// Security notes:
	//   - The cookie is intentionally NOT HttpOnly: SPA JS must read it
	//     to echo into the header on unsafe requests.
	//   - Secure, SameSite, Path and Domain come from CookiePolicy, the
	//     same attributes as the session cookie.
	//   - The cookie carries the SAME per-session token reachable via
	//     GetToken(sessionID). Single-use tokens MUST NOT be exposed via
	//     this cookie - they are consumed on validation and the client
	//     would echo a stale value. When SingleUse is true the cookie
	//     write is skipped automatically.
	WriteXSRFCookie bool

	// XSRFCookieName is the name of the non-HttpOnly cookie written
	// when WriteXSRFCookie is true. Default "XSRF-TOKEN" (axios
	// convention). Header echoed by the client is X-XSRF-TOKEN by
	// convention; the framework also accepts HeaderName.
	XSRFCookieName string

	// Store keeps the token of each session. velocity.New sets a
	// stores.SessionBagStore when CSRF binds to the session, so the token
	// lives in the session (saved with it, valid on every instance that
	// can read the session, ended with it). Left nil, NewE installs a
	// stores.MemoryStore: a map in this process, for session-less use.
	Store Store

	// SessionIDResolver returns the plaintext session ID that CSRF tokens
	// are keyed by. It is REQUIRED: csrf.NewE returns
	// ErrInsecureCSRFConfig when this field is nil. There is no fallback
	// to reading SessionCookieName as a raw value from the request - that
	// legacy path let an unauthenticated attacker mint a CSRF token
	// against any self-chosen string by sending the cookie under the
	// configured name (the cookie value never went through session
	// middleware), and has been removed.
	//
	// Frameworks that encrypt the session cookie (e.g. velocity/auth
	// session manager) MUST inject a resolver that decrypts the cookie
	// and returns the underlying plaintext session ID. Keying CSRF tokens
	// by the raw ciphertext cookie value is also incorrect: the IV
	// changes on every Save() and the stored token becomes unreachable.
	//
	// velocity.New auto-installs a resolver that answers with the session
	// the session store accepts (and, with it, a stores.SessionBagStore)
	// when the session cookie name and CSRFConfig.SessionCookieName
	// align. When they do not, velocity.New installs
	// a strict-reject resolver (returns ErrNoSession on every request) so
	// the deployment fails closed (419 on every unsafe request) instead
	// of silently bypassing CSRF; operators wire a real resolver here to
	// override.
	//
	// The resolver returns ErrNoSession when no session is present.
	SessionIDResolver func(*http.Request) (string, error)

	// Exception handling
	ExcludePaths []string
	ExcludeFunc  func(*http.Request) bool

	// Error handling

	// ErrorMessage is the client-facing text of a rejection: the 419
	// detail on the router path (carried by TokenMismatchError) and the
	// body the bare Middleware writes.
	ErrorMessage string
	ErrorHandler func(http.ResponseWriter, *http.Request, error)
}

// DefaultConfig returns the default CSRF configuration
func DefaultConfig() *Config {
	return &Config{
		HeaderName:        "X-CSRF-Token",
		FormField:         "_token",
		SessionCookieName: "session_id", // Default session cookie name
		MaxFormBodyBytes:  DefaultMaxFormBodyBytes,
		Mode:              ModeSession,
		SingleUse:         false,
		WriteXSRFCookie:   true,
		XSRFCookieName:    "XSRF-TOKEN",
		ErrorMessage:      "CSRF token validation failed. Please refresh and try again.",
	}
}

// DefaultMaxFormBodyBytes caps the amount of an x-www-form-urlencoded
// request body the CSRF middleware will buffer while looking for a token.
// 1 MiB is generous for hidden _token fields and still bounds resource
// use. Operators needing larger payloads must send the token in the
// configured header.
const DefaultMaxFormBodyBytes int64 = 1 << 20

// Validate checks the Config. The XSRF cookie's attributes come from
// CookiePolicy, which velocity.New derives from the validated session
// config, so no cookie rule lives here.
//
// Rules:
//   - Mode must be ModeSession (ModeDoubleSubmit is reserved)
func (c *Config) Validate() error {
	if c == nil {
		return fmt.Errorf("%w: nil config", ErrInsecureCSRFConfig)
	}
	if c.Mode != ModeSession {
		return fmt.Errorf("%w: Mode=%s is not yet implemented; use ModeSession", ErrInsecureCSRFConfig, c.Mode)
	}
	return nil
}
