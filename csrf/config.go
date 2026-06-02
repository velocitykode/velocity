package csrf

import (
	"errors"
	"fmt"
	"net/http"
	"time"

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

// ErrInsecureCSRFConfig is returned from Config.Validate when the
// configuration would produce cookies that are exploitable in production
// (Secure=false outside testing/dev, HttpOnly=false without opt-in, zero
// SameSite, or SameSite=None without Secure). New also returns this error
// when Mode requests an unsupported binding strategy.
var ErrInsecureCSRFConfig = errors.New("velocity/csrf: insecure config")

// Config holds CSRF protection configuration
type Config struct {
	// Token settings
	TokenLifetime     time.Duration
	HeaderName        string
	FormField         string
	CookieName        string
	SessionCookieName string // Name of the session cookie to read session ID from

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

	// Security settings
	SameSite http.SameSite
	Secure   bool
	// HttpOnly matches the casing of net/http.Cookie.HttpOnly.
	HttpOnly  bool
	SingleUse bool

	// AllowJSAccess opts in to HttpOnly=false. Without this flag the
	// CSRF cookie MUST be HttpOnly - JavaScript has no legitimate need
	// to read it in the default flow (forms and XHR echo the value via
	// hidden input / custom header). Name intentionally loud.
	AllowJSAccess bool

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
	//   - Secure is true when the request scheme is https OR when
	//     Config.Secure is set (the default), so the token cookie is
	//     Secure behind a TLS-terminating proxy. Only an explicit
	//     Secure=false dev/test config emits a non-Secure cookie over
	//     plain HTTP.
	//   - SameSite=Lax matches the Set-Cookie semantics most SPAs need
	//     (the cookie travels on top-level POST navigations from the
	//     same site but not cross-site).
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

	// Storage strategy
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
	// velocity.New auto-installs an encrypted-session resolver when the
	// app encryptor, session cookie name, and CSRFConfig.SessionCookieName
	// all align. When any of those conditions miss, velocity.New installs
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
	ErrorMessage string
	ErrorHandler func(http.ResponseWriter, *http.Request, error)
}

// DefaultConfig returns the default CSRF configuration
func DefaultConfig() *Config {
	return &Config{
		TokenLifetime:     24 * time.Hour,
		HeaderName:        "X-CSRF-Token",
		FormField:         "_token",
		CookieName:        "csrf_token",
		SessionCookieName: "session_id", // Default session cookie name
		MaxFormBodyBytes:  DefaultMaxFormBodyBytes,
		Mode:              ModeSession,
		SameSite:          http.SameSiteLaxMode,
		Secure:            true,
		HttpOnly:          true,
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

// Validate checks the Config for insecure defaults. Pass env to enable
// environment-aware rules: Secure=false is allowed when env is a dev or
// test profile (per contract.IsDevOrTestEnv: "development", "dev", "test",
// "testing", "local"), rejected otherwise. An empty env is treated as
// production for strict validation.
//
// Rules:
//   - Mode must be ModeSession (ModeDoubleSubmit is reserved)
//   - HttpOnly must be true unless AllowJSAccess is set
//   - Secure must be true outside the canonical dev/test profiles
//   - SameSite must be set (non-zero value)
//   - SameSite=None requires Secure=true
func (c *Config) Validate(env string) error {
	if c == nil {
		return fmt.Errorf("%w: nil config", ErrInsecureCSRFConfig)
	}
	if c.Mode != ModeSession {
		return fmt.Errorf("%w: Mode=%s is not yet implemented; use ModeSession", ErrInsecureCSRFConfig, c.Mode)
	}
	if !c.HttpOnly && !c.AllowJSAccess {
		return fmt.Errorf("%w: HttpOnly=false requires AllowJSAccess=true opt-in", ErrInsecureCSRFConfig)
	}
	if !c.Secure && !contract.IsDevOrTestEnv(env) {
		return fmt.Errorf("%w: Secure=false is not permitted in %q env (set APP_ENV to a dev or test profile to allow)", ErrInsecureCSRFConfig, env)
	}
	// SameSite is an int enum. SameSiteDefaultMode (0) is ambiguous (browsers
	// differ). Require an explicit value.
	if c.SameSite == http.SameSiteDefaultMode {
		return fmt.Errorf("%w: SameSite must be set to Lax, Strict, or None (got default/zero)", ErrInsecureCSRFConfig)
	}
	if c.SameSite == http.SameSiteNoneMode && !c.Secure {
		return fmt.Errorf("%w: SameSite=None requires Secure=true", ErrInsecureCSRFConfig)
	}
	return nil
}
