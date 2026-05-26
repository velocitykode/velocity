package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DatabaseBatchRepository persists batch state in a SQL table so workers
// on any host can observe progress, cancellation, and completion.
//
// C-03 root cause: the prior in-memory `batchStore` map was process-local;
// a worker on host B that popped a Batchable job dispatched from host A
// would not find the batch and would silently skip cancel checks and
// progress counters. The DB-backed repository replaces that map with a
// shared `job_batches` row whose counters move atomically under SQL
// UPDATEs. The Then/Catch/Finally callbacks remain in the dispatcher
// process's callback registry (closures cannot cross processes); the
// repository's CAS on `completed_at` guarantees the dispatcher fires
// each terminal callback at most once even when the last job completes
// on a remote worker.
//
// Supported drivers: postgres, mysql, sqlite. Placeholders are written
// as `$N` and rewritten to `?` for mysql/sqlite by rewriteQuery.
type DatabaseBatchRepository struct {
	db       *sql.DB
	dbDriver string
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
	return &DatabaseBatchRepository{db: db, dbDriver: dbDriver}, nil
}

// rewriteQuery converts `$N` placeholders to `?` for mysql/sqlite.
// Identical to DatabaseDriver.rewriteQuery; duplicated here so the
// repository does not depend on a Driver instance (callers may use
// it without the queue driver).
func (r *DatabaseBatchRepository) rewriteQuery(q string) string {
	if r.dbDriver == "postgres" {
		return q
	}
	var b strings.Builder
	b.Grow(len(q))
	for i := 0; i < len(q); i++ {
		if q[i] == '$' && i+1 < len(q) && q[i+1] >= '0' && q[i+1] <= '9' {
			j := i + 1
			for j < len(q) && q[j] >= '0' && q[j] <= '9' {
				j++
			}
			b.WriteByte('?')
			i = j - 1
			continue
		}
		b.WriteByte(q[i])
	}
	return b.String()
}

// Find loads a batch row and reconstructs an in-process *Batch from it.
// The reconstructed Batch carries the persisted counters (pending,
// completed, failed) and flags (cancelled, finished). Local callback
// closures are populated from the global registry when present so that
// a worker process which is also the dispatcher process can fire
// Then/Catch/Finally directly without a separate event hop.
func (r *DatabaseBatchRepository) Find(ctx context.Context, id BatchID) (*Batch, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	const q = `SELECT id, total_jobs, pending_jobs, completed_jobs, failed_jobs,
	                  allow_failures, queue, cancelled_at, completed_at, last_error
	           FROM job_batches WHERE id = $1`
	row := r.db.QueryRowContext(ctx, r.rewriteQuery(q), string(id))

	var (
		rid           string
		totalJobs     int
		pendingJobs   int32
		completedJobs int32
		failedJobs    int32
		allowFailures bool
		queueName     string
		cancelledAt   sql.NullTime
		completedAt   sql.NullTime
		lastError     sql.NullString
	)
	if err := row.Scan(&rid, &totalJobs, &pendingJobs, &completedJobs, &failedJobs,
		&allowFailures, &queueName, &cancelledAt, &completedAt, &lastError); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("velocity/queue: batch find: %w", err)
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

	// Attach local callbacks if this process dispatched the batch. Cross-
	// process workers will not have these (closures don't serialise) and
	// rely on BatchCompleted events for downstream coordination.
	if entry := globalCallbacks.get(b.id); entry != nil {
		b.thenFn = entry.thenFn
		b.catchFn = entry.catchFn
		b.finallyFn = entry.finallyFn
		b.dispatchEvent = entry.dispatchEvent
	}

	return b, nil
}

