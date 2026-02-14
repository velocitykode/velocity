package csrf

import (
	"net/http"
	"time"
)

// Config holds CSRF protection configuration
type Config struct {
	// Token settings
	TokenLifetime     time.Duration
	HeaderName        string
	FormField         string
	CookieName        string
	SessionCookieName string // Name of the session cookie to read session ID from

	// Security settings
	SameSite  http.SameSite
	Secure    bool
	HTTPOnly  bool
	SingleUse bool

	// Storage strategy
	Store Store

	// Exception handling
	ExcludePaths []string
	ExcludeFunc  func(*http.Request) bool

	// Error handling
	ErrorTemplate string
	ErrorMessage  string
	ErrorHandler  func(http.ResponseWriter, *http.Request, error)
}

// DefaultConfig returns the default CSRF configuration
func DefaultConfig() *Config {
	return &Config{
		TokenLifetime:     24 * time.Hour,
		HeaderName:        "X-CSRF-Token",
		FormField:         "_token",
		CookieName:        "csrf_token",
		SessionCookieName: "session_id", // Default session cookie name
		SameSite:          http.SameSiteLaxMode,
		Secure:            true,
		HTTPOnly:          true,
		SingleUse:         false,
		ErrorMessage:      "CSRF token validation failed. Please refresh and try again.",
	}
}
