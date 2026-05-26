package queue

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// failingIdentifiableJob is an Identifiable job that always fails. Used
// to drive the worker through repeated retry attempts without relying
// on the in-memory sync.Map cache (which is keyed by job pointer for
// non-Identifiable jobs and resets on every Pop with a fresh wrapper).
type failingIdentifiableJob struct {
	ID    string
	calls *atomic.Int32
}

func (j *failingIdentifiableJob) Handle() error {
	if j.calls != nil {
		j.calls.Add(1)
	}
	return errors.New("force failure for attempts persistence test")
}

func (j *failingIdentifiableJob) Failed(error) {}

func (j *failingIdentifiableJob) JobID() string { return j.ID }

// failingAnonymousJob has no JobID(); used to verify the non-Identifiable
// advisory warning fires once per job kind.
type failingAnonymousJob struct {
	Tag string
}

func (j *failingAnonymousJob) Handle() error { return errors.New("anonymous failure") }
func (j *failingAnonymousJob) Failed(error)  {}

// captureLogger collects log lines for assertion in tests.
type captureLogger struct {
	mu    sync.Mutex
	lines []string
}

func (c *captureLogger) record(level, msg string, kvs []any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b bytes.Buffer
	b.WriteString(level)
	b.WriteString(" ")
	b.WriteString(msg)
	for i := 0; i+1 < len(kvs); i += 2 {
		fmt.Fprintf(&b, " %v=%v", kvs[i], kvs[i+1])
	}
	c.lines = append(c.lines, b.String())
}

func (c *captureLogger) Info(msg string, kvs ...any)  { c.record("INFO", msg, kvs) }
func (c *captureLogger) Warn(msg string, kvs ...any)  { c.record("WARN", msg, kvs) }
func (c *captureLogger) Error(msg string, kvs ...any) { c.record("ERROR", msg, kvs) }

func (c *captureLogger) countContaining(needle string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, l := range c.lines {
		if strings.Contains(l, needle) {
			n++
		}
	}
	return n
}

// TestMemoryDriver_AttemptsPersistAcrossPopsTripsMaxAttempts pins the
// M-43 invariant: a failing job that bounces between two worker
// goroutines sharing one queue driver must trip MaxAttempts after the
// configured number of pops, not run forever. The pre-fix worker keyed
// its sync.Map on the job pointer (or zero-valued attempts column);
// every fresh Pop returned a different pointer / token.Attempts == 0,
// so MaxAttempts could never trip. With persisted attempts on the
// reservation token, the post-increment value carries across pops and
// MaxAttempts wins on the 4th pop with default maxRetries=3.
func TestMemoryDriver_AttemptsPersistAcrossPopsTripsMaxAttempts(t *testing.T) {
	driver := NewMemoryDriver()
	driver.Start()
	t.Cleanup(func() { _ = driver.Shutdown(context.Background()) })

	calls := &atomic.Int32{}
	job := &failingIdentifiableJob{ID: "persist-attempts-1", calls: calls}
	if err := driver.PushCtx(context.Background(), job, "attempts-queue"); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	// Two worker goroutines share one driver. Each one represents a
	// separate process for the purposes of the attempts contract: the
	// per-worker sync.Map cache is not shared, so the only way
	// MaxAttempts can trip is through the persisted Payload.Attempts
	// surfaced on the reservation token.
	wg := sync.WaitGroup{}
	wg.Add(2)
	handler := func(j Job) error { return j.Handle() }
	workerA := NewWorker(driver, "attempts-queue", handler,
		WithMaxRetries(3),
		WithBackoff(FixedBackoff(0)),
		WithInterval(5*time.Millisecond),
		WithWorkerLogger(nullLogger{}),
	)
	workerB := NewWorker(driver, "attempts-queue", handler,
		WithMaxRetries(3),
		WithBackoff(FixedBackoff(0)),
		WithInterval(5*time.Millisecond),
		WithWorkerLogger(nullLogger{}),
	)
	go func() { defer wg.Done(); workerA.Start(context.Background()) }()
	go func() { defer wg.Done(); workerB.Start(context.Background()) }()

	// Poll until the job lands in the failed list or we time out. The
	// fail-driven retry cycle is fast (FixedBackoff(0) + 5ms interval),
	// so a 3-second budget is generous enough even on a slow CI box.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if time.Now().After(deadline) {
			break
		}
		failed, _ := driver.GetFailed("attempts-queue")
		if len(failed) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	workerA.Stop()
	workerB.Stop()
	wg.Wait()

	failed, _ := driver.GetFailed("attempts-queue")
	if len(failed) != 1 {
		t.Fatalf("expected exactly 1 failed_jobs entry, got %d", len(failed))
	}
	// MaxAttempts is 3; the handler must have been invoked exactly 3
	// times (attempt 1, 2, 3 then terminal failure). Pre-fix this
	// would run forever because attempts reset to zero on every pop.
	if got := calls.Load(); got != 3 {
		t.Fatalf("expected handler called 3 times before MaxAttempts trips, got %d", got)
	}
}

