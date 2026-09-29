package interceptors

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/fallbacklog"
	"github.com/velocitykode/velocity/internal/latency"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/trace"
)

// LoggingConfig configures the logging interceptor
type LoggingConfig struct {
	// Logger receives one line per request. Nil means the framework's
	// standalone fallback logger, which writes only the warn and error
	// lines (failed and slow requests) to standard error.
	Logger contract.Logger

	// SkipMethods is a list of methods to skip logging
	SkipMethods map[string]bool

	// SkipHealthChecks skips logging for health check endpoints
	SkipHealthChecks bool

	// SlowThreshold writes a successful call that ran longer than it at
	// warn level ("gRPC request (slow)") instead of info. Zero disables
	// the rule. The ORM's DB_SLOW_QUERY_THRESHOLD follows the same rule
	// (internal/latency), and both lines write the duration as
	// duration_ms. Logging defaults it to 5s.
	SlowThreshold time.Duration

	// ExtraFields adds extra fields to log entries
	ExtraFields func(ctx context.Context) []interface{}

	// EventDispatcher dispatches gRPC events. If nil, no events are dispatched.
	EventDispatcher grpcevents.EventDispatchFunc

	// events holds EventDispatcher and applies the failure policy to a
	// failed dispatch; Logging builds it once the options are applied.
	events *eventemit.Emitter
}

// LoggingOption configures logging behavior
type LoggingOption func(*LoggingConfig)

// WithLoggingLogger sets the logger request lines go to (see
// LoggingConfig.Logger for the nil default).
func WithLoggingLogger(logger contract.Logger) LoggingOption {
	return func(c *LoggingConfig) {
		c.Logger = logger
	}
}

// WithSkipMethods sets methods to skip logging
func WithSkipMethods(methods ...string) LoggingOption {
	return func(c *LoggingConfig) {
		c.SkipMethods = make(map[string]bool)
		for _, m := range methods {
			c.SkipMethods[m] = true
		}
	}
}

// WithSkipHealthChecks skips logging for health check endpoints
func WithSkipHealthChecks(skip bool) LoggingOption {
	return func(c *LoggingConfig) {
		c.SkipHealthChecks = skip
	}
}

// WithSlowThreshold sets the slow request threshold (see
// LoggingConfig.SlowThreshold); zero disables it.
func WithSlowThreshold(d time.Duration) LoggingOption {
	return func(c *LoggingConfig) {
		c.SlowThreshold = d
	}
}

// WithExtraFields adds extra fields to log entries
func WithExtraFields(fn func(ctx context.Context) []interface{}) LoggingOption {
	return func(c *LoggingConfig) {
		c.ExtraFields = fn
	}
}

// WithEventDispatcher sets the event dispatcher for gRPC events.
func WithEventDispatcher(dispatcher grpcevents.EventDispatchFunc) LoggingOption {
	return func(c *LoggingConfig) {
		c.EventDispatcher = dispatcher
	}
}

