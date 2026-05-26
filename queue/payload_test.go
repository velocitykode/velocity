package queue

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// crossProcessJob is a plain-data job whose Handle has an observable side
// effect (incrementing a registry-resolved atomic counter) so we can prove
// that the original handler logic ran on the consumer side, not a silent
// no-op stub. It deliberately has no closure / chan / func fields, mirroring
// the shape of a real production job.
type crossProcessJob struct {
	ID    string `json:"id"`
	Value int    `json:"value"`
}

// crossProcessRan counts how many times crossProcessJob.Handle has executed
// the real branch. Each registered factory grabs the producer-side pointer to
// this counter at registration time; if hydration silently substituted a stub
// (the pre-C-01 GenericJob path) the counter would stay at 0.
var crossProcessRan atomic.Int64

func (j *crossProcessJob) Handle() error {
	crossProcessRan.Add(1)
	return nil
}

func (j *crossProcessJob) Failed(err error) {}

// TestC01_DatabaseDriver_CrossProcessHydration is the headline regression
// test for finding C-01. It proves a job pushed via one DatabaseDriver
// instance can be popped and executed via a SECOND, independent
// DatabaseDriver instance that shares only the backing SQLite database (no
// shared package-global jobStore, no shared in-memory pointers).
//
// Before the fix, CreateJobWrapper stashed the live Job pointer in a
// package-level map keyed by an opaque job_id and wrote only that opaque
// id into Payload.Data. Because the registry/jobStore was process-local,
// a worker hydrating the wrapper from the DB row would miss the lookup and
// fall through to &GenericJob{}, whose Handle() returns nil. The worker
// reported success, deleted the row inside the same pop transaction (C-02
// territory), and the real job effects vanished without a trace.
//
// After the fix, the job is JSON-marshalled into Payload.Data at push time
// and reconstructed via the registry at pop time, so any worker on any
// process can run it.
func TestC01_DatabaseDriver_CrossProcessHydration(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)
	crossProcessRan.Store(0)

	// Register the factory exactly once, then construct TWO drivers against
	// the same backing DB. Each driver simulates a separate worker process.
	t.Cleanup(func() {
		registry.mu.Lock()
		delete(registry.handlers, "crossProcessJob")
		registry.mu.Unlock()
	})
	RegisterJob(func(data []byte) (*crossProcessJob, error) {
		j := &crossProcessJob{}
		if len(data) == 0 {
			return j, nil
		}
		if err := json.Unmarshal(data, j); err != nil {
			return nil, err
		}
		return j, nil
	})

	producer, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	// Re-use the same *sql.DB so the consumer sees the row, but build a
	// brand-new DatabaseDriver. This is the multi-process worker fleet model:
	// shared backing store, distinct process state. There is no shared in-
	// memory job pointer between producer and consumer, so cross-process
	// hydration MUST round-trip through the wire payload alone.
	consumer := NewDatabaseDriver(producer.db, "sqlite")

	original := &crossProcessJob{ID: "c01-proof", Value: 7}
	if err := producer.PushCtx(context.Background(), original, "c01-queue"); err != nil {
		t.Fatalf("push: %v", err)
	}

	popped, err := consumer.PopCtx(context.Background(), "c01-queue")
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if popped == nil {
		t.Fatal("pop returned nil; the only producer push must surface to the consumer")
	}

	// The hydrated job must be the real type, not a fallback stub.
	cpj, ok := popped.(*crossProcessJob)
	if !ok {
		t.Fatalf("pop returned %T, want *crossProcessJob (pre-fix would have returned *queue.GenericJob)", popped)
	}
	if cpj.ID != "c01-proof" {
		t.Errorf("hydrated ID = %q, want %q (state did not survive the wire)", cpj.ID, "c01-proof")
	}
	if cpj.Value != 7 {
		t.Errorf("hydrated Value = %d, want 7 (state did not survive the wire)", cpj.Value)
	}

	// Execute Handle and assert the real branch ran. This is the
	// behavioural test: the pre-fix GenericJob.Handle returned nil and did
	// nothing, so the counter would stay at 0.
	if err := popped.Handle(); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if got := crossProcessRan.Load(); got != 1 {
		t.Fatalf("crossProcessJob.Handle ran %d times, want 1 (pre-fix GenericJob stub would yield 0)", got)
	}

	// Belt-and-suspenders: payload bytes themselves must contain real job
	// state, not the {"job_id":"..."} stub the pre-fix code wrote. We push
	// a second job and inspect the row directly.
	if err := producer.PushCtx(context.Background(), &crossProcessJob{ID: "wire-shape", Value: 99}, "c01-shape"); err != nil {
		t.Fatalf("push wire-shape: %v", err)
	}
	row := producer.db.QueryRow("SELECT payload FROM jobs WHERE queue = ?", "c01-shape")
	var rawPayload string
	if err := row.Scan(&rawPayload); err != nil {
		t.Fatalf("scan payload: %v", err)
	}
	var wrapperOnTheWire JobWrapper
	if err := json.Unmarshal([]byte(rawPayload), &wrapperOnTheWire); err != nil {
		t.Fatalf("unmarshal wire payload: %v", err)
	}
	if wrapperOnTheWire.Payload == nil {
		t.Fatal("wire payload has no Payload section")
	}
	// Decode the inner Data and assert it has the real fields. The pre-fix
	// code wrote {"job_id":"job_<N>"} here; the fix writes the job's own
	// JSON.
	var inner crossProcessJob
	if err := json.Unmarshal(wrapperOnTheWire.Payload.Data, &inner); err != nil {
		t.Fatalf("unmarshal inner data %q: %v", string(wrapperOnTheWire.Payload.Data), err)
	}
	if inner.ID != "wire-shape" || inner.Value != 99 {
		t.Fatalf("wire payload Data did not carry job state: got %+v (raw=%s)", inner, string(wrapperOnTheWire.Payload.Data))
	}
}

