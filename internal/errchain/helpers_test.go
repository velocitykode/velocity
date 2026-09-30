package errchain

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// codeErr is a concrete error type As looks for.
type codeErr struct{ code int }

func (e *codeErr) Error() string { return fmt.Sprintf("code %d", e.code) }

// coder is an interface type As looks for.
type coder interface{ Code() int }

func (e *codeErr) Code() int { return e.code }

// asProvider answers As for *codeErr through its own As method.
type asProvider struct{ code int }

func (asProvider) Error() string { return "provider" }
func (a asProvider) As(target any) bool {
	if p, ok := target.(**codeErr); ok {
		*p = &codeErr{code: a.code}
		return true
	}
	return false
}

// hostileMethods runs its Code in the method named by method.
type hostileMethods struct {
	code   *hostile.Code
	method string
	next   error
}

func (e *hostileMethods) Error() string {
	if e.method == "Error" {
		e.code.Run()
	}
	return "hostile methods"
}

func (e *hostileMethods) Unwrap() error {
	if e.method == "Unwrap" {
		e.code.Run()
	}
	return e.next
}

func (e *hostileMethods) As(target any) bool {
	if e.method == "As" {
		e.code.Run()
	}
	return false
}

// panicky panics in every method.
type panicky struct{}

func (panicky) Error() string   { panic("Error broke") }
func (panicky) Unwrap() error   { panic("Unwrap broke") }
func (panicky) Is(error) bool   { panic("Is broke") }
func (panicky) As(any) bool     { panic("As broke") }
func (panicky) Code() int       { return 0 }
func (panicky) String() string  { return "panicky" }
func (panicky) Timeout() bool   { return false }
func (panicky) Temporary() bool { return false }

func TestAs(t *testing.T) {
	shallow, deep := &codeErr{1}, &codeErr{2}
	for _, tc := range []struct {
		name     string
		err      error
		wantCode int
		want     bool
	}{
		{"nil", nil, 0, false},
		{"itself", shallow, 1, true},
		{"wrapped", fmt.Errorf("w: %w", shallow), 1, true},
		{"through As method", fmt.Errorf("w: %w", asProvider{code: 7}), 7, true},
		{"shallowest first", errors.Join(fmt.Errorf("x: %w", deep), shallow), 1, true},
		{"absent", io.EOF, 0, false},
		{"self loop", &selfLoop{}, 0, false},
		{"past the bound", wideJoin{target: shallow}, 0, false},
		{"loop beside the target", errors.Join(&selfLoop{}, shallow), 1, true},
		{"panicking Unwrap", panicky{}, 0, false},
		{"match before a panic", errors.Join(shallow, panicky{}), 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got *codeErr
			var ok bool
			hostile.Within(t, hostile.Deadline, func() { got, ok = As[*codeErr](tc.err) })
			if ok != tc.want {
				t.Fatalf("As ok = %v, want %v", ok, tc.want)
			}
			if ok && got.code != tc.wantCode {
				t.Fatalf("As = code %d, want %d", got.code, tc.wantCode)
			}
			if !ok && got != nil {
				t.Fatalf("As = %v on a miss, want the zero value", got)
			}
		})
	}
}

// As finds an interface type the way a type assertion does.
func TestAs_Interface(t *testing.T) {
	c, ok := As[coder](fmt.Errorf("w: %w", &codeErr{3}))
	if !ok || c.Code() != 3 {
		t.Fatalf("As[coder] = %v, %v; want code 3", c, ok)
	}
	if _, ok := As[coder](io.EOF); ok {
		t.Fatal("As[coder] found a coder in io.EOF")
	}
}

// As agrees with errors.As on chains without a branch order question.
func TestAs_AgreesWithErrorsAs(t *testing.T) {
	for _, err := range []error{
		&codeErr{1},
		fmt.Errorf("a: %w", fmt.Errorf("b: %w", &codeErr{2})),
		fmt.Errorf("a: %w", asProvider{code: 3}),
		errors.Join(io.EOF, &codeErr{4}),
		io.EOF,
	} {
		var want *codeErr
		wantOK := errors.As(err, &want)
		got, ok := As[*codeErr](err)
		if ok != wantOK || (ok && got.code != want.code) {
			t.Errorf("%v: As = %v, %v; errors.As = %v, %v", err, got, ok, want, wantOK)
		}
	}
}

