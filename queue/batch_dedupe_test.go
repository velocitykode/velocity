package queue

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	testsync "github.com/velocitykode/velocity/testing"
)

// ----- deterministic dedupe key contract -----------------------------------

// TestBatchCallbackJob_DedupeKey_IsDeterministic asserts that two
// BatchCallbackJob values with the same (BatchID, Kind) produce
// byte-identical DedupeKey strings. This is the load-bearing
// guarantee that the queue-layer dedupe relies on: a reaper retry
// reconstructs the job from the persisted (batchID, kind, name) tuple
// and must derive the same key as the original dispatch.
func TestBatchCallbackJob_DedupeKey_IsDeterministic(t *testing.T) {
	a := &BatchCallbackJob{BatchID: "batch_abc", Kind: CallbackThen, Name: "send-email"}
	b := &BatchCallbackJob{BatchID: "batch_abc", Kind: CallbackThen, Name: "different-name"}
	if a.DedupeKey() != b.DedupeKey() {
		t.Errorf("DedupeKey must depend ONLY on (BatchID, Kind); got %q vs %q", a.DedupeKey(), b.DedupeKey())
	}

	// Different batch -> different key.
	c := &BatchCallbackJob{BatchID: "batch_xyz", Kind: CallbackThen, Name: "send-email"}
	if a.DedupeKey() == c.DedupeKey() {
		t.Error("DedupeKey must change when BatchID changes")
	}

	// Different kind -> different key.
	d := &BatchCallbackJob{BatchID: "batch_abc", Kind: CallbackFinally, Name: "send-email"}
	if a.DedupeKey() == d.DedupeKey() {
		t.Error("DedupeKey must change when Kind changes")
	}

	// JobID matches DedupeKey for queue-layer dedupe symmetry.
	if a.JobID() != a.DedupeKey() {
		t.Error("JobID and DedupeKey must agree so the worker's attempt counter dedups too")
	}
}

// ----- memory driver dedupe surface ----------------------------------------

// TestMemoryDriver_PushIfNotExistsCtx_NoDoubleInsert verifies the
// memory driver's at-most-once contract: two pushes with the same key
// produce exactly one queued row. Size after both calls is 1.
func TestMemoryDriver_PushIfNotExistsCtx_NoDoubleInsert(t *testing.T) {
	drv := NewMemoryDriver()
	drv.Start()
	defer drv.Shutdown(context.Background())

	job1 := &BatchCallbackJob{BatchID: "batch_abc", Kind: CallbackThen, Name: "noop"}
	job2 := &BatchCallbackJob{BatchID: "batch_abc", Kind: CallbackThen, Name: "noop"}

	if err := drv.PushIfNotExistsCtx(context.Background(), job1, job1.DedupeKey(), "default"); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if err := drv.PushIfNotExistsCtx(context.Background(), job2, job2.DedupeKey(), "default"); err != nil {
		t.Fatalf("second push (must be no-op): %v", err)
	}

	size, _ := drv.Size("default")
	if size != 1 {
		t.Errorf("expected exactly 1 queued job after dedup'd double push, got %d", size)
	}
}

// TestMemoryDriver_PushIfNotExistsCtx_StaleAfterPop is the critical
// post-Pop test: after Pop consumes the job, the dedupe key is HELD
// (not released), so a follow-up PushIfNotExistsCtx for the same key
// is still a no-op. This is what prevents a stale reaper retry from
// re-running the user callback after the original execution.
func TestMemoryDriver_PushIfNotExistsCtx_StaleAfterPop(t *testing.T) {
	drv := NewMemoryDriver()
	drv.Start()
	defer drv.Shutdown(context.Background())

	job := &BatchCallbackJob{BatchID: "batch_pop", Kind: CallbackThen, Name: "noop"}
	if err := drv.PushIfNotExistsCtx(context.Background(), job, job.DedupeKey(), "default"); err != nil {
		t.Fatalf("push: %v", err)
	}
	popped, err := drv.PopCtx(context.Background(), "default")
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if popped == nil {
		t.Fatal("pop returned nil")
	}

	// Second push with same key after Pop: must remain a no-op.
	if err := drv.PushIfNotExistsCtx(context.Background(), job, job.DedupeKey(), "default"); err != nil {
		t.Fatalf("re-push after pop: %v", err)
	}
	size, _ := drv.Size("default")
	if size != 0 {
		t.Errorf("re-push after pop must NOT insert a fresh row; got size=%d", size)
	}
}

