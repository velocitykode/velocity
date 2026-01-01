// Package router provides HTTP routing functionality for the Velocity framework.
// It's built on top of gorilla/mux and provides automatic route discovery,
// RESTful resources, and a clean API for web applications.
package router

import (
	"net/http"
	"sync"

	"github.com/gorilla/mux"
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

// VelocityRouter is the main router implementation using gorilla/mux
type VelocityRouter struct {
	mux           *mux.Router
	prefix        string
	middlewares   []MiddlewareFunc
	namedRoutes   map[string]*mux.Route
	mu            sync.RWMutex
	staticDir     string
	staticFS      http.Handler
	staticEnabled bool
}

// routeWrapper wraps a mux.Route to implement our Route interface
type routeWrapper struct {
	route       *mux.Route
	router      *VelocityRouter
	handler     HandlerFunc
	middlewares []MiddlewareFunc
}

// resourceWrapper implements ResourceRoute interface
type resourceWrapper struct {
	router     *VelocityRouter
	path       string
	controller interface{}
	methods    map[string]bool
}

var (
	// globalRouter is the singleton router instance
	globalRouter *VelocityRouter
	once         sync.Once
)

// New creates a new router instance
func New() *VelocityRouter {
	return &VelocityRouter{
		mux:         mux.NewRouter(),
		namedRoutes: make(map[string]*mux.Route),
	}
}

// Get returns the global router instance, creating it if necessary
func Get() *VelocityRouter {
	once.Do(func() {
		globalRouter = New()
	})
	return globalRouter
}

// Get registers a GET route
func (r *VelocityRouter) Get(path string, handler HandlerFunc) RouteConfig {
	fullPath := r.buildPath(path)
	finalHandler := r.applyMiddleware(handler)
	route := r.mux.HandleFunc(fullPath, Wrap(finalHandler)).Methods("GET")
	return &routeWrapper{route: route, router: r, handler: handler}
}

// Post registers a POST route
func (r *VelocityRouter) Post(path string, handler HandlerFunc) RouteConfig {
	fullPath := r.buildPath(path)
	finalHandler := r.applyMiddleware(handler)
	route := r.mux.HandleFunc(fullPath, Wrap(finalHandler)).Methods("POST")
	return &routeWrapper{route: route, router: r, handler: handler}
}

// Put registers a PUT route
func (r *VelocityRouter) Put(path string, handler HandlerFunc) RouteConfig {
	fullPath := r.buildPath(path)
	finalHandler := r.applyMiddleware(handler)
	route := r.mux.HandleFunc(fullPath, Wrap(finalHandler)).Methods("PUT")
	return &routeWrapper{route: route, router: r, handler: handler}
}

// Delete registers a DELETE route
func (r *VelocityRouter) Delete(path string, handler HandlerFunc) RouteConfig {
	fullPath := r.buildPath(path)
	finalHandler := r.applyMiddleware(handler)
	route := r.mux.HandleFunc(fullPath, Wrap(finalHandler)).Methods("DELETE")
	return &routeWrapper{route: route, router: r, handler: handler}
}

// Patch registers a PATCH route
func (r *VelocityRouter) Patch(path string, handler HandlerFunc) RouteConfig {
	fullPath := r.buildPath(path)
	finalHandler := r.applyMiddleware(handler)
	route := r.mux.HandleFunc(fullPath, Wrap(finalHandler)).Methods("PATCH")
	return &routeWrapper{route: route, router: r, handler: handler}
}

// Options registers an OPTIONS route
func (r *VelocityRouter) Options(path string, handler HandlerFunc) RouteConfig {
	fullPath := r.buildPath(path)
	finalHandler := r.applyMiddleware(handler)
	route := r.mux.HandleFunc(fullPath, Wrap(finalHandler)).Methods("OPTIONS")
	return &routeWrapper{route: route, router: r, handler: handler}
}

// Head registers a HEAD route
func (r *VelocityRouter) Head(path string, handler HandlerFunc) RouteConfig {
	fullPath := r.buildPath(path)
	finalHandler := r.applyMiddleware(handler)
	route := r.mux.HandleFunc(fullPath, Wrap(finalHandler)).Methods("HEAD")
	return &routeWrapper{route: route, router: r, handler: handler}
}

// Group creates a new router group with a prefix.
// Optionally accepts a closure to define routes within the group.
func (r *VelocityRouter) Group(prefix string, fn ...func(Router)) Router {
	fullPrefix := r.buildPath(prefix)
	// Copy parent middleware to child group
	childMiddlewares := make([]MiddlewareFunc, len(r.middlewares))
	copy(childMiddlewares, r.middlewares)

	group := &VelocityRouter{
		mux:         r.mux,
		prefix:      fullPrefix,
		middlewares: childMiddlewares,
		namedRoutes: r.namedRoutes, // Share named routes
	}

	// If closure provided, execute it
	if len(fn) > 0 && fn[0] != nil {
		fn[0](group)
	}

	return group
}

// Use adds middleware to the router/group
func (r *VelocityRouter) Use(middlewares ...MiddlewareFunc) Router {
	r.middlewares = append(r.middlewares, middlewares...)
	return r
}

// applyMiddleware chains all middleware around the handler
func (r *VelocityRouter) applyMiddleware(handler HandlerFunc) HandlerFunc {
	if len(r.middlewares) == 0 {
		return handler
	}

	// Chain middleware: first middleware wraps second, second wraps third, etc.
	h := handler
	for i := len(r.middlewares) - 1; i >= 0; i-- {
		h = r.middlewares[i](h)
	}
	return h
}

// Prefix sets a prefix for all routes in this router
func (r *VelocityRouter) Prefix(prefix string) {
	r.prefix = prefix
}

// Resource creates RESTful routes for a controller
func (r *VelocityRouter) Resource(path string, controller interface{}) ResourceRoute {
	return &resourceWrapper{
		router:     r,
		path:       path,
		controller: controller,
		methods: map[string]bool{
			"index":   true,
			"create":  true,
			"store":   true,
			"show":    true,
			"edit":    true,
			"update":  true,
			"destroy": true,
		},
	}
}

// Static serves static files from the specified directory
// Files are served at the root level (e.g., /logo.png serves from public/logo.png)
func (r *VelocityRouter) Static(directory string) {
	r.staticDir = directory
	r.staticFS = http.FileServer(http.Dir(directory))
	r.staticEnabled = true
}

// ServeHTTP implements http.Handler interface
func (r *VelocityRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Try to serve static file if enabled
	if r.staticEnabled {
		if file, err := http.Dir(r.staticDir).Open(req.URL.Path); err == nil {
			stat, statErr := file.Stat()
			file.Close()
			// Only serve if it's a file, not a directory
			if statErr == nil && !stat.IsDir() {
				r.staticFS.ServeHTTP(w, req)
				return
			}
		}
	}
	// Otherwise use the router
	r.mux.ServeHTTP(w, req)
}

// Handle returns the underlying http.Handler
func (r *VelocityRouter) Handle() http.Handler {
	return r.mux
}

// buildPath constructs the full path including any prefix
func (r *VelocityRouter) buildPath(path string) string {
	if r.prefix != "" {
		return r.prefix + path
	}
	return path
}

// Name sets a name for the route
func (rw *routeWrapper) Name(name string) RouteConfig {
	rw.route.Name(name)
	rw.router.mu.Lock()
	rw.router.namedRoutes[name] = rw.route
	rw.router.mu.Unlock()
	return rw
}

// Use adds middleware to a specific route
func (rw *routeWrapper) Use(middlewares ...MiddlewareFunc) RouteConfig {
	rw.middlewares = append(rw.middlewares, middlewares...)

	// Rebuild the handler with all middleware
	finalHandler := rw.buildHandler()
	rw.route.Handler(Wrap(finalHandler))

	return rw
}

// buildHandler builds the final handler with all middleware applied
func (rw *routeWrapper) buildHandler() HandlerFunc {
	h := rw.handler

	// Apply route-specific middleware first (in reverse order)
	for i := len(rw.middlewares) - 1; i >= 0; i-- {
		h = rw.middlewares[i](h)
	}

	// Then apply group/router middleware (in reverse order)
	for i := len(rw.router.middlewares) - 1; i >= 0; i-- {
		h = rw.router.middlewares[i](h)
	}

	return h
}

// Only specifies which resource methods to create
func (rr *resourceWrapper) Only(methods ...string) ResourceRoute {
	// Reset methods map
	for k := range rr.methods {
		rr.methods[k] = false
	}
	// Enable only specified methods
	for _, method := range methods {
		rr.methods[method] = true
	}
	rr.register()
	return rr
}

// Except specifies which resource methods to exclude
func (rr *resourceWrapper) Except(methods ...string) ResourceRoute {
	// Disable specified methods
	for _, method := range methods {
		rr.methods[method] = false
	}
	rr.register()
	return rr
}

// register creates the actual routes for the resource
func (rr *resourceWrapper) register() {
	// Implementation will use reflection to call controller methods
	// This is a simplified version - full implementation would handle
	// the controller methods properly
}