// TestC01_DatabaseDriver_UnregisteredJobReturnsError documents the
// post-fix failure mode: a payload whose Type is not in the registry no
// longer silently degrades to a no-op GenericJob. It surfaces an error so
// the worker can route the row to failed_jobs / JobFailed observers.
//
// It also asserts the C-01 follow-up guarantee: the offending row is
// QUARANTINED (deleted from jobs, inserted into failed_jobs) before the
// error is returned, so the worker's next pop does not reselect the same
// poison row. The error is [ErrPoisonJob] so the worker can treat it as a
// recoverable pop error.
func TestC01_DatabaseDriver_UnregisteredJobReturnsError(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	// Push a TestJob, then steal its registry entry so the consumer side
	// cannot resolve it. (We restore at the end via t.Cleanup so we do not
	// disturb sibling tests that rely on the *queue.TestJob registration.)
	original := func(data []byte) (Job, error) {
		j := &TestJob{}
		if err := json.Unmarshal(data, j); err != nil {
			return nil, err
		}
		return j, nil
	}
	t.Cleanup(func() {
		Register("*queue.TestJob", original)
	})

	if err := driver.PushCtx(context.Background(), &TestJob{ID: "no-registry"}, "no-registry-queue"); err != nil {
		t.Fatalf("push: %v", err)
	}
	// Remove the registry entry to simulate a worker that has not registered
	// this job type.
	registry.mu.Lock()
	delete(registry.handlers, "TestJob")
	registry.mu.Unlock()

	job, err := driver.PopCtx(context.Background(), "no-registry-queue")
	if err == nil {
		t.Fatalf("pop returned no error; the pre-fix code silently produced a GenericJob stub (job=%T)", job)
	}
	if !errors.Is(err, ErrPoisonJob) {
		t.Errorf("pop error did not wrap ErrPoisonJob: %v", err)
	}
	if job != nil {
		t.Errorf("pop returned non-nil job alongside hydration error: %T", job)
	}

	// The row must have left `jobs` and landed in `failed_jobs`.
	var liveCount int
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = ?", "no-registry-queue").Scan(&liveCount); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if liveCount != 0 {
		t.Errorf("poison row left in jobs: count=%d (head-of-line starvation would resume)", liveCount)
	}
	var failedCount int
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM failed_jobs WHERE queue = ?", "no-registry-queue").Scan(&failedCount); err != nil {
		t.Fatalf("count failed_jobs: %v", err)
	}
	if failedCount != 1 {
		t.Errorf("poison row not recorded in failed_jobs: count=%d", failedCount)
	}
	// Sanity-check that the recorded exception carries the hydration error.
	var exception string
	if err := driver.db.QueryRow("SELECT exception FROM failed_jobs WHERE queue = ?", "no-registry-queue").Scan(&exception); err != nil {
		t.Fatalf("scan exception: %v", err)
	}
	if exception == "" {
		t.Error("failed_jobs exception column is empty; operator cannot tell why this row was quarantined")
	}
}

