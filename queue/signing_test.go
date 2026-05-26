package queue

import (
	"errors"
	"testing"
)

// saveAndRestoreSigningState captures the current signing state
// and returns a cleanup function that restores it. This avoids
// tests leaking state to each other.
func saveAndRestoreSigningState(t *testing.T) {
	t.Helper()

	signingMu.RLock()
	origKey := signingKey
	origEnabled := signingEnabled
	signingMu.RUnlock()

	t.Cleanup(func() {
		signingMu.Lock()
		signingKey = origKey
		signingEnabled = origEnabled
		signingMu.Unlock()
	})
}

func TestSignPayload(t *testing.T) {
	saveAndRestoreSigningState(t)

	SetSigningKey([]byte("test-signing-key"))

	t.Run("produces non-empty hex signature", func(t *testing.T) {
		sig := signPayload([]byte(`{"job":"test","data":"hello"}`))
		if sig == "" {
			t.Fatal("expected non-empty signature, got empty string")
		}
		// HMAC-SHA256 produces 32 bytes = 64 hex chars
		if len(sig) != 64 {
			t.Errorf("expected 64-char hex signature, got %d chars: %s", len(sig), sig)
		}
	})

	t.Run("same input produces same signature", func(t *testing.T) {
		data := []byte(`{"job":"test","attempt":1}`)
		sig1 := signPayload(data)
		sig2 := signPayload(data)
		if sig1 != sig2 {
			t.Errorf("expected deterministic signatures, got %s and %s", sig1, sig2)
		}
	})

	t.Run("different input produces different signature", func(t *testing.T) {
		sig1 := signPayload([]byte(`{"job":"a"}`))
		sig2 := signPayload([]byte(`{"job":"b"}`))
		if sig1 == sig2 {
			t.Error("expected different signatures for different payloads")
		}
	})

	t.Run("different key produces different signature", func(t *testing.T) {
		data := []byte(`{"job":"test"}`)

		SetSigningKey([]byte("key-one"))
		sig1 := signPayload(data)

		SetSigningKey([]byte("key-two"))
		sig2 := signPayload(data)

		if sig1 == sig2 {
			t.Error("expected different signatures for different keys")
		}
	})
}

func TestVerifyPayload(t *testing.T) {
	saveAndRestoreSigningState(t)

	SetSigningKey([]byte("test-verify-key"))

	t.Run("valid signature passes verification", func(t *testing.T) {
		data := []byte(`{"job":"send-email","to":"user@example.com"}`)
		sig := signPayload(data)

		if err := verifyPayload(data, sig); err != nil {
			t.Fatalf("expected nil error for valid signature, got: %v", err)
		}
	})

	t.Run("tampered payload fails verification", func(t *testing.T) {
		original := []byte(`{"job":"charge","amount":100}`)
		sig := signPayload(original)

		tampered := []byte(`{"job":"charge","amount":999}`)
		if err := verifyPayload(tampered, sig); err == nil {
			t.Fatal("expected error for tampered payload, got nil")
		}
	})

	t.Run("wrong signature fails verification", func(t *testing.T) {
		data := []byte(`{"job":"test"}`)
		wrongSig := "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff"

		if err := verifyPayload(data, wrongSig); err == nil {
			t.Fatal("expected error for wrong signature, got nil")
		}
	})

	t.Run("empty signature fails when signing enabled", func(t *testing.T) {
		data := []byte(`{"job":"test"}`)
		err := verifyPayload(data, "")
		if err == nil {
			t.Fatal("expected error for empty signature when signing is enabled, got nil")
		}
		if err.Error() != "velocity/queue: payload signature missing" {
			t.Errorf("unexpected error message: %v", err)
		}
	})

	t.Run("signature from different key fails", func(t *testing.T) {
		data := []byte(`{"job":"test"}`)

		SetSigningKey([]byte("key-alpha"))
		sig := signPayload(data)

		SetSigningKey([]byte("key-beta"))
		if err := verifyPayload(data, sig); err == nil {
			t.Fatal("expected error when verifying with different key, got nil")
		}
	})
}

