// Package grpcevents provides event types for gRPC operations.
// This package is separate to avoid import cycles between grpc and interceptors.
package grpcevents

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
)

// Protocol indicates how the request was received
type Protocol string

const (
	// ProtocolGRPC indicates a direct gRPC request
	ProtocolGRPC Protocol = "grpc"
	// ProtocolHTTP indicates a request via HTTP gateway (grpc-gateway)
	ProtocolHTTP Protocol = "http"
)

// EventDispatchFunc is a function type for dispatching events.
type EventDispatchFunc func(event interface{}) error

// RequestStarted is dispatched when a gRPC request begins
type RequestStarted struct {
	Method    string
	Protocol  Protocol // "grpc" or "http"
	StartTime time.Time
	Context   context.Context
	Metadata  map[string][]string
}

// Name returns the event name
func (e *RequestStarted) Name() string {
	return "grpc.request.started"
}

// RequestCompleted is dispatched when a gRPC request completes successfully
type RequestCompleted struct {
	Method     string
	Protocol   Protocol // "grpc" or "http"
	StartTime  time.Time
	EndTime    time.Time
	Duration   time.Duration
	StatusCode codes.Code
	Context    context.Context
	UserID     uint
	TeamID     uint
}

// Name returns the event name
func (e *RequestCompleted) Name() string {
	return "grpc.request.completed"
}

// RequestFailed is dispatched when a gRPC request fails
type RequestFailed struct {
	Method     string
	Protocol   Protocol // "grpc" or "http"
	StartTime  time.Time
	EndTime    time.Time
	Duration   time.Duration
	StatusCode codes.Code
	Error      error
	Context    context.Context
	UserID     uint
	TeamID     uint
}

// Name returns the event name
func (e *RequestFailed) Name() string {
	return "grpc.request.failed"
}

// StreamStarted is dispatched when a gRPC stream begins
type StreamStarted struct {
	Method    string
	Protocol  Protocol // "grpc" or "http"
	StartTime time.Time
	Context   context.Context
	Metadata  map[string][]string
}

// Name returns the event name
func (e *StreamStarted) Name() string {
	return "grpc.stream.started"
}

// StreamCompleted is dispatched when a gRPC stream completes
type StreamCompleted struct {
	Method       string
	Protocol     Protocol // "grpc" or "http"
	StartTime    time.Time
	EndTime      time.Time
	Duration     time.Duration
	MessagesSent int
	MessagesRecv int
	Context      context.Context
	UserID       uint
	TeamID       uint
}

// Name returns the event name
func (e *StreamCompleted) Name() string {
	return "grpc.stream.completed"
}

// StreamFailed is dispatched when a gRPC stream fails
type StreamFailed struct {
	Method       string
	Protocol     Protocol // "grpc" or "http"
	StartTime    time.Time
	EndTime      time.Time
	Duration     time.Duration
	Error        error
	MessagesSent int
	MessagesRecv int
	Context      context.Context
	UserID       uint
	TeamID       uint
}

// Name returns the event name
func (e *StreamFailed) Name() string {
	return "grpc.stream.failed"
}

// ServerStarted is dispatched when the gRPC server starts
type ServerStarted struct {
	Port      string
	StartTime time.Time
}

// Name returns the event name
func (e *ServerStarted) Name() string {
	return "grpc.server.started"
}

// ServerStopped is dispatched when the gRPC server stops
type ServerStopped struct {
	Port     string
	StopTime time.Time
	Duration time.Duration // Total server uptime
}

// Name returns the event name
func (e *ServerStopped) Name() string {
	return "grpc.server.stopped"
}

// GatewayStarted is dispatched when the HTTP gateway starts
type GatewayStarted struct {
	Port         string
	GRPCEndpoint string
	StartTime    time.Time
}

// Name returns the event name
func (e *GatewayStarted) Name() string {
	return "grpc.gateway.started"
}

// GatewayStopped is dispatched when the HTTP gateway stops
type GatewayStopped struct {
	Port     string
	StopTime time.Time
	Duration time.Duration
}

// Name returns the event name
func (e *GatewayStopped) Name() string {
	return "grpc.gateway.stopped"
}

// PanicRecovered is dispatched when a panic is recovered in a gRPC handler
type PanicRecovered struct {
	Method     string
	Panic      interface{}
	StackTrace string
	Time       time.Time
	Context    context.Context
}

// Name returns the event name
func (e *PanicRecovered) Name() string {
	return "grpc.panic.recovered"
}

// AuthFailed is dispatched when authentication fails
type AuthFailed struct {
	Method  string
	Token   string // Masked token (first/last few chars)
	Reason  string
	Time    time.Time
	Context context.Context
}

// Name returns the event name
func (e *AuthFailed) Name() string {
	return "grpc.auth.failed"
}
