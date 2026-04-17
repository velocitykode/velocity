package drivers

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"golang.org/x/crypto/hkdf"
)

// AESDriver implements AES encryption with CBC and GCM modes
type AESDriver struct {
	key          []byte   // Primary encryption key (used for GCM which provides its own auth)
	hmacKey      []byte   // Derived HMAC key for CBC mode (separate from encryption key)
	previousKeys [][]byte // Previous keys for rotation
	cipher       string   // Cipher mode (AES-128-CBC, AES-256-CBC, AES-128-GCM, AES-256-GCM)
	keySize      int      // Key size in bytes
}

// NewAESDriver creates a new AES driver
func NewAESDriver(key []byte, previousKeys [][]byte, cipher string) (*AESDriver, error) {
	d := &AESDriver{
		previousKeys: previousKeys,
		cipher:       strings.ToUpper(cipher),
	}

	// Determine required key size
	switch d.cipher {
	case "AES-128-CBC", "AES-128-GCM":
		d.keySize = 16
	case "AES-256-CBC", "AES-256-GCM":
		d.keySize = 32
	default:
		return nil, fmt.Errorf("velocity/crypto: unsupported cipher: %s", cipher)
	}

	// Reject empty or nil keys
	if len(key) == 0 {
		return nil, fmt.Errorf("velocity/crypto: invalid key size: key must not be empty")
	}

	// Derive separate encryption and HMAC subkeys directly from the original key
	// via HKDF with distinct info strings. HKDF handles any input key size, so
	// no intermediate normalization step is needed.
	encKey, err := deriveSubkey(key, d.keySize, []byte("encryption"))
	if err != nil {
		return nil, fmt.Errorf("velocity/crypto: failed to derive encryption key: %w", err)
	}
	hmacKey, err := deriveSubkey(key, 32, []byte("hmac"))
	if err != nil {
		return nil, fmt.Errorf("velocity/crypto: failed to derive hmac key: %w", err)
	}

	d.key = encKey
	d.hmacKey = hmacKey

	return d, nil
}

// hkdfSalt is a static salt for HKDF key derivation.
var hkdfSalt = []byte("velocity-framework-hkdf-salt-v1")

