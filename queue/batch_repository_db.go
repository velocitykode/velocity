package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/velocitykode/velocity/async"
)

// ErrBatchRepositoryClosed is returned when an operation hits a closed
// DatabaseBatchRepository. App.Shutdown closes the auto-installed repo
// to release its references before the underlying *sql.DB is closed by
// the ORM manager; an in-flight worker that still holds a stale repo
// pointer needs a deterministic error rather than a panic on a torn-
// down connection pool.
var ErrBatchRepositoryClosed = errors.New("velocity/queue: batch repository is closed")

// DefaultReaperInterval is the period between sweeps of undispatched
// callbacks. 15s is short enough that a transient Redis outage recovers
// within a few ticks; long enough that the SELECT on job_batches is not
// a hot loop on idle apps. Override
// via NewDatabaseBatchRepositoryWithReaperInterval when wiring a
// repository whose backing queue has a different SLA.
const DefaultReaperInterval = 15 * time.Second

// reaperBatchSize bounds how many undispatched callbacks the reaper
// claims per tick. The bound exists so a backlog under partition
// recovery cannot flood the queue (and consequently OOM the worker
// fleet) in one tick. 100 is large enough that legitimate steady-state
// retries clear quickly and small enough to stay well below any
// queue-driver batch limit.
const reaperBatchSize = 100

// DatabaseBatchRepository persists batch state in a SQL table so workers
// on any host can observe progress, cancellation, and completion.
//
// C-03 root cause: the prior in-memory `batchStore` map was process-local;
// a worker on host B that popped a Batchable job dispatched from host A
// would not find the batch and would silently skip cancel checks and
// progress counters. The DB-backed repository replaces that map with a
// shared `job_batches` row whose counters move atomically under SQL
// UPDATEs.
//
// Cross-process callback delivery (C-03-fb2): Then/Catch/Finally
// closures cannot cross process boundaries, but the persisted
// `then_callback` / `catch_callback` / `finally_callback` columns name
// a callback registered via RegisterBatchCallback. When the repository's
// completion CAS fires on ANY host, the queue.Job runner for that
// callback is enqueued so a worker (anywhere) picks it up and invokes
// the registered handler. Closures registered locally still fire on
// the dispatcher process for the convenience path, but cross-process
// delivery uses the named-callback mechanism.
//
// Supported drivers: postgres, mysql, sqlite. Placeholders are written
// as `$N` and rewritten to `?` for mysql/sqlite by rewriteQuery.
type DatabaseBatchRepository struct {
	db       *sql.DB
	dbDriver string

	// closed is set to true by Close so subsequent calls fail loudly
	// instead of silently writing to a torn-down repo. Atomic.Bool
	// because Close races with in-flight worker callbacks during app
	// shutdown.
	closed atomic.Bool

	// reaperInterval and reaper-control plumbing for the goroutine that
	// retries undispatched callback enqueues. Started by Start (called
	// implicitly by NewDatabaseBatchRepository) and torn down by Close.
	reaperInterval time.Duration
	reaperStop     chan struct{}
	reaperStopOnce sync.Once
	reaperDone     chan struct{}
	reaperStarted  atomic.Bool
}

// NewDatabaseBatchRepository constructs a database-backed repository.
// The supplied *sql.DB is expected to already have the `job_batches`
// table provisioned (call EnsureJobBatchesTable or run the migration
// returned by JobBatchesMigrationSQL).
//
// dbDriver must be one of "postgres", "mysql", "sqlite", or "sqlite3"
// so the repository can pick the right placeholder style and the right
// dialect for the atomic increment UPDATEs. An empty driver name is
// rejected at construction so apps fail fast at boot rather than
// silently corrupting batch state with mis-written SQL.
func NewDatabaseBatchRepository(db *sql.DB, dbDriver string) (*DatabaseBatchRepository, error) {
	return NewDatabaseBatchRepositoryWithReaperInterval(db, dbDriver, DefaultReaperInterval)
}

