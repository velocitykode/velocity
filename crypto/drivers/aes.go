// Package drivers implements the AES encryption wire format used by
// velocity's crypto.Encryptor.
//
// # Wire format versioning
//
// Encrypted payloads are emitted in one of two versions. The version is
// determined by a literal sentinel on the outer serialized string:
//
//	v1 (current): "v1:" + base64url(JSON{iv, value, mac|tag})
//	v0 (legacy):              base64url(JSON{iv, value, mac|tag})
//
// The colon character is not part of either the standard or URL base64
// alphabet, so a legacy v0 payload can never begin with the v1 sentinel.
// This keeps the two formats unambiguously distinguishable on decrypt.
//
// v1 and v0 differ only in how the CBC MAC is computed:
//
//	v1 MAC: HMAC-SHA256(hmacKey, "velocity\x00" || iv || ciphertext)
//	v0 MAC: HMAC-SHA256(hmacKey, "base64:"+base64(ciphertext)+"."+base64(iv))
//
// GCM-mode payloads share the same outer sentinel plumbing but the
// authenticated tag is cipher-provided, so v0 and v1 are decoded the same
// way once the sentinel is stripped. Adding the sentinel to GCM payloads
// keeps every ciphertext produced by this package self-describing under a
// single format rule.
//
// All payloads emitted by this package are v1. v0 is accepted on decrypt
// for one release cycle and will be removed in v2.0. When a v0 payload is
// decrypted successfully, a one-shot WARN is logged and a
// crypto.legacy_decrypt event is dispatched so operators can track the
// rotation window.
package drivers

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/hkdf"
)

// cryptoDebug controls whether the driver emits per-failure debug lines via
// stdlib log. Off by default to avoid noise; operators flip CRYPTO_DEBUG=true
// to trace decrypt errors without exposing them on the wire.
var cryptoDebug = os.Getenv("CRYPTO_DEBUG") == "true"

// debugDecryptFailure logs the underlying cause of a decrypt failure for
// operator-side debugging. The public API only ever returns ErrDecrypt to
// callers (so error messages cannot form a padding-oracle), but operators
// running with CRYPTO_DEBUG=true can see why a given payload was rejected.
// Keep the log line free of secret material (no key bytes, no plaintext).
func debugDecryptFailure(stage string, err error) {
	if !cryptoDebug {
		return
	}
	if err == nil {
		log.Printf("velocity/crypto: decrypt failed stage=%s", stage)
		return
	}
	log.Printf("velocity/crypto: decrypt failed stage=%s err=%v", stage, err)
}

// v1Sentinel marks payloads produced by the current (domain-separated MAC)
// wire format. The colon is unreachable in base64 output, so it can never
// collide with a legacy v0 payload.
const v1Sentinel = "v1:"

// AESDriver implements AES encryption with CBC and GCM modes
type AESDriver struct {
	key          []byte   // Primary encryption key (used for GCM which provides its own auth)
	hmacKey      []byte   // Derived HMAC key for CBC mode (separate from encryption key)
	previousKeys [][]byte // Previous keys for rotation
	cipher       string   // Cipher mode (AES-128-CBC, AES-256-CBC, AES-128-GCM, AES-256-GCM)
	keySize      int      // Key size in bytes

	// Event dispatcher wiring (mirrors the cache/queue/mail pattern).
	// mu guards eventDispatcher and legacyWarned.
	mu              sync.RWMutex
	eventDispatcher func(ctx context.Context, event interface{}) error
	legacyWarnOnce  sync.Once
}

