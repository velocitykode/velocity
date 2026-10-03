package router

import (
	"context"
	"encoding/json"
	"net/http"
	"runtime"
	"strings"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/errchain"
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
	// was already written, or when the request was answered before the
	// error happened (a panic in a Timeout handler goroutine after the
	// 503, see Timeout). A handler must not write a second response then;
	// it can only report.
	Committed bool
	// RequestID, TraceID and SpanID identify the request for reporting.
	RequestID string
	TraceID   string
	SpanID    string
}

// isAbortPanic reports whether a recovered panic value is net/http's
// documented abort: an error matching http.ErrAbortHandler, which a
// handler panics with to cut its response off without the server logging
// an error (httputil.ReverseProxy does, mid-body). It is not a bug, so
// every recover site in the router (the matched, unmatched and static
// dispatch, the pre-commit hook at finalize, and the Timeout handler
// goroutine) skips the boundary for it: no PanicError, no RequestFailed,
// no report and no response, and pending pre-commit hooks do not run.
// The request's bookkeeping (RequestHandled with the recorded status,
// returning the Context to the pool) still runs, and the value is
// re-panicked last so net/http aborts the connection.
func isAbortPanic(recovered any) bool {
	err, ok := recovered.(error)
	return ok && errchain.Is(err, http.ErrAbortHandler)
}

// PanicError is a recovered panic carried as an error, with the stack
// captured inside the deferred recover. The router hands one to the error
// boundary for every recovered panic, and the Timeout middleware forwards
// its handler goroutine's panic as one so the boundary reports it with the
// goroutine's stack.
//
// A PanicError always resolves to status 500 and always reports: a panic
// is a bug, not a response, even when the panic value is itself an
// HTTP-shaped error. The one panic that is not a bug, net/http's
// documented abort http.ErrAbortHandler, never becomes a PanicError: the
// router re-panics it so net/http aborts the connection (see
// isAbortPanic).
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
	return errchain.Text(e.Err)
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
	// fields are the per-field messages of a 4xx answer's problem body.
	fields map[string][]string
	write  bool
	level  defaultLogLevel
}

// errorFacts is what one walk of an error chain found, breadth first
// through errchain.Walk: the first StatusError's status, the first
// HeaderError's headers, the first MessageError's status and client
// message, the first field messages (a value with Errors, for a 4xx
// answer's problem body) and the first *PanicError, whether a
// contract.RecoveredPanic is in the chain (panicked; a *PanicError is one,
// and supplies the stack fields when present), and whether
// *http.MaxBytesError, context.Canceled, context.DeadlineExceeded and
// contract.ErrResponseWritten are.
//
// Every method of the error the classification needs (Unwrap, Is, As,
// StatusCode, Headers, ClientMessage, Errors) is user code, and is called
// inside the walk, bounded and contained. A walk that panicked, or that
// errchain.Max cut short, leaves no facts at all: the error answers as an
// unnamed 500, with no headers and no message of its own, since what it
// would have said cannot be known.
type errorFacts struct {
	headers       http.Header
	clientMessage string
	fields        map[string][]string
	panicErr      *PanicError

	statusCode    int
	messageStatus int

	haveStatus  bool
	haveHeader  bool
	haveMessage bool
	haveFields  bool
	panicked    bool
	maxBytes    bool
	canceled    bool
	deadline    bool
	written     bool
}

// classifyError walks err's chain once (see errorFacts).
func classifyError(err error) errorFacts {
	var f errorFacts
	if errchain.Walk(err, f.visit) != errchain.Ended {
		return errorFacts{}
	}
	return f
}

// visit records the facts e itself carries. It never stops the walk: a
// later node can still carry a fact not found yet. A node answers a fact
// by type assertion or, when it has one, through its own As or Is method,
// as errchain.MatchesAs and errchain.Matches do; the As method is looked
// up once per node.
func (f *errorFacts) visit(e error) bool {
	if !f.haveStatus {
		if se, ok := e.(contract.StatusError); ok {
			f.statusCode, f.haveStatus = se.StatusCode(), true
		}
	}
	if !f.haveHeader {
		if he, ok := e.(contract.HeaderError); ok {
			f.headers, f.haveHeader = he.Headers(), true
		}
	}
	if !f.haveMessage {
		if me, ok := e.(contract.MessageError); ok {
			f.messageStatus, f.clientMessage, f.haveMessage = me.StatusCode(), me.ClientMessage(), true
		}
	}
	if !f.haveFields {
		if fm, ok := e.(fieldMessager); ok {
			f.fields, f.haveFields = fm.Errors(), true
		}
	}
	if !f.panicked {
		_, f.panicked = e.(contract.RecoveredPanic)
	}
	if f.panicErr == nil {
		f.panicErr, _ = e.(*PanicError)
	}
	if !f.maxBytes {
		_, f.maxBytes = e.(*http.MaxBytesError)
	}
	if _, ok := e.(interface{ As(any) bool }); ok {
		f.visitAs(e)
	}
	f.canceled = f.canceled || errchain.Matches(e, context.Canceled)
	f.deadline = f.deadline || errchain.Matches(e, context.DeadlineExceeded)
	f.written = f.written || errchain.Matches(e, contract.ErrResponseWritten)
	return false
}

