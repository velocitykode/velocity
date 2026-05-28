package queue

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	testsync "github.com/velocitykode/velocity/testing"
)

// flakyDriver is a queue driver that fails the first N PushCtx calls
// and then succeeds. Used by the reaper recovery tests to simulate a
// transient Redis outage / DB hiccup. PushCtx is the only method the
// reaper exercises; the other Driver methods exist so the type still
// satisfies the interface but are not asserted on.
type flakyDriver struct {
	mu          sync.Mutex
	failCount   int // how many initial PushCtx calls to fail
	pushedJobs  []Job
	pushedNames []string
}

func newFlakyDriver(failFirstN int) *flakyDriver {
	return &flakyDriver{failCount: failFirstN}
}

func (d *flakyDriver) PushCtx(ctx context.Context, job Job, queue ...string) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.failCount > 0 {
		d.failCount--
		return &stringErr{"flaky driver: simulated PushCtx failure"}
	}
	q := "default"
	if len(queue) > 0 {
		q = queue[0]
	}
	d.pushedJobs = append(d.pushedJobs, job)
	d.pushedNames = append(d.pushedNames, q)
	return nil
}

func (d *flakyDriver) PushDelayedCtx(ctx context.Context, job Job, delay time.Duration, queue ...string) error {
	return d.PushCtx(ctx, job, queue...)
}
func (d *flakyDriver) PopCtx(ctx context.Context, queue string) (Job, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.pushedJobs) == 0 {
		return nil, nil
	}
	job := d.pushedJobs[0]
	d.pushedJobs = d.pushedJobs[1:]
	return job, nil
}
func (d *flakyDriver) Failed(Job, error, string) error                     { return nil }
func (d *flakyDriver) FailedCtx(context.Context, Job, error, string) error { return nil }
func (d *flakyDriver) Size(string) (int64, error)                          { return 0, nil }
func (d *flakyDriver) SizeCtx(context.Context, string) (int64, error)      { return 0, nil }
func (d *flakyDriver) Clear(string) error                                  { return nil }
func (d *flakyDriver) ClearCtx(context.Context, string) error              { return nil }
func (d *flakyDriver) Shutdown(context.Context) error                      { return nil }

func (d *flakyDriver) successCount() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.pushedJobs)
}

type stringErr struct{ s string }

func (e *stringErr) Error() string { return e.s }

// TestReaper_RetriesAfterEnqueueFailure simulates a transient queue
// outage: the first PushCtx attempt fails, leaving the row's
// then_dispatched flag at false. The reaper sweeps every ~50ms and
// must re-attempt the enqueue, succeeding on the second call. The
// handler then runs exactly once.
func TestReaper_RetriesAfterEnqueueFailure(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	resetBatchStoreForTest(t)
	ResetBatchCallbacksForTest()

	// Tiny reaper interval so the test runs in milliseconds, not 15s.
	repo, err := NewDatabaseBatchRepositoryWithReaperInterval(db, "sqlite", 50*time.Millisecond)
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	prev := DefaultBatchRepository()
	SetDefaultBatchRepository(repo)
	t.Cleanup(func() { SetDefaultBatchRepository(prev) })

	// Driver fails the FIRST PushCtx call (the inline one from
	// dispatchBatchCallbackJob) and succeeds on the second (the reaper
	// retry). Two attempts proves the reaper actually retries; if it
	// did not, the handler would never fire because the dispatched
	// flag never flips.
	flaky := newFlakyDriver(1)
	SetBatchCallbackQueue(flaky, "default")
	t.Cleanup(func() { ResetBatchCallbackQueueForTest() })

	var handlerFired atomic.Int32
	RegisterBatchCallback("notify-then", func(ctx context.Context, b *Batch) error {
		handlerFired.Add(1)
		return nil
	})

	// Spin up a worker that runs the BatchCallbackJob after the reaper
	// successfully enqueues it.
	callbackWorker := NewWorker(flaky, "default", func(j Job) error { return j.Handle() },
		WithInterval(5*time.Millisecond), WithMaxRetries(0))
	callbackWorker.Start(context.Background())
	t.Cleanup(callbackWorker.Stop)

	jobsDriver := newMemoryDriver()
	batch, err := NewBatch(&testBatchJob{}).
		OnComplete("notify-then").
		Dispatch(context.Background(), jobsDriver)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	batch.recordSuccess(context.Background())

	// The reaper picks up the row whose then_dispatched=false and
	// retries. Within a few ticks the second PushCtx succeeds and the
	// handler runs once. Wait long enough to cover at least 3 reaper
	// ticks so a one-tick race does not flake the test.
	testsync.Eventually(t, func() bool { return handlerFired.Load() == 1 }, 2*time.Second,
		"reaper retries enqueue after first failure and handler runs")

	// Stability window: confirm the handler does NOT fire a second
	// time after dispatched=true.
	time.Sleep(200 * time.Millisecond)
	if handlerFired.Load() != 1 {
		t.Errorf("handler fired %d times, want exactly 1", handlerFired.Load())
	}

	// And the row should now have then_dispatched=true.
	got, _ := repo.Find(context.Background(), batch.ID())
	if !got.thenDispatched.Load() {
		t.Error("expected then_dispatched=true after successful retry")
	}
}

