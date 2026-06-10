package queue

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
)

// saveAndRestoreEncryptionState snapshots the package-level payload
// encryptor and restores it on cleanup, so tests that flip encryption on
// cannot leak it into tests that assume plaintext payloads.
func saveAndRestoreEncryptionState(t *testing.T) {
	t.Helper()

	encryptionMu.RLock()
	orig := payloadEncryptor
	encryptionMu.RUnlock()

	t.Cleanup(func() {
		SetPayloadEncryptor(orig)
	})
}

// newTestEncryptor returns a deterministic AES-256 encryptor for the given
// cipher mode. The key is hard-coded so seal/open round trips are stable
// across runs.
func newTestEncryptor(t *testing.T, cipher string) contract.Encryptor {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    "base64:MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		Cipher: cipher,
	})
	if err != nil {
		t.Fatalf("newTestEncryptor(%s): %v", cipher, err)
	}
	return enc
}

func TestSealOpenPayload_RoundTrip(t *testing.T) {
	saveAndRestoreEncryptionState(t)
	SetPayloadEncryptor(newTestEncryptor(t, "AES-256-GCM"))

	job := &TestJob{ID: "enc-1", Message: "super-secret-token"}
	payload, err := MarshalJob(job, "default")
	if err != nil {
		t.Fatalf("MarshalJob: %v", err)
	}
	original := string(payload.Data)

	if err := sealPayload(payload); err != nil {
		t.Fatalf("sealPayload: %v", err)
	}
	if !payload.Encrypted {
		t.Fatal("sealPayload must set Encrypted")
	}
	if strings.Contains(string(payload.Data), "super-secret-token") {
		t.Fatal("sealed Data still contains plaintext job state")
	}
	// Envelope metadata stays readable for routing.
	if payload.Type != "TestJob" || payload.Queue != "default" {
		t.Fatalf("envelope metadata mutated: type=%q queue=%q", payload.Type, payload.Queue)
	}

	if err := openPayload(payload, true); err != nil {
		t.Fatalf("openPayload: %v", err)
	}
	if payload.Encrypted {
		t.Fatal("openPayload must clear Encrypted")
	}
	if string(payload.Data) != original {
		t.Fatalf("round trip mismatch: got %s want %s", payload.Data, original)
	}
}

// TestSealPayload_CBCRejected exercises the non-AEAD path: CBC ciphers
// reject AAD, and sealing must fail closed rather than fall back to an
// EncryptBytes envelope that is not bound to the job type (which would
// re-open the cross-job-type ciphertext swap AAD exists to block).
func TestSealPayload_CBCRejected(t *testing.T) {
	saveAndRestoreEncryptionState(t)
	SetPayloadEncryptor(newTestEncryptor(t, "AES-256-CBC"))

	job := &TestJob{ID: "enc-cbc", Message: "cbc-secret"}
	payload, err := MarshalJob(job, "default")
	if err != nil {
		t.Fatalf("MarshalJob: %v", err)
	}
	before := string(payload.Data)

	err = sealPayload(payload)
	if err == nil {
		t.Fatal("sealPayload must fail closed on a non-AEAD cipher")
	}
	if !errors.Is(err, contract.ErrInvalidCipher) {
		t.Fatalf("sealPayload on CBC: want ErrInvalidCipher, got %v", err)
	}
	if payload.Encrypted || string(payload.Data) != before {
		t.Fatal("failed seal must leave the payload unmodified")
	}
}

func TestSealPayload_NoopWhenDisabled(t *testing.T) {
	saveAndRestoreEncryptionState(t)
	SetPayloadEncryptor(nil)

	job := &TestJob{ID: "enc-off", Message: "visible"}
	payload, err := MarshalJob(job, "default")
	if err != nil {
		t.Fatalf("MarshalJob: %v", err)
	}
	before := string(payload.Data)
	if err := sealPayload(payload); err != nil {
		t.Fatalf("sealPayload: %v", err)
	}
	if payload.Encrypted || string(payload.Data) != before {
		t.Fatal("sealPayload must be a no-op when encryption is disabled")
	}
	if err := openPayload(payload, false); err != nil {
		t.Fatalf("openPayload must accept plaintext when encryption is off: %v", err)
	}
}

