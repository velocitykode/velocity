package bus

import (
	"reflect"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// unserializableCmd holds a channel, which encoding/json cannot marshal.
// Register should refuse to accept a handler for this type.
type unserializableCmd struct {
	Ch chan int
}

// TestRegister_RejectsUnserializable covers Task 5: Register must probe the
// command type via encoding/json at registration time and panic with a
// *contract.RegistrationError when marshalling fails.
func TestRegister_RejectsUnserializable(t *testing.T) {
	b := New()

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic from unserializable command type")
		}
		regErr, ok := r.(*contract.RegistrationError)
		if !ok {
			t.Fatalf("expected *contract.RegistrationError, got %T: %v", r, r)
		}
		if regErr.Package != "bus" {
			t.Errorf("expected package=bus, got %q", regErr.Package)
		}
		if !strings.Contains(regErr.Error(), "not json-serializable") {
			t.Errorf("expected 'not json-serializable' in error, got %q", regErr.Error())
		}
	}()

	Register(b, func(cmd unserializableCmd) error { return nil })
}

// TestCommandJob_HandleRejectsTypeMismatch covers Task 5: the type check in
// commandJob.Handle catches mutations between enqueue and dequeue.
func TestCommandJob_HandleRejectsTypeMismatch(t *testing.T) {
	b := New()

	Register(b, func(cmd createUser) error { return nil })

	// Build a job as DispatchAsync would, then mutate cmd to a different type
	// to simulate queue-side tampering / deserialization mismatch.
	job := &commandJob{
		cmd:     deleteUser{ID: 1},
		bus:     b,
		cmdType: reflect.TypeOf(createUser{}),
	}

	err := job.Handle()
	if err == nil {
		t.Fatal("expected type-mismatch error, got nil")
	}
	if !strings.Contains(err.Error(), "command type mismatch") {
		t.Errorf("expected 'command type mismatch' in error, got %q", err.Error())
	}
}

// TestCommandJob_HandlePassesWhenTypesMatch ensures the new type guard does
// not regress the happy path — when cmdType matches, Handle still dispatches.
func TestCommandJob_HandlePassesWhenTypesMatch(t *testing.T) {
	b := New()

	called := false
	Register(b, func(cmd createUser) error {
		called = true
		return nil
	})

	job := &commandJob{
		cmd:     createUser{Name: "Alice"},
		bus:     b,
		cmdType: reflect.TypeOf(createUser{}),
	}

	if err := job.Handle(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Fatal("handler was not invoked despite matching type")
	}
}
