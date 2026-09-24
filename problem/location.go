package problem

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/velocitykode/velocity/contract"
)

// ReloadLocator is an optional facet of the error page renderer: it names
// the target an Inertia client reloads (X-Inertia-Location) when a failed
// request gets no error page. The view engine implements it with its
// redirect host allowlist. Without it, or when it returns "", the handler
// uses inertiaLocation.
type ReloadLocator interface {
	ReloadLocation(r *http.Request) string
}

// reloadLocation returns the X-Inertia-Location target for r: the page
// renderer's ReloadLocator answer when it has one, else inertiaLocation.
func reloadLocation(page contract.ErrorPageRenderer, r *http.Request) string {
	if locator, ok := page.(ReloadLocator); ok && r != nil {
		if target := locator.ReloadLocation(r); target != "" {
			return target
		}
	}
	return inertiaLocation(r)
}

// inertiaLocation returns the same-origin path an Inertia client reloads:
// the current URL for GET and HEAD, the Referer's path and query when it is
// same-origin, and "/" otherwise.
func inertiaLocation(r *http.Request) string {
	if r == nil {
		return "/"
	}
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		if r.URL != nil && isLocalPath(r.URL.RequestURI()) {
			return r.URL.RequestURI()
		}
		return "/"
	}
	if loc := sameOriginReferer(r); loc != "" {
		return loc
	}
	return "/"
}

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
