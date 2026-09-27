package interceptors

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/trace"
)

// LoggingConfig configures the logging interceptor
type LoggingConfig struct {
	// Logger is the logger to use. Defaults to the global logger.
	Logger contract.Logger

	// LogPayloads enables logging of request/response payloads
	LogPayloads bool

	// SkipMethods is a list of methods to skip logging
	SkipMethods map[string]bool

	// SkipHealthChecks skips logging for health check endpoints
	SkipHealthChecks bool

	// SlowThreshold logs requests slower than this duration at warn level
	SlowThreshold time.Duration

	// ExtraFields adds extra fields to log entries
	ExtraFields func(ctx context.Context) []interface{}

	// EventDispatcher dispatches gRPC events. If nil, no events are dispatched.
	EventDispatcher grpcevents.EventDispatchFunc
}

// LoggingOption configures logging behavior
type LoggingOption func(*LoggingConfig)

// WithLoggingLogger sets a custom logger
func WithLoggingLogger(logger contract.Logger) LoggingOption {
	return func(c *LoggingConfig) {
		c.Logger = logger
	}
}

// WithLogPayloads enables payload logging
func WithLogPayloads(enabled bool) LoggingOption {
	return func(c *LoggingConfig) {
		c.LogPayloads = enabled
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

// WithSlowThreshold sets the slow request threshold
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

func logRequest(ctx context.Context, method string, start time.Time, err error, cfg *LoggingConfig) {
	logger := cfg.Logger
	if logger == nil {
		return // No logger configured, skip logging
	}

	duration := time.Since(start)

	// Extract status code
	code := codes.OK
	if err != nil {
		if s, ok := status.FromError(err); ok {
			code = s.Code()
		} else {
			code = codes.Unknown
		}
	}

	// Build base fields
	fields := []interface{}{
		"method", method,
		"code", code.String(),
		"duration_ms", duration.Milliseconds(),
	}

	// Add user info from context if available
	claims := ClaimsFromContext(ctx)
	if claims != nil {
		fields = append(fields,
			"user_id", claims.GetUserID(),
			"team_id", claims.GetTeamID(),
		)
	}

	// Add request ID if available
	if requestID := trace.GetRequestID(ctx); requestID != "" {
		fields = append(fields, "request_id", requestID)
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
	} else if cfg.SlowThreshold > 0 && duration > cfg.SlowThreshold {
		logger.Warn("gRPC request (slow)", fields...)
	} else {
		logger.Info("gRPC request", fields...)
	}
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

// dispatchEvent swallows dispatcher errors and panics: an event sink must
// never fail or panic a request.
func dispatchEvent(ctx context.Context, dispatcher grpcevents.EventDispatchFunc, event interface{}) {
	if dispatcher == nil {
		return
	}
	defer func() { _ = recover() }()
	_ = dispatcher(ctx, event)
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

// correlate applies the edge rules to an incoming call and returns the
// context the handler runs under.
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
	return ctx
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
