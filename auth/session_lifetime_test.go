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

// TestSessionConfig_ValidateLifetimes_ErrorText pins the wording of each
// refusal: the sentinel names the concept (an invalid lifetime) and the
// detail names the field and the rule it broke, so an absolute cap shorter
// than the idle window is not reported as a negative value.
func TestSessionConfig_ValidateLifetimes_ErrorText(t *testing.T) {
	base := SessionConfig{
		Name:         "velocity_session",
		IdleLifetime: 120,
		Path:         "/",
		Secure:       true,
		HttpOnly:     true,
		SameSite:     http.SameSiteLaxMode,
	}
	tests := []struct {
		name string
		mut  func(*SessionConfig)
		want string
	}{
		{
			name: "negative idle",
			mut:  func(c *SessionConfig) { c.IdleLifetime = -1 },
			want: "velocity/auth: invalid session lifetime: IdleLifetime -1 minutes is negative",
		},
		{
			name: "absolute shorter than idle",
			mut:  func(c *SessionConfig) { c.AbsoluteLifetime = 60 },
			want: "velocity/auth: invalid session lifetime: AbsoluteLifetime 60 minutes is shorter than IdleLifetime 120 minutes",
		},
		{
			name: "negative remember",
			mut:  func(c *SessionConfig) { c.RememberLifetime = -1 },
			want: "velocity/auth: invalid session lifetime: RememberLifetime -1 minutes is negative",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			tt.mut(&cfg)
			err := cfg.ValidateLifetimes()
			if err == nil {
				t.Fatal("expected an error")
			}
			if !errors.Is(err, ErrInvalidLifetime) {
				t.Fatalf("expected ErrInvalidLifetime, got %v", err)
			}
			if got := err.Error(); got != tt.want {
				t.Fatalf("error text\n got: %s\nwant: %s", got, tt.want)
			}
		})
	}
}
