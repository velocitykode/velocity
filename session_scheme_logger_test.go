package velocity

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/auth/drivers/schemes"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/router"
)

// The bootstrapped app's session scheme logs through the app logger: a
// cookie-mode save that outgrows the 4096 byte cookie limit reports its
// warning through a.Log.
func TestSessionScheme_LogsThroughTheAppLogger(t *testing.T) {
	capture := &levelLogger{}
	const driverName = "session-scheme-logger-capture"
	prev := log.Drivers().Override(driverName, func(context.Context, log.LogConfig) (log.Logger, error) {
		return capture, nil
	})
	t.Cleanup(func() { log.Drivers().Override(driverName, prev) })

	for k, v := range map[string]string{
		"APP_ENV":               "development",
		"APP_KEY":               strings.Repeat("k", 32),
		"AUTH_SCHEME":           "web",
		"CACHE_DRIVER":          "memory",
		"QUEUE_DRIVER":          "memory",
		"MAIL_DRIVER":           "log",
		"SESSION_SECURE":        "false",
		"SESSION_IDLE_LIFETIME": "120",
		"SESSION_STORE":         "cookie",
	} {
		t.Setenv(k, v)
	}
	cfg := ConfigFromEnv()
	cfg.Log = log.LogConfig{Driver: driverName, Config: make(map[string]any)}
	a, err := New(WithConfig(cfg))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = a.Shutdown(context.Background()) })
	a.Router.Get("/big", func(c *router.Context) error {
		schemes.SessionFromRequest(c.Request).Put("draft", strings.Repeat("x", 5000))
		return c.String(http.StatusOK, "ok")
	})

	capture.reset()
	csrfBagJar{}.send(t, a.Router, http.MethodGet, "/big", "")

	const want = "velocity/auth: session not saved: the session cookie would exceed 4096 bytes"
	capture.mu.Lock()
	defer capture.mu.Unlock()
	for _, e := range capture.entries {
		if e.level == "warn" && strings.HasPrefix(e.msg, want) {
			return
		}
	}
	t.Fatalf("no %q warning through the app logger; entries: %+v", want, capture.entries)
}
