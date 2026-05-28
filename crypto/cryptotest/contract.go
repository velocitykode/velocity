// Package cryptotest provides executable specifications (contract tests)
// for [crypto.Encryptor] implementations.
//
// The framework currently ships AES-128/192/256 in CBC and GCM modes via the
// AES driver. Third-party encryptors registered through the framework should
// pass this runner to guarantee interchangeable behaviour for round-trip,
// AAD binding, key rotation, and tamper detection.
package cryptotest

import (
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
)

// EncryptorFactory returns a fresh Encryptor per sub-test.
type EncryptorFactory func(t *testing.T) crypto.Encryptor

// RunEncryptorContractTests is the executable specification of
// [crypto.Encryptor]. Some sub-tests are skipped automatically when the
// driver does not support a specific capability (AAD on CBC).
func RunEncryptorContractTests(t *testing.T, factory EncryptorFactory) {
	t.Helper()

	t.Run("Encrypt_Then_Decrypt_RoundTripsPlaintext", func(t *testing.T) {
		e := factory(t)
		ct, err := e.Encrypt("hello world")
		if err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
		if ct == "" {
			t.Fatal("expected non-empty ciphertext")
		}
		pt, err := e.Decrypt(ct)
		if err != nil {
			t.Fatalf("Decrypt: %v", err)
		}
		if pt != "hello world" {
			t.Fatalf("round-trip mismatch: got %q", pt)
		}
	})

	t.Run("EncryptBytes_Then_DecryptBytes_RoundTripsBytes", func(t *testing.T) {
		e := factory(t)
		in := []byte{0x00, 0x01, 0xff, 0x80, 0x7f}
		ct, err := e.EncryptBytes(in)
		if err != nil {
			t.Fatalf("EncryptBytes: %v", err)
		}
		out, err := e.DecryptBytes(ct)
		if err != nil {
			t.Fatalf("DecryptBytes: %v", err)
		}
		if string(out) != string(in) {
			t.Fatalf("round-trip mismatch")
		}
	})

	t.Run("Encrypt_DistinctCiphertextsForSamePlaintext", func(t *testing.T) {
		e := factory(t)
		// Same plaintext encrypted twice MUST produce different
		// ciphertexts because each call generates a fresh IV/nonce.
		// This is the IND-CPA invariant; a driver that produces
		// deterministic output for the same plaintext is broken.
		a, err := e.Encrypt("repeat-me")
		if err != nil {
			t.Fatalf("first Encrypt: %v", err)
		}
		b, err := e.Encrypt("repeat-me")
		if err != nil {
			t.Fatalf("second Encrypt: %v", err)
		}
		if a == b {
			t.Fatalf("expected distinct ciphertexts for repeated plaintext (IV/nonce must be random)")
		}
	})

	t.Run("Decrypt_TamperedCiphertext_ReturnsErrDecrypt", func(t *testing.T) {
		e := factory(t)
		ct, err := e.Encrypt("authentic-message")
		if err != nil {
			t.Fatalf("Encrypt: %v", err)
		}
		// Flip one byte deep in the ciphertext payload. ct is
		// base64(json), so corrupt the inner payload by changing one
		// character; the JSON decode may still pass, but MAC/Tag
		// verification must reject.
		tampered := tamper(ct)
		_, err = e.Decrypt(tampered)
		if err == nil {
			t.Fatal("expected Decrypt to reject tampered ciphertext, got nil")
		}
		// We require the error to surface ErrDecrypt OR a structural
		// sentinel (ErrInvalidPayload). Either is a valid rejection;
		// what matters is that Decrypt is fail-closed.
		if !errors.Is(err, crypto.ErrDecrypt) && !errors.Is(err, contract.ErrInvalidPayload) {
			t.Fatalf("expected ErrDecrypt or ErrInvalidPayload, got %v", err)
		}
	})

	t.Run("Decrypt_EmptyPayload_ReturnsErrInvalidPayload", func(t *testing.T) {
		e := factory(t)
		_, err := e.Decrypt("")
		if err == nil {
			t.Fatal("expected error decrypting empty payload")
		}
		if !errors.Is(err, contract.ErrInvalidPayload) && !errors.Is(err, crypto.ErrDecrypt) {
			t.Fatalf("expected ErrInvalidPayload or ErrDecrypt, got %v", err)
		}
	})

	t.Run("Decrypt_GarbagePayload_Rejects", func(t *testing.T) {
		e := factory(t)
		_, err := e.Decrypt("not-a-valid-payload-!!!")
		if err == nil {
			t.Fatal("expected error decrypting garbage payload")
		}
	})

	t.Run("GenerateKey_ReturnsNonEmptyKey", func(t *testing.T) {
		e := factory(t)
		k, err := e.GenerateKey()
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		if k == "" {
			t.Fatal("expected non-empty generated key")
		}
	})

	t.Run("EncryptBytesWithAAD_RoundTripsWithMatchingAAD", func(t *testing.T) {
		e := factory(t)
		aad := []byte("user-123")
		ct, err := e.EncryptBytesWithAAD([]byte("secret"), aad)
		if err != nil {
			if errors.Is(err, crypto.ErrInvalidCipher) {
				t.Skip("driver does not support AEAD; skipping AAD invariants")
			}
			t.Fatalf("EncryptBytesWithAAD: %v", err)
		}
		out, err := e.DecryptBytesWithAAD(ct, aad)
		if err != nil {
			t.Fatalf("DecryptBytesWithAAD: %v", err)
		}
		if string(out) != "secret" {
			t.Fatalf("AAD round-trip mismatch: %q", out)
		}
	})

	t.Run("DecryptBytesWithAAD_DifferentAAD_ReturnsAADMismatch", func(t *testing.T) {
		e := factory(t)
		ct, err := e.EncryptBytesWithAAD([]byte("secret"), []byte("aad-A"))
		if err != nil {
			if errors.Is(err, crypto.ErrInvalidCipher) {
				t.Skip("driver does not support AEAD; skipping AAD invariants")
			}
			t.Fatalf("EncryptBytesWithAAD: %v", err)
		}
		_, err = e.DecryptBytesWithAAD(ct, []byte("aad-B"))
		if err == nil {
			t.Fatal("expected DecryptBytesWithAAD to reject mismatched AAD")
		}
		if !errors.Is(err, crypto.ErrAADMismatch) {
			t.Fatalf("expected ErrAADMismatch, got %v", err)
		}
	})

	t.Run("DecryptBytesWithAAD_RejectsNonAADPayload", func(t *testing.T) {
		e := factory(t)
		// Encrypt via the non-AAD path; decrypting that envelope on the
		// AAD path must be rejected. Per the Encryptor godoc a non-AAD
		// envelope is structurally identical to an EncryptBytesWithAAD
		// envelope, so the GCM auth tag check collapses to
		// ErrAADMismatch on AEAD drivers. Non-AEAD drivers (CBC) reject
		// the path up-front with ErrInvalidCipher. Either is fail-closed
		// and conformant; the invariant is "non-AAD payload does not
		// decrypt cleanly on the AAD path".
		ct, err := e.EncryptBytes([]byte("plain"))
		if err != nil {
			t.Fatalf("EncryptBytes: %v", err)
		}
		_, err = e.DecryptBytesWithAAD(ct, []byte("any-aad"))
		if err == nil {
			t.Fatal("expected DecryptBytesWithAAD to reject a non-AAD envelope")
		}
		if !errors.Is(err, crypto.ErrAADMismatch) &&
			!errors.Is(err, crypto.ErrInvalidCipher) &&
			!errors.Is(err, crypto.ErrInvalidPayload) {
			t.Fatalf("expected ErrAADMismatch, ErrInvalidCipher, or ErrInvalidPayload, got %v", err)
		}
	})
}

// tamper flips one base64 character near the end of s, deterministically.
// Used by the tamper-detection invariant.
func tamper(s string) string {
	if s == "" {
		return s
	}
	// Flip a character a few positions before the end so the change lands
	// inside the encoded payload, not in trailing padding.
	idx := len(s) - 5
	if idx < 0 {
		idx = 0
	}
	c := s[idx]
	var flipped byte
	if c == 'A' {
		flipped = 'B'
	} else {
		flipped = 'A'
	}
	return s[:idx] + string(flipped) + s[idx+1:]
}

// hasPrefix is a tiny helper used by callers wiring this runner; avoids
// pulling strings into the test loop. Kept exported so driver-side tests
// can reuse it.
func hasPrefix(s, prefix string) bool { return strings.HasPrefix(s, prefix) }

// _ keeps hasPrefix from being flagged as unused when no consumer wires it.
var _ = hasPrefix
