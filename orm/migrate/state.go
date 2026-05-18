package migrate

import (
	"context"
	"database/sql"
	"fmt"
)

// createMigrationsTable creates the migrations tracking table if it doesn't exist
func (m *Migrator) createMigrationsTable() error {
	var createSQL string

	switch m.driver {
	case "sqlite":
		createSQL = `CREATE TABLE IF NOT EXISTS migrations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			version VARCHAR(255) NOT NULL UNIQUE,
			batch INTEGER NOT NULL,
			executed_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`

	case "postgres":
		createSQL = `CREATE TABLE IF NOT EXISTS migrations (
			id SERIAL PRIMARY KEY,
			version VARCHAR(255) NOT NULL UNIQUE,
			batch INTEGER NOT NULL,
			executed_at TIMESTAMP DEFAULT NOW()
		)`

	case "mysql":
		createSQL = `CREATE TABLE IF NOT EXISTS migrations (
			id INT AUTO_INCREMENT PRIMARY KEY,
			version VARCHAR(255) NOT NULL UNIQUE,
			batch INT NOT NULL,
			executed_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		)`

	default:
		return fmt.Errorf("unsupported driver: %s", m.driver)
	}

	_, err := m.execContext(context.Background(), createSQL)
	return err
}

// getAppliedMigrations returns a list of migration versions that have been applied
func (m *Migrator) getAppliedMigrations() ([]string, error) {
	// Ensure migrations table exists
	if err := m.createMigrationsTable(); err != nil {
		return nil, fmt.Errorf("failed to create migrations table: %w", err)
	}

	rows, err := m.queryContext(context.Background(), "SELECT version FROM migrations ORDER BY version ASC")
	if err != nil {
		return nil, fmt.Errorf("failed to query applied migrations: %w", err)
	}
	defer rows.Close()

	versions := make([]string, 0)
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("failed to scan migration version: %w", err)
		}
		versions = append(versions, version)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating migration rows: %w", err)
	}

	return versions, nil
}

// getAppliedMigrationsWithBatch returns migration versions with their batch numbers
func (m *Migrator) getAppliedMigrationsWithBatch() (map[string]int, error) {
	// Ensure migrations table exists
	if err := m.createMigrationsTable(); err != nil {
		return nil, fmt.Errorf("failed to create migrations table: %w", err)
	}

	rows, err := m.queryContext(context.Background(), "SELECT version, batch FROM migrations ORDER BY version ASC")
	if err != nil {
		return nil, fmt.Errorf("failed to query applied migrations: %w", err)
	}
	defer rows.Close()

	result := make(map[string]int)
	for rows.Next() {
		var version string
		var batch int
		if err := rows.Scan(&version, &batch); err != nil {
			return nil, fmt.Errorf("failed to scan migration: %w", err)
		}
		result[version] = batch
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating migration rows: %w", err)
	}

	return result, nil
}

// recordMigration records a migration execution in the migrations table
func (m *Migrator) recordMigration(version string, batch int) error {
	query := m.placeholder("INSERT INTO migrations (version, batch) VALUES (%s, %s)")
	_, err := m.execContext(context.Background(), query, version, batch)
	if err != nil {
		return fmt.Errorf("failed to record migration %s: %w", version, err)
	}
	return nil
}

// removeMigration removes a migration record from the migrations table
func (m *Migrator) removeMigration(version string) error {
	query := m.placeholder("DELETE FROM migrations WHERE version = %s")
	_, err := m.execContext(context.Background(), query, version)
	if err != nil {
		return fmt.Errorf("failed to remove migration %s: %w", version, err)
	}
	return nil
}

// getLastBatch returns the highest batch number
func (m *Migrator) getLastBatch() (int, error) {
	// Ensure migrations table exists
	if err := m.createMigrationsTable(); err != nil {
		return 0, fmt.Errorf("failed to create migrations table: %w", err)
	}

	var batch sql.NullInt64
	err := m.queryRowContext(context.Background(), "SELECT MAX(batch) FROM migrations").Scan(&batch)
	if err != nil {
		return 0, fmt.Errorf("failed to get last batch: %w", err)
	}

	if !batch.Valid {
		return 0, nil
	}

	return int(batch.Int64), nil
}

// getMigrationsByBatch returns all migration versions for a given batch
func (m *Migrator) getMigrationsByBatch(batch int) ([]string, error) {
	query := m.placeholder("SELECT version FROM migrations WHERE batch = %s ORDER BY version DESC")
	rows, err := m.queryContext(context.Background(), query, batch)
	if err != nil {
		return nil, fmt.Errorf("failed to query migrations by batch: %w", err)
	}
	defer rows.Close()

	versions := make([]string, 0)
	for rows.Next() {
		var version string
		if err := rows.Scan(&version); err != nil {
			return nil, fmt.Errorf("failed to scan migration version: %w", err)
		}
		versions = append(versions, version)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating migration rows: %w", err)
	}

	return versions, nil
}

// getAllTables returns all table names in the database
func (m *Migrator) getAllTables() ([]string, error) {
	var query string

	switch m.driver {
	case "sqlite":
		query = "SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%'"
	case "postgres":
		query = "SELECT tablename FROM pg_tables WHERE schemaname = 'public'"
	case "mysql":
		query = "SELECT table_name FROM information_schema.tables WHERE table_schema = DATABASE()"
	default:
		return nil, fmt.Errorf("unsupported driver: %s", m.driver)
	}

	rows, err := m.queryContext(context.Background(), query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tables := make([]string, 0)
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}

	return tables, rows.Err()
}

// dropTable drops a table
func (m *Migrator) dropTable(table string) error {
	quoted := quoteIdentifier(table, m.driver)
	var query string

	switch m.driver {
	case "postgres":
		query = fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", quoted)
	case "sqlite", "mysql":
		query = fmt.Sprintf("DROP TABLE IF EXISTS %s", quoted)
	default:
		return fmt.Errorf("unsupported driver: %s", m.driver)
	}

	_, err := m.execContext(context.Background(), query)
	return err
}

// placeholder generates driver-specific SQL placeholders
func (m *Migrator) placeholder(query string) string {
	if m.driver == "postgres" {
		// PostgreSQL uses $1, $2, $3...
		count := 0
		result := ""
		skipNext := false
		for _, char := range query {
			if skipNext {
				skipNext = false
				continue
			}
			if char == '%' {
				count++
				result += fmt.Sprintf("$%d", count)
				skipNext = true // Skip the 's' after '%'
				continue
			}
			result += string(char)
		}
		return result
	}
	// SQLite and MySQL use ?
	result := ""
	skipNext := false
	for _, char := range query {
		if skipNext {
			skipNext = false
			continue
		}
		if char == '%' {
			result += "?"
			skipNext = true
			continue
		}
		result += string(char)
	}
	return result
}
