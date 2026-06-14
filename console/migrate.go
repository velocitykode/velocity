package console

import (
	"fmt"
	"sort"

	"github.com/velocitykode/prism"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/migrate"
)

// MigrateOptions holds flags for the migrate command.
type MigrateOptions struct {
	Pretend bool
}

// Migrate runs all pending database migrations.
func Migrate(db orm.Database, opts ...MigrateOptions) error {
	if db == nil {
		prism.Warning("No database configured (DB_CONNECTION not set), skipping migrations")
		return nil
	}

	var opt MigrateOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	migrations := migrate.All()
	if len(migrations) == 0 {
		prism.Warning("No migrations found")
		return nil
	}

	migrator := migrate.NewMigrator(db.DB(), db.DriverName())

	pending, err := migrate.Pending(db.DB(), db.DriverName())
	if err != nil {
		return fmt.Errorf("velocity/console: failed to get pending migrations: %w", err)
	}

	if len(pending) == 0 {
		prism.Info("Nothing to migrate")
		return nil
	}

	if opt.Pretend {
		return migratePretend(migrator, pending)
	}

	prism.Info("Running migrations...")

	if err := migrator.Up(); err != nil {
		return fmt.Errorf("velocity/console: migration failed: %w", err)
	}

	for _, m := range pending {
		prism.Success(fmt.Sprintf("%s_%s", m.Version, m.Description))
	}

	prism.Newline()
	prism.Success("Done")
	return nil
}

func migratePretend(migrator *migrate.Migrator, pending []migrate.Migration) error {
	migrator.SetPretend(true)

	for _, m := range pending {
		migrator.SetPretend(true) // reset log for each migration
		if err := m.Up(migrator); err != nil {
			return fmt.Errorf("velocity/console: pretend failed for %s: %w", m.Version, err)
		}

		prism.Info(fmt.Sprintf("%s_%s:", m.Version, m.Description))
		for _, sql := range migrator.PretendLog() {
			prism.Muted(sql)
		}
		prism.Newline()
	}

	return nil
}

// MigrateFresh drops all tables and re-runs all migrations.
//
// Like DBWipe, this is the unguarded programmatic primitive: no environment
// check or confirmation. The production gate lives in the `vel migrate:fresh`
// CLI command.
func MigrateFresh(db orm.Database) error {
	if db == nil {
		prism.Warning("No database configured (DB_CONNECTION not set), skipping migrations")
		return nil
	}

	migrations := migrate.All()
	if len(migrations) == 0 {
		prism.Warning("No migrations found")
		return nil
	}

	migrator := migrate.NewMigrator(db.DB(), db.DriverName())

	prism.Info("Dropping all tables...")

	if err := migrator.Fresh(); err != nil {
		return fmt.Errorf("velocity/console: fresh migration failed: %w", err)
	}

	prism.Info("Running migrations...")

	statuses, err := migrator.Status()
	if err != nil {
		return fmt.Errorf("velocity/console: failed to read migration status: %w", err)
	}

	descriptions := make(map[string]string, len(migrations))
	for _, m := range migrations {
		descriptions[m.Version] = m.Description
	}

	for _, s := range statuses {
		if s.State == "Applied" {
			prism.Success(fmt.Sprintf("%s_%s", s.Version, descriptions[s.Version]))
		}
	}

	prism.Newline()
	prism.Success("Done")
	return nil
}

// MigrateRollback rolls back the last batch of migrations.
//
// Like DBWipe, this is the unguarded programmatic primitive: no environment
// check or confirmation. The production gate lives in the
// `vel migrate:rollback` CLI command.
func MigrateRollback(db orm.Database, steps int) error {
	if db == nil {
		prism.Warning("No database configured (DB_CONNECTION not set), skipping rollback")
		return nil
	}

	migrations := migrate.All()
	if len(migrations) == 0 {
		prism.Warning("No migrations found")
		return nil
	}

	migrator := migrate.NewMigrator(db.DB(), db.DriverName())

	statuses, err := migrator.Status()
	if err != nil {
		return fmt.Errorf("velocity/console: failed to get rollback migrations: %w", err)
	}

	// A non-positive step count rolls back a single batch, matching Down's
	// own normalization; keep the display selection in sync.
	if steps <= 0 {
		steps = 1
	}

	// The last batch to keep is everything at or below this cutoff; Down
	// rolls back the `steps` highest batches.
	maxBatch := 0
	for _, s := range statuses {
		if s.State == "Applied" && s.Batch > maxBatch {
			maxBatch = s.Batch
		}
	}

	cutoff := maxBatch - steps
	var rollbackVersions []string
	for _, s := range statuses {
		if s.State == "Applied" && s.Batch > cutoff {
			rollbackVersions = append(rollbackVersions, s.Version)
		}
	}

	if len(rollbackVersions) == 0 {
		prism.Info("Nothing to rollback")
		return nil
	}

	// Status returns registry order (ascending); the rollback display has
	// always been newest-first.
	sort.Slice(rollbackVersions, func(i, j int) bool {
		return rollbackVersions[i] > rollbackVersions[j]
	})

	prism.Info("Rolling back migrations...")

	if err := migrator.Down(steps); err != nil {
		return fmt.Errorf("velocity/console: rollback failed: %w", err)
	}

	for _, version := range rollbackVersions {
		prism.Success(version)
	}

	prism.Newline()
	prism.Success("Done")
	return nil
}
