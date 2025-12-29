package queue

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/velocitykode/velocity/pkg/log"
	"github.com/velocitykode/velocity/pkg/orm"
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
	mu       sync.RWMutex
	workerID string
}

// NewDatabaseDriver creates a new database queue driver
func NewDatabaseDriver() Driver {
	workerID := fmt.Sprintf("worker_%d_%d", time.Now().Unix(), time.Now().Nanosecond())

	driver := &DatabaseDriver{
		workerID: workerID,
	}

	// Ensure tables exist
	driver.ensureTables()

	return driver
}

// ensureTables creates the necessary tables if they don't exist
func (d *DatabaseDriver) ensureTables() {
	// For now, we'll rely on migrations
	// In production, users should run migrations to create these tables
	log.Info("Database queue driver initialized", "worker_id", d.workerID)
}

// Push adds a job to the queue
func (d *DatabaseDriver) Push(job Job, queueName ...string) error {
	return d.PushDelayed(job, 0, queueName...)
}

// PushDelayed adds a delayed job to the queue
func (d *DatabaseDriver) PushDelayed(job Job, delay time.Duration, queueName ...string) error {
	name := "default"
	if len(queueName) > 0 && queueName[0] != "" {
		name = queueName[0]
	}

	// Check if database is available
	db := orm.DB()
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

	log.Info("Job pushed to database queue",
		"queue", name,
		"job_id", jobID,
		"scheduled_at", scheduledAt.Format(time.RFC3339),
	)

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
	dbDriver := os.Getenv("DB_CONNECTION")
	var sqlQuery string

	if dbDriver == "postgres" {
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

	row := orm.DB().QueryRow(sqlQuery, queueName, time.Now())
	err := scanJobRecord(row, &jobRecord)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil // No jobs available
		}
		return nil, fmt.Errorf("failed to fetch job: %w", err)
	}

	// Delete the job (simulating pop)
	_, err = orm.DB().Exec("DELETE FROM jobs WHERE id = $1", jobRecord.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to delete job: %w", err)
	}

	// Deserialize the job
	var wrapper JobWrapper
	if err := json.Unmarshal([]byte(jobRecord.Payload), &wrapper); err != nil {
		return nil, fmt.Errorf("failed to deserialize job: %w", err)
	}

	// Restore the job from wrapper
	job := GetJobFromWrapper(&wrapper)
	if job == nil {
		return nil, fmt.Errorf("failed to restore job from wrapper")
	}

	log.Info("Job popped from database queue",
		"queue", queueName,
		"job_id", jobRecord.ID,
		"attempts", jobRecord.Attempts,
	)

	return job, nil
}

// Size returns the number of jobs in the queue
func (d *DatabaseDriver) Size(queueName string) (int64, error) {
	var count int64
	err := orm.DB().QueryRow(
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
	_, err := orm.DB().Exec("DELETE FROM jobs WHERE queue = ?", queueName)

	if err != nil {
		return fmt.Errorf("failed to clear queue: %w", err)
	}

	log.Info("Database queue cleared", "queue", queueName)
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
	if dbErr := orm.Save(failedJob); dbErr != nil {
		return fmt.Errorf("failed to record failed job: %w", dbErr)
	}

	log.Error("Job failed and moved to failed_jobs",
		"queue", queueName,
		"error", err.Error(),
	)

	return nil
}

// GetDelayedJobs returns the number of delayed jobs
func (d *DatabaseDriver) GetDelayedJobs(queueName string) (int64, error) {
	var count int64
	err := orm.DB().QueryRow(
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
