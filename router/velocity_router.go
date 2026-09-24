package router

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"path"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/trace"
)

// compiledRouteMap is a type alias used with atomic.Pointer for lock-free reads.
type compiledRouteMap = map[string]*MatchResult

// VelocityRouterV2 is the tree-based router implementation
// This replaces gorilla/mux with a custom radix tree
type VelocityRouterV2 struct {
	tree               atomic.Pointer[Tree]
	prefix             string
	middlewares        []MiddlewareFunc
	namedRoutes        map[string]*MatchResult
	mu                 sync.Mutex
	staticDir          string
	staticFS           http.Handler
	staticEnabled      bool
	staticFallbackOnly bool

	// Compiled static routes for O(1) lookup (key: "METHOD /path").
	// Uses atomic.Pointer for lock-free reads on the hot path.
	compiledRoutes atomic.Pointer[compiledRouteMap]

	// Deferred registration support
	rootGroup *GroupDefinition
	resources []*resourceWrapperV2
	// committed is written under mu (set last in commitOnce, after all
	// other commit-state writes) and read lock-free in ServeHTTP, so the
	// steady-state hot path pays one atomic load instead of a mutex.
	committed atomic.Bool
	frozen    bool

	// Service container injected into every Context
	services *app.Services

	// Event dispatcher (instance-level, replaces package-level var)
	eventDispatcher func(ctx context.Context, event interface{}) error

	// Populated by SetAsyncEventDispatcher; nil for the default sync mode.
	// Called by ShutdownEventDispatcher to drain workers.
	stopEventDispatcher func(context.Context) error

	// OnEventDispatchError, if set, is invoked when the event dispatcher
	// returns a non-nil error (most notably ErrEventBufferFull under an
	// async dispatcher with a saturated buffer). If nil, the router
	// increments DroppedEventCount and logs the first error at WARN via
	// the services logger; subsequent errors are suppressed to avoid
	// log spam. Set this to integrate with a metrics system — silent
	// drops under saturation are the kind of failure mode that only
	// surfaces during an incident.
	//
	// The event parameter is typed as Event (instead of interface{}) so
	// listener implementations can switch on concrete router events
	// without a type assertion.
	OnEventDispatchError func(err error, event Event)

	droppedEvents   atomic.Uint64
	firstDropLogged atomic.Bool

	// Context pool for reuse
	ctxPool sync.Pool

	// TrustedProxies is the raw list of IPs/CIDRs whose X-Forwarded-For
	// headers should be honoured. Parsed lazily on first use via
	// parsedTrustedProxies; validate up-front with Router.ValidateConfig.
	TrustedProxies []string

	// parsedTrustedProxies caches the parsed form. Populated eagerly by
	// ValidateConfig / commitOnce, or lazily on first request. Stored via
	// atomic.Pointer so the request path (currentWiring) reads it lock-free
	// instead of contending on mu, which also guards boot config.
	parsedTrustedProxies atomic.Pointer[TrustedProxies]

	// RedirectAllowedHosts, when non-empty, extends same-origin redirect
	// validation to these hosts. Relative paths are always allowed.
	// Cross-host redirects to hosts outside this list are rewritten to "/".
	RedirectAllowedHosts []string

	// FileRoot is the absolute directory under which Context.File,
	// Context.Download, and Context.SaveFile are permitted to operate.
	// Configured via SetFileRoot during boot. An empty value means
	// "fall back to the process current working directory at the time
	// of router init", which preserves the legacy behaviour for
	// callers that have not opted in to an explicit root.
	//
	// At request time the router resolves FileRoot to fileRootHandle,
	// an *os.Root opened lazily and reused across requests. All
	// per-request file I/O flows through that handle so the kernel
	// (openat2 on Linux, equivalent on other platforms) enforces
	// containment with zero TOCTOU window.
	FileRoot string

	// fileRootHandle is the lazily-opened *os.Root the router hands to
	// every Context. Opened on first request (or first explicit call to
	// FileRootHandle), closed by CloseFileRoot during shutdown. Guarded
	// by fileRootMu so the lazy init is race-free.
	fileRootHandle *os.Root
	fileRootOpened bool
	fileRootMu     sync.Mutex

	// errorHandler is the error boundary seam installed by
	// SetErrorHandler. Nil means DefaultErrorHandler plus the router's own
	// default logging.
	errorHandler func(c *Context, err error, info ErrorInfo)

	// validateFn is wired during app init to run validation with DB support.
	validateFn func(c *Context, rules contract.ValidationRuleSet, messages ...contract.ValidationMessages) error
	// validateDataFn is wired during app init so ctx.BindValid validates an
	// extracted data map with the same DB support.
	validateDataFn func(c *Context, data map[string]interface{}, rules contract.ValidationRuleSet, messages ...contract.ValidationMessages) error

	// errorLogger is wired during app init (see SetErrorLogger) so the
	// default error path logs 500-class handler errors and recovered
	// panics instead of writing a silent generic 500. Nil means no
	// logging (standalone router usage). Suppressed entirely when an
	// error handler is installed with SetErrorHandler: that handler owns
	// the whole error pipeline, including logging.
	errorLogger func(msg string, kvs ...any)
	// warnLogger is the warn-level counterpart of errorLogger (see
	// SetWarnLogger), used for a request deadline answered 503. Nil means
	// no warn-level logging.
	warnLogger func(msg string, kvs ...any)

	// intendedFn is wired during app init to pull the "intended" post-login
	// URL from the session (auth's unauthenticated render rule stashes it). Lets
	// ctx.Intended read the session without router importing auth.
	intendedFn func(c *Context) string

	// signedURLKey holds the HKDF-derived HMAC subkey used by
	// SignedURL / ValidateSignature. Populated by SetSignedURLKey
	// during velocity.New() after APP_KEY is loaded; nil when the
	// framework was constructed without an APP_KEY (testing /
	// pre-key-generate development). The slot is mutex-guarded so a
	// future runtime rotation does not race against in-flight
	// signature verifications.
	signedURLKey signedURLKey

	// unmatchedHandler is the global middleware chain wrapped around a
	// synthetic terminal handler that returns the error for a request no
	// route matched: 404 for an unknown path, 405 with Allow for a known
	// path under a method it has no route for. Built once during
	// commitOnce so unmatched requests still pass through every Use(...)
	// middleware (rate limiters, security headers, body limits, etc.).
	// Stored via atomic.Pointer so the read on the hot path is lock-free;
	// written only under mu inside commitOnce / ClearRoutes.
	unmatchedHandler atomic.Pointer[HandlerFunc]

	// staticHandler is the global middleware chain wrapped around a
	// terminal handler that invokes the static FileServer. Built once
	// during commitOnce (alongside unmatchedHandler) so static responses
	// pass through every Use(...) middleware (security headers, rate
	// limits, body limits) instead of bypassing them (OWASP finding
	// V2-01). ServeHTTP only dispatches into this handler after
	// staticProbe has confirmed the FileServer will produce a response,
	// which preserves the invariant that the global chain runs exactly
	// once per request: here, in the matched route's handler, or in the
	// unmatched handler, never twice.
	staticHandler atomic.Pointer[HandlerFunc]
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

// AllowedRedirectHosts returns a defensive copy of RedirectAllowedHosts so
// downstream consumers (notably bond's redirect sanitizer, via the
// contract.RedirectAllowlist interface) cannot mutate the router-owned
// slice. The router treats RedirectAllowedHosts as immutable after
// startup; the copy keeps that invariant even if a caller mishandles
// the returned value.
//
// Returns nil when the operator has not configured a list. Callers MUST
// treat nil/empty as "no cross-origin host allowed" and decide their own
// fallback policy (router rewrites to "/"; bond falls back to r.Host
// with a one-time warning to preserve legacy behaviour).
func (r *VelocityRouterV2) AllowedRedirectHosts() []string {
	if len(r.RedirectAllowedHosts) == 0 {
		return nil
	}
	out := make([]string, len(r.RedirectAllowedHosts))
	copy(out, r.RedirectAllowedHosts)
	return out
}

// SetDataValidator sets the function used by ctx.BindValid() to validate an
// already-extracted data map. It carries the same callback seam as
// SetValidator so the DB-backed rules stay behind it and router imports
// neither the validation engine nor orm.
//
// Like SetValidator, this must be called before serving begins; it is read
// per request without synchronization.
func (r *VelocityRouterV2) SetDataValidator(fn func(c *Context, data map[string]interface{}, rules contract.ValidationRuleSet, messages ...contract.ValidationMessages) error) {
	r.validateDataFn = fn
}

// SetValidator sets the validation function used by ctx.Validate().
func (r *VelocityRouterV2) SetValidator(fn func(c *Context, rules contract.ValidationRuleSet, messages ...contract.ValidationMessages) error) {
	r.validateFn = fn
}

// SetErrorHandler installs the router's error boundary: fn receives every
// handler error that reaches the router (including one a middleware
// marked with contract.Handled, which it must report but not render) and
// every recovered panic, whatever its value, with the ErrorInfo the
// router knows. A bare contract.ErrResponseWritten returned outside a
// panic never reaches fn. fn owns rendering and
// reporting for the request: the router writes nothing and does not log
// once fn is installed, and fn must write nothing when info.Committed is
// true. A nil fn restores DefaultErrorHandler.
//
// Like SetValidator and SetErrorLogger, this must be called before
// serving begins; it is read per request without synchronization.
func (r *VelocityRouterV2) SetErrorHandler(fn func(c *Context, err error, info ErrorInfo)) {
	r.errorHandler = fn
}

// SetErrorLogger wires the function the default error path uses to log
// 500-class handler errors and recovered panics. The signature matches
// log.Logger.Error so the framework can pass its logger straight through.
// Wired during velocity.New() so the router need not import log.
//
// Logging policy (single owner, no double-logging):
//   - default path (no SetErrorHandler): exactly one error-level entry per
//     failed request that resolves to 500 or above, including recovered
//     panics (with stack) and errors a middleware already rendered with
//     contract.Handled. 4xx errors are deliberate responses, not
//     failures, and are not logged; neither is a client that went away
//     (context.Canceled with a dead request context). A request deadline
//     (503) goes to the warn logger instead (see SetWarnLogger).
//   - error handler installed with SetErrorHandler: default logging is
//     suppressed; the handler replaces the whole error pipeline
//     (rendering AND reporting).
//
// Like SetValidator, this must be called before serving begins; it is
// not synchronized for concurrent mutation at runtime.
func (r *VelocityRouterV2) SetErrorLogger(fn func(msg string, kvs ...any)) {
	r.errorLogger = fn
}

// SetWarnLogger wires the function the default error path uses to log a
// request whose context deadline was exceeded (answered 503) at warn
// level. The signature matches log.Logger.Warn. Nil (the default) means
// such requests are not logged. Suppressed, like SetErrorLogger, when an
// error handler is installed with SetErrorHandler.
//
// Like SetErrorLogger, this must be called before serving begins.
func (r *VelocityRouterV2) SetWarnLogger(fn func(msg string, kvs ...any)) {
	r.warnLogger = fn
}

// SetIntendedResolver wires the resolver ctx.Intended uses to pull the
// post-login "intended" URL from the session. The resolver must read the
// IntendedSessionKey value, remove it (one-shot), persist the session, and
// return the URL (or "" when none). Wired during velocity.New() so router
// need not import auth.
func (r *VelocityRouterV2) SetIntendedResolver(fn func(c *Context) string) {
	r.intendedFn = fn
}

// SetFileRoot configures the absolute directory under which Context.File,
// Context.Download, and Context.SaveFile may operate. Pass an empty
// string to fall back to the process current working directory at the
// time of the first file operation. The framework's New() wires this
// from the application config so the router can open an *os.Root and
// hand it to every Context for kernel-enforced containment.
//
// Safe to call before serving begins. If called after a previous root
// was opened, the previous handle is closed and a new one is opened on
// the next file operation.
func (r *VelocityRouterV2) SetFileRoot(path string) {
	r.fileRootMu.Lock()
	defer r.fileRootMu.Unlock()
	if r.fileRootHandle != nil {
		_ = r.fileRootHandle.Close()
		r.fileRootHandle = nil
	}
	r.fileRootOpened = false
	r.FileRoot = path
}

// FileRootHandle returns the *os.Root for the configured FileRoot,
// opening it on first use. Returns nil when the root cannot be opened
// (missing directory, permission denied); callers must handle nil and
// surface an error rather than dereferencing. The handle is owned by
// the router and released by CloseFileRoot during App shutdown, do
// NOT close the returned value.
func (r *VelocityRouterV2) FileRootHandle() *os.Root {
	r.fileRootMu.Lock()
	defer r.fileRootMu.Unlock()
	if r.fileRootOpened {
		return r.fileRootHandle
	}
	r.fileRootOpened = true
	root := r.FileRoot
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return nil
		}
		root = cwd
	}
	handle, err := os.OpenRoot(root)
	if err != nil {
		return nil
	}
	r.fileRootHandle = handle
	return r.fileRootHandle
}

