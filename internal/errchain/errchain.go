// Package errchain walks an error chain bounded and contained, and reads
// an error's text contained.
//
// The framework inspects errors it did not create: a driver's error, a
// handler's returned error, a listener's Accept error. Their Unwrap, Is,
// As, Error and GRPCStatus methods are user code, so they may panic, and
// their chain may loop back on itself. errors.Is and errors.As follow such
// a chain without bound and let a panic through, which hangs or crashes
// whatever inspects the error, often cleanup that must finish. Walk looks
// at a bounded number of errors and contains a panic, and each caller
// keeps its own classification policy on top of it (internal/sqlerr for a
// database error's kind, the gRPC call lifecycle for a returned error's
// status). Is, As, Unwrap and Text are the contained forms of errors.Is,
// errors.As, errors.Unwrap and an Error call; outside this package the
// framework calls those only through them (scripts/ci/check-error-inspection
// enforces it).
package errchain

import (
	"fmt"
	"reflect"
)

// Max bounds how many errors of one chain a walk looks at, so a chain that
// loops back on itself still ends.
const Max = 32

// Result says how a walk ended.
type Result uint8

const (
	// Ended means the walk looked at every error of the chain.
	Ended Result = iota
	// Stopped means the visit function asked the walk to stop.
	Stopped
	// Truncated means the chain holds more errors than Max and the walk
	// did not look at all of them.
	Truncated
	// Panicked means an Unwrap method, or the visit function, panicked
	// and the walk ended there.
	Panicked
)

// Walk calls visit with err and then with the errors of its chain, until
// visit returns true or the chain ends. The chain is walked breadth first
// through Unwrap() error and Unwrap() []error, looking at no more than Max
// errors, so a chain that loops back on itself ends and a loop in one
// branch of a join does not hide the others. Breadth first differs from
// errors.Is and errors.As, which go depth first, only in which of several
// matches in different branches is met first: the shallowest one here.
//
// A nil error of the chain is skipped but counts toward Max. A panic in an
// Unwrap method or in visit is recovered and ends the walk with Panicked;
// visit's side effects up to the panic stay. Walk of a nil err calls
// nothing and returns Ended.
func Walk(err error, visit func(e error) (stop bool)) (result Result) {
	if err == nil {
		return Ended
	}
	defer func() {
		if recover() != nil {
			result = Panicked
		}
	}()
	// A chain without a join is walked in place: breadth first and depth
	// first are the same order on it, and it needs no queue.
	e, counted := err, 1
	for {
		if e == nil {
			return Ended
		}
		if visit(e) {
			return Stopped
		}
		switch u := e.(type) {
		case interface{ Unwrap() error }:
			next := u.Unwrap()
			if counted == Max {
				return Truncated
			}
			e, counted = next, counted+1
		case interface{ Unwrap() []error }:
			return walkQueue(u.Unwrap(), counted, visit)
		default:
			return Ended
		}
	}
}

// walkQueue walks the branches of a join breadth first, counted errors
// already looked at toward Max. Walk contains its panics.
func walkQueue(branches []error, counted int, visit func(e error) (stop bool)) Result {
	var buf [Max]error
	queue := buf[:0]
	truncated := false
	add := func(next []error) {
		room := Max - counted - len(queue)
		if len(next) > room {
			next, truncated = next[:room], true
		}
		queue = append(queue, next...)
	}
	add(branches)
	for seen := 0; seen < len(queue); seen++ {
		e := queue[seen]
		if e == nil {
			continue
		}
		if visit(e) {
			return Stopped
		}
		switch u := e.(type) {
		case interface{ Unwrap() error }:
			add([]error{u.Unwrap()})
		case interface{ Unwrap() []error }:
			add(u.Unwrap())
		}
	}
	if truncated {
		return Truncated
	}
	return Ended
}

// Matches reports whether e is target, by identity or by e's own Is
// method, without following e's chain (Walk does). It calls user code and
// does not contain it: call it from a visit function, which Walk contains.
// Identity is compared only when target's dynamic type is comparable, as
// errors.Is does; the comparison can still panic for a comparable struct
// holding an incomparable value in an interface field.
func Matches(e, target error) bool {
	if target != nil && reflect.TypeOf(target).Comparable() && e == target {
		return true
	}
	if x, ok := e.(interface{ Is(error) bool }); ok {
		return x.Is(target)
	}
	return false
}

