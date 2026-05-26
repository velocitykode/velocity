package crypto

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestEncryptDecrypt(t *testing.T) {
	tests := []struct {
		name   string
		cipher string
		key    string
	}{
		{"AES-128-CBC", "AES-128-CBC", "1234567890123456"},
		{"AES-256-CBC", "AES-256-CBC", "12345678901234567890123456789012"},
		{"AES-128-GCM", "AES-128-GCM", "1234567890123456"},
		{"AES-256-GCM", "AES-256-GCM", "12345678901234567890123456789012"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Initialize encryptor
			config := Config{
				Key:    tt.key,
				Cipher: tt.cipher,
			}

			encryptor, err := NewEncryptor(config)
			if err != nil {
				t.Fatalf("Failed to create encryptor: %v", err)
			}

			// Test string encryption
			plaintext := "Hello, World! This is a test message."
			encrypted, err := encryptor.Encrypt(plaintext)
			if err != nil {
				t.Fatalf("Failed to encrypt: %v", err)
			}

			// Verify wire format is v1-prefixed + base64 URL encoded.
			if !strings.HasPrefix(encrypted, "v1:") {
				t.Errorf("encrypted payload missing v1 sentinel: %q", encrypted)
			}
			if _, err := base64.URLEncoding.DecodeString(strings.TrimPrefix(encrypted, "v1:")); err != nil {
				t.Errorf("Encrypted payload envelope is not base64 URL encoded")
			}

			// Decrypt
			decrypted, err := encryptor.Decrypt(encrypted)
			if err != nil {
				t.Fatalf("Failed to decrypt: %v", err)
			}

			if decrypted != plaintext {
				t.Errorf("Decrypted text doesn't match: got %q, want %q", decrypted, plaintext)
			}

			// Test byte encryption
			plainBytes := []byte("Binary data test: \x00\x01\x02\x03")
			encryptedBytes, err := encryptor.EncryptBytes(plainBytes)
			if err != nil {
				t.Fatalf("Failed to encrypt bytes: %v", err)
			}

			decryptedBytes, err := encryptor.DecryptBytes(encryptedBytes)
			if err != nil {
				t.Fatalf("Failed to decrypt bytes: %v", err)
			}

			if string(decryptedBytes) != string(plainBytes) {
				t.Errorf("Decrypted bytes don't match")
			}
		})
	}
}

func TestKeyRotation(t *testing.T) {
	// Create encryptor with old key
	oldKey := "oldkey1234567890oldkey1234567890"
	config := Config{
		Key:    oldKey,
		Cipher: "AES-256-CBC",
	}

	oldEncryptor, err := NewEncryptor(config)
	if err != nil {
		t.Fatalf("Failed to create old encryptor: %v", err)
	}

	// Encrypt with old key
	plaintext := "Secret data"
	encrypted, err := oldEncryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Failed to encrypt with old key: %v", err)
	}

	// Create new encryptor with key rotation
	newKey := "newkey1234567890newkey1234567890"
	config = Config{
		Key:          newKey,
		PreviousKeys: []string{oldKey},
		Cipher:       "AES-256-CBC",
	}

	newEncryptor, err := NewEncryptor(config)
	if err != nil {
		t.Fatalf("Failed to create new encryptor: %v", err)
	}

	// Should be able to decrypt old data
	decrypted, err := newEncryptor.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Failed to decrypt with rotated keys: %v", err)
	}

	if decrypted != plaintext {
		t.Errorf("Decrypted text doesn't match: got %q, want %q", decrypted, plaintext)
	}

	// New encryption should use new key
	newEncrypted, err := newEncryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Failed to encrypt with new key: %v", err)
	}

	// Old encryptor should NOT be able to decrypt new data
	_, err = oldEncryptor.Decrypt(newEncrypted)
	if err == nil {
		t.Errorf("Old encryptor should not decrypt new data")
	}
}