func TestMatchesAs_DoesNotFollowTheChain(t *testing.T) {
	if _, ok := MatchesAs[*codeErr](fmt.Errorf("w: %w", &codeErr{1})); ok {
		t.Fatal("MatchesAs followed the chain")
	}
	if got, ok := MatchesAs[*codeErr](asProvider{code: 5}); !ok || got.code != 5 {
		t.Fatalf("MatchesAs through As = %v, %v", got, ok)
	}
}

func TestIs_IdentityFastPath(t *testing.T) {
	for _, tc := range []struct {
		name     string
		err, tgt error
		want     bool
	}{
		{"nil err", nil, io.EOF, false},
		{"nil target", io.EOF, nil, false},
		{"both nil", nil, nil, false},
		{"identical", io.EOF, io.EOF, true},
		{"identical value type, matched before its Is runs", panicky{}, panicky{}, true},
		{"wrapped", fmt.Errorf("w: %w", io.EOF), io.EOF, true},
		{"through Is", isCanceled{}, context.Canceled, true},
		{"incomparable of the same type", incomparable{"x"}, incomparable{"x"}, false},
		{"trapped comparison", trapped{v: []int{1}}, trapped{v: []int{1}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Is(tc.err, tc.tgt); got != tc.want {
				t.Fatalf("Is = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestUnwrap(t *testing.T) {
	inner := io.EOF
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"nil", nil, nil},
		{"no Unwrap", io.EOF, nil},
		{"one step", fmt.Errorf("w: %w", inner), inner},
		{"join is not one step", errors.Join(inner), nil},
		{"panicking Unwrap", panicky{}, nil},
		{"self loop", &selfLoop{}, &selfLoop{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Unwrap(tc.err)
			if fmt.Sprintf("%T %v", got, got) != fmt.Sprintf("%T %v", tc.want, tc.want) {
				t.Fatalf("Unwrap = %#v, want %#v", got, tc.want)
			}
		})
	}
}

func TestText(t *testing.T) {
	var nilPtr *codeErr
	for _, tc := range []struct {
		name string
		err  error
		want string
	}{
		{"nil", nil, ""},
		{"plain", io.EOF, "EOF"},
		{"wrapped", fmt.Errorf("w: %w", io.EOF), "w: EOF"},
		{"panicking Error", panicky{}, Unreadable},
		{"nil pointer receiver", nilPtr, Unreadable},
		{"join with a panicking branch", errors.Join(io.EOF, panicky{}), Unreadable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Text(tc.err); got != tc.want {
				t.Fatalf("Text = %q, want %q", got, tc.want)
			}
		})
	}
}

// The hostile sweep over the helpers this file tests: an error whose
// Error, Unwrap or As method panics, blocks or calls back into the helper.
// A panic gives the helper's fixed answer; a block delays only its own
// call, and another call runs meanwhile; a re-entry runs its own call and
// the outer one goes on.
func TestHelpers_HostileSweep(t *testing.T) {
	type helper struct {
		name   string
		method string
		call   func(err error) any
		// want is the answer once the hostile code returned normally,
		// and onPanic after it panicked.
		want, onPanic any
	}
	helpers := []helper{
		{"As", "As", func(err error) any { _, ok := As[*codeErr](err); return ok }, true, false},
		{"As/Unwrap", "Unwrap", func(err error) any { _, ok := As[*codeErr](err); return ok }, true, false},
		{"Unwrap", "Unwrap", func(err error) any { return Unwrap(err) != nil }, true, false},
		{"Text", "Error", func(err error) any { return Text(err) }, "hostile methods", Unreadable},
	}
	for _, mode := range hostile.Modes() {
		for _, h := range helpers {
			t.Run(mode.String()+"/"+h.name, func(t *testing.T) {
				var inner any
				code := hostile.New(t, mode, func() { inner = h.call(&hostileMethods{method: "none", next: &codeErr{9}}) })
				err := &hostileMethods{code: code, method: h.method, next: &codeErr{9}}
				done := make(chan any, 1)
				go func() { done <- h.call(err) }()
				switch mode {
				case hostile.Panic:
					if got := <-done; got != h.onPanic {
						t.Fatalf("%s after a panic = %v, want %v", h.name, got, h.onPanic)
					}
				case hostile.Block:
					if !code.AwaitEntered(t) {
						return
					}
					var other any
					hostile.Within(t, hostile.Deadline, func() { other = h.call(&hostileMethods{method: "none", next: &codeErr{9}}) })
					if other != h.want {
						t.Fatalf("a concurrent %s = %v while another was blocked, want %v", h.name, other, h.want)
					}
					code.Release()
					if got := <-done; got != h.want {
						t.Fatalf("%s once released = %v, want %v", h.name, got, h.want)
					}
				case hostile.Reenter:
					if got := <-done; got != h.want {
						t.Fatalf("%s after a re-entry = %v, want %v", h.name, got, h.want)
					}
					if inner != h.want {
						t.Fatalf("the re-entered %s = %v, want %v", h.name, inner, h.want)
					}
				}
			})
		}
	}
}

// A join below a chain of single wraps counts the wraps toward Max, and a
// target at depth Max-1 is found while one at depth Max is not.
func TestWalk_BoundSpansWrapsAndJoins(t *testing.T) {
	var err error = wideJoin{target: io.EOF}
	for i := 0; i < Max-2; i++ {
		err = fmt.Errorf("w: %w", err)
	}
	n := 0
	if r := Walk(err, func(error) bool { n++; return false }); r != Truncated || n > Max {
		t.Fatalf("Walk = %v after %d visits, want Truncated within Max (%d)", r, n, Max)
	}
	deep := func(depth int) error {
		var e error = io.EOF
		for i := 0; i < depth; i++ {
			e = fmt.Errorf("w: %w", e)
		}
		return e
	}
	if !Is(deep(Max-1), io.EOF) {
		t.Error("a target at the last counted position was not found")
	}
	if Is(deep(Max), io.EOF) {
		t.Error("a target past the bound was found")
	}
}

// stringerFunc is a Stringer whose String runs f.
type stringerFunc func() string

func (f stringerFunc) String() string { return f() }

// formatterFunc is a Formatter whose Format writes what f returns.
type formatterFunc func() string

func (f formatterFunc) Format(s fmt.State, _ rune) { _, _ = s.Write([]byte(f())) }

// hostileStringer runs its Code in String.
type hostileStringer struct{ code *hostile.Code }

func (h hostileStringer) String() string { h.code.Run(); return "hostile stringer" }

// nestedPanic panics with a value whose own String panics: fmt re-raises
// that panic instead of containing it.
type nestedPanic struct{}

func (nestedPanic) String() string { panic(stringerFunc(func() string { panic("again") })) }

func TestSprint(t *testing.T) {
	var nilPtr *codeErr
	for _, tc := range []struct {
		name string
		v    any
		want string
	}{
		{"nil", nil, "<nil>"},
		{"plain value", 42, "42"},
		{"struct", struct{ A int }{1}, "{1}"},
		{"error", io.EOF, "EOF"},
		{"stringer", stringerFunc(func() string { return "s" }), "s"},
		{"formatter", formatterFunc(func() string { return "f" }), "f"},
		{"panicking Error", panicky{}, Unreadable},
		{"panicking String", stringerFunc(func() string { panic("broke") }), Unreadable},
		{"panicking Format", formatterFunc(func() string { panic("broke") }), Unreadable},
		{"nil pointer receiver", nilPtr, Unreadable},
		{"nested panic fmt re-raises", []any{nestedPanic{}}, Unreadable},
		{"panic fmt contains inside a value", []any{stringerFunc(func() string { panic("x") })}, "[%!v(PANIC=String method: x)]"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := Sprint(tc.v); got != tc.want {
				t.Fatalf("Sprint = %q, want %q", got, tc.want)
			}
		})
	}
}

// The hostile sweep for Sprint: a String method that panics, blocks or
// calls back into Sprint.
func TestSprint_HostileSweep(t *testing.T) {
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			var inner string
			code := hostile.New(t, mode, func() { inner = Sprint(io.EOF) })
			done := make(chan string, 1)
			go func() { done <- Sprint(hostileStringer{code: code}) }()
			switch mode {
			case hostile.Panic:
				if got := <-done; got != Unreadable {
					t.Fatalf("Sprint after a panic = %q, want Unreadable", got)
				}
			case hostile.Block:
				if !code.AwaitEntered(t) {
					return
				}
				var other string
				hostile.Within(t, hostile.Deadline, func() { other = Sprint(io.EOF) })
				if other != "EOF" {
					t.Fatalf("a concurrent Sprint = %q while another was blocked", other)
				}
				code.Release()
				if got := <-done; got != "hostile stringer" {
					t.Fatalf("Sprint once released = %q", got)
				}
			case hostile.Reenter:
				if got := <-done; got != "hostile stringer" {
					t.Fatalf("Sprint after a re-entry = %q", got)
				}
				if inner != "EOF" {
					t.Fatalf("the re-entered Sprint = %q, want EOF", inner)
				}
			}
		})
	}
}
