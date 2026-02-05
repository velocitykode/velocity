// Package interceptors provides gRPC interceptors for Velocity applications.
//
// Interceptors are middleware for gRPC that can:
//   - Log requests and responses
//   - Handle authentication
//   - Recover from panics
//   - Add request IDs
//   - Rate limit requests
//
// Usage:
//
//	server := grpc.NewServer(grpc.WithPort("50051"))
//	server.Use(
//	    interceptors.RecoveryInterceptor(),
//	    interceptors.LoggingInterceptor(),
//	)
//	server.UseStream(
//	    interceptors.StreamRecoveryInterceptor(),
//	    interceptors.StreamLoggingInterceptor(),
//	)
//
// Or using UseAll with pairs:
//
//	server.UseAll(
//	    interceptors.Recovery(),
//	    interceptors.Logging(),
//	)
package interceptors

import (
	"google.golang.org/grpc"
)

// InterceptorPair holds both unary and stream interceptor variants.
// Many interceptors need both variants, so this groups them together.
type InterceptorPair struct {
	Unary  grpc.UnaryServerInterceptor
	Stream grpc.StreamServerInterceptor
}

// Interceptor is a function that returns an InterceptorPair.
// This allows for lazy initialization and configuration.
type Interceptor func() InterceptorPair
