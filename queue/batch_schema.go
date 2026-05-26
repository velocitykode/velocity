package queue

import (
	"context"
	"database/sql"
	"fmt"
)

// JobBatchesMigrationSQL returns the CREATE TABLE DDL for the
// `job_batches` table for the given driver. Callers may run these
// statements directly or wrap them in an orm/migrate.Migration.
//
// The schema mirrors Laravel's `job_batches` so multi-host workers can
// observe pending/completed/failed counters and the dispatcher process
// can CAS on `completed_at` to fire Then/Catch/Finally callbacks
// exactly once.
//
// Driver names accepted: "postgres", "mysql", "sqlite" (or any other
// value, which falls through to the SQLite dialect).
func JobBatchesMigrationSQL(driver string) []string {
	switch driver {
	case "postgres":
		return []string{
			`CREATE TABLE IF NOT EXISTS job_batches (
				id TEXT PRIMARY KEY,
				total_jobs INTEGER NOT NULL,
				pending_jobs INTEGER NOT NULL,
				completed_jobs INTEGER NOT NULL DEFAULT 0,
				failed_jobs INTEGER NOT NULL DEFAULT 0,
				allow_failures BOOLEAN NOT NULL DEFAULT FALSE,
				queue TEXT NOT NULL DEFAULT 'default',
				cancelled_at TIMESTAMP NULL,
				completed_at TIMESTAMP NULL,
				last_error TEXT NULL,
				created_at TIMESTAMP NOT NULL DEFAULT NOW(),
				updated_at TIMESTAMP NOT NULL DEFAULT NOW()
			)`,
			`CREATE INDEX IF NOT EXISTS idx_job_batches_completed_at ON job_batches (completed_at)`,
		}
	case "mysql":
		return []string{
			`CREATE TABLE IF NOT EXISTS job_batches (
				id VARCHAR(64) PRIMARY KEY,
				total_jobs INT NOT NULL,
				pending_jobs INT NOT NULL,
				completed_jobs INT NOT NULL DEFAULT 0,
				failed_jobs INT NOT NULL DEFAULT 0,
				allow_failures TINYINT(1) NOT NULL DEFAULT 0,
				queue VARCHAR(64) NOT NULL DEFAULT 'default',
				cancelled_at TIMESTAMP NULL,
				completed_at TIMESTAMP NULL,
				last_error TEXT NULL,
				created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
			) ENGINE=InnoDB`,
			`CREATE INDEX idx_job_batches_completed_at ON job_batches (completed_at)`,
		}
	default: // sqlite + fallback
		return []string{
			`CREATE TABLE IF NOT EXISTS job_batches (
				id TEXT PRIMARY KEY,
				total_jobs INTEGER NOT NULL,
				pending_jobs INTEGER NOT NULL,
				completed_jobs INTEGER NOT NULL DEFAULT 0,
				failed_jobs INTEGER NOT NULL DEFAULT 0,
				allow_failures INTEGER NOT NULL DEFAULT 0,
				queue TEXT NOT NULL DEFAULT 'default',
				cancelled_at DATETIME NULL,
				completed_at DATETIME NULL,
				last_error TEXT NULL,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE INDEX IF NOT EXISTS idx_job_batches_completed_at ON job_batches (completed_at)`,
		}
	}
}

// EnsureJobBatchesTable applies the CREATE TABLE DDL for the given
// driver. Idempotent (uses IF NOT EXISTS). Useful for tests and
// app-level wiring that does not use the orm/migrate package.
func EnsureJobBatchesTable(ctx context.Context, db *sql.DB, driver string) error {
	if db == nil {
		return fmt.Errorf("velocity/queue: EnsureJobBatchesTable requires a non-nil *sql.DB")
	}
	for _, stmt := range JobBatchesMigrationSQL(driver) {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("velocity/queue: job_batches schema: %w", err)
		}
	}
	return nil
}
