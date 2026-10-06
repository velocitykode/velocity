package cases

import (
	"errors"
	"fmt"
	"io"
)

type logger interface {
	Error(msg string, kvs ...any)
}

type concrete struct{ next error }

func (c *concrete) Error() string { return "concrete" }
func (c *concrete) Unwrap() error { return c.next }
func (c *concrete) Is(error) bool { return false }
func (c *concrete) As(any) bool   { return false }

// wrapper delegates its text to a user error.
type wrapper struct{ err error }

func (w wrapper) Error() string { return w.err.Error() } // want text

type statusErr interface {
	error
	Status() int
}

func Cases(err error, l logger, c *concrete, s statusErr, x fmt.Stringer) {
	_ = errors.Is(err, io.EOF) // want is
	var c2 *concrete
	_ = errors.As(err, &c2)      // want as
	_ = errors.Unwrap(err)       // want unwrap
	_ = err.Error()              // want text
	_ = s.Error()                // want text
	_ = s.Status()               // not an error method
	l.Error("failed", "e", err)  // a logger's Error(msg, kvs...) is not one
	_ = c.Error()                // concrete type
	_ = c.Unwrap()               // concrete type
	_ = x.String()               // not an error method
	_ = fmt.Sprint(err)          // want format
	_ = fmt.Errorf("w: %w", err) // want format
	_ = errors.New("x")
	_ = errors.Join(err)

	if u, ok := err.(interface{ Unwrap() error }); ok {
		_ = u.Unwrap() // want unwrap
	}
	if u, ok := err.(interface{ Unwrap() []error }); ok {
		_ = u.Unwrap() // want unwrap
	}
	if i, ok := err.(interface{ Is(error) bool }); ok {
		_ = i.Is(io.EOF) // want is
	}
	if a, ok := err.(interface{ As(any) bool }); ok {
		_ = a.As(&c2) // want as
	}
	f := func() { _ = errors.Is(err, io.ErrUnexpectedEOF) } // want is
	f()
}

var initErr = func() bool { return errors.Is(io.EOF, io.EOF) }() // want is

func Markers(err error) {
	_ = err.Error() //error-inspection-ok: text of a sentinel this package made
	_ = err.Error() /* want text */ //error-inspection-ok:
	_ = err.Error() /* want text */ //error-inspection-ok: x
}

func Stale() {
	_ = 1 //error-inspection-ok: nothing is inspected here // want stale
}

func Format[T any](err error, v any, x fmt.Stringer, w io.Writer, kvs []any, c *concrete, t T, n int, s string) {
	_ = fmt.Sprintf("%v", v)         // want format
	_ = fmt.Sprintf("%s", x)         // want format
	_ = fmt.Sprintf("%d %s", n, s)   // plain operands
	_ = fmt.Sprintf("%v", c)         // concrete type: the module's own method
	_ = fmt.Sprintf("%v", nil)       // untyped nil
	_ = fmt.Sprint(kvs...)           // want format
	_ = fmt.Sprintln(n, s)           // plain operands
	_ = fmt.Sprintf("%v", t)         // want format
	_, _ = fmt.Fprintf(w, "%d\n", n) // the writer is not an operand
	_, _ = fmt.Fprintf(w, "%v", err) // want format
	_, _ = fmt.Fprintln(w, err)      // want format
	_ = fmt.Appendf(nil, "%v", err)  // want format
	_ = fmt.Append(nil, n)           // plain operand
	_ = fmt.Errorf("plain %d", n)    // plain operand
	_ = fmt.Errorf("w: %w", err)     /* want format */ //error-inspection-ok: bare
	_ = fmt.Errorf("w: %w", err)     //error-inspection-ok: a sentinel this package made
}

// Store is an interface this module declares; StoreAlias names it too.
type Store interface{ Get() int }

type StoreAlias = Store

func Boundary(s Store, a StoreAlias, e error, v any, x fmt.Stringer, c *concrete) bool {
	if s == nil { // want nil
		return true
	}
	if nil != a { // want nil
		return true
	}
	_ = e == nil                           // error: a non-nil error is an error
	_ = v == nil                           // the empty interface
	_ = x == nil                           // declared outside the module
	_ = c == nil                           // concrete pointer
	f := func() bool { return (s) == nil } // want nil
	local := s
	_ = local == nil // a local, not the parameter
	return f()
}

func unexportedBoundary(s Store) bool { return s == nil } // not callable from outside

type hidden struct{}

func (hidden) Exported(s Store) bool { return s == nil } // want nil

type Shown struct{}

func (*Shown) Set(s Store) bool { return s == nil } // want nil
func (*Shown) set(s Store) bool { return s == nil } // not exported

type Box[T any] struct{}

func (Box[T]) Put(s Store) bool { return s != nil } // want nil

func NilMarkers(s Store) bool {
	_ = s == nil    //error-inspection-ok: every caller passes a value type
	return s == nil /* want nil */ //error-inspection-ok: x
}

var _ = unexportedBoundary
var _ = (*Shown).set

// AnonAlias names an interface with no name of its own; the module still
// declares it. EmptyAlias is the empty interface under another name.
type AnonAlias = interface{ Get() int }

type AnonAliasAlias = AnonAlias

type EmptyAlias = interface{}

func AnonBoundary(s AnonAlias, a AnonAliasAlias, e EmptyAlias, in interface{ Get() int }) bool {
	_ = e == nil  // the empty interface
	_ = in == nil // an interface literal in the signature, declared nowhere
	if a == nil { // want nil
		return true
	}
	return s == nil // want nil
}
