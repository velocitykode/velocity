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
)
