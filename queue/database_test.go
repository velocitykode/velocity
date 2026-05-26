package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// newSQLiteQueueDB spins up a per-test SQLite database with the jobs and
// failed_jobs tables the DatabaseDriver expects. It returns the driver
// pointed at that DB plus a cleanup func.
//
// File-backed (t.TempDir()) rather than in-memory: a context-cancelled
// transaction inside PopCtxWithTrace causes database/sql to discard the
// connection on which the tx was running, and SQLite in-memory databases
// live on that single connection. After the cancel, follow-up queries open
// a fresh connection and see an empty DB ("no such table: jobs"), which
// flaked TestC01_DatabaseDriver_PoisonRowSurvivesCallerCancellation under
// -race. File-backed schema survives reconnects. Each call gets a unique
// path under t.TempDir() so parallel subtests cannot collide.
func newSQLiteQueueDB(t *testing.T) (*DatabaseDriver, func()) {
	t.Helper()

	dsn := "file:" + t.TempDir() + "/queue.db?_busy_timeout=5000&_journal_mode=WAL"
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Cap the pool at 1 connection. The DatabaseDriver pop path holds d.mu
	// across BeginTx; with multiple conns SQLite can throw "database is
	// locked" on WAL writers. A single connection serialises access at the
	// sql.DB layer instead of relying on SQLite's lock manager. Scaling the
	// pool is a DB-level concern, not the pop-correctness concern these
	// tests are validating.
	db.SetMaxOpenConns(1)

	schema := []string{
		`CREATE TABLE jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			queue TEXT NOT NULL,
			payload TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			scheduled_at DATETIME NOT NULL,
			reserved_at DATETIME,
			reserved_by TEXT,
			failed_at DATETIME,
			failed_reason TEXT,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
		`CREATE TABLE failed_jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			queue TEXT NOT NULL,
			payload TEXT NOT NULL,
			exception TEXT NOT NULL,
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		)`,
	}
	for _, s := range schema {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}

	driver := NewDatabaseDriver(db, "sqlite")
	// Register TestJob so GetJobFromWrapper can restore it.
	Register("*queue.TestJob", func(data []byte) (Job, error) {
		j := &TestJob{}
		if err := json.Unmarshal(data, j); err != nil {
			return nil, err
		}
		return j, nil
	})

	return driver, func() { _ = db.Close() }
}

// TestDatabaseDriver_Pop_TwoWorkers_SingleJob asserts that a single job
// inserted into the DB is delivered to exactly one worker even when N
// workers race to Pop it concurrently. This is the core correctness
// invariant of the transactional Pop implementation.
func TestDatabaseDriver_Pop_TwoWorkers_SingleJob(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil) // disable signing for simplicity

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	// Push exactly one job.
	job := &TestJob{ID: "only-one", Message: "race-me"}
	if err := driver.PushCtx(context.Background(), job, "pop-race"); err != nil {
		t.Fatalf("push: %v", err)
	}

	const workers = 16
	var popped int32
	var wg sync.WaitGroup

	// Gate all goroutines to start at the same instant to maximise contention.
	start := make(chan struct{})

	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// Each worker tries up to N times to Pop; at most one will succeed.
			for attempt := 0; attempt < 5; attempt++ {
				got, err := driver.PopCtx(context.Background(), "pop-race")
				if err != nil {
					t.Errorf("pop error: %v", err)
					return
				}
				if got != nil {
					atomic.AddInt32(&popped, 1)
					return
				}
				time.Sleep(time.Millisecond)
			}
		}()
	}

	close(start)
	wg.Wait()

	if got := atomic.LoadInt32(&popped); got != 1 {
		t.Fatalf("expected exactly one worker to pop the job, got %d", got)
	}
	size, err := driver.Size("pop-race")
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	if size != 0 {
		t.Errorf("expected queue empty after race, got size=%d", size)
	}
}

// TestDatabaseDriver_Pop_RejectsTamperedPayload verifies that a row whose
// HMAC signature does not validate is treated as poison and routed through
// the quarantine path: it is removed from `jobs` and recorded in
// `failed_jobs` with the integrity-failure exception. The earlier
// "preserved for forensic inspection" property is honoured by the
// failed_jobs row, not by leaving the poison row live in the queue (which
// would head-of-line starve every other due job after C-01-fb4).
func TestDatabaseDriver_Pop_RejectsTamperedPayload(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey([]byte("integrity-test-key"))

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	job := &TestJob{ID: "tamper-me", Message: "please"}
	if err := driver.PushCtx(context.Background(), job, "tamper-queue"); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Tamper with the stored payload: replace the ID so the HMAC no longer matches.
	row := driver.db.QueryRow("SELECT id, payload FROM jobs WHERE queue = ?", "tamper-queue")
	var id int
	var payload string
	if err := row.Scan(&id, &payload); err != nil {
		t.Fatalf("scan: %v", err)
	}
	var wrapper map[string]interface{}
	if err := json.Unmarshal([]byte(payload), &wrapper); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Mutate nested payload Data field to trigger signature mismatch.
	p, ok := wrapper["payload"].(map[string]interface{})
	if !ok {
		t.Fatalf("unexpected wrapper shape: %T", wrapper["payload"])
	}
	// Replace the Data field with arbitrary bytes; the signature is now invalid.
	p["data"] = json.RawMessage(`{"id":"MALICIOUS","message":"evil"}`)
	tampered, err := json.Marshal(wrapper)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := driver.db.Exec("UPDATE jobs SET payload = ? WHERE id = ?", string(tampered), id); err != nil {
		t.Fatalf("update: %v", err)
	}

	popped, popErr := driver.PopCtx(context.Background(), "tamper-queue")
	if popErr == nil {
		t.Fatalf("expected integrity error, got nil (job=%v)", popped)
	}
	if !errors.Is(popErr, ErrPoisonJob) {
		t.Errorf("integrity error did not wrap ErrPoisonJob (worker would not treat as recoverable): %v", popErr)
	}
	if popped != nil {
		t.Errorf("expected nil job on integrity failure, got %T", popped)
	}

	// Tampered row must be GONE from jobs (head-of-line starvation guard).
	var liveCount int
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = ?", "tamper-queue").Scan(&liveCount); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if liveCount != 0 {
		t.Errorf("tampered row left live in jobs: count=%d (HOL starvation regression)", liveCount)
	}

	// And recorded in failed_jobs with the integrity-failure exception
	// preserved for forensic inspection.
	var failedCount int
	var exception, storedPayload string
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM failed_jobs WHERE queue = ?", "tamper-queue").Scan(&failedCount); err != nil {
		t.Fatalf("count failed_jobs: %v", err)
	}
	if failedCount != 1 {
		t.Fatalf("tampered row not recorded in failed_jobs: count=%d", failedCount)
	}
	if err := driver.db.QueryRow("SELECT exception, payload FROM failed_jobs WHERE queue = ?", "tamper-queue").Scan(&exception, &storedPayload); err != nil {
		t.Fatalf("scan failed_jobs row: %v", err)
	}
	if !strings.Contains(exception, "integrity check failed") {
		t.Errorf("failed_jobs.exception does not name the integrity-failure cause: %q", exception)
	}
	if storedPayload != string(tampered) {
		t.Errorf("failed_jobs.payload does not match the on-wire (tampered) bytes; operator cannot inspect what came off the queue")
	}
}

// TestDatabaseDriver_Push_Size_Clear covers the placeholder rewriting for
// non-Postgres drivers: every code path must survive with `?` placeholders.
func TestDatabaseDriver_Push_Size_Clear(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	for i := 0; i < 3; i++ {
		if err := driver.PushCtx(context.Background(), &TestJob{ID: fmt.Sprintf("job-%d", i)}, "q"); err != nil {
			t.Fatalf("push %d: %v", i, err)
		}
	}
	size, err := driver.Size("q")
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	if size != 3 {
		t.Errorf("size=%d, want 3", size)
	}
	if err := driver.Clear("q"); err != nil {
		t.Fatalf("clear: %v", err)
	}
	size, _ = driver.Size("q")
	if size != 0 {
		t.Errorf("size after clear=%d, want 0", size)
	}
}

// TestDatabaseDriver_Pop_SIGKILLRecoverable proves the C-02 invariant:
// a job whose worker dies between pop and ack (modelled here by cancelling
// the worker context mid-handler and never calling Ack/Release/Fail)
// remains durable, and a future worker reclaims it after the lease
// expires. Pre-fix, PopCtx DELETEd the row before the handler ran, so a
// SIGKILL permanently lost the job. Post-fix, the row is leased and
// becomes reclaimable once reserved_at < now - retryAfter.
func TestDatabaseDriver_Pop_SIGKILLRecoverable(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	// Short lease so the test does not have to wait 90 seconds for
	// reclamation. 50ms is long enough to exclude flakes from clock skew
	// and short enough to keep the test fast.
	const lease = 50 * time.Millisecond
	driver.SetRetryAfter(lease)

	job := &TestJob{ID: "kill-me", Message: "halfway"}
	if err := driver.PushCtx(context.Background(), job, "sigkill"); err != nil {
		t.Fatalf("push: %v", err)
	}

	// First worker pops and reserves the row. We then drop the
	// reservation token on the floor (no Ack / Release / Fail) to model
	// a SIGKILL between pop and ack.
	popped, token, _, err := driver.PopCtxReserved(context.Background(), "sigkill")
	if err != nil {
		t.Fatalf("first pop: %v", err)
	}
	if popped == nil || token.IsZero() {
		t.Fatalf("expected a reserved job, got job=%v token=%+v", popped, token)
	}
	if token.Attempts != 1 {
		t.Errorf("first reservation attempts = %d, want 1", token.Attempts)
	}

	// While the lease is fresh, no other worker can claim the row.
	// (Size filters reserved_at IS NULL, so it should report 0.)
	if size, _ := driver.Size("sigkill"); size != 0 {
		t.Errorf("Size during active lease = %d, want 0 (reserved rows excluded)", size)
	}
	stolen, stolenToken, _, err := driver.PopCtxReserved(context.Background(), "sigkill")
	if err != nil {
		t.Fatalf("second pop during lease: %v", err)
	}
	if stolen != nil || !stolenToken.IsZero() {
		t.Fatalf("a second worker stole the leased row before retryAfter; job=%v token=%+v", stolen, stolenToken)
	}

	// The row must still be in the table (not deleted on pop). This is
	// the invariant violated by the pre-fix code path.
	var totalRows int
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = ?", "sigkill").Scan(&totalRows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if totalRows != 1 {
		t.Fatalf("row removed before ack; rows=%d, want 1", totalRows)
	}

	// Simulate the SIGKILL: the "worker" never acks, never releases,
	// never fails. Wait past the lease window so the row becomes
	// eligible for reclamation.
	time.Sleep(lease + 50*time.Millisecond)

	reclaimed, reclaimToken, _, err := driver.PopCtxReserved(context.Background(), "sigkill")
	if err != nil {
		t.Fatalf("reclaim pop: %v", err)
	}
	if reclaimed == nil {
		t.Fatal("expected lease to be reclaimable after retryAfter, got nil")
	}
	if reclaimToken.IsZero() {
		t.Errorf("reclaim returned zero token")
	}
	// The reclaimed row is the same physical row (same DB id) as the
	// original reservation; reservation tokens encode the row id.
	if reclaimToken.ID != token.ID {
		t.Errorf("reclaim returned different row id; got %d, want %d", reclaimToken.ID, token.ID)
	}
	// The token must carry the post-increment persisted attempts value
	// so the worker can make MaxAttempts decisions without consulting
	// in-memory state. This is the C-02 follow-up invariant: a
	// hypothetical restart between pops would otherwise lose the count.
	if reclaimToken.Attempts != 2 {
		t.Errorf("reclaim token attempts = %d, want 2 (one per reserve)", reclaimToken.Attempts)
	}

	// The persisted attempts counter must have advanced (once per
	// reserve). Both pops should have incremented it, so attempts == 2.
	var attempts int
	if err := driver.db.QueryRow("SELECT attempts FROM jobs WHERE id = ?", reclaimToken.ID).Scan(&attempts); err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	if attempts != 2 {
		t.Errorf("persisted attempts = %d, want 2 (one per reserve)", attempts)
	}

	// Cleanly ack so the test leaves no residue.
	if err := driver.AckCtx(context.Background(), reclaimToken); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = ?", "sigkill").Scan(&totalRows); err != nil {
		t.Fatalf("post-ack count: %v", err)
	}
	if totalRows != 0 {
		t.Errorf("row not removed after ack; rows=%d, want 0", totalRows)
	}
}

// TestDatabaseDriver_Pop_ReleaseRequeuesInPlace verifies that ReleaseCtx
// updates the existing reserved row in place (clearing reserved_at and
// pushing scheduled_at forward) rather than creating a churned duplicate.
// The persisted attempts counter must therefore survive the retry.
func TestDatabaseDriver_Pop_ReleaseRequeuesInPlace(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()
	driver.SetRetryAfter(time.Second)

	job := &TestJob{ID: "release-me"}
	if err := driver.PushCtx(context.Background(), job, "rel"); err != nil {
		t.Fatalf("push: %v", err)
	}

	_, token, _, err := driver.PopCtxReserved(context.Background(), "rel")
	if err != nil || token.IsZero() {
		t.Fatalf("first pop: token=%+v err=%v", token, err)
	}

	// Release with a small delay; the row should be visible to the next
	// pop after delay elapses, and the id (token) must be unchanged.
	if err := driver.ReleaseCtx(context.Background(), token, 30*time.Millisecond); err != nil {
		t.Fatalf("release: %v", err)
	}

	// During the release delay, the row is not poppable.
	if j, tok, _, _ := driver.PopCtxReserved(context.Background(), "rel"); j != nil || !tok.IsZero() {
		t.Errorf("row visible before release delay elapsed; job=%v token=%+v", j, tok)
	}

	time.Sleep(60 * time.Millisecond)
	_, retryToken, _, err := driver.PopCtxReserved(context.Background(), "rel")
	if err != nil {
		t.Fatalf("retry pop: %v", err)
	}
	if retryToken.ID != token.ID {
		t.Errorf("release created a new row; got token=%d, want %d (in-place update)", retryToken.ID, token.ID)
	}
	if retryToken.Attempts != 2 {
		t.Errorf("retry token attempts = %d, want 2", retryToken.Attempts)
	}

	var rows int
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = ?", "rel").Scan(&rows); err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Errorf("release created %d rows; want exactly 1 in-place row", rows)
	}

	// attempts must have advanced twice (one reserve per pop).
	var attempts int
	if err := driver.db.QueryRow("SELECT attempts FROM jobs WHERE id = ?", token.ID).Scan(&attempts); err != nil {
		t.Fatalf("read attempts: %v", err)
	}
	if attempts != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
}

// TestDatabaseDriver_FailReservedCtx_AtomicMove verifies that a terminal
// failure moves the row to failed_jobs and deletes the original in one
// transaction.
func TestDatabaseDriver_FailReservedCtx_AtomicMove(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	job := &TestJob{ID: "fail-me"}
	if err := driver.PushCtx(context.Background(), job, "term"); err != nil {
		t.Fatalf("push: %v", err)
	}

	popped, token, _, err := driver.PopCtxReserved(context.Background(), "term")
	if err != nil || popped == nil || token.IsZero() {
		t.Fatalf("pop: token=%+v err=%v", token, err)
	}

	if err := driver.FailReservedCtx(context.Background(), token, popped, fmt.Errorf("terminal"), "term"); err != nil {
		t.Fatalf("fail: %v", err)
	}

	var jobs, failed int
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = ?", "term").Scan(&jobs); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobs != 0 {
		t.Errorf("reserved row not deleted on terminal fail; rows=%d", jobs)
	}
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM failed_jobs WHERE queue = ?", "term").Scan(&failed); err != nil {
		t.Fatalf("count failed: %v", err)
	}
	if failed != 1 {
		t.Errorf("failed_jobs not written on terminal fail; rows=%d", failed)
	}
}

// TestDatabaseDriver_PersistedAttemptsBoundMaxAttempts proves the C-02
// follow-up invariant: a worker restart between attempts must not reset
// the MaxAttempts budget. Worker1 pops the job, the handler fails, the
// row is released for retry. Worker1 is then discarded; worker2 (with a
// fresh, empty in-memory attempts cache) pops the same row. The second
// attempt's persisted attempts value is 2 and MaxAttempts is 2, so
// worker2 must route the failure through FailReservedCtx instead of
// retrying again. If the in-memory cache were authoritative (the
// pre-follow-up behaviour) worker2 would observe attempts=1 and retry.
//
// Note: we drive Worker.processJob directly rather than start the pump,
// which keeps the test deterministic on a single SQLite connection.
func TestDatabaseDriver_PersistedAttemptsBoundMaxAttempts(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()
	// Lease short enough that worker2's reservation reclaims any row
	// worker1 might have left in flight without an explicit release.
	// The retry release uses an explicit small delay, so this is mostly
	// belt-and-braces for the test.
	driver.SetRetryAfter(50 * time.Millisecond)

	if err := driver.PushCtx(context.Background(), &TestJob{ID: "persist-attempts"}, "pa"); err != nil {
		t.Fatalf("push: %v", err)
	}

	var attempt1, attempt2 int32
	failingHandler := func(target *int32) func(Job) error {
		return func(Job) error {
			atomic.AddInt32(target, 1)
			return fmt.Errorf("intentional failure")
		}
	}

	// Worker 1: max 2 attempts, immediate retry (no backoff delay) so
	// worker2's pop happens cleanly after the row is released.
	w1 := NewWorker(driver, "pa", failingHandler(&attempt1),
		WithMaxRetries(2),
		WithBackoff(func(int) time.Duration { return 0 }),
		WithWorkerLogger(nullLogger{}),
	)
	w1.ctx, w1.cancel = context.WithCancel(context.Background())
	defer w1.cancel()

	// Drive a single pop+handle cycle on worker1. The handler returns
	// an error; handleJobFailure observes token.Attempts == 1 < 2, so
	// it releases the row for retry.
	if err := w1.processJob(); err == nil {
		t.Fatalf("worker1 processJob: expected job-failed error, got nil")
	}
	if got := atomic.LoadInt32(&attempt1); got != 1 {
		t.Fatalf("worker1 handler invocations = %d, want 1", got)
	}

	// Confirm the row is back in the table, unreserved, scheduled for now.
	var rows, reserved int
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = ?", "pa").Scan(&rows); err != nil {
		t.Fatalf("count rows after release: %v", err)
	}
	if rows != 1 {
		t.Fatalf("post-release rows = %d, want 1 (in-place release)", rows)
	}
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = ? AND reserved_at IS NULL", "pa").Scan(&reserved); err != nil {
		t.Fatalf("count unreserved: %v", err)
	}
	if reserved != 1 {
		t.Fatalf("post-release unreserved rows = %d, want 1", reserved)
	}

	// Confirm persisted attempts has advanced to 1 (worker1's reserve).
	var persisted int
	if err := driver.db.QueryRow("SELECT attempts FROM jobs WHERE queue = ?", "pa").Scan(&persisted); err != nil {
		t.Fatalf("read attempts after worker1: %v", err)
	}
	if persisted != 1 {
		t.Errorf("persisted attempts after worker1 = %d, want 1", persisted)
	}

	// "Restart": discard worker1 (its sync.Map cache evaporates with
	// it) and construct worker2 with the same MaxRetries. Worker2 has
	// never seen this job; w2.attempts is empty.
	w1.cancel()

	w2 := NewWorker(driver, "pa", failingHandler(&attempt2),
		WithMaxRetries(2),
		WithBackoff(func(int) time.Duration { return 0 }),
		WithWorkerLogger(nullLogger{}),
	)
	w2.ctx, w2.cancel = context.WithCancel(context.Background())
	defer w2.cancel()

	// Sanity: w2's in-memory attempt counter is empty (this is the
	// pre-fix authoritative source). If anything starts at 1, the test
	// assumption is wrong.
	if _, ok := w2.attempts.Load(w2.jobKey(&TestJob{})); ok {
		t.Fatal("w2.attempts unexpectedly populated for a fresh worker")
	}

	// Drive worker2's pop+handle cycle. The persisted attempts column
	// now advances to 2 inside the reservation. attemptNumber(token)
	// must return 2 (from token.Attempts), not 1 (from w2.attempts).
	// With MaxAttempts == 2 this is terminal: FailReservedCtx fires.
	if err := w2.processJob(); err == nil {
		t.Fatalf("worker2 processJob: expected job-failed error, got nil")
	}
	if got := atomic.LoadInt32(&attempt2); got != 1 {
		t.Fatalf("worker2 handler invocations = %d, want 1", got)
	}

	// The jobs row must be gone (terminal failure) and a failed_jobs
	// row must exist. If MaxAttempts had been driven by w2.attempts the
	// row would still be in jobs (released for a second retry).
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = ?", "pa").Scan(&rows); err != nil {
		t.Fatalf("count jobs after worker2: %v", err)
	}
	if rows != 0 {
		t.Errorf("jobs row not removed on terminal fail; rows=%d (persisted attempts ignored?)", rows)
	}
	var failed int
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM failed_jobs WHERE queue = ?", "pa").Scan(&failed); err != nil {
		t.Fatalf("count failed_jobs: %v", err)
	}
	if failed != 1 {
		t.Errorf("failed_jobs rows = %d, want 1 (worker2 should have terminated)", failed)
	}
}

// TestDatabaseDriver_StaleLeaseFencedByToken proves the fencing-token
// invariant: a worker that holds a stale lease (its lease window
// expired and another worker reclaimed the row) MUST NOT be able to
// mutate the row. AckCtx, ReleaseCtx, and FailReservedCtx invoked with
// the stale token return ErrLeaseLost and the row stays owned by the
// new worker.
//
// Pre-fix, mutators fenced on row id only and the stale token would
// happily delete (or clobber) the new owner's row.
func TestDatabaseDriver_StaleLeaseFencedByToken(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()
	driver.SetRetryAfter(40 * time.Millisecond)

	if err := driver.PushCtx(context.Background(), &TestJob{ID: "stale-lease"}, "stale"); err != nil {
		t.Fatalf("push: %v", err)
	}

	// First worker reserves the row, then "stalls" past the lease.
	popped, stale, _, err := driver.PopCtxReserved(context.Background(), "stale")
	if err != nil || popped == nil || stale.IsZero() {
		t.Fatalf("first pop: token=%+v err=%v", stale, err)
	}
	time.Sleep(60 * time.Millisecond)

	// To simulate a *different* worker reclaiming, swap the driver's
	// workerID before the reclaim pop. Without this the token's
	// ReservedBy would match the same string and the row id +
	// attempts mismatch alone would carry the fence, but we want to
	// exercise the full (id, attempts, reserved_by) tuple.
	originalWorker := driver.workerID
	driver.workerID = "different-worker-id"
	defer func() { driver.workerID = originalWorker }()

	// Second worker reclaims via the retryAfter predicate.
	popped2, fresh, _, err := driver.PopCtxReserved(context.Background(), "stale")
	if err != nil || popped2 == nil || fresh.IsZero() {
		t.Fatalf("reclaim pop: token=%+v err=%v", fresh, err)
	}
	if fresh.ID != stale.ID {
		t.Fatalf("reclaim returned different row id; got %d, want %d", fresh.ID, stale.ID)
	}
	if fresh.Attempts <= stale.Attempts {
		t.Fatalf("attempts did not advance on reclaim; fresh=%d stale=%d", fresh.Attempts, stale.Attempts)
	}
	if fresh.ReservedBy == stale.ReservedBy {
		t.Fatalf("reserved_by did not change on reclaim; both=%q", fresh.ReservedBy)
	}

	// The stale token must now be fenced off from every mutator.
	if err := driver.AckCtx(context.Background(), stale); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("stale AckCtx: got err=%v, want ErrLeaseLost", err)
	}
	if err := driver.ReleaseCtx(context.Background(), stale, 0); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("stale ReleaseCtx: got err=%v, want ErrLeaseLost", err)
	}
	if err := driver.FailReservedCtx(context.Background(), stale, popped, fmt.Errorf("stale"), "stale"); !errors.Is(err, ErrLeaseLost) {
		t.Errorf("stale FailReservedCtx: got err=%v, want ErrLeaseLost", err)
	}

	// The new owner's row must be intact. reserved_by should still
	// reflect the second worker, attempts unchanged, and no failed_jobs
	// row should have been written by the stale FailReservedCtx call.
	var reservedBy string
	var persistedAttempts int
	if err := driver.db.QueryRow("SELECT reserved_by, attempts FROM jobs WHERE id = ?", fresh.ID).Scan(&reservedBy, &persistedAttempts); err != nil {
		t.Fatalf("inspect row: %v", err)
	}
	if reservedBy != fresh.ReservedBy {
		t.Errorf("reserved_by mutated by stale lease; got %q, want %q", reservedBy, fresh.ReservedBy)
	}
	if persistedAttempts != fresh.Attempts {
		t.Errorf("attempts mutated by stale lease; got %d, want %d", persistedAttempts, fresh.Attempts)
	}
	var failed int
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM failed_jobs WHERE queue = ?", "stale").Scan(&failed); err != nil {
		t.Fatalf("count failed_jobs: %v", err)
	}
	if failed != 0 {
		t.Errorf("failed_jobs unexpectedly written by stale FailReservedCtx; rows=%d", failed)
	}

	// The fresh token still works.
	if err := driver.AckCtx(context.Background(), fresh); err != nil {
		t.Errorf("fresh AckCtx: %v", err)
	}
}

// TestDatabaseDriver_TerminalCleanupSurvivesCancelledCtx proves that
// FailReservedCtx still moves the row to failed_jobs when the caller's
// jobCtx is already cancelled (the common case when the per-job
// timeout fires). The worker.failJob path uses a fresh detached ctx
// for the cleanup write, so we exercise that here at the driver level
// by passing a pre-cancelled ctx and confirming that FailReservedCtx
// honours it (returning the ctx error) while the worker-level path
// supplies a fresh ctx.
//
// This test asserts two things:
//  1. The driver method DOES honour a cancelled ctx (so the worker's
//     fresh-ctx wrapping is the only way to survive a jobCtx timeout).
//  2. With a fresh background ctx, the row moves to failed_jobs and
//     is deleted from jobs even when the original jobCtx is dead.
func TestDatabaseDriver_TerminalCleanupSurvivesCancelledCtx(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	if err := driver.PushCtx(context.Background(), &TestJob{ID: "cancel-cleanup"}, "cc"); err != nil {
		t.Fatalf("push: %v", err)
	}

	popped, token, _, err := driver.PopCtxReserved(context.Background(), "cc")
	if err != nil || popped == nil || token.IsZero() {
		t.Fatalf("pop: token=%+v err=%v", token, err)
	}

	// (1) A pre-cancelled ctx is honoured (proves we need the fresh-ctx wrap).
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := driver.FailReservedCtx(cancelled, token, popped, fmt.Errorf("timeout"), "cc"); err == nil {
		t.Fatalf("FailReservedCtx with cancelled ctx: expected ctx.Err, got nil")
	} else if !errors.Is(err, context.Canceled) {
		t.Fatalf("FailReservedCtx with cancelled ctx: got %v, want context.Canceled", err)
	}

	// Row must still be in jobs (the cancelled ctx aborted before any
	// write). This is the trap the pre-fix worker fell into.
	var jobs int
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = ?", "cc").Scan(&jobs); err != nil {
		t.Fatalf("count jobs after cancelled fail: %v", err)
	}
	if jobs != 1 {
		t.Errorf("jobs row count after cancelled fail = %d, want 1 (write should have been aborted)", jobs)
	}

	// (2) A fresh ctx (the kind the worker's failJob constructs) lets
	// the cleanup write complete despite the original ctx being dead.
	freshCtx, freshCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer freshCancel()
	if err := driver.FailReservedCtx(freshCtx, token, popped, fmt.Errorf("timeout"), "cc"); err != nil {
		t.Fatalf("FailReservedCtx with fresh ctx: %v", err)
	}

	if err := driver.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = ?", "cc").Scan(&jobs); err != nil {
		t.Fatalf("count jobs after fresh-ctx fail: %v", err)
	}
	if jobs != 0 {
		t.Errorf("jobs row not removed by fresh-ctx fail; rows=%d", jobs)
	}
	var failed int
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM failed_jobs WHERE queue = ?", "cc").Scan(&failed); err != nil {
		t.Fatalf("count failed_jobs: %v", err)
	}
	if failed != 1 {
		t.Errorf("failed_jobs not written by fresh-ctx fail; rows=%d", failed)
	}
}

// TestWorker_TimedOutHandlerLandsInFailedJobs is the end-to-end variant
// of the previous test: drive the worker through the per-job timeout
// branch in processJob and verify the row lands in failed_jobs. The
// only way this can pass is if failJob detaches the cleanup write from
// the now-dead jobCtx.
func TestWorker_TimedOutHandlerLandsInFailedJobs(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	driver, cleanup := newSQLiteQueueDB(t)
	defer cleanup()

	if err := driver.PushCtx(context.Background(), &TestJob{ID: "to-fail"}, "to"); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Handler that blocks past the per-job timeout. The worker's
	// processJob will fire the jobCtx-timeout branch and route the
	// failure into handleJobFailure -> failJob with an already-dead
	// ctx. failJob must construct a fresh ctx for the cleanup write.
	handler := func(job Job) error {
		// Sleep well past the per-job timeout set below.
		time.Sleep(500 * time.Millisecond)
		return nil
	}

	w := NewWorker(driver, "to", handler,
		WithMaxRetries(1),                // first failure is terminal
		WithTimeout(50*time.Millisecond), // forces the jobCtx-timeout branch
		WithBackoff(func(int) time.Duration { return 0 }),
		WithWorkerLogger(nullLogger{}),
	)
	w.ctx, w.cancel = context.WithCancel(context.Background())
	defer w.cancel()

	// Reduce defaultHandlerKillCeiling so drainHandler does not stall the test.
	saved := defaultHandlerKillCeiling
	defaultHandlerKillCeiling = 200 * time.Millisecond
	defer func() { defaultHandlerKillCeiling = saved }()

	if err := w.processJob(); err == nil {
		t.Fatalf("processJob: expected timeout error, got nil")
	}

	var jobs, failed int
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM jobs WHERE queue = ?", "to").Scan(&jobs); err != nil {
		t.Fatalf("count jobs: %v", err)
	}
	if jobs != 0 {
		t.Errorf("timed-out job not removed from jobs; rows=%d (terminal cleanup used dead ctx?)", jobs)
	}
	if err := driver.db.QueryRow("SELECT COUNT(*) FROM failed_jobs WHERE queue = ?", "to").Scan(&failed); err != nil {
		t.Fatalf("count failed_jobs: %v", err)
	}
	if failed != 1 {
		t.Errorf("timed-out job not recorded in failed_jobs; rows=%d", failed)
	}
}

// TestDatabaseDriver_RewriteQuery exercises the placeholder rewriter directly
// so regressions are caught even if no driver-specific test runs.
func TestDatabaseDriver_RewriteQuery(t *testing.T) {
	cases := []struct {
		name     string
		dbDriver string
		in       string
		want     string
	}{
		{"postgres untouched", "postgres", "WHERE a = $1 AND b = $2", "WHERE a = $1 AND b = $2"},
		{"sqlite rewrite", "sqlite", "WHERE a = $1 AND b = $2", "WHERE a = ? AND b = ?"},
		{"mysql rewrite", "mysql", "SET x = $10 WHERE y = $1", "SET x = ? WHERE y = ?"},
		{"no placeholders", "sqlite", "SELECT COUNT(*) FROM t", "SELECT COUNT(*) FROM t"},
		{"literal $ untouched", "sqlite", "x = '$foo'", "x = '$foo'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := &DatabaseDriver{dbDriver: tc.dbDriver}
			got := d.rewriteQuery(tc.in)
			if got != tc.want {
				t.Errorf("rewriteQuery(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
