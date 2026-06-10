package velocity

import (
	"errors"
	"testing"

	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/queue"
)

// resetQueueEncryptionState clears the queue's package-level payload
// encryptor after a test so encryption cannot leak into tests that assume
// plaintext payloads.
func resetQueueEncryptionState(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		queue.SetPayloadEncryptor(nil)
	})
}

// TestInitQueue_EncryptRequiresEncryptor asserts the fail-closed guard for
// V2-07: QUEUE_ENCRYPT=true with no app encryptor (APP_KEY unset, crypto
// subsystem disabled) must stop boot rather than silently persisting the
// plaintext payloads the operator believes are encrypted.
func TestInitQueue_EncryptRequiresEncryptor(t *testing.T) {
	resetQueueSigningState(t)
	resetQueueEncryptionState(t)
	t.Setenv("QUEUE_ACCEPT_UNSIGNED", "")

	_, err := initQueue(QueueConfig{Driver: "memory", Encrypt: true}, nil, "", "", "", "local", nil, log.NewNullLogger())
	if !errors.Is(err, queue.ErrEncryptorRequired) {
		t.Fatalf("expected ErrEncryptorRequired, got %v", err)
	}
	if queue.IsEncryptionEnabled() {
		t.Fatal("encryption must remain disabled after a failed boot")
	}
}

// TestInitQueue_EncryptWiresEncryptor asserts the happy path: with
// QUEUE_ENCRYPT=true and a constructed app encryptor, initQueue installs
// it as the queue payload encryptor.
func TestInitQueue_EncryptWiresEncryptor(t *testing.T) {
	resetQueueSigningState(t)
	resetQueueEncryptionState(t)
	t.Setenv("QUEUE_ACCEPT_UNSIGNED", "")

	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    "base64:MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}

	d, err := initQueue(QueueConfig{Driver: "memory", Encrypt: true}, nil, "", "", "", "local", enc, log.NewNullLogger())
	if err != nil {
		t.Fatalf("initQueue: %v", err)
	}
	if d == nil {
		t.Fatal("expected a driver instance")
	}
	if !queue.IsEncryptionEnabled() {
		t.Fatal("payload encryption must be enabled when QUEUE_ENCRYPT=true and an encryptor exists")
	}
}

// TestInitQueue_EncryptOffClearsEncryptor asserts that a boot WITHOUT
// QUEUE_ENCRYPT clears any encryptor left behind by a previous app
// instance in the same process (tests, embed mode).
func TestInitQueue_EncryptOffClearsEncryptor(t *testing.T) {
	resetQueueSigningState(t)
	resetQueueEncryptionState(t)
	t.Setenv("QUEUE_ACCEPT_UNSIGNED", "")

	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    "base64:MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	queue.SetPayloadEncryptor(enc)

	if _, err := initQueue(QueueConfig{Driver: "memory"}, nil, "", "", "", "local", nil, log.NewNullLogger()); err != nil {
		t.Fatalf("initQueue: %v", err)
	}
	if queue.IsEncryptionEnabled() {
		t.Fatal("encryption must be cleared when QUEUE_ENCRYPT is off")
	}
}