func TestOpenPayload_TamperedCiphertextRejected(t *testing.T) {
	saveAndRestoreEncryptionState(t)
	SetPayloadEncryptor(newTestEncryptor(t, "AES-256-GCM"))

	job := &TestJob{ID: "enc-tamper", Message: "secret"}
	payload, err := MarshalJob(job, "default")
	if err != nil {
		t.Fatalf("MarshalJob: %v", err)
	}
	if err := sealPayload(payload); err != nil {
		t.Fatalf("sealPayload: %v", err)
	}

	// Flip one character inside the ciphertext envelope, deterministically.
	payload.Data = []byte(tamperJSONStringValue(t, string(payload.Data)))

	if err := openPayload(payload, true); err == nil {
		t.Fatal("openPayload must reject tampered ciphertext")
	}
}

// TestOpenPayload_TypeSwapRejected pins the AAD binding: a valid ciphertext
// sealed for one job type must not open as the Data of another type.
func TestOpenPayload_TypeSwapRejected(t *testing.T) {
	saveAndRestoreEncryptionState(t)
	SetPayloadEncryptor(newTestEncryptor(t, "AES-256-GCM"))

	job := &TestJob{ID: "enc-aad", Message: "secret"}
	payload, err := MarshalJob(job, "default")
	if err != nil {
		t.Fatalf("MarshalJob: %v", err)
	}
	if err := sealPayload(payload); err != nil {
		t.Fatalf("sealPayload: %v", err)
	}

	payload.Type = "*queue.SomeOtherJob"
	if err := openPayload(payload, true); err == nil {
		t.Fatal("openPayload must reject a ciphertext replayed under a different job type")
	}
}

// TestOpenPayload_LegacyPlaintextGating pins the one-deploy transition
// rule: with encryption on, a plaintext (pre-flip) payload is accepted
// only when its signature already verified, and rejected otherwise.
func TestOpenPayload_LegacyPlaintextGating(t *testing.T) {
	saveAndRestoreEncryptionState(t)
	SetPayloadEncryptor(newTestEncryptor(t, "AES-256-GCM"))

	job := &TestJob{ID: "legacy", Message: "old-fleet"}
	payload, err := MarshalJob(job, "default")
	if err != nil {
		t.Fatalf("MarshalJob: %v", err)
	}

	if err := openPayload(payload, true); err != nil {
		t.Fatalf("signed legacy plaintext must be accepted during transition: %v", err)
	}
	if err := openPayload(payload, false); err == nil {
		t.Fatal("unsigned plaintext must be rejected when encryption is on")
	}
}

func TestOpenPayload_EncryptedWithoutEncryptorFails(t *testing.T) {
	saveAndRestoreEncryptionState(t)
	SetPayloadEncryptor(newTestEncryptor(t, "AES-256-GCM"))

	job := &TestJob{ID: "enc-orphan", Message: "secret"}
	payload, err := MarshalJob(job, "default")
	if err != nil {
		t.Fatalf("MarshalJob: %v", err)
	}
	if err := sealPayload(payload); err != nil {
		t.Fatalf("sealPayload: %v", err)
	}

	SetPayloadEncryptor(nil)
	if err := openPayload(payload, true); err == nil {
		t.Fatal("encrypted payload must fail closed when no encryptor is configured")
	}
}

