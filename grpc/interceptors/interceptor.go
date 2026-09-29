// Package interceptors provides gRPC interceptors for Velocity applications.
//
// Interceptors are middleware for gRPC. CallLifecycle owns a call's
// observability: its correlation (span and request id), panic recovery,
// request line, lifecycle events and one error report. Auth authenticates
// calls, and Propagation carries the trace and request id on outgoing
// client calls.
//
// A framework-built server installs CallLifecycle at both ends of its chain by
// default (see grpc.WithCallOptions to configure it), so every call is
// observed under one span and request id, and a panic anywhere in the
// chain ends the call as an error:
//
//	server := grpc.NewServer(grpc.WithPort("50051"), grpc.WithReporter(reporter))
//	server.UseAll(interceptors.Auth(validator))
//
// A chain built on a bare grpc-go server installs the same CallLifecycle pair
// first and last:
//
//	calls := interceptors.CallLifecycle(interceptors.WithReporter(reporter))
//	auth := interceptors.Auth(validator)
//	grpc.NewServer(
//	    grpc.ChainUnaryInterceptor(calls.Unary, auth.Unary, calls.Unary),
//	    grpc.ChainStreamInterceptor(calls.Stream, auth.Stream, calls.Stream),
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

	// IsAuth marks the pair as an authentication interceptor. The Auth
	// constructor sets it so the server can detect, at Build time, whether
	// authentication has been wired and warn when an entire service surface
	// would otherwise be served unauthenticated. Plain (non-auth) pairs leave
	// it false.
	IsAuth bool
}

// Interceptor is a function that returns an InterceptorPair.
// This allows for lazy initialization and configuration.
type Interceptor func() InterceptorPair
