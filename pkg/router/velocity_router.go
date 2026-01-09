package router

import (
	"net/http"
	"sync"
)

// VelocityRouterV2 is the tree-based router implementation
// This replaces gorilla/mux with a custom radix tree
type VelocityRouterV2 struct {
	tree          *Tree
	prefix        string
	middlewares   []MiddlewareFunc
	namedRoutes   map[string]*MatchResult
	mu            sync.RWMutex
	staticDir     string
	staticFS      http.Handler
	staticEnabled bool

	// Deferred registration support
	rootGroup *GroupDefinition
	resources []*resourceWrapperV2
	committed bool
}

// NewV2 creates a new tree-based router instance
func NewV2() *VelocityRouterV2 {
	return &VelocityRouterV2{
		tree:        NewTree(),
		namedRoutes: make(map[string]*MatchResult),
		rootGroup:   NewGroupDefinition("", nil),
	}
}

// Get registers a GET route
func (r *VelocityRouterV2) Get(path string, handler HandlerFunc) RouteConfig {
	return r.addRoute("GET", path, handler)
}

// Post registers a POST route
func (r *VelocityRouterV2) Post(path string, handler HandlerFunc) RouteConfig {
	return r.addRoute("POST", path, handler)
}

// Put registers a PUT route
func (r *VelocityRouterV2) Put(path string, handler HandlerFunc) RouteConfig {
	return r.addRoute("PUT", path, handler)
}

// Delete registers a DELETE route
func (r *VelocityRouterV2) Delete(path string, handler HandlerFunc) RouteConfig {
	return r.addRoute("DELETE", path, handler)
}

// Patch registers a PATCH route
func (r *VelocityRouterV2) Patch(path string, handler HandlerFunc) RouteConfig {
	return r.addRoute("PATCH", path, handler)
}

// Options registers an OPTIONS route
func (r *VelocityRouterV2) Options(path string, handler HandlerFunc) RouteConfig {
	return r.addRoute("OPTIONS", path, handler)
}

// Head registers a HEAD route
func (r *VelocityRouterV2) Head(path string, handler HandlerFunc) RouteConfig {
	return r.addRoute("HEAD", path, handler)
}

// Any registers a route that matches any HTTP method
func (r *VelocityRouterV2) Any(path string, handler HandlerFunc) RouteConfig {
	return r.addRoute("ANY", path, handler)
}

// Match registers a route for specific HTTP methods
func (r *VelocityRouterV2) Match(methods []string, path string, handler HandlerFunc) RouteConfig {
	var lastConfig RouteConfig
	for _, method := range methods {
		lastConfig = r.addRoute(method, path, handler)
	}
	return lastConfig
}

// addRoute adds a route to the current group
func (r *VelocityRouterV2) addRoute(method, path string, handler HandlerFunc) RouteConfig {
	fullPath := r.buildPath(path)
	route := r.currentGroup().AddRoute(method, fullPath, handler)
	return &routeConfigV2{route: route, router: r}
}

// currentGroup returns the current group (root or nested)
func (r *VelocityRouterV2) currentGroup() *GroupDefinition {
	return r.rootGroup
}

// Group creates a new router group with a prefix
func (r *VelocityRouterV2) Group(prefix string, fn ...func(Router)) Router {
	// Use relative prefix - full path calculated during CommitToTree
	child := r.rootGroup.AddChild(prefix)

	groupRouter := &groupRouterV2{
		group:  child,
		router: r,
	}

	// Execute closure if provided
	if len(fn) > 0 && fn[0] != nil {
		fn[0](groupRouter)
	}

	return groupRouter
}

// Use adds middleware to the router
func (r *VelocityRouterV2) Use(middlewares ...MiddlewareFunc) Router {
	r.middlewares = append(r.middlewares, middlewares...)
	return r
}

// Prefix sets a prefix for all routes
func (r *VelocityRouterV2) Prefix(prefix string) {
	r.prefix = prefix
}

// Resource creates RESTful routes for a controller
func (r *VelocityRouterV2) Resource(path string, controller interface{}) ResourceRoute {
	rr := &resourceWrapperV2{
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
	// Store for deferred registration during commitOnce()
	r.resources = append(r.resources, rr)
	return rr
}

// Static serves static files from the specified directory
func (r *VelocityRouterV2) Static(directory string) {
	r.staticDir = directory
	r.staticFS = http.FileServer(http.Dir(directory))
	r.staticEnabled = true
}

// ServeHTTP implements http.Handler interface
func (r *VelocityRouterV2) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Commit routes on first request
	r.commitOnce()

	// Try to serve static file if enabled
	if r.staticEnabled {
		if file, err := http.Dir(r.staticDir).Open(req.URL.Path); err == nil {
			stat, statErr := file.Stat()
			file.Close()
			if statErr == nil && !stat.IsDir() {
				r.staticFS.ServeHTTP(w, req)
				return
			}
		}
	}

	// Match route
	result := r.tree.Match(req.Method, req.URL.Path)
	if result == nil {
		// Try ANY method
		result = r.tree.Match("ANY", req.URL.Path)
	}

	if result == nil {
		http.NotFound(w, req)
		return
	}

	// Set params and route name in request context
	req = SetParams(req, result.Params)
	if result.Name != "" {
		req = SetRouteName(req, result.Name)
	}

	// Create context and call handler
	ctx := NewContextV2(w, req)
	if err := result.Handler(ctx); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

// Handle returns the underlying http.Handler
func (r *VelocityRouterV2) Handle() http.Handler {
	return r
}

// commitOnce commits all routes to the tree on first request
func (r *VelocityRouterV2) commitOnce() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.committed {
		return
	}

	// Commit root group with global middleware
	r.rootGroup.CommitToTree(r.tree, r.middlewares)

	// Register resource routes with global middleware
	for _, res := range r.resources {
		res.registerWithMiddlewares(r.middlewares)
	}

	// Copy named routes from tree to router for URL generation
	r.namedRoutes = r.tree.namedRoutes

	r.committed = true
}

