package queue

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// hashCountJob is a non-Identifiable job whose MarshalJSON bumps a counter,
// so a test/benchmark can observe exactly how many times the payload is
// hashed for attempt tracking over one job lifecycle. It is non-Identifiable
// on purpose: that is the path that pays the JSON-marshal + SHA-256 cost in
// Worker.jobKey.
type hashCountJob struct {
	N int
}

func (hashCountJob) Handle() error { return nil }
func (hashCountJob) Failed(error)  {}

func (j hashCountJob) MarshalJSON() ([]byte, error) {
	atomic.AddInt64(&marshalCalls, 1)
	// Stable, content-derived bytes; value irrelevant to the count.
	return []byte(`{"n":` + itoa(j.N) + `}`), nil
}

var marshalCalls int64

// itoa keeps the marshal hot path's import set minimal; N is small and
// non-negative in these tests.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// noMarshalDriver is a no-op Driver that, unlike MemoryDriver, never
// serializes the job. That isolation matters: MemoryDriver.Failed marshals
// the job for failed_jobs persistence, which is unrelated to attempt-tracking
// and would otherwise be miscounted as a jobKey hash. With this driver the
// only payload marshal in the failure lifecycle is jobKey's.
type noMarshalDriver struct{}

func (noMarshalDriver) PushCtx(context.Context, Job, ...string) error { return nil }
func (noMarshalDriver) PushDelayedCtx(context.Context, Job, time.Duration, ...string) error {
	return nil
}
func (noMarshalDriver) PopCtx(context.Context, string) (Job, error) { return nil, nil }
func (noMarshalDriver) Size(string) (int64, error)                  { return 0, nil }
func (noMarshalDriver) Clear(string) error                          { return nil }
func (noMarshalDriver) Failed(Job, error, string) error             { return nil }
func (noMarshalDriver) Shutdown(context.Context) error              { return nil }

// newFailureWorker builds a worker over the no-op driver with a zero
// reservation token, so handleJobFailure exercises the in-memory attempt
// cache path (incrementAttempts then, on terminal failure, removeAttempts)
// without any driver-side serialization muddying the hash count.
func newFailureWorker() *Worker {
	w := NewWorker(noMarshalDriver{}, "bench", func(Job) error { return nil },
		WithMaxRetries(1), // attempt 1 == maxAttempts -> terminal on first failure
		WithBackoff(func(int) time.Duration { return 0 }),
		WithWorkerLogger(nullLogger{}),
	)
	w.ctx, w.cancel = context.WithCancel(context.Background())
	return w
}

var errBench = &benchError{}

type benchError struct{}

func (*benchError) Error() string { return "bench: job failed" }

// TestProcessJob_NonIdentifiable_HashesOnce locks in the optimization: one
// full failure lifecycle (incrementAttempts via attemptNumber, then
// removeAttempts via failJob) must hash the payload exactly once, not twice.
func TestProcessJob_NonIdentifiable_HashesOnce(t *testing.T) {
	w := newFailureWorker()
	defer w.cancel()

	atomic.StoreInt64(&marshalCalls, 0)
	w.handleJobFailure(context.Background(), hashCountJob{N: 7}, "hashCountJob",
		errBench, 0, ReservationToken{})

	if got := atomic.LoadInt64(&marshalCalls); got != 1 {
		t.Fatalf("payload hashed %d times over one lifecycle, want 1 "+
			"(jobKey must be computed once in handleJobFailure and threaded "+
			"to attemptNumber/failJob)", got)
	}
}

// TestAttemptKey_SkipsHashWhenReservationCarriesAttempts proves the
// reservation fast-path: when the token already carries the persisted
// attempt count, the worker skips the content hash entirely (zero marshals).
func TestAttemptKey_SkipsHashWhenReservationCarriesAttempts(t *testing.T) {
	w := newFailureWorker()
	defer w.cancel()

	atomic.StoreInt64(&marshalCalls, 0)
	if key := w.attemptKey(hashCountJob{N: 7}, ReservationToken{ID: 1, Attempts: 1}); key != nil {
		t.Fatalf("attemptKey = %v, want nil when token carries Attempts", key)
	}
	if got := atomic.LoadInt64(&marshalCalls); got != 0 {
		t.Fatalf("payload hashed %d times on reservation-authoritative path, want 0", got)
	}
}

// TestAttemptPrimitives_DoNotHash guards the core of the fix: the
// incrementAttempts/removeAttempts primitives now take a precomputed key and
// must never re-derive it (no hash of their own).
func TestAttemptPrimitives_DoNotHash(t *testing.T) {
	w := newFailureWorker()
	defer w.cancel()

	key := w.attemptKey(hashCountJob{N: 7}, ReservationToken{})
	atomic.StoreInt64(&marshalCalls, 0)
	w.incrementAttempts(key)
	w.removeAttempts(key)
	if got := atomic.LoadInt64(&marshalCalls); got != 0 {
		t.Fatalf("attempt primitives hashed %d times, want 0 (key must be precomputed)", got)
	}
}

// BenchmarkProcessJob_NonIdentifiable measures one failure lifecycle for a
// non-Identifiable job. Before the fix the payload was hashed twice
// (incrementAttempts + removeAttempts each called jobKey); now it is hashed
// once. The reported hashes/op metric makes the single hash explicit.
func BenchmarkProcessJob_NonIdentifiable(b *testing.B) {
	w := newFailureWorker()
	defer w.cancel()
	ctx := context.Background()
	job := hashCountJob{N: 7}

	atomic.StoreInt64(&marshalCalls, 0)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w.handleJobFailure(ctx, job, "hashCountJob", errBench, 0, ReservationToken{})
	}
	b.StopTimer()

	b.ReportMetric(float64(atomic.LoadInt64(&marshalCalls))/float64(b.N), "hashes/op")
}
