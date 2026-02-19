package router

import (
	"context"
	"io"
	"log"
	"net/http"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/trace"
)

// compiledRouteMap is a type alias used with atomic.Pointer for lock-free reads.
type compiledRouteMap = map[string]*MatchResult

// VelocityRouterV2 is the tree-based router implementation
// This replaces gorilla/mux with a custom radix tree
type VelocityRouterV2 struct {
	tree          atomic.Pointer[Tree]
	prefix        string
	middlewares   []MiddlewareFunc
	namedRoutes   map[string]*MatchResult
	mu            sync.Mutex
	staticDir     string
	staticFS      http.Handler
	staticEnabled bool

	// Compiled static routes for O(1) lookup (key: "METHOD /path").
	// Uses atomic.Pointer for lock-free reads on the hot path.
	compiledRoutes atomic.Pointer[compiledRouteMap]

	// Deferred registration support
	rootGroup *GroupDefinition
	resources []*resourceWrapperV2
	committed bool
	frozen    bool

	// Service container injected into every Context
	services *app.Services

	// Event dispatcher (instance-level, replaces package-level var)
	eventDispatcher func(event interface{}) error

	// Context pool for reuse
	ctxPool sync.Pool

	// Trusted proxies for X-Forwarded-For support
	TrustedProxies []string

	// ErrorHandler is called when a handler returns an error or a panic occurs.
	// If nil, the default behavior (HTTP 500) is used.
	ErrorHandler func(*Context, error)

	// validateFn is wired during app init to run validation with DB support.
	validateFn func(c *Context, rules map[string][]string, messages ...map[string]string)

}

// NewV2 creates a new tree-based router instance
func NewV2() *VelocityRouterV2 {
	r := &VelocityRouterV2{
		namedRoutes: make(map[string]*MatchResult),
		rootGroup:   NewGroupDefinition("", nil),
	}
	r.tree.Store(NewTree())
	r.ctxPool.New = func() interface{} {
		return &Context{
			params: make([]RouteParam, 0, 8),
			values: make(map[string]interface{}),
		}
	}
	return r
}

// SetServices sets the service container that will be injected into every Context.
func (r *VelocityRouterV2) SetServices(s *app.Services) {
	r.services = s
}

// SetValidator sets the validation function used by ctx.Validate().
func (r *VelocityRouterV2) SetValidator(fn func(c *Context, rules map[string][]string, messages ...map[string]string)) {
	r.validateFn = fn
}

// SetInstanceEventDispatcher sets the event dispatcher on this router instance.
func (r *VelocityRouterV2) SetInstanceEventDispatcher(fn func(event interface{}) error) {
	r.eventDispatcher = fn
}