// Logging creates a logging interceptor pair that logs all requests.
// The unary variant lives in logging_unary.go and the stream variant in
// logging_stream.go; this file holds the shared configuration, the
// logRequest helper, event-dispatch plumbing, and correlate, which gives
// every call its span and request id from the incoming metadata.
func Logging(opts ...LoggingOption) InterceptorPair {
	cfg := &LoggingConfig{
		SkipMethods:      make(map[string]bool),
		SkipHealthChecks: true,
		SlowThreshold:    5 * time.Second,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	cfg.events = newEventEmitter(cfg.EventDispatcher, cfg.Logger)

	return InterceptorPair{
		Unary:  loggingUnary(cfg),
		Stream: loggingStream(cfg),
	}
}

func shouldSkip(method string, cfg *LoggingConfig) bool {
	// Skip explicitly configured methods
	if cfg.SkipMethods[method] {
		return true
	}

	// Skip health checks if configured
	if cfg.SkipHealthChecks && isHealthCheck(method) {
		return true
	}

	return false
}

func isHealthCheck(method string) bool {
	return method == "/grpc.health.v1.Health/Check" ||
		method == "/grpc.health.v1.Health/Watch"
}

// logRequest writes the call's line through the configured logger (the
// fallback when none is set), bound to the call's request, trace and span
// ids (trace.LogFields).
func logRequest(ctx context.Context, method string, start time.Time, err error, cfg *LoggingConfig) {
	logger := fallbacklog.Resolve(cfg.Logger).With(trace.LogFields(ctx)...)

	duration := time.Since(start)

	code := statusCodeOf(err)

	// Build base fields
	fields := []interface{}{
		"method", method,
		"code", code.String(),
		latency.Key, latency.Millis(duration),
	}

	// Add user info from context if available
	claims := ClaimsFromContext(ctx)
	if claims != nil {
		fields = append(fields,
			"user_id", claims.GetUserID(),
			"team_id", claims.GetTeamID(),
		)
	}

	// Add extra fields if configured
	if cfg.ExtraFields != nil {
		fields = append(fields, cfg.ExtraFields(ctx)...)
	}

	// Determine log level based on result and duration
	if err != nil && code != codes.Canceled && code != codes.NotFound {
		if code == codes.Internal || code == codes.Unknown {
			logger.Error("gRPC request", fields...)
		} else {
			logger.Warn("gRPC request", fields...)
		}
	} else if latency.Slow(duration, cfg.SlowThreshold) {
		logger.Warn("gRPC request (slow)", fields...)
	} else {
		logger.Info("gRPC request", fields...)
	}
}

// statusCodeOf returns the gRPC status code a handler's error ends the call
// with, as grpc-go derives the status it sends: OK for nil, the error's own
// status when it carries one (wrapped included), else the status of a
// context error (Canceled, DeadlineExceeded, wrapped included), else
// Unknown.
func statusCodeOf(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	if s, ok := status.FromError(err); ok {
		return s.Code()
	}
	return status.FromContextError(err).Code()
}

// Event dispatching helpers, shared between unary and stream variants.

// redactMetadata returns a copy of md with sensitive headers redacted
func redactMetadata(md map[string][]string) map[string][]string {
	if md == nil {
		return nil
	}
	redacted := make(map[string][]string, len(md))
	for k, v := range md {
		lower := strings.ToLower(k)
		if lower == "authorization" || lower == "cookie" || lower == "set-cookie" || lower == "x-api-key" ||
			strings.Contains(lower, "token") || strings.Contains(lower, "secret") {
			redacted[k] = []string{"[REDACTED]"}
		} else {
			redacted[k] = v
		}
	}
	return redacted
}

// newEventEmitter returns the emitter an interceptor dispatches its events
// through: it holds dispatch (none when nil) and logs a failed dispatch
// through logger (the framework's standalone fallback logger when nil).
func newEventEmitter(dispatch grpcevents.EventDispatchFunc, logger contract.Logger) *eventemit.Emitter {
	e := &eventemit.Emitter{}
	if dispatch != nil {
		e.Set(dispatch)
	}
	e.UseLogger(func() contract.Logger { return logger })
	return e
}

// eventsInstalled reports whether events holds a dispatcher, so an
// interceptor builds an event only when one would receive it.
func eventsInstalled(events *eventemit.Emitter) bool {
	return events != nil && events.Installed()
}

// dispatchEvent hands event to the dispatcher events holds. A failed
// dispatch, an error or a panic, goes to the failure policy (counted, its
// event's first failure logged) and never reaches the request: an event
// sink must never fail or panic a request.
func dispatchEvent(ctx context.Context, events *eventemit.Emitter, event interface{}) {
	if !eventsInstalled(events) {
		return
	}
	defer func() {
		if p := recover(); p != nil {
			events.Fail(ctx, panicerr.FromRecovered(p), event)
		}
	}()
	events.Emit(ctx, event)
}

// detectProtocol determines if the request came via HTTP gateway or direct gRPC
func detectProtocol(ctx context.Context) grpcevents.Protocol {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return grpcevents.ProtocolGRPC
	}

	// grpc-gateway adds these headers when proxying HTTP requests
	if _, hasGateway := md["grpcgateway-accept"]; hasGateway {
		return grpcevents.ProtocolHTTP
	}
	if _, hasContentType := md["grpcgateway-content-type"]; hasContentType {
		return grpcevents.ProtocolHTTP
	}
	// Also check for x-forwarded headers which indicate HTTP proxy
	if _, hasForwarded := md["x-forwarded-for"]; hasForwarded {
		return grpcevents.ProtocolHTTP
	}
	// Check for user-agent containing grpc-gateway
	if ua, hasUA := md["user-agent"]; hasUA && len(ua) > 0 {
		for _, agent := range ua {
			if agent != "" && !strings.HasPrefix(agent, "grpc") {
				// Non-grpc user agent likely means HTTP
				return grpcevents.ProtocolHTTP
			}
		}
	}

	return grpcevents.ProtocolGRPC
}

