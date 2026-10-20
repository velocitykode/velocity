// Package grpcconfig holds the gRPC server and gateway configuration type.
// It imports only the standard library, so the root velocity package can
// name it in velocity.Config without linking grpc-go into every app; the
// grpc package re-exports it as grpc.Config.
package grpcconfig
