package router

// RouteURL generates a URL for a named route with the given parameters.
// This is safe to call concurrently after routes are committed (frozen).
func (r *VelocityRouterV2) RouteURL(name string, params map[string]string) (string, error) {
	if !r.frozen {
		return "", &RouteNotFoundError{Name: name}
	}

	result, exists := r.namedRoutes[name]
	if !exists {
		return "", &RouteNotFoundError{Name: name}
	}

	// Use BuildPath from segment.go to generate URL
	return BuildPath(result.segments, params)
}