func TestBase64Key(t *testing.T) {
	// Generate a base64 encoded key
	key := base64.StdEncoding.EncodeToString([]byte("12345678901234567890123456789012"))
	config := Config{
		Key:    "base64:" + key,
		Cipher: "AES-256-CBC",
	}

	encryptor, err := NewEncryptor(config)
	if err != nil {
		t.Fatalf("Failed to create encryptor with base64 key: %v", err)
	}

	plaintext := "Test with base64 key"
	encrypted, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Failed to encrypt: %v", err)
	}

	decrypted, err := encryptor.Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Failed to decrypt: %v", err)
	}

	if decrypted != plaintext {
		t.Errorf("Decrypted text doesn't match: got %q, want %q", decrypted, plaintext)
	}
}

func TestGenerateKey(t *testing.T) {
	ciphers := []struct {
		cipher  string
		keySize int
	}{
		{"AES-128-CBC", 16},
		{"AES-256-CBC", 32},
		{"AES-128-GCM", 16},
		{"AES-256-GCM", 32},
	}

	for _, tc := range ciphers {
		t.Run(tc.cipher, func(t *testing.T) {
			// Create a temporary encryptor
			tempKey := strings.Repeat("x", tc.keySize)
			config := Config{
				Key:    tempKey,
				Cipher: tc.cipher,
			}

			encryptor, err := NewEncryptor(config)
			if err != nil {
				t.Fatalf("Failed to create encryptor: %v", err)
			}

			// Generate a new key
			generatedKey, err := encryptor.GenerateKey()
			if err != nil {
				t.Fatalf("Failed to generate key: %v", err)
			}

			// Verify it starts with base64:
			if !strings.HasPrefix(generatedKey, "base64:") {
				t.Errorf("Generated key should start with 'base64:' prefix")
			}

			// Verify the key can be used
			config.Key = generatedKey
			newEncryptor, err := NewEncryptor(config)
			if err != nil {
				t.Fatalf("Failed to create encryptor with generated key: %v", err)
			}

			// Test encryption/decryption with generated key
			plaintext := "Test with generated key"
			encrypted, err := newEncryptor.Encrypt(plaintext)
			if err != nil {
				t.Fatalf("Failed to encrypt with generated key: %v", err)
			}

			decrypted, err := newEncryptor.Decrypt(encrypted)
			if err != nil {
				t.Fatalf("Failed to decrypt with generated key: %v", err)
			}

			if decrypted != plaintext {
				t.Errorf("Decrypted text doesn't match: got %q, want %q", decrypted, plaintext)
			}
		})
	}
}

func TestPayloadFormat(t *testing.T) {
	config := Config{
		Key:    "12345678901234567890123456789012",
		Cipher: "AES-256-CBC",
	}

	encryptor, err := NewEncryptor(config)
	if err != nil {
		t.Fatalf("Failed to create encryptor: %v", err)
	}

	plaintext := "Test payload format"
	encrypted, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Failed to encrypt: %v", err)
	}

	// Decrypt the base64 payload to check structure
	payload, err := DeserializePayload(encrypted)
	if err != nil {
		t.Fatalf("Failed to deserialize payload: %v", err)
	}

	// Check CBC payload has MAC
	if payload.MAC == "" {
		t.Errorf("CBC payload should have MAC")
	}

	// Check IV is present
	if payload.IV == "" {
		t.Errorf("Payload should have IV")
	}

	// Check value is present
	if payload.Value == "" {
		t.Errorf("Payload should have encrypted value")
	}

	// Check GCM payload has Tag instead of MAC
	config.Cipher = "AES-256-GCM"
	gcmEncryptor, err := NewEncryptor(config)
	if err != nil {
		t.Fatalf("Failed to create GCM encryptor: %v", err)
	}

	gcmEncrypted, err := gcmEncryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Failed to encrypt with GCM: %v", err)
	}

	gcmPayload, err := DeserializePayload(gcmEncrypted)
	if err != nil {
		t.Fatalf("Failed to deserialize GCM payload: %v", err)
	}

	// Check GCM payload has Tag
	if gcmPayload.Tag == "" {
		t.Errorf("GCM payload should have Tag")
	}

	// Check GCM payload doesn't have MAC
	if gcmPayload.MAC != "" {
		t.Errorf("GCM payload should not have MAC")
	}
}

