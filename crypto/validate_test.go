package crypto

import (
	"errors"
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
