package problem

import (
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"reflect"
	"sync"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/clientip"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// Verify *Handler implements contract.ErrorHandler at compile time.
var _ contract.ErrorHandler = (*Handler)(nil)

// Logger is the logging interface for the exception handler.
type Logger interface {
	Warn(msg string, kvs ...any)
}

type stdLogger struct{}

func (stdLogger) Warn(msg string, _ ...any) { log.Println("[WARN]", msg) }

// Handler is the main exception handler.
type Handler struct {
	mu          sync.RWMutex
	reporters   []Reporter
	renderers   map[string]Renderer
	dontReport  map[string]bool
	debug       bool
	environment string
	apiMode     bool     // When true, always respond with JSON
	apiPrefixes []string // URL prefixes that indicate API routes
	logger      Logger

	// Custom handlers for specific exception types
	customHandlers map[reflect.Type]func(RenderContext, error, *ErrorContext)

	// Rules and hooks registered through contract.ErrorHandler.
	mapRules          []contract.MapRule
	renderRules       []contract.RenderRule
	reportRules       []contract.ReportRule
	ignoreRules       []contract.IgnoreRule
	levelRules        []contract.LevelRule
	throttleRules     []contract.ThrottleRule
	ignorePredicates  []func(error, *ErrorContext) bool
	contextProviders  []func(error, *ErrorContext) map[string]any
	jsonWhen          func(*http.Request, error) bool
	beforeRender      []func(RenderContext, error, int) int
	errorPageRenderer contract.ErrorPageRenderer

	// trustedProxies is the parsed list of proxy networks whose
	// forwarded headers (Forwarded, X-Forwarded-For, X-Real-IP) may
	// be honoured when capturing the client IP for the
	// ErrorContext. Nil means "no proxies trusted" (the secure
	// default): forwarded headers are ignored and the RemoteAddr IP
	// is recorded. Set via SetTrustedProxies during boot.
	//
	// Logging the real client IP and refusing to honour spoofed
	// headers prevents log poisoning / forensics evasion from any
	// direct-internet deployment (audit C-05 finding 4).
	trustedProxies []*net.IPNet
}

// Option is a functional option for configuring the Handler.
type Option func(*Handler)

// NewHandler creates a new exception handler.
func NewHandler(opts ...Option) *Handler {
	h := &Handler{
		reporters:      []Reporter{NewLogReporter()},
		renderers:      make(map[string]Renderer),
		dontReport:     make(map[string]bool),
		customHandlers: make(map[reflect.Type]func(RenderContext, error, *ErrorContext)),
		debug:          false,
		environment:    "production",
		logger:         stdLogger{},
	}

	// Set default renderers
	h.renderers["json"] = NewJSONRenderer()
	h.renderers["html"] = NewHTMLRenderer()

	for _, opt := range opts {
		opt(h)
	}

	// Force-disable debug mode in production to prevent exposing stack traces and source code
	if contract.IsProductionEnv(h.environment) && h.debug {
		h.debug = false
		h.logger.Warn("APP_DEBUG=true is ignored in production — debug mode has been force-disabled to prevent exposing stack traces and source code in error responses")
	} else if h.debug {
		h.logger.Warn("Exception handler running in debug mode — stack traces and source code will be exposed in error responses. Ensure APP_DEBUG is not enabled in production.")
	}

	return h
}

// WithHandlerLogger sets the logger for the exception handler.
func WithHandlerLogger(l Logger) Option {
	return func(h *Handler) {
		h.logger = l
	}
}

// WithDebug enables or disables debug mode.
func WithDebug(debug bool) Option {
	return func(h *Handler) {
		h.debug = debug
	}
}

// WithEnvironment sets the environment name.
func WithEnvironment(env string) Option {
	return func(h *Handler) {
		h.environment = env
	}
}

// WithReporters sets the reporters.
func WithReporters(reporters ...Reporter) Option {
	return func(h *Handler) {
		h.reporters = reporters
	}
}

// WithRenderers sets the renderers.
func WithRenderers(renderers map[string]Renderer) Option {
	return func(h *Handler) {
		for k, v := range renderers {
			h.renderers[k] = v
		}
	}
}

// WithDontReport sets exception types that should not be reported. Each name
// is matched against the error's runtime type as produced by fmt.Sprintf("%T",
// err) with leading '*'/'&' and the framework's own "exceptions." package
// qualifier stripped (see getExceptionType). So a builtin is named bare
// ("ValidationException"), a custom type keeps its package qualifier
// ("mypkg.MyError"), and errors.New values are "errors.errorString".
func WithDontReport(types ...string) Option {
	return func(h *Handler) {
		for _, t := range types {
			h.dontReport[t] = true
		}
	}
}

// WithAPIMode enables API mode where all responses are JSON.
func WithAPIMode(enabled bool) Option {
	return func(h *Handler) {
		h.apiMode = enabled
	}
}

// WithTrustedProxies installs the parsed proxy-network list used to
// resolve the client IP recorded on the ErrorContext. Pass nil to
// disable XFF/Forwarded resolution (secure default; the RemoteAddr IP
// is logged). Safe for use at construction time; SetTrustedProxies is
// the runtime equivalent.
func WithTrustedProxies(proxies []*net.IPNet) Option {
	return func(h *Handler) {
		// Deep-clone so caller mutation of any *net.IPNet's IP / Mask
		// (or the slice header) cannot flip the handler's trust
		// decisions at runtime. A shallow []*net.IPNet copy reuses
		// the same IPNet pointers and re-exposes the audit-finding
		// hole.
		h.trustedProxies = clientip.CloneIPNets(proxies)
	}
}

// WithAPIPrefixes sets URL prefixes that indicate API routes.
// Requests to these paths will always receive JSON responses.
func WithAPIPrefixes(prefixes ...string) Option {
	return func(h *Handler) {
		h.apiPrefixes = prefixes
	}
}

// SetDebug sets the debug mode.
func (h *Handler) SetDebug(debug bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if debug && contract.IsProductionEnv(h.environment) {
		log.Println("[WARN] Refusing to enable debug mode in production environment — stack traces and source code will not be exposed")
		return
	}
	h.debug = debug
	if debug {
		log.Println("[WARN] Exception handler debug mode enabled — stack traces and source code will be exposed in error responses. Ensure this is not enabled in production.")
	}
}

// IsDebug returns whether debug mode is enabled.
func (h *Handler) IsDebug() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.debug
}