// NewDatabaseBatchRepositoryWithReaperInterval is the constructor used
// by tests to shrink the reaper tick (15s is too slow for unit tests).
// Production callers should use NewDatabaseBatchRepository; the reaper
// interval is part of the public API for tuning, not for skipping the
// reaper entirely. Passing a non-positive interval disables the reaper.
func NewDatabaseBatchRepositoryWithReaperInterval(db *sql.DB, dbDriver string, interval time.Duration) (*DatabaseBatchRepository, error) {
	if db == nil {
		return nil, errors.New("velocity/queue: NewDatabaseBatchRepository requires a non-nil *sql.DB")
	}
	switch dbDriver {
	case "postgres", "mysql", "sqlite", "sqlite3":
	case "":
		return nil, errors.New("velocity/queue: NewDatabaseBatchRepository requires a non-empty dbDriver")
	default:
		return nil, fmt.Errorf("velocity/queue: unsupported dbDriver %q for batch repository", dbDriver)
	}
	r := &DatabaseBatchRepository{
		db:             db,
		dbDriver:       dbDriver,
		reaperInterval: interval,
		reaperStop:     make(chan struct{}),
		reaperDone:     make(chan struct{}),
	}
	if interval > 0 {
		r.startReaper()
	} else {
		// Reaper disabled: close the done channel so Close does not
		// block waiting for a goroutine that never started.
		close(r.reaperDone)
	}
	return r, nil
}

// startReaper kicks off the background sweep that retries enqueue for
// any callback row whose dispatched flag is still false. Idempotent:
// repeated calls (e.g. after a hot config reload) are no-ops because
// reaperStarted gates the goroutine spawn.
//
// The reaper:
//   - Wakes every reaperInterval (default 15s).
//   - Calls FindUndispatchedCallbacks(limit=100) - filtered SELECT on
//     the job_batches index.
//   - For each row, builds a BatchCallbackJob and PushCtx's it via the
//     wired callback queue driver. On success, calls
//     MarkCallbackDispatched so the row is no longer eligible.
//   - On PushCtx failure (driver down, ctx canceled, etc.) leaves the
//     dispatched flag false; the next tick retries.
//   - Stops on Close (signals reaperStop, waits via reaperDone).
//
// Wrapped in async.Go so any unrecovered panic in the loop is reported
// via the framework's panic logger rather than crashing the worker
// process. The async.Go path also reseeds the loop after panic so a
// single bad iteration does not orphan the goroutine.
func (r *DatabaseBatchRepository) startReaper() {
	if !r.reaperStarted.CompareAndSwap(false, true) {
		return
	}
	async.Go(func() {
		defer close(r.reaperDone)
		ticker := time.NewTicker(r.reaperInterval)
		defer ticker.Stop()
		for {
			select {
			case <-r.reaperStop:
				return
			case <-ticker.C:
				r.reaperTick()
			}
		}
	})
}

// reaperTick is one sweep. Pulled out so tests can drive a single pass
// without waiting for the timer to fire. Errors from
// FindUndispatchedCallbacks are swallowed: this is a best-effort retry
// loop, and surfacing a Logger here would require dragging the queue
// Logger type into the repository struct. Storage failures become
// visible at the next worker.go call site, which has the logger wired.
func (r *DatabaseBatchRepository) reaperTick() {
	if r.closed.Load() {
		return
	}
	rows, err := r.FindUndispatchedCallbacks(context.Background(), reaperBatchSize)
	if err != nil || len(rows) == 0 {
		return
	}
	driver := callbackQueueDriver()
	if driver == nil {
		// No queue driver wired - nothing to dispatch onto. Leaving the
		// flag false means the reaper retries when a driver is wired
		// (typically from the framework's initQueue on next boot).
		return
	}
	queueName := callbackQueueName()
	for _, row := range rows {
		if r.closed.Load() {
			return
		}
		job := &BatchCallbackJob{
			BatchID:  row.BatchID,
			Name:     row.Name,
			Kind:     row.Kind,
			ErrorMsg: row.ErrMsg,
		}
		// Per-row bounded timeout so a slow driver does not block the
		// whole sweep. The next tick reclaims any row that did not
		// finish.
		pushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		// pushBatchCallback routes through DedupeAwarePusher when the
		// driver supports it (postgres, mysql, sqlite via job_dedupe;
		// memory via in-process map; Redis via SETNX). That makes the
		// retry idempotent at the queue layer: a successful push on a
		// prior tick whose MarkCallbackDispatched then failed will
		// no-op here instead of inserting a duplicate row.
		err := pushBatchCallback(pushCtx, driver, queueName, job)
		cancel()
		if err != nil {
			// Enqueue failed: leave dispatched=false so the next tick
			// retries. Recording the error against the row would be
			// useful but is out of scope for this fix.
			continue
		}
		// PushIfNotExistsCtx succeeded (either inserted a fresh row
		// or no-op'd against an existing dedupe key). Mark the
		// dispatched flag so the reaper stops sweeping this row.
		// MarkCallbackDispatched uses a fresh background context
		// with a short timeout because the inherited context here is
		// already detached from the original caller.
		markCtx, markCancel := context.WithTimeout(context.Background(), 5*time.Second)
		_ = r.MarkCallbackDispatched(markCtx, row.BatchID, row.Kind)
		markCancel()
	}
}

