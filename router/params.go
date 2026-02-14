package router

import (
	"context"
	"net/http"
)

// paramsKey is the context key for route parameters
type paramsKey struct{}

// routeNameKey is the context key for current route name
type routeNameKey struct{}

// SetParams stores route parameters in the request context
// Returns a new request with the parameters stored
func SetParams(r *http.Request, params map[string]string) *http.Request {
	ctx := context.WithValue(r.Context(), paramsKey{}, params)
	return r.WithContext(ctx)
}

// GetParams retrieves route parameters from the request context
func GetParams(r *http.Request) map[string]string {
	if params, ok := r.Context().Value(paramsKey{}).(map[string]string); ok {
		return params
	}
	return nil
}

// SetRouteName stores the current route name in the request context
func SetRouteName(r *http.Request, name string) *http.Request {
	ctx := context.WithValue(r.Context(), routeNameKey{}, name)
	return r.WithContext(ctx)
}

// GetRouteName retrieves the current route name from the request context
func GetRouteName(r *http.Request) string {
	if name, ok := r.Context().Value(routeNameKey{}).(string); ok {
		return name
	}
	return ""
}
