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
func StatusOf(err error) (status int, headers http.Header, ok bool) {
	if err == nil {
		return 0, nil, false
	}
	status = http.StatusInternalServerError
	var se StatusError
	if errors.As(err, &se) {
		status = validStatus(se.StatusCode())
		ok = true
	}
	var he HeaderError
	if errors.As(err, &he) {
		headers = he.Headers()
	}
	return status, headers, ok
}

// ErrResponseWritten reports that the response was already written, so the
// error pipeline must not write another. Returned bare, it marks a
// deliberate response with nothing to report; Handled(cause) keeps a cause
// visible for reporting.
var ErrResponseWritten = errors.New("velocity: response already written")

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

// HandledCause returns the cause a Handled value carries anywhere in err's
// chain, or nil (including for a bare ErrResponseWritten).
func HandledCause(err error) error {
	var h *handledError
	if errors.As(err, &h) {
		return h.cause
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
// returns nil; an already-marked err is returned unchanged.
func MarkReported(err error) error {
	if err == nil || IsReported(err) {
		return err
	}
	return &reportedError{err: err}
}

// IsReported reports whether err's chain carries the MarkReported marker.
func IsReported(err error) bool {
	var r *reportedError
	return errors.As(err, &r)
}
