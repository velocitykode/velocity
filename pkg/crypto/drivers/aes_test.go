package drivers

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func TestNewAESDriver(t *testing.T) {
	tests := []struct {
		name         string
		key          []byte
		previousKeys [][]byte
		cipher       string
		wantErr      bool
		errContains  string
	}{
		{
			name:    "creates driver with AES-128-CBC and valid 16-byte key",
			key:     make([]byte, 16),
			cipher:  "AES-128-CBC",
			wantErr: false,
		},
		{
			name:    "creates driver with AES-256-CBC and valid 32-byte key",
			key:     make([]byte, 32),
			cipher:  "AES-256-CBC",
			wantErr: false,
		},
		{
			name:    "creates driver with AES-128-GCM and valid 16-byte key",
			key:     make([]byte, 16),
			cipher:  "AES-128-GCM",
			wantErr: false,
		},
		{
			name:    "creates driver with AES-256-GCM and valid 32-byte key",
			key:     make([]byte, 32),
			cipher:  "AES-256-GCM",
			wantErr: false,
		},
		{
			name:    "creates driver with lowercase cipher name",
			key:     make([]byte, 16),
			cipher:  "aes-128-cbc",
			wantErr: false,
		},
		{
			name:    "creates driver with mixed case cipher name",
			key:     make([]byte, 32),
			cipher:  "Aes-256-Gcm",
			wantErr: false,
		},
		{
			name:         "creates driver with previous keys for rotation",
			key:          make([]byte, 16),
			previousKeys: [][]byte{make([]byte, 16), make([]byte, 16)},
			cipher:       "AES-128-CBC",
			wantErr:      false,
		},
		{
			name:        "returns error for unsupported cipher",
			key:         make([]byte, 16),
			cipher:      "AES-512-CBC",
			wantErr:     true,
			errContains: "unsupported cipher",
		},
		{
			name:        "returns error for invalid cipher name",
			key:         make([]byte, 16),
			cipher:      "BLOWFISH-128",
			wantErr:     true,
			errContains: "unsupported cipher",
		},
		{
			name:        "returns error for empty cipher name",
			key:         make([]byte, 16),
			cipher:      "",
			wantErr:     true,
			errContains: "unsupported cipher",
		},
		{
			name:        "returns error when key too short for AES-128",
			key:         make([]byte, 8),
			cipher:      "AES-128-CBC",
			wantErr:     true,
			errContains: "invalid key size",
		},
		{
			name:        "returns error when key too long for AES-128",
			key:         make([]byte, 32),
			cipher:      "AES-128-CBC",
			wantErr:     true,
			errContains: "invalid key size",
		},
		{
			name:        "returns error when key too short for AES-256",
			key:         make([]byte, 16),
			cipher:      "AES-256-CBC",
			wantErr:     true,
			errContains: "invalid key size",
		},
		{
			name:        "returns error when key too long for AES-256",
			key:         make([]byte, 64),
			cipher:      "AES-256-GCM",
			wantErr:     true,
			errContains: "invalid key size",
		},
		{
			name:        "returns error for empty key",
			key:         []byte{},
			cipher:      "AES-128-CBC",
			wantErr:     true,
			errContains: "invalid key size",
		},
		{
			name:        "returns error for nil key",
			key:         nil,
			cipher:      "AES-128-CBC",
			wantErr:     true,
			errContains: "invalid key size",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver, err := NewAESDriver(tt.key, tt.previousKeys, tt.cipher)
			if tt.wantErr {
				if err == nil {
					t.Errorf("expected error containing %q, got nil", tt.errContains)
					return
				}
				if !strings.Contains(err.Error(), tt.errContains) {
					t.Errorf("error = %v, want error containing %q", err, tt.errContains)
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if driver == nil {
				t.Error("expected non-nil driver")
			}
		})
	}
}

func TestPkcs7Pad(t *testing.T) {
	tests := []struct {
		name      string
		data      []byte
		blockSize int
		wantLen   int
	}{
		{
			name:      "pads empty input to block size",
			data:      []byte{},
			blockSize: 16,
			wantLen:   16,
		},
		{
			name:      "pads single byte to block size",
			data:      []byte{0x01},
			blockSize: 16,
			wantLen:   16,
		},
		{
			name:      "pads 15 bytes to block size",
			data:      make([]byte, 15),
			blockSize: 16,
			wantLen:   16,
		},
		{
			name:      "pads exact block size to double block size",
			data:      make([]byte, 16),
			blockSize: 16,
			wantLen:   32,
		},
		{
			name:      "pads 17 bytes to 32 bytes",
			data:      make([]byte, 17),
			blockSize: 16,
			wantLen:   32,
		},
		{
			name:      "pads 31 bytes to 32 bytes",
			data:      make([]byte, 31),
			blockSize: 16,
			wantLen:   32,
		},
		{
			name:      "pads with block size 8",
			data:      make([]byte, 5),
			blockSize: 8,
			wantLen:   8,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pkcs7Pad(tt.data, tt.blockSize)
			if len(got) != tt.wantLen {
				t.Errorf("padded length = %d, want %d", len(got), tt.wantLen)
			}
			// Verify padding value is correct
			padding := got[len(got)-1]
			if int(padding) > tt.blockSize || padding == 0 {
				t.Errorf("invalid padding value: %d", padding)
			}
			// Verify all padding bytes are the same
			for i := len(got) - int(padding); i < len(got); i++ {
				if got[i] != padding {
					t.Errorf("inconsistent padding at position %d: got %d, want %d", i, got[i], padding)
				}
			}
		})
	}
}

