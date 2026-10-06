package problem

import (
	"context"
	"net"
	"net/http"
	"reflect"
	"sync"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/clientip"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/nilval"
	"github.com/velocitykode/velocity/trace"
)

// Verify *Handler implements contract.ErrorHandler at compile time.
var _ contract.ErrorHandler = (*Handler)(nil)

// RenderContext is the surface the pipeline renders through.
type RenderContext = contract.RenderContext

// Handler is the error pipeline. One Handler serves every request
// concurrently: rules and settings sit behind a read-write mutex, every
// mutation replaces the affected slice rather than editing it in place, and
// each request works on a snapshot taken under the read lock.
type Handler struct {
	mu sync.RWMutex

	reporters   []Reporter
	renderers   map[string]Renderer
	debug       bool
	environment string
	apiMode     bool
	apiPrefixes []string
	logger      contract.Logger
	// ownReporter is the LogReporter NewHandler built when no reporters
	// were given; SetLogger moves it with the handler's own logger. Nil
	// when the application supplied the reporters.
	ownReporter *LogReporter

	// User rules, registered through the contract.ErrorHandler methods and
	// the generic helpers. They outrank the framework rules below.
	mapRules         []contract.MapRule
	renderRules      []contract.RenderRule
	reportRules      []contract.ReportRule
	ignoreRules      []contract.IgnoreRule
	unignoreRules    []contract.IgnoreRule
	levelRules       []contract.LevelRule
	throttleRules    []contract.ThrottleRule
	ignorePredicates []func(error, *ErrorContext) bool
	contextProviders []func(error, *ErrorContext) map[string]any
	jsonWhen         func(*http.Request, error) bool
	beforeRender     []func(RenderContext, error, int) int
	errorPage        contract.ErrorPageRenderer

	// Framework rules: lower precedence than every user rule.
	frameworkIgnores []contract.IgnoreRule
	frameworkPrepare []contract.MapRule
	frameworkRender  []contract.RenderRule
	frameworkLevels  []contract.LevelRule

	// trustedProxies is the parsed list of proxy networks whose forwarded
	// headers (Forwarded, X-Forwarded-For, X-Real-IP) may be honoured when
	// recording the client IP on the ErrorContext. Nil trusts no proxy: the
	// RemoteAddr IP is recorded, so a direct client cannot poison the log
	// with a spoofed header.
	trustedProxies []*net.IPNet

	throttle *throttleBuckets
}

// Option configures a Handler at construction.
type Option func(*Handler)

// NewHandler returns a Handler with the JSON and HTML renderers, the
// framework rules that need only the standard library, and, unless
// WithReporters is given, one LogReporter bound to the handler logger.
// Debug mode is force-disabled in a production environment.
func NewHandler(opts ...Option) *Handler {
	h := &Handler{
		renderers:   map[string]Renderer{"json": NewJSONRenderer(), "html": NewHTMLRenderer()},
		environment: "production",
		throttle:    newThrottleBuckets(),
	}
	for _, opt := range opts {
		opt(h)
	}
	h.logger = fallbacklog.Resolve(h.logger)
	if h.reporters == nil {
		h.ownReporter = NewLogReporter(WithLogger(h.logger))
		h.reporters = []Reporter{h.ownReporter}
	}
	registerStdlibRules(h)

	if h.debug && contract.IsProductionEnv(h.environment) {
		h.debug = false
		h.logger.Warn(debugForcedOffWarning)
	} else if h.debug {
		h.logger.Warn("error handler running in debug mode: stack traces and source are exposed in error responses; never enable APP_DEBUG in production")
	}
	return h
}

// debugForcedOffWarning is logged when debug rendering is turned off
// because the environment is production.
const debugForcedOffWarning = "APP_DEBUG=true is ignored in production: debug rendering is force-disabled so stack traces and source never reach clients"

// WithHandlerLogger sets the logger for the handler's own messages (debug
// notices, renderer and reporter failures) and for the default LogReporter.
// Without it, or with nil, both write through the framework's standalone
// fallback logger, which writes warnings and errors to standard error.
func WithHandlerLogger(l contract.Logger) Option {
	return func(h *Handler) { h.logger = l }
}

var _ contract.LoggerAware = (*Handler)(nil)

// SetLogger replaces the logger for the handler's own messages and for the
// LogReporter NewHandler built when no reporters were given; nil restores
// the framework's standalone fallback logger for both. Reporters the
// application supplied (WithReporters, AddReporter, SetReporters) keep
// their own loggers. Safe while errors are reported and requests served: a
// report already writing finishes on the logger it read. velocity.New's
// logger sweep calls it, so a module or callback replacing Services.Log
// moves the built-in error pipeline at the next lifecycle boundary.
func (h *Handler) SetLogger(l contract.Logger) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.logger = fallbacklog.Resolve(l)
	if h.ownReporter != nil {
		h.ownReporter.SetLogger(l)
	}
}

