package contract

import (
	"net/url"
	"strings"
)

// SanitizeRedirect validates a redirect target against an explicit host
// allowlist. It returns target unchanged when it is safe to send as a
// Location header and "/" otherwise. The router's Context.Redirect, the
// RenderContext implementations and bond's same-origin redirect helpers
// all apply it.
//
// A target is accepted when it is non-empty, carries no byte a browser or
// net/http drops before resolving it (see HasUnsafeRedirectBytes),
// contains no backslash or Unicode slash lookalike, and is one of:
//   - a path with exactly one leading "/" ("/dashboard", "/a?b=c");
//   - an absolute URL whose host equals an entry of allowedHosts (an
//     empty entry matches nothing, and a nil or empty allowedHosts
//     rejects every absolute URL);
//   - a schemeless, hostless relative reference ("foo.html"), same-origin
//     by definition.
//
// Everything else becomes "/". The cases this must keep rejecting:
//   - "//evil", "///evil": network-path references parse with an empty
//     host, so a host check alone does not catch them.
//   - "javascript:...", "data:...": a scheme without a host is a live XSS
//     vector.
//   - "https://trusted@evil": the parsed host is "evil", so the allowlist
//     rejects it.
//   - "/\evil", "\\evil" and the lookalikes U+FF0F FULLWIDTH SOLIDUS,
//     U+29F8 BIG SOLIDUS, U+2044 FRACTION SLASH and U+2215 DIVISION
//     SLASH: browsers and intermediaries may fold them into "/", turning a
//     leading "/" into the network-path reference "//".
//   - "/\t/evil", "/\n/evil", " //evil": the WHATWG URL parser removes
//     TAB, LF and CR and trims edge C0-control-or-space, and net/http
//     trims header values, so each reaches the browser as "//evil".
//
// An accepted target is returned byte-for-byte, so SanitizeRedirect(t,
// hosts) != t reports a rejection ("/" itself is accepted unchanged). A
// caller must write exactly the returned value: any transformation after
// this check (stripping, trimming, unescaping) invalidates it.
func SanitizeRedirect(target string, allowedHosts []string) string {
	if target == "" || HasUnsafeRedirectBytes(target) || containsSlashLookalike(target) {
		return "/"
	}
	if strings.HasPrefix(target, "//") {
		return "/"
	}
	if strings.HasPrefix(target, "/") {
		return target
	}
	u, err := url.Parse(target)
	if err != nil {
		return "/"
	}
	if u.Host != "" {
		for _, allowed := range allowedHosts {
			if allowed != "" && u.Host == allowed {
				return target
			}
		}
		return "/"
	}
	if u.Scheme != "" {
		return "/"
	}
	return target
}

// HasUnsafeRedirectBytes reports whether s contains a byte that is dropped
// between validation and the browser's URL parser: any C0 control or DEL
// anywhere, or a space at either end. The WHATWG URL parser removes
// TAB/LF/CR from the whole input and trims leading/trailing
// C0-control-or-space; net/http trims spaces and tabs from header values
// on write. Any of them can collapse an accepted "/x" into the
// network-path reference "//host", so a target carrying one must be
// rejected, never stripped: stripping after validation changes the bytes
// that were validated ("/\n/evil" becomes "//evil"). An interior space is
// kept by the browser and is not unsafe. The empty string has no unsafe
// byte.
func HasUnsafeRedirectBytes(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if b := s[i]; b < 0x20 || b == 0x7f {
			return true
		}
	}
	return s[0] == ' ' || s[len(s)-1] == ' '
}

// containsSlashLookalike reports whether target contains a backslash or a
// Unicode codepoint some clients or intermediaries normalise to "/":
// U+005C REVERSE SOLIDUS, U+FF0F FULLWIDTH SOLIDUS, U+29F8 BIG SOLIDUS,
// U+2044 FRACTION SLASH or U+2215 DIVISION SLASH.
func containsSlashLookalike(target string) bool {
	for _, r := range target {
		switch r {
		case '\\', '／', '⧸', '⁄', '∕':
			return true
		}
	}
	return false
}
