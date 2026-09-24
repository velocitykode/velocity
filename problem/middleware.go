package problem

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"runtime/debug"
	"time"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// Middleware returns net/http middleware that recovers a panic in next and
// hands it to h as a recovered error (always reported, always a 500). An
// http.ErrAbortHandler panic is passed on so net/http aborts the response
// as documented.
//
// next writes through a TrackedWriter, and the recovery renders over that
// same writer: a panic after the response was committed (a WriteHeader, a
// body write or a flush) is reported and nothing more is written.
func Middleware(h contract.ErrorHandler) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			tw := trackCommit(w)
			defer recoverInto(h, tw, r)
			next.ServeHTTP(tw, r)
		})
	}
}

// MiddlewareFunc is Middleware for http.HandlerFunc chains.
func MiddlewareFunc(h contract.ErrorHandler) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			tw := trackCommit(w)
			defer recoverInto(h, tw, r)
			next(tw, r)
		}
	}
}

// trackCommit returns w when it already reports its own commitment, and a
// TrackedWriter over w otherwise.
func trackCommit(w http.ResponseWriter) http.ResponseWriter {
	if _, ok := w.(contract.CommitReporter); ok {
		return w
	}
	return NewTrackedWriter(w)
}

// recoverInto recovers a panic and hands it to h. It must be called
// directly by a deferred statement.
func recoverInto(h contract.ErrorHandler, w http.ResponseWriter, r *http.Request) {
	p := recover()
	if p == nil {
		return
	}
	if err, ok := p.(error); ok && errors.Is(err, http.ErrAbortHandler) {
		panic(p)
	}
	ctx := NewErrorContext()
	ctx.Recovered = true
	ctx.PanicStack = string(debug.Stack())
	ctx.StackTrace = contract.CaptureStackTrace(1)
	ctx.Timestamp = time.Now()
	h.HandleRequest(contract.NewRenderContext(w, r), panicerr.FromRecovered(p), ctx)
}

// ErrorHandler returns a function that hands a returned error to h, for
// routers and handlers that surface errors as values. The function renders
// through contract.NewRenderContext over the writer it is given, so a
// caller that passes the TrackedWriter its handler wrote through (or any
// contract.CommitReporter) gets an error after a committed response
// reported with nothing more written; over a plain writer the function
// cannot tell whether the handler already answered.
func ErrorHandler(h contract.ErrorHandler) func(http.ResponseWriter, *http.Request, error) {
	return func(w http.ResponseWriter, r *http.Request, err error) {
		h.HandleRequest(contract.NewRenderContext(w, r), err, nil)
	}
}

// TrackedWriter is an http.ResponseWriter that records whether its
// response was committed: a final WriteHeader (1xx other than 101 does not
// commit), a Write (which sends an implicit 200), a successful Flush or a
// successful Hijack. Each is recorded only after the wrapped writer
// returns, so one that panics before committing (a panicking pre-commit
// hook) leaves the response uncommitted. It reports that through Committed, which
// contract.NewRenderContext honours, so an error rendered over it after
// the handler already answered writes nothing. Flush, Hijack and Push pass
// through to the wrapped writer, and Unwrap exposes it to
// http.ResponseController. Like the writer it wraps, it is used by one
// goroutine.
type TrackedWriter struct {
	http.ResponseWriter
	committed bool
}

// NewTrackedWriter returns a TrackedWriter over w.
func NewTrackedWriter(w http.ResponseWriter) *TrackedWriter {
	return &TrackedWriter{ResponseWriter: w}
}

// Committed reports whether the response was committed.
func (t *TrackedWriter) Committed() bool {
	return t.committed
}

// WriteHeader writes code through and records a final status as
// committing the response.
func (t *TrackedWriter) WriteHeader(code int) {
	t.ResponseWriter.WriteHeader(code)
	if code < 100 || code > 199 || code == http.StatusSwitchingProtocols {
		t.committed = true
	}
}

// Write writes p through and records the response committed once the
// wrapped writer returns (its implicit 200 went out even when the write
// itself failed).
func (t *TrackedWriter) Write(p []byte) (int, error) {
	n, err := t.ResponseWriter.Write(p)
	t.committed = true
	return n, err
}

// FlushError flushes the wrapped writer through http.ResponseController
// and records the response committed when the flush succeeds.
func (t *TrackedWriter) FlushError() error {
	err := http.NewResponseController(t.ResponseWriter).Flush()
	if err == nil {
		t.committed = true
	}
	return err
}

// Flush is FlushError for callers that use http.Flusher.
func (t *TrackedWriter) Flush() {
	_ = t.FlushError()
}

// Hijack takes over the connection through http.ResponseController and
// records the response committed when it succeeds.
func (t *TrackedWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	conn, rw, err := http.NewResponseController(t.ResponseWriter).Hijack()
	if err == nil {
		t.committed = true
	}
	return conn, rw, err
}

// Push initiates an HTTP/2 server push through the wrapped writer, or
// returns http.ErrNotSupported when it cannot push.
func (t *TrackedWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := t.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}

// Unwrap returns the wrapped writer for http.ResponseController.
func (t *TrackedWriter) Unwrap() http.ResponseWriter {
	return t.ResponseWriter
}
