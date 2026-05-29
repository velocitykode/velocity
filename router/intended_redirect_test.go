package router

import (
	"net/http"
	"testing"
)

// stashIntended wires c.intendedFn to return raw once, mimicking the
// session-backed resolver wired by Router.SetIntendedResolver (which auth's
// denyUnauthenticated feeds via the IntendedSessionKey session value).
func stashIntended(c *Context, raw string) {
	c.intendedFn = func(*Context) string { return raw }
}

// TestIntended_SafeRelativePassthrough confirms a clean relative path
// stashed in the session survives the sanitiser untouched.
func TestIntended_SafeRelativePassthrough(t *testing.T) {
	c, _ := NewTestContext("GET", "/login")
	stashIntended(c, "/dashboard")
	got := c.Intended("/")
	if got != "/dashboard" {
		t.Errorf("Intended = %q, want /dashboard", got)
	}
}

// TestIntended_SafeNestedPathPassthrough exercises a multi-segment path
// with query string to confirm the consumer hands back the raw value
// when sanitisation does not flag it.
func TestIntended_SafeNestedPathPassthrough(t *testing.T) {
	original := "/admin/users/42?tab=audit"
	c, _ := NewTestContext("GET", "/login")
	stashIntended(c, original)
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
	c, _ := NewTestContext("GET", "/login")
	stashIntended(c, "https://evil.com/pwned")
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
			c, _ := NewTestContext("GET", "/login")
			stashIntended(c, tc.raw)
			got := c.Intended("/home")
			if got != "/home" {
				t.Errorf("Intended(%q) = %q, want /home (lookalike must fall back)", tc.raw, got)
			}
		})
	}
}

// TestIntended_NoResolverFallsBack confirms that when no resolver is wired
// (nothing ever stashed an intended URL) the fallback is returned verbatim.
func TestIntended_NoResolverFallsBack(t *testing.T) {
	c, _ := NewTestContext("GET", "/login")
	// intendedFn left nil.
	got := c.Intended("/home")
	if got != "/home" {
		t.Errorf("Intended = %q, want /home", got)
	}
}

// TestIntended_EmptyStashFallsBack confirms that a resolver returning ""
// (no value in the session) returns the fallback rather than triggering
// the "rejected" branch.
func TestIntended_EmptyStashFallsBack(t *testing.T) {
	c, _ := NewTestContext("GET", "/login")
	stashIntended(c, "")
	got := c.Intended("/dashboard")
	if got != "/dashboard" {
		t.Errorf("Intended = %q, want /dashboard", got)
	}
}

// TestIntended_LegitimateRootSlashPreserved confirms that a stored value
// of literal "/" is honoured and is NOT confused with the "rejected by
// sanitiser" branch. The internal `safe == "/" && raw != "/"` guard
// distinguishes the two cases.
func TestIntended_LegitimateRootSlashPreserved(t *testing.T) {
	c, _ := NewTestContext("GET", "/login")
	stashIntended(c, "/")
	got := c.Intended("/home")
	if got != "/" {
		t.Errorf("Intended = %q, want / (literal / must pass through, not fall back)", got)
	}
}

// TestIntended_FallbackAlsoSanitised confirms that a buggy caller who
// passes an attacker-controlled string as fallback cannot smuggle an
// open redirect through Intended. The fallback string is also validated
// through sanitizeRedirect.
func TestIntended_FallbackAlsoSanitised(t *testing.T) {
	c, _ := NewTestContext("GET", "/login")
	// No stash; default empty allowlist collapses the malicious fallback.
	got := c.Intended("https://evil.com/")
	if got != "/" {
		t.Errorf("Intended = %q, want / (malicious fallback must be sanitised)", got)
	}
}

// TestIntended_AllowlistedAbsolutePassthrough confirms an absolute URL to
// a host in the allowlist does pass through Intended unchanged.
func TestIntended_AllowlistedAbsolutePassthrough(t *testing.T) {
	c, _ := NewTestContext("GET", "/login")
	stashIntended(c, "https://trusted.example/home")
	c.redirectAllowedHosts = []string{"trusted.example"}
	got := c.Intended("/")
	if got != "https://trusted.example/home" {
		t.Errorf("Intended = %q, want https://trusted.example/home", got)
	}
}

// TestRedirectToIntended_EmitsSeeOtherToSafeTarget confirms the canonical
// helper writes a 303 with the sanitised destination.
func TestRedirectToIntended_EmitsSeeOtherToSafeTarget(t *testing.T) {
	c, rec := NewTestContext("GET", "/login")
	stashIntended(c, "/dashboard")
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

// TestRedirectToIntended_HostileStashFallsBackToFallback proves a forged
// destination smuggled into the session cannot reach the browser:
// RedirectToIntended sanitises and falls back to the safe default.
func TestRedirectToIntended_HostileStashFallsBackToFallback(t *testing.T) {
	c, rec := NewTestContext("GET", "/login")
	stashIntended(c, "https://evil.com/")
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
