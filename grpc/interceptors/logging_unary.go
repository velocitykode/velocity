package interceptors

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/grpc/grpcevents"
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

	dispatchEvent(dispatcher, &grpcevents.RequestStarted{
		Method:    method,
		Protocol:  protocol,
		StartTime: start,
		Context:   ctx,
		Metadata:  md,
	})
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
