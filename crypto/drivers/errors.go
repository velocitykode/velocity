package drivers

import "errors"

// Sentinel errors returned by the driver. They live here (not in the
// parent crypto package) so the driver does not need a runtime setter
// or an import cycle to expose them. crypto/crypto.go re-exports these
// under the same identity, so errors.Is(err, crypto.ErrInvalidCipher)
// works against errors returned from this package.
var (
	ErrInvalidCipher    = errors.New("velocity/crypto: unsupported cipher")
	ErrAADMismatch      = errors.New("velocity/crypto: AAD mismatch")
	ErrInvalidPayload   = errors.New("velocity/crypto: invalid payload format")
	ErrDecryptionFailed = errors.New("velocity/crypto: decryption failed")
	// ErrInvalidKeyLength is returned when the supplied raw key length does
	// not match the cipher's required key size (AES-128 = 16 bytes,
	// AES-192 = 24, AES-256 = 32). HKDF is not used to stretch undersized
	// keys; doing so would launder low-entropy input into a full-length
	// derived key with the same entropy ceiling as the original. Operators
	// must supply a key whose raw byte length matches the cipher.
	ErrInvalidKeyLength = errors.New("velocity/crypto: invalid key length for cipher")
)