// Correlation returns the interceptor pair that gives a call its span and
// request id (see correlate for the rules), once: a later Logging, and any
// interceptor or handler after it, runs under the same ids, so the reports
// and lines of every interceptor in the chain carry the ids of the call. A
// framework-built server installs it first in its chain, ahead of its
// default recovery; a chain built by hand puts it first as well.
func Correlation() InterceptorPair {
	return InterceptorPair{
		Unary: func(ctx context.Context, req interface{}, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
			return handler(correlate(ctx), req)
		},
		Stream: func(srv interface{}, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
			return handler(srv, &tracedServerStream{inner: ss, ctx: correlate(ss.Context())})
		},
	}
}

// correlatedKey marks a context correlate returned; its value is the span id
// correlate gave the call.
type correlatedKey struct{}

// correlate applies the edge rules to an incoming call and returns the
// context the handler runs under.
//
// A context correlate already returned (an earlier Correlation or Logging
// in the chain), still carrying the span it gave the call, is returned
// unchanged: a call is correlated once.
//
// Trace: when an earlier interceptor in this process already put a trace in
// ctx, the call is a new span under it. Otherwise the traceparent metadata
// the caller sent is the carrier for trace.StartSpan: a valid one is
// continued with a new span whose parent is the caller's span, anything
// else (absent, malformed, repeated) starts a root span.
//
// Request id: an id already in ctx is kept; otherwise the x-request-id the
// caller sent when trace.ValidRequestID accepts it, else a generated one.
func correlate(ctx context.Context) context.Context {
	if span, ok := ctx.Value(correlatedKey{}).(string); ok && span == trace.GetSpanID(ctx) {
		return ctx
	}
	md, _ := metadata.FromIncomingContext(ctx)

	parent := trace.Parent{TraceID: trace.GetTraceID(ctx), SpanID: trace.GetSpanID(ctx)}
	if parent.TraceID == "" {
		parent, _ = trace.ParseTraceparent(singleMetadataValue(md, trace.TraceparentHeader))
	}
	ctx = trace.StartSpan(ctx, parent)

	if trace.GetRequestID(ctx) == "" {
		id := singleMetadataValue(md, trace.RequestIDHeader)
		if !trace.ValidRequestID(id) {
			id = trace.GenerateRequestID()
		}
		ctx = trace.WithRequestID(ctx, id)
	}
	return context.WithValue(ctx, correlatedKey{}, trace.GetSpanID(ctx))
}

// singleMetadataValue returns the one value md holds for key, or the empty
// string when it holds none or several: a repeated carrier is ambiguous and
// treated as absent.
func singleMetadataValue(md metadata.MD, key string) string {
	values := md.Get(key)
	if len(values) != 1 {
		return ""
	}
	return values[0]
}
