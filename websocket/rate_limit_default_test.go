package websocket

import (
	"testing"
)

// TestNew_DefaultMessageRateLimitApplied covers audit D-03: when callers
// leave Config.MessageRateLimit at its zero value, New installs the secure
// default (DefaultMessageRateLimit) so the readPump enforces a cap rather
// than running unrate-limited.
func TestNew_DefaultMessageRateLimitApplied(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.MessageRateLimit != 0 {
		t.Fatalf("precondition: DefaultConfig must leave MessageRateLimit at 0, got %d", cfg.MessageRateLimit)
	}

	s := New(cfg)
	if s.config.MessageRateLimit != DefaultMessageRateLimit {
		t.Errorf("zero MessageRateLimit must be normalised to DefaultMessageRateLimit; got %d, want %d", s.config.MessageRateLimit, DefaultMessageRateLimit)
	}
}

// TestNew_NegativeMessageRateLimitOptsOut documents the explicit opt-out
// path: a negative value normalises to 0, which the readPump treats as
// unlimited. This is the only supported way to disable the cap.
func TestNew_NegativeMessageRateLimitOptsOut(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MessageRateLimit = -1

	s := New(cfg)
	if s.config.MessageRateLimit != 0 {
		t.Errorf("negative MessageRateLimit must normalise to 0 (unlimited); got %d", s.config.MessageRateLimit)
	}
}

// TestNew_ExplicitMessageRateLimitPreserved confirms a positive caller-set
// value is left alone so callers who want a different cap still get it.
func TestNew_ExplicitMessageRateLimitPreserved(t *testing.T) {
	cfg := DefaultConfig()
	cfg.MessageRateLimit = 42

	s := New(cfg)
	if s.config.MessageRateLimit != 42 {
		t.Errorf("explicit MessageRateLimit must be preserved; got %d, want 42", s.config.MessageRateLimit)
	}
}
