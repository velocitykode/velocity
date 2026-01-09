package router

// RouteURL generates a URL for a named route with the given parameters
func (r *VelocityRouterV2) RouteURL(name string, params map[string]string) (string, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	if !r.committed {
		return "", &RouteNotFoundError{Name: name}
	}

	result, exists := r.namedRoutes[name]
	if !exists {
		return "", &RouteNotFoundError{Name: name}
	}

	// Use BuildPath from segment.go to generate URL
	return BuildPath(result.segments, params)
}
