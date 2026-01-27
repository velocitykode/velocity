package router

import (
	"context"
	"time"
)

// RequestStarted is dispatched when an HTTP request begins processing
type RequestStarted struct {
	Context    context.Context
	Method     string
	Path       string
	RemoteAddr string
	UserAgent  string
	RequestID  string
	StartedAt  time.Time
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
}

// Name returns the event name
func (e *RequestHandled) Name() string {
	return "request.handled"
}

// RequestFailed is dispatched when an HTTP request fails with an error or panic
type RequestFailed struct {
	Context   context.Context
	RequestID string
	Method    string
	Path      string
	Error     error
	Stack     string // Stack trace if panic recovered
	Recovered bool   // true if recovered from panic
}

// Name returns the event name
func (e *RequestFailed) Name() string {
	return "request.failed"
}
