package http

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gorilla/mux"
)

type HandlerFunc func(*Context) error

type MiddlewareFunc func(HandlerFunc) HandlerFunc

type ErrorHandler func(*Context, error)

type Config struct {
	ErrorHandler ErrorHandler
}

type Router struct {
	mux              *mux.Router
	globalMiddleware []MiddlewareFunc
	namedRoutes      map[string]*Route
	errorHandler     ErrorHandler
}

type localKV struct {
	key   string
	value any
}

// Error represents an HTTP error with status code
type Error struct {
	Code    int
	Message string
}

func (e *Error) Error() string {
	return e.Message
}

// NewError creates a new HTTP error
func NewError(code int, message ...string) *Error {
	msg := http.StatusText(code)
	if len(message) > 0 && message[0] != "" {
		msg = message[0]
	}
	return &Error{Code: code, Message: msg}
}

// Predefined errors
var (
	ErrBadRequest          = NewError(400)
	ErrUnauthorized        = NewError(401)
	ErrForbidden           = NewError(403)
	ErrNotFound            = NewError(404)
	ErrMethodNotAllowed    = NewError(405)
	ErrInternalServerError = NewError(500)
	ErrBadGateway          = NewError(502)
	ErrServiceUnavailable  = NewError(503)
)

type Route struct {
	name            string
	pattern         string
	method          string
	handler         HandlerFunc
	middleware      []MiddlewareFunc // Route-specific middleware
	groupMiddleware []MiddlewareFunc // Group middleware
	muxRoute        *mux.Route
}

type Context struct {
	Request *http.Request
	Writer  http.ResponseWriter
	Params  map[string]string
	ctx     context.Context

	// Trace IDs (extracted from headers, not auto-generated)
	TraceID   string
	RequestID string

	// Error tracking (generated on error)
	ErrorID string

	// Request-scoped storage (slice-based like fasthttp)
	locals []localKV
}

// DefaultErrorHandler handles errors with JSON response
func DefaultErrorHandler(c *Context, err error) {
	code := 500
	message := err.Error()

	// Check for custom Error type
	if e, ok := err.(*Error); ok {
		code = e.Code
		message = e.Message
	}

	// Generate error ID if not set
	if c.ErrorID == "" {
		c.ErrorID = fmt.Sprintf("err-%d-%s", time.Now().Unix(), generateShortID())
	}

	c.JSON(code, map[string]string{
		"error":      message,
		"error_id":   c.ErrorID,
		"trace_id":   c.TraceID,
		"request_id": c.RequestID,
	})
}

func generateShortID() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 8)
	rand.Read(b)
	for i := range b {
		b[i] = charset[b[i]%byte(len(charset))]
	}
	return string(b)
}

func NewRouter(config ...Config) *Router {
	cfg := Config{ErrorHandler: DefaultErrorHandler}
	if len(config) > 0 && config[0].ErrorHandler != nil {
		cfg.ErrorHandler = config[0].ErrorHandler
	}

	return &Router{
		mux:              mux.NewRouter(),
		globalMiddleware: make([]MiddlewareFunc, 0),
		namedRoutes:      make(map[string]*Route),
		errorHandler:     cfg.ErrorHandler,
	}
}

func (r *Router) GET(pattern string, handler HandlerFunc) *Route {
	return r.addRoute("GET", pattern, handler)
}

func (r *Router) POST(pattern string, handler HandlerFunc) *Route {
	return r.addRoute("POST", pattern, handler)
}

func (r *Router) PUT(pattern string, handler HandlerFunc) *Route {
	return r.addRoute("PUT", pattern, handler)
}

func (r *Router) DELETE(pattern string, handler HandlerFunc) *Route {
	return r.addRoute("DELETE", pattern, handler)
}

func (r *Router) PATCH(pattern string, handler HandlerFunc) *Route {
	return r.addRoute("PATCH", pattern, handler)
}

func (r *Router) OPTIONS(pattern string, handler HandlerFunc) *Route {
	return r.addRoute("OPTIONS", pattern, handler)
}

func (r *Router) HEAD(pattern string, handler HandlerFunc) *Route {
	return r.addRoute("HEAD", pattern, handler)
}

func (r *Router) addRoute(method, pattern string, handler HandlerFunc) *Route {
	route := &Route{
		pattern:         pattern,
		method:          method,
		handler:         handler,
		middleware:      make([]MiddlewareFunc, 0),
		groupMiddleware: make([]MiddlewareFunc, 0), // No group middleware for root routes
	}

	muxRoute := r.mux.HandleFunc(pattern, r.wrapHandler(route)).Methods(method)
	route.muxRoute = muxRoute

	return route
}

