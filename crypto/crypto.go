package crypto

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/velocitykode/velocity/crypto/drivers"
)

// Errors
var (
	ErrInvalidKey       = errors.New("velocity/crypto: invalid encryption key")
	ErrInvalidPayload   = errors.New("velocity/crypto: invalid payload format")
	ErrDecryptionFailed = errors.New("velocity/crypto: decryption failed")
	ErrInvalidCipher    = errors.New("velocity/crypto: unsupported cipher")
	ErrNotInitialized   = errors.New("velocity/crypto: encryptor not initialized")
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

// DefaultConfig returns a Config with sensible defaults.
func DefaultConfig() Config {
	return Config{
		Cipher: "AES-256-GCM",
	}
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

// DeserializePayload converts base64 JSON to a payload
func DeserializePayload(encoded string) (*Payload, error) {
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
