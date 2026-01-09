package http

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type HandlerFunc func(*Context) error

type MiddlewareFunc func(HandlerFunc) HandlerFunc

type ErrorHandler func(*Context, error)

type Config struct {
	ErrorHandler ErrorHandler
}

// Router is a lightweight HTTP router using radix tree
type Router struct {
	tree             *routeTree
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
	middleware      []MiddlewareFunc
	groupMiddleware []MiddlewareFunc
	segments        []segment
	router          *Router
}

type Context struct {
	Request *http.Request
	Writer  http.ResponseWriter
	Params  map[string]string
	ctx     context.Context

	TraceID   string
	RequestID string
	ErrorID   string
	locals    []localKV
}

// Simple radix tree for routing
type routeTree struct {
	root *treeNode
}

type treeNode struct {
	segment      string
	isParam      bool
	paramName    string
	isWildcard   bool
	regex        *regexp.Regexp
	children     map[string]*treeNode
	paramChild   *treeNode
	wildcardChild *treeNode
	handlers     map[string]*Route
}

type segment struct {
	value     string
	isParam   bool
	paramName string
	isWildcard bool
	regex     *regexp.Regexp
}

func newRouteTree() *routeTree {
	return &routeTree{
		root: &treeNode{
			children: make(map[string]*treeNode),
			handlers: make(map[string]*Route),
		},
	}
}

func parseSegments(pattern string) []segment {
	pattern = strings.Trim(pattern, "/")
	if pattern == "" {
		return nil
	}

	parts := strings.Split(pattern, "/")
	segments := make([]segment, 0, len(parts))

	for _, part := range parts {
		if part == "" {
			continue
		}

		seg := segment{value: part}

		if strings.HasPrefix(part, "{") && strings.HasSuffix(part, "}") {
			inner := part[1 : len(part)-1]
			colonIdx := strings.Index(inner, ":")

			if colonIdx == -1 {
				seg.isParam = true
				seg.paramName = inner
			} else {
				name := inner[:colonIdx]
				pattern := inner[colonIdx+1:]

				if pattern == ".*" {
					seg.isWildcard = true
					seg.paramName = name
				} else {
					seg.isParam = true
					seg.paramName = name
					seg.regex, _ = regexp.Compile("^" + pattern + "$")
				}
			}
		}

		segments = append(segments, seg)
	}

	return segments
}

func (t *routeTree) insert(method, pattern string, route *Route) {
	segments := parseSegments(pattern)
	route.segments = segments

	node := t.root

	for _, seg := range segments {
		if seg.isWildcard {
			if node.wildcardChild == nil {
				node.wildcardChild = &treeNode{
					segment:    seg.value,
					isWildcard: true,
					paramName:  seg.paramName,
					children:   make(map[string]*treeNode),
					handlers:   make(map[string]*Route),
				}
			}
			node = node.wildcardChild
			break
		} else if seg.isParam {
			if node.paramChild == nil {
				node.paramChild = &treeNode{
					segment:   seg.value,
					isParam:   true,
					paramName: seg.paramName,
					regex:     seg.regex,
					children:  make(map[string]*treeNode),
					handlers:  make(map[string]*Route),
				}
			}
			node = node.paramChild
		} else {
			if node.children == nil {
				node.children = make(map[string]*treeNode)
			}
			child, exists := node.children[seg.value]
			if !exists {
				child = &treeNode{
					segment:  seg.value,
					children: make(map[string]*treeNode),
					handlers: make(map[string]*Route),
				}
				node.children[seg.value] = child
			}
			node = child
		}
	}

	if node.handlers == nil {
		node.handlers = make(map[string]*Route)
	}
	node.handlers[method] = route
}

func (t *routeTree) match(method, path string) (*Route, map[string]string) {
	path = strings.Trim(path, "/")
	params := make(map[string]string)

	var parts []string
	if path != "" {
		parts = strings.Split(path, "/")
	}

	route := t.root.match(parts, method, params)
	return route, params
}

