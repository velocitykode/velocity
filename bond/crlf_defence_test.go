package bond

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// CRLF defence-in-depth: net/http catches CR/LF in header values at
// write time, but the framework should never let those bytes reach a
// Header().Set sink. The three call paths exercised here are:
//
//   - Bond.RedirectWithStatus  (X-Inertia-Location for Inertia requests)
//   - Bond.Location            (X-Inertia-Location for 409 responses)
//   - Bond.Middleware          (Location header for version-mismatch 409)
//
// In every case a "/foo\r\nX-Injected: 1" input must NOT land an
// X-Injected header on the response, and the surviving Location /
// X-Inertia-Location value must have CR/LF stripped.

func TestStripCRLF_RemovesBothBytes(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/foo\r\nX-Injected: 1", "/fooX-Injected: 1"},
		{"/bar\n\rmore", "/barmore"},
		{"/clean", "/clean"},
		{"", ""},
		{"\r", ""},
		{"\n", ""},
	}
	for _, tc := range cases {
		if got := stripCRLF(tc.in); got != tc.want {
			t.Errorf("stripCRLF(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRedirectWithStatus_StripsCRLFFromXInertiaLocation(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/login", nil)
	r.Header.Set("X-Inertia", "true")

	// Construct a relative target that includes CR/LF + a forged header
	// line. sanitizeRedirectURL passes "/" through unchanged (no host),
	// so the CRLF strip is the only defence in this layer.
	b.RedirectWithStatus(w, r, "/dashboard\r\nX-Injected: 1", http.StatusSeeOther)

	if w.Header().Get("X-Injected") != "" {
		t.Errorf("X-Injected header present: %q", w.Header().Get("X-Injected"))
	}
	loc := w.Header().Get("X-Inertia-Location")
	if strings.ContainsAny(loc, "\r\n") {
		t.Errorf("X-Inertia-Location contains CR/LF: %q", loc)
	}
	if loc == "" {
		t.Errorf("X-Inertia-Location empty; expected sanitised /dashboardX-Injected: 1")
	}
}

func TestLocation_StripsCRLFFromXInertiaLocation(t *testing.T) {
	b := setupBond(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("X-Inertia", "true")

	b.Location(w, r, "https://external.example/\r\nX-Injected: 1")

	if w.Header().Get("X-Injected") != "" {
		t.Errorf("X-Injected header present: %q", w.Header().Get("X-Injected"))
	}
	loc := w.Header().Get("X-Inertia-Location")
	if strings.ContainsAny(loc, "\r\n") {
		t.Errorf("X-Inertia-Location contains CR/LF: %q", loc)
	}
}

func TestMiddleware_VersionMismatch_StripsCRLFFromLocation(t *testing.T) {
	b := setupBond(t)

	// Inner handler must not be called: a 409 is emitted before next.
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("next handler must not run on version mismatch")
	})
	h := b.Middleware(next)

	// Forge a path containing CR/LF. httptest.NewRequest will accept
	// the URL via url.Parse; we go through url.URL directly to bypass
	// any sanitisation Go's helpers might otherwise apply.
	u := &url.URL{Path: "/foo\r\nX-Injected: 1"}
	w := httptest.NewRecorder()
	r := &http.Request{
		Method: http.MethodGet,
		URL:    u,
		Header: http.Header{
			"X-Inertia":   []string{"true"},
			HeaderVersion: []string{"client-version-mismatch"},
		},
	}

	h.ServeHTTP(w, r)

	if w.Code != http.StatusConflict {
		t.Errorf("status = %d, want %d", w.Code, http.StatusConflict)
	}
	if w.Header().Get("X-Injected") != "" {
		t.Errorf("X-Injected header present: %q", w.Header().Get("X-Injected"))
	}
	loc := w.Header().Get(HeaderLocation)
	if strings.ContainsAny(loc, "\r\n") {
		t.Errorf("Location contains CR/LF: %q", loc)
	}
}
