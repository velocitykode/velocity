package crypto

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto/drivers"
)

// Errors. Sentinels owned by crypto/drivers are re-exported here under
// the same identity so callers can use errors.Is against either package.
// ErrInvalidKey, ErrInvalidPreviousKey, and ErrInvalidPayload are hoisted
// to the contract package; the aliases below preserve identity so
// existing errors.Is(err, crypto.ErrInvalidKey) calls keep matching.
var (
	ErrInvalidKey     = contract.ErrInvalidKey
	ErrNotInitialized = errors.New("velocity/crypto: encryptor not initialized")
	ErrInvalidCipher  = drivers.ErrInvalidCipher
	ErrInvalidPayload = drivers.ErrInvalidPayload
	// ErrInvalidPreviousKey indicates that an entry in
	// Config.PreviousKeys could not be parsed (malformed base64,
	// wrong-length decoded bytes, etc.). The constructor fails fast so a
	// typo in APP_PREVIOUS_KEY does not silently disable key rotation.
	// Every non-empty malformed or wrong-length previous key is rejected;
	// there is no opt-out.
	ErrInvalidPreviousKey = contract.ErrInvalidPreviousKey
	// ErrDecrypt is the single sentinel for any cryptographic decrypt
	// failure (wrong key, wrong MAC, bad padding, malformed IV bytes).
	// Callers MUST NOT include the error message in user-visible output;
	// branch on errors.Is(err, crypto.ErrDecrypt) and log the real cause
	// server-side. Operators can enable CRYPTO_DEBUG=true to surface the
	// underlying stage via stdlib log.
	ErrDecrypt          = drivers.ErrDecrypt
	ErrDecryptionFailed = drivers.ErrDecryptionFailed
	ErrAADMismatch      = drivers.ErrAADMismatch
	ErrInvalidKeyLength = drivers.ErrInvalidKeyLength
	// ErrLegacyPayloadDisabled is returned when a v0 (pre-domain-separated
	// MAC) payload is decrypted by a driver constructed with
	// CRYPTO_DISABLE_V0=true. Operators flip this flag once their
	// rotation window completes; the sentinel lets cookie / signed-URL
	// pipelines force re-encrypt rather than treat the rejection as
	// tamper.
	ErrLegacyPayloadDisabled = drivers.ErrLegacyPayloadDisabled
)

// Encryptor interface defines encryption operations.
//
// Implementations must pass cryptotest.RunEncryptorContractTests. See
// cryptotest for the executable specification.
type Encryptor interface {
	// Encrypt encrypts plaintext and returns a base64 encoded payload
	Encrypt(plaintext string) (string, error)

	// EncryptBytes encrypts bytes and returns a base64 encoded payload
	EncryptBytes(plaintext []byte) (string, error)

	// Decrypt decrypts a base64 encoded payload and returns plaintext
	Decrypt(payload string) (string, error)

	// DecryptBytes decrypts a base64 encoded payload and returns bytes
	DecryptBytes(payload string) ([]byte, error)

	// EncryptBytesWithAAD encrypts plaintext and binds aad into the
	// authentication check: GCM folds it into the AEAD tag, CBC mixes
	// it into the encrypt-then-MAC HMAC input under a dedicated domain
	// prefix (see crypto/drivers package doc for the exact framing).
	// Decryption with a different aad fails in both modes. aad is not
	// stored in the payload; the caller supplies the same aad on
	// DecryptBytesWithAAD. nil and zero-length aad are equivalent (an
	// empty aad produces the same ciphertext semantics as EncryptBytes).
	EncryptBytesWithAAD(plaintext, aad []byte) (string, error)

	// DecryptBytesWithAAD decrypts a payload produced by
	// EncryptBytesWithAAD.
	//
	// Returns ErrAADMismatch on any authentication failure under the
	// supplied aad. The auth check (GCM tag or CBC HMAC) cannot
	// distinguish wrong key, wrong aad, AAD-vs-no-AAD payload mixing, or
	// ciphertext tamper. All four collapse to ErrAADMismatch by design;
	// operators investigating an unexpected mismatch should check key
	// rotation, ciphertext integrity, and aad construction together.
	//
	// Key rotation via Config.PreviousKeys is supported: the active key
	// is attempted first, then each previous master in turn (with the
	// same aad). Matches the rotation semantics of DecryptBytes.
	//
	// Returns ErrInvalidPayload only for STRUCTURAL envelope defects:
	// empty payload, legacy v0 wire format, or a v1 envelope whose
	// nonce / tag fields are missing or undersized. A non-AAD envelope
	// (produced by EncryptBytes) is NOT structurally distinguishable
	// from an EncryptBytesWithAAD envelope once decoded, so the auth
	// check is the only available probe; supplying a non-AAD payload
	// on this path with a non-empty aad therefore collapses to
	// ErrAADMismatch, not ErrInvalidPayload. Callers that need the
	// distinction must track which path produced each ciphertext at the
	// application layer.
	DecryptBytesWithAAD(payload string, aad []byte) ([]byte, error)

	// GenerateKey generates a new encryption key for the cipher
	GenerateKey() (string, error)
}

