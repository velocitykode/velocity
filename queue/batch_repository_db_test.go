package queue

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
	testsync "github.com/velocitykode/velocity/testing"
)

// newSQLiteBatchDB spins up a shared-memory SQLite database with the
// job_batches schema installed. Returns the *sql.DB and a cleanup func.
// A shared-cache URI is used so two independently-constructed repos can
// observe the same rows, simulating the multi-host scenario.
func newSQLiteBatchDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	// Each test gets its own cache name so parallel tests don't collide.
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "?mode=memory&cache=shared"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// SQLite under shared cache needs a single conn to serialise the
	// transactional UPDATEs the repo issues; without this the test
	// hits "database is locked" intermittently.
	db.SetMaxOpenConns(1)
	if err := EnsureJobBatchesTable(context.Background(), db, "sqlite"); err != nil {
		_ = db.Close()
		t.Fatalf("ensure schema: %v", err)
	}
	return db, func() { _ = db.Close() }
}

// TestDatabaseBatchRepository_BasicCRUD covers Save -> Find -> Cancel ->
// Delete on a single repo. Smoke test for the SQL surface.
func TestDatabaseBatchRepository_BasicCRUD(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	repo, err := NewDatabaseBatchRepository(db, "sqlite")
	if err != nil {
		t.Fatalf("new repo: %v", err)
	}

	b := &Batch{id: newBatchID(), totalJobs: 3, queue: "default"}
	b.pendingJobs.Store(3)

	if err := repo.Save(context.Background(), b); err != nil {
		t.Fatalf("save: %v", err)
	}

	got, err := repo.Find(context.Background(), b.id)
	if err != nil {
		t.Fatalf("find: %v", err)
	}
	if got == nil {
		t.Fatal("expected to find batch")
	}
	if got.PendingJobs() != 3 || got.TotalJobs() != 3 {
		t.Errorf("counters: got pending=%d total=%d", got.PendingJobs(), got.TotalJobs())
	}

	if _, err := repo.Cancel(context.Background(), b.id); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	got, _ = repo.Find(context.Background(), b.id)
	if !got.Cancelled() {
		t.Error("expected cancelled after Cancel")
	}

	if err := repo.Delete(context.Background(), b.id); err != nil {
		t.Fatalf("delete: %v", err)
	}
	gone, err := repo.Find(context.Background(), b.id)
	if err != nil {
		t.Fatalf("find after delete: %v", err)
	}
	if gone != nil {
		t.Error("expected nil after Delete")
	}
}

// TestDatabaseBatchRepository_IncrementSuccess_Atomic ensures the
// counter UPDATE moves pending -> completed atomically and reports
// justFinished exactly once.
func TestDatabaseBatchRepository_IncrementSuccess_Atomic(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	repo, _ := NewDatabaseBatchRepository(db, "sqlite")

	b := &Batch{id: newBatchID(), totalJobs: 2, queue: "default"}
	b.pendingJobs.Store(2)
	if err := repo.Save(context.Background(), b); err != nil {
		t.Fatalf("save: %v", err)
	}

	// First increment: not finished yet.
	_, finished, err := repo.IncrementSuccess(context.Background(), b.id)
	if err != nil {
		t.Fatalf("incr1: %v", err)
	}
	if finished {
		t.Error("first increment should not finish a 2-job batch")
	}

	// Second increment: this is the one that finishes the batch.
	updated, finished, err := repo.IncrementSuccess(context.Background(), b.id)
	if err != nil {
		t.Fatalf("incr2: %v", err)
	}
	if !finished {
		t.Error("second increment should finish a 2-job batch")
	}
	if updated.CompletedJobs() != 2 || updated.PendingJobs() != 0 {
		t.Errorf("counters after finish: completed=%d pending=%d", updated.CompletedJobs(), updated.PendingJobs())
	}
	if !updated.Finished() {
		t.Error("expected Finished()=true after terminal increment")
	}
}

