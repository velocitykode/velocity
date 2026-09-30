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
