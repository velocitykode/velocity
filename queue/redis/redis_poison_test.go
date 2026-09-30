package redis

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/queue"
)

// captureRedisJobFailed installs a JobFailed-capturing dispatcher onto
// the driver and returns a snapshot func plus the underlying slice mu.
// Used by every poison-payload test in this file so they all converge on
// the same shape: dispatcher recorded under the driver's atomic.Pointer,
// captured events readable from the test goroutine without races.
func captureRedisJobFailed(driver *RedisDriver) (snapshot func() []*queue.JobFailed) {
	var mu sync.Mutex
	var events []*queue.JobFailed
	driver.SetEventDispatcher(func(ctx context.Context, e interface{}) error {
		if jf, ok := e.(*queue.JobFailed); ok {
			mu.Lock()
			events = append(events, jf)
			mu.Unlock()
		}
		return nil
	})
	return func() []*queue.JobFailed {
		mu.Lock()
		defer mu.Unlock()
		cp := make([]*queue.JobFailed, len(events))
		copy(cp, events)
		return cp
	}
}

// readRedisFailedQuarantineRecord pops the first entry off the per-queue
// failed-jobs list (the location quarantinePoisonedPayload writes to)
// and decodes it into the shape this fix persists. Returns the raw
// stored bytes so a test can assert that the exact poison payload was
// preserved verbatim regardless of UTF-8 validity.
type quarantineRecord struct {
	Queue      string    `json:"queue"`
	PayloadB64 string    `json:"payload_b64"`
	Exception  string    `json:"exception"`
	FailedAt   time.Time `json:"failed_at"`
	Poison     bool      `json:"poison"`
}

func readRedisFailedQuarantineRecord(t *testing.T, driver *RedisDriver, queueName string) quarantineRecord {
	t.Helper()
	failedKey := driver.getFailedKey(queueName)
	raw, err := driver.client.LPop(context.Background(), failedKey).Result()
	if err != nil {
		t.Fatalf("LPop %s: %v", failedKey, err)
	}
	var rec quarantineRecord
	if err := json.Unmarshal([]byte(raw), &rec); err != nil {
		t.Fatalf("unmarshal quarantine record: %v\nraw=%q", err, raw)
	}
	return rec
}

// TestRedisDriver_PopCtxWithTrace_MalformedJSONQuarantined exercises the
// M-06 fix for the BLPop poison-loss path: a payload that fails JSON
// unmarshal must (a) surface ErrPoisonJob to the caller so the worker
// treats the failure as recoverable, (b) land in the per-queue
// failed-jobs list with the raw bytes preserved verbatim (base64 so
// non-UTF-8 bytes survive a round trip), and (c) emit no JobFailed
// event, since the job never ran. Before the fix BLPop consumed the entry and
// the function returned an error with no breadcrumbs.
func TestRedisDriver_PopCtxWithTrace_MalformedJSONQuarantined(t *testing.T) {
	saveAndRestoreSigningState(t)
	queue.SetSigningKey(nil)

	driver, mr := newMiniRedisDriver(t)
	snapshot := captureRedisJobFailed(driver)

	queueName := "poison-malformed"
	queueKey := driver.getQueueKey(queueName)

	// Seed the queue with deliberately corrupt JSON. miniredis stores
	// list entries as opaque strings so we can plant anything.
	const poisoned = `{this is not json at all`
	if _, err := mr.Lpush(queueKey, poisoned); err != nil {
		t.Fatalf("seed poison entry: %v", err)
	}

	job, _, err := driver.PopCtxWithTrace(context.Background(), queueName)
	if err == nil {
		t.Fatalf("PopCtxWithTrace accepted malformed payload: job=%T", job)
	}
	if !errors.Is(err, queue.ErrPoisonJob) {
		t.Errorf("malformed-JSON pop error did not wrap ErrPoisonJob (worker would not treat as recoverable): %v", err)
	}
	if job != nil {
		t.Errorf("PopCtxWithTrace returned non-nil job alongside malformed-JSON error: %T", job)
	}

	// Quarantine record landed with the raw bytes preserved.
	rec := readRedisFailedQuarantineRecord(t, driver, queueName)
	if rec.Queue != queueName {
		t.Errorf("rec.Queue = %q, want %q", rec.Queue, queueName)
	}
	if !rec.Poison {
		t.Error("rec.Poison = false; quarantine breadcrumb missing")
	}
	rawBytes, decErr := base64.StdEncoding.DecodeString(rec.PayloadB64)
	if decErr != nil {
		t.Fatalf("decode preserved payload: %v", decErr)
	}
	if string(rawBytes) != poisoned {
		t.Errorf("preserved payload mismatch:\nwant %q\ngot  %q", poisoned, string(rawBytes))
	}
	if !strings.Contains(rec.Exception, "failed to unmarshal payload") {
		t.Errorf("exception does not name the cause: %q", rec.Exception)
	}

	// The job never ran, so no queue.job.failed event is dispatched.
	if n := len(snapshot()); n != 0 {
		t.Errorf("JobFailed events = %d, want 0: a poison job never ran", n)
	}
}