func TestInvalidDecryption(t *testing.T) {
	config := Config{
		Key:    "12345678901234567890123456789012",
		Cipher: "AES-256-CBC",
	}

	encryptor, err := NewEncryptor(config)
	if err != nil {
		t.Fatalf("Failed to create encryptor: %v", err)
	}

	// Test with invalid base64
	_, err = encryptor.Decrypt("not-valid-base64!")
	if err == nil {
		t.Errorf("Should fail with invalid base64")
	}

	// Test with tampered payload
	plaintext := "Original message"
	encrypted, err := encryptor.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Failed to encrypt: %v", err)
	}

	// Tamper with the encrypted data
	tampered := encrypted[:len(encrypted)-4] + "XXXX"
	_, err = encryptor.Decrypt(tampered)
	if err == nil {
		t.Errorf("Should fail with tampered payload")
	}
}

func TestInvalidCipher(t *testing.T) {
	config := Config{
		Key:    "12345678901234567890123456789012",
		Cipher: "INVALID-CIPHER",
	}

	_, err := NewEncryptor(config)
	if err != ErrInvalidCipher {
		t.Errorf("Expected ErrInvalidCipher, got %v", err)
	}
}

func TestInvalidKey(t *testing.T) {
	tests := []struct {
		name   string
		cipher string
		key    string
	}{
		{"Empty key", "AES-256-CBC", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := Config{
				Key:    tt.key,
				Cipher: tt.cipher,
			}

			_, err := NewEncryptor(config)
			if err == nil {
				t.Errorf("Expected error for invalid key")
			}
		})
	}
}

func TestKeyDerivation_RejectsWrongLength(t *testing.T) {
	// Keys whose raw byte length does not match the cipher's required size
	// MUST be rejected. Earlier behaviour silently HKDF-stretched short keys,
	// laundering low-entropy input into a full-length derived key; that path
	// is gone. Operators must supply a key with the correct length.
	tests := []struct {
		name   string
		cipher string
		key    string
	}{
		{"Short key for AES-128", "AES-128-CBC", "shortkey"},
		{"Short key for AES-256", "AES-256-CBC", "shortkey"},
		{"Long key for AES-128", "AES-128-GCM", "this-is-a-much-longer-key-than-required"},
		{"Long key for AES-256", "AES-256-GCM", "this-is-a-much-longer-key-than-required"},
		{"One byte short of AES-128", "AES-128-CBC", strings.Repeat("a", 15)},
		{"One byte over AES-128", "AES-128-CBC", strings.Repeat("a", 17)},
		{"One byte short of AES-192", "AES-192-CBC", strings.Repeat("a", 23)},
		{"One byte short of AES-256", "AES-256-CBC", strings.Repeat("a", 31)},
		{"One byte over AES-256", "AES-256-CBC", strings.Repeat("a", 33)},
		{"24-byte key under AES-256", "AES-256-CBC", strings.Repeat("a", 24)},
		{"16-byte key under AES-256", "AES-256-CBC", strings.Repeat("a", 16)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewEncryptor(Config{Key: tt.key, Cipher: tt.cipher})
			if err == nil {
				t.Fatalf("expected ErrInvalidKeyLength rejection for %q, got nil", tt.key)
			}
			if !errors.Is(err, ErrInvalidKeyLength) {
				t.Fatalf("expected ErrInvalidKeyLength, got %v", err)
			}
		})
	}
}

