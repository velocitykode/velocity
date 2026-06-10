package queue

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"

	"github.com/velocitykode/velocity/contract"
)

// Payload encryption (opt-in via QUEUE_ENCRYPT=true) protects job state at
// rest. Signing (signing.go) gives integrity only: a signed payload is still
// stored as plaintext JSON in the jobs / failed_jobs tables and Redis lists,
// so reset tokens, PII, and other job state are readable by anyone with
// store access, and failed_jobs rows keep them indefinitely. When an
// encryptor is installed, [sealPayload] encrypts Payload.Data (the
// job-state JSON) before the payload is persisted and [openPayload]
// decrypts it after the integrity check on pop.
//
// Only Data is encrypted. The envelope metadata the drivers need for
// routing and bookkeeping (Type, Queue, Attempts, CreatedAt, trace ids,
// DedupeKey) stays readable. The payload Type is bound into the
// ciphertext as AAD (mirroring the flash-cookie pattern in
// router.SealFlash) so a ciphertext cannot be transplanted onto a
// different job type. That binding is mandatory: queue encryption
// requires an AEAD cipher (AES-GCM). Non-AEAD (CBC) ciphers reject AAD
// with ErrInvalidCipher and sealing fails closed rather than degrading
// to an unbound EncryptBytes envelope, because without the type binding
// a valid ciphertext for one job type could be replayed as the Data of
// another. (router.SealFlash accepts that degradation for flash cookies;
// queue payloads do not.)
//
// Ordering: encrypt-then-sign. Producers seal Data BEFORE the wrapper is
// marshalled and signed, so the HMAC covers the ciphertext (and the
// Encrypted flag). Consumers verify the signature first and only then
// decrypt, so verification never runs on undecrypted attacker-controlled
// bytes and a tampered ciphertext is quarantined by the signature check
// before the decryptor ever sees it. With signing disabled, the AEAD
// authentication built into the encryptor is the only integrity layer;
// decryption failure quarantines the payload.
//
// Deploy transition: payloads enqueued before QUEUE_ENCRYPT was turned on
// are plaintext (Encrypted=false). They are accepted ONLY when they carry
// a signature that already verified; fail-closed otherwise, because with
// encryption on an unsigned plaintext payload is indistinguishable from an
// attacker writing directly to the store. This keeps the one-deploy window
// safe: jobs in flight during the flip were signed by the old fleet and
// drain normally; after they drain every live payload is ciphertext. The
// reverse flip (turning encryption OFF with ciphertext in flight) is not
// supported: encrypted payloads fail hydration until the encryptor is
// restored.
//
// The memory driver skips encryption entirely: its queue lives in process
// memory and dies with the process, so there is no at-rest exposure to
// protect, and its same-process fast path hands the live Job pointer back
// without ever re-reading Payload.Data. Encrypting there would burn CPU
// for no security gain.
var (
	encryptionMu     sync.RWMutex
	payloadEncryptor contract.Encryptor
)

// SetPayloadEncryptor installs the encryptor used to seal Payload.Data at
// rest. Nil disables payload encryption. Called from the framework boot
// path (initQueue) when QUEUE_ENCRYPT=true; safe to call concurrently.
func SetPayloadEncryptor(enc contract.Encryptor) {
	encryptionMu.Lock()
	defer encryptionMu.Unlock()
	payloadEncryptor = enc
}

// IsEncryptionEnabled reports whether a payload encryptor is installed.
func IsEncryptionEnabled() bool {
	encryptionMu.RLock()
	defer encryptionMu.RUnlock()
	return payloadEncryptor != nil
}

// payloadAAD returns the additional authenticated data binding a sealed
// payload to its job type, so a valid ciphertext for one job type cannot
// be replayed as the Data of another.
func payloadAAD(jobType string) []byte {
	return []byte("velocity.queue.payload." + jobType)
}

// quarantineAAD binds sealed quarantine blobs to the quarantine context.
// It is deliberately distinct from every payloadAAD value so a quarantined
// ciphertext can never be replayed as a live payload's Data and a live
// ciphertext can never masquerade as a quarantine record.
var quarantineAAD = []byte("velocity.queue.quarantine")

// sealQuarantineBlob protects raw poison-payload bytes before they are
// persisted to failed-job storage (failed_jobs rows, Redis failed lists).
// Poison payloads are attacker-shaped by definition, but they can also be
// legitimate jobs mangled in flight, so their bytes get the same at-rest
// confidentiality as sealed payloads.
//
// Returns (blob, sealed): with no encryptor installed the raw bytes pass
// through unchanged (sealed=false, current default behaviour). With an
// encryptor installed the blob is the AAD-bound ciphertext envelope. If
// sealing itself fails, the raw bytes are NOT persisted; a hash-only
// forensic stub is returned instead, because plaintext must never reach
// failed storage while encryption is on.
func sealQuarantineBlob(raw string) (string, bool) {
	encryptionMu.RLock()
	enc := payloadEncryptor
	encryptionMu.RUnlock()

	if enc == nil {
		return raw, false
	}
	sealed, err := enc.EncryptBytesWithAAD([]byte(raw), quarantineAAD)
	if err != nil {
		sum := sha256.Sum256([]byte(raw))
		return fmt.Sprintf(
			"velocity/queue: poison payload redacted (quarantine seal failed: %v); sha256=%s len=%d",
			err, hex.EncodeToString(sum[:]), len(raw),
		), true
	}
	return sealed, true
}

