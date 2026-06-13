package crypto

import (
	"errors"
	"strings"
	"testing"
)

// TestNewEncryptor_RejectsInvalidPreviousKey asserts the constructor
// fails fast (rather than silently skipping) when a PreviousKeys entry
// cannot be parsed. The old behaviour would `continue` past malformed
// entries, letting an operator's typo in APP_PREVIOUS_KEY disable key
// rotation without any signal: encrypts and Decrypts of current-key
// ciphertexts kept working, but pre-rotation ciphertexts started
// returning ErrDecrypt with no obvious cause. This locks the change in.
func TestNewEncryptor_RejectsInvalidPreviousKey(t *testing.T) {
	cfg := Config{
		Key:    strings.Repeat("a", 32),
		Cipher: "AES-256-CBC",
		PreviousKeys: []string{
			"base64:!!!not-base64!!!",
		},
	}
	_, err := NewEncryptor(cfg)
	if err == nil {
		t.Fatal("expected ErrInvalidPreviousKey, got nil")
	}
	if !errors.Is(err, ErrInvalidPreviousKey) {
		t.Fatalf("expected ErrInvalidPreviousKey, got %v", err)
	}
}

// TestNewEncryptor_RejectsInvalidPreviousKey_LengthMismatch covers the
// second class of "looks parseable but is unusable" PreviousKeys: an
// entry that decodes cleanly but produces wrong-length raw bytes for
// the cipher. Without a configuration-layer length check the wrong-
// length entry parses cleanly, gets appended to previousKeys, and is
// then silently filtered inside NewAESDriver, leaving the operator
// believing rotation is active while pre-rotation ciphertexts fail.
// NewEncryptor therefore rejects length-mismatched entries up front
// with ErrInvalidPreviousKey, mirroring the parse-error fail-fast.
func TestNewEncryptor_RejectsInvalidPreviousKey_LengthMismatch(t *testing.T) {
	cfg := Config{
		Key:    strings.Repeat("a", 32),
		Cipher: "AES-256-CBC",
		PreviousKeys: []string{
			// Decodes to 16 bytes, not 32.
			"base64:MTIzNDU2Nzg5MGFiY2RlZg==",
		},
	}
	_, err := NewEncryptor(cfg)
	if err == nil {
		t.Fatal("expected ErrInvalidPreviousKey, got nil")
	}
	if !errors.Is(err, ErrInvalidPreviousKey) {
		t.Fatalf("expected ErrInvalidPreviousKey, got %v", err)
	}
}

// TestNewEncryptor_AcceptsValidPreviousKeys is a positive control:
// well-formed previous keys must not trip the fail-fast path.
func TestNewEncryptor_AcceptsValidPreviousKeys(t *testing.T) {
	cfg := Config{
		Key:    strings.Repeat("a", 32),
		Cipher: "AES-256-CBC",
		PreviousKeys: []string{
			strings.Repeat("b", 32),
			strings.Repeat("c", 32),
		},
	}
	if _, err := NewEncryptor(cfg); err != nil {
		t.Fatalf("expected accept, got %v", err)
	}
}

// TestNewEncryptor_EmptyPreviousKeysIsAllowed pins that an empty-string
// entry is silently skipped (not rejected). Empty strings model a
// no-op slot in the list, useful when env-parsing splits on commas.
func TestNewEncryptor_EmptyPreviousKeysIsAllowed(t *testing.T) {
	cfg := Config{
		Key:    strings.Repeat("a", 32),
		Cipher: "AES-256-CBC",
		PreviousKeys: []string{
			"",
			strings.Repeat("b", 32),
			"",
		},
	}
	if _, err := NewEncryptor(cfg); err != nil {
		t.Fatalf("empty PreviousKey entries must be tolerated, got %v", err)
	}
}
