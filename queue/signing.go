package queue

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/hkdf"
)

// minSigningKeyBytes is the floor for a raw QUEUE_SIGNING_KEY. HMAC-SHA256
// keys shorter than the hash output add no work for a brute-force attacker
// beyond their own length, so anything under 32 bytes weakens the only
// barrier between a store-writing attacker and the worker pipeline.
const minSigningKeyBytes = 32

var (
	signingMu      sync.RWMutex
	signingKey     []byte
	signingEnabled bool
	signingLogger  Logger
)

// SetSigningLogger installs a package-level logger used by
// ConfigureSigning to report signing-key diagnostics. Nil disables
// logging. Safe to call concurrently.
func SetSigningLogger(l Logger) {
	signingMu.Lock()
	defer signingMu.Unlock()
	signingLogger = l
}

// SigningOptions tunes how ConfigureSigningWith reacts when no signing
// key is available. Callers that only need the default (refuse to boot
// without a key outside dev/test) should use ConfigureSigning.
type SigningOptions struct {
	// AcceptUnsigned, when true, lets the queue boot with payload signing
	// disabled even when AllowUnsignedInDev is false. This is the loud
	// opt-in for operators migrating an existing fleet onto a signing key
	// or running a local dev queue without one. Set via the
	// QUEUE_ACCEPT_UNSIGNED=true env var; the warning log path stays so
	// the operator's intent is visible in startup logs.
	AcceptUnsigned bool

	// AllowUnsignedInDev, when true, treats an empty signing key as
	// non-fatal because the process is running under a development or
	// test profile. Production callers must leave this false so a missing
	// key is fatal; the framework passes true only when APP_ENV is local,
	// development, or test/testing.
	AllowUnsignedInDev bool
}

// ConfigureSigning configures payload signing from the provided keys with
// the fail-closed defaults: an empty key in a production environment
// returns ErrSigningKeyRequired so the boot path stops. Callers that need
// to override this (operator opt-in or dev/test profile) should use
// ConfigureSigningWith.
func ConfigureSigning(rawSigningKey, appKey string) error {
	return ConfigureSigningWith(rawSigningKey, appKey, SigningOptions{})
}

// ConfigureSigningWith is the option-aware form of ConfigureSigning. It
// derives the key from rawSigningKey (preferred) or appKey via HKDF, and
// when both are empty consults opts to decide whether the boot should
// fail. Returns ErrSigningKeyRequired when no key is configured AND
// neither AcceptUnsigned nor AllowUnsignedInDev is set; this is the
// fail-closed default that protects against an attacker who can write to
// the queue store enqueueing arbitrary jobs into an unverifying worker.
// Returns ErrSigningKeyTooShort when rawSigningKey is set but shorter
// than 32 bytes, since the raw key is used verbatim as the HMAC key.
//
// Must be called from velocity.New() after config is loaded.
func ConfigureSigningWith(rawSigningKey, appKey string, opts SigningOptions) error {
	key := rawSigningKey
	useAppKey := false
	if key == "" {
		key = appKey
		useAppKey = true
	}

	signingMu.Lock()
	defer signingMu.Unlock()

	if key == "" {
		switch {
		case opts.AcceptUnsigned:
			// Operator-acknowledged opt-in: warn once and proceed
			// without signing. The warning is the only signal that the
			// fleet is running unsigned, so it stays even when a
			// logger is wired.
			if signingLogger != nil {
				signingLogger.Warn("velocity/queue: QUEUE_ACCEPT_UNSIGNED=true; payload signing disabled. Set QUEUE_SIGNING_KEY or APP_KEY to enable HMAC verification.")
			}
			signingKey = nil
			signingEnabled = false
			return nil
		case opts.AllowUnsignedInDev:
			// Dev/test profile: unsigned payloads are tolerated so
			// unit tests and local-dev runs do not require a key.
			if signingLogger != nil {
				signingLogger.Warn("velocity/queue: no signing key found (QUEUE_SIGNING_KEY or APP_KEY); payload signing disabled in dev/test environment")
			}
			signingKey = nil
			signingEnabled = false
			return nil
		default:
			// Fail-closed: refuse to boot. An empty signing key in
			// production means any payload an attacker can place into
			// the queue store will be executed by a worker.
			signingKey = nil
			signingEnabled = false
			return ErrSigningKeyRequired
		}
	}

	if useAppKey {
		if signingLogger != nil {
			signingLogger.Warn("velocity/queue: using APP_KEY for queue signing. Set a dedicated QUEUE_SIGNING_KEY for production environments")
		}
		// Derive a queue-specific key from APP_KEY using HKDF to avoid
		// using the same key material for different purposes.
		r := hkdf.New(sha256.New, []byte(key), nil, []byte("queue-signing"))
		derived := make([]byte, 32)
		if _, err := io.ReadFull(r, derived); err != nil {
			return fmt.Errorf("velocity/queue: failed to derive signing key from app_key: %w", err)
		}
		signingKey = derived
	} else {
		// The raw key is used verbatim as the HMAC-SHA256 key, so enforce
		// the same 32-byte floor auth applies to JWT HMAC secrets. Reject
		// rather than HKDF-expand: changing the derivation for existing
		// valid keys would invalidate signatures on in-flight jobs.
		if len(key) < minSigningKeyBytes {
			signingKey = nil
			signingEnabled = false
			return fmt.Errorf("%w (got %d)", ErrSigningKeyTooShort, len(key))
		}
		signingKey = []byte(key)
	}
	signingEnabled = true
	return nil
}