// TestRedisDriver_PopCtxWithTrace_BadSignatureQuarantined drives the
// integrity-mismatch branch (verifyPayload error after a successful
// JSON unmarshal). With signing enabled but the wire bytes tampered,
// verifyPayload returns a mismatch error; the entry must land in the
// failed-jobs list, with no JobFailed event.
func TestRedisDriver_PopCtxWithTrace_BadSignatureQuarantined(t *testing.T) {
	saveAndRestoreSigningState(t)
	queue.SetSigningKey([]byte("test-key-for-poison-redis"))

	driver, mr := newMiniRedisDriver(t)
	snapshot := captureRedisJobFailed(driver)

	queueName := "poison-badsig"
	queueKey := driver.getQueueKey(queueName)

	// Build a structurally valid payload that has a signature on the
	// wire but the signature does NOT match the bytes (forged producer
	// or in-transit tampering).
	payload := queue.Payload{
		Type:      "TamperedJob",
		Data:      json.RawMessage(`{"x":1}`),
		Queue:     queueName,
		CreatedAt: time.Now(),
		Signature: "deadbeef" + strings.Repeat("00", 28), // 64-hex-char sham
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal tampered payload: %v", err)
	}
	if _, err := mr.Lpush(queueKey, string(raw)); err != nil {
		t.Fatalf("seed tampered entry: %v", err)
	}

	job, _, err := driver.PopCtxWithTrace(context.Background(), queueName)
	if err == nil {
		t.Fatalf("PopCtxWithTrace accepted bad signature: job=%T", job)
	}
	if !errors.Is(err, queue.ErrPoisonJob) {
		t.Errorf("bad-signature pop error did not wrap ErrPoisonJob: %v", err)
	}
	if job != nil {
		t.Errorf("PopCtxWithTrace returned non-nil job alongside bad-signature error: %T", job)
	}

	rec := readRedisFailedQuarantineRecord(t, driver, queueName)
	if !rec.Poison {
		t.Error("rec.Poison = false; quarantine breadcrumb missing")
	}
	if !strings.Contains(rec.Exception, "integrity check failed") &&
		!strings.Contains(rec.Exception, "signature verification failed") {
		t.Errorf("exception does not name the cause: %q", rec.Exception)
	}
	rawBytes, decErr := base64.StdEncoding.DecodeString(rec.PayloadB64)
	if decErr != nil {
		t.Fatalf("decode preserved payload: %v", decErr)
	}
	if string(rawBytes) != string(raw) {
		t.Errorf("preserved payload mismatch:\nwant %q\ngot  %q", string(raw), string(rawBytes))
	}

	// The job never ran, so no queue.job.failed event is dispatched.
	if n := len(snapshot()); n != 0 {
		t.Errorf("JobFailed events = %d, want 0: a poison job never ran", n)
	}
}