// rewriteQuery converts `$N` placeholders to `?` for mysql/sqlite. Thin
// method over the package-level rewriteQueryFor so the repository does not
// depend on a Driver instance (callers may use it without the queue driver).
func (r *DatabaseBatchRepository) rewriteQuery(q string) string {
	return rewriteQueryFor(r.dbDriver, q)
}

// batchRowScanner is the minimal surface shared by *sql.Row and *sql.Rows;
// scanBatchRow accepts either.
type batchRowScanner interface{ Scan(dest ...any) error }

// scanBatchRow reconstructs an in-process *Batch from a row selected with
// the canonical column list. The column order MUST match the SELECT lists
// in Find and incrementCounter exactly:
//
//	id, total_jobs, pending_jobs, completed_jobs, failed_jobs,
//	allow_failures, queue, then_callback, catch_callback, finally_callback,
//	then_dispatched, catch_dispatched, finally_dispatched,
//	cancelled_at, completed_at, last_error
//
// Local callback closures are attached from the global registry when this
// process dispatched the batch; cross-process workers get nil closures and
// rely on the persisted callback names instead. The raw Scan error is
// returned unwrapped so callers can apply their own context (and detect
// sql.ErrNoRows).
func scanBatchRow(scanner batchRowScanner) (*Batch, error) {
	var (
		rid               string
		totalJobs         int
		pendingJobs       int32
		completedJobs     int32
		failedJobs        int32
		allowFailures     bool
		queueName         string
		thenCallback      sql.NullString
		catchCallback     sql.NullString
		finallyCallback   sql.NullString
		thenDispatched    bool
		catchDispatched   bool
		finallyDispatched bool
		cancelledAt       sql.NullTime
		completedAt       sql.NullTime
		lastError         sql.NullString
	)
	if err := scanner.Scan(&rid, &totalJobs, &pendingJobs, &completedJobs, &failedJobs,
		&allowFailures, &queueName, &thenCallback, &catchCallback, &finallyCallback,
		&thenDispatched, &catchDispatched, &finallyDispatched,
		&cancelledAt, &completedAt, &lastError); err != nil {
		return nil, err
	}

	b := &Batch{
		id:            BatchID(rid),
		totalJobs:     totalJobs,
		allowFailures: allowFailures,
		queue:         queueName,
	}
	b.pendingJobs.Store(pendingJobs)
	b.completedJobs.Store(completedJobs)
	b.failedJobs.Store(failedJobs)
	if thenCallback.Valid {
		b.thenName = thenCallback.String
	}
	if catchCallback.Valid {
		b.catchName = catchCallback.String
	}
	if finallyCallback.Valid {
		b.finallyName = finallyCallback.String
	}
	b.thenDispatched.Store(thenDispatched)
	b.catchDispatched.Store(catchDispatched)
	b.finallyDispatched.Store(finallyDispatched)
	if cancelledAt.Valid {
		b.cancelled.Store(true)
	}
	if completedAt.Valid {
		b.finished.Store(true)
		b.finishedAt = completedAt.Time
	}
	if lastError.Valid {
		b.lastError = lastError.String
	}

	// Attach local closures if this process dispatched the batch. Cross-
	// process workers will not have these and rely on the persisted
	// callback names (resolved via BatchCallbackJob) instead.
	if entry := globalCallbacks.get(b.id); entry != nil {
		b.thenFn = entry.thenFn
		b.catchFn = entry.catchFn
		b.finallyFn = entry.finallyFn
		b.dispatchEvent = entry.dispatchEvent
	}
	return b, nil
}

