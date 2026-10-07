// Package panicerr provides a shared helper for converting recovered panic
// values into errors. All framework goroutines use this instead of ad-hoc
// panic-to-error conversion.
package panicerr

import (
	"github.com/velocitykode/velocity/internal/errchain"
)

// Error is the typed representation of a recovered panic. It is returned as
// an `error` (so the existing `error` consumers keep working) but exposes the
// raw recovered value via `Recovered()` for callers that want to inspect or
// type-assert the original panic.
//
// `errors.Is(err, e)` works for the wrapped chain when the recovered value
// itself was an error.
type Error struct {
	value any
}

// New constructs a typed panic error from a recovered value. Pass the result
// of `recover()`. Returns nil if value is nil so callers can write
// `if err := panicerr.New(recover()); err != nil { ... }`.
func New(value any) *Error {
	if value == nil {
		return nil
	}
	return &Error{value: value}
}

// Recovered returns the raw value handed to recover(). Useful for callers
// that want to inspect the original panic (e.g. type-assert to a custom
// panic struct) rather than the formatted message.
func (e *Error) Recovered() any {
	if e == nil {
		return nil
	}
	return e.value
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	if err, ok := e.value.(error); ok {
		return "panic: " + errchain.Text(err)
	}
	return "panic: " + errchain.Sprint(e.value)
}

// Unwrap returns the underlying error if the recovered value was an error,
// so errors.Is / errors.As walk the chain.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	if err, ok := e.value.(error); ok {
		return err
	}
	return nil
}

// FromRecovered converts a recovered panic value into an error.
// If the recovered value is already an error, it is wrapped so that
// errors.Is / errors.As work on the original. Otherwise the value is
// formatted with %v.
//
// Returns a `*Error` typed as `error` so existing call sites that store
// the result in an `error` variable continue to work; new callers may
// type-assert to `*Error` (or use `errors.As`) to access `Recovered()`.
//
// A *Listener is converted as the value it carries, so a commit listener's
// panic reads the same wherever it is recovered.
func FromRecovered(r any) error {
	if r == nil {
		return nil
	}
	if l, ok := r.(*Listener); ok && l != nil {
		r = l.value
	}
	return &Error{value: r}
}

// Listener is the value a response's commit listener panic travels as: the
// router's writer recovers the listener's own value where the listener
// ran and panics again with a *Listener carrying it. The type is what
// tells the origin of a panic to the frames it unwinds through: the router
// contains a listener's panic itself (it answers it and runs the listeners
// still pending), so a recovery frame between the write and the router
// that cannot do that passes a *Listener on, and the router retries its
// answer only for one. A value's origin cannot be told from the outside
// any other way: the same write may panic for a reason of its own.
//
// It also names the commit owner whose listener panicked. One request can
// meet several owners (an error handler that renders through router.Wrap
// has its own), and a panic of another owner's listener says nothing about
// the listeners this owner still holds: see From.
//
// It is never built for http.ErrAbortHandler, which travels as itself so
// net/http recognises it.
//
// A nil *Listener is not a mark. User code can raise one (a listener that
// panics with a typed nil of this type), so every function here that takes
// a recovered value treats it as the plain value it is, and the methods
// answer on a nil receiver as an empty value would.
type Listener struct {
	value any
	owner any
}

// NewListener carries value, the value a listener of owner panicked with.
// A value that already is a *Listener (a listener of owner ran code whose
// own owner's listener panicked) is carried as the value it holds: the
// panic now is owner's listener's.
func NewListener(value, owner any) *Listener {
	if l, ok := value.(*Listener); ok && l != nil {
		value = l.value
	}
	return &Listener{value: value, owner: owner}
}

// From reports whether the panic came from a listener of owner.
func (l *Listener) From(owner any) bool {
	return l != nil && l.owner == owner
}

// Recovered returns the value the listener panicked with.
func (l *Listener) Recovered() any {
	if l == nil {
		return nil
	}
	return l.value
}

// Error describes the panic as FromRecovered does for the carried value.
func (l *Listener) Error() string {
	if l == nil {
		return (&Error{}).Error()
	}
	return (&Error{value: l.value}).Error()
}

// Unwrap returns the carried value when it is an error.
func (l *Listener) Unwrap() error {
	if l == nil {
		return nil
	}
	if err, ok := l.value.(error); ok {
		return err
	}
	return nil
}

// IsListener reports whether p, a recovered value, is a commit listener's
// panic.
func IsListener(p any) bool {
	l, ok := p.(*Listener)
	return ok && l != nil
}

// IsListenerOf reports whether p, a recovered value, is the panic of a
// listener of owner.
func IsListenerOf(p, owner any) bool {
	l, ok := p.(*Listener)
	return ok && l.From(owner)
}

// AsTyped extracts a *Error from any error value, returning nil if the error
// is not (and does not wrap) a panic-error.
func AsTyped(err error) *Error {
	if pe, ok := errchain.As[*Error](err); ok {
		return pe
	}
	return nil
}
