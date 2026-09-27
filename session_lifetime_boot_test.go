package velocity

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth"
)

// setSessionBootEnv sets the environment for a New() that reaches the
// session config checks: a session scheme, in-memory drivers and a key.
func setSessionBootEnv(t *testing.T, env map[string]string) {
	t.Helper()
	base := map[string]string{
		"APP_KEY":      strings.Repeat("k", 32),
		"AUTH_SCHEME":  "web",
		"LOG_DRIVER":   "null",
		"CACHE_DRIVER": "memory",
		"QUEUE_DRIVER": "memory",
		"MAIL_DRIVER":  "log",
	}
	for k, v := range env {
		base[k] = v
	}
	for k, v := range base {
		t.Setenv(k, v)
	}
}

// A negative session lifetime is a value error, not a cookie security
// relaxation: New refuses it in a development profile exactly as in
// production, with the same error.
func TestNew_NegativeSessionLifetimeFailsInEveryEnvironment(t *testing.T) {
	for _, lifetime := range []struct {
		name string
		key  string
	}{
		{"negative remember lifetime", "SESSION_REMEMBER_LIFETIME"},
		{"negative idle lifetime", "SESSION_IDLE_LIFETIME"},
	} {
		for _, env := range []struct {
			appEnv string
			secure string
		}{
			{"local", "false"},
			{"development", "false"},
			{"testing", "false"},
			{"production", "true"},
		} {
			t.Run(lifetime.name+"/"+env.appEnv, func(t *testing.T) {
				setSessionBootEnv(t, map[string]string{
					"APP_ENV":        env.appEnv,
					"SESSION_SECURE": env.secure,
					lifetime.key:     "-1",
				})
				a, err := New(WithConfig(ConfigFromEnv()))
				if err == nil {
					_ = a.Shutdown(context.Background())
					t.Fatalf("New succeeded with %s=-1 in %q, want ErrInvalidLifetime", lifetime.key, env.appEnv)
				}
				if !errors.Is(err, auth.ErrInvalidLifetime) {
					t.Fatalf("New = %v, want ErrInvalidLifetime", err)
				}
				if !errors.Is(err, ErrInvalidConfig) {
					t.Fatalf("New = %v, want ErrInvalidConfig", err)
				}
			})
		}
	}
}

// An insecure cookie attribute stays relaxed in a development profile: New
// boots (the warning itself is pinned by TestCheckSessionCookieSecurity).
func TestNew_InsecureSessionCookieBootsInDevelopment(t *testing.T) {
	setSessionBootEnv(t, map[string]string{
		"APP_ENV":           "local",
		"SESSION_SECURE":    "false",
		"SESSION_HTTP_ONLY": "false",
	})
	a, err := New(WithConfig(ConfigFromEnv()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	_ = a.Shutdown(context.Background())
}