// Payload represents the encrypted data structure. It aliases
// drivers.Payload (the canonical definition) so the wire shape has a single
// source; the alias keeps crypto.Payload usable by repo callers without an
// import of crypto/drivers.
type Payload = drivers.Payload

// Config holds encryption configuration
type Config struct {
	Key          string   // Primary encryption key
	PreviousKeys []string // Previous keys for rotation
	Cipher       string   // Cipher algorithm
}

// Validate checks that the Config is structurally usable. Allowed ciphers
// are the AES variants with 128/192/256-bit keys, in CBC or GCM mode.
//
// Returns ErrInvalidKey for empty keys, ErrInvalidCipher for
// unsupported cipher names, and ErrInvalidKeyLength when the supplied
// key (raw or base64-decoded) does not match the cipher's required raw
// byte length. Callers can branch on errors.Is for each case.
//
// Validate is consistent with NewAESDriver: anything Validate accepts
// will also pass driver construction, and anything it rejects will be
// rejected by NewEncryptor too. The split exists so callers that want
// a config-time check without constructing a driver (e.g. startup
// validators) can call Validate on its own.
func (c Config) Validate() error {
	if c.Key == "" {
		return ErrInvalidKey
	}
	cipher := normalizeCipher(c.Cipher)
	want, err := drivers.KeySize(cipher)
	if err != nil {
		return err
	}
	raw, err := parseKey(c.Key)
	if err != nil {
		// Malformed base64 key: surface the underlying decode error
		// (callers checking errors.Is against ErrInvalidKey still
		// trip on the empty case above; base64 parse failures get
		// the verbatim error so operators can see what went wrong).
		return err
	}
	if len(raw) != want {
		return fmt.Errorf("%w: cipher %s requires %d-byte key, got %d", ErrInvalidKeyLength, cipher, want, len(raw))
	}
	// PreviousKeys must satisfy the same parse + length contract as the
	// primary key; otherwise Validate would accept a config that
	// NewEncryptor later rejects, breaking the documented symmetry and
	// letting startup validators sign off on configs that fail at
	// runtime.
	if _, err := validatePreviousKeys(c.PreviousKeys, cipher, want); err != nil {
		return err
	}
	return nil
}

// normalizeCipher applies the shared cipher normalization policy: an
// empty cipher defaults to AES-256-GCM, and the name is upper-cased.
// Validate and newDriver both run config through this before calling
// drivers.KeySize so the two paths share one normalization policy; a
// config that NewEncryptor would run with cannot be rejected by Validate
// (or vice versa) due to a default applied in only one place.
func normalizeCipher(cipher string) string {
	if cipher == "" {
		cipher = "AES-256-GCM"
	}
	return strings.ToUpper(cipher)
}