// NewAESDriver creates a new AES driver. The supplied master key MUST have
// exactly the cipher's required raw byte length (AES-128 = 16, AES-192 = 24,
// AES-256 = 32). Shorter inputs are rejected with ErrInvalidKeyLength rather
// than silently stretched, because HKDF cannot manufacture entropy that is
// not present in the input; a 4-byte ASCII key (~32 bits of entropy) would
// still be brute-forceable in seconds regardless of how it is expanded.
//
// HKDF is still used internally as a domain separator, deriving distinct
// encryption and HMAC subkeys from the validated full-length master via
// distinct info strings.
func NewAESDriver(key []byte, previousKeys [][]byte, cipher string) (*AESDriver, error) {
	d := &AESDriver{
		previousKeys: previousKeys,
		cipher:       strings.ToUpper(cipher),
	}

	// Determine required key size. Only AES-128/192/256 are permitted.
	switch d.cipher {
	case "AES-128-CBC", "AES-128-GCM":
		d.keySize = 16
	case "AES-192-CBC", "AES-192-GCM":
		d.keySize = 24
	case "AES-256-CBC", "AES-256-GCM":
		d.keySize = 32
	default:
		return nil, fmt.Errorf("velocity/crypto: unsupported cipher: %s", cipher)
	}

	// Enforce raw key length against the cipher. Empty, undersized, and
	// oversized keys all reject through the same sentinel so callers can
	// branch on errors.Is(err, ErrInvalidKeyLength).
	if len(key) != d.keySize {
		return nil, fmt.Errorf("%w: cipher %s requires %d-byte key, got %d", ErrInvalidKeyLength, d.cipher, d.keySize, len(key))
	}

	// Validate previous keys with the same rule. A rotated-out key that
	// does not match the cipher's size is dropped rather than silently
	// accepted; bad entries here would mask a misconfigured rotation
	// window. The driver still accepts an empty PreviousKeys slice.
	validPrev := make([][]byte, 0, len(previousKeys))
	for _, pk := range previousKeys {
		if len(pk) != d.keySize {
			continue
		}
		validPrev = append(validPrev, pk)
	}
	d.previousKeys = validPrev

	// Derive separate encryption and HMAC subkeys from the validated master
	// via HKDF with distinct info strings. HKDF here is a subkey separator,
	// not a stretcher: input entropy already meets the cipher's required
	// length.
	encKey, err := deriveSubkey(key, d.keySize, []byte("encryption"))
	if err != nil {
		return nil, fmt.Errorf("velocity/crypto: failed to derive encryption key: %w", err)
	}
	hmacKey, err := deriveSubkey(key, 32, []byte("hmac"))
	if err != nil {
		return nil, fmt.Errorf("velocity/crypto: failed to derive hmac key: %w", err)
	}

	d.key = encKey
	d.hmacKey = hmacKey

	return d, nil
}

// hkdfSalt is a static salt for HKDF key derivation.
var hkdfSalt = []byte("velocity-framework-hkdf-salt-v1")

// deriveSubkey derives a subkey from a master key using HKDF-SHA256.
func deriveSubkey(master []byte, size int, info []byte) ([]byte, error) {
	r := hkdf.New(sha256.New, master, hkdfSalt, info)
	out := make([]byte, size)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

// SetEventDispatcher sets the function used to dispatch events. Mirrors the
// cache/mail/queue pattern so bootstrap wiring can plug velocity's events
// package in without the crypto package importing it.
func (d *AESDriver) SetEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.eventDispatcher = fn
}

// dispatchEvent dispatches an event if a dispatcher is configured.
// Crypto operations operate without a request-scoped ctx (encryption is
// CPU-bound and not request-bound), so callers pass context.Background()
// here. Listeners that need a real ctx should plumb their own.
func (d *AESDriver) dispatchEvent(event interface{}) {
	d.mu.RLock()
	fn := d.eventDispatcher
	d.mu.RUnlock()
	if fn == nil {
		return
	}
	// Dispatcher is called inline; mirrors the cache package which does
	// not spawn a goroutine here. Listeners that need async behaviour can
	// opt in via queued listeners.
	_ = fn(context.Background(), event)
}

// Encrypt encrypts plaintext
func (d *AESDriver) Encrypt(plaintext string) (string, error) {
	return d.EncryptBytes([]byte(plaintext))
}

// EncryptBytes encrypts bytes
func (d *AESDriver) EncryptBytes(plaintext []byte) (string, error) {
	if strings.Contains(d.cipher, "GCM") {
		return d.encryptGCM(plaintext)
	}
	return d.encryptCBC(plaintext)
}

