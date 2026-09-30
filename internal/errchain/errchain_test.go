package errchain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// selfLoop unwraps to itself: a chain with a cycle.
type selfLoop struct{}

func (e *selfLoop) Error() string { return "loop" }
func (e *selfLoop) Unwrap() error { return e }

// joinLoop joins itself: a multi-branch chain with a cycle.
type joinLoop struct{}

func (e *joinLoop) Error() string   { return "join loop" }
func (e *joinLoop) Unwrap() []error { return []error{e, e} }

// wideJoin joins more errors than a walk looks at, target last.
type wideJoin struct{ target error }

func (wideJoin) Error() string { return "wide" }
func (w wideJoin) Unwrap() []error {
	errs := make([]error, 0, 2*Max+1)
	for i := 0; i < 2*Max; i++ {
		errs = append(errs, errors.New("filler"))
	}
	return append(errs, w.target)
}

// hostileError runs its Code in Unwrap, Is or both, as selected.
type hostileError struct {
	code       *hostile.Code
	unwrap, is bool
	next       error
}

func (e *hostileError) Error() string { return "hostile" }
func (e *hostileError) Unwrap() error {
	if e.unwrap {
		e.code.Run()
	}
	return e.next
}
func (e *hostileError) Is(target error) bool {
	if e.is {
		e.code.Run()
	}
	return false
}

// incomparable is an error whose dynamic type cannot be compared.
type incomparable []string

func (incomparable) Error() string { return "incomparable" }

// trapped is a comparable struct whose interface field can hold an
// incomparable value, so comparing two of them panics.
type trapped struct{ v any }

func (trapped) Error() string { return "trapped" }

func TestWalk_Order(t *testing.T) {
	a, b, c := errors.New("a"), errors.New("b"), errors.New("c")
	err := errors.Join(fmt.Errorf("wrap: %w", c), a, b)
	var got []error
	if r := Walk(err, func(e error) bool { got = append(got, e); return false }); r != Ended {
		t.Fatalf("Walk = %v, want Ended", r)
	}
	// Breadth first: the join, its three branches, then the wrapped c.
	wrapped := err.(interface{ Unwrap() []error }).Unwrap()[0]
	want := []error{err, wrapped, a, b, c}
	if len(got) != len(want) {
		t.Fatalf("visited %d errors, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("visit %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// The shallowest match wins, unlike errors.As, which goes depth first.
func TestWalk_ShallowestMatchFirst(t *testing.T) {
	deep, shallow := errors.New("deep"), errors.New("shallow")
	err := errors.Join(fmt.Errorf("x: %w", deep), shallow)
	var first error
	Walk(err, func(e error) bool {
		if e == deep || e == shallow {
			first = e
			return true
		}
		return false
	})
	if first != shallow {
		t.Fatalf("first match = %v, want the shallow one", first)
	}
}

func TestWalk_Nil(t *testing.T) {
	called := false
	if r := Walk(nil, func(error) bool { called = true; return true }); r != Ended || called {
		t.Fatalf("Walk(nil) = %v, visited %v; want Ended without a visit", r, called)
	}
}

func TestWalk_Stopped(t *testing.T) {
	if r := Walk(io.EOF, func(error) bool { return true }); r != Stopped {
		t.Fatalf("Walk = %v, want Stopped", r)
	}
}

// A chain that loops back on itself ends, in one branch or several, and
// a loop in one branch of a join does not hide the others.
func TestWalk_CyclicChainsEnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"self loop", &selfLoop{}, false},
		{"join loop", &joinLoop{}, false},
		{"loop beside the target", errors.Join(&selfLoop{}, io.EOF), true},
		{"join loop beside the target", errors.Join(&joinLoop{}, io.EOF), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got bool
			hostile.Within(t, hostile.Deadline, func() { got = Is(tc.err, io.EOF) })
			if got != tc.want {
				t.Fatalf("Is = %v, want %v", got, tc.want)
			}
		})
	}
}

// No walk looks at more than Max errors, and a chain cut short says so.
func TestWalk_Bounded(t *testing.T) {
	for _, err := range []error{&selfLoop{}, &joinLoop{}, wideJoin{target: io.EOF}} {
		n := 0
		r := Walk(err, func(error) bool { n++; return false })
		if n > Max {
			t.Errorf("%T: visited %d errors, more than Max (%d)", err, n, Max)
		}
		if r != Truncated {
			t.Errorf("%T: Walk = %v, want Truncated", err, r)
		}
	}
	if Is(wideJoin{target: io.EOF}, io.EOF) {
		t.Error("Is found a target past the bound")
	}
	if Is(fmt.Errorf("a: %w", fmt.Errorf("b: %w", wideJoin{target: io.EOF})), io.EOF) {
		t.Error("Is found a wrapped target past the bound")
	}
}