// TestReaper_RecoversCallbackAfterDispatcherCrash simulates the worst
// case: the terminal CAS UPDATE succeeded but the dispatcher process
// crashed before PushCtx ran. The job_batches row has completed_at
// set and then_dispatched=false; the reaper running on a recovered
// process must re-attempt the enqueue.
//
// To simulate the crash we skip the inline dispatch entirely: we
// directly INSERT a row with completed_at set and then_dispatched
// false, then start a reaper and assert the callback fires.
func TestReaper_RecoversCallbackAfterDispatcherCrash(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	resetBatchStoreForTest(t)
	ResetBatchCallbacksForTest()

	// First repo: we use it to write the "post-crash" row.
	writerRepo, _ := NewDatabaseBatchRepositoryWithReaperInterval(db, "sqlite", 0) // reaper disabled
	defer writerRepo.Close()

	id := newBatchID()
	now := time.Now()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO job_batches (
			id, total_jobs, pending_jobs, completed_jobs, failed_jobs,
			allow_failures, queue, then_callback, catch_callback, finally_callback,
			then_dispatched, catch_dispatched, finally_dispatched,
			cancelled_at, completed_at, last_error, created_at, updated_at
		) VALUES (?, 2, 0, 2, 0, 0, 'default', ?, NULL, NULL, 0, 0, 0, NULL, ?, NULL, ?, ?)`,
		string(id), "crash-recovery-then", now, now, now)
	if err != nil {
		t.Fatalf("seed crashed batch row: %v", err)
	}

	// Second repo: this is the "recovered process" with reaper enabled.
	repo, _ := NewDatabaseBatchRepositoryWithReaperInterval(db, "sqlite", 50*time.Millisecond)
	prev := DefaultBatchRepository()
	SetDefaultBatchRepository(repo)
	t.Cleanup(func() { SetDefaultBatchRepository(prev) })

	driver := newMemoryDriver()
	SetBatchCallbackQueue(driver, "default")
	t.Cleanup(func() { ResetBatchCallbackQueueForTest() })

	var recovered atomic.Int32
	RegisterBatchCallback("crash-recovery-then", func(ctx context.Context, b *Batch) error {
		recovered.Add(1)
		return nil
	})

	worker := NewWorker(driver, "default", func(j Job) error { return j.Handle() },
		WithInterval(5*time.Millisecond), WithMaxRetries(0))
	worker.Start(context.Background())
	t.Cleanup(worker.Stop)

	testsync.Eventually(t, func() bool { return recovered.Load() == 1 }, 2*time.Second,
		"reaper enqueues callback for orphaned completed-but-undispatched row")

	// then_dispatched must now be true so the reaper doesn't keep
	// retrying.
	got, _ := repo.Find(context.Background(), id)
	if got == nil {
		t.Fatal("batch row missing")
	}
	if !got.thenDispatched.Load() {
		t.Error("then_dispatched should be true after successful reaper recovery")
	}
}

// TestReaper_DispatchedFlagIsIdempotent confirms the reaper does not
// re-enqueue a callback whose dispatched column is already true. This
// is the steady-state guarantee that prevents a runaway reaper from
// spamming the queue with duplicate callbacks.
func TestReaper_DispatchedFlagIsIdempotent(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	resetBatchStoreForTest(t)
	ResetBatchCallbacksForTest()

	repo, _ := NewDatabaseBatchRepositoryWithReaperInterval(db, "sqlite", 50*time.Millisecond)
	prev := DefaultBatchRepository()
	SetDefaultBatchRepository(repo)
	t.Cleanup(func() { SetDefaultBatchRepository(prev) })

	// Use flakyDriver with failCount=0 so we can inspect successCount
	// directly; memoryDriver lacks an inspection accessor.
	driver := newFlakyDriver(0)
	SetBatchCallbackQueue(driver, "default")
	t.Cleanup(func() { ResetBatchCallbackQueueForTest() })

	// Seed a completed batch with then_dispatched ALREADY true so the
	// reaper has nothing to do.
	id := newBatchID()
	now := time.Now()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO job_batches (
			id, total_jobs, pending_jobs, completed_jobs, failed_jobs,
			allow_failures, queue, then_callback, catch_callback, finally_callback,
			then_dispatched, catch_dispatched, finally_dispatched,
			cancelled_at, completed_at, last_error, created_at, updated_at
		) VALUES (?, 1, 0, 1, 0, 0, 'default', ?, NULL, NULL, 1, 0, 0, NULL, ?, NULL, ?, ?)`,
		string(id), "idempotent-then", now, now, now)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Wait several reaper ticks. If the reaper ignored dispatched=true
	// we would see N callback jobs pushed to the driver.
	time.Sleep(300 * time.Millisecond)

	if got := driver.successCount(); got != 0 {
		t.Errorf("reaper pushed %d callback jobs for a dispatched=true row; want 0", got)
	}
}

