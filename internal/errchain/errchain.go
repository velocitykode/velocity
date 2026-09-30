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
// errors.As, errors.Unwrap and an Error call, and Errorf, Sprintf and
// Sprint the contained forms of fmt's formatting of a value whose type the
// caller does not know; outside this package the framework calls those
// only through them (scripts/ci/check-error-inspection enforces it).
package errchain

import (
	"errors"
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
func Walk(err error, visit func(e error) (stop bool)) Result {
	return WalkSteps(err, func(e error) Step {
		if visit(e) {
			return Stop
		}
		return Descend
	})
}

// Step is what a WalkSteps visit tells the walk to do after an error.
type Step uint8

const (
	// Descend goes on, into the error's own chain.
	Descend Step = iota
	// Stop ends the walk with Stopped.
	Stop
	// Skip goes on without the error's own chain: nothing it unwraps
	// to is visited through it.
	Skip
)

// WalkSteps is Walk whose visit can also leave out an error's own chain
// (Skip), for a classification that must not look below a node (the
// value a recovered panic carries). Order, bound and containment are
// Walk's; an error reached only through a skipped one is not visited and
// does not count toward Max.
func WalkSteps(err error, visit func(e error) Step) (result Result) {
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
		switch visit(e) {
		case Stop:
			return Stopped
		case Skip:
			return Ended
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
// already looked at toward Max. WalkSteps contains its panics.
func walkQueue(branches []error, counted int, visit func(e error) Step) Result {
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
		switch visit(e) {
		case Stop:
			return Stopped
		case Skip:
			continue
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
func Text(err error) string {
	text, _ := ReadText(err)
	return text
}

// ReadText is Text that also reports whether err's Error method returned:
// ok is false, and text Unreadable, when it panicked. For a caller whose
// outcome changes when the text cannot be read.
func ReadText(err error) (text string, ok bool) {
	if err == nil {
		return "", true
	}
	defer func() {
		if recover() != nil {
			text, ok = Unreadable, false
		}
	}()
	return err.Error(), true
}

// Sprint returns fmt.Sprint(v) with v's own formatting method called
// contained: when v is a fmt.Formatter, an error or a fmt.Stringer (the
// order fmt consults them for %v), that method is called directly, and a
// panic in it yields Unreadable. A panic fmt raises formatting what v
// holds (a field's String method whose panic value's formatting panics
// too) yields Unreadable as well; one fmt contains itself stays fmt's own
// "%!v(PANIC=...)" text. A nil pointer whose method panics yields
// Unreadable too, where fmt writes "<nil>". Like Text it does not bound how long the method
// runs or how long its text is. Sprint is ReadValue with Unreadable for a
// value ReadValue cannot read.
func Sprint(v any) string {
	if text, ok := ReadValue(v); ok {
		return text
	}
	return Unreadable
}

// ReadValue returns v's text as Sprint formats it and true, or "" and false
// when a formatting method panicked. It is the form for text that must not
// stand in for a value it could not read: an identity derived from user
// code, where two unreadable values sharing one text would share one key.
func ReadValue(v any) (text string, ok bool) {
	defer func() {
		if recover() != nil {
			text, ok = "", false
		}
	}()
	switch x := v.(type) {
	case nil:
		return fmt.Sprint(nil), true
	case string:
		// The common log value: fmt would copy it to the same text.
		return x, true
	case fmt.Formatter:
		var s state
		x.Format(&s, 'v')
		return string(s.buf), true
	case error:
		return x.Error(), true
	case fmt.Stringer:
		return x.String(), true
	}
	return fmt.Sprint(v), true
}

// state is the fmt.State ReadValue hands a Formatter: the plain %v verb, no
// width, precision or flags.
type state struct{ buf []byte }

func (s *state) Write(b []byte) (int, error) { s.buf = append(s.buf, b...); return len(b), nil }
func (s *state) Width() (int, bool)          { return 0, false }
func (s *state) Precision() (int, bool)      { return 0, false }
func (s *state) Flag(int) bool               { return false }

// Errorf returns fmt.Errorf(format, args...), contained. fmt recovers a
// panic in an operand's Error, String or Format method once and writes
// "%!v(PANIC=...)", but it formats the panic value too, and a panic there
// crashes the caller. On such a panic Errorf formats again with every
// operand that is not a plain value (a boolean, a number, a string or a
// byte slice, of a type without methods) standing in as its Sprint text,
// so the hostile operand reads Unreadable and the rest of the message
// stays. The result unwraps to the original %w operands, as fmt.Errorf's
// does; a %T or %p of a stand-in names the stand-in. For a benign operand the result is
// fmt.Errorf's own, text and type. Like Text it does not bound how long a
// method runs.
func Errorf(format string, args ...any) (err error) {
	defer func() {
		if recover() != nil {
			err = standInErrorf(format, args)
		}
	}()
	return fmt.Errorf(format, args...)
}

// Sprintf returns fmt.Sprintf(format, args...), contained as Errorf is.
func Sprintf(format string, args ...any) (text string) {
	defer func() {
		if recover() != nil {
			text = standInSprintf(format, args)
		}
	}()
	return fmt.Sprintf(format, args...)
}

// standInErrorf is Errorf's second formatting, with stand-ins: its text
// is fmt's over the stand-ins, and it unwraps to the original %w operands,
// as fmt.Errorf's result does, never to a stand-in. Every stand-in formats
// contained, so it cannot panic; the recover is a last guard, and an error
// of fixed text wrapping the same operands is what it yields.
func standInErrorf(format string, args []any) (err error) {
	text := Unreadable
	var wrapped []error
	defer func() {
		if recover() != nil {
			err = newWrapped(Unreadable, wrapped)
		}
	}()
	e := fmt.Errorf(format, standIns(args)...)
	text = e.Error()
	switch u := e.(type) {
	case interface{ Unwrap() error }:
		wrapped = originals([]error{u.Unwrap()})
	case interface{ Unwrap() []error }:
		wrapped = originals(u.Unwrap())
	}
	return newWrapped(text, wrapped)
}

// originals maps the stand-ins fmt wrapped back to their operands.
func originals(errs []error) []error {
	out := make([]error, 0, len(errs))
	for _, e := range errs {
		if s, ok := e.(*errorStandIn); ok {
			e = s.err
		}
		out = append(out, e)
	}
	return out
}

// newWrapped returns an error of text unwrapping to errs the way
// fmt.Errorf's result does: none, one (Unwrap() error) or several
// (Unwrap() []error).
func newWrapped(text string, errs []error) error {
	switch len(errs) {
	case 0:
		return errors.New(text)
	case 1:
		return &wrapError{text: text, err: errs[0]}
	}
	return &wrapErrors{text: text, errs: errs}
}

type wrapError struct {
	text string
	err  error
}

func (e *wrapError) Error() string { return e.text }
func (e *wrapError) Unwrap() error { return e.err }

type wrapErrors struct {
	text string
	errs []error
}

func (e *wrapErrors) Error() string   { return e.text }
func (e *wrapErrors) Unwrap() []error { return e.errs }

// standInSprintf is Sprintf's second formatting, guarded as standInErrorf.
func standInSprintf(format string, args []any) (text string) {
	defer func() {
		if recover() != nil {
			text = Unreadable
		}
	}()
	return fmt.Sprintf(format, standIns(args)...)
}

// standIns returns args with every operand that can reach user code
// replaced by a stand-in.
func standIns(args []any) []any {
	out := make([]any, len(args))
	for i, a := range args {
		if e, ok := a.(error); ok {
			out[i] = &errorStandIn{err: e}
		} else if plain(a) {
			out[i] = a
		} else {
			out[i] = &standIn{v: a}
		}
	}
	return out
}

// plain reports whether fmt formats a without calling a method: nil, or a
// boolean, number, string or byte slice of a type without methods.
func plain(a any) bool {
	t := reflect.TypeOf(a)
	if t == nil {
		return true
	}
	if t.NumMethod() != 0 {
		return false
	}
	switch k := t.Kind(); {
	case k >= reflect.Bool && k <= reflect.Complex128, k == reflect.String:
		return true
	case k == reflect.Slice:
		return t.Elem().Kind() == reflect.Uint8 && t.Elem().NumMethod() == 0
	}
	return false
}

// standIn formats as its value's Sprint text, under the directive's verb,
// flags, width and precision (%w reads as %v).
type standIn struct{ v any }

func (s *standIn) Format(f fmt.State, verb rune) { formatText(f, verb, Sprint(s.v)) }

// errorStandIn is a standIn for an error operand: an error itself, so %w
// accepts it, and one that unwraps to the operand.
type errorStandIn struct{ err error }

func (s *errorStandIn) Error() string                 { return Text(s.err) }
func (s *errorStandIn) Unwrap() error                 { return s.err }
func (s *errorStandIn) Format(f fmt.State, verb rune) { formatText(f, verb, Sprint(s.err)) }

func formatText(f fmt.State, verb rune, text string) {
	if verb == 'w' {
		verb = 'v'
	}
	fmt.Fprintf(f, fmt.FormatString(f, verb), text)
}