// Is reports whether target is in err's chain, by Walk and Matches. It is
// false when the walk panicked or looked at Max errors without meeting
// target. err identical to a target of pointer kind (every errors.New
// sentinel) answers true before the walk: comparing two pointers calls no
// user code and cannot panic.
func Is(err, target error) bool {
	if target != nil && reflect.TypeOf(target).Kind() == reflect.Pointer && err == target {
		return true
	}
	return Walk(err, func(e error) bool { return Matches(e, target) }) == Stopped
}

// MatchesAs reports whether e itself is a T, by type assertion or by e's
// own As method, and returns it; it does not follow e's chain (As does).
// Like Matches it calls user code and does not contain it: call it from a
// visit function, which Walk contains.
func MatchesAs[T any](e error) (T, bool) {
	if t, ok := e.(T); ok {
		return t, true
	}
	if x, ok := e.(interface{ As(any) bool }); ok {
		// Declared here, so only a node with an As method moves it to
		// the heap.
		p := new(T)
		if x.As(p) {
			return *p, true
		}
	}
	var zero T
	return zero, false
}

// As returns the first T in err's chain, by Walk and MatchesAs: the
// shallowest one, where errors.As returns the first depth first. It is
// false when the walk panicked, or looked at Max errors, before a match.
func As[T any](err error) (T, bool) {
	var (
		found T
		ok    bool
	)
	Walk(err, func(e error) bool {
		found, ok = MatchesAs[T](e)
		return ok
	})
	if !ok {
		var zero T
		return zero, false
	}
	return found, true
}

// Unwrap returns the error err's Unwrap() error method returns, or nil
// when err has none or the method panics.
func Unwrap(err error) (next error) {
	u, ok := err.(interface{ Unwrap() error })
	if !ok {
		return nil
	}
	defer func() {
		if recover() != nil {
			next = nil
		}
	}()
	return u.Unwrap()
}

// Unreadable is the text Text and Sprint return for a value whose Error,
// String or Format method panics. It is fixed: formatting the panic value
// would call user code again.
const Unreadable = "text unavailable: its Error, String or Format method panicked"

// Text returns err's text, "" for a nil err, and Unreadable when err's
// Error method panics. It does not bound how long Error runs or how long
// its text is: an Error that blocks delays only its caller, which must not
// hold a lock while calling Text.
func Text(err error) (text string) {
	if err == nil {
		return ""
	}
	defer func() {
		if recover() != nil {
			text = Unreadable
		}
	}()
	return err.Error()
}

// Sprint returns fmt.Sprint(v) with v's own formatting method called
// contained: when v is a fmt.Formatter, an error or a fmt.Stringer (the
// order fmt consults them for %v), that method is called directly, and a
// panic in it yields Unreadable. A panic fmt raises formatting what v
// holds (a field's String method whose panic value's formatting panics
// too) yields Unreadable as well; one fmt contains itself stays fmt's own
// "%!v(PANIC=...)" text. A nil pointer whose method panics yields
// Unreadable too, where fmt writes "<nil>". Like Text it does not bound how long the method
// runs or how long its text is.
func Sprint(v any) (text string) {
	defer func() {
		if recover() != nil {
			text = Unreadable
		}
	}()
	switch x := v.(type) {
	case nil:
		return fmt.Sprint(nil)
	case fmt.Formatter:
		var s state
		x.Format(&s, 'v')
		return string(s.buf)
	case error:
		return x.Error()
	case fmt.Stringer:
		return x.String()
	}
	return fmt.Sprint(v)
}

// state is the fmt.State Sprint hands a Formatter: the plain %v verb, no
// width, precision or flags.
type state struct{ buf []byte }

func (s *state) Write(b []byte) (int, error) { s.buf = append(s.buf, b...); return len(b), nil }
func (s *state) Width() (int, bool)          { return 0, false }
func (s *state) Precision() (int, bool)      { return 0, false }
func (s *state) Flag(int) bool               { return false }
