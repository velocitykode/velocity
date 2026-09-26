package velocity

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
)

// The CSRF token lives on the session's lifetime policy: New sets its idle
// lifetime from the session's idle timeout (the absolute cap when the
// session has no idle timeout), whatever the CSRF config carried.
func TestNew_CSRFTokenLivesOnSessionLifetime(t *testing.T) {
	tests := []struct {
		name     string
		idle     string
		absolute string
		want     time.Duration
	}{
		{"idle timeout", "45", "", 45 * time.Minute},
		{"no idle timeout: absolute cap", "0", "600", 600 * time.Minute},
		{"no idle timeout: default absolute cap", "0", "", 30 * 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range map[string]string{
				"APP_ENV":                   "local",
				"APP_KEY":                   strings.Repeat("k", 32),
				"AUTH_SCHEME":               "web",
				"LOG_DRIVER":                "null",
				"CACHE_DRIVER":              "memory",
				"QUEUE_DRIVER":              "memory",
				"MAIL_DRIVER":               "log",
				"SESSION_SECURE":            "false",
				"SESSION_IDLE_LIFETIME":     tt.idle,
				"SESSION_ABSOLUTE_LIFETIME": tt.absolute,
			} {
				t.Setenv(k, v)
			}
			cfg := ConfigFromEnv()
			cfg.CSRF.TokenIdleLifetime = 24 * time.Hour
			a, err := New(WithConfig(cfg))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
			if got := a.config.CSRF.TokenIdleLifetime; got != tt.want {
				t.Fatalf("CSRF TokenIdleLifetime = %v, want %v", got, tt.want)
			}
		})
	}
}

// csrfTokenIdleLifetime leaves the token store default for a session with
// neither an idle timeout nor an absolute cap.
func TestCSRFTokenIdleLifetime_UnboundedSession(t *testing.T) {
	if got := csrfTokenIdleLifetime(auth.SessionConfig{IdleLifetime: 0, AbsoluteLifetime: -1}); got != 0 {
		t.Fatalf("got %v, want 0 (store default)", got)
	}
}

// SESSION_REMEMBER_LIFETIME sets the remember-me lifetime on its own,
// unset leaves the 30-day default.
func TestConfigFromEnv_SessionRememberLifetime(t *testing.T) {
	tests := []struct {
		env  string
		want time.Duration
	}{
		{"", 30 * 24 * time.Hour},
		{"20160", 14 * 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Setenv("SESSION_IDLE_LIFETIME", "120")
		t.Setenv("SESSION_REMEMBER_LIFETIME", tt.env)
		if got := ConfigFromEnv().Session.RememberTimeout(); got != tt.want {
			t.Errorf("SESSION_REMEMBER_LIFETIME=%q: RememberTimeout = %v, want %v", tt.env, got, tt.want)
		}
	}
}