// WithDebug enables or disables debug rendering.
func WithDebug(debug bool) Option {
	return func(h *Handler) { h.debug = debug }
}

// WithEnvironment sets the environment name.
func WithEnvironment(env string) Option {
	return func(h *Handler) { h.environment = env }
}

// WithReporters sets the reporters, replacing the default LogReporter. An
// empty call leaves the handler with no reporters.
func WithReporters(reporters ...Reporter) Option {
	return func(h *Handler) { h.reporters = append([]Reporter{}, reporters...) }
}

// WithRenderers adds or replaces renderers by content type key ("json",
// "html").
func WithRenderers(renderers map[string]Renderer) Option {
	return func(h *Handler) {
		for k, v := range renderers {
			if v != nil {
				h.renderers[k] = v
			}
		}
	}
}

// WithAPIMode makes every response render as JSON when enabled.
func WithAPIMode(enabled bool) Option {
	return func(h *Handler) { h.apiMode = enabled }
}

// WithAPIPrefixes sets the path prefixes whose responses render as JSON,
// matched as SetAPIPrefixes describes.
func WithAPIPrefixes(prefixes ...string) Option {
	return func(h *Handler) { h.apiPrefixes = append([]string(nil), prefixes...) }
}

// WithTrustedProxies installs the proxy networks trusted for client IP
// resolution. The list is deep-cloned so later caller mutation cannot change
// the handler's trust decisions.
func WithTrustedProxies(proxies []*net.IPNet) Option {
	return func(h *Handler) { h.trustedProxies = clientip.CloneIPNets(proxies) }
}

// SetDebug toggles debug rendering. Enabling it in a production environment
// is refused and logged.
func (h *Handler) SetDebug(debug bool) {
	h.mu.Lock()
	refused := debug && contract.IsProductionEnv(h.environment)
	if !refused {
		h.debug = debug
	}
	logger := h.logger
	h.mu.Unlock()
	switch {
	case refused:
		logger.Warn("refusing to enable debug mode in a production environment: stack traces and source stay hidden")
	case debug:
		logger.Warn("error handler debug mode enabled: stack traces and source are exposed in error responses; never enable it in production")
	}
}

// IsDebug reports whether debug rendering is on.
func (h *Handler) IsDebug() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.debug
}

// SetEnvironment sets the environment name. Moving a debug handler to a
// production environment turns debug rendering off and logs it, the same
// guard NewHandler applies.
func (h *Handler) SetEnvironment(env string) {
	h.mu.Lock()
	h.environment = env
	forced := h.debug && contract.IsProductionEnv(env)
	if forced {
		h.debug = false
	}
	logger := h.logger
	h.mu.Unlock()
	if forced {
		logger.Warn(debugForcedOffWarning)
	}
}

// GetEnvironment returns the environment name.
func (h *Handler) GetEnvironment() string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.environment
}

// AddReporter appends a reporter.
func (h *Handler) AddReporter(reporter Reporter) {
	if nilval.Is(reporter) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reporters = appendCopy(h.reporters, reporter)
}

// SetReporters replaces every reporter.
func (h *Handler) SetReporters(reporters ...Reporter) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.reporters = append([]Reporter{}, reporters...)
}

// AddRenderer sets the renderer for a content type key ("json", "html").
func (h *Handler) AddRenderer(contentType string, renderer Renderer) {
	if nilval.Is(renderer) {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	next := make(map[string]Renderer, len(h.renderers)+1)
	for k, v := range h.renderers {
		next[k] = v
	}
	next[contentType] = renderer
	h.renderers = next
}

// SetAPIMode makes every response render as JSON when enabled.
func (h *Handler) SetAPIMode(enabled bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.apiMode = enabled
}

// IsAPIMode reports whether API mode is on.
func (h *Handler) IsAPIMode() bool {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.apiMode
}

// SetAPIPrefixes sets the path prefixes whose responses render as JSON.
// A prefix matches by path segment: "/api" covers "/api" and "/api/users"
// but not "/apiary"; a prefix ending in "/" covers every path starting
// with it; an empty prefix covers nothing.
//
// It replaces the whole list, including the prefixes chain.Routing.API
// registered. App.Errors callbacks run after routes are registered, so an
// application that wants its own prefixes beside those appends to
// GetAPIPrefixes():
//
//	h.SetAPIPrefixes(append(h.GetAPIPrefixes(), "/rpc")...)
func (h *Handler) SetAPIPrefixes(prefixes ...string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.apiPrefixes = append([]string(nil), prefixes...)
}

// GetAPIPrefixes returns a copy of the configured API path prefixes.
func (h *Handler) GetAPIPrefixes() []string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return append([]string(nil), h.apiPrefixes...)
}

