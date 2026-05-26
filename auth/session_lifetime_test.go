package auth

import (
	"errors"
	"net/http"
	"testing"
)

// TestSessionConfig_Validate_RejectsNegativeLifetime covers audit M-07:
// a negative Lifetime would produce a cookie with Expires already in the
// past, which browsers may interpret as a deletion. Refuse to boot.
func TestSessionConfig_Validate_RejectsNegativeLifetime(t *testing.T) {
	cfg := SessionConfig{
		Name:     "velocity_session",
		Lifetime: -1,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	err := cfg.Validate("production")
	if err == nil {
		t.Fatal("expected error for negative Lifetime")
	}
	if !errors.Is(err, ErrInvalidLifetime) {
		t.Errorf("expected ErrInvalidLifetime, got %v", err)
	}
}

// TestSessionConfig_Validate_AllowsZeroLifetime covers the legitimate
// "session cookie" use case: Lifetime == 0 must not error. The cookie
// driver writes a MaxAge=0 / no-Expires cookie which RFC 6265 specifies
// as a session-lifetime cookie.
func TestSessionConfig_Validate_AllowsZeroLifetime(t *testing.T) {
	cfg := SessionConfig{
		Name:     "velocity_session",
		Lifetime: 0,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}
	if err := cfg.Validate("production"); err != nil {
		t.Fatalf("Lifetime=0 must be valid, got %v", err)
	}
}