func TestWalk_EmptyJoinEnds(t *testing.T) {
	if r := Walk(errors.Join(nil), func(error) bool { return false }); r != Ended {
		t.Fatalf("Walk = %v, want Ended", r)
	}
	var e interface{ Unwrap() []error } = &emptyJoin{}
	if r := Walk(e.(error), func(error) bool { return false }); r != Ended {
		t.Fatalf("Walk = %v, want Ended", r)
	}
}

type emptyJoin struct{}

func (*emptyJoin) Error() string   { return "empty" }
func (*emptyJoin) Unwrap() []error { return nil }

// A panic in an Unwrap method or in visit ends the walk with Panicked,
// and visit's effects up to the panic stay.
func TestWalk_PanicIsContained(t *testing.T) {
	code := hostile.New(t, hostile.Panic, nil)
	var visited int
	r := Walk(&hostileError{code: code, unwrap: true, next: io.EOF}, func(error) bool { visited++; return false })
	if r != Panicked || visited != 1 {
		t.Fatalf("Walk = %v after %d visits, want Panicked after 1", r, visited)
	}
	r = Walk(io.EOF, func(error) bool { panic("visit broke") })
	if r != Panicked {
		t.Fatalf("Walk with a panicking visit = %v, want Panicked", r)
	}
}

func TestMatches(t *testing.T) {
	for _, tc := range []struct {
		name      string
		e, target error
		want      bool
	}{
		{"identity", io.EOF, io.EOF, true},
		{"different", io.EOF, io.ErrUnexpectedEOF, false},
		{"through Is", isCanceled{}, context.Canceled, true},
		{"incomparable error", incomparable{"x"}, io.EOF, false},
		{"incomparable target", io.EOF, incomparable{"x"}, false},
		{"not followed", fmt.Errorf("w: %w", io.EOF), io.EOF, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Matches(tc.e, tc.target); got != tc.want {
				t.Fatalf("Matches = %v, want %v", got, tc.want)
			}
		})
	}
}

// A comparable type holding an incomparable value panics when compared:
// Is contains it.
func TestIs_ComparisonPanicIsContained(t *testing.T) {
	target := trapped{v: []int{1}}
	if Is(trapped{v: []int{1}}, target) {
		t.Fatal("Is = true for a comparison that panics")
	}
}

type isCanceled struct{}

func (isCanceled) Error() string        { return "canceled" }
func (isCanceled) Is(target error) bool { return target == context.Canceled }

// The hostile sweep: an error whose Unwrap or Is panics, blocks or calls
// back into the walk. A panic ends that walk with Panicked; a block delays
// only its own walk, which holds nothing another walk needs; a re-entry
// walks another chain and the outer walk goes on.
func TestWalk_HostileSweep(t *testing.T) {
	for _, mode := range hostile.Modes() {
		for _, method := range []string{"Unwrap", "Is"} {
			t.Run(mode.String()+"/"+method, func(t *testing.T) {
				var inner bool
				code := hostile.New(t, mode, func() { inner = Is(fmt.Errorf("x: %w", io.EOF), io.EOF) })
				err := &hostileError{code: code, unwrap: method == "Unwrap", is: method == "Is", next: io.EOF}
				done := make(chan bool, 1)
				go func() { done <- Is(err, io.EOF) }()
				switch mode {
				case hostile.Panic:
					if got := <-done; got {
						t.Fatal("Is = true after a panic")
					}
				case hostile.Block:
					if !code.AwaitEntered(t) {
						return
					}
					// Another walk runs while this one is blocked.
					var other bool
					hostile.Within(t, hostile.Deadline, func() { other = Is(fmt.Errorf("y: %w", io.EOF), io.EOF) })
					if !other {
						t.Fatal("a concurrent walk failed while another was blocked")
					}
					code.Release()
					if got := <-done; !got {
						t.Fatal("Is = false once released")
					}
				case hostile.Reenter:
					if got := <-done; !got {
						t.Fatal("Is = false after a re-entry")
					}
					if !inner {
						t.Fatal("the re-entered walk did not find its target")
					}
				}
			})
		}
	}
}
