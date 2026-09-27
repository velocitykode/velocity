package auth

import (
	"errors"
	"net/http"
	"testing"
)

// TestSessionConfig_ValidateCookieSecurity_RejectsInsecureDefaults pins the matrix of
// SessionConfig.ValidateCookieSecurity rules. Every production-env misconfiguration must
// surface as an ErrInsecureSessionConfig before boot completes — the
// previous config shipped without a Validate method, so apps could ship
// with Secure=false / HttpOnly=false / zero SameSite and boot silently.
func TestSessionConfig_ValidateCookieSecurity_RejectsInsecureDefaults(t *testing.T) {
	// A config that is valid under production rules. Each test row mutates
	// one field at a time so failures point at the single broken rule.
	ok := SessionConfig{
		Name:         "velocity_session",
		IdleLifetime: 120,
		Path:         "/",
		Secure:       true,
		HttpOnly:     true,
		SameSite:     http.SameSiteLaxMode,
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
			mutate:  func(c *SessionConfig) { c.SameSite = 0 },
			env:     "production",
			wantErr: true,
		},
		{
			name:    "SameSite default mode rejected",
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
			err := cfg.ValidateCookieSecurity(tt.env)
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

// TestSessionConfig_ValidateLifetimes_AbsoluteLifetime pins the V2-09 config rule: a
// positive absolute cap shorter than the rolling IdleLifetime window is a
// misconfiguration; zero (default) and negative (explicit opt-out) pass.
func TestSessionConfig_ValidateLifetimes_AbsoluteLifetime(t *testing.T) {
	base := SessionConfig{
		Name:         "velocity_session",
		IdleLifetime: 120,
		Path:         "/",
		Secure:       true,
		HttpOnly:     true,
		SameSite:     http.SameSiteLaxMode,
	}

	tests := []struct {
		name     string
		absolute int
		wantErr  bool
	}{
		{name: "zero (framework default) accepted", absolute: 0, wantErr: false},
		{name: "negative (explicit opt-out) accepted", absolute: -1, wantErr: false},
		{name: "equal to IdleLifetime accepted", absolute: 120, wantErr: false},
		{name: "greater than IdleLifetime accepted", absolute: 43200, wantErr: false},
		{name: "shorter than IdleLifetime rejected", absolute: 60, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			cfg.AbsoluteLifetime = tt.absolute
			err := cfg.ValidateLifetimes()
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

// TestSessionConfig_ValidateLifetimes_RememberLifetime: the remember lifetime is its
// own and may be shorter or longer than the session lifetime; only a
// negative value is rejected.
func TestSessionConfig_ValidateLifetimes_RememberLifetime(t *testing.T) {
	base := SessionConfig{
		Name:         "velocity_session",
		IdleLifetime: 120,
		Path:         "/",
		Secure:       true,
		HttpOnly:     true,
		SameSite:     http.SameSiteLaxMode,
	}
	tests := []struct {
		name     string
		remember int
		wantErr  bool
	}{
		{name: "zero (framework default) accepted", remember: 0},
		{name: "shorter than IdleLifetime accepted", remember: 60},
		{name: "longer than the absolute cap accepted", remember: 90 * 24 * 60},
		{name: "negative rejected", remember: -1, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := base
			cfg.RememberLifetime = tt.remember
			err := cfg.ValidateLifetimes()
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

// TestSessionConfig_ValidationFamiliesAreSeparate pins the split between the
// two rule families: a lifetime error is reported by ValidateLifetimes only
// (whatever the environment), a cookie security error by
// ValidateCookieSecurity only, so the environment gate at boot relaxes
// cookie attributes and never a lifetime value.
func TestSessionConfig_ValidationFamiliesAreSeparate(t *testing.T) {
	ok := SessionConfig{
		Name:         "velocity_session",
		IdleLifetime: 120,
		Path:         "/",
		Secure:       true,
		HttpOnly:     true,
		SameSite:     http.SameSiteLaxMode,
	}
	tests := []struct {
		name         string
		mutate       func(c *SessionConfig)
		wantLifetime bool
		wantCookie   bool
	}{
		{name: "valid", mutate: func(c *SessionConfig) {}},
		{name: "negative idle lifetime", mutate: func(c *SessionConfig) { c.IdleLifetime = -1 }, wantLifetime: true},
		{name: "negative remember lifetime", mutate: func(c *SessionConfig) { c.RememberLifetime = -1 }, wantLifetime: true},
		{name: "absolute cap shorter than idle", mutate: func(c *SessionConfig) { c.AbsoluteLifetime = 60 }, wantLifetime: true},
		{name: "negative absolute lifetime (no cap)", mutate: func(c *SessionConfig) { c.AbsoluteLifetime = -1 }},
		{name: "Secure=false", mutate: func(c *SessionConfig) { c.Secure = false }, wantCookie: true},
		{name: "HttpOnly=false", mutate: func(c *SessionConfig) { c.HttpOnly = false }, wantCookie: true},
		{name: "zero SameSite", mutate: func(c *SessionConfig) { c.SameSite = 0 }, wantCookie: true},
		{
			name: "negative remember lifetime and Secure=false",
			mutate: func(c *SessionConfig) {
				c.RememberLifetime = -1
				c.Secure = false
			},
			wantLifetime: true,
			wantCookie:   true,
		},
	}
	for _, tt := range tests {
		for _, env := range []string{"production", "development", "testing", ""} {
			t.Run(tt.name+"/"+env, func(t *testing.T) {
				cfg := ok
				tt.mutate(&cfg)
				lifetimeErr := cfg.ValidateLifetimes()
				if tt.wantLifetime != (lifetimeErr != nil) {
					t.Fatalf("ValidateLifetimes = %v, want error %v", lifetimeErr, tt.wantLifetime)
				}
				if lifetimeErr != nil && !errors.Is(lifetimeErr, ErrInvalidLifetime) {
					t.Fatalf("ValidateLifetimes = %v, want ErrInvalidLifetime", lifetimeErr)
				}
				if errors.Is(lifetimeErr, ErrInsecureSessionConfig) {
					t.Fatalf("ValidateLifetimes = %v, must not report a cookie security error", lifetimeErr)
				}
				cookieErr := cfg.ValidateCookieSecurity(env)
				// Secure=false alone is permitted in the dev and test profiles.
				wantCookie := tt.wantCookie && !(cfg.HttpOnly && cfg.SameSite == http.SameSiteLaxMode && (env == "development" || env == "testing"))
				if wantCookie != (cookieErr != nil) {
					t.Fatalf("ValidateCookieSecurity(%q) = %v, want error %v", env, cookieErr, wantCookie)
				}
				if cookieErr != nil && !errors.Is(cookieErr, ErrInsecureSessionConfig) {
					t.Fatalf("ValidateCookieSecurity(%q) = %v, want ErrInsecureSessionConfig", env, cookieErr)
				}
				if errors.Is(cookieErr, ErrInvalidLifetime) {
					t.Fatalf("ValidateCookieSecurity(%q) = %v, must not report a lifetime error", env, cookieErr)
				}
			})
		}
	}
}