func TestHydrateJob_RejectsEncryptedPayload(t *testing.T) {
	saveAndRestoreEncryptionState(t)
	SetPayloadEncryptor(newTestEncryptor(t, "AES-256-GCM"))

	job := &TestJob{ID: "enc-hydrate", Message: "secret"}
	payload, err := MarshalJob(job, "default")
	if err != nil {
		t.Fatalf("MarshalJob: %v", err)
	}
	if err := sealPayload(payload); err != nil {
		t.Fatalf("sealPayload: %v", err)
	}
	if _, err := HydrateJob(payload); err == nil {
		t.Fatal("HydrateJob must refuse a still-encrypted payload")
	}
}

// TestDatabaseDriver_EncryptionRoundTrip is the dispatch-pop-handle round
// trip on the database driver with signing AND encryption on: the stored
// row must hold ciphertext, and the popped job must hydrate and run with
// its original state.
func TestDatabaseDriver_EncryptionRoundTrip(t *testing.T) {
	saveAndRestoreSigningState(t)
	saveAndRestoreEncryptionState(t)
	SetSigningKey([]byte("integrity-test-key-32-bytes-long"))
	SetPayloadEncryptor(newTestEncryptor(t, "AES-256-GCM"))

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	handled := false
	if err := driver.PushCtx(context.Background(), &TestJob{ID: "rt-1", Message: "pii-reset-token"}, "enc-rt"); err != nil {
		t.Fatalf("push: %v", err)
	}

	// At-rest row must be ciphertext with the envelope metadata readable.
	var raw string
	if err := driver.db.QueryRow("SELECT payload FROM jobs WHERE queue = 'enc-rt'").Scan(&raw); err != nil {
		t.Fatalf("read raw row: %v", err)
	}
	if strings.Contains(raw, "pii-reset-token") {
		t.Fatal("jobs row contains plaintext job state with encryption on")
	}
	if !strings.Contains(raw, `"encrypted":true`) {
		t.Fatal("jobs row missing encrypted marker")
	}
	if !strings.Contains(raw, `"type":"TestJob"`) {
		t.Fatal("envelope type must stay readable for routing")
	}
	if !strings.Contains(raw, `"signature":`) {
		t.Fatal("payload must still be signed with encryption on")
	}

	got, err := driver.PopCtx(context.Background(), "enc-rt")
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	tj, ok := got.(*TestJob)
	if !ok || tj == nil {
		t.Fatalf("popped job has wrong type: %T", got)
	}
	if tj.ID != "rt-1" || tj.Message != "pii-reset-token" {
		t.Fatalf("job state lost in round trip: %+v", tj)
	}
	tj.Handler = func() error { handled = true; return nil }
	if err := tj.Handle(); err != nil {
		t.Fatalf("handle: %v", err)
	}
	if !handled {
		t.Fatal("handler did not run")
	}
}

// TestDatabaseDriver_TamperedCiphertextQuarantinedBeforeDecrypt pins the
// encrypt-then-sign ordering: with signing on, a tampered ciphertext is
// rejected by the SIGNATURE check (and quarantined) before the decryptor
// ever sees the bytes. The failed_jobs exception proves which layer fired.
func TestDatabaseDriver_TamperedCiphertextQuarantinedBeforeDecrypt(t *testing.T) {
	saveAndRestoreSigningState(t)
	saveAndRestoreEncryptionState(t)
	SetSigningKey([]byte("integrity-test-key-32-bytes-long"))
	SetPayloadEncryptor(newTestEncryptor(t, "AES-256-GCM"))

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	if err := driver.PushCtx(context.Background(), &TestJob{ID: "tamper-1", Message: "secret"}, "enc-tamper"); err != nil {
		t.Fatalf("push: %v", err)
	}

	var raw string
	if err := driver.db.QueryRow("SELECT payload FROM jobs WHERE queue = 'enc-tamper'").Scan(&raw); err != nil {
		t.Fatalf("read raw row: %v", err)
	}

	// Flip a character inside the ciphertext envelope without re-signing.
	var wrapper jobWrapper
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		t.Fatalf("unmarshal wrapper: %v", err)
	}
	data := string(wrapper.Payload.Data)
	tampered := tamperJSONStringValue(t, data)
	wrapper.Payload.Data = json.RawMessage(tampered)
	mutated, err := json.Marshal(&wrapper)
	if err != nil {
		t.Fatalf("marshal tampered wrapper: %v", err)
	}
	if _, err := driver.db.Exec("UPDATE jobs SET payload = ? WHERE queue = 'enc-tamper'", string(mutated)); err != nil {
		t.Fatalf("write tampered row: %v", err)
	}

	_, popErr := driver.PopCtx(context.Background(), "enc-tamper")
	if !errors.Is(popErr, ErrPoisonJob) {
		t.Fatalf("expected ErrPoisonJob, got %v", popErr)
	}

	var exception string
	if err := driver.db.QueryRow("SELECT exception FROM failed_jobs WHERE queue = 'enc-tamper'").Scan(&exception); err != nil {
		t.Fatalf("read failed_jobs: %v", err)
	}
	if !strings.Contains(exception, "integrity check failed") {
		t.Fatalf("tamper must be caught by the signature layer, got exception: %s", exception)
	}
	if strings.Contains(exception, "decrypt") {
		t.Fatalf("decryptor must not run on tampered bytes, got exception: %s", exception)
	}
}

