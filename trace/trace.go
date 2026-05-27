// Package trace provides distributed tracing context helpers for APM instrumentation.
package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// Context keys for trace information
type contextKey string

const (
	traceIDKey  contextKey = "velocity_trace_id"
	spanIDKey   contextKey = "velocity_span_id"
	parentIDKey contextKey = "velocity_parent_id"
)

// FallbackTraceIDPrefix is the prefix used by per-call fallback trace
// IDs when crypto/rand is unavailable. The full ID has the shape
//
//	velocity_trace_norand_<processStartNs>_<counter>
//
// which is:
//   - non-hex, so any APM that pattern-matches ^[0-9a-f]{32}$ filters
//     it out and cannot conflate it with a real trace,
//   - unique within a process (monotonic atomic counter), so concurrent
//     in-flight traces stay correlated even under a rand outage,
//   - unique across process restarts (processStartNs varies), so a
//     restarted node does not reuse the previous process's fallback
//     ids,
//   - independent of crypto/rand (the very thing that failed).
const FallbackTraceIDPrefix = "velocity_trace_norand_"

// FallbackSpanIDPrefix is the span-equivalent of FallbackTraceIDPrefix.
// Same shape, same guarantees, different prefix so traces and spans
// remain distinguishable in logs.
const FallbackSpanIDPrefix = "velocity_span_norand_"

// randReader is the entropy source used by the package. It is exposed as
// a package-level variable so tests can inject a faulty reader. Defaults
// to crypto/rand.Reader.
var randReader io.Reader = rand.Reader

// randFallbackWarnOnce guards the one-time WARN log emitted by the Must*
// helpers when crypto/rand is unavailable.
var randFallbackWarnOnce sync.Once

// fallbackCounter is a monotonic counter that distinguishes per-call
// fallback IDs within a single process. atomic.Uint64 is safe across
// goroutines without a mutex, which matters because Must* helpers can
// be called from any request-handling goroutine.
var fallbackCounter atomic.Uint64

// processStartNs is captured once at package init and embedded in every
// fallback ID. It lets operators distinguish fallback IDs minted by
// different process incarnations of the same service, so a restart
// doesn't silently merge two distinct entropy outages in dashboards.
var processStartNs = time.Now().UnixNano()

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
// a one-time WARN log and returns a per-call fallback ID generated
// without crypto/rand (see fallbackTraceID).
//
// The fallback IDs are unique per call (atomic counter + process start
// nanosecond timestamp) so concurrent in-flight traces stay correlated
// even under an entropy outage, and the shape is non-hex so APM tooling
// cannot conflate them with real trace IDs.
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
	return fallbackTraceID()
}

// MustGenerateSpanID returns a fresh span ID. Mirrors MustGenerateTraceID
// for the span case: one retry then a per-call non-hex fallback ID.
func MustGenerateSpanID() string {
	if id, err := generateHexID(8); err == nil {
		return id
	}
	if id, err := generateHexID(8); err == nil {
		return id
	}
	warnRandUnavailable()
	return fallbackSpanID()
}

// fallbackTraceID returns a per-call trace ID that does not require
// crypto/rand. Format: velocity_trace_norand_<processStartNs>_<counter>.
// See FallbackTraceIDPrefix for the rationale and guarantees.
func fallbackTraceID() string {
	return fmt.Sprintf("%s%d_%d", FallbackTraceIDPrefix, processStartNs, fallbackCounter.Add(1))
}

// fallbackSpanID returns a per-call span ID with the same shape and
// guarantees as fallbackTraceID. Shares the same monotonic counter so
// span IDs and trace IDs minted in the same outage are never equal even
// though their lengths overlap.
func fallbackSpanID() string {
	return fmt.Sprintf("%s%d_%d", FallbackSpanIDPrefix, processStartNs, fallbackCounter.Add(1))
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
