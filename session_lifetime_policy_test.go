package velocity

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/csrf/stores"
)

// The CSRF token lives in the session, so it has the session's lifetime:
// New installs the session-bag token store, which refuses a consumed
// single-use token for as long as a captured session cookie can be kept
// alive by renewal, the session's absolute cap.
func TestNew_CSRFTokenLivesInTheSession(t *testing.T) {
	tests := []struct {
		name     string
		idle     string
		absolute string
		want     time.Duration
	}{
		{"idle timeout: default absolute cap", "45", "", 30 * 24 * time.Hour},
		{"idle timeout: absolute cap", "45", "600", 600 * time.Minute},
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
			a, err := New(WithConfig(ConfigFromEnv()))
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
			if _, ok := a.config.CSRF.Store.(*stores.SessionBagStore); !ok {
				t.Fatalf("CSRF store %T, want *stores.SessionBagStore", a.config.CSRF.Store)
			}
			if got := csrfConsumedTokenLifetime(a.config.Session); got != tt.want {
				t.Fatalf("consumed token lifetime = %v, want %v", got, tt.want)
			}
		})
	}
}

// A session with no absolute cap can be renewed forever, so a consumed
// single-use token could never be refused for as long as a captured cookie
// carries it: New refuses that combination and accepts either half alone.
func TestNew_SingleUseCSRFNeedsAnAbsoluteCap(t *testing.T) {
	for _, tt := range []struct {
		name      string
		absolute  string
		singleUse string
		wantErr   bool
	}{
		{"single use without an absolute cap", "-1", "true", true},
		{"single use with the default cap", "", "true", false},
		{"no absolute cap without single use", "-1", "false", false},
	} {
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
				"SESSION_ABSOLUTE_LIFETIME": tt.absolute,
				"CSRF_SINGLE_USE":           tt.singleUse,
			} {
				t.Setenv(k, v)
			}
			a, err := New(WithConfig(ConfigFromEnv()))
			if tt.wantErr {
				if !errors.Is(err, ErrInvalidConfig) {
					t.Fatalf("New = %v, want ErrInvalidConfig", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
		})
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
