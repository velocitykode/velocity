package interceptors

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/velocitykode/velocity/grpc/grpcevents"
)

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
