package contract

import (
	"io"
	"net/http"
	"time"
)

// ErrorHandler is the application's error pipeline: it reports an error
// once and renders one response for it. Rules registered through the Add*
// methods are non-generic; typed sugar over them lives in the error
// package.
type ErrorHandler interface {
	// HandleRequest reports err (subject to the report gate) and renders
	// the response for it through rc. A nil ctx is allowed.
	HandleRequest(rc RenderContext, err error, ctx *ErrorContext)
	// HandleConsole reports err for a console command and returns the
	// process exit code. stderr receives any console output.
	HandleConsole(stderr io.Writer, err error) int
	// Report sends err to the reporters when the report gate allows it.
	Report(err error, ctx *ErrorContext)
	// TryReport reports err exactly as Report does and returns whether the
	// report was actually handled: true only when something took the report
	// (a SelfReporting error, a report rule, or the configured reporters,
	// each of which is then called), false when err was nil or already
	// reported, when the report gate dropped it, and when the report ended
	// early without being handled (a panic inside the pipeline). Callers
	// that would otherwise report the same failure a second time through
	// another path skip that path only on true.
	TryReport(err error, ctx *ErrorContext) bool
	// Render writes the response for err through rc.
	Render(rc RenderContext, err error, ctx *ErrorContext)
	// ShouldReport reports whether err passes the report gate.
	ShouldReport(err error) bool

	// AddMapRule registers a rule that replaces a matched error before it
	// is reported and rendered.
	AddMapRule(rule MapRule)
	// AddRenderRule registers a rule that renders a matched error.
	AddRenderRule(rule RenderRule)
	// AddReportRule registers a rule that reports a matched error.
	AddReportRule(rule ReportRule)
	// AddIgnoreRule registers a rule that keeps a matched error from being
	// reported or, with Unignore set, forces it back through the gate.
	AddIgnoreRule(rule IgnoreRule)
	// AddLevelRule registers the log level for a matched error.
	AddLevelRule(rule LevelRule)
	// AddThrottleRule registers report throttling for a matched error.
	AddThrottleRule(rule ThrottleRule)
	// IgnoreIf registers a predicate; an error for which it returns true
	// is not reported.
	IgnoreIf(pred func(err error, ctx *ErrorContext) bool)
	// ContextUsing registers a provider whose fields are merged into
	// ErrorContext.Extra for every report.
	ContextUsing(fn func(err error, ctx *ErrorContext) map[string]any)
	// JSONWhen replaces the predicate deciding whether a response renders
	// as JSON. The pipeline asks it once per failure, with the error as it
	// will be rendered (after the framework's own status mapping, a
	// recovered panic as a 500 HTTPError wrapping it).
	JSONWhen(fn func(r *http.Request, err error) bool)
	// WantsJSON reports whether the response to r for err renders as JSON:
	// the JSONWhen predicate alone when set, otherwise API mode, then the
	// API prefixes, then WantsJSON(r). Render rules see the pipeline's
	// answer through RenderContext.WantsJSON, so code that answers an
	// error outside the pipeline (a validator writing a redirect) asks
	// here to agree with it; the pipeline asks with the error as it will
	// be rendered. err may be nil.
	WantsJSON(r *http.Request, err error) bool
	// BeforeRender registers a hook run before the response is written. It
	// may set headers through rc and returns the status to write.
	BeforeRender(fn func(rc RenderContext, err error, status int) int)
	// SetErrorPageRenderer installs the renderer for error pages. Outside
	// debug mode it answers Inertia requests at the real status and
	// full-page HTML requests for which the application registered no page
	// of its own, ahead of the built-in template.
	SetErrorPageRenderer(r ErrorPageRenderer)

	// AddReporter appends a reporter.
	AddReporter(reporter Reporter)
	// SetReporters replaces every reporter.
	SetReporters(reporters ...Reporter)
	// AddRenderer sets the renderer for a content type key.
	AddRenderer(contentType string, renderer Renderer)

	// SetDebug toggles debug rendering (refused in production).
	SetDebug(debug bool)
	// IsDebug reports whether debug rendering is on.
	IsDebug() bool
	// SetEnvironment sets the environment name; a production name turns
	// debug rendering off.
	SetEnvironment(env string)
	// GetEnvironment returns the environment name.
	GetEnvironment() string

	// SetAPIMode makes every response render as JSON when enabled.
	SetAPIMode(enabled bool)
	// IsAPIMode reports whether API mode is on.
	IsAPIMode() bool
	// SetAPIPrefixes sets the path prefixes whose responses render as
	// JSON, matched by path segment ("/api" covers "/api/users", not
	// "/apiary"; a prefix ending in "/" covers every path starting with
	// it). It replaces the whole list, including prefixes registered by
	// API route groups; append to GetAPIPrefixes() to keep them.
	SetAPIPrefixes(prefixes ...string)
	// GetAPIPrefixes returns the configured API path prefixes.
	GetAPIPrefixes() []string
}

// Reporter receives reported errors.
type Reporter interface {
	Report(err error, ctx *ErrorContext)
}

// Renderer renders an error response for one content type at the status the
// pipeline resolved.
type Renderer interface {
	Render(rc RenderContext, err error, ctx *ErrorContext, status int, debug bool) error
	ContentType() string
}