// SetSigningKey configures the HMAC key for queue payload signing.
// Pass nil or empty to disable signing.
func SetSigningKey(key []byte) {
	signingMu.Lock()
	defer signingMu.Unlock()

	if len(key) > 0 {
		signingKey = key
		signingEnabled = true
	} else {
		signingKey = nil
		signingEnabled = false
	}
}

// IsSigningEnabled returns whether payload signing is active
func IsSigningEnabled() bool {
	signingMu.RLock()
	defer signingMu.RUnlock()
	return signingEnabled
}

// signPayload computes HMAC-SHA256 of the given data using the signing key.
// Returns empty string if signing is disabled.
func signPayload(data []byte) string {
	signingMu.RLock()
	defer signingMu.RUnlock()

	if !signingEnabled || len(signingKey) == 0 {
		return ""
	}

	mac := hmac.New(sha256.New, signingKey)
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

// marshalSigned marshals v to JSON, signs those bytes, and (when signing is
// enabled) calls setSig with the signature and re-marshals so the returned
// bytes carry the Signature field. The signature is computed over the UNSIGNED
// marshal; the signed bytes are the same value re-encoded with only the
// Signature field added. This is the shared producer-side serialize/sign/
// re-serialize dance used by every driver push path.
//
// unsignedErrMsg and signedErrMsg are the full error prefixes each call site
// wraps its two json.Marshal failures with, preserved verbatim so the helper
// is byte-for-byte and error-for-error equivalent to the inline code it
// replaces.
func marshalSigned(v any, setSig func(string), unsignedErrMsg, signedErrMsg string) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", unsignedErrMsg, err)
	}
	if sig := signPayload(data); sig != "" {
		setSig(sig)
		data, err = json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", signedErrMsg, err)
		}
	}
	return data, nil
}

// verifyPayload checks the HMAC signature of the given data.
// Returns nil if signing is disabled and the payload has no signature, or if the signature is valid.
// Rejects payloads that carry a signature when signing is disabled — a present
// signature indicates it came from a signed producer and must not be silently
// accepted by a verifier that cannot validate it.
func verifyPayload(data []byte, signature string) error {
	signingMu.RLock()
	enabled := signingEnabled
	key := signingKey
	signingMu.RUnlock()

	if !enabled || len(key) == 0 {
		if signature != "" {
			return fmt.Errorf("velocity/queue: payload has signature but signing is disabled")
		}
		return nil // Signing disabled and payload is unsigned; accept.
	}

	if signature == "" {
		return fmt.Errorf("velocity/queue: payload signature missing")
	}

	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	expected := hex.EncodeToString(mac.Sum(nil))

	if subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
		return fmt.Errorf("velocity/queue: payload signature verification failed")
	}

	return nil
}
