package velocity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/velocitykode/velocity/auth"
	"github.com/velocitykode/velocity/csrf"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/router"
)

// baseSignedURLBootConfig produces a minimal Config suitable for boot
// guard regression tests. Callers override Env / Key on the returned
// copy. Memory drivers everywhere so no external services are touched.
// Session and CSRF cookies are pre-populated with production-safe
// defaults (Secure + HttpOnly + SameSite=Lax) so the App.New() cookie
// validators do not short-circuit before the signed-URL boot guard runs.
func baseSignedURLBootConfig() Config {
	csrfCfg := csrf.DefaultConfig()
	return Config{
		Debug: false, // production must not set Debug=true
		Port:  "0",
		Cache: CacheConfig{Driver: "memory", Prefix: "test_cache"},
		Log: log.LogConfig{
			Driver: "console",
			Config: make(map[string]any),
		},
		Queue: QueueConfig{Driver: "memory"},
		Mail:  mail.MailConfig{Driver: "log"},
		Session: auth.SessionConfig{
			Name:     "velocity_session",
			Lifetime: 120,
			Path:     "/",
			Secure:   true,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		},
		CSRF: *csrfCfg,
	}
}

// TestNew_ProductionRefusesBootWithoutAppKey is the M-16 boot-guard
// regression. Before the fix, an operator who set CRYPTO_KEY (which
// satisfies the line ~190 crypto check) but left APP_KEY empty would
// boot to production with the router's signed-URL key slot nil. The
// SignedMiddleware then failed open and turned every protected signed
// route into an unauthenticated route. The fix mirrors the existing
// APP_KEY/CRYPTO_KEY gating: production (anything other than testing /
// development) must have APP_KEY set, full stop.
func TestNew_ProductionRefusesBootWithoutAppKey(t *testing.T) {
	// Opt out of the queue-signing prod gate so the signed-URL guard
	// (the subject of this test) is the one that fires, not the
	// upstream queue check.
	t.Setenv("QUEUE_ACCEPT_UNSIGNED", "true")
	cfg := baseSignedURLBootConfig()
	cfg.Env = "production"
	cfg.Key = "" // M-16 trigger: no APP_KEY
	// Mimic the operator-only-set-CRYPTO_KEY misconfiguration: the
	// crypto subsystem has a dedicated key so the line ~190 check
	// does not fire, but APP_KEY is still empty so the signed-URL
	// boot guard must.
	cfg.Crypto.Key = "0123456789abcdef0123456789abcdef" // 32 bytes for AES-256

	_, err := New(WithConfig(cfg))
	if err == nil {
		t.Fatal("expected New() to refuse production boot with empty APP_KEY")
	}
	if !errors.Is(err, ErrNoAppKey) {
		t.Fatalf("expected error to wrap ErrNoAppKey, got %v", err)
	}
}

// TestNew_StagingRefusesBootWithoutAppKey pins the same guard for any
// non-{testing,development} environment. A staging deployment that
// silently downgraded signed routes to unsigned routes would be a
// classic "tests pass in CI, prod cuts auth" trap.
func TestNew_StagingRefusesBootWithoutAppKey(t *testing.T) {
	t.Setenv("QUEUE_ACCEPT_UNSIGNED", "true")
	cfg := baseSignedURLBootConfig()
	cfg.Env = "staging"
	cfg.Key = ""
	cfg.Crypto.Key = "0123456789abcdef0123456789abcdef"

	_, err := New(WithConfig(cfg))
	if err == nil {
		t.Fatal("expected New() to refuse staging boot with empty APP_KEY")
	}
	if !errors.Is(err, ErrNoAppKey) {
		t.Fatalf("expected error to wrap ErrNoAppKey, got %v", err)
	}
}

// TestNew_DevelopmentBootsWithoutAppKey verifies the dev-ergonomics
// escape hatch survives the fix. A fresh project before `vel
// key:generate` must still boot, and the middleware must be in the
// fail-closed mode (proven separately by router tests) so any signed
// route the developer wires up surfaces the misconfiguration on the
// first request rather than silently downgrading to unsigned.
func TestNew_DevelopmentBootsWithoutAppKey(t *testing.T) {
	cfg := baseSignedURLBootConfig()
	cfg.Env = "development"
	cfg.Key = ""
	// Crypto.Key intentionally empty too; matches a brand-new project
	// where the operator has not run vel key:generate yet.

	app, err := New(WithConfig(cfg))
	if err != nil {
		t.Fatalf("development boot without APP_KEY should succeed (warn-only), got: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	// Router signed-URL key must remain unset: the boot path skipped
	// derivation, and SignedMiddleware will fail closed on any signed
	// route that gets hit.
	mw := app.Router.SignedMiddleware()
	wrapped := mw(func(c *router.Context) error { return nil })
	req := httptest.NewRequest("GET", "/anything?signature=x&expires=99999999999", nil)
	c, _ := router.NewTestContext("GET", "/anything")
	c.Request = req
	err = wrapped(c)
	httpErr, ok := err.(*router.HTTPError)
	if !ok {
		t.Fatalf("expected *router.HTTPError when key missing, got %T (%v)", err, err)
	}
	if httpErr.Code != http.StatusForbidden {
		t.Errorf("expected 403, got %d", httpErr.Code)
	}
	if !errors.Is(httpErr.Internal, router.ErrSignedURLKeyMissing) {
		t.Errorf("expected Internal to wrap router.ErrSignedURLKeyMissing, got %v", httpErr.Internal)
	}
}

// TestNew_TestingBootsWithoutAppKey is the test-harness escape hatch.
// Unit tests that construct a real *App via New() but do not need
// signed URLs must keep working without wiring an APP_KEY. The
// NewTestApp helper relies on this.
func TestNew_TestingBootsWithoutAppKey(t *testing.T) {
	cfg := baseSignedURLBootConfig()
	cfg.Env = "testing"
	cfg.Key = ""

	app, err := New(WithConfig(cfg))
	if err != nil {
		t.Fatalf("testing boot without APP_KEY must succeed, got: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
}

// TestNew_ProductionBootsWithAppKey is the happy-path lock: when
// APP_KEY is set, the boot guard does not fire, the router's
// signed-URL key gets derived, and SignedMiddleware enforces.
func TestNew_ProductionBootsWithAppKey(t *testing.T) {
	cfg := baseSignedURLBootConfig()
	cfg.Env = "production"
	cfg.Key = "this-is-a-32-byte-app-key-aaaa!!"
	cfg.Crypto.Key = "this-is-a-32-byte-app-key-aaaa!!"

	app, err := New(WithConfig(cfg))
	if err != nil {
		t.Fatalf("production boot with APP_KEY must succeed, got: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	// Mint a signed URL through the router and prove the middleware
	// accepts it. Round-trips the full HKDF derivation path.
	app.Router.Get("/signed-route/{id}", func(c *router.Context) error {
		return c.String(http.StatusOK, "ok")
	}).Name("signed.route")
	// Commit by serving a no-op request.
	w := httptest.NewRecorder()
	app.Router.ServeHTTP(w, httptest.NewRequest("GET", "/signed-route/1", nil))

	signed, err := app.Router.TemporarySignedURL("signed.route", map[string]string{"id": "42"}, nil, time.Hour)
	if err != nil {
		t.Fatalf("SignedURL after boot: %v", err)
	}
	if err := app.Router.ValidateSignature(httptest.NewRequest("GET", signed, nil)); err != nil {
		t.Fatalf("ValidateSignature on freshly minted URL: %v", err)
	}
}