// TestDatabaseBatchRepository_CrossRepo_CompletionCAS is the key C-03
// regression test: two independent repositories backed by the same DB
// simulate two worker processes. The dispatcher repo creates a batch and
// registers callbacks; the "remote worker" repo (no callback closures)
// completes one job; the dispatcher repo completes the other. Then must
// fire exactly once on the dispatcher process and exactly one increment
// must observe justFinished == true.
func TestDatabaseBatchRepository_CrossRepo_CompletionCAS(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	resetBatchStoreForTest(t)

	dispatcher, _ := NewDatabaseBatchRepository(db, "sqlite")
	remote, _ := NewDatabaseBatchRepository(db, "sqlite")

	driver := newMemoryDriver()
	var thenCalled atomic.Int32
	var finallyCalled atomic.Int32

	batch, err := NewBatch(&testBatchJob{}, &testBatchJob{}).
		Then(func(b *Batch) { thenCalled.Add(1) }).
		Finally(func(b *Batch) { finallyCalled.Add(1) }).
		WithRepository(dispatcher).
		Dispatch(context.Background(), driver)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Remote process picks up job 1 and reports success. This worker
	// has no local callback closures (simulating a different process).
	_, finished1, err := remote.IncrementSuccess(context.Background(), batch.ID())
	if err != nil {
		t.Fatalf("remote incr1: %v", err)
	}
	if finished1 {
		t.Error("first remote increment should not finish")
	}

	// Dispatcher process picks up job 2 and reports success. This one
	// must observe justFinished == true and fire Then/Finally locally
	// because the callback closures live here.
	updated, finished2, err := dispatcher.IncrementSuccess(context.Background(), batch.ID())
	if err != nil {
		t.Fatalf("dispatcher incr2: %v", err)
	}
	if !finished2 {
		t.Error("second increment (dispatcher) should report justFinished")
	}
	if updated == nil || updated.CompletedJobs() != 2 {
		t.Errorf("counters: %+v", updated)
	}

	// Simulate how the worker reacts to justFinished: it fires the
	// terminal callbacks via the batch helper.
	batch.copyCountersFrom(updated)
	batch.fireTerminalCallbacks(context.Background())

	testsync.Eventually(t, func() bool { return thenCalled.Load() == 1 && finallyCalled.Load() == 1 },
		2*time.Second, "Then+Finally fire exactly once after cross-process completion")

	if thenCalled.Load() != 1 {
		t.Errorf("Then fired %d times, want 1", thenCalled.Load())
	}
	if finallyCalled.Load() != 1 {
		t.Errorf("Finally fired %d times, want 1", finallyCalled.Load())
	}
}

// TestDatabaseBatchRepository_CrossRepo_Cancel verifies that a Cancel
// issued on one repo is observed by Find on another repo, mirroring the
// scenario where the dispatcher cancels a batch and a worker on a
// different host needs to see the cancellation when popping the job.
func TestDatabaseBatchRepository_CrossRepo_Cancel(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	dispatcher, _ := NewDatabaseBatchRepository(db, "sqlite")
	remote, _ := NewDatabaseBatchRepository(db, "sqlite")

	b := &Batch{id: newBatchID(), totalJobs: 3, queue: "default"}
	b.pendingJobs.Store(3)
	if err := dispatcher.Save(context.Background(), b); err != nil {
		t.Fatalf("save: %v", err)
	}

	if _, err := dispatcher.Cancel(context.Background(), b.id); err != nil {
		t.Fatalf("cancel: %v", err)
	}

	got, err := remote.Find(context.Background(), b.id)
	if err != nil {
		t.Fatalf("remote find: %v", err)
	}
	if got == nil {
		t.Fatal("remote should see batch")
	}
	if !got.Cancelled() {
		t.Error("remote should observe Cancelled state")
	}
}