// CloseFileRoot releases the *os.Root file descriptor associated with
// FileRoot. Idempotent. Called from App.Shutdown so the FD is returned
// to the kernel during graceful shutdown.
func (r *VelocityRouterV2) CloseFileRoot() error {
	r.fileRootMu.Lock()
	defer r.fileRootMu.Unlock()
	if r.fileRootHandle == nil {
		r.fileRootOpened = false
		return nil
	}
	err := r.fileRootHandle.Close()
	r.fileRootHandle = nil
	r.fileRootOpened = false
	return err
}

// SetEventDispatcher sets the event dispatcher on this router instance.
func (r *VelocityRouterV2) SetEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	r.eventDispatcher = fn
}

// dispatchInstanceEvent dispatches an event using the instance-level dispatcher.
// Errors from the dispatcher (e.g. ErrEventBufferFull under an async dispatcher
// with a saturated buffer) are routed to OnEventDispatchError if set, otherwise
// counted via DroppedEventCount and logged once at WARN. The ctx is propagated
// to the dispatcher so listeners observe the request-scoped values that ctx
// already carries (request ID, trace IDs).
func (r *VelocityRouterV2) dispatchInstanceEvent(ctx context.Context, event Event) {
	if r.eventDispatcher == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := r.eventDispatcher(ctx, event)
	if err == nil {
		return
	}
	r.reportDispatchError(err, event)
}

