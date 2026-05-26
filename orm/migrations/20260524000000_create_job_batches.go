// Package migrations holds the framework's built-in schema migrations.
//
// Each migration registers itself with the global migrate registry via
// an init() block. Apps that want the framework tables provisioned
// import this package for side-effects:
//
//	import _ "github.com/velocitykode/velocity/orm/migrations"
//
// Migrations here cover framework-owned tables only (job_batches,
// outbox, etc). App-owned tables belong in the app's own migrations
// package.
package migrations

import (
	"github.com/velocitykode/velocity/orm/migrate"
	"github.com/velocitykode/velocity/queue"
)

// 20260524000000_create_job_batches provisions the `job_batches` table
// used by queue.DatabaseBatchRepository.
//
// C-03 fix: before this migration the only batch state was a process-
// local map in queue/batch.go. Workers on a separate host that popped a
// Batchable job could not find the batch, so cancel checks, progress
// counters, and Then/Catch/Finally callbacks all silently failed across
// a multi-host fleet. The job_batches table moves the counters and the
// `completed_at` CAS gate into shared storage so any worker, anywhere,
// can mutate them safely.
//
// Refs: docs/security-audit-2026-05/00-MASTER.md [C-03]
func init() {
	migrate.Register(&migrate.Migration{
		Version:     "20260524000000",
		Description: "create job_batches table for cross-process batch state",
		Up: func(m *migrate.Migrator) error {
			for _, stmt := range queue.JobBatchesMigrationSQL(m.Driver()) {
				if err := m.Raw(stmt); err != nil {
					return err
				}
			}
			return nil
		},
		Down: func(m *migrate.Migrator) error {
			return m.DropTable("job_batches")
		},
	})
}