// openQuarantineBlob reverses sealQuarantineBlob for operator tooling and
// tests. It fails when no encryptor is installed or the blob is a redacted
// stub rather than ciphertext.
func openQuarantineBlob(blob string) ([]byte, error) {
	encryptionMu.RLock()
	enc := payloadEncryptor
	encryptionMu.RUnlock()

	if enc == nil {
		return nil, errors.New("velocity/queue: no payload encryptor installed")
	}
	return enc.DecryptBytesWithAAD(blob, quarantineAAD)
}

// sealPayload encrypts p.Data in place when a payload encryptor is
// installed, marking the payload via p.Encrypted. The ciphertext envelope
// (a base64 string) is stored as a JSON string so Data remains valid
// json.RawMessage. No-op when encryption is disabled, p is nil, or the
// payload is already sealed.
//
// Callers MUST seal before computing the payload signature so the HMAC
// covers the ciphertext (encrypt-then-sign; see the package comment above).
func sealPayload(p *Payload) error {
	encryptionMu.RLock()
	enc := payloadEncryptor
	encryptionMu.RUnlock()

	if enc == nil || p == nil || p.Encrypted {
		return nil
	}

	sealed, err := enc.EncryptBytesWithAAD(p.Data, payloadAAD(p.Type))
	if err != nil {
		// Fail closed on non-AEAD ciphers: without AAD the ciphertext is
		// not bound to the job type, so a sealed Data blob could be
		// replayed under a different job type. No EncryptBytes fallback.
		if errors.Is(err, contract.ErrInvalidCipher) {
			return fmt.Errorf("velocity/queue: payload encryption requires an AEAD cipher (AES-GCM) to bind ciphertext to the job type; set CRYPTO_CIPHER to a GCM cipher or disable QUEUE_ENCRYPT: %w", err)
		}
		return fmt.Errorf("velocity/queue: failed to encrypt payload data: %w", err)
	}

	quoted, err := json.Marshal(sealed)
	if err != nil {
		return fmt.Errorf("velocity/queue: failed to encode encrypted payload data: %w", err)
	}
	p.Data = quoted
	p.Encrypted = true
	return nil
}

// openPayload decrypts p.Data in place after the driver's integrity check.
// signatureVerified must be true only when the payload carried a real
// signature that verified (not when signing is disabled and the payload
// was simply unsigned); it gates the legacy-plaintext transition path.
//
// Behaviour matrix:
//   - p.Encrypted && encryptor installed: decrypt (AAD-bound, AEAD
//     only, mirroring sealPayload). Decryption failure is returned to
//     the caller for quarantine: fail closed, never a plaintext parse of
//     ciphertext bytes.
//   - p.Encrypted && no encryptor: error. Encryption was turned off (or
//     never configured on this worker) while ciphertext is in flight.
//   - !p.Encrypted && encryption on: legacy plaintext from before the
//     QUEUE_ENCRYPT flip. Accepted only when signatureVerified; rejected
//     otherwise so a store-writing attacker cannot smuggle plaintext past
//     an encrypting fleet by omitting the Encrypted flag.
//   - !p.Encrypted && encryption off: no-op (current default behaviour).
func openPayload(p *Payload, signatureVerified bool) error {
	encryptionMu.RLock()
	enc := payloadEncryptor
	encryptionMu.RUnlock()

	if p == nil {
		return nil
	}

	if !p.Encrypted {
		if enc != nil && !signatureVerified {
			return fmt.Errorf("velocity/queue: plaintext payload rejected: payload encryption is enabled and the payload carries no verified signature")
		}
		return nil
	}

	if enc == nil {
		return fmt.Errorf("velocity/queue: payload is encrypted but no payload encryptor is configured (was QUEUE_ENCRYPT turned off with ciphertext in flight?)")
	}

	var envelope string
	if err := json.Unmarshal(p.Data, &envelope); err != nil {
		return fmt.Errorf("velocity/queue: malformed encrypted payload envelope: %w", err)
	}

	plaintext, err := enc.DecryptBytesWithAAD(envelope, payloadAAD(p.Type))
	if err != nil {
		// Fail closed: sealPayload only ever produces AAD-bound envelopes,
		// so any failure here (non-AEAD cipher, AEAD auth, wrong key,
		// malformed envelope) is surfaced for quarantine. No DecryptBytes
		// fallback that would accept ciphertext unbound from the job type.
		return fmt.Errorf("velocity/queue: failed to decrypt payload data: %w", err)
	}

	p.Data = plaintext
	p.Encrypted = false
	return nil
}