// Find loads a batch row and reconstructs an in-process *Batch from it.
// The reconstructed Batch carries the persisted counters (pending,
// completed, failed) and flags (cancelled, finished). Local callback
// closures are populated from the global registry when present so that
// a worker process which is also the dispatcher process can fire
// Then/Catch/Finally directly without a separate event hop.
func (r *DatabaseBatchRepository) Find(ctx context.Context, id BatchID) (*Batch, error) {
	if err := r.closedErr(); err != nil {
		return nil, err
	}
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	const q = `SELECT id, total_jobs, pending_jobs, completed_jobs, failed_jobs,
	                  allow_failures, queue, then_callback, catch_callback, finally_callback,
	                  then_dispatched, catch_dispatched, finally_dispatched,
	                  cancelled_at, completed_at, last_error
	           FROM job_batches WHERE id = $1`
	row := r.db.QueryRowContext(ctx, r.rewriteQuery(q), string(id))

	b, err := scanBatchRow(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("velocity/queue: batch find: %w", err)
	}
	return b, nil
}

// Save inserts a new batch row. Idempotent under primary-key conflict so
// retried dispatch calls (rare; e.g. transient DB blip on the INSERT)
// surface as a clear error rather than corrupting state.
func (r *DatabaseBatchRepository) Save(ctx context.Context, batch *Batch) error {
	if err := r.closedErr(); err != nil {
		return err
	}
	if err := ctxErr(ctx); err != nil {
		return err
	}
	now := time.Now().UTC()
	// The *_dispatched columns are explicitly initialised to false here
	// rather than relying on the DEFAULT 0 so a misapplied migration that
	// dropped the DEFAULT (or a re-issued INSERT against a row built from
	// schema-v1) produces a deterministic failure instead of inheriting
	// stale state.
	const q = `INSERT INTO job_batches
	    (id, total_jobs, pending_jobs, completed_jobs, failed_jobs,
	     allow_failures, queue, then_callback, catch_callback, finally_callback,
	     then_dispatched, catch_dispatched, finally_dispatched,
	     cancelled_at, completed_at, last_error, created_at, updated_at)
	    VALUES ($1, $2, $3, 0, 0, $4, $5, $6, $7, $8, $9, $10, $11, NULL, NULL, NULL, $12, $13)`
	_, err := r.db.ExecContext(ctx, r.rewriteQuery(q),
		string(batch.id),
		batch.totalJobs,
		batch.pendingJobs.Load(),
		batch.allowFailures,
		batch.queue,
		nullableString(batch.thenName),
		nullableString(batch.catchName),
		nullableString(batch.finallyName),
		false, false, false,
		now,
		now,
	)
	if err != nil {
		return fmt.Errorf("velocity/queue: batch save: %w", err)
	}
	return nil
}

// nullableString returns a sql.NullString from a Go string, preferring
// NULL over the empty-string sentinel so a column lookup can distinguish
// "callback unset" from "callback explicitly empty".
func nullableString(s string) sql.NullString {
	if s == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: s, Valid: true}
}

// IncrementSuccess atomically decrements pending_jobs and increments
// completed_jobs. When the decrement would drive pending_jobs to zero,
// the same statement sets completed_at via a `WHERE completed_at IS NULL`
// guard so exactly one increment in the fleet observes the transition
// (justFinished == true on that caller, false on every other).
//
// The implementation runs inside a transaction so the dispatch-time
// readback returns the post-update counter values consistent with the
// row that won the CAS. Without the readback we would have to issue a
// follow-up SELECT and could race with a sibling worker's Cancel.
func (r *DatabaseBatchRepository) IncrementSuccess(ctx context.Context, id BatchID) (*Batch, bool, error) {
	return r.incrementCounter(ctx, id, true, false, nil)
}

// IncrementFailure mirrors IncrementSuccess but increments failed_jobs
// instead of completed_jobs and stores the error on last_error for
// post-mortem inspection. Truncated to 4 KiB so a runaway error chain
// does not bloat the row; full error text remains available on the
// failed_jobs row written by Driver.Failed.
func (r *DatabaseBatchRepository) IncrementFailure(ctx context.Context, id BatchID, jobErr error) (*Batch, bool, error) {
	var truncated string
	if jobErr != nil {
		truncated = truncateErrorText(jobErr.Error(), 4096)
	}
	return r.incrementCounter(ctx, id, false, true, &truncated)
}

