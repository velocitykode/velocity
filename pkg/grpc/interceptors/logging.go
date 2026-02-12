package interceptors

import (
	"context"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/pkg/grpc/grpcevents"
	"github.com/velocitykode/velocity/pkg/log"
)

// LoggingConfig configures the logging interceptor
type LoggingConfig struct {
	// Logger is the logger to use. Defaults to the global logger.
	Logger log.Logger

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
func WithLoggingLogger(logger log.Logger) LoggingOption {
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

// LoggingInterceptor creates a unary logging interceptor.
// This is a convenience function for when you only need the unary interceptor.
func LoggingInterceptor(opts ...LoggingOption) grpc.UnaryServerInterceptor {
	return Logging(opts...).Unary
}

// StreamLoggingInterceptor creates a stream logging interceptor.
// This is a convenience function for when you only need the stream interceptor.
func StreamLoggingInterceptor(opts ...LoggingOption) grpc.StreamServerInterceptor {
	return Logging(opts...).Stream
}

func loggingUnary(cfg *LoggingConfig) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// Check if we should skip this method
		if shouldSkip(info.FullMethod, cfg) {
			return handler(ctx, req)
		}

		start := time.Now()

		// Dispatch request started event
		dispatchRequestStarted(ctx, info.FullMethod, start, cfg.EventDispatcher)

		// Call handler
		resp, err := handler(ctx, req)

		// Log the request
		logRequest(ctx, info.FullMethod, start, err, cfg)

		// Dispatch completion event
		dispatchRequestCompleted(ctx, info.FullMethod, start, err, cfg.EventDispatcher)

		return resp, err
	}
}

func loggingStream(cfg *LoggingConfig) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		// Check if we should skip this method
		if shouldSkip(info.FullMethod, cfg) {
			return handler(srv, ss)
		}

		start := time.Now()

		// Dispatch stream started event
		dispatchStreamStarted(ss.Context(), info.FullMethod, start, cfg.EventDispatcher)

		// Call handler
		err := handler(srv, ss)

		// Log the request
		logRequest(ss.Context(), info.FullMethod, start, err, cfg)

		// Dispatch stream completion event
		dispatchStreamCompleted(ss.Context(), info.FullMethod, start, err, cfg.EventDispatcher)

		return err
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
	if requestID := RequestIDFromContext(ctx); requestID != "" {
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

// Event dispatching helpers

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

func dispatchEvent(dispatcher grpcevents.EventDispatchFunc, event interface{}) {
	if dispatcher != nil {
		_ = dispatcher(event)
	}
}

func dispatchRequestStarted(ctx context.Context, method string, start time.Time, dispatcher grpcevents.EventDispatchFunc) {
	if dispatcher == nil {
		return
	}

	var md map[string][]string
	protocol := detectProtocol(ctx)
	if inMD, ok := metadata.FromIncomingContext(ctx); ok {
		md = redactMetadata(inMD)
	}

	dispatchEvent(dispatcher, &grpcevents.RequestStarted{
		Method:    method,
		Protocol:  protocol,
		StartTime: start,
		Context:   ctx,
		Metadata:  md,
	})
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
			if len(agent) > 0 && (agent == "grpc-go" || agent[:4] != "grpc") {
				// Non-grpc user agent likely means HTTP
				return grpcevents.ProtocolHTTP
			}
		}
	}

	return grpcevents.ProtocolGRPC
}

func dispatchRequestCompleted(ctx context.Context, method string, start time.Time, err error, dispatcher grpcevents.EventDispatchFunc) {
	if dispatcher == nil {
		return
	}

	end := time.Now()
	duration := end.Sub(start)
	protocol := detectProtocol(ctx)

	code := codes.OK
	if err != nil {
		if s, ok := status.FromError(err); ok {
			code = s.Code()
		} else {
			code = codes.Unknown
		}
	}

	var userID, teamID uint
	if claims := ClaimsFromContext(ctx); claims != nil {
		userID = claims.GetUserID()
		teamID = claims.GetTeamID()
	}

	if err != nil {
		dispatchEvent(dispatcher, &grpcevents.RequestFailed{
			Method:     method,
			Protocol:   protocol,
			StartTime:  start,
			EndTime:    end,
			Duration:   duration,
			StatusCode: code,
			Error:      err,
			Context:    ctx,
			UserID:     userID,
			TeamID:     teamID,
		})
	} else {
		dispatchEvent(dispatcher, &grpcevents.RequestCompleted{
			Method:     method,
			Protocol:   protocol,
			StartTime:  start,
			EndTime:    end,
			Duration:   duration,
			StatusCode: code,
			Context:    ctx,
			UserID:     userID,
			TeamID:     teamID,
		})
	}
}

func dispatchStreamStarted(ctx context.Context, method string, start time.Time, dispatcher grpcevents.EventDispatchFunc) {
	if dispatcher == nil {
		return
	}

	var md map[string][]string
	protocol := detectProtocol(ctx)
	if inMD, ok := metadata.FromIncomingContext(ctx); ok {
		md = redactMetadata(inMD)
	}

	dispatchEvent(dispatcher, &grpcevents.StreamStarted{
		Method:    method,
		Protocol:  protocol,
		StartTime: start,
		Context:   ctx,
		Metadata:  md,
	})
}

func dispatchStreamCompleted(ctx context.Context, method string, start time.Time, err error, dispatcher grpcevents.EventDispatchFunc) {
	if dispatcher == nil {
		return
	}

	end := time.Now()
	duration := end.Sub(start)
	protocol := detectProtocol(ctx)

	var userID, teamID uint
	if claims := ClaimsFromContext(ctx); claims != nil {
		userID = claims.GetUserID()
		teamID = claims.GetTeamID()
	}

	if err != nil {
		dispatchEvent(dispatcher, &grpcevents.StreamFailed{
			Method:    method,
			Protocol:  protocol,
			StartTime: start,
			EndTime:   end,
			Duration:  duration,
			Error:     err,
			Context:   ctx,
			UserID:    userID,
			TeamID:    teamID,
		})
	} else {
		dispatchEvent(dispatcher, &grpcevents.StreamCompleted{
			Method:    method,
			Protocol:  protocol,
			StartTime: start,
			EndTime:   end,
			Duration:  duration,
			Context:   ctx,
			UserID:    userID,
			TeamID:    teamID,
		})
	}
}

// Additional context helpers for logging

const requestIDKey contextKey = "grpc_request_id"

// ContextWithRequestID adds a request ID to the context
func ContextWithRequestID(ctx context.Context, requestID string) context.Context {
	return context.WithValue(ctx, requestIDKey, requestID)
}

// RequestIDFromContext extracts the request ID from the context.
// Returns empty string if no request ID is present.
func RequestIDFromContext(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}
