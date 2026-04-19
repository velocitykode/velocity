package csrf

import (
	"errors"
	"fmt"
	"net/http"
	"time"
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

	// Mode selects how tokens are bound to the client. Default ModeSession.
	Mode Mode

	// Security settings
	SameSite http.SameSite
	Secure   bool
	// HttpOnly matches the casing of net/http.Cookie.HttpOnly.
	HttpOnly  bool
	SingleUse bool

	// AllowJSAccess opts in to HttpOnly=false. Without this flag the
	// CSRF cookie MUST be HttpOnly — JavaScript has no legitimate need
	// to read it in the default flow (forms and XHR echo the value via
	// hidden input / custom header). Name intentionally loud.
	AllowJSAccess bool

	// Storage strategy
	Store Store

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
		Mode:              ModeSession,
		SameSite:          http.SameSiteLaxMode,
		Secure:            true,
		HttpOnly:          true,
		SingleUse:         false,
		ErrorMessage:      "CSRF token validation failed. Please refresh and try again.",
	}
}

// Validate checks the Config for insecure defaults. Pass env to enable
// environment-aware rules: Secure=false is allowed when env is "testing"
// or "development", rejected otherwise. An empty env is treated as
// production for strict validation.
//
// Rules:
//   - Mode must be ModeSession (ModeDoubleSubmit is reserved)
//   - HttpOnly must be true unless AllowJSAccess is set
//   - Secure must be true outside testing/development
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
	if !c.Secure && !isNonProdEnv(env) {
		return fmt.Errorf("%w: Secure=false is not permitted in %q env (set APP_ENV=testing or development to allow)", ErrInsecureCSRFConfig, env)
	}
	// SameSite is an int enum. SameSiteDefaultMode (0) is ambiguous —
	// browsers differ. Require an explicit value.
	if c.SameSite == http.SameSiteDefaultMode {
		return fmt.Errorf("%w: SameSite must be set to Lax, Strict, or None (got default/zero)", ErrInsecureCSRFConfig)
	}
	if c.SameSite == http.SameSiteNoneMode && !c.Secure {
		return fmt.Errorf("%w: SameSite=None requires Secure=true", ErrInsecureCSRFConfig)
	}
	return nil
}

// isNonProdEnv reports whether env is a non-production environment that
// may relax the Secure cookie requirement.
func isNonProdEnv(env string) bool {
	switch env {
	case "testing", "development":
		return true
	}
	return false
}
