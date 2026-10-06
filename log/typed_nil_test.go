package log

import "testing"

type nilLog struct{ Logger }

// The redaction wrappers answer a typed-nil logger with plain nil: no
// wrapper is built around a logger that is not there, and a caller's own
// nil check on the result holds.
func TestRedactionWrappers_TypedNilIsNotWrapped(t *testing.T) {
	var typed *nilLog
	if got := WithRedactors(typed, EmailRedactor()); got != nil {
		t.Errorf("WithRedactors(typed nil) = %T, want nil", got)
	}
	cfg := LogConfig{Config: map[string]any{"redact": true}}
	if got := WrapWithRedactors(typed, cfg); got != nil {
		t.Errorf("WrapWithRedactors(typed nil) = %T, want nil", got)
	}
}
