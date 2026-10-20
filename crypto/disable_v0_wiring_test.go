package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"golang.org/x/crypto/hkdf"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	logdrivers "github.com/velocitykode/velocity/log/drivers"
)

const v0TestKey = "0123456789abcdef"

// legacyV0Payload re-frames a v1 CBC payload from enc as a v0 payload: same
// iv and ciphertext, the legacy MAC over the base64 fields, no sentinel.
// The HMAC subkey is derived the way crypto/drivers derives it.
func legacyV0Payload(t *testing.T, enc Encryptor, plaintext string) string {
	t.Helper()
	v1, err := enc.Encrypt(plaintext)
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	data, err := base64.URLEncoding.DecodeString(strings.TrimPrefix(v1, "v1:"))
	if err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	var p Payload
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatalf("unmarshal envelope: %v", err)
	}
	hmacKey := make([]byte, 32)
	if _, err := io.ReadFull(hkdf.New(sha256.New, []byte(v0TestKey), []byte("velocity-framework-hkdf-salt-v1"), []byte("hmac")), hmacKey); err != nil {
		t.Fatalf("derive hmac key: %v", err)
	}
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write([]byte("base64:" + p.Value + "." + p.IV))
	p.MAC = base64.StdEncoding.EncodeToString(mac.Sum(nil))
	out, err := json.Marshal(&p)
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return base64.URLEncoding.EncodeToString(out)
}

func newV0TestEncryptor(t *testing.T, cfg Config) Encryptor {
	t.Helper()
	cfg.Key, cfg.Cipher = v0TestKey, "AES-128-CBC"
	enc, err := NewEncryptor(cfg)
	if err != nil {
		t.Fatalf("NewEncryptor: %v", err)
	}
	// The legacy-decrypt warning goes to a discarded logger.
	if la, ok := enc.(contract.LoggerAware); ok {
		la.SetLogger(logdrivers.NewConsoleLoggerTo(&fallbacklogtest.Output{}, 0))
	}
	return enc
}

// Config.DisableV0 reaches the driver NewEncryptor builds: on, a v0
// payload is ErrLegacyPayloadDisabled; off, it decrypts.
func TestNewEncryptor_DisableV0ReachesDriver(t *testing.T) {
	payload := legacyV0Payload(t, newV0TestEncryptor(t, Config{}), "classified")

	if got, err := newV0TestEncryptor(t, Config{DisableV0: false}).Decrypt(payload); err != nil || got != "classified" {
		t.Fatalf("DisableV0=false: Decrypt = (%q, %v), want (classified, nil)", got, err)
	}
	_, err := newV0TestEncryptor(t, Config{DisableV0: true}).Decrypt(payload)
	if !errors.Is(err, ErrLegacyPayloadDisabled) {
		t.Fatalf("DisableV0=true: Decrypt error = %v, want ErrLegacyPayloadDisabled", err)
	}
}

// Config.Debug reaches the driver: a failed decrypt writes debug lines
// through the driver's logger only when it is set.
func TestNewEncryptor_DebugReachesDriver(t *testing.T) {
	sealed, err := newV0TestEncryptor(t, Config{}).Encrypt("secret")
	if err != nil {
		t.Fatalf("Encrypt: %v", err)
	}
	for _, debug := range []bool{true, false} {
		enc, err := NewEncryptor(Config{Key: "fedcba9876543210", Cipher: "AES-128-CBC", Debug: debug})
		if err != nil {
			t.Fatalf("NewEncryptor: %v", err)
		}
		la, ok := enc.(contract.LoggerAware)
		if !ok {
			t.Fatalf("encryptor %T has no SetLogger", enc)
		}
		out := &fallbacklogtest.Output{}
		la.SetLogger(logdrivers.NewConsoleLoggerTo(out, 0))
		if _, err := enc.Decrypt(sealed); err == nil {
			t.Fatal("Decrypt under the wrong key succeeded")
		}
		lines := strings.Count(out.String(), "DEBUG: velocity/crypto: decrypt failed")
		if debug && lines == 0 {
			t.Errorf("Debug=true: no decrypt-failure debug line (%q)", out.String())
		}
		if !debug && lines != 0 {
			t.Errorf("Debug=false: %d debug lines, want 0 (%q)", lines, out.String())
		}
	}
}