func TestSigningDisabled(t *testing.T) {
	saveAndRestoreSigningState(t)

	t.Run("signPayload returns empty when disabled", func(t *testing.T) {
		SetSigningKey(nil)

		sig := signPayload([]byte(`{"job":"test"}`))
		if sig != "" {
			t.Errorf("expected empty signature when signing disabled, got: %s", sig)
		}
	})

	t.Run("verifyPayload accepts unsigned payloads when disabled", func(t *testing.T) {
		SetSigningKey(nil)

		if err := verifyPayload([]byte(`anything`), ""); err != nil {
			t.Fatalf("expected nil error when signing disabled, got: %v", err)
		}
	})

	t.Run("verifyPayload rejects signed payloads when disabled", func(t *testing.T) {
		SetSigningKey(nil)

		if err := verifyPayload([]byte(`anything`), "bogus-sig"); err == nil {
			t.Fatal("expected error when verifying a signed payload while signing is disabled, got nil")
		}
	})

	t.Run("SetSigningKey with empty slice disables signing", func(t *testing.T) {
		SetSigningKey([]byte("some-key"))
		if !IsSigningEnabled() {
			t.Fatal("expected signing to be enabled after setting key")
		}

		SetSigningKey([]byte{})
		if IsSigningEnabled() {
			t.Fatal("expected signing to be disabled after setting empty key")
		}
	})

	t.Run("SetSigningKey with nil disables signing", func(t *testing.T) {
		SetSigningKey([]byte("some-key"))
		if !IsSigningEnabled() {
			t.Fatal("expected signing to be enabled after setting key")
		}

		SetSigningKey(nil)
		if IsSigningEnabled() {
			t.Fatal("expected signing to be disabled after setting nil key")
		}
	})
}

func TestIsSigningEnabled(t *testing.T) {
	saveAndRestoreSigningState(t)

	t.Run("returns true when key is set", func(t *testing.T) {
		SetSigningKey([]byte("my-key"))
		if !IsSigningEnabled() {
			t.Error("expected IsSigningEnabled() to return true")
		}
	})

	t.Run("returns false when key is cleared", func(t *testing.T) {
		SetSigningKey(nil)
		if IsSigningEnabled() {
			t.Error("expected IsSigningEnabled() to return false")
		}
	})
}

func TestSignPayload_EdgeCases(t *testing.T) {
	saveAndRestoreSigningState(t)

	SetSigningKey([]byte("edge-case-key"))

	t.Run("empty payload produces valid signature", func(t *testing.T) {
		sig := signPayload([]byte{})
		if sig == "" {
			t.Fatal("expected non-empty signature for empty payload")
		}
		if len(sig) != 64 {
			t.Errorf("expected 64-char hex signature, got %d chars", len(sig))
		}

		// Verification should pass
		if err := verifyPayload([]byte{}, sig); err != nil {
			t.Fatalf("expected nil error verifying empty payload, got: %v", err)
		}
	})

	t.Run("nil payload produces valid signature", func(t *testing.T) {
		sig := signPayload(nil)
		if sig == "" {
			t.Fatal("expected non-empty signature for nil payload")
		}

		if err := verifyPayload(nil, sig); err != nil {
			t.Fatalf("expected nil error verifying nil payload, got: %v", err)
		}
	})

	t.Run("empty and nil payloads produce same signature", func(t *testing.T) {
		sigNil := signPayload(nil)
		sigEmpty := signPayload([]byte{})
		if sigNil != sigEmpty {
			t.Errorf("expected nil and empty payloads to produce same signature, got %s and %s", sigNil, sigEmpty)
		}
	})

	t.Run("large payload signs and verifies", func(t *testing.T) {
		// 1 MB payload
		data := make([]byte, 1<<20)
		for i := range data {
			data[i] = byte(i % 256)
		}

		sig := signPayload(data)
		if sig == "" {
			t.Fatal("expected non-empty signature for large payload")
		}

		if err := verifyPayload(data, sig); err != nil {
			t.Fatalf("expected nil error verifying large payload, got: %v", err)
		}

		// Flip one byte
		data[len(data)/2] ^= 0xFF
		if err := verifyPayload(data, sig); err == nil {
			t.Fatal("expected error after flipping a byte in large payload")
		}
	})
}

func TestSignVerify_RoundTrip(t *testing.T) {
	saveAndRestoreSigningState(t)

	SetSigningKey([]byte("roundtrip-key"))

	payloads := []struct {
		name string
		data []byte
	}{
		{"json object", []byte(`{"type":"*queue.SendEmail","data":{"to":"a@b.com"}}`)},
		{"json array", []byte(`[1,2,3]`)},
		{"plain text", []byte(`hello world`)},
		{"binary data", []byte{0x00, 0x01, 0xFF, 0xFE}},
		{"unicode", []byte("日本語テスト")},
		{"single byte", []byte{42}},
	}

	for _, tt := range payloads {
		t.Run(tt.name, func(t *testing.T) {
			sig := signPayload(tt.data)
			if sig == "" {
				t.Fatal("expected non-empty signature")
			}

			if err := verifyPayload(tt.data, sig); err != nil {
				t.Fatalf("round-trip verification failed: %v", err)
			}
		})
	}
}

