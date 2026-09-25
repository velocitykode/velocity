package router

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"runtime"
	"strings"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/panicerr"
)

// ErrorInfo carries the facts the router knows about a failed request to
// the error handler installed with SetErrorHandler (and to
// DefaultErrorHandler).
type ErrorInfo struct {
	// Recovered is true when the error came from a recovered panic: one
	// the router recovered, or a returned error whose chain holds a
	// contract.RecoveredPanic (a *PanicError the Timeout middleware
	// forwarded, or a consumer recovery middleware's own error).
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
//
// PanicError implements contract.RecoveredPanic, so it is the boundary of
// the panic in an error chain: a report-once or response-written marker
// inside it counts for nothing, while one wrapped around it (a middleware
// that reported or rendered the panic) counts. A PanicError built by hand
// behaves the same as one the router built.
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

// Recovered returns the value handed to recover(): the value of the
// recovered-panic error Err wraps, or Err itself when it wraps none.
func (e *PanicError) Recovered() any {
	if e == nil {
		return nil
	}
	if pe := panicerr.AsTyped(e.Err); pe != nil {
		return pe.Recovered()
	}
	return e.Err
}

// PanicError is the boundary node of a recovered panic.
var _ contract.RecoveredPanic = (*PanicError)(nil)

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
// error handler writes when the client wants JSON: the members the error
// pipeline writes outside debug mode.
type problemBody struct {
	Type      string              `json:"type"`
	Title     string              `json:"title"`
	Status    int                 `json:"status"`
	Detail    string              `json:"detail,omitempty"`
	Instance  string              `json:"instance,omitempty"`
	Errors    map[string][]string `json:"errors,omitempty"`
	RequestID string              `json:"request_id,omitempty"`
	TraceID   string              `json:"trace_id,omitempty"`
}

// defaultLogLevel is the level the router's default path logs an error at.
type defaultLogLevel int

const (
	logNone defaultLogLevel = iota
	logWarn
	logError
)

// defaultResolution is what the default error path decided for one error:
// the status to answer with, the headers the error carries, the client
// message a 4xx answer echoes, whether to write at all, and at which level
// (if any) the router logs it.
type defaultResolution struct {
	status  int
	headers http.Header
	message string
	write   bool
	level   defaultLogLevel
}

// walkLimit bounds how many nodes deep classifyError walks an error chain
// by hand before it hands the rest of that branch to the errors package,
// whose answer is the same at any depth.
const walkLimit = 64

// errorFacts is what one walk of an error chain found: the first
// StatusError, HeaderError, MessageError and *PanicError in errors.As
// order, whether errors.As would find a contract.RecoveredPanic (panicked;
// a *PanicError is one, and supplies the stack fields when present), and
// whether errors.Is would match *http.MaxBytesError, context.Canceled,
// context.DeadlineExceeded and contract.ErrResponseWritten.
type errorFacts struct {
	status   contract.StatusError
	header   contract.HeaderError
	message  contract.MessageError
	panicErr *PanicError

	haveStatus  bool
	haveHeader  bool
	haveMessage bool
	panicked    bool
	maxBytes    bool
	canceled    bool
	deadline    bool
	written     bool
}

// classifyError walks err's chain once. The walk visits nodes in the
// order errors.As and errors.Is do and matches with type assertions, so no
// target escapes to the heap. A node with an As method, or one deeper than
// walkLimit, has its branch handed to errors.As (and, past the limit,
// errors.Is), so every answer matches the errors package for any chain.
func classifyError(err error) errorFacts {
	var f errorFacts
	f.walk(err, 0, false)
	return f
}

// walk visits err and the errors below it. skipAs is set below a node
// whose As method already had its branch searched by errors.As.
func (f *errorFacts) walk(err error, depth int, skipAs bool) {
	for err != nil {
		if depth >= walkLimit {
			if !skipAs {
				f.fallbackAs(err)
			}
			f.fallbackIs(err)
			return
		}
		if !skipAs {
			f.matchAs(err)
			if _, ok := err.(interface{ As(any) bool }); ok {
				f.fallbackAs(err)
				skipAs = true
			}
		}
		f.matchIs(err)
		switch x := err.(type) {
		case interface{ Unwrap() error }:
			err = x.Unwrap()
			depth++
		case interface{ Unwrap() []error }:
			for _, e := range x.Unwrap() {
				if e != nil {
					f.walk(e, depth+1, skipAs)
				}
			}
			return
		default:
			return
		}
	}
}

// matchAs records the As targets err itself satisfies.
func (f *errorFacts) matchAs(err error) {
	if !f.haveStatus {
		if se, ok := err.(contract.StatusError); ok {
			f.status, f.haveStatus = se, true
		}
	}
	if !f.haveHeader {
		if he, ok := err.(contract.HeaderError); ok {
			f.header, f.haveHeader = he, true
		}
	}
	if !f.haveMessage {
		if me, ok := err.(contract.MessageError); ok {
			f.message, f.haveMessage = me, true
		}
	}
	if !f.panicked {
		if _, ok := err.(contract.RecoveredPanic); ok {
			f.panicked = true
		}
	}
	if f.panicErr == nil {
		if pe, ok := err.(*PanicError); ok {
			f.panicErr = pe
		}
	}
	if !f.maxBytes {
		if _, ok := err.(*http.MaxBytesError); ok {
			f.maxBytes = true
		}
	}
}

// matchIs records the sentinels err itself matches, as errors.Is does:
// equality, then an Is method.
func (f *errorFacts) matchIs(err error) {
	if !f.canceled {
		f.canceled = isNode(err, context.Canceled)
	}
	if !f.deadline {
		f.deadline = isNode(err, context.DeadlineExceeded)
	}
	if !f.written {
		f.written = isNode(err, contract.ErrResponseWritten)
	}
}

// isNode reports whether err itself (not its chain) matches target under
// errors.Is. target must be comparable.
func isNode(err, target error) bool {
	if err == target {
		return true
	}
	x, ok := err.(interface{ Is(error) bool })
	return ok && x.Is(target)
}

// fallbackAs resolves the As targets still missing over err's whole
// branch through errors.As.
func (f *errorFacts) fallbackAs(err error) {
	if !f.haveStatus {
		var se contract.StatusError
		if errors.As(err, &se) {
			f.status, f.haveStatus = se, true
		}
	}
	if !f.haveHeader {
		var he contract.HeaderError
		if errors.As(err, &he) {
			f.header, f.haveHeader = he, true
		}
	}
	if !f.haveMessage {
		var me contract.MessageError
		if errors.As(err, &me) {
			f.message, f.haveMessage = me, true
		}
	}
	if !f.panicked {
		var rp contract.RecoveredPanic
		f.panicked = errors.As(err, &rp)
	}
	if f.panicErr == nil {
		var pe *PanicError
		if errors.As(err, &pe) {
			f.panicErr = pe
		}
	}
	if !f.maxBytes {
		var mbe *http.MaxBytesError
		f.maxBytes = errors.As(err, &mbe)
	}
}

// fallbackIs resolves the sentinels still unmatched over err's whole
// branch through errors.Is.
func (f *errorFacts) fallbackIs(err error) {
	if !f.canceled {
		f.canceled = errors.Is(err, context.Canceled)
	}
	if !f.deadline {
		f.deadline = errors.Is(err, context.DeadlineExceeded)
	}
	if !f.written {
		f.written = errors.Is(err, contract.ErrResponseWritten)
	}
}

// markedWritten reports whether err, classified into f, marks a response
// written on purpose: contract.IsResponseWritten holds for it (the bare
// sentinel or a contract.Handled value outside the value of any recovered
// panic it carries). A panic is a 500 whatever its value, so a marker
// inside a contract.RecoveredPanic node (a *PanicError is one) counts for
// nothing, and when recovered is set but err carries no such node the
// whole of err is the panic value and nothing counts. A Handled value
// wrapping a *PanicError (a middleware rendered the panic) still marks the
// response written.
// f.written is the cheap precondition: no marker anywhere, none outside.
func (f *errorFacts) markedWritten(err error, recovered bool) bool {
	if !f.written {
		return false
	}
	if recovered && !carriesRecoveredPanic(err) {
		return false
	}
	return contract.IsResponseWritten(err)
}

// markedWritten is errorFacts.markedWritten for an unclassified error.
func markedWritten(err error, recovered bool) bool {
	f := classifyError(err)
	return f.markedWritten(err, recovered)
}

// carriesRecoveredPanic reports whether err's chain holds a
// contract.RecoveredPanic node.
func carriesRecoveredPanic(err error) bool {
	var rp contract.RecoveredPanic
	return errors.As(err, &rp)
}

// answer resolves the status and headers for an error that is neither a
// recovered panic nor a client-gone cancellation. First match wins: an
// explicit StatusError (with the first HeaderError's headers), a deadline
// (503), an oversized body (413), otherwise 500 with the first
// HeaderError's headers. named is false only in the last case.
func (f *errorFacts) answer() (status int, headers http.Header, named bool) {
	switch {
	case f.haveStatus:
		status, named = f.status.StatusCode(), true
		if status < 100 || status > 999 {
			status = http.StatusInternalServerError
		}
	case f.deadline:
		return http.StatusServiceUnavailable, nil, true
	case f.maxBytes:
		return http.StatusRequestEntityTooLarge, nil, true
	default:
		status = http.StatusInternalServerError
	}
	if f.haveHeader {
		headers = f.header.Headers()
	}
	return status, headers, named
}

// resolveDefault decides how the default error path answers err. The
// cases, first match wins:
//
//   - a bare contract.ErrResponseWritten outside a recovered panic (see
//     errorFacts.markedWritten): nothing written, nothing logged.
//   - a contract.Handled value outside a recovered panic: nothing
//     written; the cause resolves (and logs) through these same cases.
//   - info.Recovered (or a contract.RecoveredPanic in the chain, such as
//     a *PanicError): 500, logged at error level with the stack when there
//     is one, whatever the panic value carries.
//   - context.Canceled while the request context is dead: when its cause
//     is contract.ErrServerShuttingDown the server cut the request off
//     while shutting down, answered 503 with Retry-After: 1 and
//     Connection: close and logged at warn; otherwise the client is gone,
//     nothing written, nothing logged.
//   - an explicit StatusError: its status, with the headers of the first
//     HeaderError in the chain, even when it wraps a deadline or an
//     oversized body.
//   - context.DeadlineExceeded: 503.
//   - *http.MaxBytesError: 413.
//   - otherwise 500.
//
// A 503 whose chain holds context.DeadlineExceeded (explicit or not), and
// a request the server cancelled while shutting down, log at warn; any
// other status of 500 and above logs at error level; below 500 nothing is
// logged. info.Committed turns off writing but keeps the
// logging decision.
func resolveDefault(c *Context, err error, info ErrorInfo) defaultResolution {
	f := classifyError(err)
	return resolveClassified(c, err, &f, info)
}

// resolveClassified is resolveDefault for an error already classified
// into f.
func resolveClassified(c *Context, err error, f *errorFacts, info ErrorInfo) defaultResolution {
	if f.markedWritten(err, info.Recovered) {
		cause := contract.HandledCause(err)
		if cause == nil {
			return defaultResolution{}
		}
		res := resolveDefault(c, cause, info)
		res.write = false
		return res
	}

	var res defaultResolution
	switch {
	case info.Recovered || f.panicked:
		res = defaultResolution{status: http.StatusInternalServerError, level: logError}
	case f.canceled && requestGone(c):
		if !serverCancelled(c) {
			return defaultResolution{}
		}
		shutdown := serverShutdownError(err)
		res = defaultResolution{status: shutdown.StatusCode(), headers: shutdown.Headers(), level: logWarn}
	default:
		res.status, res.headers, _ = f.answer()
		switch {
		case res.status == http.StatusServiceUnavailable && f.deadline:
			res.level = logWarn
		case res.status >= http.StatusInternalServerError:
			res.level = logError
		}
		if res.status < http.StatusInternalServerError && f.haveMessage && f.message.StatusCode() == res.status {
			res.message = f.message.ClientMessage()
		}
	}
	res.write = !info.Committed
	return res
}

// requestGone reports whether the request context of c is already done:
// the client went away, or the server cancelled the request (see
// serverCancelled).
func requestGone(c *Context) bool {
	return c != nil && c.Request != nil && c.Request.Context().Err() != nil
}

// serverCancelled reports whether the request context of c is done because
// the server is shutting down: its cause is contract.ErrServerShuttingDown.
func serverCancelled(c *Context) bool {
	if c == nil || c.Request == nil {
		return false
	}
	ctx := c.Request.Context()
	return ctx.Err() != nil && errors.Is(context.Cause(ctx), contract.ErrServerShuttingDown)
}

// serverShutdownError is the answer to a request the server cancelled
// while shutting down: 503, retry after a second, on a new connection.
func serverShutdownError(cause error) *contract.HTTPError {
	return (&contract.HTTPError{Status: http.StatusServiceUnavailable, Message: http.StatusText(http.StatusServiceUnavailable)}).
		WithHeader("Retry-After", "1").
		WithHeader("Connection", "close").
		WithCause(cause)
}

// DefaultErrorHandler is the router's own error response, used when no
// handler is installed with SetErrorHandler and by Wrap. It writes nothing
// when info.Committed is true, for an error matching
// contract.ErrResponseWritten outside a recovered panic (a panic answers
// 500 whatever its value), and for a context.Canceled whose request
// context is dead (the client is gone), unless that context's cause is
// contract.ErrServerShuttingDown: the server cut the request off while
// shutting down, and it answers 503 with Retry-After: 1 and Connection:
// close. Otherwise the status resolves as follows, first match wins: a
// recovered panic is 500; an explicit
// contract.StatusError names the status, with the headers of the first
// contract.HeaderError in the chain, even when it wraps a deadline or an
// oversized body; context.DeadlineExceeded is 503; *http.MaxBytesError is
// 413; any other error is 500.
//
// The body is application/problem+json (type, title, status, detail,
// instance as the request path, errors, and request_id and trace_id when
// info carries them) when contract.WantsJSON holds for the request, plain
// text otherwise: the body the error pipeline writes outside debug mode.
// Unless the request is an Inertia request, the response lists Accept in
// its Vary header.
// The title is contract.StatusTitle. A 4xx answer echoes the message of
// the first contract.MessageError in the chain (HTTPError.Message, for
// one) when it names the answered status, and carries the per-field
// messages of the first error in the chain with an
// Errors() map[string][]string method (a validation failure) as its
// errors member; a 5xx answer shows only the status title, so server-side
// detail never reaches the client. Headers the error carries
// (Retry-After, Allow, ...) are copied before the status line is written;
// a key or value containing CR or LF is dropped.
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
	writeDefaultError(c, err, res, info)
}

