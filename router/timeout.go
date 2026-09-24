package router

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// ErrHandlerTimeout is returned by the timeout-safe response writer when a
// handler attempts to write to the client after the configured timeout has
// already fired. It mirrors net/http's ErrHandlerTimeout sentinel.
var ErrHandlerTimeout = errors.New("velocity/router: handler timeout")

// timeoutWriter wraps the underlying http.ResponseWriter so that handler
// writes are buffered in memory rather than streamed to the wire. This
// mirrors net/http.TimeoutHandler's design: the handler only ever sees
// the buffer, and the middleware decides at completion (or timeout)
// whether to flush the buffer to the real writer or discard it and
// return a 503 error for the router's error boundary to render.
//
// Because every handler-facing method only touches in-memory state
// guarded by tw.mu, a blocked or stuck underlying network write cannot
// pin tw.mu and prevent the timeout path from running. The handler never
// touches the real ResponseWriter: the middleware flushes to it under
// tw.mu when the handler finished in time, and after a timeout only the
// error boundary writes to it, once the middleware has returned.
//
// Streaming (http.Flusher) is intentionally unsupported under Timeout
// middleware: a flush would have to commit headers to the wire, after
// which a subsequent timeout cannot safely overwrite them. This matches
// the stdlib net/http.TimeoutHandler limitation. Hijack and Push
// likewise refuse once the writer has been wrapped because we cannot
// hand a buffered writer to a connection upgrade.
type timeoutWriter struct {
	w  http.ResponseWriter
	mu sync.Mutex

	// h is the buffered header map the handler sees via Header(). It
	// is never the underlying ResponseWriter's header map, so handler
	// mutations after timeout cannot leak to the real response.
	h http.Header

	// wbuf accumulates body bytes written by the handler.
	wbuf bytes.Buffer

	// code is the status code captured from WriteHeader. Defaults to
	// 200 if the handler never calls WriteHeader before completing.
	code int

	// wroteHeader records whether the handler called WriteHeader, so
	// repeat calls become no-ops (matching net/http semantics).
	wroteHeader bool

	// timedOut is set by the middleware once it has decided to abandon
	// the buffered response and return its 503 error. Subsequent handler
	// Writes return ErrHandlerTimeout.
	timedOut bool
}

func newTimeoutWriter(w http.ResponseWriter) *timeoutWriter {
	return &timeoutWriter{
		w:    w,
		h:    make(http.Header),
		code: http.StatusOK,
	}
}

// Header returns the buffered header map. Handler mutations operate on
// this private map and never reach the underlying ResponseWriter until
// the middleware decides to flush the buffer.
func (tw *timeoutWriter) Header() http.Header {
	return tw.h
}

func (tw *timeoutWriter) Write(p []byte) (int, error) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.timedOut {
		return 0, ErrHandlerTimeout
	}
	if !tw.wroteHeader {
		tw.wroteHeader = true
	}
	return tw.wbuf.Write(p)
}

func (tw *timeoutWriter) WriteHeader(code int) {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.timedOut {
		return
	}
	if tw.wroteHeader {
		return
	}
	tw.wroteHeader = true
	tw.code = code
}

// flushBuffered copies the buffered headers, status code, and body to
// the real ResponseWriter. It must only be called when the middleware
// has decided the handler finished in time. Returns true if anything
// was committed; false if the writer was already in the timed-out
// state.
func (tw *timeoutWriter) flushBuffered() bool {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	if tw.timedOut {
		return false
	}
	dst := tw.w.Header()
	for k, vv := range tw.h {
		dst[k] = vv
	}
	if tw.wroteHeader {
		tw.w.WriteHeader(tw.code)
	}
	if tw.wbuf.Len() > 0 {
		_, _ = tw.w.Write(tw.wbuf.Bytes())
	}
	return true
}

// markTimedOut switches the writer to the timed-out state and discards
// any buffered handler output: every later handler Write returns
// ErrHandlerTimeout and WriteHeader is a no-op, so nothing the handler
// does from here on reaches the real ResponseWriter. The real writer has
// never had a status written (the handler's WriteHeader only mutated the
// buffer), so the error boundary can still render the 503.
func (tw *timeoutWriter) markTimedOut() {
	tw.mu.Lock()
	defer tw.mu.Unlock()
	tw.timedOut = true
	tw.wbuf.Reset()
}

// Unwrap exposes the wrapped writer for http.ResponseController.
func (tw *timeoutWriter) Unwrap() http.ResponseWriter { return tw.w }

// Hijack refuses once the writer has been wrapped. Buffered timeout
// semantics are incompatible with connection takeover.
func (tw *timeoutWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, http.ErrNotSupported
}

// Push refuses once the writer has been wrapped. Buffered timeout
// semantics are incompatible with server push.
func (tw *timeoutWriter) Push(target string, opts *http.PushOptions) error {
	return http.ErrNotSupported
}