// Decrypt decrypts a payload
func (d *AESDriver) Decrypt(payload string) (string, error) {
	data, err := d.DecryptBytes(payload)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// DecryptBytes decrypts a payload to bytes. Accepts both v1 (current
// domain-separated MAC) and v0 (legacy fmt-concatenated MAC) payloads.
//
// Returns ErrInvalidPayload only for structural envelope problems (empty
// input, non-base64 outer, malformed JSON). Every cryptographic failure
// (wrong MAC, wrong key, bad padding, malformed IV bytes, etc.) collapses
// to ErrDecrypt so callers cannot distinguish between them via the error
// message; the variants would otherwise form a padding-oracle precursor if
// the message were ever forwarded to a client. Callers MUST NOT include
// the error message in user-visible output; branch on errors.Is.
func (d *AESDriver) DecryptBytes(payload string) ([]byte, error) {
	if payload == "" {
		return nil, ErrInvalidPayload
	}

	version, envelope := splitVersion(payload)

	// Parse the inner base64+JSON envelope.
	p, err := deserializePayload(envelope)
	if err != nil {
		return nil, err
	}

	// Try current key first (already-derived enc + hmac subkeys)
	plaintext, err := d.decryptWithKeys(p, d.key, d.hmacKey, version)
	if err == nil {
		d.noteLegacyIfV0(version)
		return plaintext, nil
	}

	// Try previous keys for rotation support (derive subkeys directly from each master key)
	for _, masterKey := range d.previousKeys {
		encKey, ekErr := deriveSubkey(masterKey, d.keySize, []byte("encryption"))
		hk, hkErr := deriveSubkey(masterKey, 32, []byte("hmac"))
		if ekErr != nil || hkErr != nil {
			continue
		}
		plaintext, err = d.decryptWithKeys(p, encKey, hk, version)
		if err == nil {
			d.noteLegacyIfV0(version)
			return plaintext, nil
		}
	}

	debugDecryptFailure("decrypt-all-keys", err)
	return nil, ErrDecrypt
}

// splitVersion peeks the payload's leading sentinel and returns (version,
// inner envelope). The version byte / sentinel is not secret, so direct
// branching is fine (and preferable to feeding attacker-controlled prefix
// bytes into the MAC path).
func splitVersion(payload string) (version int, envelope string) {
	if strings.HasPrefix(payload, v1Sentinel) {
		return 1, payload[len(v1Sentinel):]
	}
	return 0, payload
}

// noteLegacyIfV0 emits the one-shot WARN log and the crypto.legacy_decrypt
// event the first time a v0 payload successfully decrypts. Only fires once
// per Encryptor instance regardless of how many v0 payloads flow through.
func (d *AESDriver) noteLegacyIfV0(version int) {
	if version != 0 {
		return
	}
	d.legacyWarnOnce.Do(func() {
		log.Print("velocity/crypto: legacy v0 payload decrypted, rotate before v2.0")
	})
	// Dispatch every time so operators can count/alert on the stream.
	// The once-per-instance log is about noise, not signal.
	d.dispatchEvent(&LegacyDecryptEvent{
		Cipher: d.cipher,
		At:     time.Now().UTC(),
	})
}

// LegacyDecryptEvent is dispatched each time a v0 payload is decrypted.
// Operators can count these to gauge how much pre-versioned ciphertext
// remains before upgrading to v2.0 (which drops v0 support).
type LegacyDecryptEvent struct {
	Cipher string    // e.g. "AES-256-CBC"
	At     time.Time // when the decrypt happened (UTC)
}

// Name returns the event name.
func (e *LegacyDecryptEvent) Name() string { return "crypto.legacy_decrypt" }

// GenerateKey generates a new encryption key
func (d *AESDriver) GenerateKey() (string, error) {
	key := make([]byte, d.keySize)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return "", err
	}
	return "base64:" + base64.StdEncoding.EncodeToString(key), nil
}

// EncryptBytesWithAAD encrypts plaintext under the GCM tag with aad bound
// in. CBC ciphers return ErrInvalidCipher (no AEAD). aad is not persisted
// in the payload; the caller supplies the same aad on decrypt.
func (d *AESDriver) EncryptBytesWithAAD(plaintext, aad []byte) (string, error) {
	if !strings.Contains(d.cipher, "GCM") {
		return "", ErrInvalidCipher
	}
	return d.encryptGCMWithAAD(plaintext, aad)
}

