package contract

import (
	"net/http"
	"strings"
)

// WantsJSON reports whether r asks for a JSON response. The first Accept
// media range decides (the text before the first comma, trimmed, with its
// parameters stripped): application/json or any type whose subtype ends in
// +json (application/problem+json, application/vnd.api+json) wants JSON. An
// XMLHttpRequest (X-Requested-With) wants JSON when that first range is
// empty or */*. An Inertia request (X-Inertia present) never wants JSON: it
// expects an Inertia page or a location reload.
func WantsJSON(r *http.Request) bool {
	if r == nil || IsInertia(r) {
		return false
	}
	media := firstMediaRange(r.Header.Get("Accept"))
	if media == "application/json" || strings.HasSuffix(media, "+json") {
		return true
	}
	if media == "" || media == "*/*" {
		return strings.EqualFold(r.Header.Get("X-Requested-With"), "XMLHttpRequest")
	}
	return false
}

// IsInertia reports whether r is an Inertia request (X-Inertia header
// present and non-empty).
func IsInertia(r *http.Request) bool {
	return r != nil && r.Header.Get("X-Inertia") != ""
}

// firstMediaRange returns the first media range of an Accept header value,
// lower-cased, trimmed and without parameters.
func firstMediaRange(accept string) string {
	if i := strings.IndexByte(accept, ','); i >= 0 {
		accept = accept[:i]
	}
	if i := strings.IndexByte(accept, ';'); i >= 0 {
		accept = accept[:i]
	}
	return strings.ToLower(strings.TrimSpace(accept))
}
