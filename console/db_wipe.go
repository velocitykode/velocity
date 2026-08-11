package console

import (
	"fmt"

	"github.com/velocitykode/prism"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/migrate"
)

// DBWipe drops all database tables without re-running migrations.
//
// DBWipe performs no environment check, confirmation, or --force handling by
// design: it is the programmatic primitive. The production gate lives in the
// `vel db wipe` CLI command; programmatic callers are expected to apply their
// own safeguards.
func DBWipe(db orm.Database) error {
	if db == nil {
		prism.Warning("No database configured")
		return nil
	}

	migrator := migrate.NewMigrator(db.DB(), db.DriverName())

	tables, err := migrator.AllTables()
	if err != nil {
		return fmt.Errorf("velocity/console: failed to list tables: %w", err)
	}

	if len(tables) == 0 {
		prism.Info("No tables to drop")
		return nil
	}

	for _, table := range tables {
		if err := migrator.DropTable(table); err != nil {
			return fmt.Errorf("velocity/console: failed to drop table %s: %w", table, err)
		}
	}

	prism.Success("All tables dropped successfully.")
	return nil
}
