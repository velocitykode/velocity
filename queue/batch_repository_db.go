package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// ErrBatchRepositoryClosed is returned when an operation hits a closed
// DatabaseBatchRepository. App.Shutdown closes the auto-installed repo
// to release its references before the underlying *sql.DB is closed by
// the ORM manager; an in-flight worker that still holds a stale repo
// pointer needs a deterministic error rather than a panic on a torn-
// down connection pool.
var ErrBatchRepositoryClosed = errors.New("velocity/queue: batch repository is closed")

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
	if err := r.closedErr(); err != nil {
		return nil, err
	}
	if err := ctxErr(ctx); err != nil {
		return nil, err
	}
	const q = `SELECT id, total_jobs, pending_jobs, completed_jobs, failed_jobs,
	                  allow_failures, queue, then_callback, catch_callback, finally_callback,
	                  cancelled_at, completed_at, last_error
	           FROM job_batches WHERE id = $1`
	row := r.db.QueryRowContext(ctx, r.rewriteQuery(q), string(id))

	var (
		rid             string
		totalJobs       int
		pendingJobs     int32
		completedJobs   int32
		failedJobs      int32
		allowFailures   bool
		queueName       string
		thenCallback    sql.NullString
		catchCallback   sql.NullString
		finallyCallback sql.NullString
		cancelledAt     sql.NullTime
		completedAt     sql.NullTime
		lastError       sql.NullString
	)
	if err := row.Scan(&rid, &totalJobs, &pendingJobs, &completedJobs, &failedJobs,
		&allowFailures, &queueName, &thenCallback, &catchCallback, &finallyCallback,
		&cancelledAt, &completedAt, &lastError); err != nil {
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
	if thenCallback.Valid {
		b.thenName = thenCallback.String
	}
	if catchCallback.Valid {
		b.catchName = catchCallback.String
	}
	if finallyCallback.Valid {
		b.finallyName = finallyCallback.String
	}
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
	now := time.Now()
	const q = `INSERT INTO job_batches
	    (id, total_jobs, pending_jobs, completed_jobs, failed_jobs,
	     allow_failures, queue, then_callback, catch_callback, finally_callback,
	     cancelled_at, completed_at, last_error, created_at, updated_at)
	    VALUES ($1, $2, $3, 0, 0, $4, $5, $6, $7, $8, NULL, NULL, NULL, $9, $10)`
	_, err := r.db.ExecContext(ctx, r.rewriteQuery(q),
		string(batch.id),
		batch.totalJobs,
		batch.pendingJobs.Load(),
		batch.allowFailures,
		batch.queue,
		nullableString(batch.thenName),
		nullableString(batch.catchName),
		nullableString(batch.finallyName),
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
	// BatchCallbackJob instances on terminal completion.
	const selQ = `SELECT total_jobs, pending_jobs, completed_jobs, failed_jobs,
	                     allow_failures, queue, then_callback, catch_callback, finally_callback,
	                     cancelled_at, completed_at, last_error
	              FROM job_batches WHERE id = $1`
	row := tx.QueryRowContext(ctx, r.rewriteQuery(selQ), string(id))
	var (
		totalJobs       int
		pendingJobs     int32
		completedJobs   int32
		failedJobs      int32
		allowFailures   bool
		queueName       string
		thenCallback    sql.NullString
		catchCallback   sql.NullString
		finallyCallback sql.NullString
		cancelledAt     sql.NullTime
		completedAt     sql.NullTime
		lastError       sql.NullString
	)
	if scanErr := row.Scan(&totalJobs, &pendingJobs, &completedJobs, &failedJobs,
		&allowFailures, &queueName, &thenCallback, &catchCallback, &finallyCallback,
		&cancelledAt, &completedAt, &lastError); scanErr != nil {
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
	if thenCallback.Valid {
		b.thenName = thenCallback.String
	}
	if catchCallback.Valid {
		b.catchName = catchCallback.String
	}
	if finallyCallback.Valid {
		b.finallyName = finallyCallback.String
	}
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
	if err := r.closedErr(); err != nil {
		return nil, err
	}
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
	cutoff := time.Now().Add(-olderThan)
	const q = `DELETE FROM job_batches WHERE completed_at IS NOT NULL AND completed_at < $1`
	res, err := r.db.ExecContext(ctx, r.rewriteQuery(q), cutoff)
	if err != nil {
		return 0, fmt.Errorf("velocity/queue: batch prune: %w", err)
	}
	rows, _ := res.RowsAffected()
	return int(rows), nil
}

// Close marks the repository as closed so subsequent operations return
// ErrBatchRepositoryClosed instead of writing to a *sql.DB that may
// already be torn down by the ORM manager.
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
