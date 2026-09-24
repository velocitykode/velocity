package contract

import (
	"errors"
	"fmt"
	"net/http"
	"runtime"
	"strconv"
	"strings"
)

// RegistrationError is a typed error for registration-time failures.
// Methods that cannot return error (e.g. Router.Get) panic with this type
// so misuse is loud at bootstrap and debuggable with recover.
type RegistrationError struct {
	Package string
	Message string
}

func (e *RegistrationError) Error() string {
	return fmt.Sprintf("velocity/%s: %s", e.Package, e.Message)
}

// NewRegistrationError creates a new RegistrationError.
func NewRegistrationError(pkg, msg string) *RegistrationError {
	return &RegistrationError{Package: pkg, Message: msg}
}

// Cross-package sentinel errors.
//
// These errors are owned by the contract package so callers can match
// "not found" / "invalid key" outcomes uniformly across driver boundaries
// without importing the concrete driver package. Each owning package
// re-exports the same identity under its conventional name (e.g.
// queue.ErrJobNotFound), so existing errors.Is(err, queue.ErrJobNotFound)
// continues to match.
//
// Scope is intentionally narrow: only sentinels that callers reasonably
// check across package boundaries are hoisted here. Sentinels that are
// purely diagnostic or scoped to a single driver remain local.
//
// Stability is enforced by TestSentinelStability (contract/errors_test.go).
var (
	// ErrJobNotFound is returned when a queue job lookup fails (Find by id,
	// failed_jobs lookup, etc.). Hoisted from queue.ErrJobNotFound.
	ErrJobNotFound = errors.New("velocity/queue: job not found")

	// ErrBatchNotFound is returned by batch repository lookups when the
	// batch id is unknown. Hoisted from queue.ErrBatchNotFound.
	ErrBatchNotFound = errors.New("velocity/queue: batch not found")

	// ErrCacheStoreNotFound is returned by the cache manager when a named
	// store has not been registered. Hoisted from cache.ErrStoreNotFound.
	ErrCacheStoreNotFound = errors.New("velocity/cache: store not found")

	// ErrCacheKeyNotFound is returned by cache typed-lookup helpers when
	// the key is absent. Hoisted from cache.ErrKeyNotFound.
	ErrCacheKeyNotFound = errors.New("velocity/cache: key not found")

	// ErrFileNotFound is returned by storage drivers when a path does not
	// exist. Hoisted from storage.ErrFileNotFound.
	ErrFileNotFound = errors.New("velocity/storage: file not found")

	// ErrDiskNotFound is returned by the storage manager when a named disk
	// has not been configured. Hoisted from storage.ErrDiskNotFound.
	ErrDiskNotFound = errors.New("velocity/storage: disk not found")

	// ErrBroadcastDriverNotFound is returned when a broadcast driver is
	// not registered under the requested name. Hoisted from
	// broadcast.ErrDriverNotFound.
	ErrBroadcastDriverNotFound = errors.New("velocity/broadcast: driver not found")

	// ErrInvalidKey is returned by the crypto subsystem when an encryption
	// key is empty, malformed, or the wrong length for the configured
	// cipher. Hoisted from crypto.ErrInvalidKey.
	ErrInvalidKey = errors.New("velocity/crypto: invalid encryption key")

	// ErrInvalidPreviousKey is returned when an entry in Config.PreviousKeys
	// is malformed (bad base64, wrong length, etc.). Hoisted from
	// crypto.ErrInvalidPreviousKey.
	ErrInvalidPreviousKey = errors.New("velocity/crypto: invalid previous key")

	// ErrInvalidPayload is returned by crypto drivers when the ciphertext
	// envelope is structurally invalid (empty, wrong version, truncated).
	// Distinct from ErrDecrypt: structural defects vs. cryptographic failure.
	// Hoisted from crypto/drivers.ErrInvalidPayload.
	ErrInvalidPayload = errors.New("velocity/crypto: invalid payload format")

	// ErrInvalidCipher is returned when the configured cipher name is
	// unknown (config validation, driver construction). The framework's
	// AES driver binds AAD in both GCM (AEAD tag) and CBC (HMAC framing)
	// modes, so the *WithAAD methods no longer reject CBC with this
	// sentinel; third-party drivers without any way to authenticate AAD
	// may still return it from those methods.
	// Hoisted from crypto/drivers.ErrInvalidCipher.
	ErrInvalidCipher = errors.New("velocity/crypto: unsupported cipher")
)

