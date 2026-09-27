package router

import (
	"net/http"

	"github.com/velocitykode/velocity/trace"
)

// GetRequestID returns the request id of r. The router gives every request
// one, generated on first read by trace.GenerateRequestID: 20 lowercase hex
// characters encoding the Unix time in seconds (8), a per-process counter
// (4) and 32 random bits (8), for example 66f6a0b2002a9c41e07b. The id lives
// in the request context under trace's request id key, so code holding only
// the context reads the same value with trace.GetRequestID, and httpclient
// sends it on outbound calls as X-Request-ID.
func GetRequestID(r *http.Request) string {
	return trace.GetRequestID(r.Context())
}

// GetRoutePattern extracts the matched route pattern from the request
// context. Matched routes carry it in the bundled routeData; the
// RoutePatternKey form is retained for compatibility with any caller
// that sets it directly.
func GetRoutePattern(r *http.Request) string {
	// routeDataContext answers RoutePatternKey from the bundled match; a
	// RoutePatternKey override layered above wins first (last-writer-wins).
	if pattern, ok := r.Context().Value(RoutePatternKey).(string); ok {
		return pattern
	}
	return ""
}
