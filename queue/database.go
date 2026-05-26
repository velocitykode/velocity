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

// rewriteQuery expands `$N`-style placeholders in a query template into the
// driver-appropriate form. All queries in this file are authored with `$N`
// placeholders; `rewriteQuery` replaces them with `?` for MySQL/SQLite while
// leaving them intact for Postgres.
func (d *DatabaseDriver) rewriteQuery(q string) string {
	if d.dbDriver == "postgres" {
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

// DatabaseDriver implements the Driver interface using database
type DatabaseDriver struct {
	mu       sync.RWMutex
	db       *sql.DB
	workerID string
	dbDriver string // "postgres", "mysql", "sqlite"
	// eventDispatcher is stored via atomic.Pointer so the dispatcher path
	// never acquires d.mu. PushDelayedCtx may grab d.mu under future
	// refactors (and PopCtxWithTrace already does); routing dispatch
	// through an atomic load keeps SetEventDispatcher lock-free and
	// deadlock-proof regardless of the caller's lock state.
	eventDispatcher atomic.Pointer[dispatcherFn]
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

	return driver
}

// SetEventDispatcher installs the event dispatcher. The assignment goes
// through atomic.Pointer and never touches d.mu, so it is safe to call from
// inside callers that already hold the queue lock.
func (d *DatabaseDriver) SetEventDispatcher(fn func(ctx context.Context, event interface{}) error) {
	if fn == nil {
		d.eventDispatcher.Store(nil)
		return
	}
	f := dispatcherFn(fn)
	d.eventDispatcher.Store(&f)
}

// dispatchEvent dispatches an event if a dispatcher is configured. The
// caller-supplied ctx is propagated so listeners observe request-scoped
// values. The dispatcher pointer is loaded atomically, so this method is
// safe to invoke from paths that already hold d.mu.
func (d *DatabaseDriver) dispatchEvent(ctx context.Context, event interface{}) {
	p := d.eventDispatcher.Load()
	if p == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	(*p)(ctx, event)
}

// PushCtx adds a job to the queue.
func (d *DatabaseDriver) PushCtx(ctx context.Context, job Job, queueName ...string) error {
	return d.PushDelayedCtx(ctx, job, 0, queueName...)
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

	wrapper, err := CreateJobWrapper(job, name)
	if err != nil {
		return fmt.Errorf("velocity/queue: failed to create job wrapper: %w", err)
	}

	wrapper.Payload.TraceID, wrapper.Payload.SpanID, wrapper.Payload.ParentID = trace.GetTraceContext(ctx)

	payload, err := json.Marshal(wrapper)
	if err != nil {
		return fmt.Errorf("velocity/queue: failed to serialize job: %w", err)
	}

	if sig := signPayload(payload); sig != "" {
		wrapper.Payload.Signature = sig
		payload, err = json.Marshal(wrapper)
		if err != nil {
			return fmt.Errorf("velocity/queue: failed to serialize signed job: %w", err)
		}
	}

	scheduledAt := time.Now()
	if delay > 0 {
		scheduledAt = scheduledAt.Add(delay)
	}

	now := time.Now()
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

	dispatchJobQueued(d.dispatchEvent, ctx, wrapper.Payload.Type, name, delay > 0, delay)
	return nil
}

// PopCtx retrieves and removes a job from the queue, using the caller's ctx
// for every transactional round-trip so worker shutdown aborts a blocking
// SELECT instead of waiting for the driver deadline.
//
// The read and delete run inside a single BEGIN/COMMIT transaction. On
// PostgreSQL/MySQL 8+ the SELECT uses FOR UPDATE SKIP LOCKED so competing
// workers never hand out the same job; SQLite falls back to a BEGIN IMMEDIATE
// transaction (it serializes writers at the BEGIN). The payload is verified
// BEFORE the DELETE so a tampered job is rejected without being removed from
// the queue — the transaction is rolled back and the job stays reserved for
// the next worker (or becomes visible again for inspection).
func (d *DatabaseDriver) PopCtx(ctx context.Context, queueName string) (Job, error) {
	job, _, err := d.PopCtxWithTrace(ctx, queueName)
	return job, err
}

// PopCtxWithTrace is the trace-aware variant of PopCtx. It returns the
// producer-side trace context recovered from the persisted payload so the
// worker can rebuild ctx for downstream events and HandleCtxer handlers.
// Implements TraceAwareDriver.
func (d *DatabaseDriver) PopCtxWithTrace(ctx context.Context, queueName string) (Job, TraceContext, error) {
	var tc TraceContext
	if err := ctx.Err(); err != nil {
		return nil, tc, err
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
		return nil, tc, fmt.Errorf("velocity/queue: failed to begin transaction: %w", err)
	}
	// Rollback is a no-op if Commit already succeeded.
	defer func() { _ = tx.Rollback() }()

	var selectQuery string
	switch d.dbDriver {
	case "postgres", "mysql":
		selectQuery = d.rewriteQuery(`SELECT id, queue, payload, attempts, scheduled_at, reserved_at, reserved_by, failed_at, failed_reason, created_at, updated_at
			FROM jobs
			WHERE queue = $1
			AND scheduled_at <= $2
			AND reserved_at IS NULL
			AND failed_at IS NULL
			ORDER BY scheduled_at ASC, id ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED`)
	default:
		// SQLite (and any unrecognised driver) — the outer transaction
		// already serializes writers, so no row-level locking hint is needed.
		selectQuery = d.rewriteQuery(`SELECT id, queue, payload, attempts, scheduled_at, reserved_at, reserved_by, failed_at, failed_reason, created_at, updated_at
			FROM jobs
			WHERE queue = $1
			AND scheduled_at <= $2
			AND reserved_at IS NULL
			AND failed_at IS NULL
			ORDER BY scheduled_at ASC, id ASC
			LIMIT 1`)
	}

	var jobRecord JobRecord
	row := tx.QueryRowContext(ctx, selectQuery, queueName, time.Now())
	if err := scanJobRecord(row, &jobRecord); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, tc, nil // No jobs available
		}
		return nil, tc, fmt.Errorf("velocity/queue: failed to fetch job: %w", err)
	}

	// Deserialize and verify the payload BEFORE delete. If verification
	// fails we return without issuing the DELETE; the deferred rollback
	// releases the row lock and leaves the job in place.
	var wrapper JobWrapper
	if err := json.Unmarshal([]byte(jobRecord.Payload), &wrapper); err != nil {
		return nil, tc, fmt.Errorf("velocity/queue: failed to deserialize job: %w", err)
	}

	if wrapper.Payload != nil {
		sig := wrapper.Payload.Signature
		wrapper.Payload.Signature = "" // Remove signature before verification
		verifyData, marshalErr := json.Marshal(wrapper)
		if marshalErr != nil {
			return nil, tc, fmt.Errorf("velocity/queue: failed to marshal payload for verification: %w", marshalErr)
		}
		if err := verifyPayload(verifyData, sig); err != nil {
			return nil, tc, fmt.Errorf("velocity/queue: queue integrity check failed: %w", err)
		}
		tc = TraceContext{
			TraceID:  wrapper.Payload.TraceID,
			SpanID:   wrapper.Payload.SpanID,
			ParentID: wrapper.Payload.ParentID,
		}
	}

	// Restore the job from the wrapper. The deserialised wrapper has
	// Job == nil (the field is `json:"-"`), so hydration always goes through
	// the registry via GetJobFromWrapper -> HydrateJob. Failure to hydrate
	// (unregistered type, factory decode error) means the row is a permanent
	// "poison": no worker in this process can ever turn it into a runnable
	// Job. Returning a plain error and rolling back would leave the row in
	// place; the worker's next SELECT (ordered by scheduled_at, id) would
	// reselect it and starve every other due job, indefinitely.
	//
	// To avoid that head-of-line starvation we quarantine the row to
	// failed_jobs (with the hydration error preserved) BEFORE returning to
	// the worker, and return [ErrPoisonJob] so the worker treats it as a
	// transient pop error and tries again. The next pop now skips the
	// (now-deleted) poison row and picks the next eligible job.
	//
	// This is the C-01 fix: the previous code path silently substituted
	// &GenericJob{} (Handle() = nil) so cross-process pops succeeded
	// vacuously and dropped every job.
	job, err := GetJobFromWrapper(&wrapper)
	if err != nil {
		hydrationErr := fmt.Errorf("velocity/queue: failed to restore job from wrapper: %w", err)
		// Quarantine inside the same tx (the row is locked under
		// FOR UPDATE SKIP LOCKED / BEGIN IMMEDIATE, so no other worker can
		// race us). The Exec calls use a fresh background-derived context
		// so a caller-side timeout that fires AFTER hydration cannot abort
		// the quarantine half-way and leave the poison row in place.
		if qErr := d.quarantinePoisonLocked(tx, jobRecord.ID, jobRecord.Payload, queueName, hydrationErr); qErr != nil {
			// Quarantine itself failed (e.g. DB error inserting into
			// failed_jobs). Surface the original hydration error joined
			// with the quarantine error so the operator sees both, and
			// let the deferred Rollback leave the row in place. The
			// poison row will be retried; that is preferable to silently
			// dropping it.
			return nil, tc, errors.Join(hydrationErr, qErr)
		}
		if commitErr := tx.Commit(); commitErr != nil {
			return nil, tc, errors.Join(hydrationErr, fmt.Errorf("velocity/queue: failed to commit poison-job quarantine: %w", commitErr))
		}
		return nil, tc, errors.Join(ErrPoisonJob, hydrationErr)
	}

	// Signature verified — safe to delete.
	deleteQuery := d.rewriteQuery("DELETE FROM jobs WHERE id = $1")
	if _, err := tx.ExecContext(ctx, deleteQuery, jobRecord.ID); err != nil {
		return nil, tc, fmt.Errorf("velocity/queue: failed to delete job: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, tc, fmt.Errorf("velocity/queue: failed to commit pop transaction: %w", err)
	}

	return job, tc, nil
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

// Clear removes all jobs from a queue
func (d *DatabaseDriver) Clear(queueName string) error {
	query := d.rewriteQuery("DELETE FROM jobs WHERE queue = $1")
	_, err := d.db.Exec(query, queueName)

	if err != nil {
		return fmt.Errorf("velocity/queue: failed to clear queue: %w", err)
	}

	return nil
}

// Failed marks a job as failed
func (d *DatabaseDriver) Failed(job Job, err error, queueName string) error {
	// Create job wrapper for serialization
	wrapper, wrapErr := CreateJobWrapper(job, queueName)
	if wrapErr != nil {
		return fmt.Errorf("velocity/queue: failed to create job wrapper: %w", wrapErr)
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
		failedJob.Queue, failedJob.Payload, failedJob.Exception, time.Now(), time.Now(),
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
	err := d.db.QueryRow(query, queueName, time.Now()).Scan(&count)

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
	batchStore.close() // stop package-level batch cleanup goroutine (idempotent)
	return nil
}

// quarantinePoisonTimeout bounds how long the poison-quarantine statements
// (DELETE from jobs + INSERT into failed_jobs) may run before being aborted.
// The bound exists so a slow DB cannot hang the worker pop loop indefinitely;
// if quarantine times out the row stays in jobs and will be reselected, but
// at least the worker is not held inside Pop forever.
const quarantinePoisonTimeout = 10 * time.Second

// quarantinePoisonLocked moves a row that failed hydration from `jobs` into
// `failed_jobs` inside the supplied transaction. The caller (PopCtxWithTrace)
// already holds the row lock for `jobID` under FOR UPDATE SKIP LOCKED (PG /
// MySQL) or BEGIN IMMEDIATE (SQLite), so no competing worker can race us for
// the same row before the tx commits.
//
// The DELETE + INSERT statements run with a fresh background-derived context
// (bounded by [quarantinePoisonTimeout]) rather than the caller's ctx. This
// is deliberate: PopCtxWithTrace is reachable from worker pop loops whose ctx
// may carry a short per-tick deadline. If hydration fails right at the edge
// of that deadline we want quarantine to still complete; leaving the poison
// row in place would head-of-line-starve every other due job (the next pop
// SELECT would reselect it). The fresh ctx is not used for `BeginTx` (the
// caller already owns the tx) but it is the right scope for the statements
// themselves.
//
// On success, the caller commits the transaction. On error, the caller is
// expected to roll back; the row remains in `jobs` and will be retried.
//
// Schema note: failed_jobs has columns (id, queue, payload, exception,
// created_at, updated_at). We persist the raw on-wire payload so an operator
// can inspect what came off the queue, and the hydration error string as the
// exception so the failure mode is self-documenting.
func (d *DatabaseDriver) quarantinePoisonLocked(tx *sql.Tx, jobID uint, rawPayload, queueName string, hydrationErr error) error {
	ctx, cancel := context.WithTimeout(context.Background(), quarantinePoisonTimeout)
	defer cancel()

	deleteQuery := d.rewriteQuery("DELETE FROM jobs WHERE id = $1")
	if _, err := tx.ExecContext(ctx, deleteQuery, jobID); err != nil {
		return fmt.Errorf("velocity/queue: failed to delete poison row %d: %w", jobID, err)
	}

	now := time.Now()
	insertQuery := d.rewriteQuery(
		"INSERT INTO failed_jobs (queue, payload, exception, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)",
	)
	if _, err := tx.ExecContext(ctx, insertQuery, queueName, rawPayload, hydrationErr.Error(), now, now); err != nil {
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