func (r *Router) wrapHandler(route *Route) http.HandlerFunc {
	return func(w http.ResponseWriter, req *http.Request) {
		ctx := &Context{
			Request:   req,
			Writer:    w,
			Params:    make(map[string]string),
			ctx:       req.Context(),
			TraceID:   req.Header.Get("X-Trace-ID"),
			RequestID: req.Header.Get("X-Request-ID"),
			locals:    make([]localKV, 0, 4), // Pre-allocate capacity
		}

		// Echo trace IDs back in response headers
		if ctx.TraceID != "" {
			w.Header().Set("X-Trace-ID", ctx.TraceID)
		}
		if ctx.RequestID != "" {
			w.Header().Set("X-Request-ID", ctx.RequestID)
		}

		vars := mux.Vars(req)
		for k, v := range vars {
			ctx.Params[k] = v
		}

		// Panic recovery
		defer func() {
			if rvr := recover(); rvr != nil {
				err, ok := rvr.(error)
				if !ok {
					err = fmt.Errorf("%v", rvr)
				}
				r.errorHandler(ctx, err)
			}
		}()

		handler := route.handler

		// Apply middleware in order: Route -> Group -> Global
		// They execute in reverse order: Global -> Group -> Route

		// Route-specific middleware (innermost)
		for i := len(route.middleware) - 1; i >= 0; i-- {
			handler = route.middleware[i](handler)
		}

		// Group middleware (middle)
		for i := len(route.groupMiddleware) - 1; i >= 0; i-- {
			handler = route.groupMiddleware[i](handler)
		}

		// Global middleware (outermost)
		for i := len(r.globalMiddleware) - 1; i >= 0; i-- {
			handler = r.globalMiddleware[i](handler)
		}

		if err := handler(ctx); err != nil {
			r.errorHandler(ctx, err)
		}
	}
}

func (r *Router) Middleware(middleware ...MiddlewareFunc) {
	r.globalMiddleware = append(r.globalMiddleware, middleware...)
}

func (route *Route) Middleware(middleware ...MiddlewareFunc) *Route {
	route.middleware = append(route.middleware, middleware...)
	return route
}

func (route *Route) Name(name string) *Route {
	route.name = name
	if route.muxRoute != nil {
		route.muxRoute.Name(name)
	}
	return route
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.mux.ServeHTTP(w, req)
}

func (r *Router) URL(name string, params map[string]string) (string, error) {
	route := r.mux.Get(name)
	if route == nil {
		return "", fmt.Errorf("route with name '%s' not found", name)
	}

	var pairs []string
	for k, v := range params {
		pairs = append(pairs, k, v)
	}

	url, err := route.URL(pairs...)
	if err != nil {
		return "", err
	}

	return url.String(), nil
}

func (r *Router) Prefix(prefix string) *RouteGroup {
	return &RouteGroup{
		router:     r,
		prefix:     prefix,
		middleware: make([]MiddlewareFunc, 0),
		subrouter:  r.mux.PathPrefix(prefix).Subrouter(),
	}
}

type RouteGroup struct {
	router     *Router
	prefix     string
	middleware []MiddlewareFunc
	subrouter  *mux.Router
}

func (g *RouteGroup) GET(pattern string, handler HandlerFunc) *Route {
	return g.addRoute("GET", pattern, handler)
}

func (g *RouteGroup) POST(pattern string, handler HandlerFunc) *Route {
	return g.addRoute("POST", pattern, handler)
}

func (g *RouteGroup) PUT(pattern string, handler HandlerFunc) *Route {
	return g.addRoute("PUT", pattern, handler)
}

func (g *RouteGroup) DELETE(pattern string, handler HandlerFunc) *Route {
	return g.addRoute("DELETE", pattern, handler)
}

func (g *RouteGroup) PATCH(pattern string, handler HandlerFunc) *Route {
	return g.addRoute("PATCH", pattern, handler)
}

func (g *RouteGroup) addRoute(method, pattern string, handler HandlerFunc) *Route {
	route := &Route{
		pattern:         pattern,
		method:          method,
		handler:         handler,
		middleware:      make([]MiddlewareFunc, 0),                   // Route-specific starts empty
		groupMiddleware: append([]MiddlewareFunc{}, g.middleware...), // Copy group middleware
	}

	muxRoute := g.subrouter.HandleFunc(pattern, g.router.wrapHandler(route)).Methods(method)
	route.muxRoute = muxRoute

	return route
}

func (g *RouteGroup) Middleware(middleware ...MiddlewareFunc) *RouteGroup {
	g.middleware = append(g.middleware, middleware...)
	return g
}

