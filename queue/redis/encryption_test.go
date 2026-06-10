package redis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/crypto"
	"github.com/velocitykode/velocity/queue"
)

// encJob is the round-trip fixture for the leaf's encryption tests.
type encJob struct {
	ID      string `json:"id"`
	Message string `json:"message"`
}

func (j *encJob) Handle() error  { return nil }
func (j *encJob) Failed(_ error) {}

func init() {
	queue.RegisterJob(func(data []byte) (*encJob, error) {
		var job encJob
		if err := json.Unmarshal(data, &job); err != nil {
			return nil, err
		}
		return &job, nil
	})
}

// saveAndRestoreEncryptionState clears the queue package's payload
// encryptor on cleanup so encryption tests cannot leak state into tests
// that assume plaintext payloads.
func saveAndRestoreEncryptionState(t *testing.T) {
	t.Helper()
	t.Cleanup(func() {
		queue.SetPayloadEncryptor(nil)
	})
}

func newTestEncryptor(t *testing.T) contract.Encryptor {
	t.Helper()
	enc, err := crypto.NewEncryptor(crypto.Config{
		Key:    "base64:MDEyMzQ1Njc4OTAxMjM0NTY3ODkwMTIzNDU2Nzg5MDE=",
		Cipher: "AES-256-GCM",
	})
	if err != nil {
		t.Fatalf("newTestEncryptor: %v", err)
	}
	return enc
}

// TestRedisDriver_EncryptionRoundTrip is the dispatch-pop round trip on the
// Redis driver with signing AND encryption on: the RPUSHed list entry must
// hold ciphertext, and the popped job must hydrate with its original state.
func TestRedisDriver_EncryptionRoundTrip(t *testing.T) {
	saveAndRestoreSigningState(t)
	saveAndRestoreEncryptionState(t)
	queue.SetSigningKey([]byte("integrity-test-key-32-bytes-long"))
	queue.SetPayloadEncryptor(newTestEncryptor(t))

	driver, mr := newMiniRedisDriver(t)
	defer mr.Close()
	defer driver.Shutdown(context.Background())

	if err := driver.PushCtx(context.Background(), &encJob{ID: "rt-1", Message: "pii-reset-token"}, "enc-rt"); err != nil {
		t.Fatalf("push: %v", err)
	}

	// At-rest list entry must be ciphertext with envelope metadata readable.
	raw, err := driver.client.LRange(context.Background(), driver.getQueueKey("enc-rt"), 0, -1).Result()
	if err != nil || len(raw) != 1 {
		t.Fatalf("LRange: %v (len %d)", err, len(raw))
	}
	if strings.Contains(raw[0], "pii-reset-token") {
		t.Fatal("redis list entry contains plaintext job state with encryption on")
	}
	if !strings.Contains(raw[0], `"encrypted":true`) {
		t.Fatal("redis list entry missing encrypted marker")
	}
	if !strings.Contains(raw[0], `"type":"encJob"`) {
		t.Fatal("envelope type must stay readable for routing")
	}
	if !strings.Contains(raw[0], `"signature":`) {
		t.Fatal("payload must still be signed with encryption on")
	}

	got, _, err := driver.PopCtxWithTrace(context.Background(), "enc-rt")
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	ej, ok := got.(*encJob)
	if !ok || ej == nil {
		t.Fatalf("popped job has wrong type: %T", got)
	}
	if ej.ID != "rt-1" || ej.Message != "pii-reset-token" {
		t.Fatalf("job state lost in round trip: %+v", ej)
	}
}

