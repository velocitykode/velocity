package crypto_test

import (
	"crypto/rand"
	"encoding/base64"
	"testing"

	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/crypto/cryptotest"
)

func newKey(t *testing.T, size int) string {
	t.Helper()
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("rand: %v", err)
	}
	return "base64:" + base64.StdEncoding.EncodeToString(b)
}

// TestAES256GCM_Contract runs the cryptotest spec against AES-256-GCM, the
// production default. Covers AEAD, AAD binding, key rotation.
func TestAES256GCM_Contract(t *testing.T) {
	cryptotest.RunEncryptorContractTests(t, func(t *testing.T) crypto.Encryptor {
		e, err := crypto.NewEncryptor(crypto.Config{
			Key:    newKey(t, 32),
			Cipher: "AES-256-GCM",
		})
		if err != nil {
			t.Fatalf("NewEncryptor: %v", err)
		}
		return e
	})
}

// TestAES256CBC_Contract runs the cryptotest spec against AES-256-CBC.
// CBC binds AAD via the HMAC framing, so the AAD invariants run here too.
func TestAES256CBC_Contract(t *testing.T) {
	cryptotest.RunEncryptorContractTests(t, func(t *testing.T) crypto.Encryptor {
		e, err := crypto.NewEncryptor(crypto.Config{
			Key:    newKey(t, 32),
			Cipher: "AES-256-CBC",
		})
		if err != nil {
			t.Fatalf("NewEncryptor: %v", err)
		}
		return e
	})
}

// TestAES128GCM_Contract runs the spec against AES-128-GCM (shorter key).
func TestAES128GCM_Contract(t *testing.T) {
	cryptotest.RunEncryptorContractTests(t, func(t *testing.T) crypto.Encryptor {
		e, err := crypto.NewEncryptor(crypto.Config{
			Key:    newKey(t, 16),
			Cipher: "AES-128-GCM",
		})
		if err != nil {
			t.Fatalf("NewEncryptor: %v", err)
		}
		return e
	})
}
