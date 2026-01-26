package exceptions

import (
	"errors"
	"testing"
)

func TestNewBaseException(t *testing.T) {
	tests := []struct {
		name    string
		message string
		code    int
	}{
		{
			name:    "basic exception",
			message: "test error",
			code:    100,
		},
		{
			name:    "empty message",
			message: "",
			code:    0,
		},
		{
			name:    "negative code",
			message: "negative",
			code:    -1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exc := NewBaseException(tt.message, tt.code)

			if exc.GetMessage() != tt.message {
				t.Errorf("GetMessage() = %q, want %q", exc.GetMessage(), tt.message)
			}
			if exc.GetCode() != tt.code {
				t.Errorf("GetCode() = %d, want %d", exc.GetCode(), tt.code)
			}
			if exc.GetPrevious() != nil {
				t.Error("GetPrevious() should be nil for new exception")
			}
			if exc.context == nil {
				t.Error("context should be initialized")
			}
		})
	}
}

func TestBaseException_Error(t *testing.T) {
	tests := []struct {
		name     string
		message  string
		previous error
		want     string
	}{
		{
			name:     "without previous",
			message:  "test error",
			previous: nil,
			want:     "test error",
		},
		{
			name:     "with previous",
			message:  "outer error",
			previous: errors.New("inner error"),
			want:     "outer error: inner error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exc := NewBaseException(tt.message, 0)
			if tt.previous != nil {
				exc.WithPrevious(tt.previous)
			}

			if got := exc.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBaseException_WithPrevious(t *testing.T) {
	prev := errors.New("previous error")
	exc := NewBaseException("test", 0).WithPrevious(prev)

	if exc.GetPrevious() != prev {
		t.Error("WithPrevious did not set previous error")
	}

	// Test chaining
	exc2 := NewBaseException("test2", 0)
	result := exc2.WithPrevious(prev)
	if result != exc2 {
		t.Error("WithPrevious should return the exception for chaining")
	}
}

func TestBaseException_WithContext(t *testing.T) {
	exc := NewBaseException("test", 0).
		WithContext("key1", "value1").
		WithContext("key2", 42)

	ctx := exc.GetContext()
	if ctx["key1"] != "value1" {
		t.Errorf("context[key1] = %v, want value1", ctx["key1"])
	}
	if ctx["key2"] != 42 {
		t.Errorf("context[key2] = %v, want 42", ctx["key2"])
	}
}

func TestBaseException_WithContext_NilContext(t *testing.T) {
	exc := &BaseException{message: "test", code: 0}
	exc.context = nil

	exc.WithContext("key", "value")
	if exc.context == nil {
		t.Error("WithContext should initialize nil context")
	}
	if exc.context["key"] != "value" {
		t.Error("WithContext should set the value")
	}
}

func TestBaseException_WithContextMap(t *testing.T) {
	exc := NewBaseException("test", 0).
		WithContextMap(map[string]any{
			"a": 1,
			"b": "two",
		})

	ctx := exc.GetContext()
	if ctx["a"] != 1 || ctx["b"] != "two" {
		t.Error("WithContextMap did not set values correctly")
	}
}

func TestBaseException_WithContextMap_NilContext(t *testing.T) {
	exc := &BaseException{message: "test", code: 0}
	exc.context = nil

	exc.WithContextMap(map[string]any{"key": "value"})
	if exc.context == nil {
		t.Error("WithContextMap should initialize nil context")
	}
}

func TestBaseException_GetContext_Nil(t *testing.T) {
	exc := &BaseException{message: "test", code: 0}
	exc.context = nil

	ctx := exc.GetContext()
	if ctx == nil {
		t.Error("GetContext should return empty map, not nil")
	}
}

func TestBaseException_ShouldReport(t *testing.T) {
	exc := NewBaseException("test", 0)
	if !exc.ShouldReport() {
		t.Error("BaseException.ShouldReport() should return true")
	}
}

func TestBaseException_Unwrap(t *testing.T) {
	prev := errors.New("previous")
	exc := NewBaseException("test", 0).WithPrevious(prev)

	if exc.Unwrap() != prev {
		t.Error("Unwrap should return previous error")
	}

	// Test errors.Is compatibility
	if !errors.Is(exc, prev) {
		t.Error("errors.Is should work with Unwrap")
	}
}

func TestException_Interface(t *testing.T) {
	var _ Exception = (*BaseException)(nil)
	var _ Reportable = (*BaseException)(nil)
}