// TestDatabaseDriver_TamperedCiphertextRejectedByAEAD covers the
// signing-disabled configuration: the encryptor's own authentication is
// then the integrity layer and a tampered ciphertext is quarantined at
// decrypt time.
func TestDatabaseDriver_TamperedCiphertextRejectedByAEAD(t *testing.T) {
	saveAndRestoreSigningState(t)
	saveAndRestoreEncryptionState(t)
	SetSigningKey(nil)
	SetPayloadEncryptor(newTestEncryptor(t, "AES-256-GCM"))

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	if err := driver.PushCtx(context.Background(), &TestJob{ID: "tamper-2", Message: "secret"}, "enc-aead"); err != nil {
		t.Fatalf("push: %v", err)
	}

	var raw string
	if err := driver.db.QueryRow("SELECT payload FROM jobs WHERE queue = 'enc-aead'").Scan(&raw); err != nil {
		t.Fatalf("read raw row: %v", err)
	}
	var wrapper jobWrapper
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		t.Fatalf("unmarshal wrapper: %v", err)
	}
	data := string(wrapper.Payload.Data)
	tampered := tamperJSONStringValue(t, data)
	wrapper.Payload.Data = json.RawMessage(tampered)
	mutated, err := json.Marshal(&wrapper)
	if err != nil {
		t.Fatalf("marshal tampered wrapper: %v", err)
	}
	if _, err := driver.db.Exec("UPDATE jobs SET payload = ? WHERE queue = 'enc-aead'", string(mutated)); err != nil {
		t.Fatalf("write tampered row: %v", err)
	}

	_, popErr := driver.PopCtx(context.Background(), "enc-aead")
	if !errors.Is(popErr, ErrPoisonJob) {
		t.Fatalf("expected ErrPoisonJob, got %v", popErr)
	}

	var exception string
	if err := driver.db.QueryRow("SELECT exception FROM failed_jobs WHERE queue = 'enc-aead'").Scan(&exception); err != nil {
		t.Fatalf("read failed_jobs: %v", err)
	}
	if !strings.Contains(exception, "decrypt") {
		t.Fatalf("expected a decryption failure, got exception: %s", exception)
	}
}

