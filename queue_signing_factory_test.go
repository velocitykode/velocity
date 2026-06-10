package velocity

import (
	"errors"
	"testing"

	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/queue"
)

// resetQueueSigningState restores the queue's package-level signing
// state after a test mutates it. Without this, a fail-closed test that
// flips signingEnabled=false would leak into the next test that assumes
// a key is wired.
func resetQueueSigningState(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		queue.SetSigningKey(nil)
	})
}

// TestInitQueue_FailsClosedOnEmptyKeyInProduction asserts the fail-closed
// guard for M-42: when appEnv is "production" and the operator has not
// opted into unsigned payloads, initQueue must return
// queue.ErrSigningKeyRequired so boot stops. Allowing an empty key
// through silently accepts any payload an attacker plants in the queue
// store.
func TestInitQueue_FailsClosedOnEmptyKeyInProduction(t *testing.T) {
	resetQueueSigningState(t)
	t.Setenv("QUEUE_ACCEPT_UNSIGNED", "")

	_, err := initQueue(QueueConfig{Driver: "memory"}, nil, "", "", "", "production", nil, log.NewNullLogger())
	if !errors.Is(err, queue.ErrSigningKeyRequired) {
		t.Fatalf("expected ErrSigningKeyRequired, got %v", err)
	}
}

// TestInitQueue_QueueAcceptUnsignedAllowsEmptyKey asserts the operator
// opt-in: setting QUEUE_ACCEPT_UNSIGNED=true must let the queue boot
// without a signing key even in production, because some operators need
// a migration window or are running a local-dev queue. The warning
// log path stays so the choice is visible.
func TestInitQueue_QueueAcceptUnsignedAllowsEmptyKey(t *testing.T) {
	resetQueueSigningState(t)
	t.Setenv("QUEUE_ACCEPT_UNSIGNED", "true")

	d, err := initQueue(QueueConfig{Driver: "memory"}, nil, "", "", "", "production", nil, log.NewNullLogger())
	if err != nil {
		t.Fatalf("expected nil error with QUEUE_ACCEPT_UNSIGNED=true, got %v", err)
	}
	if d == nil {
		t.Fatal("expected a driver instance")
	}
	if queue.IsSigningEnabled() {
		t.Fatal("signing must remain disabled when operator opts into unsigned")
	}
}

// TestInitQueue_ValidSigningKeyEnablesSigning asserts the happy path:
// a real key always wins regardless of env, so a production deploy with
// QUEUE_SIGNING_KEY set boots with HMAC verification on.
func TestInitQueue_ValidSigningKeyEnablesSigning(t *testing.T) {
	resetQueueSigningState(t)
	t.Setenv("QUEUE_ACCEPT_UNSIGNED", "")

	d, err := initQueue(QueueConfig{Driver: "memory", SigningKey: "real-signing-key-32-bytes-long!!"}, nil, "", "real-signing-key-32-bytes-long!!", "", "production", nil, log.NewNullLogger())
	if err != nil {
		t.Fatalf("expected nil error with a real signing key, got %v", err)
	}
	if d == nil {
		t.Fatal("expected a driver instance")
	}
	if !queue.IsSigningEnabled() {
		t.Fatal("signing must be enabled when QUEUE_SIGNING_KEY is set")
	}
}

// TestInitQueue_ShortSigningKeyFailsClosed asserts the 32-byte floor on a
// raw QUEUE_SIGNING_KEY (V2-13): the raw key is used verbatim as the
// HMAC-SHA256 key, so a short key must stop boot with
// queue.ErrSigningKeyTooShort instead of running with a weak barrier.
func TestInitQueue_ShortSigningKeyFailsClosed(t *testing.T) {
	resetQueueSigningState(t)
	t.Setenv("QUEUE_ACCEPT_UNSIGNED", "")

	shortKey := "31-byte-key-aaaaaaaaaaaaaaaaaaa"
	_, err := initQueue(QueueConfig{Driver: "memory", SigningKey: shortKey}, nil, "", shortKey, "", "production", nil, log.NewNullLogger())
	if !errors.Is(err, queue.ErrSigningKeyTooShort) {
		t.Fatalf("expected ErrSigningKeyTooShort, got %v", err)
	}
	if queue.IsSigningEnabled() {
		t.Fatal("signing must remain disabled after a rejected short key")
	}
}

// TestInitQueue_DevEnvAllowsEmptyKey asserts the dev/test relaxation:
// appEnv "local" with no signing key must not break unit tests or local
// runs that simply do not wire a key. Production callers don't hit this
// branch because Config.Env is unset or "production".
func TestInitQueue_DevEnvAllowsEmptyKey(t *testing.T) {
	resetQueueSigningState(t)
	t.Setenv("QUEUE_ACCEPT_UNSIGNED", "")

	d, err := initQueue(QueueConfig{Driver: "memory"}, nil, "", "", "", "local", nil, log.NewNullLogger())
	if err != nil {
		t.Fatalf("expected nil error in dev env, got %v", err)
	}
	if d == nil {
		t.Fatal("expected a driver instance")
	}
	if queue.IsSigningEnabled() {
		t.Fatal("signing must remain disabled when no key is configured")
	}
}