func (g *RouteGroup) Group(prefix string) *RouteGroup {
	return &RouteGroup{
		router:     g.router,
		prefix:     g.prefix + prefix,
		middleware: append([]MiddlewareFunc{}, g.middleware...),
		subrouter:  g.subrouter.PathPrefix(prefix).Subrouter(),
	}
}

func (ctx *Context) JSON(code int, data interface{}) error {
	ctx.Writer.Header().Set("Content-Type", "application/json")
	ctx.Writer.WriteHeader(code)
	return json.NewEncoder(ctx.Writer).Encode(data)
}

func (ctx *Context) String(code int, format string, values ...interface{}) error {
	ctx.Writer.Header().Set("Content-Type", "text/plain")
	ctx.Writer.WriteHeader(code)
	_, err := fmt.Fprintf(ctx.Writer, format, values...)
	return err
}

func (ctx *Context) HTML(code int, html string) error {
	ctx.Writer.Header().Set("Content-Type", "text/html")
	ctx.Writer.WriteHeader(code)
	_, err := ctx.Writer.Write([]byte(html))
	return err
}

func (ctx *Context) Redirect(code int, location string) error {
	http.Redirect(ctx.Writer, ctx.Request, location, code)
	return nil
}

func (ctx *Context) Param(key string) string {
	return ctx.Params[key]
}

func (ctx *Context) Query(key string) string {
	return ctx.Request.URL.Query().Get(key)
}

func (ctx *Context) PostForm(key string) string {
	return ctx.Request.FormValue(key)
}

func (ctx *Context) Context() context.Context {
	return ctx.ctx
}

func (ctx *Context) SetContext(c context.Context) {
	ctx.ctx = c
}

func (ctx *Context) Status(code int) *Context {
	ctx.Writer.WriteHeader(code)
	return ctx
}

// SetLocal stores a value in request-scoped storage
func (ctx *Context) SetLocal(key string, value any) {
	// Check if key exists, update if found
	for i := range ctx.locals {
		if ctx.locals[i].key == key {
			ctx.locals[i].value = value
			return
		}
	}
	// Append new key-value pair
	ctx.locals = append(ctx.locals, localKV{key: key, value: value})
}

// GetLocal retrieves a value from request-scoped storage
func (ctx *Context) GetLocal(key string) (any, bool) {
	for i := range ctx.locals {
		if ctx.locals[i].key == key {
			return ctx.locals[i].value, true
		}
	}
	return nil, false
}

// Locals retrieves a value (returns nil if not found)
func (ctx *Context) Locals(key string) any {
	val, _ := ctx.GetLocal(key)
	return val
}

// Error response helpers
func (ctx *Context) BadRequest(message string) error {
	return NewError(400, message)
}

func (ctx *Context) Unauthorized(message string) error {
	return NewError(401, message)
}

func (ctx *Context) Forbidden(message string) error {
	return NewError(403, message)
}

func (ctx *Context) NotFound(message string) error {
	return NewError(404, message)
}

func (ctx *Context) InternalServerError(message string) error {
	return NewError(500, message)
}

// Type-safe parameter extraction
func (ctx *Context) ParamInt(key string) (int, error) {
	val := ctx.Param(key)
	if val == "" {
		return 0, fmt.Errorf("param '%s' not found", key)
	}
	return strconv.Atoi(val)
}

func (ctx *Context) QueryInt(key string, defaultValue ...int) int {
	val := ctx.Query(key)
	if val == "" {
		if len(defaultValue) > 0 {
			return defaultValue[0]
		}
		return 0
	}
	i, err := strconv.Atoi(val)
	if err != nil && len(defaultValue) > 0 {
		return defaultValue[0]
	}
	return i
}

func (ctx *Context) QueryBool(key string) bool {
	val := ctx.Query(key)
	b, _ := strconv.ParseBool(val)
	return b
}

// Request binding methods
func (ctx *Context) BindJSON(v interface{}) error {
	if ctx.Request.Body == nil {
		return fmt.Errorf("request body is empty")
	}
	return json.NewDecoder(ctx.Request.Body).Decode(v)
}

func (ctx *Context) Body() ([]byte, error) {
	if ctx.Request.Body == nil {
		return nil, fmt.Errorf("request body is empty")
	}
	return io.ReadAll(ctx.Request.Body)
}

// Header helper methods
func (ctx *Context) Get(key string) string {
	return ctx.Request.Header.Get(key)
}

func (ctx *Context) Set(key, value string) {
	ctx.Writer.Header().Set(key, value)
}