// TestDatabaseDriver_LegacyPlaintextTransition simulates the QUEUE_ENCRYPT
// deploy flip: a payload signed by the pre-encryption fleet is still in the
// jobs table when the encrypting fleet pops. The verified signature gates
// its acceptance.
func TestDatabaseDriver_LegacyPlaintextTransition(t *testing.T) {
	saveAndRestoreSigningState(t)
	saveAndRestoreEncryptionState(t)
	SetSigningKey([]byte("integrity-test-key-32-bytes-long"))
	SetPayloadEncryptor(nil) // old fleet: signing only

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	if err := driver.PushCtx(context.Background(), &TestJob{ID: "legacy-1", Message: "in-flight"}, "enc-legacy"); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Deploy flips encryption on.
	SetPayloadEncryptor(newTestEncryptor(t, "AES-256-GCM"))

	got, err := driver.PopCtx(context.Background(), "enc-legacy")
	if err != nil {
		t.Fatalf("signed legacy payload must drain during transition: %v", err)
	}
	tj, ok := got.(*TestJob)
	if !ok || tj.ID != "legacy-1" {
		t.Fatalf("legacy job state lost: %+v", got)
	}
}

// TestDatabaseDriver_LegacyPlaintextUnsignedRejected pins the fail-closed
// half of the transition rule: with encryption on, an UNSIGNED plaintext
// payload (indistinguishable from an attacker's direct store write) is
// quarantined.
func TestDatabaseDriver_LegacyPlaintextUnsignedRejected(t *testing.T) {
	saveAndRestoreSigningState(t)
	saveAndRestoreEncryptionState(t)
	SetSigningKey(nil)
	SetPayloadEncryptor(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	if err := driver.PushCtx(context.Background(), &TestJob{ID: "legacy-2", Message: "unsigned"}, "enc-unsigned"); err != nil {
		t.Fatalf("push: %v", err)
	}

	SetPayloadEncryptor(newTestEncryptor(t, "AES-256-GCM"))

	_, popErr := driver.PopCtx(context.Background(), "enc-unsigned")
	if !errors.Is(popErr, ErrPoisonJob) {
		t.Fatalf("unsigned plaintext must be quarantined when encryption is on, got %v", popErr)
	}

	var exception string
	if err := driver.db.QueryRow("SELECT exception FROM failed_jobs WHERE queue = 'enc-unsigned'").Scan(&exception); err != nil {
		t.Fatalf("read failed_jobs: %v", err)
	}
	if !strings.Contains(exception, "plaintext payload rejected") {
		t.Fatalf("expected the plaintext-rejection error, got: %s", exception)
	}
}

// TestDatabaseDriver_FailedJobsRowIsCiphertext asserts the automatic win
// from V2-07: when encryption is on, the long-lived failed_jobs copy of a
// job holds ciphertext too.
func TestDatabaseDriver_FailedJobsRowIsCiphertext(t *testing.T) {
	saveAndRestoreSigningState(t)
	saveAndRestoreEncryptionState(t)
	SetSigningKey([]byte("integrity-test-key-32-bytes-long"))
	SetPayloadEncryptor(newTestEncryptor(t, "AES-256-GCM"))

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	job := &TestJob{ID: "fail-1", Message: "pii-in-failed-row"}
	if err := driver.Failed(job, errors.New("handler exploded"), "enc-failed"); err != nil {
		t.Fatalf("Failed: %v", err)
	}

	var raw string
	if err := driver.db.QueryRow("SELECT payload FROM failed_jobs WHERE queue = 'enc-failed'").Scan(&raw); err != nil {
		t.Fatalf("read failed_jobs: %v", err)
	}
	if strings.Contains(raw, "pii-in-failed-row") {
		t.Fatal("failed_jobs row contains plaintext job state with encryption on")
	}
	if !strings.Contains(raw, `"encrypted":true`) {
		t.Fatal("failed_jobs row missing encrypted marker")
	}
}

// TestDatabaseDriver_PoisonQuarantineNotPlaintext closes the review gap on
// V2-07: an unsigned plaintext payload rejected under encryption-on is
// quarantined into failed_jobs, and the quarantined copy must NOT be the
// plaintext bytes. The stored blob must be a sealed envelope that round-trips
// through the quarantine open path back to the original poison bytes.
func TestDatabaseDriver_PoisonQuarantineNotPlaintext(t *testing.T) {
	saveAndRestoreSigningState(t)
	saveAndRestoreEncryptionState(t)
	SetSigningKey(nil)
	SetPayloadEncryptor(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	const marker = "poison-secret-reset-token-9f8e7d"
	if err := driver.PushCtx(context.Background(), &TestJob{ID: "poison-1", Message: marker}, "enc-poison"); err != nil {
		t.Fatalf("push: %v", err)
	}

	SetPayloadEncryptor(newTestEncryptor(t, "AES-256-GCM"))

	if _, popErr := driver.PopCtx(context.Background(), "enc-poison"); !errors.Is(popErr, ErrPoisonJob) {
		t.Fatalf("unsigned plaintext must be quarantined when encryption is on, got %v", popErr)
	}

	var stored string
	if err := driver.db.QueryRow("SELECT payload FROM failed_jobs WHERE queue = 'enc-poison'").Scan(&stored); err != nil {
		t.Fatalf("read failed_jobs: %v", err)
	}
	if strings.Contains(stored, marker) {
		t.Fatal("failed_jobs holds plaintext poison payload with encryption on")
	}

	opened, err := openQuarantineBlob(stored)
	if err != nil {
		t.Fatalf("quarantined blob must open via the quarantine path: %v", err)
	}
	if !strings.Contains(string(opened), marker) {
		t.Fatalf("opened quarantine blob lost the original payload: %s", opened)
	}
}

// TestQuarantineBlob_PassThroughWhenEncryptionOff pins the default
// behaviour: with no encryptor installed, quarantine bytes pass through
// unchanged (operators keep raw forensics, exactly as before V2-07).
func TestQuarantineBlob_PassThroughWhenEncryptionOff(t *testing.T) {
	saveAndRestoreEncryptionState(t)
	SetPayloadEncryptor(nil)

	blob, sealed := sealQuarantineBlob(`{"raw":"poison"}`)
	if sealed || blob != `{"raw":"poison"}` {
		t.Fatalf("expected pass-through, got sealed=%v blob=%s", sealed, blob)
	}
}

// TestQuarantineBlob_AADDomainSeparation: a sealed quarantine blob must not
// decrypt under the live-payload AAD (and so cannot be replayed as a live
// payload's Data), and vice versa.
func TestQuarantineBlob_AADDomainSeparation(t *testing.T) {
	saveAndRestoreEncryptionState(t)
	enc := newTestEncryptor(t, "AES-256-GCM")
	SetPayloadEncryptor(enc)

	blob, sealed := sealQuarantineBlob("poison-bytes")
	if !sealed {
		t.Fatal("expected sealed blob with encryptor installed")
	}
	if _, err := enc.DecryptBytesWithAAD(blob, payloadAAD("TestJob")); err == nil {
		t.Fatal("quarantine blob must not verify under a live-payload AAD")
	}
	if _, err := openQuarantineBlob(blob); err != nil {
		t.Fatalf("quarantine blob must open under the quarantine AAD: %v", err)
	}
}

// tamperJSONStringValue deterministically flips one character inside a
// quoted JSON string value, preserving JSON validity. The byte at the
// middle of the string body is replaced with 'A' (or 'B' when it already
// is 'A'), so the mutation can never be a no-op regardless of which
// characters the ciphertext happens to contain. The sealed envelope is a
// base64 payload string, so no JSON escape sequences can sit at the
// mutation point.
func tamperJSONStringValue(t *testing.T, data string) string {
	t.Helper()
	if len(data) < 4 || data[0] != '"' || data[len(data)-1] != '"' {
		t.Fatalf("expected quoted JSON string envelope, got: %s", data)
	}
	mid := len(data) / 2
	c := byte('A')
	if data[mid] == 'A' {
		c = 'B'
	}
	tampered := data[:mid] + string(c) + data[mid+1:]
	if tampered == data {
		t.Fatal("tamper produced identical payload")
	}
	return tampered
}
