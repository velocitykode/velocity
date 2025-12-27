package crypto

import (
	"encoding/base64"
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

			// Verify it's base64 encoded
			if _, err := base64.URLEncoding.DecodeString(encrypted); err != nil {
				t.Errorf("Encrypted payload is not base64 URL encoded")
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

func TestGlobalEncryptor(t *testing.T) {
	// Initialize global encryptor
	config := Config{
		Key:    "12345678901234567890123456789012",
		Cipher: "AES-256-CBC",
	}

	if err := Init(config); err != nil {
		t.Fatalf("Failed to initialize global encryptor: %v", err)
	}

	// Test global functions
	plaintext := "Test global encryptor"
	encrypted, err := Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Failed to encrypt with global: %v", err)
	}

	decrypted, err := Decrypt(encrypted)
	if err != nil {
		t.Fatalf("Failed to decrypt with global: %v", err)
	}

	if decrypted != plaintext {
		t.Errorf("Global decrypt mismatch: got %q, want %q", decrypted, plaintext)
	}

	// Test global byte functions
	plainBytes := []byte("Test bytes")
	encryptedBytes, err := EncryptBytes(plainBytes)
	if err != nil {
		t.Fatalf("Failed to encrypt bytes with global: %v", err)
	}

	decryptedBytes, err := DecryptBytes(encryptedBytes)
	if err != nil {
		t.Fatalf("Failed to decrypt bytes with global: %v", err)
	}

	if string(decryptedBytes) != string(plainBytes) {
		t.Errorf("Global decrypt bytes mismatch")
	}

	// Test generate key
	key, err := GenerateKey()
	if err != nil {
		t.Fatalf("Failed to generate key with global: %v", err)
	}

	if !strings.HasPrefix(key, "base64:") {
		t.Errorf("Generated key should have base64: prefix")
	}
}

func TestUninitializedGlobal(t *testing.T) {
	// Reset global encryptor
	globalMux.Lock()
	globalEncryptor = nil
	globalMux.Unlock()

	// Should return error when not initialized
	_, err := Encrypt("test")
	if err != ErrNotInitialized {
		t.Errorf("Expected ErrNotInitialized, got %v", err)
	}

	_, err = Decrypt("test")
	if err != ErrNotInitialized {
		t.Errorf("Expected ErrNotInitialized, got %v", err)
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
		{"Wrong size for AES-128", "AES-128-CBC", "wrongsize"},
		{"Wrong size for AES-256", "AES-256-CBC", "wrongsize"},
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