// dispatchInstanceEvent dispatches an event using the instance-level dispatcher.
func (r *VelocityRouterV2) dispatchInstanceEvent(event interface{}) {
	if r.eventDispatcher != nil {
		r.eventDispatcher(event)
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
	if r.frozen {
		log.Println("velocity: route registered after server start, this route will not be served")
	}
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
	if r.frozen {
		log.Println("velocity: route registered after server start, this route will not be served")
	}
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
	if r.frozen {
		log.Println("velocity: middleware registered after server start, this middleware will not be applied")
	}
	r.middlewares = append(r.middlewares, middlewares...)
	return r
}

// Prefix sets a prefix for all routes
func (r *VelocityRouterV2) Prefix(prefix string) {
	r.prefix = prefix
}

// Resource creates RESTful routes for a controller
func (r *VelocityRouterV2) Resource(path string, controller interface{}) ResourceRoute {
	if r.frozen {
		log.Println("velocity: resource registered after server start, this resource will not be served")
	}
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

// Static serves static files from the specified directory.
// Note: The underlying http.Dir follows symlinks by default. If this is a concern,
// ensure the directory does not contain symlinks pointing outside the intended root,
// or use a custom http.FileSystem that rejects symlinks.
func (r *VelocityRouterV2) Static(directory string) {
	r.staticDir = directory
	r.staticFS = http.FileServer(http.Dir(directory))
	r.staticEnabled = true
}

// statusCaptureWriter wraps http.ResponseWriter to capture the status code
// without writing through to the underlying writer when the status is 404.
// This is used for static file serving to allow fallthrough to route matching.
type statusCaptureWriter struct {
	http.ResponseWriter
	status   int
	wrote    bool
	suppress bool
}

func (w *statusCaptureWriter) WriteHeader(code int) {
	if w.wrote {
		return
	}
	w.status = code
	w.wrote = true
	if code == http.StatusNotFound {
		w.suppress = true
		return
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusCaptureWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if w.suppress {
		return len(b), nil // discard
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusCaptureWriter) ReadFrom(r io.Reader) (int64, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if w.suppress {
		return io.Copy(io.Discard, r)
	}
	return io.Copy(w.ResponseWriter, r)
}

// ServeHTTP implements http.Handler interface
func (r *VelocityRouterV2) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Commit routes on first request
	r.commitOnce()

	// Generate request ID and capture start time
	requestID := generateRequestID()
	startedAt := time.Now()

	// Generate trace context
	reqCtx, traceID, spanID := trace.StartTrace(req.Context())

	// Add request ID to context
	reqCtx = context.WithValue(reqCtx, RequestIDKey, requestID)
	req = req.WithContext(reqCtx)

	// Wrap response writer to capture metrics
	rw := newResponseWriter(w)

	// Dispatch RequestStarted event
	r.dispatchInstanceEvent(&RequestStarted{
		Context:    req.Context(),
		Method:     req.Method,
		Path:       req.URL.Path,
		RemoteAddr: req.RemoteAddr,
		UserAgent:  req.UserAgent(),
		RequestID:  requestID,
		StartedAt:  startedAt,
		TraceID:    traceID,
		SpanID:     spanID,
	})

	// Try to serve static file if enabled (single-stat via capture writer)
	if r.staticEnabled {
		cw := &statusCaptureWriter{ResponseWriter: rw}
		// Dispatch routed event before serving (preserves original event ordering)
		r.dispatchInstanceEvent(&RequestRouted{
			Context:   req.Context(),
			RequestID: requestID,
			Route:     "[static]",
			Matched:   true,
		})

		r.staticFS.ServeHTTP(cw, req)
		if !cw.suppress {
			// Static file was served successfully
			r.dispatchInstanceEvent(&RequestHandled{
				Context:      req.Context(),
				RequestID:    requestID,
				Method:       req.Method,
				Path:         req.URL.Path,
				Route:        "[static]",
				StatusCode:   rw.Status(),
				BytesWritten: rw.BytesWritten(),
				Duration:     time.Since(startedAt),
				TraceID:      traceID,
				SpanID:       spanID,
			})
			return
		}
		// 404 from static — fall through to route matching
	}

	// Match route — try compiled static routes first (O(1)), then fall back to tree.
	// Both compiledRoutes and tree are read via atomic load so no lock is needed
	// on the hot path, and ClearRoutes can swap them safely mid-flight.
	path := req.URL.Path
	tree := r.tree.Load()
	var result *MatchResult
	if compiled := r.compiledRoutes.Load(); compiled != nil {
		result = (*compiled)[req.Method+" "+path]
		if result == nil {
			result = (*compiled)["ANY "+path]
		}
	}
	if result == nil {
		result = tree.Match(req.Method, path)
	}
	if result == nil {
		result = tree.Match("ANY", path)
	}

	if result == nil {
		// Dispatch routed event for 404
		r.dispatchInstanceEvent(&RequestRouted{
			Context:   req.Context(),
			RequestID: requestID,
			Matched:   false,
		})

		http.NotFound(rw, req)

		// Dispatch handled event for 404
		r.dispatchInstanceEvent(&RequestHandled{
			Context:      req.Context(),
			RequestID:    requestID,
			Method:       req.Method,
			Path:         req.URL.Path,
			StatusCode:   http.StatusNotFound,
			BytesWritten: rw.BytesWritten(),
			Duration:     time.Since(startedAt),
			TraceID:      traceID,
			SpanID:       spanID,
		})
		return
	}

	// Dispatch routed event for matched route
	r.dispatchInstanceEvent(&RequestRouted{
		Context:   req.Context(),
		RequestID: requestID,
		Route:     result.Path,
		RouteName: result.Name,
		Params:    result.Params,
		Matched:   true,
	})

	// Set params and route name in request context
	req = SetParams(req, result.Params)
	if result.Name != "" {
		req = SetRouteName(req, result.Name)
	}
	// Also store route pattern in context
	reqCtx = context.WithValue(req.Context(), RoutePatternKey, result.Path)
	req = req.WithContext(reqCtx)

	// Stash services on the request context so Wrap/NewContext inherit them
	if r.services != nil {
		req = WithServices(req, r.services)
	}

	// Acquire context from pool
	ctx := r.ctxPool.Get().(*Context)
	ctx.Response = rw
	ctx.Request = req
	ctx.services = r.services
	ctx.trustedProxies = r.TrustedProxies
	ctx.validateFn = r.validateFn

	// Build params from match result
	if result.segments != nil {
		ctx.params = ctx.params[:0]
		valueIdx := 0
		for _, seg := range result.segments {
			if seg.Type == SegmentParam || seg.Type == SegmentRegex || seg.Type == SegmentWildcard {
				if valueIdx < len(result.matchedValues) {
					ctx.params = append(ctx.params, RouteParam{Key: seg.Value, Value: result.matchedValues[valueIdx]})
					valueIdx++
				}
			}
		}
	}

	// Use defer for panic recovery, event dispatch, and context return to pool
	var handlerErr error
	defer func() {
		if recovered := recover(); recovered != nil {
			// Capture stack trace
			buf := make([]byte, 4096)
			n := runtime.Stack(buf, false)
			stack := string(buf[:n])

			// Convert recovered value to error
			var err error
			switch v := recovered.(type) {
			case error:
				err = v
			default:
				err = &panicError{value: v}
			}

			// Dispatch failed event
			r.dispatchInstanceEvent(&RequestFailed{
				Context:   req.Context(),
				RequestID: requestID,
				Method:    req.Method,
				Path:      req.URL.Path,
				Error:     err,
				Stack:     stack,
				Recovered: true,
				TraceID:   traceID,
				SpanID:    spanID,
			})

			// Write error response
			r.handleError(ctx, rw, err)
		} else if handlerErr != nil {
			// Dispatch failed event for handler error
			r.dispatchInstanceEvent(&RequestFailed{
				Context:   req.Context(),
				RequestID: requestID,
				Method:    req.Method,
				Path:      req.URL.Path,
				Error:     handlerErr,
				Recovered: false,
				TraceID:   traceID,
				SpanID:    spanID,
			})
		}

		// Always dispatch the handled event (covers panic, error, and success)
		r.dispatchInstanceEvent(&RequestHandled{
			Context:      req.Context(),
			RequestID:    requestID,
			Method:       req.Method,
			Path:         req.URL.Path,
			Route:        result.Path,
			StatusCode:   rw.Status(),
			BytesWritten: rw.BytesWritten(),
			Duration:     time.Since(startedAt),
			TraceID:      traceID,
			SpanID:       spanID,
		})

		// Return context to pool
		ctx.reset()
		r.ctxPool.Put(ctx)
	}()

	handlerErr = result.Handler(ctx)
	if handlerErr != nil {
		r.handleError(ctx, rw, handlerErr)
	}
}

// handleError writes an error response using the custom ErrorHandler if set,
// or falls back to the default HTTP 500 behavior.
func (r *VelocityRouterV2) handleError(ctx *Context, rw *responseWriter, err error) {
	if r.ErrorHandler != nil {
		r.ErrorHandler(ctx, err)
		return
	}

	// Check for HTTPError type
	if he, ok := err.(*HTTPError); ok {
		http.Error(rw, he.Message, he.Code)
		return
	}

	http.Error(rw, "Internal Server Error", http.StatusInternalServerError)
}

// AbortValidation is a sentinel panic value used by validate.Form to stop
// handler execution when validation fails (response already sent).
type AbortValidation struct{}

// panicError wraps a recovered panic value as an error
type panicError struct {
	value interface{}
}

func (e *panicError) Error() string {
	return "panic: " + toString(e.value)
}

// toString converts an interface to string
func toString(v interface{}) string {
	switch val := v.(type) {
	case string:
		return val
	case error:
		return val.Error()
	default:
		return "unknown panic"
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
	tree := r.tree.Load()
	r.rootGroup.CommitToTree(tree, r.middlewares)

	// Register resource routes with global middleware
	for _, res := range r.resources {
		res.registerWithMiddlewares(r.middlewares)
	}

	// Copy named routes from tree to router for URL generation
	r.namedRoutes = tree.namedRoutes

	// Compile static routes for O(1) lookup
	compiled := tree.CompileStaticRoutes()
	r.compiledRoutes.Store(&compiled)

	r.committed = true
	r.frozen = true
}

// ClearCompiledRoutes clears the compiled route cache.
// Routes will be re-compiled from the tree on the next request.
func (r *VelocityRouterV2) ClearCompiledRoutes() {
	r.compiledRoutes.Store(nil)
}

// ClearRoutes fully resets the router — tree, compiled cache, groups, and resources.
// After calling this, new routes can be registered and will be committed on the next request.
func (r *VelocityRouterV2) ClearRoutes() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tree.Store(NewTree())
	r.namedRoutes = make(map[string]*MatchResult)
	r.rootGroup = NewGroupDefinition("", nil)
	r.resources = nil
	r.compiledRoutes.Store(nil)
	r.committed = false
	r.frozen = false
}

// buildPath constructs the full path including any prefix
func (r *VelocityRouterV2) buildPath(path string) string {
	if r.prefix != "" {
		return r.prefix + path
	}
	return path
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

func (g *groupRouterV2) ClearCompiledRoutes() {
	g.router.ClearCompiledRoutes()
}

func (g *groupRouterV2) ClearRoutes() {
	g.router.ClearRoutes()
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