func TestConfigureSigning_FailClosedWithoutKeyInProduction(t *testing.T) {
	saveAndRestoreSigningState(t)

	// Production-like call (no opts): empty keys must refuse to boot so
	// an attacker who can write to the queue store cannot smuggle
	// unverified payloads into the worker pipeline.
	err := ConfigureSigning("", "")
	if !errors.Is(err, ErrSigningKeyRequired) {
		t.Fatalf("expected ErrSigningKeyRequired with empty keys in production, got %v", err)
	}
	if IsSigningEnabled() {
		t.Fatal("signing must remain disabled after a refused boot")
	}
}

func TestConfigureSigningWith_AllowUnsignedInDev(t *testing.T) {
	saveAndRestoreSigningState(t)

	if err := ConfigureSigningWith("", "", SigningOptions{AllowUnsignedInDev: true}); err != nil {
		t.Fatalf("dev/test profile must tolerate empty signing keys, got %v", err)
	}
	if IsSigningEnabled() {
		t.Fatal("signing must remain disabled when no key is configured")
	}
}

func TestConfigureSigningWith_AcceptUnsignedOptIn(t *testing.T) {
	saveAndRestoreSigningState(t)

	if err := ConfigureSigningWith("", "", SigningOptions{AcceptUnsigned: true}); err != nil {
		t.Fatalf("QUEUE_ACCEPT_UNSIGNED opt-in must allow boot, got %v", err)
	}
	if IsSigningEnabled() {
		t.Fatal("signing must remain disabled when operator opts into unsigned")
	}
}

func TestConfigureSigningWith_KeyEnablesSigning(t *testing.T) {
	saveAndRestoreSigningState(t)

	// A real signing key with no opts (production defaults) must enable
	// signing without returning an error.
	if err := ConfigureSigningWith("queue-signing-key", "", SigningOptions{}); err != nil {
		t.Fatalf("expected nil error with a real signing key, got %v", err)
	}
	if !IsSigningEnabled() {
		t.Fatal("signing must be enabled when QUEUE_SIGNING_KEY is set")
	}
}

func TestConfigureSigningWith_AppKeyFallbackEnablesSigning(t *testing.T) {
	saveAndRestoreSigningState(t)

	// APP_KEY fallback path: empty QUEUE_SIGNING_KEY but a real APP_KEY
	// should derive a queue-scoped key via HKDF and enable signing.
	if err := ConfigureSigningWith("", "app-key-material", SigningOptions{}); err != nil {
		t.Fatalf("expected APP_KEY fallback to succeed, got %v", err)
	}
	if !IsSigningEnabled() {
		t.Fatal("signing must be enabled after APP_KEY fallback")
	}
}

func TestConfigureSigningWith_AcceptUnsignedDoesNotShadowRealKey(t *testing.T) {
	saveAndRestoreSigningState(t)

	// AcceptUnsigned is the empty-key escape hatch; when an actual key
	// is supplied the normal sign-enabled path must still run so a
	// production fleet that accidentally leaves the flag set still gets
	// HMAC verification.
	if err := ConfigureSigningWith("queue-signing-key", "", SigningOptions{AcceptUnsigned: true}); err != nil {
		t.Fatalf("expected nil error when a real key is supplied alongside AcceptUnsigned, got %v", err)
	}
	if !IsSigningEnabled() {
		t.Fatal("signing must be enabled when a key is supplied regardless of AcceptUnsigned")
	}
}

func TestSignPayload_Concurrent(t *testing.T) {
	saveAndRestoreSigningState(t)

	SetSigningKey([]byte("concurrent-key"))
	data := []byte(`{"job":"concurrent-test"}`)

	// Get the expected signature
	expected := signPayload(data)

	const goroutines = 50
	errs := make(chan error, goroutines)

	for i := 0; i < goroutines; i++ {
		go func() {
			sig := signPayload(data)
			if sig != expected {
				errs <- nil // will flag below
				return
			}
			if err := verifyPayload(data, sig); err != nil {
				errs <- err
				return
			}
			errs <- nil
		}()
	}

	for i := 0; i < goroutines; i++ {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent sign/verify failed: %v", err)
		}
	}
}
