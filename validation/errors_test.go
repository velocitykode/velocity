package validation

import (
	"errors"
	"testing"
)

func TestResultErr_NilOnSuccess(t *testing.T) {
	r := &Result{}
	if err := r.Err(); err != nil {
		t.Fatalf("expected nil err on clean Result, got %v", err)
	}
}

func TestResultErr_WrapsErrValidationFailed(t *testing.T) {
	r := &Result{errors: map[string][]string{
		"email": {"The email field is required."},
	}}
	err := r.Err()
	if err == nil {
		t.Fatal("expected error from failed Result")
	}
	if !errors.Is(err, ErrValidationFailed) {
		t.Fatalf("expected errors.Is(err, ErrValidationFailed), got %v", err)
	}

	// errors.As must unwrap to ValidationErrors so callers can read field
	// messages.
	var ve ValidationErrors
	if !errors.As(err, &ve) {
		t.Fatalf("expected errors.As to yield ValidationErrors, got %v", err)
	}
	if ve.First("email") == "" {
		t.Fatalf("expected field message to survive errors.As")
	}
}

func TestErrValidationFailed_IsUsable(t *testing.T) {
	// Sentinel must be a plain, comparable error value.
	if ErrValidationFailed == nil {
		t.Fatal("sentinel is nil")
	}
	if ErrValidationFailed.Error() == "" {
		t.Fatal("sentinel has no message")
	}
}