func (n *treeNode) match(parts []string, method string, params map[string]string) *Route {
	if len(parts) == 0 {
		if n.handlers != nil {
			if route, ok := n.handlers[method]; ok {
				return route
			}
		}
		return nil
	}

	part := parts[0]
	rest := parts[1:]

	// Try static match first
	if n.children != nil {
		if child, ok := n.children[part]; ok {
			if route := child.match(rest, method, params); route != nil {
				return route
			}
		}
	}

	// Try param match
	if n.paramChild != nil {
		if n.paramChild.regex == nil || n.paramChild.regex.MatchString(part) {
			params[n.paramChild.paramName] = part
			if route := n.paramChild.match(rest, method, params); route != nil {
				return route
			}
			delete(params, n.paramChild.paramName)
		}
	}

	// Try wildcard match
	if n.wildcardChild != nil {
		params[n.wildcardChild.paramName] = strings.Join(parts, "/")
		if n.wildcardChild.handlers != nil {
			if route, ok := n.wildcardChild.handlers[method]; ok {
				return route
			}
		}
		delete(params, n.wildcardChild.paramName)
	}

	return nil
}

// DefaultErrorHandler handles errors with JSON response
func DefaultErrorHandler(c *Context, err error) {
	code := 500
	message := err.Error()

	if e, ok := err.(*Error); ok {
		code = e.Code
		message = e.Message
	}

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
		tree:             newRouteTree(),
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
		groupMiddleware: make([]MiddlewareFunc, 0),
		router:          r,
	}

	r.tree.insert(method, pattern, route)
	return route
}

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	route, params := r.tree.match(req.Method, req.URL.Path)

	if route == nil {
		http.NotFound(w, req)
		return
	}

	ctx := &Context{
		Request:   req,
		Writer:    w,
		Params:    params,
		ctx:       req.Context(),
		TraceID:   req.Header.Get("X-Trace-ID"),
		RequestID: req.Header.Get("X-Request-ID"),
		locals:    make([]localKV, 0, 4),
	}

	if ctx.TraceID != "" {
		w.Header().Set("X-Trace-ID", ctx.TraceID)
	}
	if ctx.RequestID != "" {
		w.Header().Set("X-Request-ID", ctx.RequestID)
	}

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

	for i := len(route.middleware) - 1; i >= 0; i-- {
		handler = route.middleware[i](handler)
	}

	for i := len(route.groupMiddleware) - 1; i >= 0; i-- {
		handler = route.groupMiddleware[i](handler)
	}

	for i := len(r.globalMiddleware) - 1; i >= 0; i-- {
		handler = r.globalMiddleware[i](handler)
	}

	if err := handler(ctx); err != nil {
		r.errorHandler(ctx, err)
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
	if route.router != nil {
		route.router.namedRoutes[name] = route
	}
	return route
}

func (r *Router) URL(name string, params map[string]string) (string, error) {
	route, ok := r.namedRoutes[name]
	if !ok {
		return "", fmt.Errorf("route with name '%s' not found", name)
	}

	path := route.pattern
	for key, value := range params {
		path = strings.ReplaceAll(path, "{"+key+"}", value)
		path = regexp.MustCompile(`\{`+key+`:[^}]+\}`).ReplaceAllString(path, value)
	}

	// Check if any params are still unreplaced
	if strings.Contains(path, "{") && strings.Contains(path, "}") {
		return "", fmt.Errorf("missing required parameters for route '%s'", name)
	}

	return path, nil
}

func (r *Router) Prefix(prefix string) *RouteGroup {
	return &RouteGroup{
		router:     r,
		prefix:     prefix,
		middleware: make([]MiddlewareFunc, 0),
	}
}

type RouteGroup struct {
	router     *Router
	prefix     string
	middleware []MiddlewareFunc
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
		pattern:         g.prefix + pattern,
		method:          method,
		handler:         handler,
		middleware:      make([]MiddlewareFunc, 0),
		groupMiddleware: append([]MiddlewareFunc{}, g.middleware...),
		router:          g.router,
	}

	g.router.tree.insert(method, g.prefix+pattern, route)
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

func (ctx *Context) SetLocal(key string, value any) {
	for i := range ctx.locals {
		if ctx.locals[i].key == key {
			ctx.locals[i].value = value
			return
		}
	}
	ctx.locals = append(ctx.locals, localKV{key: key, value: value})
}

func (ctx *Context) GetLocal(key string) (any, bool) {
	for i := range ctx.locals {
		if ctx.locals[i].key == key {
			return ctx.locals[i].value, true
		}
	}
	return nil, false
}

func (ctx *Context) Locals(key string) any {
	val, _ := ctx.GetLocal(key)
	return val
}

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

func (ctx *Context) Get(key string) string {
	return ctx.Request.Header.Get(key)
}

func (ctx *Context) Set(key, value string) {
	ctx.Writer.Header().Set(key, value)
}
