package csrf

import (
	"errors"
	"testing"
)

// TestCSRFConfig_Validate pins the Config.Validate rules. The XSRF
// cookie's attributes come from CookiePolicy (validated with the session
// config), so the only rule left is the supported binding Mode.
func TestCSRFConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(c *Config)
		wantErr bool
	}{
		{name: "default config ok", mutate: func(c *Config) {}},
		{name: "unsupported mode ModeDoubleSubmit rejected", mutate: func(c *Config) { c.Mode = ModeDoubleSubmit }, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DefaultConfig()
			tt.mutate(cfg)
			err := cfg.Validate()
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
	if err := c.Validate(); err == nil {
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