func TestKeyDerivation_AcceptsExactLength(t *testing.T) {
	// Round trip each AES variant with a raw key of exactly the cipher's
	// required byte length. These ARE the only sizes the constructor will
	// admit; anything else is rejected by TestKeyDerivation_RejectsWrongLength.
	tests := []struct {
		name   string
		cipher string
		key    string
	}{
		{"AES-128-CBC with 16 raw bytes", "AES-128-CBC", strings.Repeat("a", 16)},
		{"AES-128-GCM with 16 raw bytes", "AES-128-GCM", strings.Repeat("a", 16)},
		{"AES-192-CBC with 24 raw bytes", "AES-192-CBC", strings.Repeat("a", 24)},
		{"AES-192-GCM with 24 raw bytes", "AES-192-GCM", strings.Repeat("a", 24)},
		{"AES-256-CBC with 32 raw bytes", "AES-256-CBC", strings.Repeat("a", 32)},
		{"AES-256-GCM with 32 raw bytes", "AES-256-GCM", strings.Repeat("a", 32)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			enc, err := NewEncryptor(Config{Key: tt.key, Cipher: tt.cipher})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			ciphertext, err := enc.Encrypt("hello world")
			if err != nil {
				t.Fatalf("Encrypt failed: %v", err)
			}
			plaintext, err := enc.Decrypt(ciphertext)
			if err != nil {
				t.Fatalf("Decrypt failed: %v", err)
			}
			if plaintext != "hello world" {
				t.Errorf("Expected 'hello world', got %q", plaintext)
			}
		})
	}
}

func BenchmarkEncryptCBC(b *testing.B) {
	config := Config{
		Key:    "12345678901234567890123456789012",
		Cipher: "AES-256-CBC",
	}

	encryptor, _ := NewEncryptor(config)
	plaintext := strings.Repeat("a", 1024) // 1KB

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = encryptor.Encrypt(plaintext)
	}
}

func BenchmarkDecryptCBC(b *testing.B) {
	config := Config{
		Key:    "12345678901234567890123456789012",
		Cipher: "AES-256-CBC",
	}

	encryptor, _ := NewEncryptor(config)
	plaintext := strings.Repeat("a", 1024) // 1KB
	encrypted, _ := encryptor.Encrypt(plaintext)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = encryptor.Decrypt(encrypted)
	}
}

func BenchmarkEncryptGCM(b *testing.B) {
	config := Config{
		Key:    "12345678901234567890123456789012",
		Cipher: "AES-256-GCM",
	}

	encryptor, _ := NewEncryptor(config)
	plaintext := strings.Repeat("a", 1024) // 1KB

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = encryptor.Encrypt(plaintext)
	}
}

func BenchmarkDecryptGCM(b *testing.B) {
	config := Config{
		Key:    "12345678901234567890123456789012",
		Cipher: "AES-256-GCM",
	}

	encryptor, _ := NewEncryptor(config)
	plaintext := strings.Repeat("a", 1024) // 1KB
	encrypted, _ := encryptor.Encrypt(plaintext)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = encryptor.Decrypt(encrypted)
	}
}

func TestEncryptorAAD_MismatchSentinel(t *testing.T) {
	enc, err := NewEncryptor(Config{Key: "12345678901234567890123456789012", Cipher: "AES-256-GCM"})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	env, err := enc.EncryptBytesWithAAD([]byte("secret"), []byte("team:42|cred:7"))
	if err != nil {
		t.Fatalf("EncryptBytesWithAAD: %v", err)
	}
	_, err = enc.DecryptBytesWithAAD(env, []byte("team:42|cred:8"))
	if !errors.Is(err, ErrAADMismatch) {
		t.Fatalf("want ErrAADMismatch, got %v", err)
	}
}