// TestRedisDriver_TamperedCiphertextQuarantinedBeforeDecrypt pins the
// encrypt-then-sign ordering on the Redis driver: a tampered ciphertext is
// rejected by the signature check (and quarantined) before decryption.
func TestRedisDriver_TamperedCiphertextQuarantinedBeforeDecrypt(t *testing.T) {
	saveAndRestoreSigningState(t)
	saveAndRestoreEncryptionState(t)
	queue.SetSigningKey([]byte("integrity-test-key-32-bytes-long"))
	queue.SetPayloadEncryptor(newTestEncryptor(t))

	driver, mr := newMiniRedisDriver(t)
	defer mr.Close()
	defer driver.Shutdown(context.Background())
	snapshot := captureRedisJobFailed(driver)

	if err := driver.PushCtx(context.Background(), &encJob{ID: "tamper-1", Message: "secret"}, "enc-tamper"); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Pull the raw entry, flip a ciphertext character without re-signing,
	// and put it back.
	queueKey := driver.getQueueKey("enc-tamper")
	raw, err := driver.client.LPop(context.Background(), queueKey).Result()
	if err != nil {
		t.Fatalf("LPop: %v", err)
	}
	var payload queue.Payload
	if err := json.Unmarshal([]byte(raw), &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	data := string(payload.Data)
	tampered := tamperJSONStringValue(t, data)
	payload.Data = json.RawMessage(tampered)
	mutated, err := json.Marshal(&payload)
	if err != nil {
		t.Fatalf("marshal tampered payload: %v", err)
	}
	if err := driver.client.RPush(context.Background(), queueKey, mutated).Err(); err != nil {
		t.Fatalf("RPush tampered: %v", err)
	}

	_, _, popErr := driver.PopCtxWithTrace(context.Background(), "enc-tamper")
	if !errors.Is(popErr, queue.ErrPoisonJob) {
		t.Fatalf("expected ErrPoisonJob, got %v", popErr)
	}

	rec := readRedisFailedQuarantineRecord(t, driver, "enc-tamper")
	if !strings.Contains(rec.Exception, "integrity check failed") {
		t.Fatalf("tamper must be caught by the signature layer, got: %s", rec.Exception)
	}
	if strings.Contains(rec.Exception, "decrypt") {
		t.Fatalf("decryptor must not run on tampered bytes, got: %s", rec.Exception)
	}
	if len(snapshot()) == 0 {
		t.Fatal("expected a JobFailed event for the quarantined entry")
	}
}

// TestRedisDriver_LegacyPlaintextTransition simulates the QUEUE_ENCRYPT
// deploy flip on Redis: a payload signed by the pre-encryption fleet is
// accepted by an encrypting consumer because its signature verified;
// an unsigned plaintext payload is quarantined.
func TestRedisDriver_LegacyPlaintextTransition(t *testing.T) {
	saveAndRestoreSigningState(t)
	saveAndRestoreEncryptionState(t)
	queue.SetSigningKey([]byte("integrity-test-key-32-bytes-long"))
	queue.SetPayloadEncryptor(nil) // old fleet: signing only

	driver, mr := newMiniRedisDriver(t)
	defer mr.Close()
	defer driver.Shutdown(context.Background())

	if err := driver.PushCtx(context.Background(), &encJob{ID: "legacy-1", Message: "in-flight"}, "enc-legacy"); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Deploy flips encryption on.
	queue.SetPayloadEncryptor(newTestEncryptor(t))

	got, _, err := driver.PopCtxWithTrace(context.Background(), "enc-legacy")
	if err != nil {
		t.Fatalf("signed legacy payload must drain during transition: %v", err)
	}
	ej, ok := got.(*encJob)
	if !ok || ej.ID != "legacy-1" {
		t.Fatalf("legacy job state lost: %+v", got)
	}
}

// TestRedisDriver_FailedListEntryIsCiphertext asserts the failed-jobs list
// copy stays ciphertext when encryption is on.
func TestRedisDriver_FailedListEntryIsCiphertext(t *testing.T) {
	saveAndRestoreSigningState(t)
	saveAndRestoreEncryptionState(t)
	queue.SetSigningKey([]byte("integrity-test-key-32-bytes-long"))
	queue.SetPayloadEncryptor(newTestEncryptor(t))

	driver, mr := newMiniRedisDriver(t)
	defer mr.Close()
	defer driver.Shutdown(context.Background())

	if err := driver.Failed(&encJob{ID: "fail-1", Message: "pii-in-failed-row"}, errors.New("handler exploded"), "enc-failed"); err != nil {
		t.Fatalf("Failed: %v", err)
	}

	raw, err := driver.client.LRange(context.Background(), driver.getFailedKey("enc-failed"), 0, -1).Result()
	if err != nil || len(raw) != 1 {
		t.Fatalf("LRange failed list: %v (len %d)", err, len(raw))
	}
	if strings.Contains(raw[0], "pii-in-failed-row") {
		t.Fatal("failed list entry contains plaintext job state with encryption on")
	}
	if !strings.Contains(raw[0], `"encrypted":true`) {
		t.Fatal("failed list entry missing encrypted marker")
	}
}

// TestRedisDriver_PoisonQuarantineNotPlaintext closes the review gap on
// V2-07 for the Redis driver: an unsigned plaintext entry rejected under
// encryption-on lands in the failed list as a sealed blob, never plaintext.
func TestRedisDriver_PoisonQuarantineNotPlaintext(t *testing.T) {
	saveAndRestoreSigningState(t)
	saveAndRestoreEncryptionState(t)
	queue.SetSigningKey(nil)
	queue.SetPayloadEncryptor(nil)

	driver, mr := newMiniRedisDriver(t)
	defer mr.Close()
	defer driver.Shutdown(context.Background())

	const marker = "poison-secret-reset-token-1a2b3c"
	if err := driver.PushCtx(context.Background(), &encJob{ID: "poison-1", Message: marker}, "enc-poison"); err != nil {
		t.Fatalf("push: %v", err)
	}

	queue.SetPayloadEncryptor(newTestEncryptor(t))

	if _, _, err := driver.PopCtxWithTrace(context.Background(), "enc-poison"); !errors.Is(err, queue.ErrPoisonJob) {
		t.Fatalf("unsigned plaintext must be quarantined when encryption is on, got %v", err)
	}

	rows, err := driver.client.LRange(context.Background(), driver.getFailedKey("enc-poison"), 0, -1).Result()
	if err != nil || len(rows) != 1 {
		t.Fatalf("failed list: %v (len %d)", err, len(rows))
	}
	if strings.Contains(rows[0], marker) {
		t.Fatal("failed list holds plaintext poison payload with encryption on")
	}
	if !strings.Contains(rows[0], `"encrypted":true`) {
		t.Fatal("sealed poison record missing encrypted marker")
	}

	var record struct {
		PayloadB64 string `json:"payload_b64"`
	}
	if err := json.Unmarshal([]byte(rows[0]), &record); err != nil {
		t.Fatalf("unmarshal poison record: %v", err)
	}
	blob, err := base64.StdEncoding.DecodeString(record.PayloadB64)
	if err != nil {
		t.Fatalf("decode payload_b64: %v", err)
	}
	opened, err := queue.OpenQuarantineBlob(string(blob))
	if err != nil {
		t.Fatalf("quarantined blob must open via OpenQuarantineBlob: %v", err)
	}
	if !strings.Contains(string(opened), marker) {
		t.Fatalf("opened quarantine blob lost the original payload: %s", opened)
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
