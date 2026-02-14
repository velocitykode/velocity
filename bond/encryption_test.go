package bond

import (
	"os"
	"testing"

	"github.com/velocitykode/velocity/crypto"
)

var testEncryptor crypto.Encryptor

func setupCrypto(t *testing.T) {
	t.Helper()
	// Initialize crypto with a proper 32-byte key (base64 encoded)
	// "01234567890123456789012345678901" is exactly 32 bytes
	key := "base64:MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE="
	os.Setenv("APP_KEY", key)
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    key,
		Cipher: "AES-256-CBC",
	})
	if err != nil {
		t.Fatalf("failed to initialize crypto: %v", err)
	}
	testEncryptor = enc
}

func TestEncryptHistoryState_Disabled(t *testing.T) {
	b := setupBond(t)
	// encryptHistory is false by default

	page := Page{
		Component: "Test",
		Props:     Props{"key": "value"},
		URL:       "/test",
		Version:   "1",
	}

	result, err := b.EncryptHistoryState(page)

	if err != nil {
		t.Fatalf("EncryptHistoryState failed: %v", err)
	}
	if result != "" {
		t.Errorf("expected empty string when encryption disabled, got %s", result)
	}
}

func TestEncryptHistoryState_Enabled(t *testing.T) {
	setupCrypto(t)

	b, _ := New(Config{
		RootTemplate:   validTemplate,
		EncryptHistory: true,
	})
	b.SetEncryptor(testEncryptor)

	page := Page{
		Component: "Test",
		Props:     Props{"key": "value"},
		URL:       "/test",
		Version:   "1",
	}

	encrypted, err := b.EncryptHistoryState(page)

	if err != nil {
		t.Fatalf("EncryptHistoryState failed: %v", err)
	}
	if encrypted == "" {
		t.Error("expected encrypted string, got empty")
	}
	// Encrypted data should be different from original
	if encrypted == `{"component":"Test","props":{"key":"value"},"url":"/test","version":"1"}` {
		t.Error("encrypted data should not match original JSON")
	}
}

func TestDecryptHistoryState_RoundTrip(t *testing.T) {
	setupCrypto(t)

	b, _ := New(Config{
		RootTemplate:   validTemplate,
		EncryptHistory: true,
	})
	b.SetEncryptor(testEncryptor)

	original := Page{
		Component: "Dashboard",
		Props: Props{
			"user":  "Ali",
			"count": float64(42), // JSON numbers are float64
		},
		URL:     "/dashboard",
		Version: "abc123",
	}

	encrypted, err := b.EncryptHistoryState(original)
	if err != nil {
		t.Fatalf("EncryptHistoryState failed: %v", err)
	}

	decrypted, err := b.DecryptHistoryState(encrypted)
	if err != nil {
		t.Fatalf("DecryptHistoryState failed: %v", err)
	}

	if decrypted.Component != original.Component {
		t.Errorf("expected component %s, got %s", original.Component, decrypted.Component)
	}
	if decrypted.URL != original.URL {
		t.Errorf("expected URL %s, got %s", original.URL, decrypted.URL)
	}
	if decrypted.Version != original.Version {
		t.Errorf("expected version %s, got %s", original.Version, decrypted.Version)
	}
	if decrypted.Props["user"] != original.Props["user"] {
		t.Errorf("expected user %v, got %v", original.Props["user"], decrypted.Props["user"])
	}
}

func TestDecryptHistoryState_InvalidData(t *testing.T) {
	setupCrypto(t)

	b := setupBond(t)

	_, err := b.DecryptHistoryState("invalid-encrypted-data")

	if err == nil {
		t.Error("expected error for invalid encrypted data")
	}
}

func TestEncryptHistoryState_WithComplexProps(t *testing.T) {
	setupCrypto(t)

	b, _ := New(Config{
		RootTemplate:   validTemplate,
		EncryptHistory: true,
	})
	b.SetEncryptor(testEncryptor)

	page := Page{
		Component: "Users/Index",
		Props: Props{
			"users": []any{
				map[string]any{"id": float64(1), "name": "Ali"},
				map[string]any{"id": float64(2), "name": "Bob"},
			},
			"meta": map[string]any{
				"total":   float64(100),
				"page":    float64(1),
				"perPage": float64(10),
			},
		},
		URL:     "/users?page=1",
		Version: "v1",
	}

	encrypted, err := b.EncryptHistoryState(page)
	if err != nil {
		t.Fatalf("EncryptHistoryState failed: %v", err)
	}

	decrypted, err := b.DecryptHistoryState(encrypted)
	if err != nil {
		t.Fatalf("DecryptHistoryState failed: %v", err)
	}

	users := decrypted.Props["users"].([]any)
	if len(users) != 2 {
		t.Errorf("expected 2 users, got %d", len(users))
	}

	meta := decrypted.Props["meta"].(map[string]any)
	if meta["total"] != float64(100) {
		t.Errorf("expected total 100, got %v", meta["total"])
	}
}

func TestEncryptHistoryState_UnmarshalableProps(t *testing.T) {
	setupCrypto(t)

	b, _ := New(Config{
		RootTemplate:   validTemplate,
		EncryptHistory: true,
	})
	b.SetEncryptor(testEncryptor)

	page := Page{
		Component: "Test",
		Props: Props{
			"badValue": make(chan int), // Cannot be JSON marshaled
		},
		URL:     "/test",
		Version: "1",
	}

	_, err := b.EncryptHistoryState(page)

	if err == nil {
		t.Error("expected error for unmarshalable props")
	}
}

func TestDecryptHistoryState_InvalidJSON(t *testing.T) {
	setupCrypto(t)

	b := setupBond(t)

	// Encrypt some invalid JSON (not a Page)
	b.SetEncryptor(testEncryptor)
	encrypted, err := testEncryptor.Encrypt("not valid json")
	if err != nil {
		t.Fatalf("testEncryptor.Encrypt failed: %v", err)
	}

	_, err = b.DecryptHistoryState(encrypted)

	if err == nil {
		t.Error("expected error for invalid JSON after decryption")
	}
}

func TestEncryptHistoryState_EmptyPage(t *testing.T) {
	setupCrypto(t)

	b, _ := New(Config{
		RootTemplate:   validTemplate,
		EncryptHistory: true,
	})
	b.SetEncryptor(testEncryptor)

	page := Page{}

	encrypted, err := b.EncryptHistoryState(page)
	if err != nil {
		t.Fatalf("EncryptHistoryState failed: %v", err)
	}

	decrypted, err := b.DecryptHistoryState(encrypted)
	if err != nil {
		t.Fatalf("DecryptHistoryState failed: %v", err)
	}

	if decrypted.Component != "" {
		t.Errorf("expected empty component, got %s", decrypted.Component)
	}
}
