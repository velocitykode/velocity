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
// then_callback / catch_callback / finally_callback columns store the
// NAME of a callback registered via RegisterBatchCallback /
// RegisterBatchFailureCallback. On terminal completion the repository
// enqueues a BatchCallbackJob carrying the name so any worker (on any
// host) can run the registered handler.
//
// then_dispatched / catch_dispatched / finally_dispatched track whether
// the corresponding callback job has been successfully PushCtx'd onto
// the queue. The terminal CAS path attempts the enqueue inline; if it
// fails (driver down, ctx canceled, queue backend partitioned) the
// dispatched flag stays false and the reaper goroutine on
// DatabaseBatchRepository re-attempts the enqueue every 15 seconds.
// This is what makes callback delivery durable across enqueue failures
// and dispatcher-process crashes that race the CAS write.
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
				then_callback TEXT NULL,
				catch_callback TEXT NULL,
				finally_callback TEXT NULL,
				then_dispatched BOOLEAN NOT NULL DEFAULT FALSE,
				catch_dispatched BOOLEAN NOT NULL DEFAULT FALSE,
				finally_dispatched BOOLEAN NOT NULL DEFAULT FALSE,
				cancelled_at TIMESTAMP NULL,
				completed_at TIMESTAMP NULL,
				last_error TEXT NULL,
				created_at TIMESTAMP NOT NULL DEFAULT NOW(),
				updated_at TIMESTAMP NOT NULL DEFAULT NOW()
			)`,
			`CREATE INDEX IF NOT EXISTS idx_job_batches_completed_at ON job_batches (completed_at)`,
			// Index supports the reaper scan: it filters on completed_at
			// being non-null AND at least one *_dispatched flag being
			// false. Postgres can use a partial index for the latter but
			// a simple composite is enough for tens of thousands of
			// rows.
			`CREATE INDEX IF NOT EXISTS idx_job_batches_callback_pending ON job_batches (completed_at, then_dispatched, catch_dispatched, finally_dispatched)`,
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
				then_callback VARCHAR(128) NULL,
				catch_callback VARCHAR(128) NULL,
				finally_callback VARCHAR(128) NULL,
				then_dispatched TINYINT(1) NOT NULL DEFAULT 0,
				catch_dispatched TINYINT(1) NOT NULL DEFAULT 0,
				finally_dispatched TINYINT(1) NOT NULL DEFAULT 0,
				cancelled_at TIMESTAMP NULL,
				completed_at TIMESTAMP NULL,
				last_error TEXT NULL,
				created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
			) ENGINE=InnoDB`,
			`CREATE INDEX idx_job_batches_completed_at ON job_batches (completed_at)`,
			`CREATE INDEX idx_job_batches_callback_pending ON job_batches (completed_at, then_dispatched, catch_dispatched, finally_dispatched)`,
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
				then_callback TEXT NULL,
				catch_callback TEXT NULL,
				finally_callback TEXT NULL,
				then_dispatched INTEGER NOT NULL DEFAULT 0,
				catch_dispatched INTEGER NOT NULL DEFAULT 0,
				finally_dispatched INTEGER NOT NULL DEFAULT 0,
				cancelled_at DATETIME NULL,
				completed_at DATETIME NULL,
				last_error TEXT NULL,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
				updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE INDEX IF NOT EXISTS idx_job_batches_completed_at ON job_batches (completed_at)`,
			`CREATE INDEX IF NOT EXISTS idx_job_batches_callback_pending ON job_batches (completed_at, then_dispatched, catch_dispatched, finally_dispatched)`,
		}
	}
}

// JobDedupeMigrationSQL returns the CREATE TABLE DDL for the
// `job_dedupe` sidecar table that backs DatabaseDriver's
// DedupeAwarePusher implementation. The table holds one row per live
// dedupe key. PushIfNotExistsCtx INSERTs into this table under a UNIQUE
// constraint (Postgres ON CONFLICT, MySQL INSERT IGNORE, SQLite INSERT
// OR IGNORE); a key collision is treated as success without inserting
// into `jobs`, so the reaper retry is idempotent at the storage layer
// even when MarkCallbackDispatched fails after a successful push.
//
// Rows are removed when the matching `jobs` row is popped (so a
// legitimate later dispatch for the same key is not blocked) and when
// PruneStaleDedupeKeys is run on a periodic schedule (defensive sweep
// for orphaned rows after a worker crash mid-pop).
func JobDedupeMigrationSQL(driver string) []string {
	switch driver {
	case "postgres":
		return []string{
			`CREATE TABLE IF NOT EXISTS job_dedupe (
				dedupe_key TEXT PRIMARY KEY,
				queue TEXT NOT NULL,
				created_at TIMESTAMP NOT NULL DEFAULT NOW()
			)`,
			`CREATE INDEX IF NOT EXISTS idx_job_dedupe_created_at ON job_dedupe (created_at)`,
		}
	case "mysql":
		return []string{
			`CREATE TABLE IF NOT EXISTS job_dedupe (
				dedupe_key VARCHAR(128) PRIMARY KEY,
				queue VARCHAR(64) NOT NULL,
				created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
			) ENGINE=InnoDB`,
			`CREATE INDEX idx_job_dedupe_created_at ON job_dedupe (created_at)`,
		}
	default: // sqlite + fallback
		return []string{
			`CREATE TABLE IF NOT EXISTS job_dedupe (
				dedupe_key TEXT PRIMARY KEY,
				queue TEXT NOT NULL,
				created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
			)`,
			`CREATE INDEX IF NOT EXISTS idx_job_dedupe_created_at ON job_dedupe (created_at)`,
		}
	}
}

// EnsureJobBatchesTable applies the CREATE TABLE DDL for the
// job_batches AND the job_dedupe sidecar tables for the given driver.
// Idempotent (uses IF NOT EXISTS). Useful for tests and app-level
// wiring that does not use the orm/migrate package.
func EnsureJobBatchesTable(ctx context.Context, db *sql.DB, driver string) error {
	if db == nil {
		return fmt.Errorf("velocity/queue: EnsureJobBatchesTable requires a non-nil *sql.DB")
	}
	stmts := JobBatchesMigrationSQL(driver)
	stmts = append(stmts, JobDedupeMigrationSQL(driver)...)
	for _, stmt := range stmts {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("velocity/queue: job_batches schema: %w", err)
		}
	}
	return nil
}
