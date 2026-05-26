package router

import (
	"net/http"
	"testing"
)

// Backslash and Unicode-similar slash characters can be folded into "/"
// by browsers or intermediaries. A target like "/\evil.com" then turns
// into "//evil.com", a protocol-relative redirect to attacker-controlled
// hosts. The router's sanitizer must reject these up front.
//
// Mirrors bond/redirect.go's backslash treatment and extends it with the
// most common Unicode slash lookalikes (U+FF0F, U+29F8, U+2044, U+2215).
func TestSanitizeRedirect_SlashLookalikesRewritten(t *testing.T) {
	cases := []struct {
		name   string
		target string
	}{
		{"ascii backslash leading", `/\evil.com/pwned`},
		{"ascii backslash double", `\\evil.com/pwned`},
		{"ascii backslash mid", `/path\evil.com`},
		{"fullwidth solidus leading", "/／evil.com/pwned"},
		{"big solidus leading", "/⧸evil.com/pwned"},
		{"fraction slash leading", "/⁄evil.com/pwned"},
		{"division slash leading", "/∕evil.com/pwned"},
		{"fullwidth solidus only", "／／evil.com/pwned"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeRedirect(tc.target, []string{"trusted.example"})
			if got != "/" {
				t.Errorf("sanitizeRedirect(%q, ...) = %q, want %q",
					tc.target, got, "/")
			}
		})
	}
}

// Plain relative paths and allow-listed absolute URLs must still flow
// through untouched after the slash-lookalike rejection lands. Guards
// against an over-broad filter that would break legitimate redirects.
func TestSanitizeRedirect_SlashLookalikesDoNotRegress(t *testing.T) {
	allowed := []string{"trusted.example"}
	cases := []struct {
		name   string
		target string
		want   string
	}{
		{"plain relative", "/dashboard", "/dashboard"},
		{"relative with query", "/dashboard?x=1", "/dashboard?x=1"},
		{"allowed absolute", "https://trusted.example/x", "https://trusted.example/x"},
		{"schemeless relative", "foo.html", "foo.html"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitizeRedirect(tc.target, allowed)
			if got != tc.want {
				t.Errorf("sanitizeRedirect(%q, %v) = %q, want %q",
					tc.target, allowed, got, tc.want)
			}
		})
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
