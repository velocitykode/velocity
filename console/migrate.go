package console

import (
	"database/sql"
	"fmt"

	cli "github.com/velocitykode/velocity-cli"
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
		cli.Warning("No database configured (DB_CONNECTION not set), skipping migrations")
		return nil
	}

	var opt MigrateOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	migrations := migrate.All()
	if len(migrations) == 0 {
		cli.Warning("No migrations found")
		return nil
	}

	migrator := migrate.NewMigrator(db.DB(), db.DriverName())

	pending, err := getPendingMigrations(db.DB(), migrations)
	if err != nil {
		return fmt.Errorf("velocity/console: failed to get pending migrations: %w", err)
	}

	if len(pending) == 0 {
		cli.Info("Nothing to migrate")
		return nil
	}

	if opt.Pretend {
		return migratePretend(migrator, pending)
	}

	cli.Info("Running migrations...")

	if err := migrator.Up(); err != nil {
		return fmt.Errorf("velocity/console: migration failed: %w", err)
	}

	for _, m := range pending {
		cli.Success(fmt.Sprintf("%s_%s", m.Version, m.Description))
	}

	cli.Newline()
	cli.Success("Done")
	return nil
}

func migratePretend(migrator *migrate.Migrator, pending []migrate.Migration) error {
	migrator.SetPretend(true)

	for _, m := range pending {
		migrator.SetPretend(true) // reset log for each migration
		if err := m.Up(migrator); err != nil {
			return fmt.Errorf("velocity/console: pretend failed for %s: %w", m.Version, err)
		}

		cli.Info(fmt.Sprintf("%s_%s:", m.Version, m.Description))
		for _, sql := range migrator.PretendLog() {
			cli.Muted(sql)
		}
		cli.Newline()
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
		cli.Warning("No database configured (DB_CONNECTION not set), skipping migrations")
		return nil
	}

	migrations := migrate.All()
	if len(migrations) == 0 {
		cli.Warning("No migrations found")
		return nil
	}

	migrator := migrate.NewMigrator(db.DB(), db.DriverName())

	cli.Info("Dropping all tables...")

	if err := migrator.Fresh(); err != nil {
		return fmt.Errorf("velocity/console: fresh migration failed: %w", err)
	}

	cli.Info("Running migrations...")

	for _, m := range migrations {
		cli.Success(fmt.Sprintf("%s_%s", m.Version, m.Description))
	}

	cli.Newline()
	cli.Success("Done")
	return nil
}

// MigrateRollback rolls back the last batch of migrations.
//
// Like DBWipe, this is the unguarded programmatic primitive: no environment
// check or confirmation. The production gate lives in the
// `vel migrate:rollback` CLI command.
func MigrateRollback(db orm.Database, steps int) error {
	if db == nil {
		cli.Warning("No database configured (DB_CONNECTION not set), skipping rollback")
		return nil
	}

	migrations := migrate.All()
	if len(migrations) == 0 {
		cli.Warning("No migrations found")
		return nil
	}

	migrator := migrate.NewMigrator(db.DB(), db.DriverName())

	rollbackVersions, err := getRollbackMigrations(db.DB(), steps)
	if err != nil {
		return fmt.Errorf("velocity/console: failed to get rollback migrations: %w", err)
	}

	if len(rollbackVersions) == 0 {
		cli.Info("Nothing to rollback")
		return nil
	}

	cli.Info("Rolling back migrations...")

	if err := migrator.Down(steps); err != nil {
		return fmt.Errorf("velocity/console: rollback failed: %w", err)
	}

	for _, version := range rollbackVersions {
		cli.Success(version)
	}

	cli.Newline()
	cli.Success("Done")
	return nil
}

func getPendingMigrations(db *sql.DB, all []migrate.Migration) ([]migrate.Migration, error) {
	appliedVersions := make(map[string]bool)

	rows, err := db.Query("SELECT version FROM migrations")
	if err != nil {
		return all, nil
	}
	defer rows.Close()

	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			continue
		}
		appliedVersions[version] = true
	}

	var pending []migrate.Migration
	for _, m := range all {
		if !appliedVersions[m.Version] {
			pending = append(pending, m)
		}
	}

	return pending, nil
}

func getRollbackMigrations(db *sql.DB, steps int) ([]string, error) {
	rows, err := db.Query("SELECT version, batch FROM migrations ORDER BY version DESC")
	if err != nil {
		return nil, nil
	}
	defer rows.Close()

	type migrationRecord struct {
		version string
		batch   int
	}
	var records []migrationRecord
	maxBatch := 0
	for rows.Next() {
		var r migrationRecord
		if err := rows.Scan(&r.version, &r.batch); err != nil {
			continue
		}
		records = append(records, r)
		if r.batch > maxBatch {
			maxBatch = r.batch
		}
	}

	if maxBatch == 0 {
		return nil, nil
	}

	cutoff := maxBatch - steps
	var versions []string
	for _, r := range records {
		if r.batch > cutoff {
			versions = append(versions, r.version)
		}
	}

	return versions, nil
}
