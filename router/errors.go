package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"strconv"
	"strings"

	"github.com/velocitykode/velocity/contract"
)

// ErrorInfo carries the facts the router knows about a failed request to
// the error handler installed with SetErrorHandler (and to
// DefaultErrorHandler).
type ErrorInfo struct {
	// Recovered is true when the error came from a recovered panic,
	// including a panic the Timeout middleware forwarded as a *PanicError.
	Recovered bool
	// Stack is the raw goroutine stack captured at the panic, or "".
	Stack string
	// StackTrace is the structured stack captured at the panic, or nil.
	StackTrace *contract.StackTrace
	// Committed is true when the response status line (or any body byte)
	// was already written. A handler must not write a second response
	// then; it can only report.
	Committed bool
	// RequestID, TraceID and SpanID identify the request for reporting.
	RequestID string
	TraceID   string
	SpanID    string
}

// PanicError is a recovered panic carried as an error, with the stack
// captured inside the deferred recover. The router hands one to the error
// boundary for every recovered panic, and the Timeout middleware forwards
// its handler goroutine's panic as one so the boundary reports it with the
// goroutine's stack.
//
// A PanicError always resolves to status 500 and always reports: a panic
// is a bug, not a response, even when the panic value is itself an
// HTTP-shaped error.
type PanicError struct {
	// Err is the recovered value converted to an error. It unwraps to the
	// panic value when that value was an error.
	Err error
	// Stack is the raw goroutine stack at the panic.
	Stack string
	// Trace is the structured stack at the panic.
	Trace *contract.StackTrace
}

// Error returns the recovered value's text.
func (e *PanicError) Error() string {
	if e == nil || e.Err == nil {
		return "panic"
	}
	return e.Err.Error()
}

// Unwrap returns Err so errors.Is and errors.As reach the panic value.
func (e *PanicError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// StatusCode returns 500: a recovered panic never answers with any other
// status.
func (e *PanicError) StatusCode() int {
	return http.StatusInternalServerError
}

// ShouldReport returns true: a recovered panic is always reported.
func (e *PanicError) ShouldReport() bool {
	return true
}

// panicStackSize bounds the raw stack captured for a recovered panic.
const panicStackSize = 4096

// newPanicError converts a recovered value into a *PanicError whose Err
// is err (callers pass panicerr.FromRecovered, possibly wrapped). It must
// be called from inside the deferred function that called recover, while
// the panicking frames are still on the stack. skip counts the frames
// above newPanicError to omit from the structured trace (the deferred
// function and any helper between it and newPanicError), so the trace
// starts at the panic site.
func newPanicError(err error, skip int) *PanicError {
	buf := make([]byte, panicStackSize)
	n := runtime.Stack(buf, false)
	return &PanicError{
		Err:   err,
		Stack: string(buf[:n]),
		Trace: contract.CaptureStackTrace(skip + 1),
	}
}

// problemBody is the application/problem+json body (RFC 9457) the default
// error handler writes when the client wants JSON.
type problemBody struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail,omitempty"`
	Instance string `json:"instance,omitempty"`
}

// defaultLogLevel is the level the router's default path logs an error at.
type defaultLogLevel int

const (
	logNone defaultLogLevel = iota
	logWarn
	logError
)

// defaultResolution is what the default error path decided for one error:
// the status to answer with, the headers the error carries, whether to
// write at all, and at which level (if any) the router logs it.
type defaultResolution struct {
	status  int
	headers http.Header
	write   bool
	level   defaultLogLevel
}

// resolveStatus maps err to the status the router answers with, apart
// from the recovered and response-written cases: a deadline is 503, an
// oversized body is 413, otherwise contract.StatusOf decides. ok is false
// when nothing in err names a status (the answer is then 500).
func resolveStatus(err error) (status int, headers http.Header, ok bool) {
	if errors.Is(err, context.DeadlineExceeded) {
		return http.StatusServiceUnavailable, nil, true
	}
	var mbe *http.MaxBytesError
	if errors.As(err, &mbe) {
		return http.StatusRequestEntityTooLarge, nil, true
	}
	return contract.StatusOf(err)
}