// reportDispatchError increments the drop counter and invokes the
// configured callback (or falls back to a once-logged WARN).
func (r *VelocityRouterV2) reportDispatchError(err error, event Event) {
	r.droppedEvents.Add(1)
	if r.OnEventDispatchError != nil {
		r.OnEventDispatchError(err, event)
		return
	}
	if r.firstDropLogged.CompareAndSwap(false, true) &&
		r.services != nil && r.services.Log != nil {
		r.services.Log.Warn(
			"velocity: event dispatch error (first occurrence; subsequent errors suppressed — poll Router.DroppedEventCount or set Router.OnEventDispatchError)",
			"error", err.Error(),
		)
	}
}

// ValidateConfig parses and validates router configuration that cannot
// be checked at compile time — specifically TrustedProxies entries.
// Call this at boot so a malformed CIDR fails startup rather than
// being silently ignored at request time.
//
// Safe to call multiple times; each call re-parses.
func (r *VelocityRouterV2) ValidateConfig() error {
	tp, err := ParseTrustedProxies(r.TrustedProxies)
	if err != nil {
		return fmt.Errorf("velocity/router: trusted proxies: %w", err)
	}
	r.parsedTrustedProxies.Store(tp)
	return nil
}

// trustedProxiesOrParse returns the parsed TrustedProxies, parsing
// lazily on first use if ValidateConfig has not been called. A parse
// error yields an empty set (no proxies trusted), operators who want
// fail-fast should call ValidateConfig at boot.
//
// The read is lock-free: it loads an atomic.Pointer rather than taking
// r.mu, so concurrent requests on the hot path do not serialize against
// each other or against boot config. The lazy populate races benignly,
// losers of the CompareAndSwap discard their parse and adopt the winner.
func (r *VelocityRouterV2) trustedProxiesOrParse() *TrustedProxies {
	if tp := r.parsedTrustedProxies.Load(); tp != nil {
		return tp
	}
	tp, err := ParseTrustedProxies(r.TrustedProxies)
	if err != nil {
		// Best-effort: never trust anything on misconfiguration.
		tp = &TrustedProxies{}
	}
	if r.parsedTrustedProxies.CompareAndSwap(nil, tp) {
		return tp
	}
	// Another goroutine populated it first; use that.
	return r.parsedTrustedProxies.Load()
}