// TestC01_DatabaseDriver_PoisonRowDoesNotStarveFollowups is the C-01
// follow-up regression test for the head-of-line starvation bug found by
// review on commit 1862c82. Setup: push a poison row (job type that will
// fail hydration on this worker) followed by a healthy row. Before the
// quarantine fix the first Pop returned an error and left the poison row
// in place; the second Pop reselected the same row and got the same error
// indefinitely. After the fix the first Pop quarantines the poison row to
// failed_jobs, and the second Pop returns the healthy row.
func TestC01_DatabaseDriver_PoisonRowDoesNotStarveFollowups(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)
	crossProcessRan.Store(0)

	// Register crossProcessJob so the healthy row hydrates cleanly.
	t.Cleanup(func() {
		registry.mu.Lock()
		delete(registry.handlers, "crossProcessJob")
		registry.mu.Unlock()
	})
	RegisterJob(func(data []byte) (*crossProcessJob, error) {
		j := &crossProcessJob{}
		if len(data) == 0 {
			return j, nil
		}
		if err := json.Unmarshal(data, j); err != nil {
			return nil, err
		}
		return j, nil
	})

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	// Manufacture a poison row: write a JSON payload that points at a job
	// type the registry does not know. This simulates a deploy where the
	// producer pushed type X but the consumer process never registered X.
	poisonPayload := `{"payload":{"type":"VanishedJob","data":{"who":"cares"},"queue":"starvation-test","attempts":0,"created_at":"2026-05-26T00:00:00Z"}}`
	now := time.Now()
	if _, err := driver.db.Exec(
		`INSERT INTO jobs (queue, payload, attempts, scheduled_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"starvation-test", poisonPayload, 0, now.Add(-2*time.Second), now, now,
	); err != nil {
		t.Fatalf("insert poison: %v", err)
	}
	// Healthy follow-up row, scheduled later than the poison so the
	// scheduled_at-ordered SELECT returns the poison first.
	if err := driver.PushCtx(context.Background(), &crossProcessJob{ID: "healthy", Value: 1}, "starvation-test"); err != nil {
		t.Fatalf("push healthy: %v", err)
	}

	// First pop: must return the poison error AND quarantine the row.
	job1, err1 := driver.PopCtx(context.Background(), "starvation-test")
	if err1 == nil {
		t.Fatalf("first pop succeeded on poison row: job=%T", job1)
	}
	if !errors.Is(err1, ErrPoisonJob) {
		t.Errorf("first pop error not ErrPoisonJob: %v", err1)
	}
	if job1 != nil {
		t.Errorf("first pop returned non-nil job alongside poison error: %T", job1)
	}

	// Second pop: must return the healthy row, NOT the poison again.
	// Without the quarantine fix this returns the same poison error
	// forever (head-of-line starvation).
	job2, err2 := driver.PopCtx(context.Background(), "starvation-test")
	if err2 != nil {
		t.Fatalf("second pop errored instead of returning healthy row (starvation regression): %v", err2)
	}
	if job2 == nil {
		t.Fatal("second pop returned nil; healthy row was not delivered")
	}
	healthy, ok := job2.(*crossProcessJob)
	if !ok {
		t.Fatalf("second pop returned %T, want *crossProcessJob (the poison row leaked back?)", job2)
	}
	if healthy.ID != "healthy" {
		t.Errorf("hydrated ID = %q, want %q", healthy.ID, "healthy")
	}

	// Verify state: jobs is empty, failed_jobs holds the poison row.
	var jobsCount, failedCount int
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = ?", "starvation-test").Scan(&jobsCount); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobsCount != 0 {
		t.Errorf("jobs not drained: count=%d", jobsCount)
	}
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM failed_jobs WHERE queue = ?", "starvation-test").Scan(&failedCount); err != nil {
		t.Fatalf("count failed_jobs: %v", err)
	}
	if failedCount != 1 {
		t.Errorf("failed_jobs poison count = %d, want 1", failedCount)
	}
}

// TestC01_DatabaseDriver_MalformedJSONIsQuarantined exercises the second
// non-hydration quarantine path that C-01-fb4 introduced: a row whose
// stored payload is not even parseable as a JobWrapper. Before fb4 this
// returned a plain error and left the row live, head-of-line starving
// every other due job. After fb4 it routes through quarantineAndReturn:
// row moved to failed_jobs with the json-unmarshal error in the exception
// column, ErrPoisonJob returned, queue keeps moving.
func TestC01_DatabaseDriver_MalformedJSONIsQuarantined(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	// Insert a row whose payload column is not valid JSON.
	now := time.Now()
	if _, err := driver.db.Exec(
		`INSERT INTO jobs (queue, payload, attempts, scheduled_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"malformed-queue", `{this is not json at all`, 0, now.Add(-time.Second), now, now,
	); err != nil {
		t.Fatalf("insert malformed: %v", err)
	}

	job, popErr := driver.PopCtx(context.Background(), "malformed-queue")
	if popErr == nil {
		t.Fatalf("PopCtx accepted malformed payload: job=%T", job)
	}
	if !errors.Is(popErr, ErrPoisonJob) {
		t.Errorf("malformed-JSON pop error did not wrap ErrPoisonJob (worker would not treat as recoverable): %v", popErr)
	}
	if job != nil {
		t.Errorf("PopCtx returned non-nil job alongside malformed-JSON error: %T", job)
	}

	// Row gone from jobs, recorded in failed_jobs.
	var liveCount, failedCount int
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = ?", "malformed-queue").Scan(&liveCount); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if liveCount != 0 {
		t.Errorf("malformed row left live in jobs: count=%d (HOL starvation regression)", liveCount)
	}
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM failed_jobs WHERE queue = ?", "malformed-queue").Scan(&failedCount); err != nil {
		t.Fatalf("count failed_jobs: %v", err)
	}
	if failedCount != 1 {
		t.Fatalf("malformed row not recorded in failed_jobs: count=%d", failedCount)
	}
	var exception string
	if err := driver.db.QueryRow("SELECT exception FROM failed_jobs WHERE queue = ?", "malformed-queue").Scan(&exception); err != nil {
		t.Fatalf("scan exception: %v", err)
	}
	if exception == "" {
		t.Error("failed_jobs.exception empty; operator cannot diagnose the malformed payload")
	}
}

