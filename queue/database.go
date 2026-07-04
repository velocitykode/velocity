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
	"time"

	"github.com/velocitykode/velocity/trace"
)

// rewriteQueryFor expands `$N`-style placeholders in a query template into the
// driver-appropriate form. Queries in this package are authored with `$N`
// placeholders; rewriteQueryFor replaces them with `?` for MySQL/SQLite while
// leaving them intact for Postgres. It takes the driver name (rather than a
// *DatabaseDriver) so the batch repository can reuse it without holding a
// Driver instance.
func rewriteQueryFor(dbDriver, q string) string {
	if dbDriver == "postgres" {
		return q
	}
	// Replace $1..$99 with ?
	var b strings.Builder
	b.Grow(len(q))
	for i := 0; i < len(q); i++ {
		if q[i] == '$' && i+1 < len(q) && q[i+1] >= '0' && q[i+1] <= '9' {
			// skip digits
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

// rewriteQuery rewrites `$N` placeholders for d's database driver. Thin method
// over rewriteQueryFor so call sites keep the d.rewriteQuery(...) spelling.
func (d *DatabaseDriver) rewriteQuery(q string) string {
	return rewriteQueryFor(d.dbDriver, q)
}

// JobRecord represents a job in the database
type JobRecord struct {
	ID           uint      `orm:"primaryKey;autoIncrement" json:"id"`
	Queue        string    `orm:"index"`
	Payload      string    `orm:"type:text"`
	Attempts     int       `orm:"default:0"`
	ScheduledAt  time.Time `orm:"index"`
	ReservedAt   *time.Time
	ReservedBy   *string
	FailedAt     *time.Time
	FailedReason *string
	CreatedAt    time.Time `orm:"autoCreateTime" json:"created_at"`
	UpdatedAt    time.Time `orm:"autoUpdateTime" json:"updated_at"`
}

func (JobRecord) TableName() string {
	return "jobs"
}

// FailedJobRecord represents a failed job
type FailedJobRecord struct {
	ID        uint `orm:"primaryKey;autoIncrement" json:"id"`
	Queue     string
	Payload   string    `orm:"type:text"`
	Exception string    `orm:"type:text"`
	CreatedAt time.Time `orm:"autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `orm:"autoUpdateTime" json:"updated_at"`
}

// DefaultRetryAfter is the lease duration applied to a row reserved by a
// worker. After this elapses without an Ack / Release / FailReserved the
// row becomes eligible for reclamation by the next PopCtxReserved call.
// The value mirrors Laravel's `retry_after` default (90 seconds).
const DefaultRetryAfter = 90 * time.Second

// Compile-time assertion that DatabaseDriver implements all the
// driver-side capability interfaces the worker depends on.
var (
	_ Driver            = (*DatabaseDriver)(nil)
	_ TraceAwareDriver  = (*DatabaseDriver)(nil)
	_ ReservationDriver = (*DatabaseDriver)(nil)
)

// DatabaseDriver implements the Driver interface using database
type DatabaseDriver struct {
	// DriverCore supplies the lock-free event-dispatch slot (SetEventDispatcher
	// / DispatchEvent) shared by every built-in driver. Embedded so the
	// promoted SetEventDispatcher satisfies contract.EventDispatcherAware.
	DriverCore

	mu       sync.RWMutex
	db       *sql.DB
	workerID string
	dbDriver string // "postgres", "mysql", "sqlite"
	// retryAfterNanos holds the row-lease duration in nanoseconds. A reserved
	// row whose reserved_at is older than this becomes eligible for
	// reclamation by the next PopCtxReserved. Stored as int64 in
	// atomic.Int64 so SetRetryAfter is lock-free and safe to invoke
	// concurrently with pumping workers.
	retryAfterNanos atomic.Int64
}

// NewDatabaseDriver creates a new database queue driver with an injected *sql.DB.
// dbDriver specifies the database driver name ("postgres", "mysql", "sqlite").
func NewDatabaseDriver(db *sql.DB, dbDriver string) *DatabaseDriver {
	workerID := fmt.Sprintf("worker_%d_%d", time.Now().Unix(), time.Now().Nanosecond())

	driver := &DatabaseDriver{
		db:       db,
		workerID: workerID,
		dbDriver: dbDriver,
	}
	driver.retryAfterNanos.Store(int64(DefaultRetryAfter))

	return driver
}

// SetRetryAfter overrides the row-lease duration. The new value takes
// effect on the next PopCtxReserved call. Values <= 0 reset to
// DefaultRetryAfter; otherwise the supplied duration is used verbatim
// (no clamping). Primarily exists for tests that need a sub-second lease
// so a SIGKILL-equivalent can be observed quickly.
func (d *DatabaseDriver) SetRetryAfter(retryAfter time.Duration) {
	if retryAfter <= 0 {
		d.retryAfterNanos.Store(int64(DefaultRetryAfter))
		return
	}
	d.retryAfterNanos.Store(int64(retryAfter))
}

// retryAfter returns the current row-lease duration.
func (d *DatabaseDriver) retryAfter() time.Duration {
	v := d.retryAfterNanos.Load()
	if v <= 0 {
		return DefaultRetryAfter
	}
	return time.Duration(v)
}

// PushCtx adds a job to the queue.
func (d *DatabaseDriver) PushCtx(ctx context.Context, job Job, queueName ...string) error {
	return d.PushDelayedCtx(ctx, job, 0, queueName...)
}

// PushIfNotExistsCtx implements DedupeAwarePusher. It first attempts to
// claim the dedupe key in `job_dedupe`: an INSERT with a UNIQUE
// PRIMARY KEY constraint that fails (or returns RowsAffected = 0) when
// the key is already held by an in-flight job. On a successful claim,
// the job is then inserted into `jobs` exactly like PushCtx.
//
// The claim and the job INSERT live in a single transaction so a crash
// between them does not leak a dedupe key. The claim is dropped on
// commit failure via the defer rollback.
//
// Empty dedupeKey falls through to PushCtx so callers that mistakenly
// invoke this path without a real dedupe identifier do not silently
// bypass insertion.
func (d *DatabaseDriver) PushIfNotExistsCtx(ctx context.Context, job Job, dedupeKey string, queueName ...string) error {
	if dedupeKey == "" {
		return d.PushCtx(ctx, job, queueName...)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	name := resolveQueueName(job, queueName...)

	db := d.db
	if db == nil {
		return fmt.Errorf("velocity/queue: database not initialized")
	}

	// Hold d.mu across the whole claim+insert transaction so a concurrent
	// Clear cannot interleave between the jobs and job_dedupe deletes and
	// strand a dedupe-less jobs row (which would let a later same-key push
	// enqueue a duplicate and break at-most-once). Clear takes the same lock.
	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("velocity/queue: PushIfNotExistsCtx begin: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Per-driver upsert syntax. All three forms have the same
	// semantics: insert when the key is new, no-op (return 0 affected
	// rows) when the key is already present.
	var dedupeQuery string
	switch d.dbDriver {
	case "postgres":
		dedupeQuery = `INSERT INTO job_dedupe (dedupe_key, queue) VALUES ($1, $2) ON CONFLICT (dedupe_key) DO NOTHING`
	case "mysql":
		dedupeQuery = `INSERT IGNORE INTO job_dedupe (dedupe_key, queue) VALUES ($1, $2)`
	default: // sqlite + fallback
		dedupeQuery = `INSERT OR IGNORE INTO job_dedupe (dedupe_key, queue) VALUES ($1, $2)`
	}
	res, err := tx.ExecContext(ctx, d.rewriteQuery(dedupeQuery), dedupeKey, name)
	if err != nil {
		return fmt.Errorf("velocity/queue: dedupe insert: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		// Dedupe key already exists: callback is already enqueued
		// (or its row was dispatched and remains in flight). Commit
		// the no-op so the caller can move on; the reaper will
		// stop retrying once MarkCallbackDispatched runs.
		_ = tx.Commit()
		return nil
	}

	wrapper, err := createJobWrapper(job, name)
	if err != nil {
		return fmt.Errorf("velocity/queue: failed to create job wrapper: %w", err)
	}
	wrapper.DedupeKey = dedupeKey
	wrapper.Payload.TraceID, wrapper.Payload.SpanID, wrapper.Payload.ParentID = trace.GetTraceContext(ctx)
	wrapper.Payload.DedupeKey = dedupeKey

	// Encrypt-then-sign: seal Data before the wrapper is marshalled so the
	// signature below covers the ciphertext (see encryption.go).
	if err := sealPayload(wrapper.Payload); err != nil {
		return err
	}

	payload, err := marshalSigned(wrapper, func(sig string) { wrapper.Payload.Signature = sig },
		"velocity/queue: failed to serialize job",
		"velocity/queue: failed to serialize signed job")
	if err != nil {
		return err
	}

	now := time.Now().UTC()
	insertQ := d.rewriteQuery(`INSERT INTO jobs (queue, payload, attempts, scheduled_at, created_at, updated_at)
	          VALUES ($1, $2, $3, $4, $5, $6)`)
	if _, err := tx.ExecContext(ctx, insertQ, name, string(payload), 0, now, now, now); err != nil {
		return fmt.Errorf("velocity/queue: failed to insert job: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("velocity/queue: PushIfNotExistsCtx commit: %w", err)
	}

	dispatchJobQueued(d.DispatchEvent, ctx, wrapper.Payload.Type, name, false, 0)
	return nil
}

// PushDelayedCtx adds a delayed job, using ctx for the INSERT round-trip so
// callers can abort mid-enqueue on shutdown or deadline.
func (d *DatabaseDriver) PushDelayedCtx(ctx context.Context, job Job, delay time.Duration, queueName ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	name := resolveQueueName(job, queueName...)

	db := d.db
	if db == nil {
		return fmt.Errorf("velocity/queue: database not initialized")
	}

	wrapper, err := createJobWrapper(job, name)
	if err != nil {
		return fmt.Errorf("velocity/queue: failed to create job wrapper: %w", err)
	}

	wrapper.Payload.TraceID, wrapper.Payload.SpanID, wrapper.Payload.ParentID = trace.GetTraceContext(ctx)

	// Encrypt-then-sign: seal Data before the wrapper is marshalled so the
	// signature below covers the ciphertext (see encryption.go).
	if err := sealPayload(wrapper.Payload); err != nil {
		return err
	}

	payload, err := marshalSigned(wrapper, func(sig string) { wrapper.Payload.Signature = sig },
		"velocity/queue: failed to serialize job",
		"velocity/queue: failed to serialize signed job")
	if err != nil {
		return err
	}

	scheduledAt := time.Now().UTC()
	if delay > 0 {
		scheduledAt = scheduledAt.Add(delay)
	}

	now := time.Now().UTC()
	var jobID uint
	if d.dbDriver == "postgres" {
		query := d.rewriteQuery(`INSERT INTO jobs (queue, payload, attempts, scheduled_at, created_at, updated_at)
		          VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`)
		if err := db.QueryRowContext(ctx, query, name, string(payload), 0, scheduledAt, now, now).Scan(&jobID); err != nil {
			return fmt.Errorf("velocity/queue: failed to insert job: %w", err)
		}
	} else {
		query := d.rewriteQuery(`INSERT INTO jobs (queue, payload, attempts, scheduled_at, created_at, updated_at)
		          VALUES ($1, $2, $3, $4, $5, $6)`)
		res, err := db.ExecContext(ctx, query, name, string(payload), 0, scheduledAt, now, now)
		if err != nil {
			return fmt.Errorf("velocity/queue: failed to insert job: %w", err)
		}
		if id, idErr := res.LastInsertId(); idErr == nil {
			jobID = uint(id)
		}
	}
	_ = jobID

	dispatchJobQueued(d.DispatchEvent, ctx, wrapper.Payload.Type, name, delay > 0, delay)
	return nil
}

// popMode selects how popSelectLocked finalises the popped row.
type popMode int

const (
	// popModeDelete is the "retrieves and removes" path used by PopCtx
	// and PopCtxWithTrace. The row is DELETEd inside the same tx as the
	// SELECT, restoring the [Driver] contract for non-worker callers.
	popModeDelete popMode = iota
	// popModeReserve is the lease path used by PopCtxReserved. The row
	// is updated with reserved_at/reserved_by/attempts and the caller
	// receives a fencing token it must pass back to Ack / Release /
	// FailReservedCtx.
	popModeReserve
)

// PopCtx retrieves and removes the next job from the queue. This honours
// the original [Driver] contract: after PopCtx returns successfully the
// row is gone from the table and the caller owns the job outright.
//
// Deprecated: PopCtx provides no lease semantics, so a worker crash
// between pop and handler completion permanently loses the job. The
// worker pipeline uses [DatabaseDriver.PopCtxReserved] instead, which
// returns a fencing token for Ack / Release / FailReservedCtx. PopCtx is
// retained as an administrative / debug helper (e.g. drain a queue from
// a script) and to satisfy the bare [Driver] interface. Production
// callers should switch to PopCtxReserved.
func (d *DatabaseDriver) PopCtx(ctx context.Context, queueName string) (Job, error) {
	job, _, _, err := d.popSelectLocked(ctx, queueName, popModeDelete)
	return job, err
}

// PopCtxWithTrace is the trace-aware variant of PopCtx with the same
// semantics: the row is removed from the queue before this returns.
//
// Deprecated: see [DatabaseDriver.PopCtx]. Use PopCtxReserved for
// lease-safe consumption; this helper exists only for the bare
// [TraceAwareDriver] interface and ad-hoc tooling.
// Implements TraceAwareDriver.
func (d *DatabaseDriver) PopCtxWithTrace(ctx context.Context, queueName string) (Job, TraceContext, error) {
	job, _, tc, err := d.popSelectLocked(ctx, queueName, popModeDelete)
	return job, tc, err
}

// PopCtxReserved leases the next available row for the worker. It either
// selects an unreserved row whose scheduled_at <= now, or reclaims a row
// whose reserved_at is older than retryAfter (the lease window). The
// selected row is updated in place: reserved_at = now, reserved_by =
// workerID, attempts = attempts + 1. The returned token must be passed
// back to AckCtx (success), ReleaseCtx (retry), or FailReservedCtx
// (terminal failure). A zero token paired with a nil job means "no job
// available".
//
// Unrecoverable hydration failures (malformed JSON, integrity mismatch,
// unregistered job type, factory decode error) are routed through the
// shared poison-quarantine path inherited from C-01: the row is moved
// to failed_jobs, deleted from jobs, and the call returns ErrPoisonJob.
// Quarantine runs BEFORE the reservation UPDATE, so a poison row never
// ends up reserved; the worker's pop loop just re-selects and moves on.
//
// Implements [ReservationDriver].
func (d *DatabaseDriver) PopCtxReserved(ctx context.Context, queueName string) (Job, ReservationToken, TraceContext, error) {
	return d.popSelectLocked(ctx, queueName, popModeReserve)
}

// popSelectLocked is the shared pop implementation. It opens a tx,
// selects the next due (or reclaimable) row, verifies payload
// integrity, rehydrates the job, then either DELETEs the row
// (popModeDelete) or UPDATEs it into a reserved state (popModeReserve)
// before committing. Returns a zero token when mode == popModeDelete.
func (d *DatabaseDriver) popSelectLocked(ctx context.Context, queueName string, mode popMode) (Job, ReservationToken, TraceContext, error) {
	var tc TraceContext
	if err := ctx.Err(); err != nil {
		return nil, ReservationToken{}, tc, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	// Use Serializable on SQLite since it lacks FOR UPDATE SKIP LOCKED;
	// default isolation elsewhere (the row lock provides mutual exclusion).
	var txOpts *sql.TxOptions
	if d.dbDriver == "sqlite" || d.dbDriver == "sqlite3" {
		txOpts = &sql.TxOptions{Isolation: sql.LevelSerializable}
	}
	tx, err := d.db.BeginTx(ctx, txOpts)
	if err != nil {
		return nil, ReservationToken{}, tc, fmt.Errorf("velocity/queue: failed to begin transaction: %w", err)
	}
	// Rollback is a no-op if Commit already succeeded.
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	reclaimCutoff := now.Add(-d.retryAfter())

	// Reservation predicate (Laravel parity): a row is poppable if it is
	// unreserved and due, OR its lease has expired (reserved_at older
	// than retryAfter). The latter clause is what makes the queue
	// recoverable after a SIGKILL, OOM, or pod eviction; no separate
	// reaper goroutine is required. The delete-mode path uses the same
	// predicate so a stuck-reserved row can still be drained by an admin
	// PopCtx after the lease expires.
	var selectQuery string
	switch d.dbDriver {
	case "postgres", "mysql":
		selectQuery = d.rewriteQuery(`SELECT id, queue, payload, attempts, scheduled_at, reserved_at, reserved_by, failed_at, failed_reason, created_at, updated_at
			FROM jobs
			WHERE queue = $1
			AND failed_at IS NULL
			AND (
				(reserved_at IS NULL AND scheduled_at <= $2)
				OR (reserved_at IS NOT NULL AND reserved_at < $3)
			)
			ORDER BY scheduled_at ASC, id ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED`)
	default:
		// SQLite (and any unrecognised driver). The outer transaction
		// already serializes writers, so no row-level locking hint is
		// needed.
		selectQuery = d.rewriteQuery(`SELECT id, queue, payload, attempts, scheduled_at, reserved_at, reserved_by, failed_at, failed_reason, created_at, updated_at
			FROM jobs
			WHERE queue = $1
			AND failed_at IS NULL
			AND (
				(reserved_at IS NULL AND scheduled_at <= $2)
				OR (reserved_at IS NOT NULL AND reserved_at < $3)
			)
			ORDER BY scheduled_at ASC, id ASC
			LIMIT 1`)
	}

	var jobRecord JobRecord
	row := tx.QueryRowContext(ctx, selectQuery, queueName, now, reclaimCutoff)
	if err := scanJobRecord(row, &jobRecord); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ReservationToken{}, tc, nil // No jobs available
		}
		return nil, ReservationToken{}, tc, fmt.Errorf("velocity/queue: failed to fetch job: %w", err)
	}

	// Quarantine path for unrecoverable pop-time failures (inherited
	// from C-01). Runs BEFORE the reservation UPDATE so a poison row
	// never gets reserved; the next pop just selects the next row.
	var wrapper jobWrapper
	if err := json.Unmarshal([]byte(jobRecord.Payload), &wrapper); err != nil {
		j, qtc, qerr := d.quarantineAndReturn(tx, tc, jobRecord, queueName,
			fmt.Errorf("velocity/queue: failed to deserialize job: %w", err))
		return j, ReservationToken{}, qtc, qerr
	}
	if wrapper.Payload != nil {
		sig := wrapper.Payload.Signature
		wrapper.Payload.Signature = "" // Remove signature before verification
		verifyData, marshalErr := json.Marshal(wrapper)
		if marshalErr != nil {
			j, qtc, qerr := d.quarantineAndReturn(tx, tc, jobRecord, queueName,
				fmt.Errorf("velocity/queue: failed to marshal payload for verification: %w", marshalErr))
			return j, ReservationToken{}, qtc, qerr
		}
		if err := verifyPayload(verifyData, sig); err != nil {
			j, qtc, qerr := d.quarantineAndReturn(tx, tc, jobRecord, queueName,
				fmt.Errorf("velocity/queue: queue integrity check failed: %w", err))
			return j, ReservationToken{}, qtc, qerr
		}
		// Decrypt AFTER the signature check so verification never runs on
		// undecrypted attacker bytes (encrypt-then-sign; see encryption.go).
		// sig != "" means a real signature verified above, which gates the
		// legacy-plaintext transition path inside openPayload.
		if err := openPayload(wrapper.Payload, sig != ""); err != nil {
			j, qtc, qerr := d.quarantineAndReturn(tx, tc, jobRecord, queueName, err)
			return j, ReservationToken{}, qtc, qerr
		}
		tc = TraceContext{
			TraceID:  wrapper.Payload.TraceID,
			SpanID:   wrapper.Payload.SpanID,
			ParentID: wrapper.Payload.ParentID,
		}
	}

	job, err := getJobFromWrapper(&wrapper)
	if err != nil {
		j, qtc, qerr := d.quarantineAndReturn(tx, tc, jobRecord, queueName,
			fmt.Errorf("velocity/queue: failed to restore job from wrapper: %w", err))
		return j, ReservationToken{}, qtc, qerr
	}

	switch mode {
	case popModeDelete:
		// Old [Driver] contract: pop fully removes the row before
		// returning. No lease, no token. Callers that need crash-safe
		// at-least-once delivery must use PopCtxReserved instead.
		deleteQuery := d.rewriteQuery("DELETE FROM jobs WHERE id = $1")
		if _, err := tx.ExecContext(ctx, deleteQuery, jobRecord.ID); err != nil {
			return nil, ReservationToken{}, tc, fmt.Errorf("velocity/queue: failed to delete job: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, ReservationToken{}, tc, fmt.Errorf("velocity/queue: failed to commit pop transaction: %w", err)
		}
		return job, ReservationToken{}, tc, nil

	case popModeReserve:
		// Reserve the row. attempts is bumped here so the column
		// reflects the durable retry budget across process restarts,
		// matching Laravel's markJobAsReserved semantics. The
		// post-increment value is computed in Go (jobRecord.Attempts
		// was loaded inside the same tx under the row lock, so it
		// cannot have been advanced by a concurrent worker) and
		// surfaced on the ReservationToken; the worker uses it as the
		// authoritative MaxAttempts source on durable drivers, so
		// retry budgets survive worker restarts.
		persistedAttempts := jobRecord.Attempts + 1
		updateQuery := d.rewriteQuery(`UPDATE jobs
			SET reserved_at = $1, reserved_by = $2, attempts = $3, updated_at = $4
			WHERE id = $5`)
		if _, err := tx.ExecContext(ctx, updateQuery, now, d.workerID, persistedAttempts, now, jobRecord.ID); err != nil {
			return nil, ReservationToken{}, tc, fmt.Errorf("velocity/queue: failed to reserve job: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return nil, ReservationToken{}, tc, fmt.Errorf("velocity/queue: failed to commit pop transaction: %w", err)
		}
		return job, ReservationToken{
			ID:         int64(jobRecord.ID),
			Attempts:   persistedAttempts,
			ReservedBy: d.workerID,
		}, tc, nil

	default:
		return nil, ReservationToken{}, tc, fmt.Errorf("velocity/queue: unknown pop mode %d", mode)
	}
}

// AckCtx deletes the reserved row after the handler returned success.
// Fenced on (id, attempts, reserved_by): if the row's current state no
// longer matches the token, the lease was reclaimed by another worker
// (or the row has already been removed) and the method returns
// [ErrLeaseLost] without mutating any row. Safe to call with a zero
// token (no-op) for symmetry with worker code paths that may not have a
// reservation. Implements [ReservationDriver].
func (d *DatabaseDriver) AckCtx(ctx context.Context, token ReservationToken) error {
	if token.IsZero() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	query := d.rewriteQuery("DELETE FROM jobs WHERE id = $1 AND attempts = $2 AND reserved_by = $3")
	res, err := d.db.ExecContext(ctx, query, token.ID, token.Attempts, token.ReservedBy)
	if err != nil {
		return fmt.Errorf("velocity/queue: failed to ack job: %w", err)
	}
	return assertFenced(res, "ack")
}

// ReleaseCtx clears the reservation on the row and pushes scheduled_at
// forward by delay so the next pop after the delay will reclaim it as a
// retry. Used when a handler returns a retryable error. Fenced on
// (id, attempts, reserved_by); see AckCtx. Implements [ReservationDriver].
//
// NB: this updates the existing row in place; it does NOT call
// PushDelayedCtx. The persisted attempts counter therefore survives the
// retry, which is the desired Laravel semantics.
func (d *DatabaseDriver) ReleaseCtx(ctx context.Context, token ReservationToken, delay time.Duration) error {
	if token.IsZero() {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if delay < 0 {
		delay = 0
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now().UTC()
	scheduledAt := now.Add(delay)
	query := d.rewriteQuery(`UPDATE jobs
		SET reserved_at = NULL, reserved_by = NULL, scheduled_at = $1, updated_at = $2
		WHERE id = $3 AND attempts = $4 AND reserved_by = $5`)
	res, err := d.db.ExecContext(ctx, query, scheduledAt, now, token.ID, token.Attempts, token.ReservedBy)
	if err != nil {
		return fmt.Errorf("velocity/queue: failed to release job: %w", err)
	}
	return assertFenced(res, "release")
}

// FailReservedCtx records the job in failed_jobs and deletes the original
// row in a single transaction. Used when a handler exhausts its retry
// budget or opts out of retries via RetryDecider. Fenced on (id,
// attempts, reserved_by): if the delete affects zero rows, the lease
// was reclaimed by another worker. The transaction is rolled back so no
// failed_jobs row is written for a lease we do not own; the function
// returns [ErrLeaseLost]. Implements [ReservationDriver].
func (d *DatabaseDriver) FailReservedCtx(ctx context.Context, token ReservationToken, job Job, jobErr error, queueName string) error {
	if token.IsZero() {
		// No reservation to clean up; fall back to the bare Failed path
		// so a failed_jobs row is still recorded.
		return d.Failed(job, jobErr, queueName)
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	wrapper, wrapErr := createJobWrapper(job, queueName)
	if wrapErr != nil {
		return fmt.Errorf("velocity/queue: failed to create job wrapper: %w", wrapErr)
	}
	// Seal the failed row's Data too: failed_jobs retains payloads
	// indefinitely, so it must not become the plaintext copy of an
	// otherwise-encrypted queue (see encryption.go).
	if err := sealPayload(wrapper.Payload); err != nil {
		return err
	}
	payload, serErr := json.Marshal(wrapper)
	if serErr != nil {
		return fmt.Errorf("velocity/queue: failed to serialize job: %w", serErr)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("velocity/queue: failed to begin failure transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Delete first so the fence check covers both the row removal and
	// the failed_jobs write atomically. If the lease was reclaimed, the
	// delete affects zero rows, we bail with ErrLeaseLost, and the
	// rollback discards the (unwritten) failed_jobs insert.
	deleteQuery := d.rewriteQuery("DELETE FROM jobs WHERE id = $1 AND attempts = $2 AND reserved_by = $3")
	res, err := tx.ExecContext(ctx, deleteQuery, token.ID, token.Attempts, token.ReservedBy)
	if err != nil {
		return fmt.Errorf("velocity/queue: failed to delete reserved job: %w", err)
	}
	if err := assertFenced(res, "fail-reserved"); err != nil {
		return err
	}

	now := time.Now().UTC()
	insertQuery := d.rewriteQuery(
		"INSERT INTO failed_jobs (queue, payload, exception, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)",
	)
	if _, err := tx.ExecContext(ctx, insertQuery, queueName, string(payload), jobErr.Error(), now, now); err != nil {
		return fmt.Errorf("velocity/queue: failed to record failed job: %w", err)
	}

	// The dedupe row in job_dedupe (if any) is INTENTIONALLY NOT
	// released here. Holding the key past terminal failure is what
	// makes the queue-layer at-most-once contract robust against the
	// worst case described in C-03 fb4: a successful push whose
	// MarkCallbackDispatched then failed, the worker consumes and
	// runs the callback to completion, and a stale reaper tick then
	// attempts a re-push. Deleting the dedupe row here would let the
	// reaper retry insert a fresh queue row and run the handler a
	// second time. The dedupe row is reclaimed by
	// PruneStaleDedupeKeys on a long horizon (default 7 days) so the
	// sidecar table does not grow unbounded.

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("velocity/queue: failed to commit failure transaction: %w", err)
	}
	return nil
}

// assertFenced inspects a mutator result. RowsAffected == 0 means the
// fencing predicate (id + attempts + reserved_by) did not match a row,
// i.e. the lease was reclaimed by another worker (or the row was
// deleted). Returns [ErrLeaseLost] in that case. Drivers that do not
// report RowsAffected reliably fall through as "ok"; our backends
// (postgres, mysql, sqlite via go-sqlite3) all support it.
func assertFenced(res sql.Result, op string) error {
	n, err := res.RowsAffected()
	if err != nil {
		// Backend cannot report rows-affected; we cannot fence safely.
		// Surface the underlying error rather than silently succeeding.
		return fmt.Errorf("velocity/queue: %s rows-affected unavailable: %w", op, err)
	}
	if n == 0 {
		return ErrLeaseLost
	}
	return nil
}

// Size returns the number of jobs in the queue
func (d *DatabaseDriver) Size(queueName string) (int64, error) {
	var count int64
	query := d.rewriteQuery("SELECT COUNT(*) FROM jobs WHERE queue = $1 AND reserved_at IS NULL AND failed_at IS NULL")
	err := d.db.QueryRow(query, queueName).Scan(&count)

	if err != nil {
		return 0, fmt.Errorf("velocity/queue: failed to count jobs: %w", err)
	}

	return count, nil
}

// Clear removes all jobs from a queue, including the queue's dedupe
// rows. Deleting the job_dedupe rows for this queue keeps Clear aligned
// with the memory driver (which releases queue-scoped dedupe keys): a
// post-Clear PushIfNotExistsCtx with a previously seen key inserts a
// fresh row instead of silently no-op'ing against a stale sentinel.
// job_dedupe carries a queue column (see the INSERT in PushIfNotExistsCtx)
// so the delete is precisely scoped to this queue.
//
// job_dedupe is an OPTIONAL sidecar: it is not part of the base jobs
// schema and is provisioned only by apps that opt into dedupe/batch
// features (EnsureJobBatchesTable or a dedicated migration). A jobs-only
// deployment has no such table and therefore no dedupe rows to release,
// so a "table missing" error from the dedupe delete is treated as
// success; any other error propagates.
func (d *DatabaseDriver) Clear(queueName string) error {
	// Serialize against PushIfNotExistsCtx (which holds d.mu for its full
	// claim+insert transaction) so the two jobs/job_dedupe deletes below
	// cannot straddle a concurrent dedupe push and orphan its jobs row.
	d.mu.Lock()
	defer d.mu.Unlock()

	query := d.rewriteQuery("DELETE FROM jobs WHERE queue = $1")
	if _, err := d.db.Exec(query, queueName); err != nil {
		return fmt.Errorf("velocity/queue: failed to clear queue: %w", err)
	}

	dedupeQuery := d.rewriteQuery("DELETE FROM job_dedupe WHERE queue = $1")
	if _, err := d.db.Exec(dedupeQuery, queueName); err != nil && !dedupeTableMissing(err) {
		return fmt.Errorf("velocity/queue: failed to clear queue dedupe keys: %w", err)
	}

	return nil
}

// dedupeTableMissing reports whether err from the job_dedupe delete in
// Clear indicates the optional sidecar table is absent rather than a
// genuine failure. Matched on message text because the three backends
// report this differently (sqlite "no such table: job_dedupe", postgres
// SQLSTATE 42P01 "relation \"job_dedupe\" does not exist", mysql 1146
// "Table '...job_dedupe' doesn't exist") and this package does not import
// the driver libraries to assert on typed codes. The "missing" phrase
// must co-occur with the job_dedupe table name so a missing-column error
// (e.g. postgres "column \"queue\" does not exist" from an older
// mis-migrated table) is not mistaken for a missing table and swallowed.
func dedupeTableMissing(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if !strings.Contains(msg, "job_dedupe") {
		return false
	}
	return strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "doesn't exist")
}

// Failed marks a job as failed
func (d *DatabaseDriver) Failed(job Job, err error, queueName string) error {
	// Create job wrapper for serialization
	wrapper, wrapErr := createJobWrapper(job, queueName)
	if wrapErr != nil {
		return fmt.Errorf("velocity/queue: failed to create job wrapper: %w", wrapErr)
	}

	// Seal the failed row's Data too: failed_jobs retains payloads
	// indefinitely, so it must not become the plaintext copy of an
	// otherwise-encrypted queue (see encryption.go).
	if sealErr := sealPayload(wrapper.Payload); sealErr != nil {
		return sealErr
	}

	// Serialize the wrapper
	payload, serErr := json.Marshal(wrapper)
	if serErr != nil {
		return fmt.Errorf("velocity/queue: failed to serialize job: %w", serErr)
	}

	// Create failed job record
	failedJob := &FailedJobRecord{
		Queue:     queueName,
		Payload:   string(payload),
		Exception: err.Error(),
	}

	// Insert into failed_jobs table
	insertQuery := d.rewriteQuery(
		"INSERT INTO failed_jobs (queue, payload, exception, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)",
	)
	_, dbErr := d.db.Exec(
		insertQuery,
		failedJob.Queue, failedJob.Payload, failedJob.Exception, time.Now().UTC(), time.Now().UTC(),
	)
	if dbErr != nil {
		return fmt.Errorf("velocity/queue: failed to record failed job: %w", dbErr)
	}

	return nil
}

// GetDelayedJobs returns the number of delayed jobs
func (d *DatabaseDriver) GetDelayedJobs(queueName string) (int64, error) {
	var count int64
	query := d.rewriteQuery(
		"SELECT COUNT(*) FROM jobs WHERE queue = $1 AND scheduled_at > $2 AND reserved_at IS NULL AND failed_at IS NULL",
	)
	err := d.db.QueryRow(query, queueName, time.Now().UTC()).Scan(&count)

	if err != nil {
		return 0, fmt.Errorf("velocity/queue: failed to count delayed jobs: %w", err)
	}

	return count, nil
}

// ProcessDelayedJobs moves ready delayed jobs to the main queue
func (d *DatabaseDriver) ProcessDelayedJobs(queueName string) error {
	// With database driver, delayed jobs are handled by scheduled_at
	// They become available automatically when scheduled_at <= now
	// So this is a no-op for database driver
	return nil
}

// Shutdown is a no-op for the database driver; the underlying DB connection
// is owned by the ORM and closed separately.
func (d *DatabaseDriver) Shutdown(ctx context.Context) error {
	// The batch repository is process-wide (see queue/batch_repository.go).
	// Closing it here would break sibling drivers in the same process and
	// double-close panics on graceful-shutdown retries; apps install a
	// custom repo via SetDefaultBatchRepository and close it from their
	// own teardown.
	return nil
}

// popQuarantineCommitHook is a TEST-ONLY hook fired between successful
// quarantine writes and tx.Commit() inside [DatabaseDriver.quarantineAndReturn].
// When set it is invoked exactly once per quarantine path; tests use it to
// inject a caller-ctx cancel deterministically and assert the safe-rollback
// branch.
//
// Held in an atomic.Pointer so concurrent installs/resets (parallel tests
// in the same binary) cannot race with concurrent quarantine reads. The
// zero value (nil pointer) is the production behaviour: the read path
// loads, sees nil, skips the hook, and goes straight to tx.Commit().
//
// Tests install via [setPopQuarantineCommitHookForTest] which returns a
// restore func suitable for t.Cleanup. Production code never sets this.
var popQuarantineCommitHook atomic.Pointer[func()]

// setPopQuarantineCommitHookForTest installs hook as the package-level
// pop-quarantine commit hook and returns a restore func that reinstates
// whatever pointer was there before. TEST-ONLY: production code must not
// call this. The restore func is idempotent.
func setPopQuarantineCommitHookForTest(hook func()) (restore func()) {
	var newPtr *func()
	if hook != nil {
		newPtr = &hook
	}
	prev := popQuarantineCommitHook.Swap(newPtr)
	return func() { popQuarantineCommitHook.Store(prev) }
}

// quarantineAndReturn moves a poisoned row to failed_jobs, commits the tx,
// and returns the appropriate (Job, TraceContext, error) tuple for
// PopCtxWithTrace and (via a small adapter) PopCtxReserved. Centralises
// the quarantine bookkeeping so all unrecoverable pop-time failures
// (malformed JSON, integrity mismatch, unregistered job type, factory
// decode error) share one code path.
//
// On the happy path the row is gone from `jobs`, lives in `failed_jobs`
// with poisonErr.Error() in the exception column, and the returned error
// wraps both [ErrPoisonJob] (so the worker treats it as a recoverable pop
// error) and poisonErr (so operators see the specific cause).
//
// On caller-ctx cancellation between quarantine writes and Commit, the
// Commit fails, the deferred Rollback discards everything, and the
// returned error does NOT carry ErrPoisonJob: the quarantine never
// landed and a false-positive signal would leave the worker thinking the
// row was handled when it is still live in `jobs`. The next pop on a
// healthy ctx re-selects and quarantines. See
// TestC01_DatabaseDriver_PoisonRowSurvivesCallerCancellation.
func (d *DatabaseDriver) quarantineAndReturn(tx *sql.Tx, tc TraceContext, rec JobRecord, queueName string, poisonErr error) (Job, TraceContext, error) {
	if qErr := d.quarantinePoisonLocked(tx, rec.ID, rec.Payload, queueName, poisonErr); qErr != nil {
		// Quarantine statements themselves failed (DB error or the
		// caller's ctx was already cancelled and database/sql refused
		// the Exec on the tx). Surface both errors; the deferred
		// Rollback leaves the row in place to be retried.
		return nil, tc, errors.Join(poisonErr, qErr)
	}
	if hookPtr := popQuarantineCommitHook.Load(); hookPtr != nil {
		(*hookPtr)()
	}
	if commitErr := tx.Commit(); commitErr != nil {
		// Commit failed (typically: caller's ctx was cancelled between
		// the quarantine Exec calls and here, so database/sql aborts
		// the tx). The DELETE/INSERT writes are discarded by the
		// deferred Rollback. We intentionally do NOT return
		// ErrPoisonJob: the quarantine did not actually land, so
		// signalling "quarantined, move on" would be a lie. The worker
		// sees a plain pop error, backs off, and on the next pop the
		// same poison row is re-selected and quarantined correctly.
		return nil, tc, errors.Join(poisonErr, fmt.Errorf("velocity/queue: failed to commit poison-job quarantine: %w", commitErr))
	}
	return nil, tc, errors.Join(ErrPoisonJob, poisonErr)
}

// quarantinePoisonTimeout bounds how long the poison-quarantine statements
// (DELETE from jobs + INSERT into failed_jobs) may run before being aborted.
// The bound exists so a slow DB cannot hang the worker pop loop indefinitely;
// if quarantine times out the row stays in jobs and will be reselected, but
// at least the worker is not held inside Pop forever.
const quarantinePoisonTimeout = 10 * time.Second

// quarantinePoisonLocked moves a row that failed hydration from `jobs` into
// `failed_jobs` inside the supplied transaction. The caller (PopCtxWithTrace
// or PopCtxReserved) already holds the row lock for `jobID` under FOR
// UPDATE SKIP LOCKED (PG / MySQL) or BEGIN IMMEDIATE (SQLite), so no
// competing worker can race us for the same row before the tx commits.
//
// Context scoping (important): the DELETE + INSERT statements use a fresh
// background-derived context bounded by [quarantinePoisonTimeout]. This
// prevents a caller-side short per-tick deadline from cancelling either
// statement mid-execution and leaving a half-quarantined row. However, the
// ENCLOSING transaction `tx` was opened by the pop method via
// `BeginTx(callerCtx, ...)`, and database/sql keeps that ctx tied to the
// tx for the entire BEGIN / Commit window. If the caller's ctx is
// cancelled between this function returning and `tx.Commit()`, the Commit
// fails, the deferred Rollback discards everything we wrote here, and the
// poison row remains live in `jobs`. The pop call returns a
// non-ErrPoisonJob error in that branch (the join of hydrationErr and the
// Commit error) so the worker does not falsely report a quarantine that
// never landed. The next pop on a non-cancelled ctx re-selects the same
// poison row and quarantines it.
//
// True caller-cancel-resistant quarantine would require rebeginning the tx
// with a detached ctx (or splitting quarantine into a second tx). Neither is
// implemented today; the existing behaviour is documented above and exercised
// by TestC01_DatabaseDriver_PoisonRowSurvivesCallerCancellation.
//
// On success, the caller commits the transaction. On error, the caller is
// expected to roll back; the row remains in `jobs` and will be retried.
//
// Schema note: failed_jobs has columns (id, queue, payload, exception,
// created_at, updated_at). We persist the on-wire payload so an operator
// can inspect what came off the queue, and the hydration error string as the
// exception so the failure mode is self-documenting. With payload encryption
// enabled the stored blob is sealed first (sealQuarantineBlob): poison bytes
// are attacker-shaped plaintext by definition, and copying them verbatim
// into the long-lived failed_jobs table would bypass the at-rest
// confidentiality QUEUE_ENCRYPT promises.
func (d *DatabaseDriver) quarantinePoisonLocked(tx *sql.Tx, jobID uint, rawPayload, queueName string, hydrationErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), quarantinePoisonTimeout)
	defer cancel()

	deleteQuery := d.rewriteQuery("DELETE FROM jobs WHERE id = $1")
	if _, err := tx.ExecContext(ctx, deleteQuery, jobID); err != nil {
		return fmt.Errorf("velocity/queue: failed to delete poison row %d: %w", jobID, err)
	}

	storedPayload, _ := sealQuarantineBlob(rawPayload)

	now := time.Now().UTC()
	insertQuery := d.rewriteQuery(
		"INSERT INTO failed_jobs (queue, payload, exception, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)",
	)
	if _, err := tx.ExecContext(ctx, insertQuery, queueName, storedPayload, hydrationErr.Error(), now, now); err != nil {
		return fmt.Errorf("velocity/queue: failed to record poison row %d in failed_jobs: %w", jobID, err)
	}
	return nil
}

// scanJobRecord scans a database row into a JobRecord
func scanJobRecord(row *sql.Row, job *JobRecord) error {
	return row.Scan(
		&job.ID,
		&job.Queue,
		&job.Payload,
		&job.Attempts,
		&job.ScheduledAt,
		&job.ReservedAt,
		&job.ReservedBy,
		&job.FailedAt,
		&job.FailedReason,
		&job.CreatedAt,
		&job.UpdatedAt,
	)
}
