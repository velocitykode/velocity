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

	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/ownctx"
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
// 90s is long enough for a typical handler to finish and short enough
// that a killed worker's rows recover promptly.
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

	// mu serves two roles, neither of which is "serialize all DB access":
	//
	//  1. Single-writer serialization of the worker paths (pop / ack /
	//     release / fail-reserved) on backends WITHOUT row-level locking
	//     (SQLite and unrecognised drivers), taken via lockWorkerPath. On
	//     postgres/mysql those paths never touch mu: FOR UPDATE SKIP LOCKED
	//     plus the (id, attempts, reserved_by) fence already give each row
	//     single-consumer semantics, so WithConcurrency(n) workers pump in
	//     parallel instead of queueing on one process-wide lock.
	//  2. Mutual exclusion between Clear and PushIfNotExistsCtx on ALL
	//     backends: Clear's jobs + job_dedupe deletes are two separate
	//     autocommit statements, so without mu a concurrent claim+insert
	//     transaction could commit between them and strand a dedupe-less
	//     jobs row, breaking at-most-once (see the comments on both
	//     methods). SKIP LOCKED does not cover this cross-statement
	//     invariant, so the lock stays regardless of dialect.
	mu       sync.Mutex
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

// hasRowLocks reports whether d's backend supports row-level pop
// isolation via FOR UPDATE SKIP LOCKED (see the SELECT in popSelect).
// On these dialects the database itself guarantees no two workers can
// select the same row, so the worker paths run without d.mu. SQLite has
// no row-level locking (single-writer database), and an unrecognised
// driver gets the conservative treatment.
func (d *DatabaseDriver) hasRowLocks() bool {
	switch d.dbDriver {
	case "postgres", "mysql":
		return true
	default:
		return false
	}
}

