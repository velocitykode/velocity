package bond

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
)

const hostFallbackWarning = "velocity/bond: no RedirectAllowlist configured"

// fallbackRedirect sends one same-host redirect through b with no
// RedirectAllowlist, the path that falls back to r.Host.
func fallbackRedirect(b *Bond) {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "same.example"
	b.Redirect(httptest.NewRecorder(), r, "/dashboard")
}

// A Bond without a logger writes the redirect-allowlist fallback warning
// through the one framework fallback, once, and nothing through the
// standard library log package or slog.Default.
func TestRedirect_WithoutLoggerWarnsThroughTheFallback(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	stdlib := fallbacklogtest.CaptureStdlib(t)

	b := setupBond(t)
	fallbackRedirect(b)
	fallbackRedirect(b)

	if got := fallback.Count("WARN", hostFallbackWarning); got != 1 {
		t.Errorf("fallback lines = %d, want 1: %q", got, fallback.String())
	}
	if out := stdlib.String(); out != "" {
		t.Errorf("stdlib log / slog.Default got %q, want nothing", out)
	}
}

// The warning is once per Bond, so each Bond's logger hears it: a Bond
// built for one app cannot swallow it for another app in the process.
func TestRedirect_FallbackWarnsOncePerBond(t *testing.T) {
	first, second := &captureLogger{}, &captureLogger{}
	a, b := setupBond(t), setupBond(t)
	a.SetLogger(first)
	b.SetLogger(second)

	fallbackRedirect(a)
	fallbackRedirect(a)
	fallbackRedirect(b)

	if got := first.warns.Load(); got != 1 {
		t.Errorf("first bond warns = %d, want 1", got)
	}
	if got := second.warns.Load(); got != 1 {
		t.Errorf("second bond warns = %d, want 1", got)
	}
}