// resolveDefault decides how the default error path answers err. The
// cases, first match wins:
//
//   - a bare contract.ErrResponseWritten: nothing written, nothing logged.
//   - a contract.Handled value: nothing written; the cause is logged when
//     it resolves to 500 or above.
//   - info.Recovered (or a *PanicError in the chain): 500, logged with the
//     stack.
//   - context.Canceled while the request context is dead: the client is
//     gone, nothing written, nothing logged.
//   - context.DeadlineExceeded: 503, logged at warn.
//   - *http.MaxBytesError: 413.
//   - otherwise contract.StatusOf; an error naming no status is 500.
//
// Other statuses of 500 and above are logged at error level.
// info.Committed turns off writing but keeps the logging decision.
func resolveDefault(c *Context, err error, info ErrorInfo) defaultResolution {
	if errors.Is(err, contract.ErrResponseWritten) {
		cause := contract.HandledCause(err)
		if cause == nil {
			return defaultResolution{}
		}
		res := resolveDefault(c, cause, info)
		res.write = false
		return res
	}

	var res defaultResolution
	var pe *PanicError
	switch {
	case info.Recovered || errors.As(err, &pe):
		res = defaultResolution{status: http.StatusInternalServerError, level: logError}
	case errors.Is(err, context.Canceled) && requestGone(c):
		return defaultResolution{}
	case errors.Is(err, context.DeadlineExceeded):
		res = defaultResolution{status: http.StatusServiceUnavailable, level: logWarn}
	default:
		status, headers, _ := resolveStatus(err)
		res = defaultResolution{status: status, headers: headers}
		if status >= http.StatusInternalServerError {
			res.level = logError
		}
	}
	res.write = !info.Committed
	return res
}

// requestGone reports whether the request context of c is already done.
func requestGone(c *Context) bool {
	return c != nil && c.Request != nil && c.Request.Context().Err() != nil
}

// DefaultErrorHandler is the router's own error response, used when no
// handler is installed with SetErrorHandler and by Wrap. It writes nothing
// when info.Committed is true, for an error matching
// contract.ErrResponseWritten, and for a context.Canceled whose request
// context is dead (the client is gone). Otherwise the status resolves as
// follows: a recovered panic is 500; context.DeadlineExceeded is 503;
// *http.MaxBytesError is 413; any other error takes its status and
// headers from contract.StatusOf, or 500 when it names none.
//
// The body is application/problem+json (type, title, status, detail,
// instance) when contract.WantsJSON holds for the request, plain text
// otherwise. A 4xx answer echoes the *contract.HTTPError message; a 5xx
// answer shows only the status text, so server-side detail never reaches
// the client. Headers the error carries (Retry-After, Allow, ...) are
// copied before the status line is written; a key or value containing CR
// or LF is dropped.
//
// DefaultErrorHandler does not log; the router's default path logs before
// calling it (see SetErrorLogger).
func DefaultErrorHandler(c *Context, err error, info ErrorInfo) {
	if c == nil || c.Response == nil || err == nil {
		return
	}
	res := resolveDefault(c, err, info)
	if !res.write {
		return
	}
	writeDefaultError(c, err, res)
}

// writeDefaultError writes the resolved default response for err.
func writeDefaultError(c *Context, err error, res defaultResolution) {
	h := c.Response.Header()
	for key, values := range res.headers {
		if key == "" || strings.ContainsAny(key, "\r\n") {
			continue
		}
		h.Del(key)
		for _, v := range values {
			if strings.ContainsAny(v, "\r\n") {
				continue
			}
			h.Add(key, v)
		}
	}

	detail := statusText(res.status)
	if res.status < http.StatusInternalServerError {
		var he *contract.HTTPError
		if errors.As(err, &he) && he.StatusCode() == res.status && he.Message != "" {
			detail = he.Message
		}
	}

	if c.Request != nil && contract.WantsJSON(c.Request) {
		body, mErr := json.Marshal(problemBody{
			Type:     "about:blank",
			Title:    statusText(res.status),
			Status:   res.status,
			Detail:   detail,
			Instance: c.Request.URL.EscapedPath(),
		})
		if mErr == nil {
			h.Del("Content-Length")
			h.Set("Content-Type", "application/problem+json")
			h.Set("X-Content-Type-Options", "nosniff")
			c.Response.WriteHeader(res.status)
			_, _ = c.Response.Write(body)
			return
		}
	}
	http.Error(c.Response, detail, res.status)
}

// statusText returns the standard text for status, or "status N" for a
// code net/http does not name.
func statusText(status int) string {
	if t := http.StatusText(status); t != "" {
		return t
	}
	return "status " + strconv.Itoa(status)
}

// ErrorHandlerMiddleware returns a middleware that offers errors from
// downstream handlers to fn. Install it per group for layered error
// rendering (JSON for /api, HTML for /):
//
//	r.Group("/api", func(g router.Router) {
//	    g.Use(router.ErrorHandlerMiddleware(jsonErrorResponder))
//	    g.Get("/users", listUsers)
//	})
//
// fn returns true when it wrote the response. The middleware then returns
// contract.Handled(err): the router boundary writes nothing more but still
// reports the error once, and RequestFailed still fires with it. fn
// returns false to leave the error to the boundary unchanged. An error
// that already matches contract.ErrResponseWritten passes through without
// calling fn.
//
// The middleware sees only errors returned through its group; recovered
// panics, unmatched routes and static files reach the router boundary
// directly (see SetErrorHandler).
func ErrorHandlerMiddleware(fn func(c *Context, err error) bool) MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			err := next(c)
			if err == nil || errors.Is(err, contract.ErrResponseWritten) {
				return err
			}
			if fn(c, err) {
				return contract.Handled(err)
			}
			return err
		}
	}
}