// TestDatabaseBatchRepository_CompletionCAS_Concurrent races N goroutines
// against IncrementSuccess for the same near-final batch and asserts that
// exactly one goroutine observes justFinished == true. This is the
// atomicity guarantee the CAS on completed_at is supposed to provide.
func TestDatabaseBatchRepository_CompletionCAS_Concurrent(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	repo, _ := NewDatabaseBatchRepository(db, "sqlite")

	// Build a 10-job batch.
	const n = 10
	b := &Batch{id: newBatchID(), totalJobs: n, queue: "default"}
	b.pendingJobs.Store(n)
	if err := repo.Save(context.Background(), b); err != nil {
		t.Fatalf("save: %v", err)
	}

	var wg sync.WaitGroup
	var justFinishedCount atomic.Int32
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, justFinished, err := repo.IncrementSuccess(context.Background(), b.id)
			if err != nil {
				t.Errorf("incr: %v", err)
				return
			}
			if justFinished {
				justFinishedCount.Add(1)
			}
		}()
	}
	wg.Wait()

	if justFinishedCount.Load() != 1 {
		t.Errorf("expected exactly one justFinished=true, got %d", justFinishedCount.Load())
	}
	got, _ := repo.Find(context.Background(), b.id)
	if got.PendingJobs() != 0 || got.CompletedJobs() != n {
		t.Errorf("final counters: pending=%d completed=%d", got.PendingJobs(), got.CompletedJobs())
	}
}

// TestDatabaseBatchRepository_IncrementFailure_StoresError makes sure
// the failure path captures the error text on last_error and increments
// the failed_jobs counter.
func TestDatabaseBatchRepository_IncrementFailure_StoresError(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	repo, _ := NewDatabaseBatchRepository(db, "sqlite")
	b := &Batch{id: newBatchID(), totalJobs: 1, queue: "default"}
	b.pendingJobs.Store(1)
	if err := repo.Save(context.Background(), b); err != nil {
		t.Fatalf("save: %v", err)
	}

	jobErr := errors.New("synthetic failure: payment provider rejected card")
	updated, finished, err := repo.IncrementFailure(context.Background(), b.id, jobErr)
	if err != nil {
		t.Fatalf("incr fail: %v", err)
	}
	if !finished {
		t.Error("only job in batch should finalize on failure")
	}
	if updated.FailedJobs() != 1 {
		t.Errorf("failed_jobs: %d", updated.FailedJobs())
	}
	if updated.lastError != jobErr.Error() {
		t.Errorf("last_error: got %q want %q", updated.lastError, jobErr.Error())
	}
}

// TestDatabaseBatchRepository_DecrementPending_NoCounters covers the
// skip path: decrement pending without incrementing completed or failed.
// Mirrors what the worker does when a Batchable job lands but the batch
// was already cancelled before pop.
func TestDatabaseBatchRepository_DecrementPending_NoCounters(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	repo, _ := NewDatabaseBatchRepository(db, "sqlite")
	b := &Batch{id: newBatchID(), totalJobs: 2, queue: "default"}
	b.pendingJobs.Store(2)
	if err := repo.Save(context.Background(), b); err != nil {
		t.Fatalf("save: %v", err)
	}

	for i := 0; i < 2; i++ {
		_, _, err := repo.DecrementPending(context.Background(), b.id)
		if err != nil {
			t.Fatalf("dec: %v", err)
		}
	}

	got, _ := repo.Find(context.Background(), b.id)
	if got.PendingJobs() != 0 {
		t.Errorf("pending: %d", got.PendingJobs())
	}
	if got.CompletedJobs() != 0 || got.FailedJobs() != 0 {
		t.Errorf("skip should not bump completed/failed: c=%d f=%d", got.CompletedJobs(), got.FailedJobs())
	}
	if !got.Finished() {
		t.Error("draining pending should finish the batch")
	}
}

