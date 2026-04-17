package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

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
		return fmt.Errorf("database not initialized")
	}

	// Create job wrapper to maintain type information
	wrapper, err := CreateJobWrapper(job, name)
	if err != nil {
		return fmt.Errorf("failed to create job wrapper: %w", err)
	}

	// Serialize the wrapper
	payload, err := json.Marshal(wrapper)
	if err != nil {
		return fmt.Errorf("failed to serialize job: %w", err)
	}

	// Sign the payload for integrity verification
	if sig := signPayload(payload); sig != "" {
		wrapper.Payload.Signature = sig
		payload, err = json.Marshal(wrapper)
		if err != nil {
			return fmt.Errorf("failed to serialize signed job: %w", err)
		}
	}

	scheduledAt := time.Now()
	if delay > 0 {
		scheduledAt = scheduledAt.Add(delay)
	}

	// Insert directly using SQL since ORM might not be fully ready
	query := `INSERT INTO jobs (queue, payload, attempts, scheduled_at, created_at, updated_at)
	          VALUES ($1, $2, $3, $4, $5, $6) RETURNING id`

	var jobID uint
	err = db.QueryRow(query, name, string(payload), 0, scheduledAt, time.Now(), time.Now()).Scan(&jobID)
	if err != nil {
		return fmt.Errorf("failed to insert job: %w", err)
	}

	// Dispatch job.queued event
	dispatchJobQueued(d.dispatchEvent, context.Background(), wrapper.Payload.Type, name, delay > 0, delay)
	return nil
}

// Pop retrieves and removes a job from the queue
func (d *DatabaseDriver) Pop(queueName string) (Job, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// For now, use raw SQL since ORM doesn't have static query builder yet
	// This should be updated when ORM gets static query methods
	var jobRecord JobRecord

	// Check which driver we're using
	var sqlQuery string

	if d.dbDriver == "postgres" {
		// PostgreSQL with proper locking
		sqlQuery = `SELECT * FROM jobs
			WHERE queue = $1
			AND scheduled_at <= $2
			AND reserved_at IS NULL
			AND failed_at IS NULL
			ORDER BY scheduled_at ASC, id ASC
			LIMIT 1
			FOR UPDATE SKIP LOCKED`
	} else {
		// SQLite fallback (no locking)
		sqlQuery = `SELECT * FROM jobs
			WHERE queue = ?
			AND scheduled_at <= ?
			AND reserved_at IS NULL
			AND failed_at IS NULL
			ORDER BY scheduled_at ASC, id ASC
			LIMIT 1`
	}

	row := d.db.QueryRow(sqlQuery, queueName, time.Now())
	err := scanJobRecord(row, &jobRecord)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // No jobs available
		}
		return nil, fmt.Errorf("failed to fetch job: %w", err)
	}

	// Delete the job (simulating pop)
	_, err = d.db.Exec("DELETE FROM jobs WHERE id = $1", jobRecord.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to delete job: %w", err)
	}

	// Deserialize the job
	var wrapper JobWrapper
	if err := json.Unmarshal([]byte(jobRecord.Payload), &wrapper); err != nil {
		return nil, fmt.Errorf("failed to deserialize job: %w", err)
	}

	// Verify payload integrity if signing is enabled
	if wrapper.Payload != nil {
		sig := wrapper.Payload.Signature
		wrapper.Payload.Signature = "" // Remove signature before verification
		verifyData, _ := json.Marshal(wrapper)
		if err := verifyPayload(verifyData, sig); err != nil {
			return nil, fmt.Errorf("queue integrity check failed: %w", err)
		}
	}

	// Restore the job from wrapper
	job := GetJobFromWrapper(&wrapper)
	if job == nil {
		return nil, fmt.Errorf("failed to restore job from wrapper")
	}

	return job, nil
}

// Size returns the number of jobs in the queue
func (d *DatabaseDriver) Size(queueName string) (int64, error) {
	var count int64
	err := d.db.QueryRow(
		"SELECT COUNT(*) FROM jobs WHERE queue = ? AND reserved_at IS NULL AND failed_at IS NULL",
		queueName,
	).Scan(&count)

	if err != nil {
		return 0, fmt.Errorf("failed to count jobs: %w", err)
	}

	return count, nil
}

// Clear removes all jobs from a queue
func (d *DatabaseDriver) Clear(queueName string) error {
	_, err := d.db.Exec("DELETE FROM jobs WHERE queue = ?", queueName)

	if err != nil {
		return fmt.Errorf("failed to clear queue: %w", err)
	}

	return nil
}

// Failed marks a job as failed
func (d *DatabaseDriver) Failed(job Job, err error, queueName string) error {
	// Create job wrapper for serialization
	wrapper, wrapErr := CreateJobWrapper(job, queueName)
	if wrapErr != nil {
		return fmt.Errorf("failed to create job wrapper: %w", wrapErr)
	}

	// Serialize the wrapper
	payload, serErr := json.Marshal(wrapper)
	if serErr != nil {
		return fmt.Errorf("failed to serialize job: %w", serErr)
	}

	// Create failed job record
	failedJob := &FailedJobRecord{
		Queue:     queueName,
		Payload:   string(payload),
		Exception: err.Error(),
	}

	// Insert into failed_jobs table
	_, dbErr := d.db.Exec(
		"INSERT INTO failed_jobs (queue, payload, exception, created_at, updated_at) VALUES ($1, $2, $3, $4, $5)",
		failedJob.Queue, failedJob.Payload, failedJob.Exception, time.Now(), time.Now(),
	)
	if dbErr != nil {
		return fmt.Errorf("failed to record failed job: %w", dbErr)
	}

	return nil
}

// GetDelayedJobs returns the number of delayed jobs
func (d *DatabaseDriver) GetDelayedJobs(queueName string) (int64, error) {
	var count int64
	err := d.db.QueryRow(
		"SELECT COUNT(*) FROM jobs WHERE queue = ? AND scheduled_at > ? AND reserved_at IS NULL AND failed_at IS NULL",
		queueName, time.Now(),
	).Scan(&count)

	if err != nil {
		return 0, fmt.Errorf("failed to count delayed jobs: %w", err)
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
