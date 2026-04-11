package console

import (
	"fmt"

	cli "github.com/velocitykode/velocity-cli"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/migrate"
)

// DBWipe drops all database tables without re-running migrations.
func DBWipe(db *orm.Manager) error {
	if db == nil {
		cli.Warning("No database configured")
		return nil
	}

	driver := db.DriverName()
	tables, err := listAllTables(db, driver)
	if err != nil {
		return fmt.Errorf("failed to list tables: %w", err)
	}

	if len(tables) == 0 {
		cli.Info("No tables to drop")
		return nil
	}

	migrator := migrate.NewMigrator(db.DB(), driver)

	for _, table := range tables {
		if err := migrator.DropTable(table); err != nil {
			return fmt.Errorf("failed to drop table %s: %w", table, err)
		}
	}

	cli.Success("All tables dropped successfully.")
	return nil
}

// listAllTables returns all user table names for the given driver.
func listAllTables(db *orm.Manager, driver string) ([]string, error) {
	var query string

	switch driver {
	case "sqlite", "sqlite3":
		query = "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
	case "postgres":
		query = "SELECT tablename FROM pg_tables WHERE schemaname = 'public'"
	case "mysql":
		query = "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE()"
	default:
		return nil, fmt.Errorf("unsupported driver: %s", driver)
	}

	rows, err := db.Raw(query)
	if err != nil {
		return nil, fmt.Errorf("failed to query tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan table name: %w", err)
		}
		tables = append(tables, name)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating table rows: %w", err)
	}

	return tables, nil
}
