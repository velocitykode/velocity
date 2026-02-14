package grpc

import (
	"github.com/velocitykode/velocity/grpc/grpcevents"
)

// Protocol type re-export
type Protocol = grpcevents.Protocol

// Protocol constants
const (
	ProtocolGRPC = grpcevents.ProtocolGRPC
	ProtocolHTTP = grpcevents.ProtocolHTTP
)

// Re-export event types from grpcevents package for convenience
type (
	RequestStarted   = grpcevents.RequestStarted
	RequestCompleted = grpcevents.RequestCompleted
	RequestFailed    = grpcevents.RequestFailed
	StreamStarted    = grpcevents.StreamStarted
	StreamCompleted  = grpcevents.StreamCompleted
	StreamFailed     = grpcevents.StreamFailed
	ServerStarted    = grpcevents.ServerStarted
	ServerStopped    = grpcevents.ServerStopped
	GatewayStarted   = grpcevents.GatewayStarted
	GatewayStopped   = grpcevents.GatewayStopped
	PanicRecovered   = grpcevents.PanicRecovered
	AuthFailed       = grpcevents.AuthFailed
)
