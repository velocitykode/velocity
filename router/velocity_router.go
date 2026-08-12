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
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/trace"
)

// ErrValidationAborted is returned when validation fails and the response
// (typically a redirect with flashed errors) has already been written.
// The router skips error handling for this sentinel.
var ErrValidationAborted = errors.New("velocity/router: validation aborted, response already written")

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
	committed bool
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

	// ErrorHandler is called when a handler returns an error or a panic occurs.
	// If nil, the default behavior (HTTP 500) is used.
	ErrorHandler func(*Context, error)

	// validateFn is wired during app init to run validation with DB support.
	validateFn func(c *Context, rules contract.ValidationRuleSet, messages ...contract.ValidationMessages) error

	// errorLogger is wired during app init (see SetErrorLogger) so the
	// default error path logs 500-class handler errors and recovered
	// panics instead of writing a silent generic 500. Nil means no
	// logging (standalone router usage). Suppressed entirely when a
	// custom ErrorHandler is installed: the ErrorHandler owns the whole
	// error pipeline, including logging.
	errorLogger func(msg string, kvs ...any)

	// intendedFn is wired during app init to pull the "intended" post-login
	// URL from the session (auth's denyUnauthenticated stashes it). Lets
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

	// notFoundHandler is the global middleware chain wrapped around a
	// synthetic terminal handler that writes the 404 response. Built once
	// during commitOnce so unmatched requests still pass through every
	// Use(...) middleware (rate limiters, security headers, body limits,
	// etc.). Stored via atomic.Pointer so the read on the hot path is
	// lock-free; written only under mu inside commitOnce / ClearRoutes.
	notFoundHandler atomic.Pointer[HandlerFunc]

	// staticHandler is the global middleware chain wrapped around a
	// terminal handler that invokes the static FileServer. Built once
	// during commitOnce (alongside notFoundHandler) so static responses
	// pass through every Use(...) middleware (security headers, rate
	// limits, body limits) instead of bypassing them (OWASP finding
	// V2-01). ServeHTTP only dispatches into this handler after
	// staticProbe has confirmed the FileServer will produce a response,
	// which preserves the invariant that the global chain runs exactly
	// once per request: here, in the matched route's handler, or in the
	// 404 handler, never twice.
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

// SetValidator sets the validation function used by ctx.Validate().
func (r *VelocityRouterV2) SetValidator(fn func(c *Context, rules contract.ValidationRuleSet, messages ...contract.ValidationMessages) error) {
	r.validateFn = fn
}

