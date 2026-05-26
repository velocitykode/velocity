package router

import (
	"bufio"
	"net"
	"net/http"
	"sync"
)

// responseWriter wraps http.ResponseWriter to capture response metrics
type responseWriter struct {
	http.ResponseWriter
	status       int
	bytesWritten int64
	wroteHeader  bool

	// beforeFirstWriteOnce gates beforeFirstWriteFn so it runs at most
	// once across the lifetime of the request, regardless of how the
	// handler commits headers (explicit WriteHeader, implicit-via-Write,
	// Hijack, etc.). Used by the save-at-end session middleware to
	// flush Set-Cookie BEFORE the response headers are committed, which
	// is the moment after which any further Header().Set is silently
	// dropped on a real net/http connection.
	beforeFirstWriteOnce sync.Once
	beforeFirstWriteFn   func()
}

// newResponseWriter creates a new response writer wrapper
func newResponseWriter(w http.ResponseWriter) *responseWriter {
	return &responseWriter{
		ResponseWriter: w,
		status:         http.StatusOK, // Default status
	}
}

// BeforeFirstWrite registers fn to run exactly once, just before the
// first WriteHeader or Write call commits the response headers. Use this
// from middleware that needs to write headers (e.g. Set-Cookie for
// save-at-end session persistence) lazily but still in time for the
// real net/http transport to flush them. Subsequent calls overwrite the
// registered hook only if it has not yet fired.
//
// fn must NOT call methods on the wrapper that themselves trip
// WriteHeader (the sync.Once gate makes that safe against re-entry but
// any output produced inside fn races against the immediately-following
// WriteHeader from the caller).
func (rw *responseWriter) BeforeFirstWrite(fn func()) {
	if fn == nil {
		return
	}
	// We deliberately do not lock around the assignment: BeforeFirstWrite
	// is called from middleware on the request goroutine BEFORE the
	// handler runs, and the WriteHeader/Write callers below also run on
	// the same goroutine. There is no concurrent reader of
	// beforeFirstWriteFn until the first WriteHeader/Write.
	rw.beforeFirstWriteFn = fn
}

// fireBeforeFirstWrite invokes the registered hook (if any) exactly
// once. Called from the WriteHeader / Write / Hijack paths so any
// pre-commit hook fires before headers are flushed to the underlying
// http.ResponseWriter.
func (rw *responseWriter) fireBeforeFirstWrite() {
	rw.beforeFirstWriteOnce.Do(func() {
		fn := rw.beforeFirstWriteFn
		rw.beforeFirstWriteFn = nil
		if fn != nil {
			fn()
		}
	})
}

// WriteHeader captures the status code
func (rw *responseWriter) WriteHeader(statusCode int) {
	if !rw.wroteHeader {
		rw.fireBeforeFirstWrite()
		rw.status = statusCode
		rw.wroteHeader = true
		rw.ResponseWriter.WriteHeader(statusCode)
	}
}

// Write captures the bytes written
func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.wroteHeader {
		rw.WriteHeader(http.StatusOK)
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytesWritten += int64(n)
	return n, err
}

// Status returns the captured status code
func (rw *responseWriter) Status() int {
	return rw.status
}

// BytesWritten returns the total bytes written
func (rw *responseWriter) BytesWritten() int64 {
	return rw.bytesWritten
}

// Unwrap returns the underlying ResponseWriter (for http.ResponseController)
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

// Hijack implements http.Hijacker for WebSocket support.
// Fires the BeforeFirstWrite hook before delegating so any pre-commit
// header writes (Set-Cookie from save-at-end middleware, etc.) land on
// the connection ahead of the 101 Switching Protocols response that the
// hijacker is about to write directly.
func (rw *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := rw.ResponseWriter.(http.Hijacker); ok {
		rw.fireBeforeFirstWrite()
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// Flush implements http.Flusher for streaming support
func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Push implements http.Pusher for HTTP/2 server push
func (rw *responseWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := rw.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}
