package bus

import (
	"errors"
	"testing"
)

// LoggingMiddleware(nil) logs through the framework's standalone fallback
// logger instead of panicking on the first dispatch; the handler's result
// comes back unchanged.
func TestLoggingMiddleware_NilLoggerUsesTheFallback(t *testing.T) {
	b := New()
	b.Through(LoggingMiddleware(nil))
	want := errors.New("fail")
	Register(b, func(createUser) error { return want })

	if err := b.Dispatch(createUser{Name: "Test"}); !errors.Is(err, want) {
		t.Fatalf("Dispatch = %v, want %v", err, want)
	}
}
