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

// TestComputeMACWithAAD_FramingPin pins the exact AAD MAC framing:
// HMAC(hmacKey, "velocity-aad\x00" || be64(len(aad)) || aad || iv || ct).
// Changing this framing invalidates every AAD-bound CBC ciphertext in
// the wild (flash cookies and app data), so it must not drift.
func TestComputeMACWithAAD_FramingPin(t *testing.T) {
	hmacKey := []byte("0123456789abcdef0123456789abcdef")
	iv := []byte("0123456789abcdef") // 16 bytes
	ct := []byte("ciphertext-bytes")
	aad := []byte("_velocity_errors")

	got := computeMACWithAAD(iv, ct, aad, hmacKey)

	expected := hmac.New(sha256.New, hmacKey)
	expected.Write([]byte("velocity-aad\x00"))
	expected.Write([]byte{0, 0, 0, 0, 0, 0, 0, 16}) // be64(len(aad))
	expected.Write(aad)
	expected.Write(iv)
	expected.Write(ct)
	wantB64 := base64.StdEncoding.EncodeToString(expected.Sum(nil))

	if got != wantB64 {
		t.Fatalf("AAD MAC framing = %s, want %s", got, wantB64)
	}

	// Domain separation: the AAD MAC must differ from the plain v1 MAC
	// over the same iv/ct.
	if got == computeMACWith(iv, ct, hmacKey) {
		t.Fatal("AAD MAC must differ from plain v1 MAC")
	}

	// Binding: a different aad changes the MAC.
	if got == computeMACWithAAD(iv, ct, []byte("_velocity_old"), hmacKey) {
		t.Fatal("MAC must change when aad changes")
	}
}

// TestComputeMACWithAAD_EmptyEqualsPlain pins the nil/zero-length aad
// equivalence: an empty aad selects the plain v1 framing, matching GCM
// where an empty AAD produces the same tag as none.
func TestComputeMACWithAAD_EmptyEqualsPlain(t *testing.T) {
	hmacKey := []byte("0123456789abcdef0123456789abcdef")
	iv := []byte("0123456789abcdef")
	ct := []byte("ciphertext-bytes")

	plain := computeMACWith(iv, ct, hmacKey)
	if computeMACWithAAD(iv, ct, nil, hmacKey) != plain {
		t.Fatal("nil aad must produce the plain v1 MAC")
	}
	if computeMACWithAAD(iv, ct, []byte{}, hmacKey) != plain {
		t.Fatal("zero-length aad must produce the plain v1 MAC")
	}
}

// TestComputeMACWithAAD_LengthPrefixPreventsBoundaryShift asserts the
// explicit aad length prefix does its job: moving a byte from the front
// of the iv to the end of the aad (which keeps the raw concatenation
// aad||iv||ct identical) must change the MAC.
func TestComputeMACWithAAD_LengthPrefixPreventsBoundaryShift(t *testing.T) {
	hmacKey := []byte("0123456789abcdef0123456789abcdef")
	iv := []byte("0123456789abcdef")
	ct := []byte("ciphertext-bytes")

	// aad="x", iv unchanged vs aad="x"+iv[0], iv shifted left one byte:
	// the concatenation aad||iv is byte-identical in both, so only the
	// length prefix separates them.
	a := computeMACWithAAD(iv, ct, []byte("x"), hmacKey)
	c := computeMACWithAAD(iv[1:], ct, append([]byte("x"), iv[0]), hmacKey)
	if a == c {
		t.Fatal("length prefix must prevent aad/iv boundary shifts from colliding")
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