// DecryptBytesWithAAD decrypts an AAD-bound GCM payload. See the
// crypto.Encryptor interface doc for full semantics. CBC ciphers return
// ErrInvalidCipher. Empty or non-v1 envelopes return ErrInvalidPayload.
// Any GCM auth failure returns ErrAADMismatch (cannot distinguish wrong
// key, wrong aad, tamper, or AAD-vs-no-AAD payload mixing).
//
// Key rotation via PreviousKeys is intentionally not iterated here: the
// spec scopes rotation to the non-AAD path. Callers that need to rotate
// AAD-bound ciphertexts must re-encrypt explicitly.
func (d *AESDriver) DecryptBytesWithAAD(payload string, aad []byte) ([]byte, error) {
	if !strings.Contains(d.cipher, "GCM") {
		return nil, ErrInvalidCipher
	}
	if payload == "" {
		return nil, ErrInvalidPayload
	}

	// AAD path is net-new: only v1 envelopes can have been produced by
	// EncryptBytesWithAAD. Reject legacy v0 explicitly so a stray pre-v1
	// payload does not surface as a fake AAD mismatch.
	version, envelope := splitVersion(payload)
	if version != 1 {
		return nil, ErrInvalidPayload
	}

	p, err := deserializePayload(envelope)
	if err != nil {
		return nil, err
	}

	plaintext, err := d.decryptGCMWithAAD(p, d.key, aad)
	if err != nil {
		return nil, ErrAADMismatch
	}
	return plaintext, nil
}

// encryptGCMWithAAD encrypts using GCM with additional authenticated data.
// Wire format is identical to encryptGCM (the AAD is not stored).
func (d *AESDriver) encryptGCMWithAAD(plaintext, aad []byte) (string, error) {
	block, err := aes.NewCipher(d.key)
	if err != nil {
		return "", err
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	ciphertext := gcm.Seal(nil, nonce, plaintext, aad)

	tagStart := len(ciphertext) - gcm.Overhead()
	tag := ciphertext[tagStart:]
	ciphertext = ciphertext[:tagStart]

	p := &Payload{
		IV:    base64.StdEncoding.EncodeToString(nonce),
		Value: base64.StdEncoding.EncodeToString(ciphertext),
		Tag:   base64.StdEncoding.EncodeToString(tag),
	}

	env, err := serializePayload(p)
	if err != nil {
		return "", err
	}
	return v1Sentinel + env, nil
}

// decryptGCMWithAAD decrypts a GCM payload using the supplied key and aad.
// Returns the raw error from gcm.Open so the caller can map it.
//
// Note on nonce length validation: crypto/cipher.gcm.Open panics when
// len(nonce) != gcm.NonceSize(). An attacker can hit that branch by
// supplying a payload whose IV base64-decodes to the wrong length
// (e.g. empty IV). We validate before calling Open so a malformed
// payload surfaces as ErrAADMismatch upstream rather than a panic
// that takes the process down.
func (d *AESDriver) decryptGCMWithAAD(p *Payload, key, aad []byte) ([]byte, error) {
	nonce, err := base64.StdEncoding.DecodeString(p.IV)
	if err != nil {
		return nil, err
	}
	ciphertext, err := base64.StdEncoding.DecodeString(p.Value)
	if err != nil {
		return nil, err
	}
	tag, err := base64.StdEncoding.DecodeString(p.Tag)
	if err != nil {
		return nil, err
	}
	ciphertext = append(ciphertext, tag...)

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(nonce) != gcm.NonceSize() {
		return nil, ErrInvalidPayload
	}
	return gcm.Open(nil, nonce, ciphertext, aad)
}

// encryptCBC encrypts using CBC mode (v1 wire format).
func (d *AESDriver) encryptCBC(plaintext []byte) (string, error) {
	// Create cipher block
	block, err := aes.NewCipher(d.key)
	if err != nil {
		return "", fmt.Errorf("velocity/crypto: %w", err)
	}

	// Generate IV
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", fmt.Errorf("velocity/crypto: failed to read iv: %w", err)
	}

	// Pad plaintext to block size
	plaintext = pkcs7Pad(plaintext, aes.BlockSize)

	// Encrypt
	mode := cipher.NewCBCEncrypter(block, iv)
	ciphertext := make([]byte, len(plaintext))
	mode.CryptBlocks(ciphertext, plaintext)

	// Generate MAC for integrity over the raw (unencoded) IV and ciphertext,
	// using domain-separated binary writes instead of string formatting.
	mac := d.computeMAC(iv, ciphertext)

	// Create payload
	p := &Payload{
		IV:    base64.StdEncoding.EncodeToString(iv),
		Value: base64.StdEncoding.EncodeToString(ciphertext),
		MAC:   mac,
	}

	env, err := serializePayload(p)
	if err != nil {
		return "", err
	}
	return v1Sentinel + env, nil
}