// writeDefaultError writes the resolved default response for err.
func writeDefaultError(c *Context, err error, res defaultResolution, info ErrorInfo) {
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

	detail := contract.StatusTitle(res.status)
	if res.message != "" {
		detail = res.message
	}

	// X-Inertia picks the Inertia answer; outside Inertia the headers
	// contract.WantsJSON reads pick problem+json or plain text. A cache
	// must key the response on every header that chose it.
	asJSON := false
	if c.Request != nil {
		inertia := contract.IsInertia(c.Request)
		varyOnNegotiation(h, inertia)
		if !inertia {
			asJSON = contract.WantsJSON(c.Request)
		}
	}
	if asJSON {
		instance := ""
		if c.Request.URL != nil {
			instance = c.Request.URL.Path
		}
		body := problemBody{
			Type:      "about:blank",
			Title:     contract.StatusTitle(res.status),
			Status:    res.status,
			Detail:    detail,
			Instance:  instance,
			RequestID: info.RequestID,
			TraceID:   info.TraceID,
		}
		var fields fieldMessager
		if res.status < http.StatusInternalServerError && errors.As(err, &fields) {
			body.Errors = fields.Errors()
		}
		raw, mErr := json.Marshal(body)
		if mErr == nil {
			h.Del("Content-Length")
			h.Set("Content-Type", "application/problem+json")
			h.Set("X-Content-Type-Options", "nosniff")
			c.Response.WriteHeader(res.status)
			_, _ = c.Response.Write(raw)
			return
		}
	}
	http.Error(c.Response, detail, res.status)
}