// SetEnvironment sets the environment.
func (h *Handler) SetEnvironment(env string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.environment = env
}

// GetEnvironment returns the current environment.
func (h *Handler) GetEnvironment() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.environment
}

// AddReporter adds a reporter to the handler.
func (h *Handler) AddReporter(reporter Reporter) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reporters = append(h.reporters, reporter)
}

// SetReporters replaces all reporters.
func (h *Handler) SetReporters(reporters ...Reporter) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reporters = reporters
}

// AddRenderer adds a renderer for a content type.
func (h *Handler) AddRenderer(contentType string, renderer Renderer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.renderers[contentType] = renderer
}

// DontReport adds an exception type to the don't report list.
func (h *Handler) DontReport(exceptionType string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.dontReport[exceptionType] = true
}

// SetAPIMode enables or disables API mode.
func (h *Handler) SetAPIMode(enabled bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.apiMode = enabled
}

// IsAPIMode returns whether API mode is enabled.
func (h *Handler) IsAPIMode() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.apiMode
}

// SetTrustedProxies installs the parsed proxy-network list used by
// ErrorHandler (and any other client-IP-sensitive surface on this
// handler) when capturing the IP onto the ErrorContext. Pass nil
// to disable XFF/Forwarded resolution (the secure default; the
// RemoteAddr IP is logged verbatim).
//
// Safe to call concurrently with request handling: the handler
// snapshots the slice under its mutex so a concurrent
// getTrustedProxies sees either the old or the new list, never a
// torn pointer.
func (h *Handler) SetTrustedProxies(proxies []*net.IPNet) {
	// Deep-clone OUTSIDE the lock so the (potentially non-trivial)
	// allocation does not extend the critical section. Caller
	// mutation of any IPNet field after this point cannot flip the
	// handler's trust decisions.
	cloned := clientip.CloneIPNets(proxies)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.trustedProxies = cloned
}

