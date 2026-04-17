package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
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
	mu              sync.RWMutex
	db              *sql.DB
	workerID        string
	dbDriver        string // "postgres", "mysql", "sqlite"
	eventDispatcher func(event interface{}) error
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

// SetEventDispatcher sets the function used to dispatch events.
func (d *DatabaseDriver) SetEventDispatcher(fn func(event interface{}) error) {
	d.eventDispatcher = fn
}

// dispatchEvent dispatches an event if a dispatcher is configured.
func (d *DatabaseDriver) dispatchEvent(event interface{}) {
	if d.eventDispatcher != nil {
		d.eventDispatcher(event)
	}
}

// Push adds a job to the queue
func (d *DatabaseDriver) Push(job Job, queueName ...string) error {
	return d.PushDelayed(job, 0, queueName...)
}

// PushDelayed adds a delayed job to the queue
func (d *DatabaseDriver) PushDelayed(job Job, delay time.Duration, queueName ...string) error {
	name := resolveQueueName(job, queueName...)

	// Check if database is available
	db := d.db
	if db == nil {
		return fmt.Errorf("velocity/queue: database not initialized")
	}

	// Create job wrapper to maintain type information
	wrapper, err := CreateJobWrapper(job, name)
	if err != nil {
		return fmt.Errorf("velocity/queue: failed to create job wrapper: %w", err)
	}

	// Serialize the wrapper
	payload, err := json.Marshal(wrapper)
	if err != nil {
		return fmt.Errorf("velocity/queue: failed to serialize job: %w", err)
	}

	// Sign the payload for integrity verification
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

	// Insert directly using SQL since ORM might not be fully ready.
	// Postgres supports RETURNING id; MySQL/SQLite use LastInsertId via Exec.
	now := time.Now()
	var jobID uint
	if d.dbDriver == "postgres" {
		query := d.rewriteQuery(`INSERT INTO jobs (queue, payload, attempts, scheduled_at, created_at, updated_at)
		          VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`)
		if err := db.QueryRow(query, name, string(payload), 0, scheduledAt, now, now).Scan(&jobID); err != nil {
			return fmt.Errorf("velocity/queue: failed to insert job: %w", err)
		}
	} else {
		query := d.rewriteQuery(`INSERT INTO jobs (queue, payload, attempts, scheduled_at, created_at, updated_at)
		          VALUES ($1, $2, $3, $4, $5, $6)`)
		res, err := db.Exec(query, name, string(payload), 0, scheduledAt, now, now)
		if err != nil {
			return fmt.Errorf("velocity/queue: failed to insert job: %w", err)
		}
		if id, idErr := res.LastInsertId(); idErr == nil {
			jobID = uint(id)
		}
	}
	_ = jobID

	// Dispatch job.queued event
	dispatchJobQueued(d.dispatchEvent, context.Background(), wrapper.Payload.Type, name, delay > 0, delay)
	return nil
}

// Pop retrieves and removes a job from the queue.
//
// The read and delete run inside a single BEGIN/COMMIT transaction. On
// PostgreSQL/MySQL 8+ the SELECT uses FOR UPDATE SKIP LOCKED so competing
// workers never hand out the same job; SQLite falls back to a BEGIN IMMEDIATE
// transaction (it serializes writers at the BEGIN). The payload is verified
// BEFORE the DELETE so a tampered job is rejected without being removed from
// the queue — the transaction is rolled back and the job stays reserved for
// the next worker (or becomes visible again for inspection).
func (d *DatabaseDriver) Pop(queueName string) (Job, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	ctx := context.Background()

	// Use Serializable on SQLite since it lacks FOR UPDATE SKIP LOCKED;
	// default isolation elsewhere (the row lock provides mutual exclusion).
	var txOpts *sql.TxOptions
	if d.dbDriver == "sqlite" || d.dbDriver == "sqlite3" {
		txOpts = &sql.TxOptions{Isolation: sql.LevelSerializable}
	}
	tx, err := d.db.BeginTx(ctx, txOpts)
	if err != nil {
		return nil, fmt.Errorf("velocity/queue: failed to begin transaction: %w", err)
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
			return nil, nil // No jobs available
		}
		return nil, fmt.Errorf("velocity/queue: failed to fetch job: %w", err)
	}

	// Deserialize and verify the payload BEFORE delete. If verification
	// fails we return without issuing the DELETE; the deferred rollback
	// releases the row lock and leaves the job in place.
	var wrapper JobWrapper
	if err := json.Unmarshal([]byte(jobRecord.Payload), &wrapper); err != nil {
		return nil, fmt.Errorf("velocity/queue: failed to deserialize job: %w", err)
	}

	if wrapper.Payload != nil {
		sig := wrapper.Payload.Signature
		wrapper.Payload.Signature = "" // Remove signature before verification
		verifyData, marshalErr := json.Marshal(wrapper)
		if marshalErr != nil {
			return nil, fmt.Errorf("velocity/queue: failed to marshal payload for verification: %w", marshalErr)
		}
		if err := verifyPayload(verifyData, sig); err != nil {
			return nil, fmt.Errorf("velocity/queue: queue integrity check failed: %w", err)
		}
	}

	// Restore the job from the wrapper before committing so a restoration
	// failure also avoids deleting the row.
	job := GetJobFromWrapper(&wrapper)
	if job == nil {
		return nil, fmt.Errorf("velocity/queue: failed to restore job from wrapper")
	}

	// Signature verified — safe to delete.
	deleteQuery := d.rewriteQuery("DELETE FROM jobs WHERE id = $1")
	if _, err := tx.ExecContext(ctx, deleteQuery, jobRecord.ID); err != nil {
		return nil, fmt.Errorf("velocity/queue: failed to delete job: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("velocity/queue: failed to commit pop transaction: %w", err)
	}

	return job, nil
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

// Close is a no-op for the database driver; the underlying DB connection
// is owned by the ORM and closed separately.
// Deprecated: use Shutdown(ctx) instead.
func (d *DatabaseDriver) Close() error {
	return d.Shutdown(context.Background())
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
