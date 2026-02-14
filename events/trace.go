package events

import (
	"context"

	"github.com/velocitykode/velocity/trace"
)

// Re-export trace functions for convenience.
// The actual implementations are in pkg/trace to avoid circular imports.

// GenerateTraceID generates a new random trace ID (32 hex characters).
func GenerateTraceID() string {
	return trace.GenerateTraceID()
}

// GenerateSpanID generates a new random span ID (16 hex characters).
func GenerateSpanID() string {
	return trace.GenerateSpanID()
}

// WithTrace returns a new context with the given trace ID and span ID.
func WithTrace(ctx context.Context, traceID, spanID string) context.Context {
	return trace.WithTrace(ctx, traceID, spanID)
}

// WithSpan returns a new context with a new span ID, preserving the trace ID.
func WithSpan(ctx context.Context, spanID string) context.Context {
	return trace.WithSpan(ctx, spanID)
}

// WithNewSpan creates a new span ID and updates the context.
func WithNewSpan(ctx context.Context) (context.Context, string) {
	return trace.WithNewSpan(ctx)
}

// GetTraceID returns the trace ID from the context.
func GetTraceID(ctx context.Context) string {
	return trace.GetTraceID(ctx)
}

// GetSpanID returns the span ID from the context.
func GetSpanID(ctx context.Context) string {
	return trace.GetSpanID(ctx)
}

// GetParentID returns the parent span ID from the context.
func GetParentID(ctx context.Context) string {
	return trace.GetParentID(ctx)
}

// GetTraceContext extracts all trace context values from the context.
func GetTraceContext(ctx context.Context) (traceID, spanID, parentID string) {
	return trace.GetTraceContext(ctx)
}

// StartTrace creates a new trace context with fresh trace and span IDs.
func StartTrace(ctx context.Context) (context.Context, string, string) {
	return trace.StartTrace(ctx)
}

// ContinueTrace continues an existing trace with a new span.
func ContinueTrace(ctx context.Context) (context.Context, string) {
	return trace.ContinueTrace(ctx)
}
