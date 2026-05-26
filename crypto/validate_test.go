package crypto

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func TestNewEncryptor_CallsValidate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr error
	}{
		{
			name:    "empty key rejected",
			cfg:     Config{Key: "", Cipher: "AES-256-GCM"},
			wantErr: ErrInvalidKey,
		},
		{
			name:    "unsupported cipher rejected",
			cfg:     Config{Key: "0123456789abcdef0123456789abcdef", Cipher: "AES-100-CBC"},
			wantErr: ErrInvalidCipher,
		},
		{
			name:    "AES-128-GCM accepted",
			cfg:     Config{Key: "0123456789abcdef", Cipher: "AES-128-GCM"},
			wantErr: nil,
		},
		{
			name:    "AES-192-CBC accepted",
			cfg:     Config{Key: "0123456789abcdef01234567", Cipher: "AES-192-CBC"},
			wantErr: nil,
		},
		{
			name:    "AES-256-GCM accepted",
			cfg:     Config{Key: "0123456789abcdef0123456789abcdef", Cipher: "AES-256-GCM"},
			wantErr: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewEncryptor(tt.cfg)
			if tt.wantErr == nil {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}

// TestConfigValidate_RejectsKeyCipherMismatch pins the M-15 contract:
// Validate now rejects a Config whose key length does not match the
// cipher's required raw byte length. Previously the doc claimed the
// check existed but the implementation did not run it, so a startup
// validator built on Validate would silently approve a 16-byte key for
// AES-256-CBC and only fail later inside NewEncryptor. After M-15 the
// check is symmetric with NewAESDriver: anything Validate accepts also
// passes driver construction.
func TestConfigValidate_RejectsKeyCipherMismatch(t *testing.T) {
	tests := []struct {
		name   string
		cipher string
		key    string
	}{
		{"16-byte raw under AES-256", "AES-256-CBC", strings.Repeat("a", 16)},
		{"24-byte raw under AES-256", "AES-256-GCM", strings.Repeat("a", 24)},
		{"32-byte raw under AES-128", "AES-128-CBC", strings.Repeat("a", 32)},
		{"33-byte raw under AES-256", "AES-256-CBC", strings.Repeat("a", 33)},
		{"31-byte raw under AES-256", "AES-256-GCM", strings.Repeat("a", 31)},
		{"15-byte raw under AES-128", "AES-128-GCM", strings.Repeat("a", 15)},
		{"shortkey under AES-128", "AES-128-CBC", "shortkey"},
		{"base64 decoded short for AES-256", "AES-256-CBC", "base64:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 16)))},
		{"base64 decoded long for AES-128", "AES-128-CBC", "base64:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			err := Config{Key: tt.key, Cipher: tt.cipher}.Validate()
			if err == nil {
				t.Fatalf("Validate accepted mismatched key/cipher; want ErrInvalidKeyLength")
			}
			if !errors.Is(err, ErrInvalidKeyLength) {
				t.Fatalf("expected ErrInvalidKeyLength, got %v", err)
			}
		})
	}
}

// TestConfigValidate_AcceptsCorrectLength is the positive counterpart:
// every cipher with a key of the matching size must pass Validate
// (both raw and base64-prefixed forms).
func TestConfigValidate_AcceptsCorrectLength(t *testing.T) {
	tests := []struct {
		name   string
		cipher string
		size   int
	}{
		{"AES-128-CBC", "AES-128-CBC", 16},
		{"AES-128-GCM", "AES-128-GCM", 16},
		{"AES-192-CBC", "AES-192-CBC", 24},
		{"AES-192-GCM", "AES-192-GCM", 24},
		{"AES-256-CBC", "AES-256-CBC", 32},
		{"AES-256-GCM", "AES-256-GCM", 32},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name+"_raw", func(t *testing.T) {
			err := Config{Key: strings.Repeat("a", tt.size), Cipher: tt.cipher}.Validate()
			if err != nil {
				t.Fatalf("Validate rejected correct-length raw key: %v", err)
			}
		})
		t.Run(tt.name+"_base64", func(t *testing.T) {
			k := "base64:" + base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", tt.size)))
			err := Config{Key: k, Cipher: tt.cipher}.Validate()
			if err != nil {
				t.Fatalf("Validate rejected correct-length base64 key: %v", err)
			}
		})
	}
}

// TestConfigValidate_ConsistentWithNewEncryptor asserts that anything
// Validate accepts also passes NewEncryptor and vice versa. Without this
// symmetry, callers using Validate as a config-time gate would still see
// surprises at runtime.
func TestConfigValidate_ConsistentWithNewEncryptor(t *testing.T) {
	cases := []Config{
		{Key: strings.Repeat("a", 32), Cipher: "AES-256-CBC"},
		{Key: strings.Repeat("a", 16), Cipher: "AES-128-GCM"},
		{Key: strings.Repeat("a", 8), Cipher: "AES-128-CBC"},   // mismatch
		{Key: strings.Repeat("a", 24), Cipher: "AES-256-CBC"},  // mismatch
		{Key: "", Cipher: "AES-256-CBC"},                       // empty
		{Key: strings.Repeat("a", 32), Cipher: "BLOWFISH-128"}, // unsupported
	}
	for i, cfg := range cases {
		cfg := cfg
		t.Run(cfg.Cipher, func(t *testing.T) {
			vErr := cfg.Validate()
			_, nErr := NewEncryptor(cfg)
			// Both must agree on accept-or-reject. The exact error
			// codes can differ in detail (Validate returns
			// ErrInvalidCipher; NewEncryptor may dive into the
			// driver), but acceptance/rejection must be symmetric.
			if (vErr == nil) != (nErr == nil) {
				t.Fatalf("case %d: Validate=%v but NewEncryptor=%v", i, vErr, nErr)
			}
		})
	}
}