// Timeout returns a middleware that cancels the request context after the
// given duration. If the handler does not finish before the deadline, the
// middleware returns a 503 *contract.HTTPError wrapping the context error
// without waiting for the handler, and the router's error boundary
// renders it. The wrapped error is context.DeadlineExceeded, or
// context.Canceled when the client went away first (the default boundary
// then writes nothing). The inner handler may still be
// running, but its subsequent writes are swallowed by a timeout-safe
// wrapper, so no race or late body can reach the wire. This mirrors the
// design of net/http.TimeoutHandler in the standard library.
//
// To make "release immediately" safe against the router's Context
// sync.Pool, the goroutine receives a shallow-cloned Context with its
// own Request, params, values, and a timeout-safe Response wrapper. The
// caller's Context can be reset and returned to the pool as soon as the
// middleware returns, without racing with a misbehaving handler that
// keeps running in the background.
//
// When the handler finishes in time, the caller's Context takes back the
// values the handler set and the request the inner chain ended with
// (context values inner middleware added, such as CSRF token state), so
// outer middleware and the error boundary see them. That request's
// context answers values from the inner request and its deadline,
// cancellation and error from the caller's request, never from the
// timeout context. After a timeout the caller's request is left as it
// was: the handler may still own the clone.
//
// Streaming (http.Flusher), Hijack, and Push are not supported under
// this middleware. Handlers that need any of those should not be
// wrapped by Timeout. This matches the stdlib net/http.TimeoutHandler
// limitation.
func Timeout(duration time.Duration) MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			ctx, cancel := context.WithTimeout(c.Request.Context(), duration)
			defer cancel()

			// Swap the response writer for a timeout-safe wrapper.
			// The wrapper outlives the surrounding Context: the
			// handler goroutine retains a reference via the cloned
			// context, while the outer middleware uses the same
			// wrapper to commit or discard the buffered response.
			tw := newTimeoutWriter(c.Response)

			// Build an isolated clone of the request-scoped state
			// for the handler goroutine. The handler must not share
			// mutable state with the pooled parent Context, which
			// may be reset and recycled the moment Timeout returns.
			// The clone is intentionally never pooled, reset, or
			// Put back: the handler goroutine may still hold it
			// after this middleware returns.
			clone := &Context{
				Response: tw,
				Request:  c.Request.WithContext(ctx),
				params:   append([]RouteParam(nil), c.params...),
				values:   cloneValues(c.values),
			}
			clone.applyWiring(c.snapshotWiring())

			done := make(chan error, 1)
			// Not async.Go: must forward a recovered panic through `done`
			// as a *PanicError (stack captured here, inside the deferred
			// recover, on the goroutine that panicked) so the router
			// boundary reports it as a recovered panic instead of the
			// panic being dropped into the package logger only. The
			// goroutine is bound to the request lifetime.
			go func() { //safe-goroutine: forwards panic via done as request error, see comment above
				defer func() {
					if r := recover(); r != nil {
						// Skip the deferred function so the trace starts
						// at the panic site.
						done <- newPanicError(fmt.Errorf("velocity/router: timeout handler panic: %w", panicerr.FromRecovered(r)), 1)
					}
				}()
				done <- next(clone)
			}()

			select {
			case err := <-done:
				// Handler finished in time. Commit the buffered
				// response to the real writer, then mirror any
				// values the handler stashed back onto the
				// pooled parent so downstream middleware
				// observes them.
				tw.flushBuffered()
				mergeValues(c.values, clone.values)
				// Hand the request the inner chain ended with back
				// to the parent, so outer middleware and the error
				// boundary see what inner middleware added (context
				// values, a replaced request). Its context takes
				// values from the inner request and its lifetime
				// from the parent's: the timeout context is
				// cancelled when Timeout returns. The handler
				// goroutine has returned, so reading the clone is
				// safe.
				if inner := clone.Request; inner != nil {
					c.Request = inner.WithContext(handoffContext{Context: c.Request.Context(), values: context.WithoutCancel(inner.Context())})
				}
				return err
			case <-ctx.Done():
				// Mark the writer timed out and return the 503
				// immediately; the error boundary writes it. The
				// handler goroutine continues to run on the
				// cloned context but can only mutate the buffer,
				// which is now discarded. This intentionally does
				// NOT block on <-done; a misbehaving handler that
				// ignores ctx.Done() must not pin this request.
				tw.markTimedOut()
				return contract.NewHTTPError(http.StatusServiceUnavailable).WithCause(ctx.Err())
			}
		}
	}
}

// handoffContext is the request context Timeout hands back to the parent
// Context when the handler finished in time: Value answers from values,
// the inner request's context behind context.WithoutCancel (the inner
// middleware's additions and, through the timeout context, every parent
// value), while Deadline, Done and Err come from the embedded parent
// request context, because the timeout context is cancelled when Timeout
// returns and must not end the rest of the request. WithoutCancel hides
// the timeout context's cancellation state from value lookups, so
// context.Cause reports the parent's state too.
type handoffContext struct {
	context.Context
	values context.Context
}

// Value returns the value for key from the inner request's context.
func (h handoffContext) Value(key any) any {
	return h.values.Value(key)
}

func cloneValues(src map[string]interface{}) map[string]interface{} {
	dst := make(map[string]interface{}, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func mergeValues(dst, src map[string]interface{}) {
	for k, v := range src {
		dst[k] = v
	}
}
