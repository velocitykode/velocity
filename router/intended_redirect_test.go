package router

import (
	"net/http"
	"net/url"
	"testing"
)

// TestIntended_SafeRelativePassthrough confirms that a syntactically clean
// relative path written by the auth middleware survives the sanitiser
// untouched and reaches the consumer.
func TestIntended_SafeRelativePassthrough(t *testing.T) {
	c, _ := NewTestContext("GET", "/login?"+IntendedRedirectQueryKey+"=/dashboard")
	got := c.Intended("/")
	if got != "/dashboard" {
		t.Errorf("Intended = %q, want /dashboard", got)
	}
}

// TestIntended_SafeNestedPathPassthrough exercises a multi-segment path
// with query string to confirm the consumer hands back the raw value
// when sanitisation does not flag it.
func TestIntended_SafeNestedPathPassthrough(t *testing.T) {
	// Auth middleware url.QueryEscapes the value before stuffing it into
	// the query string. Replicate that here so the test models the
	// real producer behaviour.
	original := "/admin/users/42?tab=audit"
	encoded := url.QueryEscape(original)
	c, _ := NewTestContext("GET", "/login?"+IntendedRedirectQueryKey+"="+encoded)
	got := c.Intended("/")
	if got != original {
		t.Errorf("Intended = %q, want %q", got, original)
	}
}

// TestIntended_AbsoluteForeignHostFallsBack confirms a fully-qualified
// URL pointing at a host not in RedirectAllowedHosts collapses to the
// fallback rather than passing through. This is the canonical open
// redirect vector ctx.Intended exists to defeat.
func TestIntended_AbsoluteForeignHostFallsBack(t *testing.T) {
	encoded := url.QueryEscape("https://evil.com/pwned")
	c, _ := NewTestContext("GET", "/login?"+IntendedRedirectQueryKey+"="+encoded)
	// Default empty allowlist rejects every absolute URL.
	got := c.Intended("/home")
	if got != "/home" {
		t.Errorf("Intended = %q, want /home (foreign host must fall back)", got)
	}
}

// TestIntended_BackslashLookalikeFallsBack covers the "/\evil.com" and
// "/／evil.com" normaliser-bypass vector. The router-side sanitiser
// already rejects these to "/"; ctx.Intended additionally distinguishes
// "/" (legitimate target) from "rejected" so the caller's fallback wins
// instead of silently sending the browser to "/".
func TestIntended_BackslashLookalikeFallsBack(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"backslash", "/\\evil.com"},
		{"fullwidth solidus", "/／evil.com"},
		{"big solidus", "/⧸evil.com"},
		{"fraction slash", "/⁄evil.com"},
		{"division slash", "/∕evil.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded := url.QueryEscape(tc.raw)
			c, _ := NewTestContext("GET", "/login?"+IntendedRedirectQueryKey+"="+encoded)
			got := c.Intended("/home")
			if got != "/home" {
				t.Errorf("Intended(%q) = %q, want /home (lookalike must fall back)", tc.raw, got)
			}
		})
	}
}

// TestIntended_MissingParamFallsBack confirms that a request without
// the redirect query param returns the fallback verbatim.
func TestIntended_MissingParamFallsBack(t *testing.T) {
	c, _ := NewTestContext("GET", "/login")
	got := c.Intended("/home")
	if got != "/home" {
		t.Errorf("Intended = %q, want /home", got)
	}
}

// TestIntended_EmptyParamFallsBack confirms that an empty query value
// ("/login?redirect=") returns the fallback rather than triggering the
// "rejected" branch and returning the fallback's sanitised form by
// accident.
func TestIntended_EmptyParamFallsBack(t *testing.T) {
	c, _ := NewTestContext("GET", "/login?"+IntendedRedirectQueryKey+"=")
	got := c.Intended("/dashboard")
	if got != "/dashboard" {
		t.Errorf("Intended = %q, want /dashboard", got)
	}
}

// TestIntended_LegitimateRootSlashPreserved confirms that a stored
// value of literal "/" is honoured and is NOT confused with the
// "rejected by sanitiser" branch. The internal `safe == "/" && raw != "/"`
// guard distinguishes the two cases.
func TestIntended_LegitimateRootSlashPreserved(t *testing.T) {
	c, _ := NewTestContext("GET", "/login?"+IntendedRedirectQueryKey+"=%2F")
	got := c.Intended("/home")
	if got != "/" {
		t.Errorf("Intended = %q, want / (literal / must pass through, not fall back)", got)
	}
}

// TestIntended_FallbackAlsoSanitised confirms that a buggy caller who
// passes an attacker-controlled string as fallback cannot smuggle an
// open redirect through Intended. The fallback string is also
// validated through sanitizeRedirect.
func TestIntended_FallbackAlsoSanitised(t *testing.T) {
	c, _ := NewTestContext("GET", "/login")
	// Default empty allowlist: any absolute URL collapses to "/".
	got := c.Intended("https://evil.com/")
	if got != "/" {
		t.Errorf("Intended = %q, want / (malicious fallback must be sanitised)", got)
	}
}

// TestIntended_AllowlistedAbsolutePassthrough confirms an absolute URL
// to a host in the allowlist does pass through Intended unchanged.
// This is the legitimate use case for cross-origin post-login destinations
// (e.g. a centralised auth host bouncing to one of N tenant subdomains).
func TestIntended_AllowlistedAbsolutePassthrough(t *testing.T) {
	encoded := url.QueryEscape("https://trusted.example/home")
	c, _ := NewTestContext("GET", "/login?"+IntendedRedirectQueryKey+"="+encoded)
	c.redirectAllowedHosts = []string{"trusted.example"}
	got := c.Intended("/")
	if got != "https://trusted.example/home" {
		t.Errorf("Intended = %q, want https://trusted.example/home", got)
	}
}

// TestRedirectToIntended_EmitsSeeOtherToSafeTarget confirms the
// canonical helper writes a 303 with the sanitised destination. This
// is the call shape the auth doc recommends: after a successful
// login, return ctx.RedirectToIntended("/").
func TestRedirectToIntended_EmitsSeeOtherToSafeTarget(t *testing.T) {
	c, rec := NewTestContext("GET", "/login?"+IntendedRedirectQueryKey+"=%2Fdashboard")
	if err := c.RedirectToIntended("/"); err != nil {
		t.Fatalf("RedirectToIntended: %v", err)
	}
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if loc := rec.Header().Get("Location"); loc != "/dashboard" {
		t.Errorf("Location = %q, want /dashboard", loc)
	}
}

// TestRedirectToIntended_HostileQueryFallsBackToFallback proves the
// integration story: a forged ?redirect=https://evil.com bounce
// through the auth gate cannot reach the browser because
// RedirectToIntended sanitises and falls back to the safe default.
func TestRedirectToIntended_HostileQueryFallsBackToFallback(t *testing.T) {
	encoded := url.QueryEscape("https://evil.com/")
	c, rec := NewTestContext("GET", "/login?"+IntendedRedirectQueryKey+"="+encoded)
	if err := c.RedirectToIntended("/home"); err != nil {
		t.Fatalf("RedirectToIntended: %v", err)
	}
	if rec.Code != http.StatusSeeOther {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if loc := rec.Header().Get("Location"); loc != "/home" {
		t.Errorf("Location = %q, want /home", loc)
	}
}