// lockWorkerPath serializes a worker-path call (pop / ack / release /
// fail-reserved) on single-writer backends and is a no-op on dialects
// with row-level locking (see the d.mu field comment). Returns the
// matching unlock func; callers must defer it.
func (d *DatabaseDriver) lockWorkerPath() (unlock func()) {
	if d.hasRowLocks() {
		return func() {}
	}
	d.mu.Lock()
	return d.mu.Unlock
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

	// Build, seal and sign the payload before taking d.mu: marshalling
	// runs the job's own MarshalJSON, user code that must not run under
	// the driver's lock. A job that cannot be marshalled fails here even
	// when its dedupe key is already held.
	wrapper, err := createJobWrapper(job, name)
	if err != nil {
		return errchain.Errorf("velocity/queue: failed to create job wrapper: %w", err)
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

	queued, err := d.claimAndInsert(ctx, db, dedupeKey, name, payload)
	if err != nil || !queued {
		return err
	}
	// Dispatched after d.mu is released: a job.queued listener is user
	// code and may push or clear through this driver.
	d.DispatchJobQueued(ctx, wrapper.Payload.Type, name, false, 0)
	return nil
}

// claimAndInsert claims dedupeKey and inserts the job's payload in one
// transaction under d.mu, and reports whether the job was queued (false
// when the key was already held).
func (d *DatabaseDriver) claimAndInsert(ctx context.Context, db *sql.DB, dedupeKey, name string, payload []byte) (bool, error) {
	// The statements run on a context the driver owns, read from ctx
	// before the lock: ctx's own methods are user code (see ownctx). It
	// holds their observation (the pool's statement observer and query
	// logger, user code) until the release, deferred before the unlock
	// so it runs after it.
	owned, held := ownctx.Hold(ctx)
	defer held.Release() // after the unlock: the statements' observer runs off the lock
	// Hold d.mu across the whole claim+insert transaction so a concurrent
	// Clear cannot interleave between the jobs and job_dedupe deletes and
	// strand a dedupe-less jobs row (which would let a later same-key push
	// enqueue a duplicate and break at-most-once). Clear takes the same lock.
	d.mu.Lock()
	defer d.mu.Unlock()

	tx, err := db.BeginTx(owned, nil)
	if err != nil {
		return false, errchain.Errorf("velocity/queue: PushIfNotExistsCtx begin: %w", err)
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
	res, err := tx.ExecContext(owned, d.rewriteQuery(dedupeQuery), dedupeKey, name)
	if err != nil {
		return false, errchain.Errorf("velocity/queue: dedupe insert: %w", err)
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		// Dedupe key already exists: callback is already enqueued
		// (or its row was dispatched and remains in flight). Commit
		// the no-op so the caller can move on; the reaper will
		// stop retrying once MarkCallbackDispatched runs.
		_ = tx.Commit()
		return false, nil
	}

	now := time.Now().UTC()
	insertQ := d.rewriteQuery(`INSERT INTO jobs (queue, payload, attempts, scheduled_at, created_at, updated_at)
	          VALUES ($1, $2, $3, $4, $5, $6)`)
	if _, err := tx.ExecContext(owned, insertQ, name, string(payload), 0, now, now, now); err != nil {
		return false, errchain.Errorf("velocity/queue: failed to insert job: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return false, errchain.Errorf("velocity/queue: PushIfNotExistsCtx commit: %w", err)
	}
	return true, nil
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
		return errchain.Errorf("velocity/queue: failed to create job wrapper: %w", err)
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
			return errchain.Errorf("velocity/queue: failed to insert job: %w", err)
		}
	} else {
		query := d.rewriteQuery(`INSERT INTO jobs (queue, payload, attempts, scheduled_at, created_at, updated_at)
		          VALUES ($1, $2, $3, $4, $5, $6)`)
		res, err := db.ExecContext(ctx, query, name, string(payload), 0, scheduledAt, now, now)
		if err != nil {
			return errchain.Errorf("velocity/queue: failed to insert job: %w", err)
		}
		if id, idErr := res.LastInsertId(); idErr == nil {
			jobID = uint(id)
		}
	}
	_ = jobID

	d.DispatchJobQueued(ctx, wrapper.Payload.Type, name, delay > 0, delay)
	return nil
}

// popMode selects how popSelect finalises the popped row.
type popMode int

const (
	// popModeDelete is the "retrieves and removes" path used by PopCtx
	// and PopCtxWithTrace. The row is reserved like a worker pop, then,
	// once its job is rebuilt, DELETEd in a second transaction fenced on
	// that reservation, restoring the [Driver] contract for non-worker
	// callers. A row cleared or reclaimed in between is not delivered: the
	// pop returns ErrLeaseLost.
	popModeDelete popMode = iota
	// popModeReserve is the lease path used by PopCtxReserved. The row
	// is updated with reserved_at/reserved_by/attempts and the caller
	// receives a fencing token it must pass back to Ack / Release /
	// FailReservedCtx.
	popModeReserve
)

// PopCtx retrieves and removes the next job from the queue. This honours
// the original [Driver] contract: after PopCtx returns successfully the
// row is gone from the table and the caller owns the job outright. When
// the row is cleared or reclaimed while its job is being rebuilt, PopCtx
// returns ErrLeaseLost and no job.
//
// Deprecated: PopCtx provides no lease semantics, so a worker crash
// between pop and handler completion permanently loses the job. The
// worker pipeline uses [DatabaseDriver.PopCtxReserved] instead, which
// returns a fencing token for Ack / Release / FailReservedCtx. PopCtx is
// retained as an administrative / debug helper (e.g. drain a queue from
// a script) and to satisfy the bare [Driver] interface. Production
// callers should switch to PopCtxReserved.
func (d *DatabaseDriver) PopCtx(ctx context.Context, queueName string) (Job, error) {
	job, _, _, err := d.popSelect(ctx, queueName, popModeDelete)
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
	job, _, tc, err := d.popSelect(ctx, queueName, popModeDelete)
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
// The job is rebuilt after its row is reserved and the reservation
// committed, since rebuilding runs user code; a crash between the two
// leaves a reserved row that the next pop reclaims after its lease, and
// quarantines then.
//
// Implements [ReservationDriver].
func (d *DatabaseDriver) PopCtxReserved(ctx context.Context, queueName string) (Job, ReservationToken, TraceContext, error) {
	return d.popSelect(ctx, queueName, popModeReserve)
}

// popSelect is the shared pop implementation, in three steps:
//
//  1. reserveNext leases the next due (or reclaimable) row in one short
//     transaction, under the worker-path lock on single-writer backends.
//  2. hydrateRecord rebuilds the job from the row's payload with no lock
//     and no transaction held: it runs the registered factory and the
//     job's own UnmarshalJSON, user code that may call back into this
//     driver (Clear, Size, a nested pop) or block.
//  3. A row that cannot be rebuilt is quarantined, and a delete-mode pop
//     removes its row; each in a second short transaction fenced on the
//     reservation, so a row the lease no longer covers is left alone.
//
// A crash between the steps leaves a reserved row, which the next pop
// reclaims once its lease expires. Returns a zero token when mode ==
// popModeDelete.
func (d *DatabaseDriver) popSelect(ctx context.Context, queueName string, mode popMode) (Job, ReservationToken, TraceContext, error) {
	var tc TraceContext
	if err := ctx.Err(); err != nil {
		return nil, ReservationToken{}, tc, err
	}
	rec, token, err := d.reserveNext(ctx, queueName)
	if err != nil || token.IsZero() {
		return nil, ReservationToken{}, tc, err
	}

	job, tc, poisonErr := hydrateRecord(rec)
	if poisonErr != nil {
		return nil, ReservationToken{}, tc, d.quarantineReserved(ctx, token, rec, queueName, poisonErr, errchain.Text(poisonErr))
	}

	if mode == popModeDelete {
		// Old [Driver] contract: pop fully removes the row before
		// returning. No lease, no token. Callers that need crash-safe
		// at-least-once delivery must use PopCtxReserved instead.
		if err := d.deleteReserved(ctx, token, "pop"); err != nil {
			return nil, ReservationToken{}, tc, err
		}
		return job, ReservationToken{}, tc, nil
	}
	return job, token, tc, nil
}

// reserveNext leases the next row of queueName that is due, or whose lease
// expired, and returns it with its fencing token; a zero token means no
// row is available. The reservation bumps attempts, so the column
// reflects the durable retry budget across process restarts. Serialized
// under d.mu only on single-writer backends; on postgres/mysql concurrent
// pops isolate via FOR UPDATE SKIP LOCKED (see lockWorkerPath).
func (d *DatabaseDriver) reserveNext(ctx context.Context, queueName string) (JobRecord, ReservationToken, error) {
	owned, held := ownctx.Hold(ctx)
	defer held.Release() // after the unlock: the statements' observer runs off the lock
	unlock := d.lockWorkerPath()
	defer unlock()

	// Use Serializable on SQLite since it lacks FOR UPDATE SKIP LOCKED;
	// default isolation elsewhere (the row lock provides mutual exclusion).
	var txOpts *sql.TxOptions
	if d.dbDriver == "sqlite" || d.dbDriver == "sqlite3" {
		txOpts = &sql.TxOptions{Isolation: sql.LevelSerializable}
	}
	tx, err := d.db.BeginTx(owned, txOpts)
	if err != nil {
		return JobRecord{}, ReservationToken{}, errchain.Errorf("velocity/queue: failed to begin transaction: %w", err)
	}
	// Rollback is a no-op if Commit already succeeded.
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	reclaimCutoff := now.Add(-d.retryAfter())

	// Reservation predicate: a row is poppable if it is unreserved and
	// due, OR its lease has expired (reserved_at older than
	// retryAfter). The latter clause is what makes the queue
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

	var rec JobRecord
	row := tx.QueryRowContext(owned, selectQuery, queueName, now, reclaimCutoff)
	if err := row.Scan(rec.scanDest()...); err != nil {
		// Row.Scan returns sql.ErrNoRows itself, unwrapped: comparing by
		// identity runs no method of a driver error under d.mu.
		if err == sql.ErrNoRows {
			return JobRecord{}, ReservationToken{}, nil // No jobs available
		}
		return JobRecord{}, ReservationToken{}, errchain.Errorf("velocity/queue: failed to fetch job: %w", err)
	}

	// The post-increment value is computed in Go (rec.Attempts was loaded
	// inside the same tx under the row lock, so it cannot have been
	// advanced by a concurrent worker) and surfaced on the token; the
	// worker uses it as the authoritative MaxAttempts source on durable
	// drivers, so retry budgets survive worker restarts.
	persistedAttempts := rec.Attempts + 1
	updateQuery := d.rewriteQuery(`UPDATE jobs
		SET reserved_at = $1, reserved_by = $2, attempts = $3, updated_at = $4
		WHERE id = $5`)
	if _, err := tx.ExecContext(owned, updateQuery, now, d.workerID, persistedAttempts, now, rec.ID); err != nil {
		return JobRecord{}, ReservationToken{}, errchain.Errorf("velocity/queue: failed to reserve job: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return JobRecord{}, ReservationToken{}, errchain.Errorf("velocity/queue: failed to commit pop transaction: %w", err)
	}
	return rec, ReservationToken{
		ID:         int64(rec.ID),
		Attempts:   persistedAttempts,
		ReservedBy: d.workerID,
	}, nil
}

// hydrateRecord verifies a row's payload and rebuilds its job. poisonErr
// is non-nil when the row can never run (malformed JSON, integrity
// mismatch, unregistered job type, a factory that fails or panics): the
// caller quarantines it. The factory and the job's UnmarshalJSON are user
// code, so the caller holds no lock and no transaction; the registry
// contains them (see rebuildContained), so a panic comes back as poisonErr
// and poisonErr's text runs no user code.
func hydrateRecord(rec JobRecord) (job Job, tc TraceContext, poisonErr error) {
	var wrapper jobWrapper
	if err := json.Unmarshal([]byte(rec.Payload), &wrapper); err != nil {
		return nil, tc, errchain.Errorf("velocity/queue: failed to deserialize job: %w", err)
	}
	if wrapper.Payload != nil {
		sig := wrapper.Payload.Signature
		wrapper.Payload.Signature = "" // Remove signature before verification
		verifyData, marshalErr := json.Marshal(wrapper)
		if marshalErr != nil {
			return nil, tc, errchain.Errorf("velocity/queue: failed to marshal payload for verification: %w", marshalErr)
		}
		if err := verifyPayload(verifyData, sig); err != nil {
			return nil, tc, errchain.Errorf("velocity/queue: queue integrity check failed: %w", err)
		}
		// Decrypt AFTER the signature check so verification never runs on
		// undecrypted attacker bytes (encrypt-then-sign; see encryption.go).
		// sig != "" means a real signature verified above, which gates the
		// legacy-plaintext transition path inside openPayload.
		if err := openPayload(wrapper.Payload, sig != ""); err != nil {
			return nil, tc, err
		}
		tc = TraceContext{
			TraceID:  wrapper.Payload.TraceID,
			SpanID:   wrapper.Payload.SpanID,
			ParentID: wrapper.Payload.ParentID,
		}
	}
	job, err := getJobFromWrapper(&wrapper)
	if err != nil {
		return nil, tc, errchain.Errorf("velocity/queue: failed to restore job from wrapper: %w", err)
	}
	return job, tc, nil
}

// deleteReserved removes the row token reserves, fenced on the token: a
// row whose lease was reclaimed, or that was cleared, is left alone and
// the call returns ErrLeaseLost. op names the step in errors.
func (d *DatabaseDriver) deleteReserved(ctx context.Context, token ReservationToken, op string) error {
	owned, held := ownctx.Hold(ctx)
	defer held.Release() // after the unlock: the statements' observer runs off the lock
	unlock := d.lockWorkerPath()
	defer unlock()

	query := d.rewriteQuery("DELETE FROM jobs WHERE id = $1 AND attempts = $2 AND reserved_by = $3")
	res, err := d.db.ExecContext(owned, query, token.ID, token.Attempts, token.ReservedBy)
	if err != nil {
		return errchain.Errorf("velocity/queue: failed to %s job: %w", op, err)
	}
	return assertFenced(res, op)
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
	return d.deleteReserved(ctx, token, "ack")
}

// ReleaseCtx clears the reservation on the row and pushes scheduled_at
// forward by delay so the next pop after the delay will reclaim it as a
// retry. Used when a handler returns a retryable error. Fenced on
// (id, attempts, reserved_by); see AckCtx. Implements [ReservationDriver].
//
// NB: this updates the existing row in place; it does NOT call
// PushDelayedCtx. The persisted attempts counter therefore survives the
// retry, which is the desired semantics: a released job keeps burning
// down its attempt budget rather than starting over.
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
	owned, held := ownctx.Hold(ctx)
	defer held.Release() // after the unlock: the statements' observer runs off the lock
	unlock := d.lockWorkerPath()
	defer unlock()

	now := time.Now().UTC()
	scheduledAt := now.Add(delay)
	query := d.rewriteQuery(`UPDATE jobs
		SET reserved_at = NULL, reserved_by = NULL, scheduled_at = $1, updated_at = $2
		WHERE id = $3 AND attempts = $4 AND reserved_by = $5`)
	res, err := d.db.ExecContext(owned, query, scheduledAt, now, token.ID, token.Attempts, token.ReservedBy)
	if err != nil {
		return errchain.Errorf("velocity/queue: failed to release job: %w", err)
	}
	return assertFenced(res, "release")
}

// FailReservedCtx records the job in failed_jobs and deletes the original
// row in a single transaction. Used when a handler exhausts its retry
// budget or opts out of retries via RetryDecider. Fenced on (id,
// attempts, reserved_by): if the delete affects zero rows, the lease
// was reclaimed by another worker. The transaction is rolled back so no
// failed_jobs row is written for a lease we do not own; the function
// returns [ErrLeaseLost]. Once the transaction commits it runs the job's
// Failed hook, once; on any error, a cancelled ctx included, it does not.
// Implements [ReservationDriver].
func (d *DatabaseDriver) FailReservedCtx(ctx context.Context, token ReservationToken, job Job, jobErr error, queueName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if token.IsZero() {
		// No reservation to clean up; record the failure the way FailedCtx
		// does, so a failed_jobs row is still recorded.
		return d.FailedCtx(ctx, job, jobErr, queueName)
	}

	wrapper, wrapErr := createJobWrapper(job, queueName)
	if wrapErr != nil {
		return errchain.Errorf("velocity/queue: failed to create job wrapper: %w", wrapErr)
	}
	// Seal the failed row's Data too: failed_jobs retains payloads
	// indefinitely, so it must not become the plaintext copy of an
	// otherwise-encrypted queue (see encryption.go).
	if err := sealPayload(wrapper.Payload); err != nil {
		return err
	}
	payload, serErr := json.Marshal(wrapper)
	if serErr != nil {
		return errchain.Errorf("velocity/queue: failed to serialize job: %w", serErr)
	}

	// The handler error's text is user code: read it, contained, before
	// the worker-path lock.
	if err := d.commitFailedReservation(ctx, token, errchain.Text(jobErr), queueName, payload); err != nil {
		return err
	}
	// The job's Failed hook runs once the failure is committed and outside
	// the worker-path lock, so a hook that re-enters the driver (Push,
	// Size) does not self-deadlock, and a lease-lost or failed transaction
	// (returned above) never runs it. A panic in the hook is contained and
	// returned as ErrFailedHookPanicked with the failure already recorded.
	// Mirrors MemoryDriver.FailReservedCtx.
	return RunFailedHook(ctx, job, jobErr)
}

// commitFailedReservation deletes the reserved row and inserts its
// failed_jobs row, recording exception, in one transaction under the
// worker-path lock (see FailReservedCtx for the fencing).
func (d *DatabaseDriver) commitFailedReservation(ctx context.Context, token ReservationToken, exception string, queueName string, payload []byte) error {
	owned, held := ownctx.Hold(ctx)
	defer held.Release() // after the unlock: the statements' observer runs off the lock
	unlock := d.lockWorkerPath()
	defer unlock()

	tx, err := d.db.BeginTx(owned, nil)
	if err != nil {
		return errchain.Errorf("velocity/queue: failed to begin failure transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Delete first so the fence check covers both the row removal and
	// the failed_jobs write atomically. If the lease was reclaimed, the
	// delete affects zero rows, we bail with ErrLeaseLost, and the
	// rollback discards the (unwritten) failed_jobs insert.
	deleteQuery := d.rewriteQuery("DELETE FROM jobs WHERE id = $1 AND attempts = $2 AND reserved_by = $3")
	res, err := tx.ExecContext(owned, deleteQuery, token.ID, token.Attempts, token.ReservedBy)
	if err != nil {
		return errchain.Errorf("velocity/queue: failed to delete reserved job: %w", err)
	}
	if err := assertFenced(res, "fail-reserved"); err != nil {
		return err
	}

	now := time.Now().UTC()
	insertQuery := d.rewriteQuery(
		"INSERT INTO failed_jobs (queue, payload, exception, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)",
	)
	if _, err := tx.ExecContext(owned, insertQuery, queueName, string(payload), exception, now, now); err != nil {
		return errchain.Errorf("velocity/queue: failed to record failed job: %w", err)
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
		return errchain.Errorf("velocity/queue: failed to commit failure transaction: %w", err)
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
		return errchain.Errorf("velocity/queue: %s rows-affected unavailable: %w", op, err)
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
		return 0, errchain.Errorf("velocity/queue: failed to count jobs: %w", err)
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
	// The dedupe delete's error is classified after the lock is released:
	// its text is read through errchain.Text, never under d.mu.
	query := d.rewriteQuery("DELETE FROM jobs WHERE queue = $1")
	dedupeQuery := d.rewriteQuery("DELETE FROM job_dedupe WHERE queue = $1")
	owned, held := ownctx.Hold(context.Background())
	defer held.Release() // after clearLocked's unlock: the statements' observer runs off the lock
	err, dedupeErr := d.clearLocked(owned, query, dedupeQuery, queueName)
	if err != nil {
		return errchain.Errorf("velocity/queue: failed to clear queue: %w", err)
	}
	if dedupeErr != nil && !dedupeTableMissing(dedupeErr) {
		return errchain.Errorf("velocity/queue: failed to clear queue dedupe keys: %w", dedupeErr)
	}
	return nil
}

// clearLocked runs Clear's two deletes under d.mu, the unlock deferred so
// a panic in them releases it, and returns their errors.
func (d *DatabaseDriver) clearLocked(owned context.Context, query, dedupeQuery, queueName string) (err, dedupeErr error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, err = d.db.ExecContext(owned, query, queueName); err != nil {
		return err, nil
	}
	_, dedupeErr = d.db.ExecContext(owned, dedupeQuery, queueName)
	return nil, dedupeErr
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
	msg := strings.ToLower(errchain.Text(err))
	if !strings.Contains(msg, "job_dedupe") {
		return false
	}
	return strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "does not exist") ||
		strings.Contains(msg, "doesn't exist")
}

// FailedCtx marks a job as failed: it records the job in failed_jobs,
// with the insert bound to ctx, and then runs the job's Failed hook under
// ctx, once. A failure to record, a ctx cancelled before or during the
// insert included, returns the error without running the hook.
func (d *DatabaseDriver) FailedCtx(ctx context.Context, job Job, err error, queueName string) error {
	// Create job wrapper for serialization
	wrapper, wrapErr := createJobWrapper(job, queueName)
	if wrapErr != nil {
		return errchain.Errorf("velocity/queue: failed to create job wrapper: %w", wrapErr)
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
		return errchain.Errorf("velocity/queue: failed to serialize job: %w", serErr)
	}

	// Create failed job record
	failedJob := &FailedJobRecord{
		Queue:     queueName,
		Payload:   string(payload),
		Exception: errchain.Text(err),
	}

	// Insert into failed_jobs table
	insertQuery := d.rewriteQuery(
		"INSERT INTO failed_jobs (queue, payload, exception, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)",
	)
	_, dbErr := d.db.ExecContext(
		ctx,
		insertQuery,
		failedJob.Queue, failedJob.Payload, failedJob.Exception, time.Now().UTC(), time.Now().UTC(),
	)
	if dbErr != nil {
		return errchain.Errorf("velocity/queue: failed to record failed job: %w", dbErr)
	}

	// The job's Failed hook runs once the failed_jobs row is recorded; a
	// failed insert (returned above) never runs it, and a panic in it is
	// contained (ErrFailedHookPanicked). Mirrors MemoryDriver.FailedCtx.
	return RunFailedHook(ctx, job, err)
}

// GetDelayedJobs returns the number of delayed jobs
func (d *DatabaseDriver) GetDelayedJobs(queueName string) (int64, error) {
	var count int64
	query := d.rewriteQuery(
		"SELECT COUNT(*) FROM jobs WHERE queue = $1 AND scheduled_at > $2 AND reserved_at IS NULL AND failed_at IS NULL",
	)
	err := d.db.QueryRow(query, queueName, time.Now().UTC()).Scan(&count)

	if err != nil {
		return 0, errchain.Errorf("velocity/queue: failed to count delayed jobs: %w", err)
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
// quarantine writes and tx.Commit() inside [DatabaseDriver.quarantineReserved].
// When set it is invoked exactly once per quarantine; tests use it to act
// deterministically inside the quarantine transaction (cancel the caller's
// ctx, park concurrent pops).
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

// quarantineReserved moves the reserved row of a job that can never run
// (see hydrateRecord) from jobs to failed_jobs in one transaction, fenced
// on the reservation, and returns what the pop reports: ErrPoisonJob
// joined with poisonErr once the move committed, so the worker moves on.
// exception is poisonErr's text, which may come from the job factory: the
// caller reads it before this takes the worker-path lock.
//
// The transaction runs on a context the driver owns (ownctx.HoldDetached): it
// carries the caller's correlation ids but not its cancellation, and no
// method of the caller's ctx runs under the lock. It is bounded by
// quarantinePoisonTimeout: once hydration failed, the move lands even when
// the caller's ctx ends, so a cancelled pop does not leave a poison row
// reserved until its lease expires. A lease no longer
// held returns ErrLeaseLost: the row belongs to another pop. Any other
// failure (a database error, the timeout) returns that failure. Neither
// carries ErrPoisonJob: the row is still in jobs, and the pop that
// reclaims it after its lease quarantines it.
//
// Schema note: failed_jobs has columns (id, queue, payload, exception,
// created_at, updated_at). We persist the on-wire payload so an operator
// can inspect what came off the queue, and the hydration error text as the
// exception so the failure mode is self-documenting. With payload
// encryption enabled the stored blob is sealed first (sealQuarantineBlob):
// poison bytes are attacker-shaped plaintext by definition, and copying
// them verbatim into the long-lived failed_jobs table would bypass the
// at-rest confidentiality QUEUE_ENCRYPT promises.
func (d *DatabaseDriver) quarantineReserved(ctx context.Context, token ReservationToken, rec JobRecord, queueName string, poisonErr error, exception string) error {
	owned, cancel, held := ownctx.HoldDetached(ctx, quarantinePoisonTimeout)
	defer cancel()
	defer held.Release() // after the unlock: the statements' observer runs off the lock
	storedPayload, _ := sealQuarantineBlob(rec.Payload)

	unlock := d.lockWorkerPath()
	defer unlock()

	tx, err := d.db.BeginTx(owned, nil)
	if err != nil {
		return errors.Join(poisonErr, errchain.Errorf("velocity/queue: failed to begin poison-job quarantine: %w", err))
	}
	defer func() { _ = tx.Rollback() }()

	deleteQuery := d.rewriteQuery("DELETE FROM jobs WHERE id = $1 AND attempts = $2 AND reserved_by = $3")
	res, err := tx.ExecContext(owned, deleteQuery, token.ID, token.Attempts, token.ReservedBy)
	if err != nil {
		return errors.Join(poisonErr, errchain.Errorf("velocity/queue: failed to delete poison row %d: %w", rec.ID, err))
	}
	if err := assertFenced(res, "quarantine"); err != nil {
		return errors.Join(poisonErr, err)
	}
	now := time.Now().UTC()
	insertQuery := d.rewriteQuery(
		"INSERT INTO failed_jobs (queue, payload, exception, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)",
	)
	if _, err := tx.ExecContext(owned, insertQuery, queueName, storedPayload, exception, now, now); err != nil {
		return errors.Join(poisonErr, errchain.Errorf("velocity/queue: failed to record poison row %d in failed_jobs: %w", rec.ID, err))
	}
	if hookPtr := popQuarantineCommitHook.Load(); hookPtr != nil {
		(*hookPtr)() //lock-held-ok: popQuarantineCommitHook is a test-only hook, nil outside tests
	}
	if err := tx.Commit(); err != nil {
		return errors.Join(poisonErr, errchain.Errorf("velocity/queue: failed to commit poison-job quarantine: %w", err))
	}
	return errors.Join(ErrPoisonJob, poisonErr)
}

// quarantinePoisonTimeout bounds the poison-quarantine transaction (see
// quarantineReserved). The bound exists so a slow DB cannot hang the
// worker pop loop indefinitely; if quarantine times out the row stays in
// jobs and is quarantined by the pop that reclaims it after its lease.
const quarantinePoisonTimeout = 10 * time.Second

// scanDest returns the scan destinations of a jobs row, in column order,
// for Row.Scan. The scan stays at the query's call site, where the
// checker sees the row was queried on a holding context (ownctx.Hold).
func (job *JobRecord) scanDest() []any {
	return []any{
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
	}
}
