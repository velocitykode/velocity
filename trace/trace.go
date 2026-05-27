// Package trace provides distributed tracing context helpers for APM instrumentation.
package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log"
	"sync"
)

// Context keys for trace information
type contextKey string

const (
	traceIDKey  contextKey = "velocity_trace_id"
	spanIDKey   contextKey = "velocity_span_id"
	parentIDKey contextKey = "velocity_parent_id"
)

// FallbackTraceID is returned by MustGenerateTraceID when crypto/rand
// is unavailable even after a retry. It is intentionally NOT 32 hex
// characters so APM tooling cannot accidentally correlate the fallback
// with a real trace. The marker shape makes the failure mode obvious in
// any downstream system that indexes traces.
const FallbackTraceID = "velocity_trace_rand_unavailable"

// FallbackSpanID is the span-equivalent of FallbackTraceID. Same
// rationale: not 16 hex characters, intentionally distinguishable from
// any valid span.
const FallbackSpanID = "velocity_span_rand_unavailable"

// randReader is the entropy source used by the package. It is exposed as
// a package-level variable so tests can inject a faulty reader. Defaults
// to crypto/rand.Reader.
var randReader io.Reader = rand.Reader

// randFallbackWarnOnce guards the one-time WARN log emitted by the Must*
// helpers when crypto/rand is unavailable.
var randFallbackWarnOnce sync.Once

// GenerateTraceID generates a new random trace ID (32 hex characters).
// A trace ID represents a single distributed trace across multiple services.
// Returns an error if the system entropy source is unavailable.
func GenerateTraceID() (string, error) {
	return generateHexID(16)
}

// GenerateSpanID generates a new random span ID (16 hex characters).
// A span ID represents a single operation within a trace.
// Returns an error if the system entropy source is unavailable.
func GenerateSpanID() (string, error) {
	return generateHexID(8)
}

// MustGenerateTraceID returns a fresh trace ID. If crypto/rand fails on
// the first attempt, it retries once. If the retry also fails, it emits
// a one-time WARN log and returns FallbackTraceID. The fallback is
// shaped so that downstream APM tools cannot conflate it with a real
// trace ID (see FallbackTraceID).
//
// Intended for hot paths (HTTP middleware, gRPC interceptors) where the
// caller cannot fail the request just because the entropy source is
// momentarily unavailable. Code paths that can propagate errors should
// prefer GenerateTraceID.
func MustGenerateTraceID() string {
	if id, err := generateHexID(16); err == nil {
		return id
	}
	if id, err := generateHexID(16); err == nil {
		return id
	}
	warnRandUnavailable()
	return FallbackTraceID
}

// MustGenerateSpanID returns a fresh span ID. Mirrors MustGenerateTraceID
// for the span case: one retry then a distinguishable fallback marker.
func MustGenerateSpanID() string {
	if id, err := generateHexID(8); err == nil {
		return id
	}
	if id, err := generateHexID(8); err == nil {
		return id
	}
	warnRandUnavailable()
	return FallbackSpanID
}

// generateHexID generates a random hex string of the given byte length.
// Returns an error if the entropy source fails; callers must NOT silently
// substitute zeros, which would collapse all concurrent traces onto the
// same ID and break distributed-trace correlation.
func generateHexID(byteLength int) (string, error) {
	b := make([]byte, byteLength)
	if _, err := io.ReadFull(randReader, b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// warnRandUnavailable emits a single WARN log line covering every
// fallback that occurs in the lifetime of the process. Spamming the
// logger on every request would amplify the original failure, so the
// one-shot is intentional.
func warnRandUnavailable() {
	randFallbackWarnOnce.Do(func() {
		log.Printf("velocity/trace: crypto/rand unavailable; emitting fallback trace markers. APM correlation impossible until entropy restored.")
	})
}

// WithTrace returns a new context with the given trace ID and span ID.
// This is typically called at the start of a request to establish the trace context.
func WithTrace(ctx context.Context, traceID, spanID string) context.Context {
	ctx = context.WithValue(ctx, traceIDKey, traceID)
	ctx = context.WithValue(ctx, spanIDKey, spanID)
	return ctx
}

// WithFullContext returns a new context with the given trace ID, span ID,
// and parent ID. Use to restore trace context from a persisted payload
// (queue worker, redis-stream subscriber, RPC entry) where all three
// fields were captured at the producer side. Empty strings are stored
// verbatim; callers that want a "no trace" outcome should pass empty
// strings or skip the call.
func WithFullContext(ctx context.Context, traceID, spanID, parentID string) context.Context {
	ctx = context.WithValue(ctx, traceIDKey, traceID)
	ctx = context.WithValue(ctx, spanIDKey, spanID)
	ctx = context.WithValue(ctx, parentIDKey, parentID)
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
//
// Uses MustGenerateSpanID, so on entropy failure the context carries the
// fallback span marker (not an all-zero string). Code paths that need
// to surface the error explicitly should call GenerateSpanID and
// WithSpan directly.
func WithNewSpan(ctx context.Context) (context.Context, string) {
	spanID := MustGenerateSpanID()
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
//
// Uses the Must* helpers internally so request-path callers cannot fail
// solely because of a transient entropy outage; the IDs degrade to the
// distinguishable fallback markers (see FallbackTraceID / FallbackSpanID)
// after a single retry. Callers that need explicit error propagation
// should call GenerateTraceID and GenerateSpanID directly.
func StartTrace(ctx context.Context) (context.Context, string, string) {
	traceID := MustGenerateTraceID()
	spanID := MustGenerateSpanID()
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
