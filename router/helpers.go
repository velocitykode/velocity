package router

import (
	"net/http"
)

// Param extracts a route parameter from the request context.
func Param(r *http.Request, name string) string {
	params := GetParams(r)
	if params == nil {
		return ""
	}
	return params[name]
}

// ParamFromRequest is an alias for Param.
func ParamFromRequest(r *http.Request, name string) string {
	return Param(r, name)
}

// Params returns all route parameters from the request
func Params(r *http.Request) map[string]string {
	return GetParams(r)
}

// CurrentRoute returns the current route name if it exists
func CurrentRoute(r *http.Request) string {
	return GetRouteName(r)
}

// RouteNotFoundError is returned when a named route doesn't exist
type RouteNotFoundError struct {
	Name string
}

func (e *RouteNotFoundError) Error() string {
	return "route not found: " + e.Name
}
