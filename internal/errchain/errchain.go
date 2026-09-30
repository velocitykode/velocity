// Package errchain walks an error chain bounded and contained.
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
// status).
package errchain

import "reflect"

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
	var buf [Max]error
	queue := append(buf[:0], err)
	truncated := false
	for seen := 0; seen < len(queue); seen++ {
		e := queue[seen]
		if e == nil {
			continue
		}
		if visit(e) {
			return Stopped
		}
		var next []error
		switch u := e.(type) {
		case interface{ Unwrap() error }:
			next = []error{u.Unwrap()}
		case interface{ Unwrap() []error }:
			next = u.Unwrap()
		}
		room := Max - len(queue)
		if len(next) > room {
			next, truncated = next[:room], true
		}
		queue = append(queue, next...)
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
// target.
func Is(err, target error) bool {
	return Walk(err, func(e error) bool { return Matches(e, target) }) == Stopped
}