// getTrustedProxies returns a deep clone of the installed proxy list
// under a read lock. The returned slice is fully owned by the caller:
// mutating any element (or its IP / Mask backing array) has no effect
// on the handler. Returns nil when no list is installed.
func (h *Handler) getTrustedProxies() []*net.IPNet {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return clientip.CloneIPNets(h.trustedProxies)
}

// SetAPIPrefixes sets URL prefixes that indicate API routes.
func (h *Handler) SetAPIPrefixes(prefixes ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.apiPrefixes = prefixes
}

// GetAPIPrefixes returns the configured API prefixes.
func (h *Handler) GetAPIPrefixes() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.apiPrefixes
}

// isAPIRequest determines if a request should receive a JSON response.
func (h *Handler) isAPIRequest(ctx RenderContext) bool {
	// Global API mode
	if h.apiMode {
		return true
	}

	// Check if path matches API prefixes
	path := requestPath(ctx)
	for _, prefix := range h.apiPrefixes {
		if len(path) >= len(prefix) && path[:len(prefix)] == prefix {
			return true
		}
	}

	// Fall back to content negotiation
	return ctx.WantsJSON()
}

// ShouldReport determines if an exception should be reported.
func (h *Handler) ShouldReport(err error) bool {
	// Check if the error type is in the don't report list
	h.mu.RLock()
	typeName := getExceptionType(err)
	if h.dontReport[typeName] {
		h.mu.RUnlock()
		return false
	}
	h.mu.RUnlock()

	// Check if the exception, or any error it wraps, implements Reportable
	var reportable Reportable
	if errors.As(err, &reportable) {
		return reportable.ShouldReport()
	}

	return true
}

// Report reports an exception to all configured reporters.
// Exceptions in the dontReport list are silently skipped.
func (h *Handler) Report(err error, ctx *ErrorContext) {
	if !h.ShouldReport(err) {
		return
	}
	h.reportToAll(err, ctx)
}

// reportToAll sends an error to every configured reporter unconditionally.
func (h *Handler) reportToAll(err error, ctx *ErrorContext) {
	h.mu.RLock()
	reporters := make([]Reporter, len(h.reporters))
	copy(reporters, h.reporters)
	h.mu.RUnlock()

	for _, reporter := range reporters {
		reporter.Report(err, ctx)
	}
}

// Render renders an exception response.
func (h *Handler) Render(ctx RenderContext, err error, exCtx *ErrorContext) {
	// Check if the error, or any error it wraps, implements Renderable
	var renderable Renderable
	if errors.As(err, &renderable) {
		if renderErr := renderable.Render(ctx); renderErr == nil {
			return
		}
		// Fall through to default rendering if custom render fails
	}

	// Check for custom handler
	h.mu.RLock()
	errType := reflect.TypeOf(err)
	customHandler, hasCustom := h.customHandlers[errType]
	h.mu.RUnlock()

	if hasCustom {
		customHandler(ctx, err, exCtx)
		return
	}

	// Determine renderer based on API mode or content negotiation
	h.mu.RLock()
	renderers := make(map[string]Renderer, len(h.renderers))
	for k, v := range h.renderers {
		renderers[k] = v
	}
	debug := h.debug
	isAPI := h.isAPIRequest(ctx)
	h.mu.RUnlock()

	var renderer Renderer
	if isAPI {
		// API requests always get JSON
		if r, ok := renderers["json"]; ok {
			renderer = r
		} else {
			renderer = NewJSONRenderer()
		}
	} else {
		// Use content negotiation for non-API requests
		renderer = NegotiateRenderer(ctx, renderers)
	}

	if renderErr := renderer.Render(ctx, err, exCtx, debug); renderErr != nil {
		// Last resort: plain text error
		ctx.SetHeader("Content-Type", "text/plain")
		ctx.WriteHeader(http.StatusInternalServerError)
		ctx.Write([]byte("Internal Server Error"))
	}
}

// HandleRequest reports and renders err. A nil exCtx is replaced by a new
// one carrying the request path and method; a missing stack trace is
// captured here.
func (h *Handler) HandleRequest(ctx RenderContext, err error, exCtx *ErrorContext) {
	if exCtx == nil {
		exCtx = NewErrorContext()
		exCtx.URL = requestPath(ctx)
		exCtx.Method = requestMethod(ctx)
	}
	if exCtx.StackTrace == nil {
		exCtx.WithStackTrace(contract.CaptureStackTrace(1))
	}

	h.Report(err, exCtx)
	h.Render(ctx, err, exCtx)
}

