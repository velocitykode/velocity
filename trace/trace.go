// Package trace carries the ids that correlate work across a request and
// across processes: the trace id, span id and parent span id of the running
// operation, and the request id.
//
// Every entry point that receives work from another process applies one
// rule, StartSpan: a valid inbound carrier (a traceparent header, gRPC
// metadata, a queued job's persisted ids) is continued with a new span whose
// parent is the caller's span; anything else starts a root span. Operations
// inside a process (a query, a cache call, an outbound HTTP call) are spans
// of their own under the enclosing span, by the same rule (ChildSpanIDs,
// ContinueTrace).
//
// Across process edges the ids travel as the W3C traceparent header
// (ParseTraceparent, FormatTraceparent, Propagate) and the request id as
// X-Request-ID. The package imports only the standard library, the contract
// leaf and the framework's standalone fallback logger.
package trace

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog"
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
//
// str/str.go has a parallel randReader seam. They are intentionally NOT
// shared: trace must never fail a request and emits fallback markers plus a
// one-shot warn on entropy failure, while str.Random returns an error.
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
	id, fellBack := mustTraceID()
	if fellBack {
		warnRandUnavailable()
	}
	return id
}

// mustTraceID is MustGenerateTraceID without the warning: it reports
// whether it fell back, so a caller holding a lock (LazyTrace's Once)
// warns once it released it.
func mustTraceID() (id string, fellBack bool) {
	if id, err := generateHexID(16); err == nil {
		return id, false
	}
	if id, err := generateHexID(16); err == nil {
		return id, false
	}
	return fallbackTraceID(), true
}

// MustGenerateSpanID returns a fresh span ID. Mirrors MustGenerateTraceID
// for the span case: one retry then a per-call non-hex fallback ID.
func MustGenerateSpanID() string {
	id, fellBack := mustSpanID()
	if fellBack {
		warnRandUnavailable()
	}
	return id
}

