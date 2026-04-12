package queue

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/crypto/hkdf"
)

var (
	signingMu      sync.RWMutex
	signingKey     []byte
	signingEnabled bool
)

// ConfigureSigning reads signing config from the environment. Must be
// called after .env has been loaded (i.e. from velocity.New() via the
// queue factory) — not from package init, because env vars injected by
// godotenv.Load aren't available until main() runs.
//
// Signing is enabled whenever QUEUE_SIGNING_KEY or APP_KEY is present.
func ConfigureSigning() {
	key := os.Getenv("QUEUE_SIGNING_KEY")
	useAppKey := false
	if key == "" {
		key = os.Getenv("APP_KEY")
		useAppKey = true
	}

	signingMu.Lock()
	defer signingMu.Unlock()

	if key == "" {
		fmt.Fprintln(os.Stderr, "queue: no signing key found (QUEUE_SIGNING_KEY or APP_KEY), payload signing disabled")
		return
	}

	if useAppKey {
		fmt.Fprintln(os.Stderr, "queue: WARNING: using APP_KEY for queue signing. Set a dedicated QUEUE_SIGNING_KEY for production environments")
		// Derive a queue-specific key from APP_KEY using HKDF to avoid
		// using the same key material for different purposes.
		r := hkdf.New(sha256.New, []byte(key), nil, []byte("queue-signing"))
		derived := make([]byte, 32)
		if _, err := io.ReadFull(r, derived); err != nil {
			fmt.Fprintf(os.Stderr, "queue: failed to derive signing key from APP_KEY: %v\n", err)
			return
		}
		signingKey = derived
	} else {
		signingKey = []byte(key)
	}
	signingEnabled = true
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
// Returns nil if signing is disabled or if the signature is valid.
func verifyPayload(data []byte, signature string) error {
	signingMu.RLock()
	enabled := signingEnabled
	key := signingKey
	signingMu.RUnlock()

	if !enabled || len(key) == 0 {
		return nil // Signing disabled; accept any payload
	}

	if signature == "" {
		return fmt.Errorf("queue: payload signature missing")
	}

	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	expected := hex.EncodeToString(mac.Sum(nil))

	if subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) != 1 {
		return fmt.Errorf("queue: payload signature verification failed")
	}

	return nil
}
