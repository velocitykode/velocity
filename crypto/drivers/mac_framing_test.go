package drivers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"testing"
)

// TestComputeMAC_DomainSeparationPrefix asserts that the MAC input starts with
// the framework's domain-separation prefix and that changing IV or ciphertext
// alone changes the MAC. This guards against payload concatenation ambiguity
// that the previous fmt.Sprintf("base64:%s.%s", ...) wiring allowed.
func TestComputeMAC_DomainSeparationPrefix(t *testing.T) {
	hmacKey := []byte("0123456789abcdef0123456789abcdef")
	iv := []byte("0123456789abcdef") // 16 bytes
	ct := []byte("ciphertext-bytes")

	got := computeMACWith(iv, ct, hmacKey)

	// Recompute expected MAC explicitly with prefix.
	expected := hmac.New(sha256.New, hmacKey)
	expected.Write([]byte("velocity\x00"))
	expected.Write(iv)
	expected.Write(ct)
	wantB64 := base64.StdEncoding.EncodeToString(expected.Sum(nil))

	if got != wantB64 {
		t.Fatalf("MAC with domain separation = %s, want %s", got, wantB64)
	}

	// And explicitly confirm that the legacy fmt-formatted MAC format is now
	// DIFFERENT from the new one, i.e. we have actually moved off the old
	// format.
	legacy := hmac.New(sha256.New, hmacKey)
	legacy.Write([]byte("base64:" + base64.StdEncoding.EncodeToString(ct) + "." + base64.StdEncoding.EncodeToString(iv)))
	legacyB64 := base64.StdEncoding.EncodeToString(legacy.Sum(nil))
	if got == legacyB64 {
		t.Fatal("new MAC format must differ from legacy fmt-based MAC")
	}
}

// TestComputeMAC_BindsIVAndCiphertext verifies that changing either the IV
// or the ciphertext while holding the other fixed changes the MAC. The AES
// wire format fixes the IV to 16 bytes so the iv/ct boundary is implicit;
// what matters is that both inputs flow into the HMAC.
func TestComputeMAC_BindsIVAndCiphertext(t *testing.T) {
	hmacKey := []byte("0123456789abcdef0123456789abcdef")
	iv := []byte("AAAAAAAAAAAAAAAA") // 16 bytes
	ct := []byte("BBBBBBBBBB")

	base := computeMACWith(iv, ct, hmacKey)

	// Flip one bit of IV.
	ivMod := append([]byte{}, iv...)
	ivMod[0] ^= 0x01
	if computeMACWith(ivMod, ct, hmacKey) == base {
		t.Fatal("MAC must change when iv changes")
	}

	// Flip one bit of ciphertext.
	ctMod := append([]byte{}, ct...)
	ctMod[0] ^= 0x01
	if computeMACWith(iv, ctMod, hmacKey) == base {
		t.Fatal("MAC must change when ciphertext changes")
	}
}

// TestEncryptDecryptRoundTrip_CBC exercises the new MAC framing end-to-end
// for CBC mode (GCM is covered by the existing encrypt/decrypt tests).
func TestEncryptDecryptRoundTrip_CBC(t *testing.T) {
	driver, err := NewAESDriver(make([]byte, 32), nil, "AES-256-CBC")
	if err != nil {
		t.Fatalf("NewAESDriver: %v", err)
	}
	plaintext := "hello world — framework-domain mac"
	payload, err := driver.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	out, err := driver.Decrypt(payload)
	if err != nil {
		t.Fatalf("Decrypt: %v", err)
	}
	if out != plaintext {
		t.Fatalf("round-trip mismatch: got %q, want %q", out, plaintext)
	}
}