// validatePreviousKeys parses and length-checks rotation keys against
// the cipher's required raw key length. Every non-empty malformed or
// wrong-length key returns ErrInvalidPreviousKey; there is no opt-out.
// Returns the parsed valid keys; callers that only need pass/fail
// (Validate) can discard the slice. Empty PreviousKeys entries are
// silent no-ops since they model empty slots in comma-split env values.
func validatePreviousKeys(prev []string, cipher string, keySize int) ([][]byte, error) {
	var parsed [][]byte
	for i, k := range prev {
		if k == "" {
			continue
		}
		prevKey, err := parseKey(k)
		if err != nil {
			return nil, fmt.Errorf("%w: index %d: %v", ErrInvalidPreviousKey, i, err)
		}
		if len(prevKey) != keySize {
			return nil, fmt.Errorf("%w: index %d: cipher %s requires %d-byte key, got %d", ErrInvalidPreviousKey, i, cipher, keySize, len(prevKey))
		}
		parsed = append(parsed, prevKey)
	}
	return parsed, nil
}

// NewEncryptor creates a new encryptor with custom configuration.
// The config is validated before constructing the driver; missing keys or
// unsupported ciphers are rejected up-front.
func NewEncryptor(config Config) (Encryptor, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return newDriver(config)
}

// newDriver creates the appropriate driver based on cipher
func newDriver(config Config) (Encryptor, error) {
	// Parse the key
	key, err := parseKey(config.Key)
	if err != nil {
		return nil, err
	}

	// Parse previous keys for rotation. Fail-fast: any malformed entry
	// rejects the entire constructor so an operator's typo cannot silently
	// disable key rotation (e.g. APP_PREVIOUS_KEY shape "base64:..." with a
	// corrupted suffix would otherwise drop the rotation entry and leave
	// decrypts of pre-rotation ciphertexts failing in production while the
	// env looks healthy).
	cipher := normalizeCipher(config.Cipher)

	// Validate cipher and determine required key size. Resolved early so
	// the previous-keys loop below can length-check each entry against
	// the cipher; otherwise a wrong-length entry that decoded cleanly
	// would silently slip past NewEncryptor and only get filtered out
	// inside NewAESDriver, defeating M-13's fail-fast contract.
	keySize, err := drivers.KeySize(cipher)
	if err != nil {
		return nil, err
	}

	previousKeys, err := validatePreviousKeys(config.PreviousKeys, cipher, keySize)
	if err != nil {
		return nil, err
	}

	return drivers.NewAESDriver(key, previousKeys, cipher)
}

// parseKey parses a key string which may be base64 encoded. The returned
// bytes are NOT length-validated here; downstream NewAESDriver enforces
// len(key) == cipher.keySize and surfaces ErrInvalidKeyLength on
// mismatch. Decoding errors on `base64:` keys are returned verbatim so
// operators see the underlying base64 failure (not an opaque length
// rejection masking a malformed key).
func parseKey(keyStr string) ([]byte, error) {
	if keyStr == "" {
		return nil, ErrInvalidKey
	}

	// Check for base64 prefix
	if strings.HasPrefix(keyStr, "base64:") {
		keyData := strings.TrimPrefix(keyStr, "base64:")
		return base64.StdEncoding.DecodeString(keyData)
	}

	// Use raw key
	return []byte(keyStr), nil
}

// SerializePayload converts a payload to base64 JSON. Delegates to
// drivers.SerializePayload (the canonical implementation); retained here so
// existing crypto.SerializePayload callers keep compiling.
func SerializePayload(p *Payload) (string, error) {
	return drivers.SerializePayload(p)
}

// DeserializePayload converts base64 JSON to a payload. Accepts both v1
// ("v1:"-prefixed) and v0 (bare base64) envelopes so tooling that inspects
// stored ciphertexts does not need to know the wire version. Delegates to
// drivers.DeserializePayload (the canonical implementation); retained here
// so existing crypto.DeserializePayload callers keep compiling.
func DeserializePayload(encoded string) (*Payload, error) {
	return drivers.DeserializePayload(encoded)
}