// SetTrustedProxies installs the proxy networks trusted for client IP
// resolution. Safe to call while requests are served.
func (h *Handler) SetTrustedProxies(proxies []*net.IPNet) {
	cloned := clientip.CloneIPNets(proxies)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.trustedProxies = cloned
}

// snapshot is the per-request view of the handler's configuration. Every
// slice and map in it is owned by the handler and never mutated after
// publication, so reading it without the lock is safe.
type snapshot struct {
	reporters        []Reporter
	renderers        map[string]Renderer
	debug            bool
	apiMode          bool
	apiPrefixes      []string
	logger           contract.Logger
	mapRules         []contract.MapRule
	renderRules      []contract.RenderRule
	reportRules      []contract.ReportRule
	ignoreRules      []contract.IgnoreRule
	unignoreRules    []contract.IgnoreRule
	levelRules       []contract.LevelRule
	throttleRules    []contract.ThrottleRule
	ignorePredicates []func(error, *ErrorContext) bool
	contextProviders []func(error, *ErrorContext) map[string]any
	jsonWhen         func(*http.Request, error) bool
	beforeRender     []func(RenderContext, error, int) int
	errorPage        contract.ErrorPageRenderer
	frameworkIgnores []contract.IgnoreRule
	frameworkPrepare []contract.MapRule
	frameworkRender  []contract.RenderRule
	frameworkLevels  []contract.LevelRule
	trustedProxies   []*net.IPNet
}

// snap takes the per-request snapshot under the read lock.
func (h *Handler) snap() *snapshot {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return &snapshot{
		reporters:        h.reporters,
		renderers:        h.renderers,
		debug:            h.debug,
		apiMode:          h.apiMode,
		apiPrefixes:      h.apiPrefixes,
		logger:           h.logger,
		mapRules:         h.mapRules,
		renderRules:      h.renderRules,
		reportRules:      h.reportRules,
		ignoreRules:      h.ignoreRules,
		unignoreRules:    h.unignoreRules,
		levelRules:       h.levelRules,
		throttleRules:    h.throttleRules,
		ignorePredicates: h.ignorePredicates,
		contextProviders: h.contextProviders,
		jsonWhen:         h.jsonWhen,
		beforeRender:     h.beforeRender,
		errorPage:        h.errorPage,
		frameworkIgnores: h.frameworkIgnores,
		frameworkPrepare: h.frameworkPrepare,
		frameworkRender:  h.frameworkRender,
		frameworkLevels:  h.frameworkLevels,
		trustedProxies:   h.trustedProxies,
	}
}

// fillRequestContext returns ctx (or, when nil, a new one built by
// trace.NewErrorContext from the request's context, carrying its ids) with
// the request facts it is missing filled from rc: method, path, client IP
// (through the trusted-proxy list), user agent and timestamp.
func fillRequestContext(ctx *ErrorContext, rc RenderContext, proxies []*net.IPNet) *ErrorContext {
	r := requestOf(rc)
	if ctx == nil {
		ctx = trace.NewErrorContext(contextOf(r))
	}
	if ctx.Timestamp.IsZero() {
		ctx.Timestamp = time.Now()
	}
	if r == nil {
		return ctx
	}
	if ctx.Method == "" {
		ctx.Method = r.Method
	}
	if ctx.URL == "" && r.URL != nil {
		ctx.URL = r.URL.Path
	}
	if ctx.IP == "" {
		ctx.IP = clientip.ExtractString(r, proxies)
	}
	if ctx.UserAgent == "" {
		ctx.UserAgent = r.UserAgent()
	}
	return ctx
}

// contextOf returns r's context, or context.Background() when r is nil.
func contextOf(r *http.Request) context.Context {
	if r == nil {
		return context.Background()
	}
	return r.Context()
}

// requestOf returns the request behind rc, or nil.
func requestOf(rc RenderContext) *http.Request {
	if rc == nil {
		return nil
	}
	return rc.Request()
}

// appendCopy returns a new slice holding s followed by v, leaving s's
// backing array untouched for snapshot readers.
func appendCopy[T any](s []T, v ...T) []T {
	out := make([]T, 0, len(s)+len(v))
	out = append(out, s...)
	return append(out, v...)
}

// ruleKey returns key when it is usable as a map key and in comparisons, or
// nil (an anonymous rule) when its dynamic type is not comparable.
func ruleKey(key any) any {
	if key == nil || !reflect.TypeOf(key).Comparable() {
		return nil
	}
	return key
}