// HandleConsole reports err for a console command and returns exit code 1,
// or 0 for a nil err.
func (h *Handler) HandleConsole(_ io.Writer, err error) int {
	if err == nil {
		return 0
	}
	h.Report(err, NewErrorContext())
	return 1
}

// AddMapRule stores a map rule.
func (h *Handler) AddMapRule(rule contract.MapRule) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.mapRules = append(h.mapRules, rule)
}

// AddRenderRule stores a render rule.
func (h *Handler) AddRenderRule(rule contract.RenderRule) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.renderRules = append(h.renderRules, rule)
}

// AddReportRule stores a report rule.
func (h *Handler) AddReportRule(rule contract.ReportRule) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reportRules = append(h.reportRules, rule)
}

// AddIgnoreRule stores an ignore rule.
func (h *Handler) AddIgnoreRule(rule contract.IgnoreRule) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ignoreRules = append(h.ignoreRules, rule)
}

// AddLevelRule stores a level rule.
func (h *Handler) AddLevelRule(rule contract.LevelRule) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.levelRules = append(h.levelRules, rule)
}

// AddThrottleRule stores a throttle rule.
func (h *Handler) AddThrottleRule(rule contract.ThrottleRule) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.throttleRules = append(h.throttleRules, rule)
}

// IgnoreIf stores an ignore predicate.
func (h *Handler) IgnoreIf(pred func(err error, ctx *ErrorContext) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ignorePredicates = append(h.ignorePredicates, pred)
}

// ContextUsing stores a report context provider.
func (h *Handler) ContextUsing(fn func(err error, ctx *ErrorContext) map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.contextProviders = append(h.contextProviders, fn)
}

// JSONWhen stores the JSON negotiation predicate.
func (h *Handler) JSONWhen(fn func(r *http.Request, err error) bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.jsonWhen = fn
}

// BeforeRender stores a pre-write hook.
func (h *Handler) BeforeRender(fn func(rc RenderContext, err error, status int) int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.beforeRender = append(h.beforeRender, fn)
}

// SetErrorPageRenderer stores the Inertia error page renderer.
func (h *Handler) SetErrorPageRenderer(r contract.ErrorPageRenderer) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.errorPageRenderer = r
}

// RegisterCustomHandler registers a custom handler for a specific exception type.
func (h *Handler) RegisterCustomHandler(exceptionType any, handler func(RenderContext, error, *ErrorContext)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.customHandlers[reflect.TypeOf(exceptionType)] = handler
}

// HandlePanic handles a recovered panic value.
func (h *Handler) HandlePanic(ctx RenderContext, recovered any) {
	err := panicerr.FromRecovered(recovered)

	exCtx := NewErrorContext()
	// Skip more frames for panic recovery path
	exCtx.WithStackTrace(contract.CaptureStackTrace(3))
	exCtx.URL = requestPath(ctx)
	exCtx.Method = requestMethod(ctx)

	// Always report panics (bypasses ShouldReport)
	h.reportToAll(err, exCtx)

	// Render as internal server error. The rendered exception carries the
	// panic's text but not its chain: a panic always renders 500, even when
	// the panic value is an HTTP exception or Renderable that errors.As would
	// otherwise resolve through the recovered-panic wrapper.
	h.Render(ctx, NewInternalServerErrorException(err.Error()).WithPrevious(errors.New(err.Error())), exCtx)
}

// requestPath returns the path of the request behind ctx, or "".
func requestPath(ctx RenderContext) string {
	if r := ctx.Request(); r != nil && r.URL != nil {
		return r.URL.Path
	}
	return ""
}

// requestMethod returns the method of the request behind ctx, or "".
func requestMethod(ctx RenderContext) string {
	if r := ctx.Request(); r != nil {
		return r.Method
	}
	return ""
}

// requestHeader returns a header of the request behind ctx, or "".
func requestHeader(ctx RenderContext, key string) string {
	if r := ctx.Request(); r != nil {
		return r.Header.Get(key)
	}
	return ""
}
