package bond

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/router"
)

// stubAllowlist is a contract.RedirectAllowlist for test wiring. Returning
// a fresh slice on each call matches the router's defensive-copy semantics.
type stubAllowlist struct{ hosts []string }

func (s *stubAllowlist) AllowedRedirectHosts() []string {
	if len(s.hosts) == 0 {
		return nil
	}
	out := make([]string, len(s.hosts))
	copy(out, s.hosts)
	return out
}

// captureLogger records Warn calls so the one-time fallback warning can
// be asserted directly.
type captureLogger struct {
	mu    atomic.Pointer[[]string]
	warns atomic.Int32
}

func (l *captureLogger) Warn(msg string, _ ...any) {
	l.warns.Add(1)
	// Snapshot append: atomic load+swap pattern to keep races out of -race.
	cur := l.mu.Load()
	var next []string
	if cur != nil {
		next = append(next, *cur...)
	}
	next = append(next, msg)
	l.mu.Store(&next)
}

func (l *captureLogger) Error(string, ...any) {}

// resetHostFallbackLatch resets the process-wide warning latch so tests
// that exercise the fallback path can each observe the warning. We must
// not depend on test ordering or parallelism.
func resetHostFallbackLatch(t *testing.T) {
	t.Helper()
	hostFallbackWarned.Store(false)
	t.Cleanup(func() { hostFallbackWarned.Store(false) })
}

func requestWithAllowlist(t *testing.T, allowed []string, host string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = host
	var rl contract.RedirectAllowlist
	if allowed != nil {
		rl = &stubAllowlist{hosts: allowed}
	}
	services := &app.Services{RedirectAllowlist: rl}
	return router.WithServices(r, services)
}

// When operators configure RedirectAllowedHosts, an attacker-supplied
// r.Host (e.g. from a misconfigured fronting proxy that forwards
// X-Forwarded-Host into r.Host) must NOT be treated as same-origin.
// The allowlist is the single source of truth.
func TestRedirect_SpoofedHostBlockedByAllowlist(t *testing.T) {
	resetHostFallbackLatch(t)
	b := setupBond(t)

	// The deployment's real canonical host is trusted.example. r.Host
	// has been spoofed to evil.example by a misconfigured proxy.
	r := requestWithAllowlist(t, []string{"trusted.example"}, "evil.example")
	w := httptest.NewRecorder()
	b.Redirect(w, r, "https://evil.example/pwned")

	if got := w.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want %q (spoofed Host must not bypass allowlist)", got, "/")
	}
}

// The allowlist permits trusted.example, so a redirect to that host
// passes through verbatim even though r.Host disagrees. Proves the
// allowlist replaces (not augments) r.Host.
func TestRedirect_AllowlistedHostPermitted(t *testing.T) {
	resetHostFallbackLatch(t)
	b := setupBond(t)

	r := requestWithAllowlist(t, []string{"trusted.example"}, "irrelevant.example")
	w := httptest.NewRecorder()
	b.Redirect(w, r, "https://trusted.example/dashboard")

	if got := w.Header().Get("Location"); got != "https://trusted.example/dashboard" {
		t.Errorf("Location = %q, want passthrough", got)
	}
}

// Back consults the same allowlist as Redirect. A Referer pointing at
// a spoofed host must be rejected when an allowlist is configured.
func TestBack_SpoofedRefererHostBlocked(t *testing.T) {
	resetHostFallbackLatch(t)
	b := setupBond(t)

	r := requestWithAllowlist(t, []string{"trusted.example"}, "evil.example")
	r.Header.Set("Referer", "https://evil.example/origin")
	w := httptest.NewRecorder()
	b.Back(w, r)

	if got := w.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want %q (spoofed Referer host must not bypass allowlist)", got, "/")
	}
}

// When the request was not routed through velocity.New() and so carries
// no Services / RedirectAllowlist, bond must fall back to r.Host so
// stand-alone *Bond usage (typical for tests and partial integrations)
// keeps working. Same Host as the absolute target -> passthrough.
func TestRedirect_FallbackToHostWhenNoAllowlist(t *testing.T) {
	resetHostFallbackLatch(t)
	b := setupBond(t)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = "same.example"
	w := httptest.NewRecorder()
	b.Redirect(w, r, "https://same.example/ok")

	if got := w.Header().Get("Location"); got != "https://same.example/ok" {
		t.Errorf("Location = %q, want passthrough under r.Host fallback", got)
	}
}

// When the request has no Services AND no Host header, the fallback
// allowlist is empty so absolute URLs must be rejected. Guards against
// "" matching "" if hostInAllowlist were not careful.
func TestRedirect_FallbackWithEmptyHostRejectsCrossOrigin(t *testing.T) {
	resetHostFallbackLatch(t)
	b := setupBond(t)

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Host = ""
	w := httptest.NewRecorder()
	b.Redirect(w, r, "https://attacker.example/pwn")

	if got := w.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want / (empty r.Host must reject absolute)", got)
	}
}

// The fallback path emits a one-time warning so operators see they have
// no allowlist configured. The latch is process-wide so subsequent
// fallback redirects in the same process MUST NOT re-warn.
func TestRedirect_FallbackWarnsOnceAcrossRedirects(t *testing.T) {
	resetHostFallbackLatch(t)

	logger := &captureLogger{}
	b := setupBond(t)
	b.SetLogger(logger)

	for i := 0; i < 3; i++ {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Host = "same.example"
		w := httptest.NewRecorder()
		b.Redirect(w, r, "/dashboard")
	}

	if got := logger.warns.Load(); got != 1 {
		t.Errorf("warn count = %d, want exactly 1 across multiple fallback redirects", got)
	}
}

// When an allowlist is configured, the fallback warning must NOT fire
// because the allowlist path supplants r.Host without any risk.
func TestRedirect_AllowlistConfiguredDoesNotWarn(t *testing.T) {
	resetHostFallbackLatch(t)

	logger := &captureLogger{}
	b := setupBond(t)
	b.SetLogger(logger)

	r := requestWithAllowlist(t, []string{"trusted.example"}, "irrelevant.example")
	w := httptest.NewRecorder()
	b.Redirect(w, r, "/dashboard")

	if got := logger.warns.Load(); got != 0 {
		t.Errorf("warn count = %d, want 0 when allowlist is configured", got)
	}
}