// DecrementPending decrements pending_jobs without touching completed or
// failed, matching the in-memory repo's contract for jobs skipped because
// their batch was cancelled before processing began.
func (r *DatabaseBatchRepository) DecrementPending(ctx context.Context, id BatchID) (*Batch, bool, error) {
	return r.incrementCounter(ctx, id, false, false, nil)
}

// incrementCounter is the shared body for the three mutating operations.
// success / failure are exclusive; both false means a pure pending
// decrement (skip path). The optional errText is appended to last_error
// only when failure is true.
//
// The function is structured to issue ONE UPDATE that does all of:
//   - decrement pending_jobs (clamped at 0)
//   - optionally increment completed_jobs or failed_jobs
//   - optionally set last_error
//   - on the transition to "no remaining work", set completed_at = NOW()
//     iff completed_at IS NULL (the CAS that gates callback firing)
//
// Then a follow-up SELECT inside the same transaction returns the new
// counter values and tells us whether *this* UPDATE was the one that
// set completed_at (justFinished == row.completed_at == NOW for this call).
//
// SQLite does not support `RETURNING *` on older versions, so we run
// a SELECT for portability. The row is already locked by the UPDATE.
func (r *DatabaseBatchRepository) incrementCounter(ctx context.Context, id BatchID, success, failure bool, errText *string) (*Batch, bool, error) {
	if err := r.closedErr(); err != nil {
		return nil, false, err
	}
	if err := ctxErr(ctx); err != nil {
		return nil, false, err
	}

	var txOpts *sql.TxOptions
	if r.dbDriver == "sqlite" || r.dbDriver == "sqlite3" {
		txOpts = &sql.TxOptions{Isolation: sql.LevelSerializable}
	}
	tx, err := r.db.BeginTx(ctx, txOpts)
	if err != nil {
		return nil, false, fmt.Errorf("velocity/queue: batch increment begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()

	updateQ, args := buildIncrementUpdate(id, success, failure, errText, now)
	res, execErr := tx.ExecContext(ctx, r.rewriteQuery(updateQ), args...)
	if execErr != nil {
		return nil, false, fmt.Errorf("velocity/queue: batch increment update: %w", execErr)
	}
	rowsAff, _ := res.RowsAffected()
	if rowsAff == 0 {
		// Batch row missing (deleted or never saved). Mirror the
		// in-memory repo's "not found" return so callers fall back to
		// no-op behaviour.
		_ = tx.Commit()
		return nil, false, nil
	}

	// Step 2: the completion CAS. Set completed_at to `now` only when
	// pending_jobs has reached zero AND completed_at is still NULL. The
	// number of rows affected by this UPDATE is exactly 1 for the
	// caller that wins the race and 0 for every other caller (including
	// remote workers in a different process), which is precisely the
	// justFinished signal we need.
	const casQ = `UPDATE job_batches SET completed_at = $1, updated_at = $2
	             WHERE id = $3 AND pending_jobs = 0 AND completed_at IS NULL`
	casRes, casErr := tx.ExecContext(ctx, r.rewriteQuery(casQ), now, now, string(id))
	if casErr != nil {
		return nil, false, fmt.Errorf("velocity/queue: batch completion CAS: %w", casErr)
	}
	casRows, _ := casRes.RowsAffected()
	justFinished := casRows == 1

	// Step 3: read back the post-update row, including the persisted
	// callback names so the caller can use them to enqueue cross-process
	// BatchCallbackJob instances on terminal completion. The dispatched
	// flags are also returned so callers can short-circuit re-enqueue
	// when a previous tick (or a sibling worker) already pushed the job.
	const selQ = `SELECT id, total_jobs, pending_jobs, completed_jobs, failed_jobs,
	                     allow_failures, queue, then_callback, catch_callback, finally_callback,
	                     then_dispatched, catch_dispatched, finally_dispatched,
	                     cancelled_at, completed_at, last_error
	              FROM job_batches WHERE id = $1`
	row := tx.QueryRowContext(ctx, r.rewriteQuery(selQ), string(id))
	b, scanErr := scanBatchRow(row)
	if scanErr != nil {
		return nil, false, fmt.Errorf("velocity/queue: batch increment readback: %w", scanErr)
	}

	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("velocity/queue: batch increment commit: %w", err)
	}

	return b, justFinished, nil
}

// Cancel sets cancelled_at iff it is currently NULL. The CAS makes
// repeated Cancel calls idempotent and the timestamp lets DB inspectors
// see exactly when cancellation propagated.
func (r *DatabaseBatchRepository) Cancel(ctx context.Context, id BatchID) (*Batch, error) {
	if err := r.closedErr(); err != nil {
		return nil, err
	}
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	const q = `UPDATE job_batches SET cancelled_at = $1, updated_at = $2
	           WHERE id = $3 AND cancelled_at IS NULL`
	now := time.Now().UTC()
	if _, err := r.db.ExecContext(ctx, r.rewriteQuery(q), now, now, string(id)); err != nil {
		return nil, fmt.Errorf("velocity/queue: batch cancel: %w", err)
	}
	return r.Find(ctx, id)
}

// Delete removes a batch row. Primarily for tests.
func (r *DatabaseBatchRepository) Delete(ctx context.Context, id BatchID) error {
	if err := r.closedErr(); err != nil {
		return err
	}
	if err := ctxErr(ctx); err != nil {
		return err
	}
	const q = `DELETE FROM job_batches WHERE id = $1`
	if _, err := r.db.ExecContext(ctx, r.rewriteQuery(q), string(id)); err != nil {
		return fmt.Errorf("velocity/queue: batch delete: %w", err)
	}
	globalCallbacks.remove(id)
	return nil
}

// PruneStale removes finished batches whose completed_at is older than
// olderThan. Callers typically run this from a periodic scheduler job
// so the table does not grow unbounded.
func (r *DatabaseBatchRepository) PruneStale(ctx context.Context, olderThan time.Duration) (int, error) {
	if err := r.closedErr(); err != nil {
		return 0, err
	}
	if err := ctxErr(ctx); err != nil {
		return 0, err
	}
	cutoff := time.Now().UTC().Add(-olderThan)
	const q = `DELETE FROM job_batches WHERE completed_at IS NOT NULL AND completed_at < $1`
	res, err := r.db.ExecContext(ctx, r.rewriteQuery(q), cutoff)
	if err != nil {
		return 0, fmt.Errorf("velocity/queue: batch prune: %w", err)
	}
	rows, _ := res.RowsAffected()
	return int(rows), nil
}

// MarkCallbackDispatched flips the row's `<kind>_dispatched` column to
// true. Used by the inline dispatch path (after a successful PushCtx)
// and by the reaper (after a successful retried PushCtx). The UPDATE is
// monotonic-set with an `... AND <kind>_dispatched = false` predicate so
// duplicate calls are no-ops and we don't churn the row.
func (r *DatabaseBatchRepository) MarkCallbackDispatched(ctx context.Context, id BatchID, kind CallbackKind) error {
	if err := r.closedErr(); err != nil {
		return err
	}
	if err := ctxErr(ctx); err != nil {
		return err
	}
	col, err := dispatchedColumnFor(kind)
	if err != nil {
		return err
	}
	q := fmt.Sprintf(`UPDATE job_batches SET %s = $1, updated_at = $2 WHERE id = $3 AND %s = $4`, col, col)
	now := time.Now().UTC()
	if _, err := r.db.ExecContext(ctx, r.rewriteQuery(q), true, now, string(id), false); err != nil {
		return fmt.Errorf("velocity/queue: mark callback dispatched: %w", err)
	}
	return nil
}

// FindUndispatchedCallbacks returns up to `limit` callback rows that
// still need to be enqueued. The query unions three kinds via a single
// SELECT plus client-side classification so we avoid three round trips:
//
//   - then_callback non-null, then_dispatched=false, batch completed
//     without failures.
//   - catch_callback non-null, catch_dispatched=false, batch has at
//     least one failure (Catch fires on first failure, independent of
//     terminal completion).
//   - finally_callback non-null, finally_dispatched=false, batch
//     completed (with or without failures).
//
// The result is the cheapest portable form: one row per BatchID with
// the column flags, and the Go side fans out the kinds. limit applies
// to BATCH ROWS, not callback rows; in practice each batch yields at
// most 3 callbacks so the row count is bounded by 3*limit.
func (r *DatabaseBatchRepository) FindUndispatchedCallbacks(ctx context.Context, limit int) ([]UndispatchedCallback, error) {
	if err := r.closedErr(); err != nil {
		return nil, err
	}
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = reaperBatchSize
	}
	q := `SELECT id, completed_at, failed_jobs,
	             then_callback, then_dispatched,
	             catch_callback, catch_dispatched,
	             finally_callback, finally_dispatched,
	             last_error
	      FROM job_batches
	      WHERE (
	          (then_callback IS NOT NULL AND then_dispatched = $1 AND completed_at IS NOT NULL AND failed_jobs = 0)
	       OR (catch_callback IS NOT NULL AND catch_dispatched = $2 AND failed_jobs > 0)
	       OR (finally_callback IS NOT NULL AND finally_dispatched = $3 AND completed_at IS NOT NULL)
	      )
	      ORDER BY completed_at ASC
	      LIMIT ` + fmt.Sprintf("%d", limit)
	rows, err := r.db.QueryContext(ctx, r.rewriteQuery(q), false, false, false)
	if err != nil {
		return nil, fmt.Errorf("velocity/queue: find undispatched callbacks: %w", err)
	}
	defer rows.Close()

	var out []UndispatchedCallback
	for rows.Next() {
		var (
			id                string
			completedAt       sql.NullTime
			failedJobs        int32
			thenCallback      sql.NullString
			thenDispatched    bool
			catchCallback     sql.NullString
			catchDispatched   bool
			finallyCallback   sql.NullString
			finallyDispatched bool
			lastError         sql.NullString
		)
		if scanErr := rows.Scan(&id, &completedAt, &failedJobs,
			&thenCallback, &thenDispatched,
			&catchCallback, &catchDispatched,
			&finallyCallback, &finallyDispatched,
			&lastError); scanErr != nil {
			return nil, fmt.Errorf("velocity/queue: scan undispatched callback row: %w", scanErr)
		}
		bid := BatchID(id)
		errMsg := ""
		if lastError.Valid {
			errMsg = lastError.String
		}
		// Catch is independent of terminal completion.
		if catchCallback.Valid && !catchDispatched && failedJobs > 0 {
			out = append(out, UndispatchedCallback{
				BatchID: bid,
				Kind:    CallbackCatch,
				Name:    catchCallback.String,
				ErrMsg:  errMsg,
			})
		}
		if completedAt.Valid {
			if thenCallback.Valid && !thenDispatched && failedJobs == 0 {
				out = append(out, UndispatchedCallback{
					BatchID: bid,
					Kind:    CallbackThen,
					Name:    thenCallback.String,
				})
			}
			if finallyCallback.Valid && !finallyDispatched {
				out = append(out, UndispatchedCallback{
					BatchID: bid,
					Kind:    CallbackFinally,
					Name:    finallyCallback.String,
					ErrMsg:  errMsg,
				})
			}
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("velocity/queue: iterate undispatched callbacks: %w", err)
	}
	return out, nil
}

// dispatchedColumnFor maps a CallbackKind to its dispatched column
// name. Identifier validation lives here (not in SQL builders) so a
// future caller cannot pass a user-controlled string and get arbitrary
// SQL injected. Returns an error rather than panicking so the reaper
// can log and continue.
func dispatchedColumnFor(kind CallbackKind) (string, error) {
	switch kind {
	case CallbackThen:
		return "then_dispatched", nil
	case CallbackCatch:
		return "catch_dispatched", nil
	case CallbackFinally:
		return "finally_dispatched", nil
	default:
		return "", fmt.Errorf("velocity/queue: unknown callback kind %q", kind)
	}
}

// Close marks the repository as closed so subsequent operations return
// ErrBatchRepositoryClosed instead of writing to a *sql.DB that may
// already be torn down by the ORM manager. Also signals the reaper
// goroutine to stop and waits for it to exit so a subsequent
// NewDatabaseBatchRepository on the same process does not race with a
// zombie reaper.
//
// The *sql.DB itself is NOT closed here: it was injected by the caller
// (typically the framework's ORM manager) and its lifecycle is owned by
// whoever passed it in. Closing it here would tear down sibling
// subsystems (cache, outbox, notification) that share the same pool.
//
// Idempotent: repeated Close calls succeed without panicking, which the
// shutdown sequence relies on (App.Shutdown may close a repo that was
// also installed via SetDefaultBatchRepository earlier).
func (r *DatabaseBatchRepository) Close() error {
	r.closed.Store(true)
	r.reaperStopOnce.Do(func() { close(r.reaperStop) })
	// Wait for the reaper goroutine to exit before returning. A bounded
	// wait would be safer if the reaper could hang on PushCtx, but each
	// PushCtx is already wrapped in a 5s timeout so the worst case is
	// one in-flight iteration completes before close returns.
	if r.reaperDone != nil {
		<-r.reaperDone
	}
	return nil
}

// closedErr returns ErrBatchRepositoryClosed when Close has been called.
// Callers should treat this exactly like ctxErr and abort the operation.
func (r *DatabaseBatchRepository) closedErr() error {
	if r.closed.Load() {
		return ErrBatchRepositoryClosed
	}
	return nil
}

// buildIncrementUpdate constructs the SET clause for the counter UPDATE.
//
// SQL ordering matters for portability. MySQL evaluates a multi-column
// SET left-to-right within a single UPDATE: by the time the RHS of the
// nth assignment runs, earlier columns in the same UPDATE have already
// been mutated. PostgreSQL and SQLite evaluate every RHS against the
// pre-update row, which is what the previous version of this code
// silently relied on.
//
// Concretely, "SET pending_jobs = pending_jobs - 1, completed_jobs =
// CASE WHEN pending_jobs > 0 THEN completed_jobs + 1 ELSE completed_jobs
// END" under MySQL sees pending_jobs already at the new value when it
// computes completed_jobs's RHS, so the clamp fires for legitimate
// increments and completed_jobs never advances. Tests under SQLite
// would not detect that, since SQLite uses pre-update semantics.
//
// The fix is dialect-portable: place completed_jobs / failed_jobs /
// last_error BEFORE pending_jobs in the SET list and keep them keyed on
// the same `pending_jobs > 0` predicate. Under MySQL the counter
// columns now execute against the pre-decrement value; under Postgres
// and SQLite the order is irrelevant because the RHS already reads the
// pre-update row. The invariant "completed_jobs + failed_jobs <=
// total_jobs" holds on all three engines.
//
// Placeholders are written as $1...$N in the order they first appear in
// the SET clause so the args slice can be built in lock-step and the
// rewriteQuery helper can rewrite to `?` for mysql/sqlite without
// permuting positional bindings.
//
// Returned as (query, args) so this can be unit-tested without a DB.
func buildIncrementUpdate(id BatchID, success, failure bool, errText *string, now time.Time) (string, []any) {
	set := []string{}
	var args []any
	nextPH := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}

	if success {
		set = append(set, "completed_jobs = CASE WHEN pending_jobs > 0 THEN completed_jobs + 1 ELSE completed_jobs END")
	}
	if failure {
		set = append(set, "failed_jobs = CASE WHEN pending_jobs > 0 THEN failed_jobs + 1 ELSE failed_jobs END")
		if errText != nil {
			ph := nextPH(*errText)
			set = append(set, fmt.Sprintf("last_error = CASE WHEN pending_jobs > 0 THEN %s ELSE last_error END", ph))
		}
	}
	// pending_jobs decrement MUST follow the counter columns above so
	// MySQL's left-to-right SET evaluation does not poison the counter
	// CASE predicates with the already-decremented value.
	set = append(set, "pending_jobs = CASE WHEN pending_jobs > 0 THEN pending_jobs - 1 ELSE pending_jobs END")
	set = append(set, fmt.Sprintf("updated_at = %s", nextPH(now)))

	idPH := nextPH(string(id))
	q := fmt.Sprintf("UPDATE job_batches SET %s WHERE id = %s", strings.Join(set, ", "), idPH)
	return q, args
}

// truncateErrorText caps an error string at max bytes. Multi-byte safe:
// the truncation respects rune boundaries by trimming any trailing
// continuation bytes so the persisted string is always valid UTF-8.
func truncateErrorText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut]
}
