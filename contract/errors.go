package contract

import (
	"errors"
	"fmt"
)

// RegistrationError is a typed error for registration-time failures.
// Methods that cannot return error (e.g. Router.Get) panic with this type
// so misuse is loud at bootstrap and debuggable with recover.
type RegistrationError struct {
	Package string
	Message string
}

func (e *RegistrationError) Error() string {
	return fmt.Sprintf("velocity/%s: %s", e.Package, e.Message)
}

// NewRegistrationError creates a new RegistrationError.
func NewRegistrationError(pkg, msg string) *RegistrationError {
	return &RegistrationError{Package: pkg, Message: msg}
}

// Cross-package sentinel errors.
//
// These errors are owned by the contract package so callers can match
// "not found" / "invalid key" outcomes uniformly across driver boundaries
// without importing the concrete driver package. Each owning package
// re-exports the same identity under its conventional name (e.g.
// queue.ErrJobNotFound), so existing errors.Is(err, queue.ErrJobNotFound)
// continues to match.
//
// Scope is intentionally narrow: only sentinels that callers reasonably
// check across package boundaries are hoisted here. Sentinels that are
// purely diagnostic or scoped to a single driver remain local.
//
// Stability is enforced by TestSentinelStability (contract/errors_test.go).
var (
	// ErrJobNotFound is returned when a queue job lookup fails (Find by id,
	// failed_jobs lookup, etc.). Hoisted from queue.ErrJobNotFound.
	ErrJobNotFound = errors.New("velocity/queue: job not found")

	// ErrBatchNotFound is returned by batch repository lookups when the
	// batch id is unknown. Hoisted from queue.ErrBatchNotFound.
	ErrBatchNotFound = errors.New("velocity/queue: batch not found")

	// ErrCacheStoreNotFound is returned by the cache manager when a named
	// store has not been registered. Hoisted from cache.ErrStoreNotFound.
	ErrCacheStoreNotFound = errors.New("velocity/cache: store not found")

	// ErrCacheKeyNotFound is returned by cache typed-lookup helpers when
	// the key is absent. Hoisted from cache.ErrKeyNotFound.
	ErrCacheKeyNotFound = errors.New("velocity/cache: key not found")

	// ErrFileNotFound is returned by storage drivers when a path does not
	// exist. Hoisted from storage.ErrFileNotFound.
	ErrFileNotFound = errors.New("velocity/storage: file not found")

	// ErrDiskNotFound is returned by the storage manager when a named disk
	// has not been configured. Hoisted from storage.ErrDiskNotFound.
	ErrDiskNotFound = errors.New("velocity/storage: disk not found")

	// ErrBroadcastDriverNotFound is returned when a broadcast driver is
	// not registered under the requested name. Hoisted from
	// broadcast.ErrDriverNotFound.
	ErrBroadcastDriverNotFound = errors.New("velocity/broadcast: driver not found")

	// ErrInvalidKey is returned by the crypto subsystem when an encryption
	// key is empty, malformed, or the wrong length for the configured
	// cipher. Hoisted from crypto.ErrInvalidKey.
	ErrInvalidKey = errors.New("velocity/crypto: invalid encryption key")

	// ErrInvalidPreviousKey is returned when an entry in Config.PreviousKeys
	// is malformed (bad base64, wrong length, etc.). Hoisted from
	// crypto.ErrInvalidPreviousKey.
	ErrInvalidPreviousKey = errors.New("velocity/crypto: invalid previous key")

	// ErrInvalidPayload is returned by crypto drivers when the ciphertext
	// envelope is structurally invalid (empty, wrong version, truncated).
	// Distinct from ErrDecrypt: structural defects vs. cryptographic failure.
	// Hoisted from crypto/drivers.ErrInvalidPayload.
	ErrInvalidPayload = errors.New("velocity/crypto: invalid payload format")

	// ErrInvalidCipher is returned when the configured cipher name is
	// unknown (config validation, driver construction). The framework's
	// AES driver binds AAD in both GCM (AEAD tag) and CBC (HMAC framing)
	// modes, so the *WithAAD methods no longer reject CBC with this
	// sentinel; third-party drivers without any way to authenticate AAD
	// may still return it from those methods.
	// Hoisted from crypto/drivers.ErrInvalidCipher.
	ErrInvalidCipher = errors.New("velocity/crypto: unsupported cipher")
)
