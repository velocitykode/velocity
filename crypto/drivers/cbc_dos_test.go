package drivers

import (
	"crypto/aes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"testing"
)

// craftLegacyCBC produces a v0 (no sentinel) CBC envelope with caller-
// chosen iv/ciphertext bytes. The MAC is computed over the base64-encoded
// strings, matching the pre-sweep legacy framing. This is the precise
// shape an attacker would build to weaponize the v0 path against the
// cipher.NewCBCDecrypter panic.
func craftLegacyCBC(t *testing.T, d *AESDriver, iv, ct []byte) string {
	t.Helper()
	ivB64 := base64.StdEncoding.EncodeToString(iv)
	ctB64 := base64.StdEncoding.EncodeToString(ct)
	mac := hmac.New(sha256.New, d.hmacKey)
	mac.Write([]byte("base64:"))
	mac.Write([]byte(ctB64))
	mac.Write([]byte("."))
	mac.Write([]byte(ivB64))
	p := Payload{
		IV:    ivB64,
		Value: ctB64,
		MAC:   base64.StdEncoding.EncodeToString(mac.Sum(nil)),
	}
	out, err := json.Marshal(&p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// v0 has no sentinel prefix.
	return base64.URLEncoding.EncodeToString(out)
}

// TestDecryptCBC_EmptyIV_NoPanic_ReturnsErrDecrypt asserts that a v0
// payload whose IV decodes to zero bytes does NOT panic the process at
// cipher.NewCBCDecrypter. The v0 MAC covers the base64 strings, so an
// attacker can pass MAC verification with iv="" and reach the stdlib
// constructor that panics on len(iv) != BlockSize.
func TestDecryptCBC_EmptyIV_NoPanic_ReturnsErrDecrypt(t *testing.T) {
	d, err := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")
	if err != nil {
		t.Fatalf("NewAESDriver: %v", err)
	}
	// A valid block-aligned ciphertext (one block of zeros) so the
	// constructor is the only thing that could fail; rules out the
	// ciphertext length check stealing the signal.
	payload := craftLegacyCBC(t, d, []byte{}, make([]byte, aes.BlockSize))
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("Decrypt panicked on empty IV: %v", r)
		}
	}()
	_, gotErr := d.Decrypt(payload)
	if gotErr == nil {
		t.Fatalf("expected ErrDecrypt for empty IV, got nil")
	}
	if !errors.Is(gotErr, ErrDecrypt) {
		t.Fatalf("expected ErrDecrypt, got %v", gotErr)
	}
}

// TestDecryptCBC_WrongIVLengths_NoPanic covers a sweep of IV lengths
// that would otherwise trip cipher.NewCBCDecrypter or CryptBlocks.
// BlockSize is 16; anything else must be rejected before the panic.
func TestDecryptCBC_WrongIVLengths_NoPanic(t *testing.T) {
	d, err := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")
	if err != nil {
		t.Fatalf("NewAESDriver: %v", err)
	}
	for _, ivLen := range []int{1, 8, 15, 17, 32, 256} {
		ivLen := ivLen
		t.Run("iv-"+strconv.Itoa(ivLen), func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Decrypt panicked on iv length %d: %v", ivLen, r)
				}
			}()
			payload := craftLegacyCBC(t, d, make([]byte, ivLen), make([]byte, aes.BlockSize))
			_, gotErr := d.Decrypt(payload)
			if gotErr == nil {
				t.Fatalf("expected error for iv length %d, got nil", ivLen)
			}
			if !errors.Is(gotErr, ErrDecrypt) {
				t.Fatalf("expected ErrDecrypt, got %v", gotErr)
			}
		})
	}
}

// TestDecryptCBC_MisalignedCiphertext_NoPanic asserts that a v0 payload
// with a ciphertext that is not a positive multiple of BlockSize does
// not panic at cipher.NewCBCDecrypter / CryptBlocks. The stdlib API
// panics with "crypto/cipher: input not full blocks" on misalignment.
func TestDecryptCBC_MisalignedCiphertext_NoPanic(t *testing.T) {
	d, err := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")
	if err != nil {
		t.Fatalf("NewAESDriver: %v", err)
	}
	cases := []struct {
		name string
		ct   []byte
	}{
		{"empty ciphertext", []byte{}},
		{"one byte short of block", make([]byte, aes.BlockSize-1)},
		{"one byte over block", make([]byte, aes.BlockSize+1)},
		{"23 bytes (between blocks)", make([]byte, 23)},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("Decrypt panicked on ct length %d: %v", len(tc.ct), r)
				}
			}()
			payload := craftLegacyCBC(t, d, make([]byte, aes.BlockSize), tc.ct)
			_, gotErr := d.Decrypt(payload)
			if gotErr == nil {
				t.Fatalf("expected error for ct length %d, got nil", len(tc.ct))
			}
			if !errors.Is(gotErr, ErrDecrypt) {
				t.Fatalf("expected ErrDecrypt, got %v", gotErr)
			}
		})
	}
}

// TestDecryptCBC_V1Path_StillRejectsBadIVLength_NoPanic confirms the
// check also fires on the v1 path; even though the v1 MAC covers raw
// bytes (so a real encrypted payload always has BlockSize iv), a
// hand-crafted v1 envelope with the matching v1 MAC over wrong-length
// iv would still bypass into the panic without an explicit length
// check.
func TestDecryptCBC_V1Path_StillRejectsBadIVLength_NoPanic(t *testing.T) {
	d, err := NewAESDriver([]byte("0123456789abcdef"), nil, "AES-128-CBC")
	if err != nil {
		t.Fatalf("NewAESDriver: %v", err)
	}
	// Craft a v1 envelope with empty IV and a v1 MAC that matches the
	// raw bytes (so the MAC check passes and we reach the length guard).
	iv := []byte{}
	ct := make([]byte, aes.BlockSize)
	mac := hmac.New(sha256.New, d.hmacKey)
	mac.Write([]byte("velocity\x00"))
	mac.Write(iv)
	mac.Write(ct)
	p := Payload{
		IV:    base64.StdEncoding.EncodeToString(iv),
		Value: base64.StdEncoding.EncodeToString(ct),
		MAC:   base64.StdEncoding.EncodeToString(mac.Sum(nil)),
	}
	js, err := json.Marshal(&p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	payload := "v1:" + base64.URLEncoding.EncodeToString(js)

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("v1 Decrypt panicked on empty IV: %v", r)
		}
	}()
	_, gotErr := d.Decrypt(payload)
	if gotErr == nil {
		t.Fatalf("expected error for v1 empty IV, got nil")
	}
	if !errors.Is(gotErr, ErrDecrypt) {
		t.Fatalf("expected ErrDecrypt, got %v", gotErr)
	}
}
