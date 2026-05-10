package crypto

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/velocitykode/velocity/crypto/drivers"
)

// Errors. Sentinels owned by crypto/drivers are re-exported here under
// the same identity so callers can use errors.Is against either package.
var (
	ErrInvalidKey       = errors.New("velocity/crypto: invalid encryption key")
	ErrNotInitialized   = errors.New("velocity/crypto: encryptor not initialized")
	ErrInvalidCipher    = drivers.ErrInvalidCipher
	ErrInvalidPayload   = drivers.ErrInvalidPayload
	ErrDecryptionFailed = drivers.ErrDecryptionFailed
	ErrAADMismatch      = drivers.ErrAADMismatch
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

// Validate checks that the Config is usable. Allowed ciphers are the AES
// variants with 128/192/256-bit keys, in CBC or GCM mode.
//
// Returns ErrInvalidKey for empty keys and ErrInvalidCipher for unsupported
// cipher names so callers can still use errors.Is for branching.
func (c Config) Validate() error {
	if c.Key == "" {
		return ErrInvalidKey
	}
	cipher := strings.ToUpper(c.Cipher)
	switch cipher {
	case "AES-128-CBC", "AES-192-CBC", "AES-256-CBC",
		"AES-128-GCM", "AES-192-GCM", "AES-256-GCM":
	default:
		return ErrInvalidCipher
	}
	return nil
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

	// Parse previous keys for rotation
	var previousKeys [][]byte
	for _, k := range config.PreviousKeys {
		if k == "" {
			continue
		}
		prevKey, err := parseKey(k)
		if err != nil {
			// Skip invalid previous keys
			continue
		}
		previousKeys = append(previousKeys, prevKey)
	}

	// Create the appropriate driver
	cipher := strings.ToUpper(config.Cipher)

	// Validate cipher and determine required key size for derivation
	switch cipher {
	case "AES-128-CBC", "AES-128-GCM":
		// 16-byte key
	case "AES-192-CBC", "AES-192-GCM":
		// 24-byte key
	case "AES-256-CBC", "AES-256-GCM":
		// 32-byte key
	default:
		return nil, ErrInvalidCipher
	}

	return drivers.NewAESDriver(key, previousKeys, cipher)
}

// parseKey parses a key string which may be base64 encoded
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