// TestRedisDriver_PopCtxWithTrace_UnregisteredJobTypeQuarantined drives
// the registry.Deserialize failure branch: a structurally valid payload
// with a job type the consumer never registered. Before the M-06 fix
// BLPop dropped this entry without trace; the fix quarantines and
// surfaces it the same as the other poison paths.
func TestRedisDriver_PopCtxWithTrace_UnregisteredJobTypeQuarantined(t *testing.T) {
	saveAndRestoreSigningState(t)
	queue.SetSigningKey(nil)

	driver, mr := newMiniRedisDriver(t)
	snapshot := captureRedisJobFailed(driver)

	queueName := "poison-unknown"
	queueKey := driver.getQueueKey(queueName)

	// A wire-valid payload whose Type has no handler registered. The
	// unique string here prevents collisions with other tests that
	// register job types into the package-level registry.
	payload := queue.Payload{
		Type:      "RedisPoisonNeverRegisteredJob",
		Data:      json.RawMessage(`{}`),
		Queue:     queueName,
		CreatedAt: time.Now(),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal unregistered payload: %v", err)
	}
	if _, err := mr.Lpush(queueKey, string(raw)); err != nil {
		t.Fatalf("seed unregistered entry: %v", err)
	}

	job, _, err := driver.PopCtxWithTrace(context.Background(), queueName)
	if err == nil {
		t.Fatalf("PopCtxWithTrace accepted unregistered job: job=%T", job)
	}
	if !errors.Is(err, queue.ErrPoisonJob) {
		t.Errorf("unregistered-type pop error did not wrap ErrPoisonJob: %v", err)
	}
	if !errors.Is(err, queue.ErrJobNotFound) {
		t.Errorf("unregistered-type pop error did not wrap ErrJobNotFound (specific cause): %v", err)
	}
	if job != nil {
		t.Errorf("PopCtxWithTrace returned non-nil job alongside unregistered-type error: %T", job)
	}

	rec := readRedisFailedQuarantineRecord(t, driver, queueName)
	if !rec.Poison {
		t.Error("rec.Poison = false; quarantine breadcrumb missing")
	}
	if !strings.Contains(rec.Exception, "no handler registered") &&
		!strings.Contains(rec.Exception, "failed to deserialize job") {
		t.Errorf("exception does not name the cause: %q", rec.Exception)
	}

	// The job never ran, so no queue.job.failed event is dispatched.
	if n := len(snapshot()); n != 0 {
		t.Errorf("JobFailed events = %d, want 0: a poison job never ran", n)
	}
}

// redisPanicFactoryJob's registered factory panics: the factory is user
// code, and the pop that rebuilds it must not let the panic unwind.
type redisPanicFactoryJob struct{}

func (*redisPanicFactoryJob) Handle() error { return nil }
func (*redisPanicFactoryJob) Failed(error)  {}

var registerRedisPanicFactory sync.Once

// A payload whose factory panics is poison like any other: BLPop already
// consumed it, so it lands in the failed-jobs list with the fixed rebuild
// panic text (the panic value is never formatted), the pop returns
// ErrPoisonJob instead of panicking, and no JobFailed event is dispatched.
func TestRedisDriver_PopCtxWithTrace_PanickingFactoryQuarantined(t *testing.T) {
	saveAndRestoreSigningState(t)
	queue.SetSigningKey(nil)
	registerRedisPanicFactory.Do(func() {
		queue.RegisterJob(func([]byte) (*redisPanicFactoryJob, error) { panic("factory exploded") })
	})

	driver, _ := newMiniRedisDriver(t)
	snapshot := captureRedisJobFailed(driver)
	const queueName = "poison-panic"
	if err := driver.PushCtx(context.Background(), &redisPanicFactoryJob{}, queueName); err != nil {
		t.Fatalf("push: %v", err)
	}

	var (
		job queue.Job
		err error
	)
	func() {
		defer func() {
			if p := recover(); p != nil {
				t.Fatalf("PopCtxWithTrace let the factory's panic reach the caller: %v", p)
			}
		}()
		job, _, err = driver.PopCtxWithTrace(context.Background(), queueName)
	}()
	if job != nil || !errors.Is(err, queue.ErrPoisonJob) {
		t.Fatalf("PopCtxWithTrace = %T, %v; want no job and ErrPoisonJob", job, err)
	}

	rec := readRedisFailedQuarantineRecord(t, driver, queueName)
	if !rec.Poison || !strings.Contains(rec.Exception, "rebuilding the job panicked") {
		t.Errorf("quarantine record = %+v; want the poison breadcrumb with the fixed panic text", rec)
	}
	if strings.Contains(rec.Exception, "factory exploded") {
		t.Errorf("exception %q formats the panic value", rec.Exception)
	}
	if n := len(snapshot()); n != 0 {
		t.Errorf("JobFailed events = %d, want 0: a poison job never ran", n)
	}
}
