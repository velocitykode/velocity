package httpclient

import (
	"context"
	"time"

	"github.com/velocitykode/velocity/trace"
)

// RequestSent is dispatched after an HTTP request completes successfully
type RequestSent struct {
	Context      context.Context
	Method       string
	URL          string
	StatusCode   int
	DurationMs   int64
	RequestSize  int64
	ResponseSize int64
	TraceID      string
	SpanID       string
	ParentID     string
}

// Name returns the event name
func (e *RequestSent) Name() string {
	return "http.request.sent"
}

// RequestFailed is dispatched when an HTTP request fails
type RequestFailed struct {
	Context    context.Context
	Method     string
	URL        string
	Error      string
	DurationMs int64
	TraceID    string
	SpanID     string
	ParentID   string
}

// Name returns the event name
func (e *RequestFailed) Name() string {
	return "http.request.failed"
}

// dispatchRequestSent dispatches a RequestSent event
func (c *Client) dispatchRequestSent(ctx context.Context, method, url string, statusCode int, duration time.Duration, requestSize, responseSize int64) {
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	c.dispatchEvent(ctx, &RequestSent{
		Context:      ctx,
		Method:       method,
		URL:          url,
		StatusCode:   statusCode,
		DurationMs:   duration.Milliseconds(),
		RequestSize:  requestSize,
		ResponseSize: responseSize,
		TraceID:      traceID,
		SpanID:       spanID,
		ParentID:     parentID,
	})
}

// dispatchRequestFailed dispatches a RequestFailed event
func (c *Client) dispatchRequestFailed(ctx context.Context, method, url string, err error, duration time.Duration) {
	traceID, spanID, parentID := trace.GetTraceContext(ctx)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	c.dispatchEvent(ctx, &RequestFailed{
		Context:    ctx,
		Method:     method,
		URL:        url,
		Error:      errMsg,
		DurationMs: duration.Milliseconds(),
		TraceID:    traceID,
		SpanID:     spanID,
		ParentID:   parentID,
	})
}