// TestReaper_StopsOnClose makes sure the reaper goroutine exits when
// Close is called. Without this, two velocity.New cycles in one
// process would leak a reaper per cycle and silently retry against
// torn-down DB pools.
func TestReaper_StopsOnClose(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	repo, _ := NewDatabaseBatchRepositoryWithReaperInterval(db, "sqlite", 50*time.Millisecond)

	// Close: must return promptly (within ~100ms) which means the
	// reaper observed the stop signal and exited.
	done := make(chan struct{})
	go func() {
		_ = repo.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close did not return within 2s; reaper likely leaked")
	}

	// Post-close mutating ops should fail.
	if err := repo.MarkCallbackDispatched(context.Background(), "nope", CallbackThen); err == nil {
		t.Error("MarkCallbackDispatched on closed repo must error")
	}
}

// TestReaper_DisabledWhenIntervalZero is a sanity check that the
// reaper-disabled path does not start a goroutine. Useful for tests
// that want to control the dispatch flow manually via reaperTick.
func TestReaper_DisabledWhenIntervalZero(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	repo, err := NewDatabaseBatchRepositoryWithReaperInterval(db, "sqlite", 0)
	if err != nil {
		t.Fatalf("repo: %v", err)
	}
	defer repo.Close()

	if repo.reaperStarted.Load() {
		t.Error("reaperStarted must be false when interval is 0")
	}
}

// TestReaper_FindUndispatchedCallbacks_FiltersDispatchedRows asserts
// the SELECT predicate logic: rows with all three dispatched columns
// true are excluded; rows with at least one dispatched=false and an
// eligible state are included.
func TestReaper_FindUndispatchedCallbacks_FiltersDispatchedRows(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	repo, _ := NewDatabaseBatchRepositoryWithReaperInterval(db, "sqlite", 0)
	defer repo.Close()

	now := time.Now()

	// Row 1: then_callback set, dispatched=false, completed. Eligible.
	_, _ = db.ExecContext(context.Background(),
		`INSERT INTO job_batches (
			id, total_jobs, pending_jobs, completed_jobs, failed_jobs,
			allow_failures, queue, then_callback, catch_callback, finally_callback,
			then_dispatched, catch_dispatched, finally_dispatched,
			cancelled_at, completed_at, last_error, created_at, updated_at
		) VALUES ('row1', 1, 0, 1, 0, 0, 'default', 'cb1', NULL, NULL, 0, 0, 0, NULL, ?, NULL, ?, ?)`,
		now, now, now)

	// Row 2: then_callback set but already dispatched. Excluded.
	_, _ = db.ExecContext(context.Background(),
		`INSERT INTO job_batches (
			id, total_jobs, pending_jobs, completed_jobs, failed_jobs,
			allow_failures, queue, then_callback, catch_callback, finally_callback,
			then_dispatched, catch_dispatched, finally_dispatched,
			cancelled_at, completed_at, last_error, created_at, updated_at
		) VALUES ('row2', 1, 0, 1, 0, 0, 'default', 'cb2', NULL, NULL, 1, 0, 0, NULL, ?, NULL, ?, ?)`,
		now, now, now)

	// Row 3: catch_callback with failures. Eligible for catch
	// regardless of completion.
	_, _ = db.ExecContext(context.Background(),
		`INSERT INTO job_batches (
			id, total_jobs, pending_jobs, completed_jobs, failed_jobs,
			allow_failures, queue, then_callback, catch_callback, finally_callback,
			then_dispatched, catch_dispatched, finally_dispatched,
			cancelled_at, completed_at, last_error, created_at, updated_at
		) VALUES ('row3', 2, 1, 0, 1, 1, 'default', NULL, 'cb3', NULL, 0, 0, 0, NULL, NULL, 'boom', ?, ?)`,
		now, now)

	// Row 4: finally_callback set, undispatched, completed. Eligible.
	_, _ = db.ExecContext(context.Background(),
		`INSERT INTO job_batches (
			id, total_jobs, pending_jobs, completed_jobs, failed_jobs,
			allow_failures, queue, then_callback, catch_callback, finally_callback,
			then_dispatched, catch_dispatched, finally_dispatched,
			cancelled_at, completed_at, last_error, created_at, updated_at
		) VALUES ('row4', 1, 0, 1, 0, 0, 'default', NULL, NULL, 'cb4', 0, 0, 0, NULL, ?, NULL, ?, ?)`,
		now, now, now)

	rows, err := repo.FindUndispatchedCallbacks(context.Background(), 100)
	if err != nil {
		t.Fatalf("find: %v", err)
	}

	want := map[string]CallbackKind{
		"row1:then":    CallbackThen,
		"row3:catch":   CallbackCatch,
		"row4:finally": CallbackFinally,
	}
	got := make(map[string]CallbackKind, len(rows))
	for _, r := range rows {
		key := string(r.BatchID) + ":" + string(r.Kind)
		got[key] = r.Kind
	}
	if len(got) != len(want) {
		t.Fatalf("rows: got %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("missing %s: got %v", k, got)
		}
	}
}
