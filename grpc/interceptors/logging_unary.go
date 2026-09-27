package interceptors

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/grpc/grpcevents"
	"github.com/velocitykode/velocity/internal/eventmeta"
)

// LoggingInterceptor creates a unary logging interceptor.
// This is a convenience function for when you only need the unary interceptor.
func LoggingInterceptor(opts ...LoggingOption) grpc.UnaryServerInterceptor {
	return Logging(opts...).Unary
}

func loggingUnary(cfg *LoggingConfig) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req interface{}, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (interface{}, error) {
		// Check if we should skip this method
		if shouldSkip(info.FullMethod, cfg) {
			return handler(ctx, req)
		}

		ctx = correlate(ctx)
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

func dispatchRequestStarted(ctx context.Context, method string, start time.Time, dispatcher grpcevents.EventDispatchFunc) {
	if dispatcher == nil {
		return
	}

	var md map[string][]string
	protocol := detectProtocol(ctx)
	if inMD, ok := metadata.FromIncomingContext(ctx); ok {
		md = redactMetadata(inMD)
	}

	meta := eventmeta.Current(ctx)
	meta.At = start
	dispatchEvent(ctx, dispatcher, &grpcevents.RequestStarted{
		EventMeta: meta,
		Method:    method,
		Protocol:  protocol,
		Metadata:  md,
	})
}

// statusCodeOf returns the gRPC status code a handler's error ends the call
// with: OK for nil, Unknown for an error that carries no status.
func statusCodeOf(err error) codes.Code {
	if err == nil {
		return codes.OK
	}
	if s, ok := status.FromError(err); ok {
		return s.Code()
	}
	return codes.Unknown
}

// dispatchRequestCompleted dispatches the end of a unary call:
// RequestFailed when the handler returned an error, RequestCompleted
// otherwise.
func dispatchRequestCompleted(ctx context.Context, method string, start time.Time, err error, dispatcher grpcevents.EventDispatchFunc) {
	if dispatcher == nil {
		return
	}

	meta := eventmeta.Current(ctx)
	duration := meta.At.Sub(start)
	protocol := detectProtocol(ctx)

	code := statusCodeOf(err)

	var userID, teamID uint
	if claims := ClaimsFromContext(ctx); claims != nil {
		userID = claims.GetUserID()
		teamID = claims.GetTeamID()
	}

	if err != nil {
		dispatchEvent(ctx, dispatcher, &grpcevents.RequestFailed{
			EventMeta:  meta,
			Method:     method,
			Protocol:   protocol,
			Duration:   duration,
			StatusCode: code,
			Err:        err,
			UserID:     userID,
			TeamID:     teamID,
		})
		return
	}
	dispatchEvent(ctx, dispatcher, &grpcevents.RequestCompleted{
		EventMeta:  meta,
		Method:     method,
		Protocol:   protocol,
		Duration:   duration,
		StatusCode: code,
		UserID:     userID,
		TeamID:     teamID,
	})
}