// deriveSubkey derives a subkey from a master key using HKDF-SHA256.
func deriveSubkey(master []byte, size int, info []byte) ([]byte, error) {
	r := hkdf.New(sha256.New, master, hkdfSalt, info)
	out := make([]byte, size)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

// Encrypt encrypts plaintext
func (d *AESDriver) Encrypt(plaintext string) (string, error) {
	return d.EncryptBytes([]byte(plaintext))
}

// EncryptBytes encrypts bytes
func (d *AESDriver) EncryptBytes(plaintext []byte) (string, error) {
	if strings.Contains(d.cipher, "GCM") {
		return d.encryptGCM(plaintext)
	}
	return d.encryptCBC(plaintext)
}

// Decrypt decrypts a payload
func (d *AESDriver) Decrypt(payload string) (string, error) {
	data, err := d.DecryptBytes(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// DecryptBytes decrypts a payload to bytes
func (d *AESDriver) DecryptBytes(payload string) ([]byte, error) {
	// Parse the payload
	p, err := deserializePayload(payload)
	if err != nil {
		return nil, err
	}

	// Try current key first (already-derived enc + hmac subkeys)
	plaintext, err := d.decryptWithKeys(p, d.key, d.hmacKey)
	if err == nil {
		return plaintext, nil
	}

	// Try previous keys for rotation support (derive subkeys directly from each master key)
	for _, masterKey := range d.previousKeys {
		encKey, ekErr := deriveSubkey(masterKey, d.keySize, []byte("encryption"))
		hk, hkErr := deriveSubkey(masterKey, 32, []byte("hmac"))
		if ekErr != nil || hkErr != nil {
			continue
		}
		plaintext, err = d.decryptWithKeys(p, encKey, hk)
		if err == nil {
			return plaintext, nil
		}
	}

	return nil, errors.New("velocity/crypto: decryption failed with all keys")
}

// GenerateKey generates a new encryption key
func (d *AESDriver) GenerateKey() (string, error) {
	key := make([]byte, d.keySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", err
	}
	return "base64:" + base64.StdEncoding.EncodeToString(key), nil
}

// encryptCBC encrypts using CBC mode
func (d *AESDriver) encryptCBC(plaintext []byte) (string, error) {
	// Create cipher block
	block, err := aes.NewCipher(d.key)
	if err != nil {
		return "", err
	}

	// Generate IV
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", err
	}

	// Pad plaintext to block size
	plaintext = pkcs7Pad(plaintext, aes.BlockSize)

	// Encrypt
	mode := cipher.NewCBCEncrypter(block, iv)
	ciphertext := make([]byte, len(plaintext))
	mode.CryptBlocks(ciphertext, plaintext)

	// Generate MAC for integrity
	mac := d.generateMAC(base64.StdEncoding.EncodeToString(ciphertext), base64.StdEncoding.EncodeToString(iv))

	// Create payload
	p := &Payload{
		IV:    base64.StdEncoding.EncodeToString(iv),
		Value: base64.StdEncoding.EncodeToString(ciphertext),
		MAC:   mac,
	}

	return serializePayload(p)
}

// encryptGCM encrypts using GCM mode
func (d *AESDriver) encryptGCM(plaintext []byte) (string, error) {
	// Create cipher block
	block, err := aes.NewCipher(d.key)
	if err != nil {
		return "", err
	}

	// Create GCM
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	// Generate nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	// Encrypt and authenticate
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	// Extract authentication tag (last 16 bytes)
	tagStart := len(ciphertext) - gcm.Overhead()
	tag := ciphertext[tagStart:]
	ciphertext = ciphertext[:tagStart]

	// Create payload
	p := &Payload{
		IV:    base64.StdEncoding.EncodeToString(nonce),
		Value: base64.StdEncoding.EncodeToString(ciphertext),
		Tag:   base64.StdEncoding.EncodeToString(tag),
	}

	return serializePayload(p)
}

// decryptWithKeys attempts to decrypt with specific encryption and HMAC keys.
func (d *AESDriver) decryptWithKeys(p *Payload, encKey, hmacKey []byte) ([]byte, error) {
	if strings.Contains(d.cipher, "GCM") {
		return d.decryptGCMWithKey(p, encKey)
	}
	return d.decryptCBCWithKey(p, encKey, hmacKey)
}

// decryptCBCWithKey decrypts CBC mode with separate encryption and HMAC keys
func (d *AESDriver) decryptCBCWithKey(p *Payload, encKey, hmacKey []byte) ([]byte, error) {
	// MAC is required for CBC decryption to ensure integrity
	if p.MAC == "" {
		return nil, errors.New("velocity/crypto: mac required for CBC decryption")
	}

	// Verify MAC using the dedicated HMAC key
	expectedMAC := generateMACWith(p.Value, p.IV, hmacKey)
	if !secureCompare(p.MAC, expectedMAC) {
		return nil, errors.New("velocity/crypto: mac verification failed")
	}

	// Decode components
	iv, err := base64.StdEncoding.DecodeString(p.IV)
	if err != nil {
		return nil, err
	}

	ciphertext, err := base64.StdEncoding.DecodeString(p.Value)
	if err != nil {
		return nil, err
	}

	// Create cipher block
	block, err := aes.NewCipher(encKey)
	if err != nil {
		return nil, err
	}

	// Decrypt
	mode := cipher.NewCBCDecrypter(block, iv)
	plaintext := make([]byte, len(ciphertext))
	mode.CryptBlocks(plaintext, ciphertext)

	// Remove padding
	plaintext, err = pkcs7Unpad(plaintext)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}

// decryptGCMWithKey decrypts GCM mode with a specific key
func (d *AESDriver) decryptGCMWithKey(p *Payload, key []byte) ([]byte, error) {
	// Decode components
	nonce, err := base64.StdEncoding.DecodeString(p.IV)
	if err != nil {
		return nil, err
	}

	ciphertext, err := base64.StdEncoding.DecodeString(p.Value)
	if err != nil {
		return nil, err
	}

	tag, err := base64.StdEncoding.DecodeString(p.Tag)
	if err != nil {
		return nil, err
	}

	// Append tag to ciphertext for GCM
	ciphertext = append(ciphertext, tag...)

	// Create cipher block
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}

	// Create GCM
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	// Decrypt and verify
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return nil, err
	}

	return plaintext, nil
}

// generateMAC generates HMAC for CBC mode using the derived HMAC key
func (d *AESDriver) generateMAC(value, iv string) string {
	return generateMACWith(value, iv, d.hmacKey)
}

// generateMACWith generates HMAC with the provided HMAC key
func generateMACWith(value, iv string, hmacKey []byte) string {
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write([]byte(fmt.Sprintf("base64:%s.%s", value, iv)))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// pkcs7Pad adds PKCS#7 padding
func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padtext := make([]byte, padding)
	for i := range padtext {
		padtext[i] = byte(padding)
	}
	return append(data, padtext...)
}

// pkcs7Unpad removes PKCS#7 padding
func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("velocity/crypto: invalid padding")
	}
	padding := int(data[len(data)-1])
	if padding == 0 || padding > aes.BlockSize || padding > len(data) {
		return nil, errors.New("velocity/crypto: invalid padding")
	}
	// Verify all padding bytes match using constant-time comparison
	expected := make([]byte, padding)
	for i := range expected {
		expected[i] = byte(padding)
	}
	if subtle.ConstantTimeCompare(data[len(data)-padding:], expected) != 1 {
		return nil, errors.New("velocity/crypto: invalid padding")
	}
	return data[:len(data)-padding], nil
}

// secureCompare performs constant-time string comparison
func secureCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Payload represents the encrypted data structure
type Payload struct {
	IV    string `json:"iv"`
	Value string `json:"value"`
	MAC   string `json:"mac,omitempty"`
	Tag   string `json:"tag,omitempty"`
}

// serializePayload converts a payload to base64 JSON
func serializePayload(p *Payload) (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(data), nil
}

// deserializePayload converts base64 JSON to a payload
func deserializePayload(encoded string) (*Payload, error) {
	// Try URL encoding first, then standard encoding
	data, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		data, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, errors.New("velocity/crypto: invalid payload format")
		}
	}

	var p Payload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, errors.New("velocity/crypto: invalid payload format")
	}

	return &p, nil
}