// HTTPError is the framework's one HTTP-shaped error value. Router, auth,
// csrf, validation and application code all construct it (directly or via
// NewHTTPError) so the error pipeline resolves status, headers and message
// the same way wherever the error came from.
//
// The fields are exported so packages can build the value as a literal.
// A literal carries no origin; NewHTTPError and WithOrigin record one.
type HTTPError struct {
	// Status is the HTTP status code. A value outside 100-999 resolves to
	// 500 through StatusCode.
	Status int
	// Message is the client-facing text. Empty means the status text.
	Message string
	// Header holds the response headers the error carries, for example
	// Retry-After or Allow. Treat it as read-only once the error is
	// returned; add entries through WithHeader.
	Header http.Header
	// Cause is the wrapped error. It reaches logs and the debug page
	// through Error and Unwrap, never a client outside debug mode.
	Cause error

	// pc is the program counter of the constructing caller, symbolised
	// only when Origin is called.
	pc uintptr
}

// NewHTTPError returns an HTTPError for status. The first message, when
// given and non-empty, becomes Message; otherwise Message is the standard
// status text. The caller's program counter is recorded for Origin.
func NewHTTPError(status int, message ...string) *HTTPError {
	e := &HTTPError{Status: status}
	if len(message) > 0 && message[0] != "" {
		e.Message = message[0]
	} else {
		e.Message = http.StatusText(status)
	}
	e.pc = callerPC(3)
	return e
}

// callerPC returns one program counter skip frames above itself
// (runtime.Callers counting), or 0 when the stack is shallower.
func callerPC(skip int) uintptr {
	var pcs [1]uintptr
	if runtime.Callers(skip, pcs[:]) == 0 {
		return 0
	}
	return pcs[0]
}

// Error returns Message (or the status text when Message is empty),
// followed by the cause chain when Cause is set. This text is for logs and
// the debug page; renderers echo only Message, and only for 4xx.
func (e *HTTPError) Error() string {
	if e == nil {
		return ""
	}
	msg := e.Message
	if msg == "" {
		msg = statusText(e.StatusCode())
	}
	if e.Cause != nil {
		return msg + ": " + e.Cause.Error()
	}
	return msg
}

// StatusCode returns Status, or 500 when Status is outside 100-999 (which
// net/http would refuse to write).
func (e *HTTPError) StatusCode() int {
	if e == nil {
		return http.StatusInternalServerError
	}
	return validStatus(e.Status)
}

// Headers returns the headers the error carries, or nil.
func (e *HTTPError) Headers() http.Header {
	if e == nil {
		return nil
	}
	return e.Header
}

