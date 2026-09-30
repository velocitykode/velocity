package grpc_test

import (
	"fmt"
	"io"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/velocitykode/velocity/grpc"
	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/hostile"
)

// errorTextPanics is an error whose Error and Is methods panic.
type errorTextPanics struct{}

func (errorTextPanics) Error() string { panic("Error broke") }
func (errorTextPanics) Is(error) bool { panic("Is broke") }

// The error helpers read a caller's error contained: a panicking Error
// gives the fixed text, a panicking Is a miss, and none of them panics.
func TestErrorHelpers_HostileErrorIsContained(t *testing.T) {
	grpc.SetDebugMode(true)
	t.Cleanup(func() { grpc.SetDebugMode(false) })
	var (
		wrapped, withCode error
		msg               string
		is                bool
	)
	if p := hostile.Within(t, hostile.Deadline, func() {
		wrapped = grpc.WrapError(errorTextPanics{})
		withCode = grpc.WrapErrorWithCode(errorTextPanics{}, codes.InvalidArgument)
		msg = grpc.Message(errorTextPanics{})
		is = grpc.ErrorIs(errorTextPanics{}, io.EOF)
	}); p != nil {
		t.Fatalf("an error helper panicked: %v", p)
	}
	if s, _ := status.FromError(wrapped); s.Message() != errchain.Unreadable {
		t.Errorf("WrapError message = %q, want the fixed text", s.Message())
	}
	if s, _ := status.FromError(withCode); s.Code() != codes.InvalidArgument || s.Message() != errchain.Unreadable {
		t.Errorf("WrapErrorWithCode = %v, want InvalidArgument with the fixed text", s)
	}
	if msg != errchain.Unreadable {
		t.Errorf("Message = %q, want the fixed text", msg)
	}
	if is {
		t.Error("ErrorIs = true for an Is that panicked")
	}
}

// errorLoop unwraps to itself.
type errorLoop struct{}

func (e *errorLoop) Error() string { return "loop" }
func (e *errorLoop) Unwrap() error { return e }

// Every error helper answers a chain that loops, or an error whose methods
// panic, without hanging or panicking: Code and IsCode read Unknown, and
// FromError gives Unknown with the fixed text.
func TestErrorHelpers_HostileChainsEnd(t *testing.T) {
	for _, err := range []error{&errorLoop{}, errorTextPanics{}} {
		if p := hostile.Within(t, hostile.Deadline, func() {
			_ = grpc.WrapError(err)
			_ = grpc.WrapErrorWithCode(err, codes.NotFound)
			_ = grpc.Message(err)
			_ = grpc.ErrorIs(err, io.EOF)
			if c := grpc.Code(err); c != codes.Unknown {
				t.Errorf("%T: Code = %v, want Unknown", err, c)
			}
			if grpc.IsNotFound(err) {
				t.Errorf("%T: IsNotFound = true", err)
			}
			if s := grpc.FromError(err); s.Code() != codes.Unknown {
				t.Errorf("%T: FromError = %v, want Unknown", err, s)
			}
		}); p != nil {
			t.Fatalf("%T: an error helper panicked: %v", err, p)
		}
	}
}

// The helpers answer what grpc-go's status.FromError answers for errors
// whose methods behave.
func TestErrorHelpers_ParityWithStatusFromError(t *testing.T) {
	st := status.New(codes.NotFound, "missing")
	for _, err := range []error{
		st.Err(),
		fmt.Errorf("wrapped: %w", st.Err()),
		io.EOF,
		fmt.Errorf("plain: %w", io.EOF),
	} {
		want, wantOK := status.FromError(err)
		got := grpc.FromError(err)
		if got.Code() != want.Code() || got.Message() != want.Message() {
			t.Errorf("%v: FromError = %v %q, status.FromError = %v %q", err, got.Code(), got.Message(), want.Code(), want.Message())
		}
		if wantOK && grpc.Code(err) != want.Code() {
			t.Errorf("%v: Code = %v, want %v", err, grpc.Code(err), want.Code())
		}
	}
}
