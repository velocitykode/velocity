package router

import (
	"net/http"

	"github.com/gorilla/mux"
)

// Param extracts a route parameter from the request
func Param(r *http.Request, name string) string {
	vars := mux.Vars(r)
	return vars[name]
}

// Params returns all route parameters from the request
func Params(r *http.Request) map[string]string {
	return mux.Vars(r)
}

// Route generates a URL for a named route
func Route(name string, params map[string]string) (string, error) {
	router := Get()
	router.mu.RLock()
	route, exists := router.namedRoutes[name]
	router.mu.RUnlock()

	if !exists {
		return "", &RouteNotFoundError{Name: name}
	}

	// Convert map to pairs for URL building
	pairs := make([]string, 0, len(params)*2)
	for k, v := range params {
		pairs = append(pairs, k, v)
	}

	url, err := route.URL(pairs...)
	if err != nil {
		return "", err
	}

	return url.String(), nil
}

// CurrentRoute returns the current route name if it exists
func CurrentRoute(r *http.Request) string {
	route := mux.CurrentRoute(r)
	if route == nil {
		return ""
	}

	name := route.GetName()
	return name
}

// RouteNotFoundError is returned when a named route doesn't exist
type RouteNotFoundError struct {
	Name string
}

func (e *RouteNotFoundError) Error() string {
	return "route not found: " + e.Name
}