// encryptGCM encrypts using GCM mode (v1 wire format).
func (d *AESDriver) encryptGCM(plaintext []byte) (string, error) {
	// Create cipher block
	block, err := aes.NewCipher(d.key)
	if err != nil {
		return "", err
	}

	// Create GCM
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}

	// Generate nonce
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}

	// Encrypt and authenticate
	ciphertext := gcm.Seal(nil, nonce, plaintext, nil)

	// Extract authentication tag (last 16 bytes)
	tagStart := len(ciphertext) - gcm.Overhead()
	tag := ciphertext[tagStart:]
	ciphertext = ciphertext[:tagStart]

	// Create payload
	p := &Payload{
		IV:    base64.StdEncoding.EncodeToString(nonce),
		Value: base64.StdEncoding.EncodeToString(ciphertext),
		Tag:   base64.StdEncoding.EncodeToString(tag),
	}

	env, err := serializePayload(p)
	if err != nil {
		return "", err
	}
	return v1Sentinel + env, nil
}

// decryptWithKeys attempts to decrypt with specific encryption and HMAC keys.
// The version selects which MAC framing to verify for CBC; GCM is
// version-independent (its tag is cipher-provided).
func (d *AESDriver) decryptWithKeys(p *Payload, encKey, hmacKey []byte, version int) ([]byte, error) {
	if strings.Contains(d.cipher, "GCM") {
		return d.decryptGCMWithKey(p, encKey)
	}
	return d.decryptCBCWithKey(p, encKey, hmacKey, version)
}