// Save inserts a new batch row. Idempotent under primary-key conflict so
// retried dispatch calls (rare; e.g. transient DB blip on the INSERT)
// surface as a clear error rather than corrupting state.
func (r *DatabaseBatchRepository) Save(ctx context.Context, batch *Batch) error {
	if err := ctxErr(ctx); err != nil {
		return err
	}
	now := time.Now()
	const q = `INSERT INTO job_batches
	    (id, total_jobs, pending_jobs, completed_jobs, failed_jobs,
	     allow_failures, queue, cancelled_at, completed_at, last_error, created_at, updated_at)
	    VALUES ($1, $2, $3, 0, 0, $4, $5, NULL, NULL, NULL, $6, $7)`
	_, err := r.db.ExecContext(ctx, r.rewriteQuery(q),
		string(batch.id),
		batch.totalJobs,
		batch.pendingJobs.Load(),
		batch.allowFailures,
		batch.queue,
		now,
		now,
	)
	if err != nil {
		return fmt.Errorf("velocity/queue: batch save: %w", err)
	}
	return nil
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

	// Step 1: atomically decrement pending_jobs and optionally bump
	// completed_jobs / failed_jobs / last_error. The row lock acquired
	// by the UPDATE serialises concurrent writers, which is what we
	// need for the follow-up CAS to be safe.
	set := []string{
		"pending_jobs = CASE WHEN pending_jobs > 0 THEN pending_jobs - 1 ELSE 0 END",
		"updated_at = $1",
	}
	args := []any{now}
	if success {
		set = append(set, "completed_jobs = completed_jobs + 1")
	}
	if failure {
		set = append(set, "failed_jobs = failed_jobs + 1")
		if errText != nil {
			args = append(args, *errText)
			set = append(set, fmt.Sprintf("last_error = $%d", len(args)))
		}
	}
	args = append(args, string(id))
	updateQ := fmt.Sprintf("UPDATE job_batches SET %s WHERE id = $%d", strings.Join(set, ", "), len(args))
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

	// Step 3: read back the post-update row.
	const selQ = `SELECT total_jobs, pending_jobs, completed_jobs, failed_jobs,
	                     allow_failures, queue, cancelled_at, completed_at, last_error
	              FROM job_batches WHERE id = $1`
	row := tx.QueryRowContext(ctx, r.rewriteQuery(selQ), string(id))
	var (
		totalJobs     int
		pendingJobs   int32
		completedJobs int32
		failedJobs    int32
		allowFailures bool
		queueName     string
		cancelledAt   sql.NullTime
		completedAt   sql.NullTime
		lastError     sql.NullString
	)
	if scanErr := row.Scan(&totalJobs, &pendingJobs, &completedJobs, &failedJobs,
		&allowFailures, &queueName, &cancelledAt, &completedAt, &lastError); scanErr != nil {
		return nil, false, fmt.Errorf("velocity/queue: batch increment readback: %w", scanErr)
	}

	if err := tx.Commit(); err != nil {
		return nil, false, fmt.Errorf("velocity/queue: batch increment commit: %w", err)
	}

	b := &Batch{
		id:            id,
		totalJobs:     totalJobs,
		allowFailures: allowFailures,
		queue:         queueName,
	}
	b.pendingJobs.Store(pendingJobs)
	b.completedJobs.Store(completedJobs)
	b.failedJobs.Store(failedJobs)
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

	if entry := globalCallbacks.get(id); entry != nil {
		b.thenFn = entry.thenFn
		b.catchFn = entry.catchFn
		b.finallyFn = entry.finallyFn
		b.dispatchEvent = entry.dispatchEvent
	}

	return b, justFinished, nil
}

// Cancel sets cancelled_at iff it is currently NULL. The CAS makes
// repeated Cancel calls idempotent and the timestamp lets DB inspectors
// see exactly when cancellation propagated.
func (r *DatabaseBatchRepository) Cancel(ctx context.Context, id BatchID) (*Batch, error) {
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	const q = `UPDATE job_batches SET cancelled_at = $1, updated_at = $2
	           WHERE id = $3 AND cancelled_at IS NULL`
	now := time.Now()
	if _, err := r.db.ExecContext(ctx, r.rewriteQuery(q), now, now, string(id)); err != nil {
		return nil, fmt.Errorf("velocity/queue: batch cancel: %w", err)
	}
	return r.Find(ctx, id)
}

// Delete removes a batch row. Primarily for tests.
func (r *DatabaseBatchRepository) Delete(ctx context.Context, id BatchID) error {
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
	if err := ctxErr(ctx); err != nil {
		return 0, err
	}
	cutoff := time.Now().Add(-olderThan)
	const q = `DELETE FROM job_batches WHERE completed_at IS NOT NULL AND completed_at < $1`
	res, err := r.db.ExecContext(ctx, r.rewriteQuery(q), cutoff)
	if err != nil {
		return 0, fmt.Errorf("velocity/queue: batch prune: %w", err)
	}
	rows, _ := res.RowsAffected()
	return int(rows), nil
}

// Close is a no-op for the DB repository; the *sql.DB is owned by the
// caller (typically the ORM manager) and closed separately.
func (r *DatabaseBatchRepository) Close() error { return nil }

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
