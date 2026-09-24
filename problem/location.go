package problem

import (
	"net/http"
	"net/url"
	"strings"
)

// isLocalPath reports whether target is a same-origin path safe to hand a
// client as a location: exactly one leading "/", no control byte, no
// backslash and no Unicode slash lookalike, any of which a browser could
// normalise into a network-path reference ("//host").
func isLocalPath(target string) bool {
	if target == "" || target[0] != '/' || strings.HasPrefix(target, "//") {
		return false
	}
	for i := 0; i < len(target); i++ {
		if b := target[i]; b < 0x20 || b == 0x7f || b == ' ' {
			return false
		}
	}
	for _, r := range target {
		switch r {
		case '\\', '／', '⧸', '⁄', '∕':
			return false
		}
	}
	return true
}

// sameOriginReferer returns the path and query of r's Referer when it points
// at r's own host (or is already a local path), or "".
func sameOriginReferer(r *http.Request) string {
	ref := r.Referer()
	if ref == "" {
		return ""
	}
	u, err := url.Parse(ref)
	if err != nil {
		return ""
	}
	if u.Scheme != "" || u.Host != "" {
		if (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || !strings.EqualFold(u.Host, r.Host) {
			return ""
		}
	}
	loc := u.EscapedPath()
	if loc == "" {
		loc = "/"
	}
	if u.RawQuery != "" {
		loc += "?" + u.RawQuery
	}
	if !isLocalPath(loc) {
		return ""
	}
	return loc
}
