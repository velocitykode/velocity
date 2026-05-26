package velocity

import (
	"net/http"
	"os"
	"testing"

	"github.com/velocitykode/velocity/csrf"
)

// TestConfigFromEnv_CSRFMatchesPackageDefault pins the contract that
// velocity.ConfigFromEnv produces a csrf.Config equivalent to
// csrf.DefaultConfig() when no CSRF_* env vars are set. Without this
// pin, a new field added to csrf.DefaultConfig (e.g. WriteXSRFCookie
// added by M-03) silently regresses to the zero value for every
// velocity.New app, since ConfigFromEnv builds its own literal rather
// than inheriting from the package default.
func TestConfigFromEnv_CSRFMatchesPackageDefault(t *testing.T) {
	// Clear CSRF_* env so the env-override branch is dormant. The test
	// runs in this process, so use t.Setenv to scope cleanup.
	for _, k := range []string{
		"CSRF_TOKEN_LIFETIME",
		"CSRF_HEADER",
		"CSRF_FORM_FIELD",
		"CSRF_COOKIE_NAME",
		"CSRF_SESSION_COOKIE",
		"CSRF_SAME_SITE",
		"CSRF_SECURE",
		"CSRF_HTTP_ONLY",
		"CSRF_SINGLE_USE",
		"CSRF_ERROR_MESSAGE",
		"CSRF_WRITE_XSRF_COOKIE",
		"CSRF_XSRF_COOKIE_NAME",
	} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}

	cfg := ConfigFromEnv()
	def := csrf.DefaultConfig()

	if cfg.CSRF.TokenLifetime != def.TokenLifetime {
		t.Errorf("TokenLifetime: env=%v default=%v", cfg.CSRF.TokenLifetime, def.TokenLifetime)
	}
	if cfg.CSRF.HeaderName != def.HeaderName {
		t.Errorf("HeaderName: env=%q default=%q", cfg.CSRF.HeaderName, def.HeaderName)
	}
	if cfg.CSRF.FormField != def.FormField {
		t.Errorf("FormField: env=%q default=%q", cfg.CSRF.FormField, def.FormField)
	}
	if cfg.CSRF.CookieName != def.CookieName {
		t.Errorf("CookieName: env=%q default=%q", cfg.CSRF.CookieName, def.CookieName)
	}
	if cfg.CSRF.SameSite != def.SameSite {
		t.Errorf("SameSite: env=%v default=%v", cfg.CSRF.SameSite, def.SameSite)
	}
	if cfg.CSRF.Secure != def.Secure {
		t.Errorf("Secure: env=%v default=%v", cfg.CSRF.Secure, def.Secure)
	}
	if cfg.CSRF.HttpOnly != def.HttpOnly {
		t.Errorf("HttpOnly: env=%v default=%v", cfg.CSRF.HttpOnly, def.HttpOnly)
	}
	if cfg.CSRF.SingleUse != def.SingleUse {
		t.Errorf("SingleUse: env=%v default=%v", cfg.CSRF.SingleUse, def.SingleUse)
	}
	if cfg.CSRF.MaxFormBodyBytes != def.MaxFormBodyBytes {
		t.Errorf("MaxFormBodyBytes: env=%d default=%d", cfg.CSRF.MaxFormBodyBytes, def.MaxFormBodyBytes)
	}
	if cfg.CSRF.Mode != def.Mode {
		t.Errorf("Mode: env=%v default=%v", cfg.CSRF.Mode, def.Mode)
	}
	if cfg.CSRF.WriteXSRFCookie != def.WriteXSRFCookie {
		t.Errorf("WriteXSRFCookie: env=%v default=%v (M-03 regression)", cfg.CSRF.WriteXSRFCookie, def.WriteXSRFCookie)
	}
	if cfg.CSRF.XSRFCookieName != def.XSRFCookieName {
		t.Errorf("XSRFCookieName: env=%q default=%q (M-03 regression)", cfg.CSRF.XSRFCookieName, def.XSRFCookieName)
	}
	if cfg.CSRF.ErrorMessage != def.ErrorMessage {
		t.Errorf("ErrorMessage: env=%q default=%q", cfg.CSRF.ErrorMessage, def.ErrorMessage)
	}
}

// TestConfigFromEnv_CSRFEnvOverridesXSRFCookie pins the explicit
// CSRF_WRITE_XSRF_COOKIE and CSRF_XSRF_COOKIE_NAME env knobs so
// operators can disable or rename the SPA cookie without editing
// source.
func TestConfigFromEnv_CSRFEnvOverridesXSRFCookie(t *testing.T) {
	t.Setenv("CSRF_WRITE_XSRF_COOKIE", "false")
	t.Setenv("CSRF_XSRF_COOKIE_NAME", "MY-XSRF")

	cfg := ConfigFromEnv()
	if cfg.CSRF.WriteXSRFCookie != false {
		t.Errorf("CSRF_WRITE_XSRF_COOKIE=false should disable; got %v", cfg.CSRF.WriteXSRFCookie)
	}
	if cfg.CSRF.XSRFCookieName != "MY-XSRF" {
		t.Errorf("CSRF_XSRF_COOKIE_NAME override; got %q", cfg.CSRF.XSRFCookieName)
	}
	// Silence the unused-import linter if http stops being needed
	// elsewhere; reserved for follow-up Secure-flag checks.
	_ = http.SameSiteLaxMode
}