// SetErrorLogger wires the function the default error path uses to log
// 500-class handler errors and recovered panics. The signature matches
// log.Logger.Error so the framework can pass its logger straight through.
// Wired during velocity.New() so the router need not import log.
//
// Logging policy (single owner, no double-logging):
//   - default path (ErrorHandler == nil): exactly one error-level entry
//     per failed request: non-HTTPError errors, *HTTPError with a 5xx
//     code, and recovered panics (with stack). 4xx HTTPErrors are
//     deliberate responses, not failures, and are not logged.
//   - custom ErrorHandler installed: default logging is suppressed; the
//     ErrorHandler replaces the whole error pipeline (rendering AND
//     reporting). Consumers typically route to the exceptions handler,
//     whose reporters then own logging.
//
// Like SetValidator, this must be called before serving begins; it is
// not synchronized for concurrent mutation at runtime.
func (r *VelocityRouterV2) SetErrorLogger(fn func(msg string, kvs ...any)) {
	r.errorLogger = fn
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
	r.commitOnce()

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
	// (static, matched route, 404) ends up handling it (V2-01).
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
		r.handleNotFound(rw, req, meta)
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

// beginRequest generates the request ID, trace IDs, and threads them
// onto the request context. Returns the populated metadata and the
// updated request.
func (r *VelocityRouterV2) beginRequest(req *http.Request) (requestMeta, *http.Request) {
	reqCtx, traceID, spanID := trace.StartTrace(req.Context())
	lazyID := &lazyRequestID{}
	meta := requestMeta{
		startedAt: time.Now(),
		traceID:   traceID,
		spanID:    spanID,
		parentID:  trace.GetParentID(reqCtx),
	}
	// The dispatched request events all carry the ID, so materialize it
	// eagerly only when an event dispatcher is wired. With no consumer,
	// the ID stays unresolved until GetRequestID reads it (if ever).
	// Both paths share the same holder, so the event ID and any later
	// GetRequestID read are guaranteed identical and stable.
	if r.eventDispatcher != nil {
		meta.id = lazyID.get()
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
// handleNotFound structure: Context acquired/released exactly once,
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
	defer func() {
		if recovered := recover(); recovered != nil {
			r.onPanic(ctx, rw, req, meta, recovered)
		} else if handlerErr != nil && !errors.Is(handlerErr, ErrValidationAborted) {
			r.dispatchInstanceEvent(req.Context(), &RequestFailed{
				Context:   req.Context(),
				RequestID: meta.id,
				Method:    req.Method,
				Path:      req.URL.Path,
				Error:     handlerErr,
				Recovered: false,
				TraceID:   meta.traceID,
				SpanID:    meta.spanID,
				ParentID:  meta.parentID,
			})
		}
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
	if handlerErr != nil && !errors.Is(handlerErr, ErrValidationAborted) {
		r.handleError(ctx, rw, handlerErr, "")
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

// handleNotFound runs the unmatched-path response through the global
// middleware chain and dispatches events. The middleware chain is built
// once during commitOnce (see notFoundHandler) so global Use(...)
// middleware (rate limiters, security headers, body limits) applies to
// 404 responses just as it does to matched routes. Without this, an
// attacker could hammer arbitrary unknown paths to bypass per-IP
// throttles while still costing the server per-request work
// (security-audit-2026-05 finding E-01).
//
// RequestRouted fires with Matched=false (no route was matched);
// RequestHandled fires after the middleware chain completes with the
// final status. A Context is acquired from the pool exactly once and
// released exactly once, matching the invokeHandler pairing.
func (r *VelocityRouterV2) handleNotFound(rw *responseWriter, req *http.Request, meta requestMeta) {
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
	defer func() {
		if recovered := recover(); recovered != nil {
			r.onPanic(ctx, rw, req, meta, recovered)
		} else if handlerErr != nil && !errors.Is(handlerErr, ErrValidationAborted) {
			r.dispatchInstanceEvent(req.Context(), &RequestFailed{
				Context:   req.Context(),
				RequestID: meta.id,
				Method:    req.Method,
				Path:      req.URL.Path,
				Error:     handlerErr,
				Recovered: false,
				TraceID:   meta.traceID,
				SpanID:    meta.spanID,
				ParentID:  meta.parentID,
			})
		}
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

	handler := r.notFoundHandler.Load()
	if handler == nil {
		// commitOnce has not run (or ClearRoutes wiped state and no
		// request has rebuilt it yet). Fall back to a bare 404 so the
		// router degrades safely rather than panicking; this branch is
		// effectively unreachable from ServeHTTP because commitOnce
		// runs at the top of every request.
		http.NotFound(rw, req)
		return
	}
	handlerErr = (*handler)(ctx)
	if handlerErr != nil && !errors.Is(handlerErr, ErrValidationAborted) {
		r.handleError(ctx, rw, handlerErr, "")
	}
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
	defer func() {
		if recovered := recover(); recovered != nil {
			r.onPanic(ctx, rw, req, meta, recovered)
		} else if handlerErr != nil && !errors.Is(handlerErr, ErrValidationAborted) {
			r.dispatchInstanceEvent(req.Context(), &RequestFailed{
				Context:   req.Context(),
				RequestID: meta.id,
				Method:    req.Method,
				Path:      req.URL.Path,
				Error:     handlerErr,
				Recovered: false,
				TraceID:   meta.traceID,
				SpanID:    meta.spanID,
				ParentID:  meta.parentID,
			})
		}
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
	if handlerErr != nil && !errors.Is(handlerErr, ErrValidationAborted) {
		r.handleError(ctx, rw, handlerErr, "")
	}
}

// onPanic converts a panic into a RequestFailed event, captures the
// stack trace, and writes the error response.
func (r *VelocityRouterV2) onPanic(ctx *Context, rw *responseWriter, req *http.Request, meta requestMeta, recovered interface{}) {
	buf := make([]byte, 4096)
	n := runtime.Stack(buf, false)
	err := panicerr.FromRecovered(recovered)

	r.dispatchInstanceEvent(req.Context(), &RequestFailed{
		Context:   req.Context(),
		RequestID: meta.id,
		Method:    req.Method,
		Path:      req.URL.Path,
		Error:     err,
		Stack:     string(buf[:n]),
		Recovered: true,
		TraceID:   meta.traceID,
		SpanID:    meta.spanID,
		ParentID:  meta.parentID,
	})
	r.handleError(ctx, rw, err, string(buf[:n]))
}

// handleError writes an error response using the custom ErrorHandler if set,
// or falls back to the default HTTP 500 behavior. On the default path,
// 500-class failures are logged via the wired errorLogger (see
// SetErrorLogger for the full logging-ownership policy); stack is
// non-empty only on the panic-recovery path.
func (r *VelocityRouterV2) handleError(ctx *Context, rw *responseWriter, err error, stack string) {
	if r.ErrorHandler != nil {
		r.ErrorHandler(ctx, err)
		return
	}

	// Check for HTTPError type
	if he, ok := err.(*HTTPError); ok {
		if he.Code >= http.StatusInternalServerError {
			// 5xx messages are server-side detail: log them, but never
			// echo handler-supplied text to the client (generic-error
			// house rule). 4xx messages are client-facing by design
			// (validation hints, etc.) and pass through unchanged.
			r.logServerError(ctx, err, stack)
			http.Error(rw, http.StatusText(he.Code), he.Code)
			return
		}
		http.Error(rw, he.Message, he.Code)
		return
	}

	r.logServerError(ctx, err, stack)
	http.Error(rw, "Internal Server Error", http.StatusInternalServerError)
}

// logServerError emits the single default-path error log entry for a
// failed request. No-op when no errorLogger is wired (standalone router).
func (r *VelocityRouterV2) logServerError(ctx *Context, err error, stack string) {
	fn := r.errorLogger
	if fn == nil {
		return
	}
	kvs := []any{"error", err.Error()}
	if ctx != nil && ctx.Request != nil {
		kvs = append(kvs, "method", ctx.Request.Method, "path", ctx.Request.URL.Path)
	}
	if stack != "" {
		kvs = append(kvs, "stack", stack)
	}
	fn("unhandled error in HTTP handler", kvs...)
}

// (panicError and toString removed — use internal/panicerr.FromRecovered)

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

	// Build the 404 handler with the global middleware chain wrapped
	// around a synthetic terminal handler. Without this wrap, unknown
	// paths would bypass every Router.Use(...) middleware (rate
	// limiters, security headers, body limits), letting an attacker
	// hammer arbitrary paths at zero cost and skipping the operator's
	// global throttle (security-audit-2026-05 finding E-01).
	terminal := HandlerFunc(func(c *Context) error {
		http.NotFound(c.Response, c.Request)
		return nil
	})
	wrapped := applyMiddlewareChain(terminal, r.middlewares)
	r.notFoundHandler.Store(&wrapped)

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

	r.committed = true
	r.frozen = true
}

// ClearCompiledRoutes clears the compiled route cache.
// Routes will be re-compiled from the tree on the next request.
func (r *VelocityRouterV2) ClearCompiledRoutes() {
	r.compiledRoutes.Store(nil)
}

// ClearRoutes fully resets the router (tree, compiled cache, groups, resources,
// and the wrapped 404 handler). After calling this, new routes can be
// registered and will be committed on the next request.
func (r *VelocityRouterV2) ClearRoutes() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.tree.Store(NewTree())
	r.namedRoutes = make(map[string]*MatchResult)
	r.rootGroup = NewGroupDefinition("", nil)
	r.resources = nil
	r.compiledRoutes.Store(nil)
	r.notFoundHandler.Store(nil)
	r.staticHandler.Store(nil)
	r.committed = false
	r.frozen = false
}

// RouteInfo represents a registered route for inspection/display.
type RouteInfo struct {
	Method string
	Path   string
	Name   string
}

// AllRoutes returns all registered routes by walking the group definition tree
// and expanding resource routes.
func (r *VelocityRouterV2) AllRoutes() []RouteInfo {
	var routes []RouteInfo
	collectGroupRoutes(r.rootGroup, &routes)
	for _, res := range r.resources {
		routes = append(routes, res.routeInfos()...)
	}
	return routes
}

func collectGroupRoutes(g *GroupDefinition, routes *[]RouteInfo) {
	prefix := g.FullPrefix()
	for _, route := range g.routes {
		*routes = append(*routes, RouteInfo{
			Method: route.Method,
			Path:   prefix + route.Path,
			Name:   route.Name,
		})
	}
	for _, child := range g.children {
		collectGroupRoutes(child, routes)
	}
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
