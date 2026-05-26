package crypto

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/velocitykode/velocity/crypto/drivers"
)

// Errors. Sentinels owned by crypto/drivers are re-exported here under
// the same identity so callers can use errors.Is against either package.
var (
	ErrInvalidKey     = errors.New("velocity/crypto: invalid encryption key")
	ErrNotInitialized = errors.New("velocity/crypto: encryptor not initialized")
	ErrInvalidCipher  = drivers.ErrInvalidCipher
	ErrInvalidPayload = drivers.ErrInvalidPayload
	// ErrInvalidPreviousKey indicates that an entry in
	// Config.PreviousKeys could not be parsed (malformed base64,
	// wrong-length decoded bytes, etc.). The constructor fails fast so a
	// typo in APP_PREVIOUS_KEY does not silently disable key rotation.
	// Operators who need the legacy "skip and continue" behaviour (e.g.
	// transient migrations) can set CRYPTO_IGNORE_INVALID_PREVIOUS_KEYS=true.
	ErrInvalidPreviousKey = errors.New("velocity/crypto: invalid previous key")
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

// Encryptor interface defines encryption operations
type Encryptor interface {
	// Encrypt encrypts plaintext and returns a base64 encoded payload
	Encrypt(plaintext string) (string, error)

	// EncryptBytes encrypts bytes and returns a base64 encoded payload
	EncryptBytes(plaintext []byte) (string, error)

	// Decrypt decrypts a base64 encoded payload and returns plaintext
	Decrypt(payload string) (string, error)

	// DecryptBytes decrypts a base64 encoded payload and returns bytes
	DecryptBytes(payload string) ([]byte, error)

	// EncryptBytesWithAAD encrypts plaintext and binds aad into the AEAD
	// authentication tag. Decryption with a different aad fails. aad is
	// not stored in the payload; the caller supplies the same aad on
	// DecryptBytesWithAAD. nil and zero-length aad are equivalent.
	// Returns ErrInvalidCipher when the configured cipher is non-AEAD
	// (any CBC mode).
	EncryptBytesWithAAD(plaintext, aad []byte) (string, error)

	// DecryptBytesWithAAD decrypts a payload produced by
	// EncryptBytesWithAAD.
	//
	// Returns ErrAADMismatch on any GCM authentication failure under the
	// supplied aad. GCM is one-shot AEAD: the tag check cannot
	// distinguish wrong key, wrong aad, AAD-vs-no-AAD payload mixing, or
	// ciphertext tamper. All four collapse to ErrAADMismatch by design;
	// operators investigating an unexpected mismatch should check key
	// rotation, ciphertext integrity, and aad construction together.
	//
	// Returns ErrInvalidPayload when the envelope is empty or not
	// produced by EncryptBytesWithAAD (only the v1 wire format is
	// accepted on this path; legacy v0 payloads are rejected up-front).
	//
	// Returns ErrInvalidCipher when the configured cipher is non-AEAD
	// (any CBC mode); the rejection is symmetric with
	// EncryptBytesWithAAD.
	DecryptBytesWithAAD(payload string, aad []byte) ([]byte, error)

	// GenerateKey generates a new encryption key for the cipher
	GenerateKey() (string, error)
}

// Payload represents the encrypted data structure
type Payload struct {
	IV    string `json:"iv"`            // Initialization vector (base64)
	Value string `json:"value"`         // Encrypted value (base64)
	MAC   string `json:"mac,omitempty"` // HMAC for CBC modes (base64)
	Tag   string `json:"tag,omitempty"` // Authentication tag for GCM modes (base64)
}

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
	cipher := strings.ToUpper(c.Cipher)
	var want int
	switch cipher {
	case "AES-128-CBC", "AES-128-GCM":
		want = 16
	case "AES-192-CBC", "AES-192-GCM":
		want = 24
	case "AES-256-CBC", "AES-256-GCM":
		want = 32
	default:
		return ErrInvalidCipher
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
	// runtime. Helper handles the CRYPTO_IGNORE_INVALID_PREVIOUS_KEYS
	// opt-out so the env knob covers both call sites uniformly.
	if _, err := validatePreviousKeys(c.PreviousKeys, cipher, want); err != nil {
		return err
	}
	return nil
}

// validatePreviousKeys parses and length-checks rotation keys against
// the cipher's required raw key length, honoring the
// CRYPTO_IGNORE_INVALID_PREVIOUS_KEYS=true opt-out for transient
// migrations. Returns the parsed valid keys; callers that only need
// pass/fail (Validate) can discard the slice. Empty PreviousKeys
// entries are silent no-ops since they model empty slots in
// comma-split env values.
func validatePreviousKeys(prev []string, cipher string, keySize int) ([][]byte, error) {
	ignoreInvalid := os.Getenv("CRYPTO_IGNORE_INVALID_PREVIOUS_KEYS") == "true"
	var parsed [][]byte
	for i, k := range prev {
		if k == "" {
			continue
		}
		prevKey, err := parseKey(k)
		if err != nil {
			if ignoreInvalid {
				continue
			}
			return nil, fmt.Errorf("%w: index %d: %v", ErrInvalidPreviousKey, i, err)
		}
		if len(prevKey) != keySize {
			if ignoreInvalid {
				continue
			}
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
	if config.Cipher == "" {
		config.Cipher = "AES-256-GCM"
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return newDriver(config)
}

// newDriver creates the appropriate driver based on cipher
func newDriver(config Config) (Encryptor, error) {
	// Default to AES-256-GCM if no cipher specified
	if config.Cipher == "" {
		config.Cipher = "AES-256-GCM"
	}

	// Parse the key
	key, err := parseKey(config.Key)
	if err != nil {
		return nil, err
	}

	// Parse previous keys for rotation. Default behaviour is fail-fast:
	// any malformed entry rejects the entire constructor so an operator's
	// typo cannot silently disable key rotation (e.g. APP_PREVIOUS_KEY
	// shape "base64:..." with a corrupted suffix would otherwise drop the
	// rotation entry and leave decrypts of pre-rotation ciphertexts
	// failing in production while the env looks healthy).
	//
	// Operators with a transient migration that legitimately needs to
	// tolerate parse failures (e.g. removing a retired key from the list
	// before redeploying) can set CRYPTO_IGNORE_INVALID_PREVIOUS_KEYS=true.
	// The opt-out is intentionally an env var, not a Config field, so it
	// is reviewable in deployment manifests.
	cipher := strings.ToUpper(config.Cipher)

	// Validate cipher and determine required key size. Resolved early so
	// the previous-keys loop below can length-check each entry against
	// the cipher; otherwise a wrong-length entry that decoded cleanly
	// would silently slip past NewEncryptor and only get filtered out
	// inside NewAESDriver, defeating M-13's fail-fast contract.
	keySize, err := cipherKeySize(cipher)
	if err != nil {
		return nil, err
	}

	previousKeys, err := validatePreviousKeys(config.PreviousKeys, cipher, keySize)
	if err != nil {
		return nil, err
	}

	return drivers.NewAESDriver(key, previousKeys, cipher)
}

// cipherKeySize returns the required raw key length for a supported
// cipher identifier. Returns ErrInvalidCipher for unknown ciphers. Used
// by NewEncryptor to length-check PreviousKeys at the configuration
// layer, matching the strict per-key length check NewAESDriver already
// performs on the primary key.
func cipherKeySize(cipher string) (int, error) {
	switch cipher {
	case "AES-128-CBC", "AES-128-GCM":
		return 16, nil
	case "AES-192-CBC", "AES-192-GCM":
		return 24, nil
	case "AES-256-CBC", "AES-256-GCM":
		return 32, nil
	default:
		return 0, ErrInvalidCipher
	}
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

// SerializePayload converts a payload to base64 JSON
func SerializePayload(p *Payload) (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(data), nil
}

// DeserializePayload converts base64 JSON to a payload. Accepts both v1
// ("v1:"-prefixed) and v0 (bare base64) envelopes so tooling that inspects
// stored ciphertexts does not need to know the wire version.
func DeserializePayload(encoded string) (*Payload, error) {
	// Strip the v1 sentinel if present; legacy v0 payloads are bare base64.
	encoded = strings.TrimPrefix(encoded, "v1:")

	// Try URL encoding first, then standard encoding
	data, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		data, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, ErrInvalidPayload
		}
	}

	var p Payload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, ErrInvalidPayload
	}

	return &p, nil
}
