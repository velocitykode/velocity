package queue

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"os"
	"sync"
)

var (
	signingMu      sync.RWMutex
	signingKey     []byte
	signingEnabled bool
)

func init() {
	configureQueueSigning()
}

// configureQueueSigning reads signing config from environment
func configureQueueSigning() {
	key := os.Getenv("QUEUE_SIGNING_KEY")
	if key == "" {
		// Fall back to APP_KEY
		key = os.Getenv("APP_KEY")
	}

	disabled := os.Getenv("QUEUE_SIGNING_DISABLED")

	signingMu.Lock()
	defer signingMu.Unlock()

	if key != "" && disabled != "true" {
		signingKey = []byte(key)
		signingEnabled = true
	}
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
