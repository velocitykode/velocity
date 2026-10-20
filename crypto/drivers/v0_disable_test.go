package drivers

import (
	"errors"
	"strings"
	"testing"
)

// TestV0Disabled_RejectsLegacyPayload asserts that WithV0Disabled(true)
// at driver-construction time causes every v0-shaped payload to be
// rejected with ErrLegacyPayloadDisabled, without touching the
// MAC-over-base64 verification path. Operators flip this on once their
// rotation window completes; the surface area of the weaker v0 MAC is
// no longer reachable.
func TestV0Disabled_RejectsLegacyPayload(t *testing.T) {
	// Construct a driver WITHOUT the disable flag so we can produce a
	// legitimate v0 payload using the test-only helper.
	dEnabled, err := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")
	if err != nil {
		t.Fatalf("NewAESDriver (enabled): %v", err)
	}
	v0Payload := handCraftLegacyCBC(t, dEnabled, []byte("classified"))

	// Sanity check: with v0 enabled the payload round-trips.
	if got, err := dEnabled.Decrypt(v0Payload); err != nil || got != "classified" {
		t.Fatalf("v0-enabled baseline: got=%q err=%v", got, err)
	}

	// Now construct a second driver with the same key but with v0
	// decoding disabled. The same payload must be rejected up-front.
	dDisabled, err := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC", WithV0Disabled(true))
	if err != nil {
		t.Fatalf("NewAESDriver (disabled): %v", err)
	}
	_, gotErr := dDisabled.Decrypt(v0Payload)
	if gotErr == nil {
		t.Fatal("expected ErrLegacyPayloadDisabled, got nil")
	}
	if !errors.Is(gotErr, ErrLegacyPayloadDisabled) {
		t.Fatalf("expected ErrLegacyPayloadDisabled, got %v", gotErr)
	}
}

// TestV0Disabled_StillAcceptsV1 confirms the disable flag is scoped to
// v0 only. v1 payloads (the format encryptCBC / encryptGCM emit today)
// must continue to round-trip unchanged.
func TestV0Disabled_StillAcceptsV1(t *testing.T) {
	d, err := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC", WithV0Disabled(true))
	if err != nil {
		t.Fatalf("NewAESDriver: %v", err)
	}
	enc, err := d.Encrypt("classified")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	if !strings.HasPrefix(enc, "v1:") {
		t.Fatalf("expected v1 sentinel, got %q", enc)
	}
	got, err := d.Decrypt(enc)
	if err != nil {
		t.Fatalf("Decrypt v1 with v0 disabled: %v", err)
	}
	if got != "classified" {
		t.Fatalf("round trip mismatch: got %q want classified", got)
	}
}

// TestV0Disabled_DefaultIsEnabled is the backward-compat guard:
// existing deployments without the env var set continue to accept v0
// payloads (the rotation window must remain operator-controlled, not
// flipped by the framework upgrade).
func TestV0Disabled_DefaultIsEnabled(t *testing.T) {
	d, err := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")
	if err != nil {
		t.Fatalf("NewAESDriver: %v", err)
	}
	payload := handCraftLegacyCBC(t, d, []byte("history"))
	got, err := d.Decrypt(payload)
	if err != nil {
		t.Fatalf("default config must accept v0, got %v", err)
	}
	if got != "history" {
		t.Fatalf("got %q want history", got)
	}
}
