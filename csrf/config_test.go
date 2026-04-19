package csrf

import (
	"errors"
	"net/http"
	"testing"
)

// TestCSRFConfig_Validate_RejectsInsecureDefaults pins the matrix of
// Config.Validate rules. The CSRF package previously had no Validate
// method — apps could ship with Secure=false / HttpOnly=false / zero
// SameSite / an unsupported Mode and boot silently.
func TestCSRFConfig_Validate_RejectsInsecureDefaults(t *testing.T) {
	ok := func() *Config { return DefaultConfig() }

	tests := []struct {
		name    string
		mutate  func(c *Config)
		env     string
		wantErr bool
	}{
		{
			name:    "baseline ok in production",
			mutate:  func(c *Config) {},
			env:     "production",
			wantErr: false,
		},
		{
			name:    "Secure=false rejected in production",
			mutate:  func(c *Config) { c.Secure = false },
			env:     "production",
			wantErr: true,
		},
		{
			name:    "Secure=false rejected with empty env (treat as prod)",
			mutate:  func(c *Config) { c.Secure = false },
			env:     "",
			wantErr: true,
		},
		{
			name:    "Secure=false allowed in development",
			mutate:  func(c *Config) { c.Secure = false },
			env:     "development",
			wantErr: false,
		},
		{
			name:    "Secure=false allowed in testing",
			mutate:  func(c *Config) { c.Secure = false },
			env:     "testing",
			wantErr: false,
		},
		{
			name:    "HttpOnly=false without opt-in rejected",
			mutate:  func(c *Config) { c.HttpOnly = false },
			env:     "production",
			wantErr: true,
		},
		{
			name: "HttpOnly=false allowed with AllowJSAccess=true opt-in",
			mutate: func(c *Config) {
				c.HttpOnly = false
				c.AllowJSAccess = true
			},
			env:     "production",
			wantErr: false,
		},
		{
			name:    "SameSite zero value rejected",
			mutate:  func(c *Config) { c.SameSite = http.SameSiteDefaultMode },
			env:     "production",
			wantErr: true,
		},
		{
			name:    "SameSite=None without Secure rejected",
			mutate:  func(c *Config) { c.SameSite = http.SameSiteNoneMode; c.Secure = false },
			env:     "production",
			wantErr: true,
		},
		{
			name:    "SameSite=None with Secure accepted",
			mutate:  func(c *Config) { c.SameSite = http.SameSiteNoneMode },
			env:     "production",
			wantErr: false,
		},
		{
			name:    "SameSite=Strict accepted",
			mutate:  func(c *Config) { c.SameSite = http.SameSiteStrictMode },
			env:     "production",
			wantErr: false,
		},
		{
			name:    "unsupported mode ModeDoubleSubmit rejected",
			mutate:  func(c *Config) { c.Mode = ModeDoubleSubmit },
			env:     "production",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := ok()
			tt.mutate(cfg)
			err := cfg.Validate(tt.env)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				if !errors.Is(err, ErrInsecureCSRFConfig) {
					t.Errorf("expected ErrInsecureCSRFConfig, got %v", err)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestCSRFConfig_Validate_NilReceiver defends against nil-config paths:
// (*Config).Validate on nil must return an error, not panic.
func TestCSRFConfig_Validate_NilReceiver(t *testing.T) {
	var c *Config
	if err := c.Validate("production"); err == nil {
		t.Fatal("expected error for nil receiver")
	}
}

// TestMode_String sanity-checks the Mode stringer — the value is used in
// error messages so a silent off-by-one would produce confusing output.
func TestMode_String(t *testing.T) {
	cases := map[Mode]string{
		ModeSession:      "session",
		ModeDoubleSubmit: "double-submit",
		Mode(42):         "unknown(42)",
	}
	for m, want := range cases {
		if got := m.String(); got != want {
			t.Errorf("Mode(%d).String() = %q, want %q", int(m), got, want)
		}
	}
}
