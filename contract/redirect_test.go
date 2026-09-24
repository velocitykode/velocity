package contract

import (
	"net/url"
	"strings"
	"testing"
)

// controlByteRedirectTargets are redirect targets that look same-origin
// to a prefix check but reach the browser as the network-path reference
// "//evil.test/x": the WHATWG URL parser removes every TAB/LF/CR and
// trims leading/trailing C0-control-or-space, and net/http trims header
// values on write. Every one must be rejected, never stripped.
var controlByteRedirectTargets = []struct {
	name   string
	target string
}{
	{"tab between slashes", "/\t/evil.test/x"},
	{"lf between slashes", "/\n/evil.test/x"},
	{"cr between slashes", "/\r/evil.test/x"},
	{"crlf between slashes", "/\r\n/evil.test/x"},
	{"leading space", " //evil.test/x"},
	{"leading tab", "\t//evil.test/x"},
	{"leading lf", "\n//evil.test/x"},
	{"leading nul", "\x00//evil.test/x"},
	{"leading unit separator", "\x1f//evil.test/x"},
	{"trailing space", "/ok "},
	{"trailing tab", "/ok\t"},
	{"nul in path", "/ok\x00"},
	{"del in path", "/ok\x7f"},
	{"tab inside scheme", "java\tscript:alert(1)"},
	{"lf inside scheme", "ht\ntps://evil.test/x"},
	{"header injection", "/ok\r\nX-Injected: 1"},
	{"header injection absolute", "https://trusted.example/\r\nX-Injected: 1"},
}

func TestSanitizeRedirect_RejectsControlBytes(t *testing.T) {
	for _, tc := range controlByteRedirectTargets {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeRedirect(tc.target, []string{"trusted.example"}); got != "/" {
				t.Errorf("SanitizeRedirect(%q, allowlist) = %q, want /", tc.target, got)
			}
			if got := SanitizeRedirect(tc.target, nil); got != "/" {
				t.Errorf("SanitizeRedirect(%q, nil) = %q, want /", tc.target, got)
			}
		})
	}
}

// TestSanitizeRedirect_InteriorSpaceAllowed pins that only edge spaces
// are rejected: an interior space is not removed by the browser, so the
// path stays same-origin.
func TestSanitizeRedirect_InteriorSpaceAllowed(t *testing.T) {
	for _, target := range []string{"/path with spaces", "/ /evil.test/x", "/search?q=a b"} {
		if got := SanitizeRedirect(target, nil); got != target {
			t.Errorf("SanitizeRedirect(%q) = %q, want unchanged", target, got)
		}
	}
}

// Backslash and Unicode-similar slash characters can be folded into "/"
// by browsers or intermediaries. A target like "/\evil.com" then turns
// into "//evil.com", a protocol-relative redirect to attacker-controlled
// hosts. The sanitizer must reject these up front.
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
			if got := SanitizeRedirect(tc.target, []string{"trusted.example"}); got != "/" {
				t.Errorf("SanitizeRedirect(%q, ...) = %q, want /", tc.target, got)
			}
		})
	}
}

// TestSanitizeRedirect_Accepts pins the targets that flow through
// unchanged: plain relative paths, allow-listed absolute URLs and bare
// relative references. Guards against an over-broad filter that would
// break legitimate redirects.
func TestSanitizeRedirect_Accepts(t *testing.T) {
	allowed := []string{"trusted.example"}
	cases := []struct {
		name   string
		target string
	}{
		{"root", "/"},
		{"plain relative", "/dashboard"},
		{"relative with query", "/dashboard?x=1"},
		{"allowed absolute", "https://trusted.example/x"},
		{"allowed absolute http", "http://trusted.example"},
		{"schemeless relative", "foo.html"},
		{"escaped path", "/%2F..%2Fescape"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeRedirect(tc.target, allowed); got != tc.target {
				t.Errorf("SanitizeRedirect(%q, %v) = %q, want unchanged", tc.target, allowed, got)
			}
		})
	}
}

// TestSanitizeRedirect_Rejects pins the structural rejections: empty
// input, network-path references, schemes without a host, hosts outside
// the allowlist (including an empty allowlist entry and a userinfo
// disguise) and unparseable URLs.
func TestSanitizeRedirect_Rejects(t *testing.T) {
	cases := []struct {
		name    string
		target  string
		allowed []string
	}{
		{"empty", "", nil},
		{"protocol relative", "//evil.example", []string{"evil.example"}},
		{"triple slash", "///evil", nil},
		{"javascript", "javascript:alert(1)", nil},
		{"data", "data:text/html,<script>", nil},
		{"scheme without host", "http:evil", nil},
		{"foreign host", "https://evil.com/pwned", []string{"trusted.example"}},
		{"nil allowlist", "https://trusted.example/x", nil},
		{"empty allowlist entry", "https://evil.com/x", []string{""}},
		{"userinfo disguise", "https://trusted.example@evil.example", []string{"trusted.example"}},
		{"unparseable", "http://[::1", []string{"trusted.example"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := SanitizeRedirect(tc.target, tc.allowed); got != "/" {
				t.Errorf("SanitizeRedirect(%q, %v) = %q, want /", tc.target, tc.allowed, got)
			}
		})
	}
}

