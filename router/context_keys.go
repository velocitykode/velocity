package router

// contextKey is a type for context keys to avoid collisions
type contextKey string

const (
	// RequestIDKey is the context key for the request ID
	RequestIDKey contextKey = "velocity.request_id"

	// RouterContextKey is the context key for the router context
	RouterContextKey contextKey = "velocity.router_context"

	// RoutePatternKey is the context key for the matched route pattern
	RoutePatternKey contextKey = "velocity.route_pattern"
)
