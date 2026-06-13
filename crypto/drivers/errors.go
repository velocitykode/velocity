package drivers

import (
	"errors"

	"github.com/velocitykode/velocity/contract"
)

// Sentinel errors returned by the driver. They live here (not in the
// parent crypto package) so the driver does not need a runtime setter
// or an import cycle to expose them. crypto/crypto.go re-exports these
// under the same identity, so errors.Is(err, crypto.ErrInvalidCipher)
// works against errors returned from this package.
var (
	// ErrInvalidCipher is aliased to contract.ErrInvalidCipher so callers
	// can errors.Is against the shared identity without importing
	// crypto/drivers (same hoisting pattern as ErrInvalidPayload below).
	ErrInvalidCipher = contract.ErrInvalidCipher
	ErrAADMismatch   = errors.New("velocity/crypto: AAD mismatch")
	// ErrLegacyPayloadDisabled is returned when a v0 (pre-domain-separated
	// MAC) payload is presented to a driver that has v0 decoding turned
	// off. Operators disable v0 once their rotation window is complete so
	// the weaker MAC-over-base64 surface is no longer reachable. The
	// sentinel is distinct from ErrDecrypt so cookie / signed-URL
	// pipelines can react with a forced re-encrypt rather than treating
	// it as tamper.
	ErrLegacyPayloadDisabled = errors.New("velocity/crypto: legacy v0 payload decoding disabled")
	// ErrInvalidPayload signals a structural problem with the envelope
	// itself (empty input, non-base64 outer, malformed JSON, wrong wire
	// version). The payload never reached the AEAD/CBC decrypt path.
	//
	// Aliased to contract.ErrInvalidPayload so callers can errors.Is
	// against the shared identity without importing crypto/drivers.
	ErrInvalidPayload = contract.ErrInvalidPayload
	// ErrInvalidPreviousKey signals a rotation key that could not be used:
	// here, one whose raw byte length does not match the cipher. Aliased to
	// contract.ErrInvalidPreviousKey so errors.Is(err, crypto.ErrInvalidPreviousKey)
	// matches errors returned from this package. NewAESDriver fails fast on
	// such keys rather than dropping them; the crypto package performs the
	// same check at the configuration layer.
	ErrInvalidPreviousKey = contract.ErrInvalidPreviousKey
	// ErrDecrypt is the single sentinel returned for any decrypt failure
	// where the inner envelope parsed but the cryptographic check failed
	// or could not be safely performed. CBC paths used to surface six
	// distinct error strings (bad IV b64, bad value b64, wrong MAC, bad
	// padding, etc.) which formed a padding-oracle precursor whenever an
	// operator's error pipeline leaked the message to the client.
	// Callers MUST NOT include the underlying message in a user-visible
	// response; branch on errors.Is(err, ErrDecrypt) and log the real
	// cause server-side. The driver itself emits the variant via stdlib
	// log so operators retain debuggability without an oracle on the
	// wire.
	ErrDecrypt = errors.New("velocity/crypto: decryption failed")
	// ErrDecryptionFailed is retained as an alias for ErrDecrypt so
	// existing callers using errors.Is keep compiling unchanged.
	ErrDecryptionFailed = ErrDecrypt
	// ErrInvalidKeyLength is returned when the supplied raw key length does
	// not match the cipher's required key size (AES-128 = 16 bytes,
	// AES-192 = 24, AES-256 = 32). HKDF is not used to stretch undersized
	// keys; doing so would launder low-entropy input into a full-length
	// derived key with the same entropy ceiling as the original. Operators
	// must supply a key whose raw byte length matches the cipher.
	ErrInvalidKeyLength = errors.New("velocity/crypto: invalid key length for cipher")
)
