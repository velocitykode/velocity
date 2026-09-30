// Package grpcevents provides event types for gRPC operations.
// This package is separate to avoid import cycles between grpc and interceptors.
package grpcevents

import (
	"context"
	"encoding/json"
	"time"

	"google.golang.org/grpc/codes"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/eventmeta"
)

// Protocol indicates how the request was received
type Protocol string

const (
	// ProtocolGRPC indicates a direct gRPC request
	ProtocolGRPC Protocol = "grpc"
	// ProtocolHTTP indicates a request via HTTP gateway (grpc-gateway)
	ProtocolHTTP Protocol = "http"
)

// EventDispatchFunc is a function type for dispatching events. The ctx is
// the request (or server-lifecycle) context in scope at the emission site,
// matching the dispatcher signature used across the framework.
type EventDispatchFunc func(ctx context.Context, event any) error

// RequestStarted is dispatched when a gRPC request begins. Its At is when
// the call arrived.
type RequestStarted struct {
	contract.EventMeta
	Method   string
	Protocol Protocol // "grpc" or "http"
	Metadata map[string][]string
}

// Name returns the event name
func (e *RequestStarted) Name() string {
	return "grpc.request.started"
}

// RequestCompleted is the terminal event of every gRPC request, whatever
// the outcome: it records the status the call ended with. A request that
// failed dispatches RequestFailed first, then RequestCompleted, as the
// router does for HTTP. Its At is when the call ended and Duration how
// long it took.
type RequestCompleted struct {
	contract.EventMeta
	Method     string
	Protocol   Protocol // "grpc" or "http"
	Duration   time.Duration
	StatusCode codes.Code
	UserID     uint
	TeamID     uint
}

// Name returns the event name
func (e *RequestCompleted) Name() string {
	return "grpc.request.completed"
}

// RequestFailed is dispatched when a gRPC request's handler returns an
// error, before the request's RequestCompleted.
type RequestFailed struct {
	contract.EventMeta
	Method     string
	Protocol   Protocol // "grpc" or "http"
	Duration   time.Duration
	StatusCode codes.Code
	Err        error
	UserID     uint
	TeamID     uint
}

// Name returns the event name
func (e *RequestFailed) Name() string {
	return "grpc.request.failed"
}

// MarshalJSON encodes the event with Err as its text.
func (e RequestFailed) MarshalJSON() ([]byte, error) {
	type fields RequestFailed
	return json.Marshal(struct {
		fields
		Err string `json:",omitempty"`
	}{fields(e), eventmeta.ErrorText(e.Err)})
}

// UnmarshalJSON decodes the event's JSON form: Err becomes an error with
// the encoded text.
func (e *RequestFailed) UnmarshalJSON(data []byte) error {
	type fields RequestFailed
	v := struct {
		*fields
		Err string `json:",omitempty"`
	}{fields: (*fields)(e)}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.Err = eventmeta.TextError(v.Err)
	return nil
}

// StreamStarted is dispatched when a gRPC stream begins. Its At is when the
// stream opened.
type StreamStarted struct {
	contract.EventMeta
	Method   string
	Protocol Protocol // "grpc" or "http"
	Metadata map[string][]string
}

// Name returns the event name
func (e *StreamStarted) Name() string {
	return "grpc.stream.started"
}

// StreamCompleted is the terminal event of every gRPC stream, whatever the
// outcome. A stream that failed dispatches StreamFailed first, then
// StreamCompleted. Its At is when the stream ended and Duration how long it
// was open.
type StreamCompleted struct {
	contract.EventMeta
	Method       string
	Protocol     Protocol // "grpc" or "http"
	Duration     time.Duration
	StatusCode   codes.Code
	MessagesSent int
	MessagesRecv int
	UserID       uint
	TeamID       uint
}

// Name returns the event name
func (e *StreamCompleted) Name() string {
	return "grpc.stream.completed"
}