// Unwrap returns Cause so errors.Is and errors.As reach the wrapped chain.
func (e *HTTPError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

// ClientMessage returns Message, the text a 4xx answer at this error's
// status echoes to the client.
func (e *HTTPError) ClientMessage() string {
	if e == nil {
		return ""
	}
	return e.Message
}

// ShouldReport reports whether the error is server-side: status 500 and
// above.
func (e *HTTPError) ShouldReport() bool {
	return e.StatusCode() >= http.StatusInternalServerError
}

// WithHeader sets header key to value and returns e for chaining. A key or
// value containing CR or LF, or an empty key, is dropped so a header can
// never split the response.
func (e *HTTPError) WithHeader(key, value string) *HTTPError {
	if e == nil || key == "" || hasCRLF(key) || hasCRLF(value) {
		return e
	}
	if e.Header == nil {
		e.Header = make(http.Header)
	}
	e.Header.Set(key, value)
	return e
}

// WithCause sets Cause and returns e for chaining.
func (e *HTTPError) WithCause(err error) *HTTPError {
	if e == nil {
		return nil
	}
	e.Cause = err
	return e
}

// WithOrigin records the caller skip frames above the caller of WithOrigin
// as the error's origin and returns e. Skip 0 records the function calling
// WithOrigin; a constructor that wraps NewHTTPError passes 1 so the origin
// is its own caller rather than itself.
func (e *HTTPError) WithOrigin(skip int) *HTTPError {
	if e == nil {
		return nil
	}
	if skip < 0 {
		skip = 0
	}
	e.pc = callerPC(3 + skip)
	return e
}

// Origin returns "file:line function" for the recorded caller, or "" when
// no origin was recorded. Symbolisation happens here, not at construction.
func (e *HTTPError) Origin() string {
	if e == nil || e.pc == 0 {
		return ""
	}
	frame, _ := runtime.CallersFrames([]uintptr{e.pc}).Next()
	if frame.File == "" {
		return frame.Function
	}
	return frame.File + ":" + strconv.Itoa(frame.Line) + " " + frame.Function
}

// StatusTokenMismatch is the status a rejected CSRF token answers with.
const StatusTokenMismatch = 419

// StatusTitle returns the short title for status that every error body
// uses: the standard status text, "Page Expired" for StatusTokenMismatch,
// or "Error" for a code net/http does not name.
func StatusTitle(status int) string {
	if status == StatusTokenMismatch {
		return "Page Expired"
	}
	if t := http.StatusText(status); t != "" {
		return t
	}
	return "Error"
}

// statusText returns the standard text for status, or "status N" for a
// code net/http does not name.
func statusText(status int) string {
	if t := http.StatusText(status); t != "" {
		return t
	}
	return "status " + strconv.Itoa(status)
}

// validStatus maps a status outside 100-999 to 500.
func validStatus(status int) int {
	if status < 100 || status > 999 {
		return http.StatusInternalServerError
	}
	return status
}

// hasCRLF reports whether s contains a carriage return or line feed.
func hasCRLF(s string) bool {
	return strings.ContainsAny(s, "\r\n")
}

// StatusError is an error that names its HTTP status.
type StatusError interface {
	error
	StatusCode() int
}

// MessageError is a StatusError that carries its own client-facing
// message. A 4xx answer at the error's status echoes ClientMessage (an
// empty one means the status title); a 5xx answer never shows it. The
// first MessageError in an error's chain (errors.As) is the one read.
// HTTPError implements it through Message.
type MessageError interface {
	StatusError
	ClientMessage() string
}

// HeaderError is an error that carries response headers.
type HeaderError interface {
	error
	Headers() http.Header
}

// Reportable is an error that decides whether it is reported.
type Reportable interface {
	error
	ShouldReport() bool
}

// Renderable is an error that renders its own response. RenderError
// returns true when it wrote the response and false to fall through to the
// pipeline's own rendering.
type Renderable interface {
	error
	RenderError(rc RenderContext, ctx *ErrorContext) bool
}

// SelfReporting is an error that reports itself. ReportError returns true
// when reporting is complete, which stops the configured reporters from
// seeing the error; false continues to them.
type SelfReporting interface {
	error
	ReportError(ctx *ErrorContext) bool
}

// Contextual is an error that contributes structured fields to its report.
// The pipeline merges Context into ErrorContext.Extra.
type Contextual interface {
	error
	Context() map[string]any
}

// ExitCoder is an error that names the process exit code a console command
// failing with it should return.
type ExitCoder interface {
	error
	ExitCode() int
}

// StatusOf resolves the HTTP status and response headers for err. The first
// StatusError in err's chain (errors.As) names the status and ok is true;
// otherwise status is 500 and ok is false. Headers come separately from the
// first HeaderError in the chain, so an error that names only a status
// still resolves. A status outside 100-999 resolves to 500. StatusOf(nil)
// returns 0, nil, false.
//
// The chain is walked by hand in the order errors.As visits it, so the
// common case (no node with an As method) allocates nothing; a node that
// has an As method, or one deeper than the walk's limit, is handed to
// errors.As, which keeps the answer identical to errors.As for any chain.
func StatusOf(err error) (status int, headers http.Header, ok bool) {
	if err == nil {
		return 0, nil, false
	}
	var f statusFinder
	f.walk(err, 0)
	status = http.StatusInternalServerError
	if f.haveStatus {
		status = validStatus(f.status.StatusCode())
		ok = true
	}
	if f.haveHeader {
		headers = f.header.Headers()
	}
	return status, headers, ok
}

// chainWalkLimit bounds how many nodes deep a hand walk of an error chain
// goes before it hands the rest of that branch to the errors package, whose
// answer is the same at any depth.
const chainWalkLimit = 64

// statusFinder collects the first StatusError and the first HeaderError of
// an error chain in one walk.
type statusFinder struct {
	status     StatusError
	header     HeaderError
	haveStatus bool
	haveHeader bool
}

// done reports whether both targets were found.
func (f *statusFinder) done() bool {
	return f.haveStatus && f.haveHeader
}

// walk visits err's chain depth-first in errors.As order (the node, its
// As method, then Unwrap() error or each Unwrap() []error branch) and
// records the first match for each target. It returns true once both
// targets are found.
func (f *statusFinder) walk(err error, depth int) bool {
	for err != nil {
		if depth >= chainWalkLimit {
			f.fallback(err)
			return f.done()
		}
		if !f.haveStatus {
			if se, ok := err.(StatusError); ok {
				f.status, f.haveStatus = se, true
			}
		}
		if !f.haveHeader {
			if he, ok := err.(HeaderError); ok {
				f.header, f.haveHeader = he, true
			}
		}
		if f.done() {
			return true
		}
		if _, ok := err.(interface{ As(any) bool }); ok {
			f.fallback(err)
			return f.done()
		}
		switch x := err.(type) {
		case interface{ Unwrap() error }:
			err = x.Unwrap()
			depth++
		case interface{ Unwrap() []error }:
			for _, e := range x.Unwrap() {
				if e != nil && f.walk(e, depth+1) {
					return true
				}
			}
			return false
		default:
			return false
		}
	}
	return false
}

// fallback resolves the targets still missing over err's whole branch
// through errors.As.
func (f *statusFinder) fallback(err error) {
	if !f.haveStatus {
		var se StatusError
		if errors.As(err, &se) {
			f.status, f.haveStatus = se, true
		}
	}
	if !f.haveHeader {
		var he HeaderError
		if errors.As(err, &he) {
			f.header, f.haveHeader = he, true
		}
	}
}

// ErrResponseWritten reports that the response was already written, so the
// error pipeline must not write another. Returned bare, it marks a
// deliberate response with nothing to report; Handled(cause) keeps a cause
// visible for reporting.
var ErrResponseWritten = errors.New("velocity: response already written")

// ErrServerShuttingDown is the cause (context.Cause) of a request context
// the server cancelled because it is shutting down: the in-flight request
// outlived the graceful drain. The error boundaries answer such a request
// 503 with Retry-After and Connection: close, log it at warn and do not
// report it; a request context done for any other cause means the client
// went away, and nothing is written.
var ErrServerShuttingDown = errors.New("velocity: server shutting down")

// RecoveredPanic is an error carrying the value a recovered panic handed
// to recover(). The framework's own recovered-panic error implements it;
// so may any other error that stands for a recovered panic.
//
// A RecoveredPanic node is the boundary of the panic in an error chain:
// the marker predicates (IsReported, IsResponseWritten, HandledCause)
// never look at it or below it, because a panic is a bug whatever its
// value carries. A marker wrapped around the node (a middleware that
// reported or rendered the recovered panic) still counts.
type RecoveredPanic interface {
	error
	Recovered() any
}

// handledError is the value Handled returns: errors.Is matches
// ErrResponseWritten, and Unwrap returns the cause.
type handledError struct {
	cause error
}

func (h *handledError) Error() string {
	return ErrResponseWritten.Error() + ": " + h.cause.Error()
}

func (h *handledError) Is(target error) bool {
	return target == ErrResponseWritten
}

func (h *handledError) Unwrap() error {
	return h.cause
}

// Handled marks cause as already rendered: the result matches
// ErrResponseWritten under errors.Is and unwraps to cause, so the pipeline
// writes nothing yet still reports cause once. Handled(nil) returns
// ErrResponseWritten.
func Handled(cause error) error {
	if cause == nil {
		return ErrResponseWritten
	}
	return &handledError{cause: cause}
}

// IsResponseWritten reports whether err marks a response written on
// purpose: a node of err's chain outside any RecoveredPanic matches
// ErrResponseWritten under errors.Is (the bare sentinel or a Handled
// value). A marker inside a recovered panic's value does not count.
func IsResponseWritten(err error) bool {
	return findMarker(err, markWritten) != nil
}

// HandledCause returns the cause the first Handled value in err's chain
// outside any RecoveredPanic carries, or nil (including for a bare
// ErrResponseWritten, and for a Handled value inside a recovered panic's
// value).
func HandledCause(err error) error {
	if h := findMarker(err, markHandled); h != nil {
		return errors.Unwrap(h)
	}
	return nil
}

// reportedError is the report-once marker MarkReported wraps around an
// error. It is transparent: same text, and Unwrap exposes the original.
type reportedError struct {
	err error
}

func (r *reportedError) Error() string {
	return r.err.Error()
}

func (r *reportedError) Unwrap() error {
	return r.err
}

// MarkReported records inside the error value that err has been reported,
// so a later report gate skips it. The wrapper is transparent to
// errors.Is, errors.As and errors.Unwrap and keeps err's text. A nil err
// returns nil; an err IsReported already holds for is returned unchanged.
// A marker inside a recovered panic's value does not count, so a recovered
// panic whose value was marked is wrapped: the report of the panic itself
// is recorded around it.
func MarkReported(err error) error {
	if err == nil || IsReported(err) {
		return err
	}
	return &reportedError{err: err}
}

// IsReported reports whether err carries the MarkReported marker on a node
// of its chain outside any RecoveredPanic. A marker inside a recovered
// panic's value does not count.
func IsReported(err error) bool {
	return findMarker(err, markReported) != nil
}

// markerKind selects the marker findMarker looks for.
type markerKind int

const (
	// markReported matches a reportedError node.
	markReported markerKind = iota
	// markWritten matches a node errors.Is would match against
	// ErrResponseWritten (equality or an Is method).
	markWritten
	// markHandled matches a handledError node.
	markHandled
)

// markerWalkCap bounds how many nodes of an error chain findMarker visits.
// Past the cap the answer is "no marker", the safe side: an error whose
// marker lies deeper is reported and rendered.
const markerWalkCap = 1024

// joinFrame is one Unwrap() []error node findMarker is part way through:
// its branches and the index of the next one to visit.
type joinFrame struct {
	errs []error
	next int
}

// findMarker walks err's chain depth-first in the order errors.As and
// errors.Is visit it (the node, then Unwrap() error or each Unwrap()
// []error branch) and returns the first node matching kind, or nil. It
// never looks at a RecoveredPanic node or below it, at any depth. A node
// with an As method is asked for the marker type the way errors.As would
// ask it. The walk is iterative and visits at most markerWalkCap nodes;
// a marker past the cap is not found. Matching uses type assertions only
// and the pending joins live in a stack buffer, so the walk allocates
// nothing unless a node's As method does or joins nest deeper than the
// buffer.
func findMarker(err error, kind markerKind) error {
	var buf [8]joinFrame
	joins := buf[:0]
	visited := 0
	for {
		for err != nil {
			if visited >= markerWalkCap {
				return nil
			}
			visited++
			if _, ok := err.(RecoveredPanic); ok {
				err = nil
				continue
			}
			if m := markerNode(err, kind); m != nil {
				return m
			}
			switch x := err.(type) {
			case interface{ Unwrap() error }:
				err = x.Unwrap()
			case interface{ Unwrap() []error }:
				joins = append(joins, joinFrame{errs: x.Unwrap()})
				err = nil
			default:
				err = nil
			}
		}
		for err == nil {
			if len(joins) == 0 {
				return nil
			}
			top := &joins[len(joins)-1]
			if top.next >= len(top.errs) {
				joins = joins[:len(joins)-1]
				continue
			}
			err = top.errs[top.next]
			top.next++
		}
	}
}

// markerNode returns the marker of kind err itself carries, or nil.
func markerNode(err error, kind markerKind) error {
	switch kind {
	case markReported:
		if r, ok := err.(*reportedError); ok {
			return r
		}
		if x, ok := err.(interface{ As(any) bool }); ok {
			var r *reportedError
			if x.As(&r) && r != nil {
				return r
			}
		}
	case markWritten:
		if err == ErrResponseWritten {
			return err
		}
		if x, ok := err.(interface{ Is(error) bool }); ok && x.Is(ErrResponseWritten) {
			return err
		}
	case markHandled:
		if h, ok := err.(*handledError); ok {
			return h
		}
		if x, ok := err.(interface{ As(any) bool }); ok {
			var h *handledError
			if x.As(&h) && h != nil {
				return h
			}
		}
	}
	return nil
}
