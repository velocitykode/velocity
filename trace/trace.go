// Package trace provides distributed tracing context helpers for APM instrumentation.
package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
)

// Context keys for trace information
type contextKey string

const (
	traceIDKey  contextKey = "velocity_trace_id"
	spanIDKey   contextKey = "velocity_span_id"
	parentIDKey contextKey = "velocity_parent_id"
)

// GenerateTraceID generates a new random trace ID (32 hex characters).
// A trace ID represents a single distributed trace across multiple services.
func GenerateTraceID() string {
	return generateHexID(16)
}

// GenerateSpanID generates a new random span ID (16 hex characters).
// A span ID represents a single operation within a trace.
func GenerateSpanID() string {
	return generateHexID(8)
}

// generateHexID generates a random hex string of the given byte length.
func generateHexID(byteLength int) string {
	b := make([]byte, byteLength)
	_, err := rand.Read(b)
	if err != nil {
		// Fallback to zeros if crypto/rand fails (extremely unlikely)
		return hex.EncodeToString(make([]byte, byteLength))
	}
	return hex.EncodeToString(b)
}

// WithTrace returns a new context with the given trace ID and span ID.
// This is typically called at the start of a request to establish the trace context.
func WithTrace(ctx context.Context, traceID, spanID string) context.Context {
	ctx = context.WithValue(ctx, traceIDKey, traceID)
	ctx = context.WithValue(ctx, spanIDKey, spanID)
	return ctx
}

// WithSpan returns a new context with a new span ID, preserving the trace ID.
// The current span ID becomes the parent ID for child span correlation.
func WithSpan(ctx context.Context, spanID string) context.Context {
	// Current span becomes parent
	if currentSpan := GetSpanID(ctx); currentSpan != "" {
		ctx = context.WithValue(ctx, parentIDKey, currentSpan)
	}
	return context.WithValue(ctx, spanIDKey, spanID)
}

// WithNewSpan creates a new span ID and updates the context.
// Returns the new context and the generated span ID.
func WithNewSpan(ctx context.Context) (context.Context, string) {
	spanID := GenerateSpanID()
	return WithSpan(ctx, spanID), spanID
}

// GetTraceID returns the trace ID from the context, or empty string if not set.
func GetTraceID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if traceID, ok := ctx.Value(traceIDKey).(string); ok {
		return traceID
	}
	return ""
}

// GetSpanID returns the span ID from the context, or empty string if not set.
func GetSpanID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if spanID, ok := ctx.Value(spanIDKey).(string); ok {
		return spanID
	}
	return ""
}

// GetParentID returns the parent span ID from the context, or empty string if not set.
func GetParentID(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	if parentID, ok := ctx.Value(parentIDKey).(string); ok {
		return parentID
	}
	return ""
}

// GetTraceContext extracts all trace context values from the context.
// Returns traceID, spanID, and parentID.
func GetTraceContext(ctx context.Context) (traceID, spanID, parentID string) {
	return GetTraceID(ctx), GetSpanID(ctx), GetParentID(ctx)
}

// StartTrace creates a new trace context with fresh trace and span IDs.
// Returns the new context, trace ID, and span ID.
func StartTrace(ctx context.Context) (context.Context, string, string) {
	traceID := GenerateTraceID()
	spanID := GenerateSpanID()
	return WithTrace(ctx, traceID, spanID), traceID, spanID
}

// ContinueTrace continues an existing trace with a new span.
// If the context has no trace ID, creates a new trace.
// Returns the updated context and the new span ID.
func ContinueTrace(ctx context.Context) (context.Context, string) {
	if GetTraceID(ctx) == "" {
		ctx, _, spanID := StartTrace(ctx)
		return ctx, spanID
	}
	return WithNewSpan(ctx)
}
