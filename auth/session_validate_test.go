package auth

import (
	"errors"
	"net/http"
	"testing"
)

// TestSessionConfig_Validate_RejectsInsecureDefaults pins the matrix of
// SessionConfig.Validate rules. Every production-env misconfiguration must
// surface as an ErrInsecureSessionConfig before boot completes — the
// previous config shipped without a Validate method, so apps could ship
// with Secure=false / HttpOnly=false / zero SameSite and boot silently.
func TestSessionConfig_Validate_RejectsInsecureDefaults(t *testing.T) {
	// A config that is valid under production rules. Each test row mutates
	// one field at a time so failures point at the single broken rule.
	ok := SessionConfig{
		Name:     "velocity_session",
		Lifetime: 120,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}

	tests := []struct {
		name    string
		mutate  func(c *SessionConfig)
		env     string
		wantErr bool
	}{
		{
			name:    "baseline ok in production",
			mutate:  func(c *SessionConfig) {},
			env:     "production",
			wantErr: false,
		},
		{
			name:    "Secure=false rejected in production",
			mutate:  func(c *SessionConfig) { c.Secure = false },
			env:     "production",
			wantErr: true,
		},
		{
			name:    "Secure=false rejected with empty env (treat as prod)",
			mutate:  func(c *SessionConfig) { c.Secure = false },
			env:     "",
			wantErr: true,
		},
		{
			name:    "Secure=false allowed in development",
			mutate:  func(c *SessionConfig) { c.Secure = false },
			env:     "development",
			wantErr: false,
		},
		{
			name:    "Secure=false allowed in testing",
			mutate:  func(c *SessionConfig) { c.Secure = false },
			env:     "testing",
			wantErr: false,
		},
		{
			name:    "HttpOnly=false without opt-in rejected",
			mutate:  func(c *SessionConfig) { c.HttpOnly = false },
			env:     "production",
			wantErr: true,
		},
		{
			name: "HttpOnly=false allowed with AllowJSAccess=true opt-in",
			mutate: func(c *SessionConfig) {
				c.HttpOnly = false
				c.AllowJSAccess = true
			},
			env:     "production",
			wantErr: false,
		},
		{
			name:    "SameSite zero value rejected",
			mutate:  func(c *SessionConfig) { c.SameSite = http.SameSiteDefaultMode },
			env:     "production",
			wantErr: true,
		},
		{
			name:    "SameSite=None without Secure rejected",
			mutate:  func(c *SessionConfig) { c.SameSite = http.SameSiteNoneMode; c.Secure = false },
			env:     "production",
			wantErr: true,
		},
		{
			name:    "SameSite=None with Secure accepted",
			mutate:  func(c *SessionConfig) { c.SameSite = http.SameSiteNoneMode },
			env:     "production",
			wantErr: false,
		},
		{
			name:    "SameSite=Strict accepted",
			mutate:  func(c *SessionConfig) { c.SameSite = http.SameSiteStrictMode },
			env:     "production",
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ok
			tt.mutate(&cfg)
			err := cfg.Validate(tt.env)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if !errors.Is(err, ErrInsecureSessionConfig) {
					t.Errorf("expected ErrInsecureSessionConfig, got %v", err)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestSessionConfig_Validate_AbsoluteLifetime pins the V2-09 config rule: a
// positive absolute cap shorter than the rolling Lifetime window is a
// misconfiguration; zero (default) and negative (explicit opt-out) pass.
func TestSessionConfig_Validate_AbsoluteLifetime(t *testing.T) {
	base := SessionConfig{
		Name:     "velocity_session",
		Lifetime: 120,
		Path:     "/",
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	}

	tests := []struct {
		name     string
		absolute int
		wantErr  bool
	}{
		{name: "zero (framework default) accepted", absolute: 0, wantErr: false},
		{name: "negative (explicit opt-out) accepted", absolute: -1, wantErr: false},
		{name: "equal to Lifetime accepted", absolute: 120, wantErr: false},
		{name: "greater than Lifetime accepted", absolute: 43200, wantErr: false},
		{name: "shorter than Lifetime rejected", absolute: 60, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			cfg.AbsoluteLifetime = tt.absolute
			err := cfg.Validate("production")
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidLifetime) {
					t.Fatalf("expected ErrInvalidLifetime, got %v", err)
				}
			} else if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}