// StreamFailed is dispatched when a gRPC stream's handler returns an error,
// before the stream's StreamCompleted.
type StreamFailed struct {
	contract.EventMeta
	Method       string
	Protocol     Protocol // "grpc" or "http"
	Duration     time.Duration
	StatusCode   codes.Code
	Err          error
	MessagesSent int
	MessagesRecv int
	UserID       uint
	TeamID       uint
}

// Name returns the event name
func (e *StreamFailed) Name() string {
	return "grpc.stream.failed"
}

// MarshalJSON encodes the event with Err as its text.
func (e StreamFailed) MarshalJSON() ([]byte, error) {
	type fields StreamFailed
	return json.Marshal(struct {
		fields
		Err string `json:",omitempty"`
	}{fields(e), eventmeta.ErrorText(e.Err)})
}

// UnmarshalJSON decodes the event's JSON form: Err becomes an error with
// the encoded text.
func (e *StreamFailed) UnmarshalJSON(data []byte) error {
	type fields StreamFailed
	v := struct {
		*fields
		Err string `json:",omitempty"`
	}{fields: (*fields)(e)}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.Err = eventmeta.TextError(v.Err)
	return nil
}

// ServerStarted is dispatched when the gRPC server starts. Its At is when
// it started.
type ServerStarted struct {
	contract.EventMeta
	Port string
}

// Name returns the event name
func (e *ServerStarted) Name() string {
	return "grpc.server.started"
}

// ServerStopped is dispatched when the gRPC server stops. Its At is when it
// stopped and Duration how long it had been up.
type ServerStopped struct {
	contract.EventMeta
	Port     string
	Duration time.Duration // Total server uptime
}

// Name returns the event name
func (e *ServerStopped) Name() string {
	return "grpc.server.stopped"
}

// GatewayStarted is dispatched when the HTTP gateway starts. Its At is when
// it started.
type GatewayStarted struct {
	contract.EventMeta
	Port         string
	GRPCEndpoint string
}

// Name returns the event name
func (e *GatewayStarted) Name() string {
	return "grpc.gateway.started"
}

// GatewayStopped is dispatched when the HTTP gateway stops. Its At is when
// it stopped and Duration how long it had been up.
type GatewayStopped struct {
	contract.EventMeta
	Port     string
	Duration time.Duration
}

// Name returns the event name
func (e *GatewayStopped) Name() string {
	return "grpc.gateway.stopped"
}

// PanicRecovered is dispatched when a panic is recovered in a gRPC handler.
// PanicValue is the panic's text ("panic: " and the recovered value's
// text), a diagnostic that survives the queue's JSON codec unchanged; the
// recovered value itself does not cross the event.
type PanicRecovered struct {
	contract.EventMeta
	Method     string
	PanicValue string
	StackTrace string
}

// Name returns the event name
func (e *PanicRecovered) Name() string {
	return "grpc.panic.recovered"
}

// AuthFailed is dispatched when authentication fails. Err is the failure
// the authenticator returned.
type AuthFailed struct {
	contract.EventMeta
	Method string
	Token  string // Masked token (first/last few chars)
	Err    error
}

// Name returns the event name
func (e *AuthFailed) Name() string {
	return "grpc.auth.failed"
}

// MarshalJSON encodes the event with Err as its text.
func (e AuthFailed) MarshalJSON() ([]byte, error) {
	type fields AuthFailed
	return json.Marshal(struct {
		fields
		Err string `json:",omitempty"`
	}{fields(e), eventmeta.ErrorText(e.Err)})
}

// UnmarshalJSON decodes the event's JSON form: Err becomes an error with
// the encoded text.
func (e *AuthFailed) UnmarshalJSON(data []byte) error {
	type fields AuthFailed
	v := struct {
		*fields
		Err string `json:",omitempty"`
	}{fields: (*fields)(e)}
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	e.Err = eventmeta.TextError(v.Err)
	return nil
}