// TestMemoryDriver_PopCtxReservedIncrementsAttempts pins the
// driver-level contract that PopCtxReserved (a) returns a non-zero
// token, (b) sets token.Attempts to the post-increment value, and (c)
// updates the wrapper's persisted Payload.Attempts so a subsequent
// ReleaseCtx + Pop reads the updated counter.
func TestMemoryDriver_PopCtxReservedIncrementsAttempts(t *testing.T) {
	driver := NewMemoryDriver()
	driver.Start()
	t.Cleanup(func() { _ = driver.Shutdown(context.Background()) })

	job := &failingIdentifiableJob{ID: "attempts-1"}
	if err := driver.PushCtx(context.Background(), job, "attempts-queue"); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	_, tok, _, err := driver.PopCtxReserved(context.Background(), "attempts-queue")
	if err != nil {
		t.Fatalf("PopCtxReserved failed: %v", err)
	}
	if tok.IsZero() {
		t.Fatal("expected non-zero reservation token")
	}
	if tok.Attempts != 1 {
		t.Fatalf("expected Attempts=1 after first pop, got %d", tok.Attempts)
	}

	// Release the lease back to the delayed heap with zero backoff so
	// the next pop reclaims it immediately.
	if err := driver.ReleaseCtx(context.Background(), tok, 0); err != nil {
		t.Fatalf("ReleaseCtx failed: %v", err)
	}

	// Give the heap promoter one tick to move the released wrapper
	// back to the main queue. The processDelayedJobs goroutine runs
	// on a 1s ticker, so poll until promotion has happened.
	deadline := time.Now().Add(3 * time.Second)
	var tok2 ReservationToken
	for time.Now().Before(deadline) {
		_, t2, _, err := driver.PopCtxReserved(context.Background(), "attempts-queue")
		if err != nil {
			t.Fatalf("PopCtxReserved (second) failed: %v", err)
		}
		if !t2.IsZero() {
			tok2 = t2
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if tok2.IsZero() {
		t.Fatal("expected a second pop to reclaim the released job within 3s")
	}
	if tok2.Attempts != 2 {
		t.Fatalf("expected Attempts=2 after second pop, got %d", tok2.Attempts)
	}
}

// TestMemoryDriver_NonIdentifiableWarningFiresOncePerType pins the
// advisory side of M-43: a non-Identifiable job triggers a one-time
// WARN per kind on first push, recommending the operator implement
// queue.Identifiable. The warning is intentionally one-shot so a
// chatty queue does not flood the log.
func TestMemoryDriver_NonIdentifiableWarningFiresOncePerType(t *testing.T) {
	driver := NewMemoryDriver()
	driver.Start()
	t.Cleanup(func() { _ = driver.Shutdown(context.Background()) })

	logger := &captureLogger{}
	driver.SetLogger(logger)

	// Push three jobs of the same non-Identifiable kind.
	for i := 0; i < 3; i++ {
		if err := driver.PushCtx(context.Background(), &failingAnonymousJob{Tag: fmt.Sprintf("a%d", i)}, "warn-queue"); err != nil {
			t.Fatalf("Push %d failed: %v", i, err)
		}
	}

	got := logger.countContaining("does not implement Identifiable")
	if got != 1 {
		t.Fatalf("expected exactly one Identifiable advisory, got %d", got)
	}
}

// TestMemoryDriver_IdentifiableJobsDoNotWarn pins the negative case:
// jobs that implement Identifiable are silent. A noisy advisory on
// every push would train operators to ignore the log channel.
func TestMemoryDriver_IdentifiableJobsDoNotWarn(t *testing.T) {
	driver := NewMemoryDriver()
	driver.Start()
	t.Cleanup(func() { _ = driver.Shutdown(context.Background()) })

	logger := &captureLogger{}
	driver.SetLogger(logger)

	if err := driver.PushCtx(context.Background(), &failingIdentifiableJob{ID: "ok-1"}, "silent-queue"); err != nil {
		t.Fatalf("Push failed: %v", err)
	}

	if got := logger.countContaining("does not implement Identifiable"); got != 0 {
		t.Fatalf("expected no Identifiable advisory for an Identifiable job, got %d", got)
	}
}