// visitAs records the facts still missing that e answers through its own
// As method.
func (f *errorFacts) visitAs(e error) {
	if !f.haveStatus {
		if se, ok := errchain.MatchesAs[contract.StatusError](e); ok && se != nil {
			f.statusCode, f.haveStatus = se.StatusCode(), true
		}
	}
	if !f.haveHeader {
		if he, ok := errchain.MatchesAs[contract.HeaderError](e); ok && he != nil {
			f.headers, f.haveHeader = he.Headers(), true
		}
	}
	if !f.haveMessage {
		if me, ok := errchain.MatchesAs[contract.MessageError](e); ok && me != nil {
			f.messageStatus, f.clientMessage, f.haveMessage = me.StatusCode(), me.ClientMessage(), true
		}
	}
	if !f.haveFields {
		if fm, ok := errchain.MatchesAs[fieldMessager](e); ok && fm != nil {
			f.fields, f.haveFields = fm.Errors(), true
		}
	}
	if !f.panicked {
		if rp, ok := errchain.MatchesAs[contract.RecoveredPanic](e); ok && rp != nil {
			f.panicked = true
		}
	}
	if f.panicErr == nil {
		f.panicErr, _ = errchain.MatchesAs[*PanicError](e)
	}
	if !f.maxBytes {
		_, f.maxBytes = errchain.MatchesAs[*http.MaxBytesError](e)
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
	_, ok := errchain.As[contract.RecoveredPanic](err)
	return ok
}

// answer resolves the status and headers for an error that is neither a
// recovered panic nor a client-gone cancellation. First match wins: an
// explicit StatusError (with the first HeaderError's headers), a deadline
// (503), an oversized body (413), otherwise 500 with the first
// HeaderError's headers. named is false only in the last case.
func (f *errorFacts) answer() (status int, headers http.Header, named bool) {
	switch {
	case f.haveStatus:
		status, named = f.statusCode, true
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
		headers = f.headers
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
// A 503 whose chain holds contract.ErrServerShuttingDown (a request the
// router refused once its Shutdown began) logs nothing.
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
		case res.status == http.StatusServiceUnavailable && errchain.Is(err, contract.ErrServerShuttingDown):
			// A request the router refused while stopping: an outcome
			// of the shutdown, not a failure, so nothing is logged.
		case res.status >= http.StatusInternalServerError:
			res.level = logError
		}
		if res.status < http.StatusInternalServerError && f.haveMessage && f.messageStatus == res.status {
			res.message = f.clientMessage
		}
		if res.status < http.StatusInternalServerError {
			res.fields = f.fields
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
	return ctx.Err() != nil && errchain.Is(context.Cause(ctx), contract.ErrServerShuttingDown)
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
// contract.ErrResponseWritten outside a recovered panic (a panic the
// router recovers answers 500 whatever its value; an http.ErrAbortHandler
// panic never reaches it), and for a context.Canceled whose request
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
// The response lists X-Inertia in its Vary header and, unless the request
// is an Inertia request, the headers contract.WantsJSON reads (Accept and
// X-Requested-With).
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
// calling it (see SetLogger).
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

// writeDefaultError writes the resolved default response for err. The body
// replaces whatever the failed attempt staged, so the Content-Length it set
// is dropped first. Content-Encoding is left to whoever wraps the writer,
// as http.Error does: a compressing middleware that set it up front
// compresses this body too.
func writeDefaultError(c *Context, err error, res defaultResolution, info ErrorInfo) {
	h := c.Response.Header()
	// The key is a canonical constant, so the map delete equals Header.Del.
	delete(h, "Content-Length")
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
		if res.status < http.StatusInternalServerError {
			body.Errors = res.fields
		}
		raw, mErr := json.Marshal(body)
		if mErr == nil {
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
// reports the error once, and RequestFailed fires with it when the
// response fn wrote has a status of 500 or above (see RequestFailed). fn
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