// decryptCBCWithKey decrypts CBC mode with separate encryption and HMAC keys.
// version == 1 uses the domain-separated MAC; version == 0 uses the
// pre-sweep fmt-concatenated MAC for backwards compatibility.
//
// Every failure returns ErrDecrypt (single sentinel). The actual cause is
// surfaced via debugDecryptFailure(stage, err) for operator-side
// debugging only. This collapse is deliberate: six distinct error strings
// reachable from a single payload form a padding-oracle precursor if any
// caller forwards the error message back to a client.
func (d *AESDriver) decryptCBCWithKey(p *Payload, encKey, hmacKey []byte, version int) ([]byte, error) {
	// MAC is required for CBC decryption to ensure integrity
	if p.MAC == "" {
		debugDecryptFailure("cbc-mac-missing", nil)
		return nil, ErrDecrypt
	}

	// Decode components BEFORE MAC verification so we can compute the MAC
	// over the raw bytes (the v1 wire format hashes raw IV+ciphertext with a
	// domain-separation prefix; v0 hashes the base64 strings).
	iv, err := base64.StdEncoding.DecodeString(p.IV)
	if err != nil {
		debugDecryptFailure("cbc-iv-decode", err)
		return nil, ErrDecrypt
	}
	ciphertext, err := base64.StdEncoding.DecodeString(p.Value)
	if err != nil {
		debugDecryptFailure("cbc-value-decode", err)
		return nil, ErrDecrypt
	}

	var expectedMAC string
	switch version {
	case 1:
		expectedMAC = computeMACWith(iv, ciphertext, hmacKey)
	case 0:
		expectedMAC = computeLegacyMACWith(p.Value, p.IV, hmacKey)
	default:
		debugDecryptFailure("cbc-version-unsupported", nil)
		return nil, ErrDecrypt
	}
	if !secureCompare(p.MAC, expectedMAC) {
		debugDecryptFailure("cbc-mac-mismatch", nil)
		return nil, ErrDecrypt
	}

	// IV and ciphertext shape must be validated before they reach
	// cipher.NewCBCDecrypter / CryptBlocks; both panic on misaligned
	// input. For v1 payloads the MAC check above implies these lengths
	// (the MAC covers raw bytes and only the framework's encryptCBC
	// produces inputs), but the v0 framing hashes the base64 strings, so
	// an attacker can hand-craft a v0 payload with an empty IV or a
	// non-block-aligned ciphertext and still pass MAC verification. The
	// length checks here close that DoS vector explicitly.
	if len(iv) != aes.BlockSize {
		debugDecryptFailure("cbc-iv-length", nil)
		return nil, ErrDecrypt
	}
	if len(ciphertext) == 0 || len(ciphertext)%aes.BlockSize != 0 {
		debugDecryptFailure("cbc-ct-length", nil)
		return nil, ErrDecrypt
	}

	// Create cipher block
	block, err := aes.NewCipher(encKey)
	if err != nil {
		debugDecryptFailure("cbc-new-cipher", err)
		return nil, ErrDecrypt
	}

	// Decrypt
	mode := cipher.NewCBCDecrypter(block, iv)
	plaintext := make([]byte, len(ciphertext))
	mode.CryptBlocks(plaintext, ciphertext)

	// Remove padding
	plaintext, err = pkcs7Unpad(plaintext)
	if err != nil {
		debugDecryptFailure("cbc-unpad", err)
		return nil, ErrDecrypt
	}

	return plaintext, nil
}

// decryptGCMWithKey decrypts GCM mode with a specific key.
//
// crypto/cipher.gcm.Open panics on a nonce of the wrong length. An
// attacker who can submit a cookie whose payload IV decodes to a
// non-standard length would otherwise crash the process via a single
// HTTP request. We validate length explicitly so a malformed payload
// surfaces as ErrDecrypt upstream instead.
//
// All cryptographic failures collapse to ErrDecrypt so distinct
// per-stage error messages do not form an oracle if the error reaches a
// client. The variant is captured via debugDecryptFailure for operator
// logs.
func (d *AESDriver) decryptGCMWithKey(p *Payload, key []byte) ([]byte, error) {
	// Decode components
	nonce, err := base64.StdEncoding.DecodeString(p.IV)
	if err != nil {
		debugDecryptFailure("gcm-iv-decode", err)
		return nil, ErrDecrypt
	}

	ciphertext, err := base64.StdEncoding.DecodeString(p.Value)
	if err != nil {
		debugDecryptFailure("gcm-value-decode", err)
		return nil, ErrDecrypt
	}

	tag, err := base64.StdEncoding.DecodeString(p.Tag)
	if err != nil {
		debugDecryptFailure("gcm-tag-decode", err)
		return nil, ErrDecrypt
	}

	// Append tag to ciphertext for GCM
	ciphertext = append(ciphertext, tag...)

	// Create cipher block
	block, err := aes.NewCipher(key)
	if err != nil {
		debugDecryptFailure("gcm-new-cipher", err)
		return nil, ErrDecrypt
	}

	// Create GCM
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		debugDecryptFailure("gcm-new-gcm", err)
		return nil, ErrDecrypt
	}

	if len(nonce) != gcm.NonceSize() {
		debugDecryptFailure("gcm-nonce-length", nil)
		return nil, ErrDecrypt
	}

	// Decrypt and verify
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		debugDecryptFailure("gcm-open", err)
		return nil, ErrDecrypt
	}

	return plaintext, nil
}

// macDomainPrefix is a domain-separation tag bound to the framework and
// the current wire-format version. The trailing NUL byte guarantees the
// prefix is never confused with IV bytes in HMAC input.
var macDomainPrefix = []byte("velocity\x00")

