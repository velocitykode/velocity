// Package router provides HTTP routing functionality for the Velocity framework.
// It uses a custom radix tree for efficient route matching and supports
// RESTful resources, middleware, and a clean API for web applications.
package router

import (
	"net/http"
)

// Router defines the interface for HTTP routing
type Router interface {
	// HTTP Methods - accepts Context-based handlers
	Get(path string, handler HandlerFunc) RouteConfig
	Post(path string, handler HandlerFunc) RouteConfig
	Put(path string, handler HandlerFunc) RouteConfig
	Delete(path string, handler HandlerFunc) RouteConfig
	Patch(path string, handler HandlerFunc) RouteConfig
	Options(path string, handler HandlerFunc) RouteConfig
	Head(path string, handler HandlerFunc) RouteConfig

	// Route Management
	// Group creates a route group with optional closure for inline route definitions.
	// Example: r.Group("/api", func(api Router) { api.Get("/users", handler) })
	Group(prefix string, fn ...func(Router)) Router
	Prefix(prefix string)
	Resource(path string, controller interface{}) ResourceRoute

	// Middleware - Context-based
	Use(middlewares ...MiddlewareFunc) Router

	// Serving
	ServeHTTP(w http.ResponseWriter, r *http.Request)
	Handle() http.Handler
}

// RouteConfig represents a single route that can be configured
type RouteConfig interface {
	Name(name string) RouteConfig
	Use(middlewares ...MiddlewareFunc) RouteConfig
}

// ResourceRoute represents a resource with configurable methods
type ResourceRoute interface {
	Only(methods ...string) ResourceRoute
	Except(methods ...string) ResourceRoute
}

// VelocityRouter is an alias for backward compatibility
type VelocityRouter = VelocityRouterV2

// New creates a new router instance
func New() *VelocityRouterV2 {
	return NewV2()
}
