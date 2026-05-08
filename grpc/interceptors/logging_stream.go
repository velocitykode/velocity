package interceptors

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/trace"
)

// tracedServerStream forwards every ServerStream method to the wrapped
// stream and overrides Context() so handlers (and any nested grpc layer
// that walks back to the outermost stream) see the trace-stamped ctx
// minted by the logging interceptor. Methods are listed explicitly rather
// than embedded so a future ServerStream addition is a compile-time miss
// rather than a silent fallthrough that bypasses the minted ctx.
type tracedServerStream struct {
	inner grpc.ServerStream
	ctx   context.Context
}

func (s *tracedServerStream) Context() context.Context        { return s.ctx }
func (s *tracedServerStream) SetHeader(md metadata.MD) error  { return s.inner.SetHeader(md) }
func (s *tracedServerStream) SendHeader(md metadata.MD) error { return s.inner.SendHeader(md) }
func (s *tracedServerStream) SetTrailer(md metadata.MD)       { s.inner.SetTrailer(md) }
func (s *tracedServerStream) SendMsg(m interface{}) error     { return s.inner.SendMsg(m) }
func (s *tracedServerStream) RecvMsg(m interface{}) error     { return s.inner.RecvMsg(m) }

// StreamLoggingInterceptor creates a stream logging interceptor.
// This is a convenience function for when you only need the stream interceptor.
func StreamLoggingInterceptor(opts ...LoggingOption) grpc.StreamServerInterceptor {
	return Logging(opts...).Stream
}

func loggingStream(cfg *LoggingConfig) grpc.StreamServerInterceptor {
	return func(srv interface{}, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		// Check if we should skip this method
		if shouldSkip(info.FullMethod, cfg) {
			return handler(srv, ss)
		}

		ctx := ensureTrace(ss.Context())
		wrapped := &tracedServerStream{inner: ss, ctx: ctx}
		start := time.Now()

		// Dispatch stream started event
		dispatchStreamStarted(ctx, info.FullMethod, start, cfg.EventDispatcher)

		// Call handler
		err := handler(srv, wrapped)

		// Log the request
		logRequest(ctx, info.FullMethod, start, err, cfg)

		// Dispatch stream completion event
		dispatchStreamCompleted(ctx, info.FullMethod, start, err, cfg.EventDispatcher)

		return err
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

	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	dispatchEvent(dispatcher, &grpcevents.StreamStarted{
		Method:    method,
		Protocol:  protocol,
		StartTime: start,
		Context:   ctx,
		Metadata:  md,
		TraceID:   traceID,
		SpanID:    spanID,
		ParentID:  parentID,
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

	traceID, spanID, parentID := trace.GetTraceContext(ctx)
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
			TraceID:   traceID,
			SpanID:    spanID,
			ParentID:  parentID,
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
			TraceID:   traceID,
			SpanID:    spanID,
			ParentID:  parentID,
		})
	}
}
