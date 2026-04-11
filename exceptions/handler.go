package exceptions

import (
	"fmt"
	"log"
	"net/http"
	"reflect"
	"sync"
)

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
	customHandlers map[reflect.Type]func(RenderContext, error, *ExceptionContext)
}

// Option is a functional option for configuring the Handler.
type Option func(*Handler)

// NewHandler creates a new exception handler.
func NewHandler(opts ...Option) *Handler {
	h := &Handler{
		reporters:      []Reporter{NewLogReporter()},
		renderers:      make(map[string]Renderer),
		dontReport:     make(map[string]bool),
		customHandlers: make(map[reflect.Type]func(RenderContext, error, *ExceptionContext)),
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
	if h.environment == "production" && h.debug {
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

// WithDontReport sets exception types that should not be reported.
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
	if debug && h.environment == "production" {
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
	path := ctx.RequestPath()
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

	// Check if the exception implements Reportable
	if reportable, ok := err.(Reportable); ok {
		return reportable.ShouldReport()
	}

	return true
}

// Report reports an exception to all configured reporters.
// Exceptions in the dontReport list are silently skipped.
func (h *Handler) Report(err error, ctx *ExceptionContext) {
	if !h.ShouldReport(err) {
		return
	}
	h.reportToAll(err, ctx)
}

// reportToAll sends an error to every configured reporter unconditionally.
func (h *Handler) reportToAll(err error, ctx *ExceptionContext) {
	h.mu.RLock()
	reporters := make([]Reporter, len(h.reporters))
	copy(reporters, h.reporters)
	h.mu.RUnlock()

	for _, reporter := range reporters {
		reporter.Report(err, ctx)
	}
}

// Render renders an exception response.
func (h *Handler) Render(ctx RenderContext, err error, exCtx *ExceptionContext) {
	// Check if error implements Renderable (only for API requests or if explicitly renderable)
	if renderable, ok := err.(Renderable); ok {
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

// Handle is a convenience method that reports and renders an exception.
func (h *Handler) Handle(ctx RenderContext, err error) {
	exCtx := NewExceptionContext()
	exCtx.WithStackTrace(CaptureStackTrace(1))
	exCtx.URL = ctx.RequestPath()
	exCtx.Method = ctx.RequestMethod()

	h.Report(err, exCtx)
	h.Render(ctx, err, exCtx)
}

// HandleWithContext reports and renders with a provided context.
func (h *Handler) HandleWithContext(ctx RenderContext, err error, exCtx *ExceptionContext) {
	if exCtx == nil {
		exCtx = NewExceptionContext()
	}
	if exCtx.StackTrace == nil {
		exCtx.WithStackTrace(CaptureStackTrace(1))
	}

	h.Report(err, exCtx)
	h.Render(ctx, err, exCtx)
}

// RegisterCustomHandler registers a custom handler for a specific exception type.
func (h *Handler) RegisterCustomHandler(exceptionType any, handler func(RenderContext, error, *ExceptionContext)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.customHandlers[reflect.TypeOf(exceptionType)] = handler
}

// HandlePanic handles a recovered panic value.
func (h *Handler) HandlePanic(ctx RenderContext, recovered any) {
	var err error
	switch v := recovered.(type) {
	case error:
		err = v
	case string:
		err = fmt.Errorf("panic: %s", v)
	default:
		err = fmt.Errorf("panic: %v", v)
	}

	exCtx := NewExceptionContext()
	// Skip more frames for panic recovery path
	exCtx.WithStackTrace(CaptureStackTrace(3))
	exCtx.URL = ctx.RequestPath()
	exCtx.Method = ctx.RequestMethod()

	// Always report panics (bypasses ShouldReport)
	h.reportToAll(err, exCtx)

	// Render as internal server error
	h.Render(ctx, NewInternalServerErrorException(err.Error()).WithPrevious(err), exCtx)
}