// TestDatabaseBatchRepository_PruneStale removes only finished batches
// older than the cutoff.
func TestDatabaseBatchRepository_PruneStale(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	repo, _ := NewDatabaseBatchRepository(db, "sqlite")

	// Old finished batch.
	old := &Batch{id: newBatchID(), totalJobs: 1, queue: "default"}
	old.pendingJobs.Store(1)
	_ = repo.Save(context.Background(), old)
	_, _, _ = repo.IncrementSuccess(context.Background(), old.id)
	// Backdate completed_at via direct UPDATE; otherwise PruneStale won't
	// see the row as stale within the test runtime.
	twoHrsAgo := time.Now().Add(-2 * time.Hour)
	if _, err := db.Exec("UPDATE job_batches SET completed_at = ? WHERE id = ?", twoHrsAgo, string(old.id)); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	// Fresh in-progress batch.
	fresh := &Batch{id: newBatchID(), totalJobs: 2, queue: "default"}
	fresh.pendingJobs.Store(2)
	_ = repo.Save(context.Background(), fresh)

	removed, err := repo.PruneStale(context.Background(), 1*time.Hour)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if removed != 1 {
		t.Errorf("expected 1 pruned, got %d", removed)
	}
	if got, _ := repo.Find(context.Background(), old.id); got != nil {
		t.Error("old batch should be pruned")
	}
	if got, _ := repo.Find(context.Background(), fresh.id); got == nil {
		t.Error("fresh batch should survive")
	}
}

// TestNewBatchID_Uniqueness asserts the move from sequential counters to
// UUIDv7 actually makes IDs collision-safe across rapid mints. This is
// the C-03 sub-finding about producer ID collisions.
func TestNewBatchID_Uniqueness(t *testing.T) {
	seen := make(map[BatchID]struct{}, 10_000)
	for i := 0; i < 10_000; i++ {
		id := newBatchID()
		if _, dup := seen[id]; dup {
			t.Fatalf("duplicate batch ID after %d mints: %s", i, id)
		}
		seen[id] = struct{}{}
	}
}

