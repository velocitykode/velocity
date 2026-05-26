package crypto

import (
	"errors"
	"os"
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
	// Clear any inherited opt-out so the test is self-contained.
	t.Setenv("CRYPTO_IGNORE_INVALID_PREVIOUS_KEYS", "")

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
	t.Setenv("CRYPTO_IGNORE_INVALID_PREVIOUS_KEYS", "")

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

// TestNewEncryptor_LengthMismatchPreviousKeyOptOut confirms the
// CRYPTO_IGNORE_INVALID_PREVIOUS_KEYS=true escape hatch also tolerates
// length-mismatched entries (in addition to parse-failure entries
// already covered by TestNewEncryptor_IgnoreInvalidPreviousKeysOptOut).
func TestNewEncryptor_LengthMismatchPreviousKeyOptOut(t *testing.T) {
	t.Setenv("CRYPTO_IGNORE_INVALID_PREVIOUS_KEYS", "true")

	cfg := Config{
		Key:    strings.Repeat("a", 32),
		Cipher: "AES-256-CBC",
		PreviousKeys: []string{
			"base64:MTIzNDU2Nzg5MGFiY2RlZg==", // 16 bytes
			strings.Repeat("b", 32),           // valid
		},
	}
	enc, err := NewEncryptor(cfg)
	if err != nil {
		t.Fatalf("opt-out should tolerate length mismatch, got %v", err)
	}
	if enc == nil {
		t.Fatal("expected non-nil encryptor")
	}
}

// TestNewEncryptor_AcceptsValidPreviousKeys is a positive control:
// well-formed previous keys must not trip the fail-fast path.
func TestNewEncryptor_AcceptsValidPreviousKeys(t *testing.T) {
	t.Setenv("CRYPTO_IGNORE_INVALID_PREVIOUS_KEYS", "")
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

// TestNewEncryptor_IgnoreInvalidPreviousKeysOptOut confirms the
// CRYPTO_IGNORE_INVALID_PREVIOUS_KEYS=true escape hatch restores the
// legacy continue-past-malformed-entries behaviour for operators with a
// transient migration. The opt-out is env-only (no Config field) so it
// shows up in deployment manifests during review.
func TestNewEncryptor_IgnoreInvalidPreviousKeysOptOut(t *testing.T) {
	t.Setenv("CRYPTO_IGNORE_INVALID_PREVIOUS_KEYS", "true")
	cfg := Config{
		Key:    strings.Repeat("a", 32),
		Cipher: "AES-256-CBC",
		PreviousKeys: []string{
			"base64:!!!not-base64!!!",
			strings.Repeat("b", 32),
		},
	}
	enc, err := NewEncryptor(cfg)
	if err != nil {
		t.Fatalf("opt-out should tolerate invalid entries, got %v", err)
	}
	if enc == nil {
		t.Fatal("expected non-nil encryptor")
	}
}

// TestNewEncryptor_IgnoreInvalidPreviousKeysOptOut_Strict_RequiresExactString
// pins the opt-out trigger string. Truthy variants like "1" or "TRUE"
// must NOT enable the legacy behaviour; operators must spell the value
// exactly so the opt-out is reviewable.
func TestNewEncryptor_IgnoreInvalidPreviousKeysOptOut_Strict_RequiresExactString(t *testing.T) {
	for _, v := range []string{"1", "TRUE", "yes", "True"} {
		v := v
		t.Run(v, func(t *testing.T) {
			t.Setenv("CRYPTO_IGNORE_INVALID_PREVIOUS_KEYS", v)
			cfg := Config{
				Key:    strings.Repeat("a", 32),
				Cipher: "AES-256-CBC",
				PreviousKeys: []string{
					"base64:!!!not-base64!!!",
				},
			}
			_, err := NewEncryptor(cfg)
			if err == nil {
				t.Fatalf("opt-out value %q should not enable legacy behaviour", v)
			}
			if !errors.Is(err, ErrInvalidPreviousKey) {
				t.Fatalf("expected ErrInvalidPreviousKey, got %v", err)
			}
		})
	}
}

// TestNewEncryptor_EmptyPreviousKeysIsAllowed pins that an empty-string
// entry is silently skipped (not rejected). Empty strings model a
// no-op slot in the list, useful when env-parsing splits on commas.
func TestNewEncryptor_EmptyPreviousKeysIsAllowed(t *testing.T) {
	// Be explicit about the env state.
	_ = os.Unsetenv("CRYPTO_IGNORE_INVALID_PREVIOUS_KEYS")
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
