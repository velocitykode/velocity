package router

import (
	"context"
	"time"
)

// Event is the minimum contract for events emitted by the router.
// Every router-owned event type (RequestStarted, RequestRouted,
// RequestHandled, RequestFailed) implements Name() to return a stable
// dot-separated identifier suitable for logging, metrics, and
// dispatcher routing.
//
// Exposing a typed interface (rather than interface{}) makes the
// OnEventDispatchError callback surface self-describing and removes
// a silent class of mis-wired listener signatures. External callers
// are still free to pass any value through SetEventDispatcher — the
// router only emits values implementing Event.
type Event interface {
	Name() string
}

// RequestStarted is dispatched when an HTTP request begins processing
type RequestStarted struct {
	Context    context.Context
	Method     string
	Path       string
	RemoteAddr string
	UserAgent  string
	RequestID  string
	StartedAt  time.Time
	TraceID    string // APM trace ID
	SpanID     string // APM span ID
	ParentID   string // Parent span ID for correlation
}

// Name returns the event name
func (e *RequestStarted) Name() string {
	return "request.started"
}

// RequestRouted is dispatched after route matching completes
type RequestRouted struct {
	Context   context.Context
	RequestID string
	Route     string            // Route pattern e.g. "/users/{id}"
	RouteName string            // Named route if any
	Params    map[string]string // Route parameters
	Matched   bool              // false for 404
}

// Name returns the event name
func (e *RequestRouted) Name() string {
	return "request.routed"
}

// RequestHandled is dispatched when an HTTP request completes successfully
type RequestHandled struct {
	Context      context.Context
	RequestID    string
	Method       string
	Path         string
	Route        string // Route pattern that was matched
	StatusCode   int
	BytesWritten int64
	Duration     time.Duration
	TraceID      string // APM trace ID
	SpanID       string // APM span ID
	ParentID     string // Parent span ID for correlation
}

// Name returns the event name
func (e *RequestHandled) Name() string {
	return "request.handled"
}

// RequestFailed is dispatched when an HTTP request fails, decided once the
// router's error boundary has answered the error: a recovered panic
// (including one the Timeout middleware forwarded, and one its handler
// goroutine recovered after the 503 went out, which dispatches this event
// after the request's RequestHandled), an answer to the error with status
// 500 or above, or an error that names no status below 500 when nothing
// answered it. The answer is the response the boundary wrote, or the one
// a middleware wrote before returning contract.Handled; its status
// decides, not the error as the handler returned it, so an error the
// installed error handler answers below 500 (a not-found sentinel it maps
// to 404, an application map rule) is a response, not a failure, and
// dispatches nothing. A response the handler committed itself before
// returning a plain error is not an answer to it: the error decides, as
// when nothing was written. A contract.Handled value dispatches its cause
// under the same rule, and a bare contract.ErrResponseWritten dispatches
// nothing, except inside the value of a recovered panic, which always
// dispatches.
type RequestFailed struct {
	Context   context.Context
	RequestID string
	Method    string
	Path      string
	Error     error
	Stack     string // Stack trace if panic recovered
	Recovered bool   // true if recovered from panic
	TraceID   string // APM trace ID
	SpanID    string // APM span ID
	ParentID  string // Parent span ID for correlation
}

// Name returns the event name
func (e *RequestFailed) Name() string {
	return "request.failed"
}