// TestMemoryDriver_PushIfNotExistsCtx_DifferentKeysCoexist confirms
// that pushes with different dedupe keys insert distinct rows even
// when the underlying job payload is identical.
func TestMemoryDriver_PushIfNotExistsCtx_DifferentKeysCoexist(t *testing.T) {
	drv := NewMemoryDriver()
	drv.Start()
	defer drv.Shutdown(context.Background())

	job1 := &BatchCallbackJob{BatchID: "batch_a", Kind: CallbackThen, Name: "noop"}
	job2 := &BatchCallbackJob{BatchID: "batch_b", Kind: CallbackThen, Name: "noop"}

	_ = drv.PushIfNotExistsCtx(context.Background(), job1, job1.DedupeKey(), "default")
	_ = drv.PushIfNotExistsCtx(context.Background(), job2, job2.DedupeKey(), "default")

	size, _ := drv.Size("default")
	if size != 2 {
		t.Errorf("distinct dedupe keys must coexist; got size=%d", size)
	}
}

// ----- database driver dedupe surface --------------------------------------

// TestDatabaseDriver_PushIfNotExistsCtx_ONCONFLICTNoDoubleInsert mirrors
// the memory driver test but exercises the SQL INSERT OR IGNORE path.
// The job_dedupe PRIMARY KEY enforces at-most-one row per key; the
// second PushIfNotExistsCtx returns nil without writing a duplicate
// jobs row.
func TestDatabaseDriver_PushIfNotExistsCtx_ONCONFLICTNoDoubleInsert(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT, queue TEXT, payload TEXT,
		attempts INTEGER, scheduled_at DATETIME, reserved_at DATETIME,
		reserved_by TEXT, failed_at DATETIME, failed_reason TEXT,
		created_at DATETIME, updated_at DATETIME)`); err != nil {
		t.Fatalf("jobs table: %v", err)
	}

	drv := NewDatabaseDriver(db, "sqlite")
	job := &BatchCallbackJob{BatchID: "batch_dup", Kind: CallbackThen, Name: "noop"}

	if err := drv.PushIfNotExistsCtx(context.Background(), job, job.DedupeKey(), "default"); err != nil {
		t.Fatalf("first push: %v", err)
	}
	if err := drv.PushIfNotExistsCtx(context.Background(), job, job.DedupeKey(), "default"); err != nil {
		t.Fatalf("second push (must be no-op): %v", err)
	}

	// Count jobs rows manually since the database driver's Size uses
	// reserved_at predicate that may not match here.
	var count int
	_ = db.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&count)
	if count != 1 {
		t.Errorf("expected exactly 1 jobs row after dedup'd double push, got %d", count)
	}
	var dedupeCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM job_dedupe").Scan(&dedupeCount)
	if dedupeCount != 1 {
		t.Errorf("expected exactly 1 job_dedupe row, got %d", dedupeCount)
	}
}

// TestDatabaseDriver_PushIfNotExistsCtx_StaleAfterPop is the database
// equivalent of the memory test: the dedupe row outlives Pop, so a
// stale reaper retry cannot re-insert.
func TestDatabaseDriver_PushIfNotExistsCtx_StaleAfterPop(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT, queue TEXT NOT NULL, payload TEXT NOT NULL,
		attempts INTEGER NOT NULL DEFAULT 0, scheduled_at DATETIME NOT NULL,
		reserved_at DATETIME, reserved_by TEXT, failed_at DATETIME, failed_reason TEXT,
		created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`); err != nil {
		t.Fatalf("jobs table: %v", err)
	}

	drv := NewDatabaseDriver(db, "sqlite")
	// BatchCallbackJob is registered globally by the queue package's
	// init() in batch_callback.go; do NOT re-register here. An earlier
	// version of this test installed a no-op factory that dropped
	// payload bytes on the floor, which polluted the global registry
	// and made every later test that popped a BatchCallbackJob see an
	// empty struct (then HandleCtx's Find returned nil and the test
	// silently no-op'd).

	job := &BatchCallbackJob{BatchID: "batch_pop_db", Kind: CallbackThen, Name: "noop"}
	if err := drv.PushIfNotExistsCtx(context.Background(), job, job.DedupeKey(), "default"); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Disable signing for this test so the verifier accepts our
	// payload without setup. SetSigningKey(nil) is the public toggle.
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	popped, err := drv.PopCtx(context.Background(), "default")
	if err != nil {
		t.Fatalf("pop: %v", err)
	}
	if popped == nil {
		t.Fatal("pop returned nil")
	}

	// Pop deleted the jobs row but MUST NOT have deleted the job_dedupe row.
	var dedupeCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM job_dedupe").Scan(&dedupeCount)
	if dedupeCount != 1 {
		t.Errorf("job_dedupe row must persist past Pop; got %d", dedupeCount)
	}

	// Re-push: must no-op against the still-present dedupe row.
	if err := drv.PushIfNotExistsCtx(context.Background(), job, job.DedupeKey(), "default"); err != nil {
		t.Fatalf("re-push after pop: %v", err)
	}
	var jobsCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&jobsCount)
	if jobsCount != 0 {
		t.Errorf("re-push after pop must NOT insert a fresh jobs row; got %d", jobsCount)
	}
}

