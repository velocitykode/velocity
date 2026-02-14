// Package grpc provides a fluent API for building gRPC servers and HTTP gateways.
//
// The package follows Velocity's patterns:
//   - Interface-first design with pluggable auth validators
//   - Global singleton with GetServer() function
//   - Auto-init from environment variables
//   - Builder pattern for configuration
//
// Basic usage:
//
//	server := grpc.NewServer(
//	    grpc.WithPort("50051"),
//	    grpc.WithReflection(true),
//	)
//	server.Use(grpc.RecoveryInterceptor(), grpc.LoggingInterceptor())
//	server.RegisterService(&myService{})
//	server.Start()
package grpc

import (
	"errors"
)

// Common errors
var (
	ErrServerNotInitialized  = errors.New("grpc server not initialized")
	ErrGatewayNotInitialized = errors.New("grpc gateway not initialized")
	ErrInvalidPort           = errors.New("invalid port")
	ErrServerAlreadyRunning  = errors.New("server already running")
	ErrNoEndpoint            = errors.New("no grpc endpoint configured for gateway")
)

// Service is an interface that all gRPC services should implement
// for registration with the server.
type Service interface {
	// ServiceDesc returns the gRPC ServiceDesc for registration
	ServiceDesc() ServiceDescriptor
}

// ServiceDescriptor wraps the information needed to register a service
type ServiceDescriptor struct {
	// ServiceName is the full name of the service (e.g., "mypackage.MyService")
	ServiceName string
	// HandlerType is the service interface type
	HandlerType interface{}
	// Handler is the actual service implementation
	Handler interface{}
	// Methods are the service methods
	Methods []MethodDesc
	// Streams are the streaming methods
	Streams []StreamDesc
}

// MethodDesc describes a unary method
type MethodDesc struct {
	MethodName string
	Handler    interface{}
}

// StreamDesc describes a streaming method
type StreamDesc struct {
	StreamName    string
	Handler       interface{}
	ServerStreams bool
	ClientStreams bool
}

// RegistrationFunc is a function type for registering a service with a gRPC server.
// This matches the pattern used by generated protobuf code:
//
//	pb.RegisterMyServiceServer(grpcServer, &myServiceImpl{})
type RegistrationFunc func(server interface{})

// HandlerRegistrationFunc is a function type for registering HTTP handlers with a gateway.
// This matches the pattern used by grpc-gateway generated code:
//
//	pb.RegisterMyServiceHandlerFromEndpoint(ctx, mux, endpoint, opts)
type HandlerRegistrationFunc func(gateway interface{}) error