// buildPath constructs the full path including any prefix
func (r *VelocityRouterV2) buildPath(path string) string {
	if r.prefix != "" {
		return r.prefix + path
	}
	return path
}

// NewContextV2 creates a new Context using the new param storage
func NewContextV2(w http.ResponseWriter, r *http.Request) *Context {
	return &Context{
		Response: w,
		Request:  r,
		params:   GetParams(r),
		values:   make(map[string]interface{}),
	}
}

// routeConfigV2 implements RouteConfig for the V2 router
type routeConfigV2 struct {
	route  *RouteDefinition
	router *VelocityRouterV2
}

func (rc *routeConfigV2) Name(name string) RouteConfig {
	rc.route.Name = name
	return rc
}

func (rc *routeConfigV2) Use(middlewares ...MiddlewareFunc) RouteConfig {
	rc.route.Middlewares = append(rc.route.Middlewares, middlewares...)
	return rc
}

// groupRouterV2 implements Router for groups
type groupRouterV2 struct {
	group  *GroupDefinition
	router *VelocityRouterV2
}

func (g *groupRouterV2) Get(path string, handler HandlerFunc) RouteConfig {
	return g.addRoute("GET", path, handler)
}

func (g *groupRouterV2) Post(path string, handler HandlerFunc) RouteConfig {
	return g.addRoute("POST", path, handler)
}

func (g *groupRouterV2) Put(path string, handler HandlerFunc) RouteConfig {
	return g.addRoute("PUT", path, handler)
}

func (g *groupRouterV2) Delete(path string, handler HandlerFunc) RouteConfig {
	return g.addRoute("DELETE", path, handler)
}

func (g *groupRouterV2) Patch(path string, handler HandlerFunc) RouteConfig {
	return g.addRoute("PATCH", path, handler)
}

func (g *groupRouterV2) Options(path string, handler HandlerFunc) RouteConfig {
	return g.addRoute("OPTIONS", path, handler)
}

func (g *groupRouterV2) Head(path string, handler HandlerFunc) RouteConfig {
	return g.addRoute("HEAD", path, handler)
}

func (g *groupRouterV2) addRoute(method, path string, handler HandlerFunc) RouteConfig {
	// Store relative path - full path is calculated during CommitToTree
	route := g.group.AddRoute(method, path, handler)
	return &routeConfigV2{route: route, router: g.router}
}

func (g *groupRouterV2) Group(prefix string, fn ...func(Router)) Router {
	child := g.group.AddChild(prefix)
	childRouter := &groupRouterV2{
		group:  child,
		router: g.router,
	}

	if len(fn) > 0 && fn[0] != nil {
		fn[0](childRouter)
	}

	return childRouter
}

func (g *groupRouterV2) Use(middlewares ...MiddlewareFunc) Router {
	g.group.Use(middlewares...)
	return g
}

func (g *groupRouterV2) Prefix(prefix string) {
	// Groups already have prefix set - this is intentionally a no-op
	_ = prefix
}

func (g *groupRouterV2) Resource(path string, controller interface{}) ResourceRoute {
	rr := &resourceWrapperV2{
		router:     g.router,
		path:       g.group.FullPrefix() + path,
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
	// Add to router's resources for deferred registration
	g.router.resources = append(g.router.resources, rr)
	return rr
}

func (g *groupRouterV2) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.router.ServeHTTP(w, r)
}

func (g *groupRouterV2) Handle() http.Handler {
	return g.router
}

// resourceWrapperV2 implements ResourceRoute for V2
type resourceWrapperV2 struct {
	router     *VelocityRouterV2
	path       string
	controller interface{}
	methods    map[string]bool
	registered bool
}

func (rr *resourceWrapperV2) Only(methods ...string) ResourceRoute {
	for k := range rr.methods {
		rr.methods[k] = false
	}
	for _, method := range methods {
		rr.methods[method] = true
	}
	return rr
}

func (rr *resourceWrapperV2) Except(methods ...string) ResourceRoute {
	for _, method := range methods {
		rr.methods[method] = false
	}
	return rr
}

// register() is defined in resource.go
