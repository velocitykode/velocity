package router

import (
	"errors"
	"net/http"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// TestRedirectSinks_MatchContract asserts the router's redirect surfaces
// give contract.SanitizeRedirect's answer, with and without an allowlist:
// the public SanitizeRedirect returns it, Context.Redirect writes "/" for
// every target it rewrites, and the Context's RenderContext refuses
// exactly those targets.
func TestRedirectSinks_MatchContract(t *testing.T) {
	targets := []string{
		"", "/", "/dashboard", "/dashboard?x=1", "foo.html",
		"//evil.com", "///evil", `/\evil.com/pwned`, `\\evil.com/pwned`,
		"/／evil.com", "/⧸evil.com", "/⁄evil.com", "/∕evil.com",
		"javascript:alert(1)", "data:text/html,x", "http:evil",
		"https://trusted.example/x", "https://evil.com/x",
		"https://trusted.example@evil.com", "http://[::1",
	}
	for _, tc := range controlByteRedirectTargets {
		targets = append(targets, tc.target)
	}
	for _, hosts := range [][]string{nil, {"trusted.example"}} {
		for _, target := range targets {
			want := contract.SanitizeRedirect(target, hosts)
			if got := SanitizeRedirect(target, hosts); got != want {
				t.Errorf("SanitizeRedirect(%q, %v) = %q, contract says %q", target, hosts, got, want)
			}

			c, rec := NewTestContext("GET", "/")
			c.redirectAllowedHosts = hosts
			if err := c.Redirect(http.StatusFound, target); err != nil {
				t.Fatal(err)
			}
			if got := rec.Header().Get("Location"); want != target && got != "/" {
				t.Errorf("Context.Redirect(%q) with %v: Location = %q, want /", target, hosts, got)
			}

			c, _ = NewTestContext("GET", "/")
			c.redirectAllowedHosts = hosts
			err := c.RenderContext().Redirect(http.StatusFound, target)
			if refused := errors.Is(err, contract.ErrInvalidRedirect); refused != (want != target) {
				t.Errorf("RenderContext().Redirect(%q) with %v = %v, want refused=%v", target, hosts, err, want != target)
			}
		}
	}
}

// End-to-end: a Context.Redirect call with a backslash payload must end
// up writing Location: / on the wire.
func TestContextRedirect_BackslashRewritten(t *testing.T) {
	c, rec := NewTestContext("GET", "/")
	if err := c.Redirect(http.StatusFound, `/\evil.com/pwned`); err != nil {
		t.Fatal(err)
	}
	if got := rec.Header().Get("Location"); got != "/" {
		t.Errorf("Location = %q, want /", got)
	}
}
