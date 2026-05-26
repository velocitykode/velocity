package drivers

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// TestDecrypt_ErrorsCollapseToErrDecrypt asserts that every cryptographic
// CBC decrypt failure returns ErrDecrypt (one sentinel, not six distinct
// strings). The previous behaviour returned different errors for bad IV
// encoding, bad value encoding, missing MAC, wrong MAC, bad padding, etc.
// If any of those messages ever reached a client (debug exception
// handler, careless wrap, etc.) the client would gain a padding-oracle
// precursor: each variant tells the attacker which check fell first.
func TestDecrypt_ErrorsCollapseToErrDecrypt(t *testing.T) {
	key := []byte("0123456789abcdef")
	d, err := NewAESDriver(key, nil, "AES-128-CBC")
	if err != nil {
		t.Fatalf("NewAESDriver: %v", err)
	}

	// Build a real v1 ciphertext we can mutate field-by-field.
	good, err := d.Encrypt("data")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	envelope := strings.TrimPrefix(good, "v1:")
	data, err := base64.URLEncoding.DecodeString(envelope)
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}

	mutate := func(fn func(p *Payload)) string {
		var p Payload
		if err := json.Unmarshal(data, &p); err != nil {
			t.Fatalf("unmarshal envelope: %v", err)
		}
		fn(&p)
		out, err := json.Marshal(&p)
		if err != nil {
			t.Fatalf("marshal envelope: %v", err)
		}
		return "v1:" + base64.URLEncoding.EncodeToString(out)
	}

	tests := []struct {
		name    string
		payload string
	}{
		{
			name: "missing MAC",
			payload: mutate(func(p *Payload) {
				p.MAC = ""
			}),
		},
		{
			name: "non-base64 IV",
			payload: mutate(func(p *Payload) {
				p.IV = "!!!"
			}),
		},
		{
			name: "non-base64 value",
			payload: mutate(func(p *Payload) {
				p.Value = "!!!"
			}),
		},
		{
			name: "mismatched MAC",
			payload: mutate(func(p *Payload) {
				p.MAC = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
			}),
		},
		{
			name: "ciphertext bit-flip after MAC adjusted off",
			payload: mutate(func(p *Payload) {
				ct, _ := base64.StdEncoding.DecodeString(p.Value)
				if len(ct) > 0 {
					ct[0] ^= 0xFF
				}
				p.Value = base64.StdEncoding.EncodeToString(ct)
			}),
		},
		{
			name: "wrong key path (all keys exhausted)",
			payload: func() string {
				// Encrypt with a different driver so MAC verification
				// fails on every key the receiving driver tries.
				other, _ := NewAESDriver([]byte("fedcba9876543210"), nil, "AES-128-CBC")
				ct, err := other.Encrypt("data")
				if err != nil {
					t.Fatalf("other encrypt: %v", err)
				}
				return ct
			}(),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.Decrypt(tc.payload)
			if err == nil {
				t.Fatalf("expected ErrDecrypt, got nil")
			}
			if !errors.Is(err, ErrDecrypt) {
				t.Fatalf("expected errors.Is(err, ErrDecrypt) for %q, got %v", tc.name, err)
			}
			// The error message MUST NOT distinguish between the
			// underlying causes; that would be the oracle. All
			// variants share the same generic surface string.
			if err.Error() != ErrDecrypt.Error() {
				t.Fatalf("error message must not vary by cause: got %q, want %q", err.Error(), ErrDecrypt.Error())
			}
		})
	}
}

// TestDecrypt_StructuralErrorsStayDistinct asserts that ErrInvalidPayload
// remains distinguishable from ErrDecrypt. Structural envelope failures
// (empty input, non-base64 outer, non-JSON inner) are not cryptographic
// decisions and revealing them does not leak oracle bits. Callers like
// the flash cookie reader actually need that distinction to fall back
// across encryption paths.
func TestDecrypt_StructuralErrorsStayDistinct(t *testing.T) {
	key := []byte("0123456789abcdef")
	d, err := NewAESDriver(key, nil, "AES-128-CBC")
	if err != nil {
		t.Fatalf("NewAESDriver: %v", err)
	}

	cases := []struct {
		name    string
		payload string
	}{
		{"empty payload", ""},
		{"non-base64 outer", "v1:!!!"},
		{"valid base64 outer but not JSON", "v1:" + base64.URLEncoding.EncodeToString([]byte("not json"))},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.Decrypt(tc.payload)
			if err == nil {
				t.Fatalf("expected ErrInvalidPayload, got nil")
			}
			if !errors.Is(err, ErrInvalidPayload) {
				t.Fatalf("expected ErrInvalidPayload, got %v", err)
			}
			if errors.Is(err, ErrDecrypt) {
				t.Fatalf("structural failures must NOT collapse to ErrDecrypt: %v", err)
			}
		})
	}
}

// TestDecrypt_GCMErrorsCollapse covers the GCM-mode equivalent: every
// per-key failure surface (bad nonce length, bad tag b64, tampered
// ciphertext, wrong key) must collapse to ErrDecrypt on the non-AAD
// path. The AAD path keeps its own ErrAADMismatch sentinel by contract.
func TestDecrypt_GCMErrorsCollapse(t *testing.T) {
	key := []byte("0123456789abcdef")
	d, err := NewAESDriver(key, nil, "AES-128-GCM")
	if err != nil {
		t.Fatalf("NewAESDriver: %v", err)
	}

	good, err := d.Encrypt("data")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	envelope := strings.TrimPrefix(good, "v1:")
	data, _ := base64.URLEncoding.DecodeString(envelope)

	mutate := func(fn func(p *Payload)) string {
		var p Payload
		_ = json.Unmarshal(data, &p)
		fn(&p)
		out, _ := json.Marshal(&p)
		return "v1:" + base64.URLEncoding.EncodeToString(out)
	}

	tests := []struct {
		name    string
		payload string
	}{
		{
			name: "non-base64 nonce",
			payload: mutate(func(p *Payload) {
				p.IV = "!!!"
			}),
		},
		{
			name: "non-base64 tag",
			payload: mutate(func(p *Payload) {
				p.Tag = "!!!"
			}),
		},
		{
			name: "wrong nonce length",
			payload: mutate(func(p *Payload) {
				p.IV = base64.StdEncoding.EncodeToString([]byte("short"))
			}),
		},
		{
			name: "tag bit-flip",
			payload: mutate(func(p *Payload) {
				tag, _ := base64.StdEncoding.DecodeString(p.Tag)
				if len(tag) > 0 {
					tag[0] ^= 0xFF
				}
				p.Tag = base64.StdEncoding.EncodeToString(tag)
			}),
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			_, err := d.Decrypt(tc.payload)
			if err == nil {
				t.Fatalf("expected ErrDecrypt, got nil")
			}
			if !errors.Is(err, ErrDecrypt) {
				t.Fatalf("expected ErrDecrypt for %q, got %v", tc.name, err)
			}
		})
	}
}

// TestErrDecryptionFailed_AliasErrDecrypt confirms the rename keeps
// callers using errors.Is(err, ErrDecryptionFailed) working unchanged.
func TestErrDecryptionFailed_AliasErrDecrypt(t *testing.T) {
	if ErrDecryptionFailed != ErrDecrypt {
		t.Fatalf("ErrDecryptionFailed must alias ErrDecrypt for backward compat")
	}
	if !errors.Is(ErrDecrypt, ErrDecryptionFailed) {
		t.Fatalf("errors.Is(ErrDecrypt, ErrDecryptionFailed) must hold")
	}
}