// TestC01_DatabaseDriver_PoisonRowSurvivesCallerCancellation documents the
// known caller-cancellation limitation of the C-01 quarantine path, driving
// the real PopCtxWithTrace end-to-end via the popQuarantineCommitHook
// test seam so a future regression in the pop function itself (mis-routing
// the Commit error, accidentally returning ErrPoisonJob on a cancelled
// commit, etc.) is caught.
//
// Setup: a poison row in `jobs`, no entries in `failed_jobs`. We install a
// hook that fires once: it cancels the caller ctx between the quarantine
// writes and tx.Commit(). database/sql keeps the BeginTx ctx tied to the
// transaction lifetime, so Commit fails with `context canceled` and the
// deferred Rollback discards the quarantine writes.
//
// Asserted invariants in the cancelled branch (driven by PopCtxWithTrace,
// NOT by direct calls to quarantinePoisonLocked):
//
//   - PopCtxWithTrace returns an error that is NOT ErrPoisonJob. The worker
//     thus does not falsely report a quarantine that never landed.
//   - The poison row remains live in `jobs` (deferred Rollback discarded
//     the DELETE).
//   - `failed_jobs` is unchanged (deferred Rollback discarded the INSERT).
//
// We then re-run PopCtxWithTrace with a fresh ctx (no hook) and assert
// quarantine completes correctly: row gone from `jobs`, row landed in
// `failed_jobs`, ErrPoisonJob returned. The worker's normal retry
// behaviour (any pop error -> back off -> pop again) closes the gap on
// its own.
//
// True caller-cancel-resistant quarantine would require rebeginning the
// tx with a detached ctx (or splitting quarantine into a second tx).
// Neither is implemented; this test guards against accidental regressions
// to the documented behaviour.
func TestC01_DatabaseDriver_PoisonRowSurvivesCallerCancellation(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	// Manufacture the poison row: a payload whose type is not in the
	// registry. Use the same on-wire shape PushCtx writes so the SELECT
	// branch happily picks it up.
	poisonPayload := `{"payload":{"type":"UnknownByDesign","data":{"k":"v"},"queue":"cancel-test","attempts":0,"created_at":"2026-05-26T00:00:00Z"}}`
	now := time.Now()
	if _, err := driver.db.Exec(
		`INSERT INTO jobs (queue, payload, attempts, scheduled_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"cancel-test", poisonPayload, 0, now.Add(-time.Second), now, now,
	); err != nil {
		t.Fatalf("insert poison: %v", err)
	}

	// Install the test hook: cancel the caller ctx exactly once, AFTER
	// quarantine writes succeed and BEFORE tx.Commit(). setPopQuarantineCommitHookForTest
	// returns a restore func that we register with t.Cleanup so sibling
	// tests are unaffected and so the hook is cleared even on early
	// t.Fatalf paths.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // safety net; the hook also calls cancel.

	var hookFired atomic.Int32
	restore := setPopQuarantineCommitHookForTest(func() {
		// Re-entrancy guard: PopCtxWithTrace must invoke the hook at
		// most once per call. If a future refactor accidentally invokes
		// it twice the second call sees hookFired > 0 and we surface
		// the bug rather than double-cancel.
		if hookFired.Add(1) != 1 {
			t.Errorf("popQuarantineCommitHook fired more than once: %d", hookFired.Load())
			return
		}
		cancel()
	})
	t.Cleanup(restore)

	job, popErr := driver.PopCtx(ctx, "cancel-test")
	if popErr == nil {
		t.Fatalf("PopCtx succeeded after caller ctx cancel: job=%T", job)
	}
	if errors.Is(popErr, ErrPoisonJob) {
		t.Errorf("PopCtx returned ErrPoisonJob despite the Commit being aborted: %v", popErr)
	}
	if job != nil {
		t.Errorf("PopCtx returned non-nil job alongside cancel error: %T", job)
	}
	if hookFired.Load() != 1 {
		t.Fatalf("hook did not fire: count=%d (test cannot guarantee it exercised the right path)", hookFired.Load())
	}

	// Assert: poison row still live in jobs, failed_jobs unchanged.
	var liveCount, failedCount int
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = ?", "cancel-test").Scan(&liveCount); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if liveCount != 1 {
		t.Errorf("after cancelled commit: jobs count = %d, want 1 (deferred Rollback should have preserved the row)", liveCount)
	}
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM failed_jobs WHERE queue = ?", "cancel-test").Scan(&failedCount); err != nil {
		t.Fatalf("count failed_jobs: %v", err)
	}
	if failedCount != 0 {
		t.Errorf("after cancelled commit: failed_jobs count = %d, want 0 (deferred Rollback should have discarded the INSERT)", failedCount)
	}

	// Symmetry check: PopCtx on a fresh ctx (no hook) must succeed in
	// quarantining the row. This proves the caller-cancel branch is not a
	// permanent quarantine-killer; the worker's normal retry behaviour
	// heals it on the next pop. We restore the hook to nil now (the
	// t.Cleanup-registered restore will run again on exit, which is fine:
	// reinstalling the same prior state twice is idempotent).
	restore()
	retryJob, retryErr := driver.PopCtx(context.Background(), "cancel-test")
	if retryErr == nil {
		t.Fatalf("retry pop succeeded on poison row: job=%T", retryJob)
	}
	if !errors.Is(retryErr, ErrPoisonJob) {
		t.Errorf("retry pop error did not wrap ErrPoisonJob: %v", retryErr)
	}
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = ?", "cancel-test").Scan(&liveCount); err != nil {
		t.Fatalf("count jobs after retry: %v", err)
	}
	if liveCount != 0 {
		t.Errorf("retry pop did not quarantine: jobs count = %d, want 0", liveCount)
	}
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM failed_jobs WHERE queue = ?", "cancel-test").Scan(&failedCount); err != nil {
		t.Fatalf("count failed_jobs after retry: %v", err)
	}
	if failedCount != 1 {
		t.Errorf("retry pop did not record: failed_jobs count = %d, want 1", failedCount)
	}
}