func TestPkcs7Unpad(t *testing.T) {
	tests := []struct {
		name    string
		data    []byte
		want    []byte
		wantErr bool
	}{
		{
			name:    "unpads single block with 1 byte padding",
			data:    append([]byte("hello world!!!!"), byte(1)),
			want:    []byte("hello world!!!!"),
			wantErr: false,
		},
		{
			name:    "unpads single block with 5 byte padding",
			data:    append([]byte("hello world"), []byte{5, 5, 5, 5, 5}...),
			want:    []byte("hello world"),
			wantErr: false,
		},
		{
			name:    "unpads full block of padding",
			data:    []byte{16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16, 16},
			want:    []byte{},
			wantErr: false,
		},
		{
			name:    "returns error for empty input",
			data:    []byte{},
			wantErr: true,
		},
		{
			name:    "returns error when padding value exceeds data length",
			data:    []byte{0x10}, // padding value 16 but only 1 byte of data
			wantErr: true,
		},
		{
			name:    "returns error for zero padding value",
			data:    []byte{0x00},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pkcs7Unpad(tt.data)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if string(got) != string(tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSecureCompare(t *testing.T) {
	tests := []struct {
		name string
		a    string
		b    string
		want bool
	}{
		{
			name: "returns true for equal strings",
			a:    "hello world",
			b:    "hello world",
			want: true,
		},
		{
			name: "returns true for empty strings",
			a:    "",
			b:    "",
			want: true,
		},
		{
			name: "returns false for different strings same length",
			a:    "hello",
			b:    "world",
			want: false,
		},
		{
			name: "returns false for different lengths",
			a:    "hello",
			b:    "hello world",
			want: false,
		},
		{
			name: "returns false when first is empty",
			a:    "",
			b:    "hello",
			want: false,
		},
		{
			name: "returns false when second is empty",
			a:    "hello",
			b:    "",
			want: false,
		},
		{
			name: "returns false for strings differing by one character",
			a:    "hello",
			b:    "hellp",
			want: false,
		},
		{
			name: "returns true for long equal strings",
			a:    strings.Repeat("a", 1000),
			b:    strings.Repeat("a", 1000),
			want: true,
		},
		{
			name: "returns false for long strings differing at end",
			a:    strings.Repeat("a", 999) + "b",
			b:    strings.Repeat("a", 1000),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := secureCompare(tt.a, tt.b)
			if got != tt.want {
				t.Errorf("secureCompare(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestEncryptDecryptRoundTrip(t *testing.T) {
	key16 := []byte("0123456789abcdef")
	key32 := []byte("0123456789abcdef0123456789abcdef")

	cipherConfigs := []struct {
		cipher string
		key    []byte
	}{
		{"AES-128-CBC", key16},
		{"AES-256-CBC", key32},
		{"AES-128-GCM", key16},
		{"AES-256-GCM", key32},
	}

	plaintexts := []struct {
		name string
		data string
	}{
		{"empty plaintext", ""},
		{"single character", "a"},
		{"short plaintext", "hello"},
		{"exactly one block", "0123456789abcdef"},
		{"slightly over one block", "0123456789abcdefg"},
		{"two blocks", "0123456789abcdef0123456789abcdef"},
		{"large plaintext", strings.Repeat("test data ", 100)},
		{"plaintext with special characters", "hello\nworld\t!@#$%^&*()"},
		{"plaintext with unicode", "hello \u4e16\u754c \u0414\u0440\u0443\u0433"},
		{"plaintext with null bytes", "hello\x00world"},
	}

	for _, cfg := range cipherConfigs {
		for _, pt := range plaintexts {
			testName := cfg.cipher + " with " + pt.name
			t.Run(testName, func(t *testing.T) {
				driver, err := NewAESDriver(cfg.key, nil, cfg.cipher)
				if err != nil {
					t.Fatalf("failed to create driver: %v", err)
				}

				encrypted, err := driver.Encrypt(pt.data)
				if err != nil {
					t.Fatalf("Encrypt() error: %v", err)
				}

				if encrypted == "" {
					t.Error("encrypted result should not be empty")
				}

				decrypted, err := driver.Decrypt(encrypted)
				if err != nil {
					t.Fatalf("Decrypt() error: %v", err)
				}

				if decrypted != pt.data {
					t.Errorf("round trip failed: got %q, want %q", decrypted, pt.data)
				}
			})
		}
	}
}

func TestEncryptBytesDecryptBytesRoundTrip(t *testing.T) {
	key16 := []byte("0123456789abcdef")

	tests := []struct {
		name   string
		cipher string
		data   []byte
	}{
		{"empty bytes with CBC", "AES-128-CBC", []byte{}},
		{"empty bytes with GCM", "AES-128-GCM", []byte{}},
		{"binary data with CBC", "AES-128-CBC", []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}},
		{"binary data with GCM", "AES-128-GCM", []byte{0x00, 0x01, 0x02, 0xFF, 0xFE}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver, err := NewAESDriver(key16, nil, tt.cipher)
			if err != nil {
				t.Fatalf("failed to create driver: %v", err)
			}

			encrypted, err := driver.EncryptBytes(tt.data)
			if err != nil {
				t.Fatalf("EncryptBytes() error: %v", err)
			}

			decrypted, err := driver.DecryptBytes(encrypted)
			if err != nil {
				t.Fatalf("DecryptBytes() error: %v", err)
			}

			if len(decrypted) != len(tt.data) {
				t.Errorf("length mismatch: got %d, want %d", len(decrypted), len(tt.data))
				return
			}

			for i := range tt.data {
				if decrypted[i] != tt.data[i] {
					t.Errorf("byte mismatch at position %d: got %x, want %x", i, decrypted[i], tt.data[i])
				}
			}
		})
	}
}

func TestDecryptBytesWithKeyRotation(t *testing.T) {
	currentKey := []byte("currentkey123456")
	oldKey1 := []byte("oldkey1234567890")
	oldKey2 := []byte("oldkey2abcdefghi")

	tests := []struct {
		name         string
		encryptKey   []byte
		currentKey   []byte
		previousKeys [][]byte
		plaintext    string
		wantErr      bool
	}{
		{
			name:         "decrypts with current key",
			encryptKey:   currentKey,
			currentKey:   currentKey,
			previousKeys: [][]byte{oldKey1, oldKey2},
			plaintext:    "secret message",
			wantErr:      false,
		},
		{
			name:         "decrypts with first previous key",
			encryptKey:   oldKey1,
			currentKey:   currentKey,
			previousKeys: [][]byte{oldKey1, oldKey2},
			plaintext:    "secret message",
			wantErr:      false,
		},
		{
			name:         "decrypts with second previous key",
			encryptKey:   oldKey2,
			currentKey:   currentKey,
			previousKeys: [][]byte{oldKey1, oldKey2},
			plaintext:    "secret message",
			wantErr:      false,
		},
		{
			name:         "fails with unknown key",
			encryptKey:   []byte("unknownkey123456"),
			currentKey:   currentKey,
			previousKeys: [][]byte{oldKey1, oldKey2},
			plaintext:    "secret message",
			wantErr:      true,
		},
		{
			name:         "decrypts with no previous keys when current matches",
			encryptKey:   currentKey,
			currentKey:   currentKey,
			previousKeys: nil,
			plaintext:    "secret message",
			wantErr:      false,
		},
	}

	ciphers := []string{"AES-128-CBC", "AES-128-GCM"}

	for _, cipher := range ciphers {
		for _, tt := range tests {
			testName := cipher + " " + tt.name
			t.Run(testName, func(t *testing.T) {
				// Encrypt with the encrypt key
				encDriver, err := NewAESDriver(tt.encryptKey, nil, cipher)
				if err != nil {
					t.Fatalf("failed to create encrypt driver: %v", err)
				}

				encrypted, err := encDriver.Encrypt(tt.plaintext)
				if err != nil {
					t.Fatalf("Encrypt() error: %v", err)
				}

				// Decrypt with current key and previous keys
				decDriver, err := NewAESDriver(tt.currentKey, tt.previousKeys, cipher)
				if err != nil {
					t.Fatalf("failed to create decrypt driver: %v", err)
				}

				decrypted, err := decDriver.Decrypt(encrypted)
				if tt.wantErr {
					if err == nil {
						t.Error("expected error, got nil")
					}
					return
				}
				if err != nil {
					t.Errorf("unexpected error: %v", err)
					return
				}

				if decrypted != tt.plaintext {
					t.Errorf("got %q, want %q", decrypted, tt.plaintext)
				}
			})
		}
	}
}

func TestTamperDetectionCBC(t *testing.T) {
	key := []byte("0123456789abcdef")
	driver, _ := NewAESDriver(key, nil, "AES-128-CBC")

	encrypted, err := driver.Encrypt("sensitive data")
	if err != nil {
		t.Fatalf("Encrypt() error: %v", err)
	}

	tests := []struct {
		name       string
		tamperFunc func(string) string
	}{
		{
			name: "fails when ciphertext is modified",
			tamperFunc: func(payload string) string {
				data, _ := base64.URLEncoding.DecodeString(payload)
				var p Payload
				json.Unmarshal(data, &p)

				// Decode, modify, and re-encode the value
				ciphertext, _ := base64.StdEncoding.DecodeString(p.Value)
				if len(ciphertext) > 0 {
					ciphertext[0] ^= 0xFF
				}
				p.Value = base64.StdEncoding.EncodeToString(ciphertext)

				modified, _ := json.Marshal(p)
				return base64.URLEncoding.EncodeToString(modified)
			},
		},
		{
			name: "fails when IV is modified",
			tamperFunc: func(payload string) string {
				data, _ := base64.URLEncoding.DecodeString(payload)
				var p Payload
				json.Unmarshal(data, &p)

				// Decode, modify, and re-encode the IV
				iv, _ := base64.StdEncoding.DecodeString(p.IV)
				if len(iv) > 0 {
					iv[0] ^= 0xFF
				}
				p.IV = base64.StdEncoding.EncodeToString(iv)

				modified, _ := json.Marshal(p)
				return base64.URLEncoding.EncodeToString(modified)
			},
		},
		{
			name: "fails when MAC is modified",
			tamperFunc: func(payload string) string {
				data, _ := base64.URLEncoding.DecodeString(payload)
				var p Payload
				json.Unmarshal(data, &p)

				// Modify the MAC
				p.MAC = "invalid_mac_value"

				modified, _ := json.Marshal(p)
				return base64.URLEncoding.EncodeToString(modified)
			},
		},
		{
			name: "fails when MAC is removed",
			tamperFunc: func(payload string) string {
				data, _ := base64.URLEncoding.DecodeString(payload)
				var p Payload
				json.Unmarshal(data, &p)

				// Remove the MAC - in CBC mode this should fail padding validation
				p.MAC = ""
				// Also corrupt the ciphertext to ensure failure
				ciphertext, _ := base64.StdEncoding.DecodeString(p.Value)
				if len(ciphertext) > 0 {
					ciphertext[len(ciphertext)-1] ^= 0xFF
				}
				p.Value = base64.StdEncoding.EncodeToString(ciphertext)

				modified, _ := json.Marshal(p)
				return base64.URLEncoding.EncodeToString(modified)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tampered := tt.tamperFunc(encrypted)
			_, err := driver.Decrypt(tampered)
			if err == nil {
				t.Error("expected decryption to fail with tampered data")
			}
		})
	}
}

func TestTamperDetectionGCM(t *testing.T) {
	key := []byte("0123456789abcdef")
	driver, _ := NewAESDriver(key, nil, "AES-128-GCM")

	encrypted, err := driver.Encrypt("sensitive data")
	if err != nil {
		t.Fatalf("Encrypt() error: %v", err)
	}

	tests := []struct {
		name       string
		tamperFunc func(string) string
	}{
		{
			name: "fails when ciphertext is modified",
			tamperFunc: func(payload string) string {
				data, _ := base64.URLEncoding.DecodeString(payload)
				var p Payload
				json.Unmarshal(data, &p)

				ciphertext, _ := base64.StdEncoding.DecodeString(p.Value)
				if len(ciphertext) > 0 {
					ciphertext[0] ^= 0xFF
				}
				p.Value = base64.StdEncoding.EncodeToString(ciphertext)

				modified, _ := json.Marshal(p)
				return base64.URLEncoding.EncodeToString(modified)
			},
		},
		{
			name: "fails when nonce is modified",
			tamperFunc: func(payload string) string {
				data, _ := base64.URLEncoding.DecodeString(payload)
				var p Payload
				json.Unmarshal(data, &p)

				nonce, _ := base64.StdEncoding.DecodeString(p.IV)
				if len(nonce) > 0 {
					nonce[0] ^= 0xFF
				}
				p.IV = base64.StdEncoding.EncodeToString(nonce)

				modified, _ := json.Marshal(p)
				return base64.URLEncoding.EncodeToString(modified)
			},
		},
		{
			name: "fails when tag is modified",
			tamperFunc: func(payload string) string {
				data, _ := base64.URLEncoding.DecodeString(payload)
				var p Payload
				json.Unmarshal(data, &p)

				tag, _ := base64.StdEncoding.DecodeString(p.Tag)
				if len(tag) > 0 {
					tag[0] ^= 0xFF
				}
				p.Tag = base64.StdEncoding.EncodeToString(tag)

				modified, _ := json.Marshal(p)
				return base64.URLEncoding.EncodeToString(modified)
			},
		},
		{
			name: "fails when tag is truncated",
			tamperFunc: func(payload string) string {
				data, _ := base64.URLEncoding.DecodeString(payload)
				var p Payload
				json.Unmarshal(data, &p)

				tag, _ := base64.StdEncoding.DecodeString(p.Tag)
				if len(tag) > 1 {
					tag = tag[:len(tag)-1]
				}
				p.Tag = base64.StdEncoding.EncodeToString(tag)

				modified, _ := json.Marshal(p)
				return base64.URLEncoding.EncodeToString(modified)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tampered := tt.tamperFunc(encrypted)
			_, err := driver.Decrypt(tampered)
			if err == nil {
				t.Error("expected decryption to fail with tampered data")
			}
		})
	}
}

func TestSerializePayload(t *testing.T) {
	tests := []struct {
		name    string
		payload *Payload
		wantErr bool
	}{
		{
			name: "serializes payload with all fields",
			payload: &Payload{
				IV:    "test_iv",
				Value: "test_value",
				MAC:   "test_mac",
				Tag:   "test_tag",
			},
			wantErr: false,
		},
		{
			name: "serializes payload with only required fields",
			payload: &Payload{
				IV:    "test_iv",
				Value: "test_value",
			},
			wantErr: false,
		},
		{
			name: "serializes payload with empty strings",
			payload: &Payload{
				IV:    "",
				Value: "",
			},
			wantErr: false,
		},
		{
			name: "serializes payload with special characters",
			payload: &Payload{
				IV:    "iv+with/special==",
				Value: "value+with/special==",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := serializePayload(tt.payload)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if result == "" {
				t.Error("expected non-empty result")
			}

			// Verify it's valid URL-safe base64
			_, err = base64.URLEncoding.DecodeString(result)
			if err != nil {
				t.Errorf("result is not valid URL-safe base64: %v", err)
			}
		})
	}
}

func TestDeserializePayload(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    *Payload
		wantErr bool
	}{
		{
			name: "deserializes URL-safe base64 payload",
			input: func() string {
				p := &Payload{IV: "test_iv", Value: "test_value", MAC: "test_mac"}
				data, _ := json.Marshal(p)
				return base64.URLEncoding.EncodeToString(data)
			}(),
			want:    &Payload{IV: "test_iv", Value: "test_value", MAC: "test_mac"},
			wantErr: false,
		},
		{
			name: "deserializes standard base64 payload",
			input: func() string {
				p := &Payload{IV: "test_iv", Value: "test_value", MAC: "test_mac"}
				data, _ := json.Marshal(p)
				return base64.StdEncoding.EncodeToString(data)
			}(),
			want:    &Payload{IV: "test_iv", Value: "test_value", MAC: "test_mac"},
			wantErr: false,
		},
		{
			name:    "returns error for invalid base64",
			input:   "not-valid-base64!!!",
			wantErr: true,
		},
		{
			name: "returns error for valid base64 but invalid JSON",
			input: func() string {
				return base64.URLEncoding.EncodeToString([]byte("not json"))
			}(),
			wantErr: true,
		},
		{
			name:    "returns error for empty input",
			input:   "",
			wantErr: true,
		},
		{
			name: "deserializes payload with tag field",
			input: func() string {
				p := &Payload{IV: "nonce", Value: "cipher", Tag: "auth_tag"}
				data, _ := json.Marshal(p)
				return base64.URLEncoding.EncodeToString(data)
			}(),
			want:    &Payload{IV: "nonce", Value: "cipher", Tag: "auth_tag"},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := deserializePayload(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Errorf("unexpected error: %v", err)
				return
			}
			if got.IV != tt.want.IV || got.Value != tt.want.Value || got.MAC != tt.want.MAC || got.Tag != tt.want.Tag {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestGenerateKey(t *testing.T) {
	tests := []struct {
		name           string
		cipher         string
		key            []byte
		wantKeyLen     int
		wantBase64Len  int
		wantPrefixWith string
	}{
		{
			name:           "generates 16-byte key for AES-128-CBC",
			cipher:         "AES-128-CBC",
			key:            make([]byte, 16),
			wantKeyLen:     16,
			wantPrefixWith: "base64:",
		},
		{
			name:           "generates 32-byte key for AES-256-CBC",
			cipher:         "AES-256-CBC",
			key:            make([]byte, 32),
			wantKeyLen:     32,
			wantPrefixWith: "base64:",
		},
		{
			name:           "generates 16-byte key for AES-128-GCM",
			cipher:         "AES-128-GCM",
			key:            make([]byte, 16),
			wantKeyLen:     16,
			wantPrefixWith: "base64:",
		},
		{
			name:           "generates 32-byte key for AES-256-GCM",
			cipher:         "AES-256-GCM",
			key:            make([]byte, 32),
			wantKeyLen:     32,
			wantPrefixWith: "base64:",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver, err := NewAESDriver(tt.key, nil, tt.cipher)
			if err != nil {
				t.Fatalf("failed to create driver: %v", err)
			}

			generated, err := driver.GenerateKey()
			if err != nil {
				t.Fatalf("GenerateKey() error: %v", err)
			}

			if !strings.HasPrefix(generated, tt.wantPrefixWith) {
				t.Errorf("generated key should start with %q, got %q", tt.wantPrefixWith, generated)
			}

			// Extract and decode the base64 portion
			b64Part := strings.TrimPrefix(generated, "base64:")
			decoded, err := base64.StdEncoding.DecodeString(b64Part)
			if err != nil {
				t.Errorf("failed to decode generated key: %v", err)
			}

			if len(decoded) != tt.wantKeyLen {
				t.Errorf("decoded key length = %d, want %d", len(decoded), tt.wantKeyLen)
			}
		})
	}
}

func TestGenerateKeyUniqueness(t *testing.T) {
	driver, _ := NewAESDriver(make([]byte, 16), nil, "AES-128-CBC")

	keys := make(map[string]bool)
	for i := 0; i < 100; i++ {
		key, err := driver.GenerateKey()
		if err != nil {
			t.Fatalf("GenerateKey() error on iteration %d: %v", i, err)
		}
		if keys[key] {
			t.Errorf("duplicate key generated: %s", key)
		}
		keys[key] = true
	}
}

func TestGenerateMAC(t *testing.T) {
	key := []byte("0123456789abcdef")
	driver, _ := NewAESDriver(key, nil, "AES-128-CBC")

	tests := []struct {
		name  string
		value string
		iv    string
	}{
		{
			name:  "generates MAC for typical values",
			value: "encrypted_value_base64",
			iv:    "iv_base64",
		},
		{
			name:  "generates MAC for empty value",
			value: "",
			iv:    "iv_base64",
		},
		{
			name:  "generates MAC for empty iv",
			value: "encrypted_value",
			iv:    "",
		},
		{
			name:  "generates MAC for both empty",
			value: "",
			iv:    "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mac := driver.generateMAC(tt.value, tt.iv)

			if mac == "" {
				t.Error("MAC should not be empty")
			}

			// Verify it's valid base64
			decoded, err := base64.StdEncoding.DecodeString(mac)
			if err != nil {
				t.Errorf("MAC is not valid base64: %v", err)
			}

			// SHA256 HMAC produces 32 bytes
			if len(decoded) != 32 {
				t.Errorf("decoded MAC length = %d, want 32", len(decoded))
			}

			// Verify determinism - same inputs should produce same MAC
			mac2 := driver.generateMAC(tt.value, tt.iv)
			if mac != mac2 {
				t.Error("MAC should be deterministic")
			}
		})
	}
}

func TestGenerateMACWithKey(t *testing.T) {
	key1 := []byte("0123456789abcdef")
	key2 := []byte("fedcba9876543210")

	value := "test_value"
	iv := "test_iv"

	tests := []struct {
		name string
		key  []byte
	}{
		{"generates MAC with primary key", key1},
		{"generates MAC with different key", key2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mac := generateMACWith(value, iv, tt.key)

			if mac == "" {
				t.Error("MAC should not be empty")
			}

			// Verify by computing expected MAC manually
			expectedMAC := hmac.New(sha256.New, tt.key)
			expectedMAC.Write([]byte("base64:" + value + "." + iv))
			expected := base64.StdEncoding.EncodeToString(expectedMAC.Sum(nil))

			if mac != expected {
				t.Errorf("MAC mismatch: got %s, want %s", mac, expected)
			}
		})
	}

	// Test that different keys produce different MACs
	t.Run("different keys produce different MACs", func(t *testing.T) {
		mac1 := generateMACWith(value, iv, key1)
		mac2 := generateMACWith(value, iv, key2)

		if mac1 == mac2 {
			t.Error("different keys should produce different MACs")
		}
	})
}

func TestEncryptProducesUniqueOutput(t *testing.T) {
	key := []byte("0123456789abcdef")
	plaintext := "same plaintext"

	ciphers := []string{"AES-128-CBC", "AES-128-GCM"}

	for _, cipher := range ciphers {
		t.Run(cipher+" produces unique ciphertext each time", func(t *testing.T) {
			driver, _ := NewAESDriver(key, nil, cipher)

			outputs := make(map[string]bool)
			for i := 0; i < 10; i++ {
				encrypted, err := driver.Encrypt(plaintext)
				if err != nil {
					t.Fatalf("Encrypt() error: %v", err)
				}
				if outputs[encrypted] {
					t.Error("encryption should produce unique output due to random IV/nonce")
				}
				outputs[encrypted] = true
			}
		})
	}
}

func TestDecryptInvalidPayloads(t *testing.T) {
	key := []byte("0123456789abcdef")

	ciphers := []string{"AES-128-CBC", "AES-128-GCM"}

	tests := []struct {
		name    string
		payload string
	}{
		{"empty payload", ""},
		{"invalid base64", "not-valid-base64!!!"},
		{"valid base64 but not JSON", base64.URLEncoding.EncodeToString([]byte("not json"))},
		{"random garbage", "aGVsbG8gd29ybGQ="},
	}

	for _, cipher := range ciphers {
		driver, _ := NewAESDriver(key, nil, cipher)

		for _, tt := range tests {
			testName := cipher + " " + tt.name
			t.Run(testName, func(t *testing.T) {
				_, err := driver.Decrypt(tt.payload)
				if err == nil {
					t.Error("expected error for invalid payload")
				}
			})
		}
	}
}

func TestDecryptWithWrongKey(t *testing.T) {
	key1 := []byte("0123456789abcdef")
	key2 := []byte("fedcba9876543210")

	ciphers := []string{"AES-128-CBC", "AES-128-GCM"}

	for _, cipher := range ciphers {
		t.Run(cipher+" fails with wrong key", func(t *testing.T) {
			driver1, _ := NewAESDriver(key1, nil, cipher)
			driver2, _ := NewAESDriver(key2, nil, cipher)

			encrypted, err := driver1.Encrypt("secret message")
			if err != nil {
				t.Fatalf("Encrypt() error: %v", err)
			}

			_, err = driver2.Decrypt(encrypted)
			if err == nil {
				t.Error("expected decryption to fail with wrong key")
			}
		})
	}
}

func TestCBCPayloadHasMAC(t *testing.T) {
	key := []byte("0123456789abcdef")
	driver, _ := NewAESDriver(key, nil, "AES-128-CBC")

	encrypted, err := driver.Encrypt("test message")
	if err != nil {
		t.Fatalf("Encrypt() error: %v", err)
	}

	// Deserialize and check for MAC
	payload, err := deserializePayload(encrypted)
	if err != nil {
		t.Fatalf("deserializePayload() error: %v", err)
	}

	if payload.MAC == "" {
		t.Error("CBC encrypted payload should have MAC")
	}
	if payload.Tag != "" {
		t.Error("CBC encrypted payload should not have Tag")
	}
}

func TestGCMPayloadHasTag(t *testing.T) {
	key := []byte("0123456789abcdef")
	driver, _ := NewAESDriver(key, nil, "AES-128-GCM")

	encrypted, err := driver.Encrypt("test message")
	if err != nil {
		t.Fatalf("Encrypt() error: %v", err)
	}

	// Deserialize and check for Tag
	payload, err := deserializePayload(encrypted)
	if err != nil {
		t.Fatalf("deserializePayload() error: %v", err)
	}

	if payload.Tag == "" {
		t.Error("GCM encrypted payload should have Tag")
	}
	if payload.MAC != "" {
		t.Error("GCM encrypted payload should not have MAC")
	}
}
