package queue

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"sync"

	"golang.org/x/crypto/hkdf"
)

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

// ConfigureSigning configures payload signing from the provided keys.
// signingKey is the dedicated QUEUE_SIGNING_KEY; appKey is the fallback APP_KEY.
// If signingKey is empty, appKey is used with HKDF derivation.
// Must be called from velocity.New() after config is loaded.
//
// Signing is enabled whenever signingKey or appKey is non-empty. Returns an
// error when HKDF derivation from APP_KEY fails so the boot sequence can
// fail visibly instead of silently running without integrity protection.
// Returns nil (and leaves signing disabled) when neither key is set.
func ConfigureSigning(rawSigningKey, appKey string) error {
	key := rawSigningKey
	useAppKey := false
	if key == "" {
		key = appKey
		useAppKey = true
	}

	signingMu.Lock()
	defer signingMu.Unlock()

	if key == "" {
		if signingLogger != nil {
			signingLogger.Warn("velocity/queue: no signing key found (QUEUE_SIGNING_KEY or APP_KEY), payload signing disabled")
		}
		signingKey = nil
		signingEnabled = false
		return nil
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