// varyNegotiated and varyInertia are the Vary values varyOnNegotiation
// sets on a response that declares none: every header contract's
// negotiation reads, joined into one value, and X-Inertia alone. They are
// shared and never written through: http.Header.Add appends into a new
// array (each slice's length equals its capacity) and Set replaces the
// slice, so one response cannot change another's value. Sharing them
// keeps the default error path from allocating for the header.
var (
	varyNegotiated = []string{negotiationVaryValue()}
	varyInertia    = []string{contract.InertiaNegotiationHeader}
)

// negotiationVaryValue joins the headers contract's negotiation reads into
// one Vary value: the JSON ones, then X-Inertia.
func negotiationVaryValue() string {
	names := contract.JSONNegotiationHeaders()
	return strings.Join(append(names[:], contract.InertiaNegotiationHeader), ", ")
}

// varyOnNegotiation declares in h's Vary header the request headers that
// chose the default response's format: X-Inertia always, and the headers
// contract.WantsJSON reads unless the request is an Inertia request (whose
// answer X-Inertia alone fixes). A response that declares no Vary gets the
// shared value; otherwise each header is appended unless already listed.
func varyOnNegotiation(h http.Header, inertia bool) {
	if _, ok := h["Vary"]; !ok {
		if inertia {
			h["Vary"] = varyInertia
		} else {
			h["Vary"] = varyNegotiated
		}
		return
	}
	if !inertia {
		for _, name := range contract.JSONNegotiationHeaders() {
			contract.AppendVary(h, name)
		}
	}
	contract.AppendVary(h, contract.InertiaNegotiationHeader)
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
// that already marks a written response passes through without calling
// fn; a panic the Timeout middleware forwarded is offered to fn whatever
// its value carries.
//
// The middleware sees only errors returned through its group; recovered
// panics, unmatched routes and static files reach the router boundary
// directly (see SetErrorHandler).
func ErrorHandlerMiddleware(fn func(c *Context, err error) bool) MiddlewareFunc {
	return func(next HandlerFunc) HandlerFunc {
		return func(c *Context) error {
			err := next(c)
			if err == nil || markedWritten(err, false) {
				return err
			}
			if fn(c, err) {
				return contract.Handled(err)
			}
			return err
		}
	}
}