// ----- end-to-end: push succeeds, mark fails, reaper does not duplicate ----

// markFailingRepo wraps a real BatchRepository but makes
// MarkCallbackDispatched always return an error. This simulates the
// network blip / process crash between push and mark that drove the
// fb4 finding.
type markFailingRepo struct {
	inner BatchRepository
}

func (r *markFailingRepo) Find(ctx context.Context, id BatchID) (*Batch, error) {
	return r.inner.Find(ctx, id)
}
func (r *markFailingRepo) Save(ctx context.Context, b *Batch) error { return r.inner.Save(ctx, b) }
func (r *markFailingRepo) IncrementSuccess(ctx context.Context, id BatchID) (*Batch, bool, error) {
	return r.inner.IncrementSuccess(ctx, id)
}
func (r *markFailingRepo) IncrementFailure(ctx context.Context, id BatchID, err error) (*Batch, bool, error) {
	return r.inner.IncrementFailure(ctx, id, err)
}
func (r *markFailingRepo) Cancel(ctx context.Context, id BatchID) (*Batch, error) {
	return r.inner.Cancel(ctx, id)
}
func (r *markFailingRepo) DecrementPending(ctx context.Context, id BatchID) (*Batch, bool, error) {
	return r.inner.DecrementPending(ctx, id)
}
func (r *markFailingRepo) Delete(ctx context.Context, id BatchID) error {
	return r.inner.Delete(ctx, id)
}
func (r *markFailingRepo) PruneStale(ctx context.Context, d time.Duration) (int, error) {
	return r.inner.PruneStale(ctx, d)
}
func (r *markFailingRepo) MarkCallbackDispatched(ctx context.Context, id BatchID, kind CallbackKind) error {
	return &stringErr2{"simulated MarkCallbackDispatched failure"}
}
func (r *markFailingRepo) FindUndispatchedCallbacks(ctx context.Context, limit int) ([]UndispatchedCallback, error) {
	return r.inner.FindUndispatchedCallbacks(ctx, limit)
}
func (r *markFailingRepo) Close() error { return r.inner.Close() }

type stringErr2 struct{ s string }

func (e *stringErr2) Error() string { return e.s }