// computeMAC generates HMAC for CBC mode using the derived HMAC key,
// over domain-separated raw bytes: prefix || iv || ciphertext.
func (d *AESDriver) computeMAC(iv, ct []byte) string {
	return computeMACWith(iv, ct, d.hmacKey)
}

// computeMACWith generates the v1 HMAC with the provided HMAC key, using
// domain-separated writes rather than string concatenation.
func computeMACWith(iv, ct, hmacKey []byte) string {
	mac := hmac.New(sha256.New, hmacKey)
	mac.Write(macDomainPrefix)
	mac.Write(iv)
	mac.Write(ct)
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// computeLegacyMACWith reproduces the pre-sweep MAC computation so that
// existing v0 ciphertexts (cookies, signed URLs, encrypted DB columns) can
// still be decrypted during the migration window. The format is
// HMAC-SHA256(hmacKey, "base64:"+valueB64+"."+ivB64), matching the
// fmt.Sprintf("base64:%s.%s", value, iv) concatenation used before the
// domain-separated sweep.
//
// This path MUST be kept in sync with the pre-sweep code exactly: value
// comes first, then iv. See git history for crypto/drivers/aes.go prior
// to commit 03152c3 for the original implementation.
func computeLegacyMACWith(valueB64, ivB64 string, hmacKey []byte) string {
	mac := hmac.New(sha256.New, hmacKey)
	// Equivalent to fmt.Sprintf("base64:%s.%s", value, iv) but without the
	// fmt package; value is written before iv to match the legacy order.
	mac.Write([]byte("base64:"))
	mac.Write([]byte(valueB64))
	mac.Write([]byte("."))
	mac.Write([]byte(ivB64))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// pkcs7Pad adds PKCS#7 padding
func pkcs7Pad(data []byte, blockSize int) []byte {
	padding := blockSize - len(data)%blockSize
	padtext := make([]byte, padding)
	for i := range padtext {
		padtext[i] = byte(padding)
	}
	return append(data, padtext...)
}

// pkcs7Unpad removes PKCS#7 padding
func pkcs7Unpad(data []byte) ([]byte, error) {
	if len(data) == 0 {
		return nil, errors.New("velocity/crypto: invalid padding")
	}
	padding := int(data[len(data)-1])
	if padding == 0 || padding > aes.BlockSize || padding > len(data) {
		return nil, errors.New("velocity/crypto: invalid padding")
	}
	// Verify all padding bytes match using constant-time comparison
	expected := make([]byte, padding)
	for i := range expected {
		expected[i] = byte(padding)
	}
	if subtle.ConstantTimeCompare(data[len(data)-padding:], expected) != 1 {
		return nil, errors.New("velocity/crypto: invalid padding")
	}
	return data[:len(data)-padding], nil
}

// secureCompare performs constant-time string comparison
func secureCompare(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// Payload represents the encrypted data structure
type Payload struct {
	IV    string `json:"iv"`
	Value string `json:"value"`
	MAC   string `json:"mac,omitempty"`
	Tag   string `json:"tag,omitempty"`
}

// serializePayload converts a payload to base64 JSON
func serializePayload(p *Payload) (string, error) {
	data, err := json.Marshal(p)
	if err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(data), nil
}

// deserializePayload converts base64 JSON to a payload. Accepts both v1
// ("v1:"-prefixed) and v0 (bare base64) envelopes. The inner parser does
// not need to know which version it is; that information is used by the
// caller to select the correct MAC verifier.
//
// Returns ErrInvalidPayload for structural failures (non-base64 outer,
// non-JSON inner). This is distinct from ErrDecrypt: callers that wish
// to distinguish "bad envelope shape" from "wrong key / tampered" can
// branch on the two sentinels.
func deserializePayload(encoded string) (*Payload, error) {
	encoded = strings.TrimPrefix(encoded, v1Sentinel)

	// Try URL encoding first, then standard encoding
	data, err := base64.URLEncoding.DecodeString(encoded)
	if err != nil {
		data, err = base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, ErrInvalidPayload
		}
	}

	var p Payload
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, ErrInvalidPayload
	}

	return &p, nil
}