func TestHasUnsafeRedirectBytes(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"", false},
		{"/", false},
		{"/clean", false},
		{"/path with spaces", false},
		{"/café", false},
		{"/a\tb", true},
		{"/a\nb", true},
		{"/a\rb", true},
		{"/a\x00b", true},
		{"/a\x1fb", true},
		{"/a\x7fb", true},
		{" /a", true},
		{"/a ", true},
		{" ", true},
	}
	for _, tc := range cases {
		if got := HasUnsafeRedirectBytes(tc.in); got != tc.want {
			t.Errorf("HasUnsafeRedirectBytes(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// FuzzSanitizeRedirect feeds arbitrary strings into SanitizeRedirect with
// a fixed allowlist. The invariant we are guarding is "no open redirect":
// after sanitization, following the URL in a browser must either stay on
// the same origin, hit an allow-listed host, or be rewritten to "/".
//
// Contract:
//  1. Never panic.
//  2. The output is one of:
//     a. "/"  (safe fallback)
//     b. a same-origin reference: starts with "/" and NOT "//", OR
//     parses to Scheme="" Host="" (e.g. "foo.html", "0")
//     c. an absolute URL whose Host is in the allowlist
//  3. When the output is in category (b) or (c), it equals the input:
//     the sanitizer does not silently rewrite legitimate redirects.
//  4. A non-fallback output survives browser URL preprocessing unchanged
//     (WHATWG: remove TAB/LF/CR, trim edge C0-control-or-space), so the
//     value validated here is the value the browser resolves. Without
//     this, "/\t/evil" satisfies (b) yet navigates to "//evil".
//
// Run ad-hoc: go test -run=^$ -fuzz=FuzzSanitizeRedirect -fuzztime=30s ./contract
func FuzzSanitizeRedirect(f *testing.F) {
	allowed := []string{"trusted.example", "api.trusted.example"}

	seeds := []string{
		"",
		"/",
		"/dashboard",
		"//evil.example",
		"///evil",
		"/\\evil",
		"http://trusted.example/ok",
		"http://evil.example/bad",
		"https://trusted.example@evil.example",
		"javascript:alert(1)",
		"data:text/html,<script>",
		"\x00",
		"/\t/evil.example",
		"/\n/evil.example",
		"/\r/evil.example",
		" //evil.example",
		"\x1f//evil.example",
		"/ok ",
		"/ok\x7f",
		"/path with spaces",
		"/%2F..%2Fescape",
		"foo.html",
		"0",
		strings.Repeat("/a", 500),
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, target string) {
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("panic on %q: %v", target, r)
			}
		}()

		got := SanitizeRedirect(target, allowed)
		if got == "/" {
			return
		}
		if got != target {
			t.Errorf("sanitizer rewrote non-fallback output: input=%q output=%q", target, got)
			return
		}

		if pre := browserPreprocess(got); pre != got {
			t.Errorf("accepted output changes under browser preprocessing: output=%q browser-view=%q", got, pre)
			return
		}
		if strings.ContainsFunc(got, func(r rune) bool { return r < 0x20 || r == 0x7f }) {
			t.Errorf("accepted output contains a control byte: %q", got)
			return
		}

		// Category (b): same-origin path.
		if strings.HasPrefix(got, "/") && !strings.HasPrefix(got, "//") {
			return
		}
		u, err := url.Parse(got)
		if err != nil {
			t.Errorf("sanitizer returned unparseable URL %q for input %q: %v", got, target, err)
			return
		}
		// Category (b) continued: schemeless, hostless relative reference.
		if u.Scheme == "" && u.Host == "" && !strings.HasPrefix(got, "//") {
			return
		}
		// Category (c): absolute, host MUST be allow-listed.
		if u.Host == "" {
			t.Errorf("sanitizer permitted scheme-only URL (potential javascript:/data:): input=%q output=%q", target, got)
			return
		}
		allowedHit := false
		for _, a := range allowed {
			if u.Host == a {
				allowedHit = true
				break
			}
		}
		if !allowedHit {
			t.Errorf("sanitizer returned disallowed host %q (input=%q output=%q)", u.Host, target, got)
		}
	})
}

// browserPreprocess applies the input preprocessing of the WHATWG URL
// parser: trim leading/trailing C0-control-or-space, then remove every
// ASCII TAB, LF and CR.
func browserPreprocess(s string) string {
	s = strings.TrimFunc(s, func(r rune) bool { return r <= 0x20 })
	return strings.NewReplacer("\t", "", "\n", "", "\r", "").Replace(s)
}
