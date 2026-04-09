package console

import (
	"database/sql"
	"fmt"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/migrate"
)

// Migrate runs all pending database migrations.
func Migrate(db *orm.Manager) error {
	if db == nil {
		fmt.Println("No database configured (DB_CONNECTION not set), skipping migrations")
		return nil
	}

	migrations := migrate.All()
	if len(migrations) == 0 {
		fmt.Println("No migrations found")
		return nil
	}

	migrator := migrate.NewMigrator(db.DB(), db.DriverName())

	pending, err := getPendingMigrations(db.DB(), migrations)
	if err != nil {
		return fmt.Errorf("failed to get pending migrations: %w", err)
	}

	if len(pending) == 0 {
		fmt.Println("Nothing to migrate")
		return nil
	}

	fmt.Println("Running migrations...")

	if err := migrator.Up(); err != nil {
		return fmt.Errorf("migration failed: %w", err)
	}

	for _, m := range pending {
		fmt.Printf("  ✓ %s_%s\n", m.Version, m.Description)
	}

	fmt.Println("\nDone")
	return nil
}

// MigrateFresh drops all tables and re-runs all migrations.
func MigrateFresh(db *orm.Manager) error {
	if db == nil {
		fmt.Println("No database configured (DB_CONNECTION not set), skipping migrations")
		return nil
	}

	migrations := migrate.All()
	if len(migrations) == 0 {
		fmt.Println("No migrations found")
		return nil
	}

	migrator := migrate.NewMigrator(db.DB(), db.DriverName())

	fmt.Println("Dropping all tables...")

	if err := migrator.Fresh(); err != nil {
		return fmt.Errorf("fresh migration failed: %w", err)
	}

	fmt.Println("Running migrations...")

	for _, m := range migrations {
		fmt.Printf("  ✓ %s_%s\n", m.Version, m.Description)
	}

	fmt.Println("\nDone")
	return nil
}

// MigrateRollback rolls back the last batch of migrations.
func MigrateRollback(db *orm.Manager, steps int) error {
	if db == nil {
		fmt.Println("No database configured (DB_CONNECTION not set), skipping rollback")
		return nil
	}

	migrations := migrate.All()
	if len(migrations) == 0 {
		fmt.Println("No migrations found")
		return nil
	}

	migrator := migrate.NewMigrator(db.DB(), db.DriverName())

	rollbackVersions, err := getRollbackMigrations(db.DB(), steps)
	if err != nil {
		return fmt.Errorf("failed to get rollback migrations: %w", err)
	}

	if len(rollbackVersions) == 0 {
		fmt.Println("Nothing to rollback")
		return nil
	}

	fmt.Println("Rolling back migrations...")

	if err := migrator.Down(steps); err != nil {
		return fmt.Errorf("rollback failed: %w", err)
	}

	for _, version := range rollbackVersions {
		fmt.Printf("  ✓ %s\n", version)
	}

	fmt.Println("\nDone")
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