// ErrorPageRenderer is an optional facet (implemented by the view layer)
// that renders the configured error page at status. The error pipeline asks
// it for Inertia requests and for full-page HTML requests the application
// has no page of its own for. It returns false, having written nothing,
// when no error page is configured.
type ErrorPageRenderer interface {
	RenderErrorPage(rc RenderContext, status int, message string) (bool, error)
}

// RequestUserIdentifier is an optional facet (implemented by the auth
// layer) that names the authenticated user of r for ErrorContext.UserID,
// or "" when there is none.
type RequestUserIdentifier interface {
	RequestUserID(r *http.Request) string
}

// ErrorMatcher reports whether a rule applies to err. Typed matchers are
// built once (errors.As against a type, errors.Is against a sentinel) and
// stored in the rule.
type ErrorMatcher func(err error) bool

// LogLevel is the level a reported error is logged at. The zero value
// leaves the choice to the reporter.
type LogLevel int

// Log levels, lowest to highest.
const (
	LogLevelUnset LogLevel = iota
	LogLevelDebug
	LogLevelInfo
	LogLevelWarn
	LogLevelError
)

// String returns the level name, or "" for LogLevelUnset and unknown
// values.
func (l LogLevel) String() string {
	switch l {
	case LogLevelDebug:
		return "debug"
	case LogLevelInfo:
		return "info"
	case LogLevelWarn:
		return "warn"
	case LogLevelError:
		return "error"
	default:
		return ""
	}
}

// Every rule below carries Key and Match. Key identifies what the rule
// matches (a type or a sentinel) so a later rule with an equal key can
// override an earlier one and throttle buckets and unignores can target
// it; it must be comparable, and nil marks an anonymous rule. Match
// decides whether the rule applies to an error.

// MapRule replaces a matched error before it is reported and rendered.
type MapRule struct {
	Key   any
	Match ErrorMatcher
	// Map returns the replacement error; nil keeps the original. Only the
	// first rule whose Match succeeds runs, so a nil result still ends the
	// search.
	Map func(err error) error
}

// RenderRule renders a matched error.
type RenderRule struct {
	Key   any
	Match ErrorMatcher
	// Render writes the response and returns true, or returns false to
	// fall through to the next rule.
	Render func(rc RenderContext, err error, ctx *ErrorContext) bool
	// Status, used when Render is nil, renders the matched error through
	// content negotiation at this status.
	Status int
}

// ReportRule reports a matched error.
type ReportRule struct {
	Key   any
	Match ErrorMatcher
	// Report handles the report and returns true to stop the configured
	// reporters from also seeing the error, or false to continue to them.
	Report func(err error, ctx *ErrorContext) bool
}

// IgnoreRule keeps a matched error from being reported.
type IgnoreRule struct {
	Key   any
	Match ErrorMatcher
	// Unignore inverts the rule: a matched error is reported even when an
	// internal or user ignore would drop it.
	Unignore bool
}

// LevelRule sets the log level for a matched error.
type LevelRule struct {
	Key   any
	Match ErrorMatcher
	Level LogLevel
}

// ThrottleRule throttles reports of a matched error.
type ThrottleRule struct {
	Key      any
	Match    ErrorMatcher
	Throttle Throttle
}

// Throttle limits how often matched errors are reported. The zero value
// throttles nothing.
type Throttle struct {
	// Sample is the fraction of matched errors reported, in (0, 1]. Zero
	// disables sampling.
	Sample float64
	// MaxPerWindow caps reports per Window for each bucket. Zero disables
	// the cap.
	MaxPerWindow int
	// Window is the length of one MaxPerWindow period.
	Window time.Duration
	// By derives the bucket key from the error, within the rule's Key. Nil
	// puts every matched error in one bucket.
	By func(err error) string
}

// ErrorContext carries the facts about where and how an error happened,
// for reporters and renderers.
type ErrorContext struct {
	RequestID string
	TraceID   string
	SpanID    string
	UserID    string
	URL       string
	Method    string
	IP        string
	UserAgent string
	Timestamp time.Time
	// Recovered is true when the error came from a recovered panic.
	Recovered bool
	// PanicStack is the raw goroutine stack captured at the panic.
	PanicStack string
	// Level is the log level the report is emitted at.
	Level      LogLevel
	StackTrace *StackTrace
	Extra      map[string]any
}

// WithRequestInfo adds request information to the context.
func (c *ErrorContext) WithRequestInfo(method, url, ip, userAgent string) *ErrorContext {
	c.Method = method
	c.URL = url
	c.IP = ip
	c.UserAgent = userAgent
	return c
}

// WithIDs adds request and trace IDs to the context.
func (c *ErrorContext) WithIDs(requestID, traceID string) *ErrorContext {
	c.RequestID = requestID
	c.TraceID = traceID
	return c
}

// WithUserID adds user ID to the context.
func (c *ErrorContext) WithUserID(userID string) *ErrorContext {
	c.UserID = userID
	return c
}

// WithStackTrace adds a stack trace to the context.
func (c *ErrorContext) WithStackTrace(st *StackTrace) *ErrorContext {
	c.StackTrace = st
	return c
}

// WithExtra adds extra data to the context.
func (c *ErrorContext) WithExtra(key string, value any) *ErrorContext {
	if c.Extra == nil {
		c.Extra = make(map[string]any)
	}
	c.Extra[key] = value
	return c
}