// mustSpanID is MustGenerateSpanID without the warning (see mustTraceID).
func mustSpanID() (id string, fellBack bool) {
	if id, err := generateHexID(8); err == nil {
		return id, false
	}
	if id, err := generateHexID(8); err == nil {
		return id, false
	}
	return fallbackSpanID(), true
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
// fallback that occurs in the lifetime of the process, through the
// package logger (see SetLogger). Spamming the logger on every request
// would amplify the original failure, so the one-shot is intentional.
func warnRandUnavailable() {
	randFallbackWarnOnce.Do(func() {
		GetLogger().Warn("velocity/trace: crypto/rand unavailable; emitting fallback trace markers. APM correlation is impossible until entropy is restored")
	})
}

// logger holds the package logger SetLogger installed; none (or nil) means
// the fallback logger. SetLogger may run on one goroutine while a request
// goroutine reads it for the entropy warning: it only swaps the target.
var logger fallbacklog.Forwarder

// SetLogger installs the logger the package writes its one warning to
// (crypto/rand unavailable, fallback trace markers in use). Nil restores
// the framework's standalone fallback logger, which writes it to standard
// error. The logger is process-wide, like the async package's: an app
// built by velocity.New hands it the app logger, the newest live app's
// logger is the installed one, and an app's Shutdown hands it back to the
// previous live app's logger, or the fallback when none is left. Safe for
// concurrent use, including from inside the package logger's own methods.
// A warning that starts after SetLogger returns goes to l; one already
// being written may still reach the logger it replaces, even after that
// logger is closed. The framework's built-in file logger sends such a late
// warning to the standalone fallback logger; a custom logger's behaviour
// after close is its own.
func SetLogger(l contract.Logger) {
	logger.Set(l)
}

// GetLogger returns the package logger: the one SetLogger installed, or
// the fallback logger. Safe for concurrent use.
func GetLogger() contract.Logger {
	return fallbacklog.Resolve(logger.Installed())
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

// LazyTrace defers trace and span ID generation until the first read.
// The real IDs (identical format and entropy guarantees to StartTrace,
// including the Must* fallback behavior) are computed at most once and
// cached, so repeated reads observe the same pair and requests where
// nothing reads the IDs never pay the entropy read or hex encode.
type LazyTrace struct {
	once    sync.Once
	traceID string
	spanID  string
}

// IDs materializes the trace and span IDs on first call and returns the
// cached pair thereafter. Safe for concurrent use.
//
// The entropy warning (see MustGenerateTraceID) is written after the Once
// is done, by the call that generated the pair: the package logger may
// read this request's ids, which would wait on the Once for good.
func (l *LazyTrace) IDs() (traceID, spanID string) {
	var fellBack bool
	l.once.Do(func() {
		var traceFell, spanFell bool
		l.traceID, traceFell = mustTraceID()
		l.spanID, spanFell = mustSpanID()
		fellBack = traceFell || spanFell
	})
	if fellBack {
		warnRandUnavailable()
	}
	return l.traceID, l.spanID
}

// lazyTraceContext answers the trace and span ID keys from a LazyTrace
// holder, materializing the IDs on first read. All other keys delegate
// to the wrapped context, so GetTraceID/GetSpanID (and any direct
// ctx.Value consumer) observe the same string values an eager
// StartTrace would have stored.
type lazyTraceContext struct {
	context.Context
	lazy *LazyTrace
}

func (c lazyTraceContext) Value(key any) any {
	switch key {
	case traceIDKey:
		traceID, _ := c.lazy.IDs()
		return traceID
	case spanIDKey:
		_, spanID := c.lazy.IDs()
		return spanID
	}
	return c.Context.Value(key)
}

// StartTraceLazy establishes a trace context whose trace and span IDs
// are generated on first read instead of eagerly. Consumers see exactly
// what StartTrace would give them; the holder is returned so callers
// that know they need the IDs up front (e.g. to populate an event
// payload) can force materialization via IDs().
func StartTraceLazy(ctx context.Context) (context.Context, *LazyTrace) {
	lazy := &LazyTrace{}
	return lazyTraceContext{Context: ctx, lazy: lazy}, lazy
}

// ContinueTrace starts a new span under ctx's current span: the same trace,
// a fresh span id, ctx's span as the parent. When ctx carries no trace the
// new span is a root. It is StartSpan with ctx's own span as the Parent, for
// an operation that runs inside the process and wraps further work (an
// outbound call, a mail send). Returns the updated context and the new span
// ID.
func ContinueTrace(ctx context.Context) (context.Context, string) {
	ctx = StartSpan(ctx, Parent{TraceID: GetTraceID(ctx), SpanID: GetSpanID(ctx)})
	return ctx, GetSpanID(ctx)
}

// StartSpan applies the continue-or-start rule and returns ctx carrying the
// new span. A parent that names a trace is continued: the new span keeps
// parent.TraceID, gets a fresh span id and records parent.SpanID as its
// parent. Any other parent (the zero Parent included) starts a root span: a
// fresh trace id, a fresh span id and no parent. Any trace, span or parent
// id ctx already carried is replaced, so a stale parent never leaks into the
// new span. Parent.Sampled does not change the rule.
//
// Every entry point that receives work from another process calls it with
// the inbound carrier: the gRPC server interceptor with the parsed
// traceparent metadata, the queue worker with the producer's persisted ids,
// the scheduler with the zero Parent (a run has no inbound carrier). A
// carrier read from the network must come through ParseTraceparent first.
func StartSpan(ctx context.Context, parent Parent) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	traceID, spanID, parentID := newSpanIDs(parent)
	return WithFullContext(ctx, traceID, spanID, parentID)
}

// ChildSpanIDs returns the ids an operation records when it runs as its own
// span under ctx's current span, without deriving a context: ctx's trace
// id, a fresh span id, and ctx's span id as the parent. When ctx carries no
// trace the operation is a root span: a fresh trace id, a fresh span id and
// no parent. Use it for an operation that wraps no further work (a query, a
// cache call); use ContinueTrace for one that does.
func ChildSpanIDs(ctx context.Context) (traceID, spanID, parentID string) {
	return newSpanIDs(Parent{TraceID: GetTraceID(ctx), SpanID: GetSpanID(ctx)})
}

// newSpanIDs is the continue-or-start rule shared by StartSpan,
// ContinueTrace and ChildSpanIDs.
func newSpanIDs(parent Parent) (traceID, spanID, parentID string) {
	if parent.TraceID != "" {
		return parent.TraceID, MustGenerateSpanID(), parent.SpanID
	}
	return MustGenerateTraceID(), MustGenerateSpanID(), ""
}