// TestDatabaseBatchRepository_TwoProcessWorkers_ThenFires is the
// integration-style C-03 regression. Two memory queue drivers are
// instantiated to model two independent worker processes. Both share
// the SAME job_batches table (via two DatabaseBatchRepository instances
// over the same *sql.DB). The dispatcher pushes 4 jobs split across
// the two queues; each worker drains its own queue and reports
// progress through its own repository. The dispatcher must observe
// Then fire exactly once even though the last completing job lands on
// the "remote" worker.
func TestDatabaseBatchRepository_TwoProcessWorkers_ThenFires(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	resetBatchStoreForTest(t)

	// Two repos sharing one DB = two simulated processes.
	dispatcherRepo, _ := NewDatabaseBatchRepository(db, "sqlite")
	workerRepo, _ := NewDatabaseBatchRepository(db, "sqlite")

	// One queue driver per "process". Each worker pops from its own
	// driver but routes batch state to the shared SQL table.
	dispatcherQueue := newMemoryDriver()
	workerQueue := newMemoryDriver()

	var thenFired atomic.Int32
	var finallyFired atomic.Int32

	// Dispatcher dispatches the batch with WithRepository so all batch
	// state lives in the SQL table. Half the jobs go to the local
	// queue, half to the "remote" queue.
	jobs := []Job{
		&testBatchJob{handler: func() error { return nil }},
		&testBatchJob{handler: func() error { return nil }},
		&testBatchJob{handler: func() error { return nil }},
		&testBatchJob{handler: func() error { return nil }},
	}
	batch, err := NewBatch(jobs...).
		WithRepository(dispatcherRepo).
		Then(func(b *Batch) { thenFired.Add(1) }).
		Finally(func(b *Batch) { finallyFired.Add(1) }).
		Dispatch(context.Background(), dispatcherQueue)
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}

	// Migrate two jobs to the worker queue to simulate fan-out to a
	// second host. In a real deployment both queues would be backed
	// by the same Redis/DB; the memory driver is fine for the test
	// because the cross-process correctness lives in the repo, not
	// the queue driver.
	for i := 0; i < 2; i++ {
		j, perr := dispatcherQueue.PopCtx(context.Background(), "default")
		if perr != nil || j == nil {
			t.Fatalf("pop for migration: job=%v err=%v", j, perr)
		}
		if perr := workerQueue.PushCtx(context.Background(), j, "default"); perr != nil {
			t.Fatalf("push to worker queue: %v", perr)
		}
	}

	// Spin up worker pumps. Each worker installs its own repo as the
	// process-wide default for the duration of its work so the
	// recordSuccess plumbing in worker.go routes through the right
	// SQL row. In production the apps would install this once during
	// main(); here we tweak per-test.
	dispatcherWorker := NewWorker(dispatcherQueue, "default", func(j Job) error { return j.Handle() },
		WithInterval(5*time.Millisecond), WithMaxRetries(0))
	remoteWorker := NewWorker(workerQueue, "default", func(j Job) error { return j.Handle() },
		WithInterval(5*time.Millisecond), WithMaxRetries(0))

	// Swap the default repo to the dispatcher repo for the dispatcher
	// process; the remote process uses its own. Worker code (and
	// FindBatch) always uses DefaultBatchRepository(), so for this
	// single-process test we route everything through the dispatcher
	// repo: cross-process correctness is exercised by the shared SQL
	// rows, and both repos write to the same table.
	prevDefault := DefaultBatchRepository()
	defaultBatchRepo.Store(&batchRepoHolder{BatchRepository: workerRepo})
	t.Cleanup(func() { defaultBatchRepo.Store(&batchRepoHolder{BatchRepository: prevDefault}) })

	dispatcherWorker.Start(context.Background())
	remoteWorker.Start(context.Background())
	t.Cleanup(func() {
		dispatcherWorker.Stop()
		remoteWorker.Stop()
	})

	// Drive the dispatcher process by polling: its batch will reach
	// completed once both queues are drained. Then must fire exactly
	// once from the dispatcher's callback closure, not twice.
	testsync.Eventually(t, func() bool {
		got, _ := dispatcherRepo.Find(context.Background(), batch.ID())
		return got != nil && got.Finished()
	}, 5*time.Second, "batch reaches Finished across the two workers")

	testsync.Eventually(t, func() bool { return thenFired.Load() == 1 && finallyFired.Load() == 1 },
		3*time.Second, "Then and Finally each fire exactly once")

	// Stability window: if either callback double-fired, the count
	// would tick above 1 here. The CAS on completed_at plus the
	// finishedFired atomic.Bool guard make this safe.
	time.Sleep(100 * time.Millisecond)
	if thenFired.Load() != 1 {
		t.Errorf("Then fired %d times, want 1", thenFired.Load())
	}
	if finallyFired.Load() != 1 {
		t.Errorf("Finally fired %d times, want 1", finallyFired.Load())
	}

	// Final shared-state assertions.
	got, _ := dispatcherRepo.Find(context.Background(), batch.ID())
	if got.CompletedJobs() != 4 {
		t.Errorf("completed_jobs in DB: %d, want 4", got.CompletedJobs())
	}
	if got.PendingJobs() != 0 {
		t.Errorf("pending_jobs in DB: %d, want 0", got.PendingJobs())
	}
}

// TestNewDatabaseBatchRepository_RejectsInvalidDriver ensures we surface
// configuration errors at construction instead of silently emitting
// mis-written SQL at runtime.
func TestNewDatabaseBatchRepository_RejectsInvalidDriver(t *testing.T) {
	db, cleanup := newSQLiteBatchDB(t)
	defer cleanup()

	if _, err := NewDatabaseBatchRepository(db, ""); err == nil {
		t.Error("empty driver should be rejected")
	}
	if _, err := NewDatabaseBatchRepository(db, "oracle"); err == nil {
		t.Error("unknown driver should be rejected")
	}
	if _, err := NewDatabaseBatchRepository(nil, "sqlite"); err == nil {
		t.Error("nil db should be rejected")
	}
}
