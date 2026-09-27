package auth

import (
	"errors"
	"net/http"
	"testing"
)

// TestSessionConfig_ValidateLifetimes_RejectsNegativeLifetime covers audit M-07:
// a negative IdleLifetime would produce a cookie with Expires already in the
// past, which browsers may interpret as a deletion. Refuse to boot.
func TestSessionConfig_ValidateLifetimes_RejectsNegativeLifetime(t *testing.T) {
	cfg := SessionConfig{
		Name:         "velocity_session",
		IdleLifetime: -1,
		Path:         "/",
		Secure:       true,
		HttpOnly:     true,
		SameSite:     http.SameSiteLaxMode,
	}
	err := cfg.ValidateLifetimes()
	if err == nil {
		t.Fatal("expected error for negative IdleLifetime")
	}
	if !errors.Is(err, ErrInvalidLifetime) {
		t.Errorf("expected ErrInvalidLifetime, got %v", err)
	}
}

// TestSessionConfig_ValidateLifetimes_AllowsZeroLifetime covers the legitimate
// "session cookie" use case: IdleLifetime == 0 must not error. The cookie
// driver writes a MaxAge=0 / no-Expires cookie which RFC 6265 specifies
// as a session-lifetime cookie.
func TestSessionConfig_ValidateLifetimes_AllowsZeroLifetime(t *testing.T) {
	cfg := SessionConfig{
		Name:         "velocity_session",
		IdleLifetime: 0,
		Path:         "/",
		Secure:       true,
		HttpOnly:     true,
		SameSite:     http.SameSiteLaxMode,
	}
	if err := cfg.ValidateLifetimes(); err != nil {
		t.Fatalf("IdleLifetime=0 must be valid, got %v", err)
	}
}
