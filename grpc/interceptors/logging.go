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
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/internal/latency"
	"github.com/velocitykode/velocity/internal/panicerr"
	"github.com/velocitykode/velocity/trace"
)

// shouldSkip reports whether method gets no request line and no
// lifecycle events under cfg.
func shouldSkip(method string, cfg *CallConfig) bool {
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

// logRequest writes the call's request line through cfg.Logger (the
// fallback when none is set), bound to the request, trace and span ids of
// ctx, the call's effective context (trace.LogFields). The user fields
// come from claims, the call's claims snapshot, and ExtraFields is read
// from user (see call.userContext). A panicking ExtraFields is contained
// and its fields omitted; a panicking logger is contained as well (see
// eventemit.WriteLine).
func logRequest(ctx, user context.Context, claims *callClaims, method string, start time.Time, err error, cfg *CallConfig) {
	duration := time.Since(start)
	code := statusCodeOf(err)
	fields := []interface{}{
		"method", method,
		"code", code.String(),
		latency.Key, latency.Millis(duration),
	}
	if claims.present {
		fields = append(fields,
			"user_id", claims.userID,
			"team_id", claims.teamID,
		)
	}
	fields = append(fields, extraFields(user, cfg)...)
	eventemit.WriteLine(ctx, cfg.Logger, func(logger contract.Logger) {
		switch {
		case err != nil && code != codes.Canceled && code != codes.NotFound:
			if code == codes.Internal || code == codes.Unknown {
				logger.Error("gRPC request", fields...)
			} else {
				logger.Warn("gRPC request", fields...)
			}
		case latency.Slow(duration, cfg.SlowThreshold):
			logger.Warn("gRPC request (slow)", fields...)
		default:
			logger.Info("gRPC request", fields...)
		}
	})
}

// extraFields returns cfg.ExtraFields(user), or none when it is unset or
// panics: a panicking ExtraFields only loses its own fields.
func extraFields(user context.Context, cfg *CallConfig) (fields []interface{}) {
	if cfg.ExtraFields == nil {
		return nil
	}
	defer func() {
		if recover() != nil {
			fields = nil
		}
	}()
	return cfg.ExtraFields(user)
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
