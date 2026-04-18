package router

import (
	"net/url"
	"strings"
	"testing"
)

// FuzzSanitizeRedirect feeds arbitrary strings into sanitizeRedirect with a
// fixed allowlist. The invariant we are guarding is "no open redirect":
// after sanitization, following the URL in a browser must either stay on
// the same origin, hit an allow-listed host, or be rewritten to "/".
//
// Contract:
//  1. Never panic.
//  2. The output is one of:
//     a. "/"  (safe fallback)
//     b. a same-origin reference: starts with "/" and NOT "//", OR
//        parses to Scheme="" Host="" (e.g. "foo.html", "0")
//     c. an absolute URL whose Host is in the allowlist
//  3. When the output is in category (b) or (c), it equals the input —
//     the sanitizer does not silently rewrite legitimate redirects.
//
// Run ad-hoc: go test -run=^$ -fuzz=FuzzSanitizeRedirect -fuzztime=30s ./router
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

		got := sanitizeRedirect(target, allowed)
		if got == "/" {
			return
		}
		if got != target {
			t.Errorf("sanitizer rewrote non-fallback output: input=%q output=%q", target, got)
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