// TestReaper_PushSucceedsMarkFails_DoesNotDoubleEnqueue is the
// load-bearing C-03 fb4 regression. dispatchBatchCallbackJob is
// invoked through a repository wrapper whose MarkCallbackDispatched
// always fails. We then INVOKE THE DISPATCH PATH MULTIPLE TIMES
// (simulating the reaper-retry sequence: each tick re-derives the
// callback intent from the unchanged then_dispatched=false row).
// Even though the wrapper never marks the flag, the deterministic
// dedupe key in PushIfNotExistsCtx keeps the jobs and job_dedupe
// tables at exactly one row each. Without the dedupe key, each retry
// would insert a fresh jobs row.
func TestReaper_PushSucceedsMarkFails_DoesNotDoubleEnqueue(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT, queue TEXT NOT NULL, payload TEXT NOT NULL,
		attempts INTEGER NOT NULL DEFAULT 0, scheduled_at DATETIME NOT NULL,
		reserved_at DATETIME, reserved_by TEXT, failed_at DATETIME, failed_reason TEXT,
		created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`); err != nil {
		t.Fatalf("jobs table: %v", err)
	}

	resetBatchStoreForTest(t)
	ResetBatchCallbacksForTest()

	// Inner repo, reaper disabled so we drive the retry sequence
	// manually via dispatchBatchCallbackJob (mirroring what the reaper
	// would do under sustained MarkCallback failures).
	innerRepo, _ := NewDatabaseBatchRepositoryWithReaperInterval(db, "sqlite", 0)
	prev := DefaultBatchRepository()
	failing := &markFailingRepo{inner: innerRepo}
	SetDefaultBatchRepository(failing)
	t.Cleanup(func() { SetDefaultBatchRepository(prev) })

	saveAndRestoreSigningState(t)
	SetSigningKey(nil)
	dbDriver := NewDatabaseDriver(db, "sqlite")
	SetBatchCallbackQueue(dbDriver, "default")
	t.Cleanup(func() { ResetBatchCallbackQueueForTest() })

	RegisterBatchCallback("noop-then", func(ctx context.Context, b *Batch) error { return nil })

	// Seed a completed batch with then_callback set, then_dispatched=false.
	id := newBatchID()
	now := time.Now()
	_, err := db.ExecContext(context.Background(),
		`INSERT INTO job_batches (
			id, total_jobs, pending_jobs, completed_jobs, failed_jobs,
			allow_failures, queue, then_callback, catch_callback, finally_callback,
			then_dispatched, catch_dispatched, finally_dispatched,
			cancelled_at, completed_at, last_error, created_at, updated_at
		) VALUES (?, 1, 0, 1, 0, 0, 'default', ?, NULL, NULL, 0, 0, 0, NULL, ?, NULL, ?, ?)`,
		string(id), "noop-then", now, now, now)
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Invoke the dispatch path FIVE times (each simulates one reaper
	// tick under sustained MarkCallback failures). Each call pushes
	// via PushIfNotExistsCtx and then MarkCallback (which fails via
	// the wrapper).
	for i := 0; i < 5; i++ {
		dispatchBatchCallbackJob(context.Background(), "noop-then", CallbackThen, id, "")
	}

	// Wait for the async push goroutines to drain. dispatchBatchCallbackJob
	// uses async.Go, so we need to let the runtime schedule them.
	testsync.Eventually(t, func() bool {
		var c int
		_ = db.QueryRow("SELECT COUNT(*) FROM job_dedupe").Scan(&c)
		return c >= 1
	}, 2*time.Second, "at least one push lands in job_dedupe")

	// Stability window so any straggling push has time to land.
	time.Sleep(200 * time.Millisecond)

	var jobsRows int
	_ = db.QueryRow("SELECT COUNT(*) FROM jobs").Scan(&jobsRows)
	if jobsRows != 1 {
		t.Errorf("expected exactly 1 jobs row after 5 dispatch attempts with failed MarkCallback; got %d", jobsRows)
	}
	var dedupeRows int
	_ = db.QueryRow("SELECT COUNT(*) FROM job_dedupe").Scan(&dedupeRows)
	if dedupeRows != 1 {
		t.Errorf("expected exactly 1 job_dedupe row; got %d", dedupeRows)
	}
}

// TestReaper_ProcessCrashAfterPush_DedupeOnRecover simulates a crash
// after PushIfNotExistsCtx succeeded but before MarkCallbackDispatched
// ran. Specifically: insert a job_dedupe row and a jobs row directly
// (simulating the post-crash state) with then_dispatched=false. Start
// a fresh reaper. Assert: the reaper's first push attempt returns
// success (the dedupe row matches), MarkCallback runs, exactly one
// callback handler execution occurs when the original queue row is
// finally popped by a worker.
func TestReaper_ProcessCrashAfterPush_DedupeOnRecover(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT, queue TEXT NOT NULL, payload TEXT NOT NULL,
		attempts INTEGER NOT NULL DEFAULT 0, scheduled_at DATETIME NOT NULL,
		reserved_at DATETIME, reserved_by TEXT, failed_at DATETIME, failed_reason TEXT,
		created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`); err != nil {
		t.Fatalf("jobs table: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS failed_jobs (
		id INTEGER PRIMARY KEY AUTOINCREMENT, queue TEXT NOT NULL, payload TEXT NOT NULL,
		exception TEXT NOT NULL, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`); err != nil {
		t.Fatalf("failed_jobs table: %v", err)
	}

	resetBatchStoreForTest(t)
	ResetBatchCallbacksForTest()
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	repo, _ := NewDatabaseBatchRepositoryWithReaperInterval(db, "sqlite", 50*time.Millisecond)
	prev := DefaultBatchRepository()
	SetDefaultBatchRepository(repo)
	t.Cleanup(func() { SetDefaultBatchRepository(prev) })

	dbDriver := NewDatabaseDriver(db, "sqlite")
	SetBatchCallbackQueue(dbDriver, "default")
	t.Cleanup(func() { ResetBatchCallbackQueueForTest() })

	// Seed a batch and pre-populate the post-crash state: jobs row +
	// job_dedupe row exist but then_dispatched=false.
	id := newBatchID()
	now := time.Now()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO job_batches (
			id, total_jobs, pending_jobs, completed_jobs, failed_jobs,
			allow_failures, queue, then_callback, catch_callback, finally_callback,
			then_dispatched, catch_dispatched, finally_dispatched,
			cancelled_at, completed_at, last_error, created_at, updated_at
		) VALUES (?, 1, 0, 1, 0, 0, 'default', ?, NULL, NULL, 0, 0, 0, NULL, ?, NULL, ?, ?)`,
		string(id), "crash-then", now, now, now); err != nil {
		t.Fatalf("seed batch: %v", err)
	}

	preCrashJob := &BatchCallbackJob{
		BatchID: id, Kind: CallbackThen, Name: "crash-then",
	}
	if err := dbDriver.PushIfNotExistsCtx(context.Background(),
		preCrashJob, preCrashJob.DedupeKey(), "default"); err != nil {
		t.Fatalf("simulated pre-crash push: %v", err)
	}

	// Register the handler.
	var handlerRuns atomic.Int32
	RegisterBatchCallback("crash-then", func(ctx context.Context, b *Batch) error {
		handlerRuns.Add(1)
		return nil
	})

	// Spin up a worker that pops and runs callback jobs.
	worker := NewWorker(dbDriver, "default", func(j Job) error { return j.Handle() },
		WithInterval(10*time.Millisecond), WithMaxRetries(0))
	worker.Start(context.Background())
	t.Cleanup(worker.Stop)

	// The reaper sees then_dispatched=false, calls PushIfNotExistsCtx
	// which no-ops (dedupe row already exists), then MarkCallback
	// succeeds. The worker pops the pre-crash job and runs the
	// handler. Handler count must be exactly 1. The 5s window absorbs
	// CPU contention under -race; the polling interval is 10ms so a
	// successful pop fires within milliseconds in practice.
	testsync.Eventually(t, func() bool { return handlerRuns.Load() >= 1 }, 5*time.Second,
		"crash recovery: handler runs at least once")
	// Stability: wait long enough for several reaper ticks AND for any
	// duplicate to land. If the dedupe were broken we'd see >1 here.
	time.Sleep(300 * time.Millisecond)
	if got := handlerRuns.Load(); got != 1 {
		t.Errorf("handler ran %d times after crash recovery; want exactly 1", got)
	}
}

// TestMemoryDriver_DedupeAwarePusher_InterfaceAssertion and the
// database one too. The Redis driver's assertion lives in the
// queue/redis leaf package alongside the driver itself.
func TestDrivers_AllImplementDedupeAwarePusher(t *testing.T) {
	var _ DedupeAwarePusher = (*MemoryDriver)(nil)
	var _ DedupeAwarePusher = (*DatabaseDriver)(nil)
}

// Trailing helpers to avoid unused-import warnings for sync and
// strings in any future refactor.
var (
	_ = sync.Mutex{}
	_ = strings.Contains
)