// DroppedEventCount returns the total number of events for which the
// dispatcher returned a non-nil error since the router started. Each
// increment means an event did not reach its listener — under
// SetAsyncEventDispatcher that almost always indicates buffer saturation.
// Expose as a metric/gauge in production.
func (r *VelocityRouterV2) DroppedEventCount() uint64 {
	return r.droppedEvents.Load()
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

// addRoute adds a route to the current group.
// Panics with *contract.RegistrationError if handler is nil.
func (r *VelocityRouterV2) addRoute(method, path string, handler HandlerFunc) RouteConfig {
	if handler == nil {
		panic(contract.NewRegistrationError("router", fmt.Sprintf("nil handler for %s %s", method, path)))
	}
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

// Use adds middleware to the router.
// Panics with *contract.RegistrationError if any middleware is nil.
func (r *VelocityRouterV2) Use(middlewares ...MiddlewareFunc) Router {
	for i, mw := range middlewares {
		if mw == nil {
			panic(contract.NewRegistrationError("router", fmt.Sprintf("nil middleware at index %d", i)))
		}
	}
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
//
// Static responses run through the full global middleware chain
// (Router.Use), same as matched routes and 404s.
//
// IMPORTANT: before dispatching to the FileServer, the router probes
// the directory for the requested path. When the file is absent the
// request falls through to route matching so routes can take
// precedence over missing files; the FileServer is never invoked and
// no middleware has run yet, so the matched route's chain is the only
// one that executes. When the probe finds the file, the request is
// served inside the middleware chain; if the file disappears between
// probe and serve (a rare race), the FileServer's 404 is returned
// as-is rather than falling through.
//
// For a typical deployment (routes matched first, Static as last
// resort) this is fine. If you want to guarantee routes always win,
// call StaticFallback explicitly instead.
//
// The underlying http.Dir follows symlinks by default. Ensure the
// directory does not contain symlinks pointing outside the intended
// root, or use a custom http.FileSystem that rejects symlinks.
func (r *VelocityRouterV2) Static(directory string) {
	r.staticDir = directory
	r.staticFS = http.FileServer(http.Dir(directory))
	r.staticEnabled = true
}

// StaticFallback is an opt-in variant of Static that only serves a
// file when no route matches the request path. Use this when routes
// must always take precedence — e.g. an SPA where "/users" is both a
// client route and a possible static directory listing.
func (r *VelocityRouterV2) StaticFallback(directory string) {
	r.staticDir = directory
	r.staticFS = http.FileServer(http.Dir(directory))
	r.staticEnabled = true
	r.staticFallbackOnly = true
}

// requestMeta holds the per-request metadata that ServeHTTP threads
// through its helper functions. Lifetime is the single request.
type requestMeta struct {
	id        string
	startedAt time.Time
	traceID   string
	spanID    string
	parentID  string
}

// ServeHTTP implements http.Handler interface.
func (r *VelocityRouterV2) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	// Lock-free fast path: once committed, skip the mutex entirely.
	if !r.committed.Load() {
		r.commitOnce()
	}

	meta, req := r.beginRequest(req)
	rw := acquireResponseWriter(w)
	defer releaseResponseWriter(rw)

	r.dispatchInstanceEvent(req.Context(), &RequestStarted{
		Context:    req.Context(),
		Method:     req.Method,
		Path:       req.URL.Path,
		RemoteAddr: req.RemoteAddr, // raw RemoteAddr field; consumers needing the originating client should use clientip.Extract themselves.
		UserAgent:  req.UserAgent(),
		RequestID:  meta.id,
		StartedAt:  meta.startedAt,
		TraceID:    meta.traceID,
		SpanID:     meta.spanID,
		ParentID:   meta.parentID,
	})

	// Static-first path (Static): if the probe says the FileServer will
	// produce a response for this path, dispatch it through the global
	// middleware chain. The probe runs BEFORE the chain so a static miss
	// falls through to route matching with no middleware having run;
	// the chain executes exactly once per request, in whichever terminal
	// (static, matched route, unmatched) ends up handling it (V2-01).
	if r.staticEnabled && !r.staticFallbackOnly && r.staticProbe(req) {
		r.dispatchStatic(rw, req, meta)
		return
	}

	result := r.matchRoute(req)
	if result == nil {
		// Last-chance static (StaticFallback): only try files if no route matched.
		if r.staticEnabled && r.staticFallbackOnly && r.staticProbe(req) {
			r.dispatchStatic(rw, req, meta)
			return
		}
		r.handleUnmatched(rw, req, meta)
		return
	}

	// Bundle the per-route context values once. The params map is built
	// lazily and cached on this bundle, so event population and any later
	// Params/GetParams consumers share one map instead of rebuilding it.
	rd := &routeData{result: result, services: r.services}

	// Materialize the param map only when an event consumer exists; with
	// no dispatcher wired the map is never built (R3 laziness).
	if r.eventDispatcher != nil {
		r.dispatchInstanceEvent(req.Context(), &RequestRouted{
			Context:   req.Context(),
			RequestID: meta.id,
			Route:     result.Path,
			RouteName: result.Name,
			Params:    rd.paramsMap(),
			Matched:   true,
		})
	}

	req = r.enrichRequest(req, rd)
	ctx := r.acquireContext(rw, req, result)
	r.invokeHandler(ctx, rw, req, result, meta)
}

// beginRequest establishes the lazy request ID and trace ID holders and
// threads them onto the request context. Returns the populated metadata
// and the updated request.
func (r *VelocityRouterV2) beginRequest(req *http.Request) (requestMeta, *http.Request) {
	reqCtx, lazyTrace := trace.StartTraceLazy(req.Context())
	lazyID := &lazyRequestID{}
	meta := requestMeta{
		startedAt: time.Now(),
		parentID:  trace.GetParentID(reqCtx),
	}
	// The dispatched request events all carry the request and trace IDs,
	// so materialize them eagerly only when an event dispatcher is
	// wired. With no consumer, the IDs stay unresolved until a read
	// (GetRequestID, trace.GetTraceID/GetSpanID) forces them (if ever).
	// All paths share the same holders, so the event IDs and any later
	// context read are guaranteed identical and stable.
	if r.eventDispatcher != nil {
		meta.id = lazyID.get()
		meta.traceID, meta.spanID = lazyTrace.IDs()
	}
	// Wrap rather than WithValue so RequestIDKey resolves to the
	// materialized string (preserving the exported key's value type)
	// while keeping generation lazy.
	reqCtx = requestIDContext{Context: reqCtx, lazy: lazyID}
	return meta, req.WithContext(reqCtx)
}

// staticProbe reports whether the static FileServer would produce a
// response (anything other than a not-found) for this request path,
// mirroring http.FileServer's path normalization. Only a missing file
// returns false (fall through to route matching); permission and other
// open errors return true so the FileServer's 403/500 is produced
// inside the middleware chain, matching what the FileServer itself
// would do. The probe costs one extra Open per static hit (probe +
// serve), the price of deciding fallthrough before any middleware runs.
func (r *VelocityRouterV2) staticProbe(req *http.Request) bool {
	upath := req.URL.Path
	if !strings.HasPrefix(upath, "/") {
		upath = "/" + upath
	}
	// http.FileServer path.Cleans before opening (".." segments are
	// resolved, not rejected; the 400 rejection lives in http.ServeFile,
	// which is not used here), so the probe must Clean identically.
	f, err := http.Dir(r.staticDir).Open(path.Clean(upath))
	if err != nil {
		return !errors.Is(err, fs.ErrNotExist)
	}
	_ = f.Close()
	return true
}

// dispatchStatic runs the middleware-wrapped static handler built by
// commitOnce. Called only after staticProbe confirmed the FileServer
// will produce a response, so the global chain never runs twice for a
// request that misses static and then matches a route. Mirrors the
// handleUnmatched structure: Context acquired/released exactly once,
// RequestRouted/RequestHandled fire exactly once with Route "[static]".
func (r *VelocityRouterV2) dispatchStatic(rw *responseWriter, req *http.Request, meta requestMeta) {
	r.dispatchInstanceEvent(req.Context(), &RequestRouted{
		Context:   req.Context(),
		RequestID: meta.id,
		Route:     "[static]",
		Matched:   true,
	})

	// Attach services so middleware that pulls from ServicesFromRequest
	// sees the configured container, matching the matched-route path.
	if r.services != nil {
		req = WithServices(req, r.services)
	}

	ctx := r.ctxPool.Get().(*Context)
	ctx.Response = rw
	ctx.Request = req
	ctx.applyWiring(r.currentWiring())

	var handlerErr error
	var failure requestFailure
	defer func() {
		if recovered := recover(); recovered != nil {
			r.onPanic(ctx, rw, req, meta, recovered)
		} else if handlerErr != nil {
			r.dispatchRequestFailed(req, meta, failure)
		}
		rw.finalize(req, r.errorLogger)
		r.dispatchInstanceEvent(req.Context(), &RequestHandled{
			Context:      req.Context(),
			RequestID:    meta.id,
			Method:       req.Method,
			Path:         req.URL.Path,
			Route:        "[static]",
			StatusCode:   rw.Status(),
			BytesWritten: rw.BytesWritten(),
			Duration:     time.Since(meta.startedAt),
			TraceID:      meta.traceID,
			SpanID:       meta.spanID,
			ParentID:     meta.parentID,
		})
		ctx.reset()
		r.ctxPool.Put(ctx)
	}()

	handler := r.staticHandler.Load()
	if handler == nil {
		// commitOnce has not run (or ClearRoutes wiped state). Effectively
		// unreachable from ServeHTTP because commitOnce runs at the top of
		// every request; degrade to an unwrapped serve rather than panic.
		r.staticFS.ServeHTTP(rw, req)
		return
	}
	handlerErr = (*handler)(ctx)
	if handlerErr != nil {
		failure = r.handleError(ctx, rw, handlerErr, ErrorInfo{})
	}
}

// matchRoute tries the compiled fast-path, then the tree. Both reads
// are lock-free via atomic.Pointer.
//
// Matching runs on the escaped (wire-form) path so regex constraints
// see the encoded form and an encoded slash (%2F) cannot split a
// segment before a {param} capture sees it; captured param values are
// PathUnescaped after the match (see tree.go).
func (r *VelocityRouterV2) matchRoute(req *http.Request) *MatchResult {
	path := req.URL.EscapedPath()
	tree := r.tree.Load()
	if compiled := r.compiledRoutes.Load(); compiled != nil {
		if m := (*compiled)[req.Method+" "+path]; m != nil {
			return m
		}
		if m := (*compiled)["ANY "+path]; m != nil {
			return m
		}
	}
	if m := tree.matchLazy(req.Method, path); m != nil {
		return m
	}
	return tree.matchLazy("ANY", path)
}

// handleUnmatched runs the response for a request no route matched
// through the global middleware chain and dispatches events. The
// terminal handler of that chain returns a 404 or 405 error (see
// unmatchedError), which the error boundary renders like any other
// handler error.
//
// The middleware chain is built once during commitOnce (see
// unmatchedHandler) so global Use(...) middleware (rate limiters,
// security headers, body limits) applies to these responses just as it
// does to matched routes. Without this, an attacker could hammer
// arbitrary unknown paths to bypass per-IP throttles while still costing
// the server per-request work (security-audit-2026-05 finding E-01).
//
// RequestRouted fires with Matched=false (no route was matched);
// RequestHandled fires after the middleware chain completes with the
// final status. A Context is acquired from the pool exactly once and
// released exactly once, matching the invokeHandler pairing.
func (r *VelocityRouterV2) handleUnmatched(rw *responseWriter, req *http.Request, meta requestMeta) {
	r.dispatchInstanceEvent(req.Context(), &RequestRouted{
		Context:   req.Context(),
		RequestID: meta.id,
		Matched:   false,
	})

	// Attach services to the request so middleware that pulls from
	// ServicesFromRequest (or relies on ctx.services) sees the
	// configured container, matching the matched-route path
	// (enrichRequest does the same wiring there).
	if r.services != nil {
		req = WithServices(req, r.services)
	}

	ctx := r.ctxPool.Get().(*Context)
	ctx.Response = rw
	ctx.Request = req
	ctx.applyWiring(r.currentWiring())

	var handlerErr error
	var failure requestFailure
	defer func() {
		if recovered := recover(); recovered != nil {
			r.onPanic(ctx, rw, req, meta, recovered)
		} else if handlerErr != nil {
			r.dispatchRequestFailed(req, meta, failure)
		}
		rw.finalize(req, r.errorLogger)
		r.dispatchInstanceEvent(req.Context(), &RequestHandled{
			Context:      req.Context(),
			RequestID:    meta.id,
			Method:       req.Method,
			Path:         req.URL.Path,
			StatusCode:   rw.Status(),
			BytesWritten: rw.BytesWritten(),
			Duration:     time.Since(meta.startedAt),
			TraceID:      meta.traceID,
			SpanID:       meta.spanID,
			ParentID:     meta.parentID,
		})
		ctx.reset()
		r.ctxPool.Put(ctx)
	}()

	handler := r.unmatchedHandler.Load()
	if handler == nil {
		// commitOnce has not run (or ClearRoutes wiped state and no
		// request has rebuilt it yet). Fall back to the bare unmatched
		// error without the middleware chain so the router degrades
		// safely rather than panicking; this branch is effectively
		// unreachable from ServeHTTP because commitOnce runs at the top
		// of every request.
		handlerErr = r.unmatchedError(req)
	} else {
		handlerErr = (*handler)(ctx)
	}
	if handlerErr != nil {
		failure = r.handleError(ctx, rw, handlerErr, ErrorInfo{})
	}
}

// unmatchedError returns the error for a request this router has no
// route for. The path may still be a real one that simply has no route
// for the method: that is a 405 naming the methods that do, not a 404
// (RFC 9110 section 15.5.6).
//
// The answer is worked out here, from this router's own tree and the
// request in hand, and is never carried from where matching happened.
// Carried state can be dropped by a middleware that runs the chain on
// another Context (Timeout clones it) and can be inherited by another
// router the request is delegated to; a value computed on the spot can
// be neither.
func (r *VelocityRouterV2) unmatchedError(req *http.Request) error {
	allowed := r.tree.Load().AllowedMethods(req.URL.EscapedPath())
	// A middleware may have rewritten the request since matching. If the
	// request as it now stands names a method this path does serve, a
	// 405 would list the very method it refuses; the honest answer for a
	// request that reached the unmatched terminal is then 404.
	if slices.Contains(allowed, req.Method) || slices.Contains(allowed, "ANY") {
		allowed = nil
	}
	return unmatchedHTTPError(allowed)
}

// unmatchedHTTPError builds the error the unmatched terminal returns. With
// no allowed methods the path is unknown: a bare 404, whose message is the
// status text like every other 404. Otherwise the path is served under
// other methods only: 405, carrying the Allow header RFC 9110 section
// 15.5.6 requires on it. The error boundary renders either one.
//
// The value is built as a literal, so it records no origin: the origin
// would point inside the router, never at application code.
func unmatchedHTTPError(allowed []string) *contract.HTTPError {
	if len(allowed) == 0 {
		return &contract.HTTPError{Status: http.StatusNotFound, Message: http.StatusText(http.StatusNotFound)}
	}
	e := &contract.HTTPError{Status: http.StatusMethodNotAllowed, Message: http.StatusText(http.StatusMethodNotAllowed)}
	return e.WithHeader("Allow", strings.Join(allowed, ", "))
}

// enrichRequest attaches route params, name, pattern, and services to
// the request context. The four per-route values are bundled into a
// single routeData carried by one context.WithValue, so a matched
// request clones its context once for routing data instead of once per
// value. The corresponding getters (GetParams, GetRouteName,
// GetRoutePattern, ServicesFromRequest) read from the bundle.
func (r *VelocityRouterV2) enrichRequest(req *http.Request, rd *routeData) *http.Request {
	return req.WithContext(routeDataContext{Context: req.Context(), rd: rd})
}

// currentWiring builds the per-request wiring snapshot handed to every
// Context this router populates (matched routes, static dispatch, and
// not-found all apply the same set).
func (r *VelocityRouterV2) currentWiring() ctxWiring {
	return ctxWiring{
		services:             r.services,
		trustedProxies:       r.trustedProxiesOrParse(),
		redirectAllowedHosts: r.RedirectAllowedHosts,
		fileRoot:             r.FileRootHandle(),
		validateFn:           r.validateFn,
		validateDataFn:       r.validateDataFn,
		intendedFn:           r.intendedFn,
		insecureFlashCookies: r.services != nil && r.services.InsecureFlashCookies,
	}
}

// acquireContext pulls a Context from the pool and populates it with
// per-request wiring (services, trusted proxies, params).
func (r *VelocityRouterV2) acquireContext(rw *responseWriter, req *http.Request, result *MatchResult) *Context {
	ctx := r.ctxPool.Get().(*Context)
	ctx.Response = rw
	ctx.Request = req
	ctx.applyWiring(r.currentWiring())

	if result.segments != nil {
		ctx.params = ctx.params[:0]
		valueIdx := 0
		for _, seg := range result.segments {
			switch seg.Type {
			case SegmentParam, SegmentRegex, SegmentWildcard:
				if valueIdx < len(result.matchedValues) {
					ctx.params = append(ctx.params, RouteParam{Key: seg.Value, Value: result.matchedValues[valueIdx]})
					valueIdx++
				}
			}
		}
	}
	return ctx
}

// invokeHandler runs the matched handler with panic recovery, event
// dispatch, and pool return. Consolidates the single defer so the
// happy path stays branch-light.
func (r *VelocityRouterV2) invokeHandler(ctx *Context, rw *responseWriter, req *http.Request, result *MatchResult, meta requestMeta) {
	var handlerErr error
	var failure requestFailure
	defer func() {
		if recovered := recover(); recovered != nil {
			r.onPanic(ctx, rw, req, meta, recovered)
		} else if handlerErr != nil {
			r.dispatchRequestFailed(req, meta, failure)
		}
		rw.finalize(req, r.errorLogger)
		r.dispatchInstanceEvent(req.Context(), &RequestHandled{
			Context:      req.Context(),
			RequestID:    meta.id,
			Method:       req.Method,
			Path:         req.URL.Path,
			Route:        result.Path,
			StatusCode:   rw.Status(),
			BytesWritten: rw.BytesWritten(),
			Duration:     time.Since(meta.startedAt),
			TraceID:      meta.traceID,
			SpanID:       meta.spanID,
			ParentID:     meta.parentID,
		})
		ctx.reset()
		r.ctxPool.Put(ctx)
	}()

	handlerErr = result.Handler(ctx)
	if handlerErr != nil {
		failure = r.handleError(ctx, rw, handlerErr, ErrorInfo{})
	}
}

// onPanic converts a recovered panic into a *PanicError, dispatches
// RequestFailed and hands the error to the boundary. It is called from
// the deferred function that recovered, so the raw and structured stacks
// captured here still include the panicking frames.
func (r *VelocityRouterV2) onPanic(ctx *Context, rw *responseWriter, req *http.Request, meta requestMeta, recovered interface{}) {
	// Skip onPanic and the deferred function so the trace starts at the
	// panic site.
	pe := newPanicError(panicerr.FromRecovered(recovered), 2)
	r.dispatchRequestFailed(req, meta, requestFailure{err: pe, stack: pe.Stack, recovered: true, fire: true})
	r.handleError(ctx, rw, pe, ErrorInfo{Recovered: true, Stack: pe.Stack, StackTrace: pe.Trace})
}

// requestFailure is the boundary's RequestFailed decision for one failed
// request: whether the event fires and, when it does, the error it
// carries (the cause of a contract.Handled value), whether that error is
// a recovered panic, and the panic's stack.
type requestFailure struct {
	err       error
	stack     string
	recovered bool
	fire      bool
}

// dispatchRequestFailed dispatches RequestFailed for a failed request as
// the boundary decided (see failureOf).
func (r *VelocityRouterV2) dispatchRequestFailed(req *http.Request, meta requestMeta, failure requestFailure) {
	if !failure.fire || r.eventDispatcher == nil {
		return
	}
	r.dispatchInstanceEvent(req.Context(), &RequestFailed{
		Context:   req.Context(),
		RequestID: meta.id,
		Method:    req.Method,
		Path:      req.URL.Path,
		Error:     failure.err,
		Stack:     failure.stack,
		Recovered: failure.recovered,
		TraceID:   meta.traceID,
		SpanID:    meta.spanID,
		ParentID:  meta.parentID,
	})
}

// failureOf decides RequestFailed for a handler error err, classified
// into f. A bare contract.ErrResponseWritten is a deliberate response and
// fires nothing; a contract.Handled value fires with its cause. A marker
// the value of a recovered panic carries counts for nothing (see
// errorFacts.markedWritten). The event fires only for a recovered panic
// (a contract.RecoveredPanic in the chain, such as a *PanicError the
// Timeout middleware forwarded; only a *PanicError carries a raw stack),
// an error resolving to status 500 or above, or an error naming no
// status. 4xx outcomes are responses, not failures.
func failureOf(err error, f *errorFacts) requestFailure {
	if f.markedWritten(err, false) {
		cause := contract.HandledCause(err)
		if cause == nil {
			return requestFailure{}
		}
		cf := classifyError(cause)
		err, f = cause, &cf
	}
	failure := requestFailure{err: err, fire: true}
	if f.panicked {
		failure.recovered = true
		if f.panicErr != nil {
			failure.stack = f.panicErr.Stack
		}
		return failure
	}
	if status, _, named := f.answer(); named && status < http.StatusInternalServerError {
		return requestFailure{}
	}
	return failure
}

// handleError is the router's error boundary for one failed request. It
// classifies err once and derives everything from that one walk: whether
// the request panicked (a returned error whose chain holds a
// contract.RecoveredPanic counts as recovered, such as a *PanicError the
// Timeout middleware forwarded), the RequestFailed decision it returns
// for the caller to dispatch, and on the default path the status,
// headers, log level and body. A bare contract.ErrResponseWritten outside a recovered
// panic ends here: the response was written deliberately and there is
// nothing to report; a panic is a 500 whatever its value, so
// panic(contract.ErrResponseWritten) does not. Otherwise the boundary
// fills in the rest of the ErrorInfo (Committed comes from the router's
// own response writer) and calls the handler installed with
// SetErrorHandler, or logs through the default policy (see
// SetErrorLogger) and writes the DefaultErrorHandler response.
//
// ctx.Response is reset to the router's writer first: every middleware
// has returned by now, so a writer one of them swapped in is stale.
func (r *VelocityRouterV2) handleError(ctx *Context, rw *responseWriter, err error, info ErrorInfo) requestFailure {
	f := classifyError(err)
	if !info.Recovered && f.panicked {
		info.Recovered = true
		if pe := f.panicErr; pe != nil {
			info.Stack = pe.Stack
			info.StackTrace = pe.Trace
		}
	}
	var failure requestFailure
	if r.eventDispatcher != nil {
		failure = failureOf(err, &f)
	}
	if f.markedWritten(err, info.Recovered) && contract.HandledCause(err) == nil {
		return failure
	}
	info.Committed = rw.Committed()
	ctx.Response = rw

	if fn := r.errorHandler; fn != nil {
		// The IDs are lazy; materialize them only for a handler that
		// reports, never on the default path.
		if req := ctx.Request; req != nil {
			info.RequestID = GetRequestID(req)
			info.TraceID = trace.GetTraceID(req.Context())
			info.SpanID = trace.GetSpanID(req.Context())
		}
		fn(ctx, err, info)
		return failure
	}
	res := resolveClassified(ctx, err, &f, info)
	r.logDefault(ctx, err, &f, info, res.level)
	if res.write {
		writeDefaultError(ctx, err, res, info)
	}
	return failure
}

// logDefault emits the single default-path log entry for a failed request
// at level, the level the resolution chose; f classifies err. No-op when
// the matching logger is not wired (standalone router).
func (r *VelocityRouterV2) logDefault(ctx *Context, err error, f *errorFacts, info ErrorInfo, level defaultLogLevel) {
	var fn func(msg string, kvs ...any)
	switch level {
	case logError:
		fn = r.errorLogger
	case logWarn:
		fn = r.warnLogger
	}
	if fn == nil {
		return
	}
	if f.markedWritten(err, info.Recovered) {
		if cause := contract.HandledCause(err); cause != nil {
			err = cause
		}
	}
	kvs := []any{"error", err.Error()}
	if ctx != nil && ctx.Request != nil {
		kvs = append(kvs, "method", ctx.Request.Method, "path", ctx.Request.URL.Path)
	}
	if info.Stack != "" {
		kvs = append(kvs, "stack", info.Stack)
	}
	fn("unhandled error in HTTP handler", kvs...)
}

// Handle returns the underlying http.Handler
func (r *VelocityRouterV2) Handle() http.Handler {
	return r
}

// Freeze commits all routes to the tree, compiles the static-route fast
// path, and marks the router immutable. Called automatically on the first
// request via commitOnce(), but serving code should call Freeze() before
// ListenAndServe to move the commit cost off the first request's hot path.
// Safe to call multiple times; subsequent calls are no-ops.
func (r *VelocityRouterV2) Freeze() {
	r.commitOnce()
}

// commitOnce commits all routes to the tree on first request
func (r *VelocityRouterV2) commitOnce() {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.committed.Load() {
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

	// Build the unmatched-request handler (404 / 405) with the global
	// middleware chain wrapped around a synthetic terminal handler.
	// Without this wrap, unknown paths would bypass every
	// Router.Use(...) middleware (rate limiters, security headers, body
	// limits), letting an attacker hammer arbitrary paths at zero cost
	// and skipping the operator's global throttle
	// (security-audit-2026-05 finding E-01). The terminal writes
	// nothing: it returns the 404 / 405 error back up the chain to the
	// error boundary.
	terminal := HandlerFunc(func(c *Context) error {
		return r.unmatchedError(c.Request)
	})
	wrapped := applyMiddlewareChain(terminal, r.middlewares)
	r.unmatchedHandler.Store(&wrapped)

	// Build the static handler with the same global chain so static
	// responses cannot bypass Use(...) middleware (V2-01, mirrors the
	// E-01 treatment of the 404 handler above). The terminal reads
	// r.staticFS at request time, so it is built unconditionally and a
	// Static() call works whenever staticEnabled gates the dispatch.
	// ServeHTTP guards entry with staticProbe, so a static miss falls
	// through to route matching without this chain ever starting.
	staticTerminal := HandlerFunc(func(c *Context) error {
		r.staticFS.ServeHTTP(c.Response, c.Request)
		return nil
	})
	wrappedStatic := applyMiddlewareChain(staticTerminal, r.middlewares)
	r.staticHandler.Store(&wrappedStatic)

	// Eagerly populate the parsed trusted-proxy set so the first request
	// does not pay the parse (and the lazy CompareAndSwap is a no-op).
	// Lock-free store; safe under mu.
	r.trustedProxiesOrParse()

	r.frozen = true

	// Set committed LAST: the atomic store publishes every commit-state
	// write above, so a goroutine that observes true via the lock-free
	// load in ServeHTTP sees fully committed state.
	r.committed.Store(true)
}

// ClearCompiledRoutes clears the compiled route cache.
// Routes will be re-compiled from the tree on the next request.
func (r *VelocityRouterV2) ClearCompiledRoutes() {
	r.compiledRoutes.Store(nil)
}

// ClearRoutes fully resets the router (tree, compiled cache, groups,
// resources, and the wrapped unmatched-request handler). After calling
// this, new routes can be registered and will be committed on the next
// request.
func (r *VelocityRouterV2) ClearRoutes() {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Clear the flag before touching any reset state so concurrent
	// requests fall into commitOnce and block on mu until the reset
	// finishes, instead of serving against a partially cleared router.
	r.committed.Store(false)
	r.tree.Store(NewTree())
	r.namedRoutes = make(map[string]*MatchResult)
	r.rootGroup = NewGroupDefinition("", nil)
	r.resources = nil
	r.compiledRoutes.Store(nil)
	r.unmatchedHandler.Store(nil)
	r.staticHandler.Store(nil)
	r.frozen = false
}

// RouteInfo represents a registered route for inspection/display. Handler and
// Middleware carry best-effort function names resolved via runtime; anonymous
// functions surface with their compiler-assigned names (e.g. "pkg.Register.func1").
type RouteInfo struct {
	Method     string   `json:"method"`
	Path       string   `json:"path"`
	Handler    string   `json:"handler"`
	Middleware []string `json:"middleware"`
	Name       string   `json:"name"`
}

// AllRoutes returns all registered routes by walking the group definition tree
// and expanding resource routes. Every route's Middleware starts with the
// global chain (Router.Use), then group middleware outermost-first, then
// per-route middleware; it is always non-nil so JSON output stays a stable
// array.
func (r *VelocityRouterV2) AllRoutes() []RouteInfo {
	global := make([]string, 0, len(r.middlewares))
	for _, mw := range r.middlewares {
		global = append(global, funcName(mw))
	}
	var routes []RouteInfo
	collectGroupRoutes(r.rootGroup, global, &routes)
	for _, res := range r.resources {
		for _, info := range res.routeInfos() {
			info.Middleware = append(global[:len(global):len(global)], info.Middleware...)
			routes = append(routes, info)
		}
	}
	return routes
}

func collectGroupRoutes(g *GroupDefinition, inherited []string, routes *[]RouteInfo) {
	prefix := g.FullPrefix()
	groupMW := inherited
	for _, mw := range g.middlewares {
		groupMW = append(groupMW[:len(groupMW):len(groupMW)], funcName(mw))
	}
	for _, route := range g.routes {
		mw := groupMW
		for _, m := range route.Middlewares {
			mw = append(mw[:len(mw):len(mw)], funcName(m))
		}
		if mw == nil {
			mw = []string{}
		}
		*routes = append(*routes, RouteInfo{
			Method:     route.Method,
			Path:       prefix + route.Path,
			Handler:    funcName(route.Handler),
			Middleware: mw,
			Name:       route.Name,
		})
	}
	for _, child := range g.children {
		collectGroupRoutes(child, groupMW, routes)
	}
}

// funcName resolves the symbol name of a function value for display, or ""
// when v is not a resolvable function.
func funcName(v any) string {
	if v == nil {
		return ""
	}
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Func || rv.IsNil() {
		return ""
	}
	fn := runtime.FuncForPC(rv.Pointer())
	if fn == nil {
		return ""
	}
	return fn.Name()
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
	if handler == nil {
		panic(contract.NewRegistrationError("router", fmt.Sprintf("nil handler for %s %s", method, path)))
	}
	if g.router.frozen {
		log.Println("velocity: route registered after server start, this route will not be served")
	}
	// Store relative path - full path is calculated during CommitToTree
	route := g.group.AddRoute(method, path, handler)
	return &routeConfigV2{route: route, router: g.router}
}

func (g *groupRouterV2) Group(prefix string, fn ...func(Router)) Router {
	if g.router.frozen {
		log.Println("velocity: route registered after server start, this route will not be served")
	}
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
	if g.router.frozen {
		log.Println("velocity: middleware registered after server start, this middleware will not be applied")
	}
	g.group.Use(middlewares...)
	return g
}

func (g *groupRouterV2) Prefix(prefix string) {
	// Groups already have prefix set - this is intentionally a no-op
	_ = prefix
}

func (g *groupRouterV2) Resource(path string, controller interface{}) ResourceRoute {
	if g.router.frozen {
		log.Println("velocity: resource registered after server start, this resource will not be served")
	}
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