func TestEncryptorAAD_CBCInvalidCipher(t *testing.T) {
	enc, err := NewEncryptor(Config{Key: "12345678901234567890123456789012", Cipher: "AES-256-CBC"})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	if _, err := enc.EncryptBytesWithAAD([]byte("x"), []byte("a")); !errors.Is(err, ErrInvalidCipher) {
		t.Fatalf("EncryptBytesWithAAD: want ErrInvalidCipher, got %v", err)
	}
	if _, err := enc.DecryptBytesWithAAD("v1:abc", []byte("a")); !errors.Is(err, ErrInvalidCipher) {
		t.Fatalf("DecryptBytesWithAAD: want ErrInvalidCipher, got %v", err)
	}
}

func TestEncryptorAAD_CrossPathRejection(t *testing.T) {
	// Payload encrypted with AAD must NOT decrypt via plain DecryptBytes.
	enc, err := NewEncryptor(Config{Key: "12345678901234567890123456789012", Cipher: "AES-256-GCM"})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	env, err := enc.EncryptBytesWithAAD([]byte("payload"), []byte("aad"))
	if err != nil {
		t.Fatalf("EncryptBytesWithAAD: %v", err)
	}
	if _, err := enc.DecryptBytes(env); err == nil {
		t.Fatal("DecryptBytes on AAD-bound payload: want error, got nil")
	}
}

func TestEncryptorAAD_ReverseCrossPathRejection(t *testing.T) {
	// Plain EncryptBytes payload must NOT decrypt via DecryptBytesWithAAD
	// when caller supplies a non-empty aad. The GCM tag was sealed with
	// nil aad; any non-nil aad collapses to ErrAADMismatch by contract.
	enc, err := NewEncryptor(Config{Key: "12345678901234567890123456789012", Cipher: "AES-256-GCM"})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	env, err := enc.EncryptBytes([]byte("payload"))
	if err != nil {
		t.Fatalf("EncryptBytes: %v", err)
	}
	if _, err := enc.DecryptBytesWithAAD(env, []byte("team:42|cred:7")); !errors.Is(err, ErrAADMismatch) {
		t.Fatalf("DecryptBytesWithAAD on plain payload with non-empty aad: want ErrAADMismatch, got %v", err)
	}
}

func TestEncryptorAAD_ZeroAADCrossPathInterop(t *testing.T) {
	// Pin the nil/empty-AAD equivalence across the plain and AAD paths:
	// EncryptBytes seals with nil aad, so DecryptBytesWithAAD with nil or
	// empty aad must round-trip. Symmetric: EncryptBytesWithAAD(nil) seals
	// the same tag, so DecryptBytes (which passes nil aad internally) must
	// also round-trip. Without this pin, a future implementer could change
	// the seal-time aad for plain EncryptBytes (e.g. inject a domain tag)
	// and only the negative cross-path test would still pass.
	enc, err := NewEncryptor(Config{Key: "12345678901234567890123456789012", Cipher: "AES-256-GCM"})
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	plain, err := enc.EncryptBytes([]byte("payload"))
	if err != nil {
		t.Fatalf("EncryptBytes: %v", err)
	}
	for _, aad := range [][]byte{nil, {}} {
		got, err := enc.DecryptBytesWithAAD(plain, aad)
		if err != nil {
			t.Fatalf("DecryptBytesWithAAD(plain, %v): %v", aad, err)
		}
		if string(got) != "payload" {
			t.Fatalf("DecryptBytesWithAAD(plain, %v): got %q want payload", aad, got)
		}
	}
	withAAD, err := enc.EncryptBytesWithAAD([]byte("payload"), nil)
	if err != nil {
		t.Fatalf("EncryptBytesWithAAD(nil): %v", err)
	}
	got, err := enc.DecryptBytes(withAAD)
	if err != nil {
		t.Fatalf("DecryptBytes(withAAD nil): %v", err)
	}
	if string(got) != "payload" {
		t.Fatalf("DecryptBytes(withAAD nil): got %q want payload", got)
	}
}
