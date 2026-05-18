package drivers

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestNewSQLiteDriver(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"creates new sqlite driver instance"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewSQLiteDriver()
			if driver == nil {
				t.Error("NewSQLiteDriver() returned nil")
			}
		})
	}
}

func TestSQLiteDriver_DriverName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"returns sqlite as driver name", "sqlite"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewSQLiteDriver()
			got := driver.DriverName()
			if got != tt.want {
				t.Errorf("DriverName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSQLiteDriver_Grammar(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"returns SQLiteGrammar instance"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewSQLiteDriver()
			grammar := driver.Grammar()
			if grammar == nil {
				t.Error("Grammar() returned nil")
			}
			if _, ok := grammar.(*SQLiteGrammar); !ok {
				t.Errorf("Grammar() returned %T, want *SQLiteGrammar", grammar)
			}
		})
	}
}

func TestSQLiteDriver_DB_ReturnsNilBeforeConnect(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"returns nil before connection is established"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewSQLiteDriver()
			if driver.DB() != nil {
				t.Error("DB() should return nil before Connect() is called")
			}
		})
	}
}

func TestSQLiteDriver_Ping_ReturnsErrorBeforeConnect(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"returns error when not connected", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewSQLiteDriver()
			err := driver.Ping()
			if (err != nil) != tt.wantErr {
				t.Errorf("Ping() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && err.Error() != "velocity/orm: no database connection" {
				t.Errorf("Ping() error = %q, want %q", err.Error(), "velocity/orm: no database connection")
			}
		})
	}
}

func TestSQLiteDriver_Close_NoErrorWhenNotConnected(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"returns nil when db is not connected", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewSQLiteDriver()
			err := driver.Close()
			if (err != nil) != tt.wantErr {
				t.Errorf("Close() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSQLiteDriver_Connect_InMemory(t *testing.T) {
	tests := []struct {
		name    string
		config  ConnectionConfig
		wantErr bool
	}{
		{
			name: "connects to in-memory database",
			config: ConnectionConfig{
				Database:     ":memory:",
				MaxIdleConns: 1, // Required for in-memory databases to persist across queries
			},
			wantErr: false,
		},
		{
			name: "connects to in-memory database with empty config",
			config: ConnectionConfig{
				MaxIdleConns: 1, // Required for in-memory databases to persist across queries
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewSQLiteDriver()
			err := driver.Connect(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("Connect() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil {
				defer driver.Close()
				if driver.DB() == nil {
					t.Error("DB() should not return nil after successful Connect()")
				}
			}
		})
	}
}

func TestSQLiteDriver_Ping_AfterConnect(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"pings successfully after connection", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewSQLiteDriver()
			if err := driver.Connect(ConnectionConfig{Database: ":memory:", MaxIdleConns: 1}); err != nil {
				t.Fatalf("Connect() error = %v", err)
			}
			defer driver.Close()

			err := driver.Ping()
			if (err != nil) != tt.wantErr {
				t.Errorf("Ping() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSQLiteDriver_CreateTable(t *testing.T) {
	tests := []struct {
		name      string
		tableName string
		columns   []Column
		wantErr   bool
	}{
		{
			name:      "creates table with basic columns",
			tableName: "test_users",
			columns: []Column{
				{Name: "id", Type: "INTEGER", Primary: true, AutoIncrement: true},
				{Name: "name", Type: "TEXT", Nullable: false},
			},
			wantErr: false,
		},
		{
			name:      "creates table with all column types",
			tableName: "test_all_types",
			columns: []Column{
				{Name: "id", Type: "INTEGER", Primary: true},
				{Name: "text_col", Type: "TEXT"},
				{Name: "int_col", Type: "INT"},
				{Name: "real_col", Type: "REAL"},
				{Name: "blob_col", Type: "BLOB"},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewSQLiteDriver()
			if err := driver.Connect(ConnectionConfig{Database: ":memory:", MaxIdleConns: 1}); err != nil {
				t.Fatalf("Connect() error = %v", err)
			}
			defer driver.Close()

			err := driver.CreateTable(tt.tableName, func(table *Table) {
				table.Columns = tt.columns
			})

			if (err != nil) != tt.wantErr {
				t.Errorf("CreateTable() error = %v, wantErr %v", err, tt.wantErr)
			}

			if err == nil {
				if !driver.HasTable(tt.tableName) {
					t.Errorf("HasTable() = false after CreateTable(), want true")
				}
			}
		})
	}
}

func TestSQLiteDriver_DropTable(t *testing.T) {
	tests := []struct {
		name      string
		tableName string
		wantErr   bool
	}{
		{
			name:      "drops existing table",
			tableName: "test_drop_table",
			wantErr:   false,
		},
		{
			name:      "drops non-existing table without error",
			tableName: "non_existing_table",
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewSQLiteDriver()
			if err := driver.Connect(ConnectionConfig{Database: ":memory:", MaxIdleConns: 1}); err != nil {
				t.Fatalf("Connect() error = %v", err)
			}
			defer driver.Close()

			// Create table first if testing drop of existing table
			if tt.tableName == "test_drop_table" {
				driver.CreateTable(tt.tableName, func(table *Table) {
					table.Columns = []Column{
						{Name: "id", Type: "INTEGER", Primary: true},
					}
				})
			}

			err := driver.DropTable(tt.tableName)
			if (err != nil) != tt.wantErr {
				t.Errorf("DropTable() error = %v, wantErr %v", err, tt.wantErr)
			}

			if err == nil && driver.HasTable(tt.tableName) {
				t.Error("HasTable() = true after DropTable(), want false")
			}
		})
	}
}

func TestSQLiteDriver_HasTable(t *testing.T) {
	tests := []struct {
		name       string
		tableName  string
		createIt   bool
		wantExists bool
	}{
		{
			name:       "returns true for existing table",
			tableName:  "existing_table",
			createIt:   true,
			wantExists: true,
		},
		{
			name:       "returns false for non-existing table",
			tableName:  "non_existing_table",
			createIt:   false,
			wantExists: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewSQLiteDriver()
			if err := driver.Connect(ConnectionConfig{Database: ":memory:", MaxIdleConns: 1}); err != nil {
				t.Fatalf("Connect() error = %v", err)
			}
			defer driver.Close()

			if tt.createIt {
				driver.CreateTable(tt.tableName, func(table *Table) {
					table.Columns = []Column{
						{Name: "id", Type: "INTEGER", Primary: true},
					}
				})
			}

			got := driver.HasTable(tt.tableName)
			if got != tt.wantExists {
				t.Errorf("HasTable() = %v, want %v", got, tt.wantExists)
			}
		})
	}
}

func TestSQLiteDriver_HasColumn(t *testing.T) {
	tests := []struct {
		name       string
		tableName  string
		columnName string
		columns    []Column
		wantExists bool
	}{
		{
			name:       "returns true for existing column",
			tableName:  "test_table",
			columnName: "email",
			columns: []Column{
				{Name: "id", Type: "INTEGER", Primary: true},
				{Name: "email", Type: "TEXT"},
			},
			wantExists: true,
		},
		{
			name:       "returns false for non-existing column",
			tableName:  "test_table",
			columnName: "phone",
			columns: []Column{
				{Name: "id", Type: "INTEGER", Primary: true},
				{Name: "email", Type: "TEXT"},
			},
			wantExists: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewSQLiteDriver()
			if err := driver.Connect(ConnectionConfig{Database: ":memory:", MaxIdleConns: 1}); err != nil {
				t.Fatalf("Connect() error = %v", err)
			}
			defer driver.Close()

			driver.CreateTable(tt.tableName, func(table *Table) {
				table.Columns = tt.columns
			})

			got := driver.HasColumn(tt.tableName, tt.columnName)
			if got != tt.wantExists {
				t.Errorf("HasColumn() = %v, want %v", got, tt.wantExists)
			}
		})
	}
}

func TestSQLiteDriver_Query(t *testing.T) {
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{Database: ":memory:", MaxIdleConns: 1}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	// Create and populate table
	driver.CreateTable("users", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INTEGER", Primary: true, AutoIncrement: true},
			{Name: "name", Type: "TEXT"},
		}
	})

	driver.ExecContext(context.Background(), "INSERT INTO users (name) VALUES (?)", "Alice")
	driver.ExecContext(context.Background(), "INSERT INTO users (name) VALUES (?)", "Bob")

	tests := []struct {
		name      string
		query     string
		args      []any
		wantCount int
		wantErr   bool
	}{
		{
			name:      "queries all rows",
			query:     "SELECT id, name FROM users",
			args:      nil,
			wantCount: 2,
			wantErr:   false,
		},
		{
			name:      "queries with parameter",
			query:     "SELECT id, name FROM users WHERE name = ?",
			args:      []any{"Alice"},
			wantCount: 1,
			wantErr:   false,
		},
		{
			name:      "queries with no results",
			query:     "SELECT id, name FROM users WHERE name = ?",
			args:      []any{"Charlie"},
			wantCount: 0,
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := driver.QueryContext(context.Background(), tt.query, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("Query() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}
			defer rows.Close()

			count := 0
			for rows.Next() {
				count++
			}
			if count != tt.wantCount {
				t.Errorf("Query() returned %d rows, want %d", count, tt.wantCount)
			}
		})
	}
}

func TestSQLiteDriver_QueryRow(t *testing.T) {
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{Database: ":memory:", MaxIdleConns: 1}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	driver.CreateTable("users", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INTEGER", Primary: true, AutoIncrement: true},
			{Name: "name", Type: "TEXT"},
		}
	})

	driver.ExecContext(context.Background(), "INSERT INTO users (name) VALUES (?)", "Alice")

	tests := []struct {
		name     string
		query    string
		args     []any
		wantName string
		wantErr  bool
	}{
		{
			name:     "queries single row",
			query:    "SELECT name FROM users WHERE id = ?",
			args:     []any{1},
			wantName: "Alice",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			row := driver.QueryRowContext(context.Background(), tt.query, tt.args...)
			var name string
			err := row.Scan(&name)
			if (err != nil) != tt.wantErr {
				t.Errorf("QueryRow().Scan() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if name != tt.wantName {
				t.Errorf("QueryRow() name = %q, want %q", name, tt.wantName)
			}
		})
	}
}

func TestSQLiteDriver_Exec(t *testing.T) {
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{Database: ":memory:", MaxIdleConns: 1}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	driver.CreateTable("users", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INTEGER", Primary: true, AutoIncrement: true},
			{Name: "name", Type: "TEXT"},
		}
	})

	tests := []struct {
		name         string
		query        string
		args         []any
		wantAffected int64
		wantErr      bool
	}{
		{
			name:         "inserts row",
			query:        "INSERT INTO users (name) VALUES (?)",
			args:         []any{"Test User"},
			wantAffected: 1,
			wantErr:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := driver.ExecContext(context.Background(), tt.query, tt.args...)
			if (err != nil) != tt.wantErr {
				t.Errorf("Exec() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}

			affected, _ := result.RowsAffected()
			if affected != tt.wantAffected {
				t.Errorf("Exec() RowsAffected() = %d, want %d", affected, tt.wantAffected)
			}
		})
	}
}

func TestSQLiteDriver_Begin(t *testing.T) {
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{Database: ":memory:", MaxIdleConns: 1}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	driver.CreateTable("counter", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INTEGER", Primary: true},
			{Name: "value", Type: "INTEGER"},
		}
	})
	driver.ExecContext(context.Background(), "INSERT INTO counter (id, value) VALUES (1, 0)")

	tests := []struct {
		name      string
		commit    bool
		wantValue int
		testName  string
		wantErr   bool
	}{
		{
			name:      "commits transaction",
			commit:    true,
			wantValue: 100,
			wantErr:   false,
		},
		{
			name:      "rolls back transaction",
			commit:    false,
			wantValue: 100, // Value from previous committed transaction
			wantErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := driver.BeginTx(context.Background(), nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("Begin() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			_, err = tx.Exec("UPDATE counter SET value = ? WHERE id = 1", tt.wantValue+1)
			if err != nil {
				t.Errorf("tx.Exec() error = %v", err)
				tx.Rollback()
				return
			}

			if tt.commit {
				// Update to expected value and commit
				tx.Exec("UPDATE counter SET value = ? WHERE id = 1", tt.wantValue)
				if err := tx.Commit(); err != nil {
					t.Errorf("Commit() error = %v", err)
				}
			} else {
				if err := tx.Rollback(); err != nil {
					t.Errorf("Rollback() error = %v", err)
				}
			}

			// Verify value
			var value int
			row := driver.QueryRowContext(context.Background(), "SELECT value FROM counter WHERE id = 1")
			row.Scan(&value)
			if value != tt.wantValue {
				t.Errorf("value = %d, want %d", value, tt.wantValue)
			}
		})
	}
}

func TestSQLiteDriver_BeginTx(t *testing.T) {
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{Database: ":memory:", MaxIdleConns: 1}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	tests := []struct {
		name    string
		wantErr bool
	}{
		{"begins transaction successfully", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tx, err := driver.BeginTx(context.Background(), nil)
			if (err != nil) != tt.wantErr {
				t.Errorf("BeginTx() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tx != nil {
				tx.Rollback()
			}
		})
	}
}

func TestSQLiteDriver_Connect_WithFilePath(t *testing.T) {
	// Create a temporary directory for test databases
	tmpDir, err := os.MkdirTemp("", "sqlite_test_*")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	tests := []struct {
		name    string
		config  ConnectionConfig
		wantErr bool
	}{
		{
			name: "creates database file in specified path",
			config: ConnectionConfig{
				Database: filepath.Join(tmpDir, "test.db"),
			},
			wantErr: false,
		},
		{
			name: "creates database file with nested path",
			config: ConnectionConfig{
				Database: filepath.Join(tmpDir, "nested", "dir", "test.db"),
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewSQLiteDriver()
			err := driver.Connect(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("Connect() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil {
				defer driver.Close()

				// Verify file was created
				if _, err := os.Stat(tt.config.Database); os.IsNotExist(err) {
					t.Errorf("Database file was not created at %s", tt.config.Database)
				}
			}
		})
	}
}

func TestSQLiteDriver_Connect_WithTimezone(t *testing.T) {
	tests := []struct {
		name    string
		config  ConnectionConfig
		wantErr bool
	}{
		{
			name: "connects with timezone setting",
			config: ConnectionConfig{
				Database:     ":memory:",
				TimeZone:     "UTC",
				MaxIdleConns: 1,
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewSQLiteDriver()
			err := driver.Connect(tt.config)
			if (err != nil) != tt.wantErr {
				t.Errorf("Connect() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err == nil {
				driver.Close()
			}
		})
	}
}

func TestSQLiteGrammar_CompileHasTable(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name  string
		table string
		want  string
	}{
		{
			name:  "returns sqlite_master query",
			table: "users",
			want:  "SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.CompileHasTable(tt.table)
			if got != tt.want {
				t.Errorf("CompileHasTable() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSQLiteGrammar_CompileHasColumn(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name   string
		table  string
		column string
		want   string
	}{
		{
			name:   "returns PRAGMA table_info query",
			table:  "users",
			column: "email",
			want:   "PRAGMA table_info(`users`)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.CompileHasColumn(tt.table, tt.column)
			if got != tt.want {
				t.Errorf("CompileHasColumn() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSQLiteGrammar_CompileSelect_WithGroupByAndHaving(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "compiles GROUP BY with HAVING",
			query: &SelectQuery{
				Table:   "orders",
				Columns: []string{"user_id", "COUNT(*) as order_count"},
				Groups:  []string{"user_id"},
				Having: []Condition{
					{Column: "order_count", Operator: ">", Value: 5, Type: "and"},
				},
			},
			wantSQL:  "SELECT `user_id`, COUNT(*) as order_count FROM `orders` GROUP BY `user_id` HAVING `order_count` > ?",
			wantArgs: []any{5},
		},
		{
			name: "compiles HAVING with IS NULL (no placeholder, no args)",
			query: &SelectQuery{
				Table:   "orders",
				Columns: []string{"user_id", "MAX(updated_at) as last_update"},
				Groups:  []string{"user_id"},
				Having: []Condition{
					{Column: "last_update", Operator: "IS NULL", Type: "and"},
				},
			},
			wantSQL:  "SELECT `user_id`, MAX(updated_at) as last_update FROM `orders` GROUP BY `user_id` HAVING `last_update` IS NULL",
			wantArgs: nil,
		},
		{
			name: "compiles HAVING with IS NOT NULL (no placeholder, no args)",
			query: &SelectQuery{
				Table:   "orders",
				Columns: []string{"user_id", "MAX(updated_at) as last_update"},
				Groups:  []string{"user_id"},
				Having: []Condition{
					{Column: "last_update", Operator: "IS NOT NULL", Type: "and"},
				},
			},
			wantSQL:  "SELECT `user_id`, MAX(updated_at) as last_update FROM `orders` GROUP BY `user_id` HAVING `last_update` IS NOT NULL",
			wantArgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileSelect(tt.query)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if len(gotArgs) != len(tt.wantArgs) {
				t.Errorf("CompileSelect() args length = %d, want %d", len(gotArgs), len(tt.wantArgs))
			}
			for i := range gotArgs {
				if i >= len(tt.wantArgs) {
					break
				}
				if gotArgs[i] != tt.wantArgs[i] {
					t.Errorf("CompileSelect() args[%d] = %v, want %v", i, gotArgs[i], tt.wantArgs[i])
				}
			}
		})
	}
}

func TestSQLiteGrammar_CompileSelect_WithOrCondition(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "compiles OR condition",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name"},
				Conditions: []Condition{
					{Column: "role", Operator: "=", Value: "admin", Type: "and"},
					{Column: "role", Operator: "=", Value: "moderator", Type: "or"},
				},
			},
			wantSQL:  "SELECT `id`, `name` FROM `users` WHERE `role` = ? OR `role` = ?",
			wantArgs: []any{"admin", "moderator"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileSelect(tt.query)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if len(gotArgs) != len(tt.wantArgs) {
				t.Errorf("CompileSelect() args length = %d, want %d", len(gotArgs), len(tt.wantArgs))
			}
		})
	}
}

func TestSQLiteGrammar_CompileSelect_IgnoresLockForUpdate(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "ignores FOR UPDATE since SQLite does not support it",
			query: &SelectQuery{
				Table:         "users",
				Columns:       []string{"id"},
				LockForUpdate: true,
			},
			wantSQL:  "SELECT `id` FROM `users`",
			wantArgs: nil,
		},
		{
			name: "ignores SKIP LOCKED since SQLite does not support it",
			query: &SelectQuery{
				Table:         "users",
				Columns:       []string{"id"},
				LockForUpdate: true,
				SkipLocked:    true,
			},
			wantSQL:  "SELECT `id` FROM `users`",
			wantArgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileSelect(tt.query)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if len(gotArgs) != len(tt.wantArgs) {
				t.Errorf("CompileSelect() args length = %d, want %d", len(gotArgs), len(tt.wantArgs))
			}
		})
	}
}

func TestSQLiteDriver_LogQueries(t *testing.T) {
	driver := NewSQLiteDriver()
	config := ConnectionConfig{
		Database:     ":memory:",
		LogQueries:   true,
		MaxIdleConns: 1,
	}
	if err := driver.Connect(config); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	// Create a table to test query logging
	driver.CreateTable("test_log", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INTEGER", Primary: true},
		}
	})

	tests := []struct {
		name string
	}{
		{"logs query without error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// These should not panic even with LogQueries enabled
			_, _ = driver.QueryContext(context.Background(), "SELECT * FROM test_log")
			_ = driver.QueryRowContext(context.Background(), "SELECT * FROM test_log WHERE id = ?", 1)
			_, _ = driver.ExecContext(context.Background(), "INSERT INTO test_log (id) VALUES (?)", 1)
		})
	}
}

// =============================================================================
// SQL Injection Prevention Tests
// =============================================================================

func TestSQLiteDriver_SQLInjectionPrevention(t *testing.T) {
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{Database: ":memory:", MaxIdleConns: 1}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	// Create test table
	driver.CreateTable("users", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INTEGER", Primary: true, AutoIncrement: true},
			{Name: "name", Type: "TEXT"},
			{Name: "email", Type: "TEXT"},
		}
	})

	tests := []struct {
		name           string
		maliciousInput string
		wantStored     string
	}{
		{
			name:           "prevents DROP TABLE injection",
			maliciousInput: "'; DROP TABLE users; --",
			wantStored:     "'; DROP TABLE users; --",
		},
		{
			name:           "prevents OR 1=1 injection",
			maliciousInput: "admin' OR '1'='1",
			wantStored:     "admin' OR '1'='1",
		},
		{
			name:           "handles single quotes in data",
			maliciousInput: "O'Brien",
			wantStored:     "O'Brien",
		},
		{
			name:           "handles double quotes in data",
			maliciousInput: `He said "hello"`,
			wantStored:     `He said "hello"`,
		},
		{
			name:           "handles semicolons in data",
			maliciousInput: "test; DELETE FROM users;",
			wantStored:     "test; DELETE FROM users;",
		},
		{
			name:           "handles backslash escape attempts",
			maliciousInput: `test\'; DROP TABLE users; --`,
			wantStored:     `test\'; DROP TABLE users; --`,
		},
		{
			name:           "handles null byte injection",
			maliciousInput: "test\x00injection",
			wantStored:     "test\x00injection",
		},
		{
			name:           "handles unicode injection",
			maliciousInput: "test\u0027 OR 1=1--",
			wantStored:     "test' OR 1=1--",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Insert malicious input using parameterized query
			result, err := driver.ExecContext(context.Background(), "INSERT INTO users (name, email) VALUES (?, ?)", tt.maliciousInput, "test@example.com")
			if err != nil {
				t.Fatalf("Insert failed: %v", err)
			}

			id, _ := result.LastInsertId()

			// Verify table still exists
			if !driver.HasTable("users") {
				t.Fatal("Table 'users' was dropped - SQL injection succeeded!")
			}

			// Verify data was stored correctly (escaped, not executed)
			var name string
			row := driver.QueryRowContext(context.Background(), "SELECT name FROM users WHERE id = ?", id)
			if err := row.Scan(&name); err != nil {
				t.Fatalf("Failed to retrieve inserted data: %v", err)
			}

			if name != tt.wantStored {
				t.Errorf("Data not properly stored: got %q, want %q", name, tt.wantStored)
			}

			// Clean up for next test
			driver.ExecContext(context.Background(), "DELETE FROM users WHERE id = ?", id)
		})
	}
}

func TestSQLiteDriver_SQLInjectionInWhereClause(t *testing.T) {
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{Database: ":memory:", MaxIdleConns: 1}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	// Create and populate test table
	driver.CreateTable("users", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INTEGER", Primary: true, AutoIncrement: true},
			{Name: "name", Type: "TEXT"},
			{Name: "password", Type: "TEXT"},
		}
	})

	driver.ExecContext(context.Background(), "INSERT INTO users (name, password) VALUES (?, ?)", "admin", "secret123")
	driver.ExecContext(context.Background(), "INSERT INTO users (name, password) VALUES (?, ?)", "user1", "password1")

	tests := []struct {
		name          string
		maliciousName string
		wantRowCount  int
	}{
		{
			name:          "OR injection does not bypass authentication",
			maliciousName: "admin' OR '1'='1",
			wantRowCount:  0,
		},
		{
			name:          "UNION injection does not work",
			maliciousName: "admin' UNION SELECT * FROM users--",
			wantRowCount:  0,
		},
		{
			name:          "comment injection does not work",
			maliciousName: "admin'--",
			wantRowCount:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := driver.QueryContext(context.Background(), "SELECT * FROM users WHERE name = ?", tt.maliciousName)
			if err != nil {
				t.Fatalf("Query failed: %v", err)
			}
			defer rows.Close()

			count := 0
			for rows.Next() {
				count++
			}

			if count != tt.wantRowCount {
				t.Errorf("Expected %d rows, got %d - injection may have succeeded", tt.wantRowCount, count)
			}
		})
	}
}

// =============================================================================
// Transaction Rollback Verification Tests
// =============================================================================

func TestSQLiteDriver_TransactionRollback(t *testing.T) {
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{Database: ":memory:", MaxIdleConns: 1}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	// Create test table
	driver.CreateTable("accounts", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INTEGER", Primary: true, AutoIncrement: true},
			{Name: "name", Type: "TEXT"},
			{Name: "balance", Type: "INTEGER"},
		}
	})

	tests := []struct {
		name           string
		setupBalance   int
		insertName     string
		shouldRollback bool
		wantBalance    int
		wantRowExists  bool
	}{
		{
			name:           "rollback reverts insert",
			setupBalance:   100,
			insertName:     "RollbackTest",
			shouldRollback: true,
			wantBalance:    100,
			wantRowExists:  false,
		},
		{
			name:           "commit persists insert",
			setupBalance:   200,
			insertName:     "CommitTest",
			shouldRollback: false,
			wantBalance:    200,
			wantRowExists:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Clear table
			driver.ExecContext(context.Background(), "DELETE FROM accounts")

			// Setup initial state
			driver.ExecContext(context.Background(), "INSERT INTO accounts (name, balance) VALUES (?, ?)", "Initial", tt.setupBalance)

			// Begin transaction
			tx, err := driver.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("Begin() error = %v", err)
			}

			// Insert new row in transaction
			_, err = tx.Exec("INSERT INTO accounts (name, balance) VALUES (?, ?)", tt.insertName, 50)
			if err != nil {
				tx.Rollback()
				t.Fatalf("tx.Exec() error = %v", err)
			}

			// Update existing row in transaction
			_, err = tx.Exec("UPDATE accounts SET balance = balance + 100 WHERE name = ?", "Initial")
			if err != nil {
				tx.Rollback()
				t.Fatalf("tx.Exec() error = %v", err)
			}

			// Rollback or commit
			if tt.shouldRollback {
				if err := tx.Rollback(); err != nil {
					t.Fatalf("Rollback() error = %v", err)
				}
			} else {
				if err := tx.Commit(); err != nil {
					t.Fatalf("Commit() error = %v", err)
				}
			}

			// Verify balance
			var balance int
			row := driver.QueryRowContext(context.Background(), "SELECT balance FROM accounts WHERE name = ?", "Initial")
			if err := row.Scan(&balance); err != nil {
				t.Fatalf("Failed to query balance: %v", err)
			}

			if tt.shouldRollback {
				// After rollback, balance should be unchanged
				if balance != tt.wantBalance {
					t.Errorf("Balance after rollback = %d, want %d", balance, tt.wantBalance)
				}
			} else {
				// After commit, balance should be updated (+100)
				expectedBalance := tt.wantBalance + 100
				if balance != expectedBalance {
					t.Errorf("Balance after commit = %d, want %d", balance, expectedBalance)
				}
			}

			// Verify inserted row existence
			var count int
			row = driver.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM accounts WHERE name = ?", tt.insertName)
			row.Scan(&count)
			rowExists := count > 0

			if rowExists != tt.wantRowExists {
				t.Errorf("Row exists = %v, want %v", rowExists, tt.wantRowExists)
			}
		})
	}
}

func TestSQLiteDriver_TransactionRollbackNestedOperations(t *testing.T) {
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{Database: ":memory:", MaxIdleConns: 1}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	// Create multiple tables
	driver.CreateTable("orders", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INTEGER", Primary: true, AutoIncrement: true},
			{Name: "customer", Type: "TEXT"},
			{Name: "total", Type: "INTEGER"},
		}
	})

	driver.CreateTable("order_items", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INTEGER", Primary: true, AutoIncrement: true},
			{Name: "order_id", Type: "INTEGER"},
			{Name: "product", Type: "TEXT"},
			{Name: "quantity", Type: "INTEGER"},
		}
	})

	driver.CreateTable("inventory", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INTEGER", Primary: true, AutoIncrement: true},
			{Name: "product", Type: "TEXT"},
			{Name: "stock", Type: "INTEGER"},
		}
	})

	// Setup initial inventory
	driver.ExecContext(context.Background(), "INSERT INTO inventory (product, stock) VALUES (?, ?)", "Widget", 100)
	driver.ExecContext(context.Background(), "INSERT INTO inventory (product, stock) VALUES (?, ?)", "Gadget", 50)

	tests := []struct {
		name            string
		shouldRollback  bool
		wantOrderCount  int
		wantItemCount   int
		wantWidgetStock int
		wantGadgetStock int
	}{
		{
			name:            "rollback reverts all nested operations",
			shouldRollback:  true,
			wantOrderCount:  0,
			wantItemCount:   0,
			wantWidgetStock: 100,
			wantGadgetStock: 50,
		},
		{
			name:            "commit persists all nested operations",
			shouldRollback:  false,
			wantOrderCount:  1,
			wantItemCount:   2,
			wantWidgetStock: 95,
			wantGadgetStock: 47,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset state
			driver.ExecContext(context.Background(), "DELETE FROM orders")
			driver.ExecContext(context.Background(), "DELETE FROM order_items")
			driver.ExecContext(context.Background(), "UPDATE inventory SET stock = 100 WHERE product = ?", "Widget")
			driver.ExecContext(context.Background(), "UPDATE inventory SET stock = 50 WHERE product = ?", "Gadget")

			// Begin transaction
			tx, err := driver.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("Begin() error = %v", err)
			}

			// Create order
			result, err := tx.Exec("INSERT INTO orders (customer, total) VALUES (?, ?)", "John", 150)
			if err != nil {
				tx.Rollback()
				t.Fatalf("Failed to insert order: %v", err)
			}
			orderID, _ := result.LastInsertId()

			// Add order items
			_, err = tx.Exec("INSERT INTO order_items (order_id, product, quantity) VALUES (?, ?, ?)", orderID, "Widget", 5)
			if err != nil {
				tx.Rollback()
				t.Fatalf("Failed to insert order item: %v", err)
			}

			_, err = tx.Exec("INSERT INTO order_items (order_id, product, quantity) VALUES (?, ?, ?)", orderID, "Gadget", 3)
			if err != nil {
				tx.Rollback()
				t.Fatalf("Failed to insert order item: %v", err)
			}

			// Update inventory
			_, err = tx.Exec("UPDATE inventory SET stock = stock - 5 WHERE product = ?", "Widget")
			if err != nil {
				tx.Rollback()
				t.Fatalf("Failed to update inventory: %v", err)
			}

			_, err = tx.Exec("UPDATE inventory SET stock = stock - 3 WHERE product = ?", "Gadget")
			if err != nil {
				tx.Rollback()
				t.Fatalf("Failed to update inventory: %v", err)
			}

			// Rollback or commit
			if tt.shouldRollback {
				tx.Rollback()
			} else {
				tx.Commit()
			}

			// Verify order count
			var orderCount int
			driver.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM orders").Scan(&orderCount)
			if orderCount != tt.wantOrderCount {
				t.Errorf("Order count = %d, want %d", orderCount, tt.wantOrderCount)
			}

			// Verify item count
			var itemCount int
			driver.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM order_items").Scan(&itemCount)
			if itemCount != tt.wantItemCount {
				t.Errorf("Item count = %d, want %d", itemCount, tt.wantItemCount)
			}

			// Verify inventory
			var widgetStock, gadgetStock int
			driver.QueryRowContext(context.Background(), "SELECT stock FROM inventory WHERE product = ?", "Widget").Scan(&widgetStock)
			driver.QueryRowContext(context.Background(), "SELECT stock FROM inventory WHERE product = ?", "Gadget").Scan(&gadgetStock)

			if widgetStock != tt.wantWidgetStock {
				t.Errorf("Widget stock = %d, want %d", widgetStock, tt.wantWidgetStock)
			}
			if gadgetStock != tt.wantGadgetStock {
				t.Errorf("Gadget stock = %d, want %d", gadgetStock, tt.wantGadgetStock)
			}
		})
	}
}

// =============================================================================
// Query Builder SQL Verification Tests
// =============================================================================

func TestSQLiteGrammar_CompileSelect_ComplexQueries(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "compiles simple select",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name"},
			},
			wantSQL:  "SELECT `id`, `name` FROM `users`",
			wantArgs: nil,
		},
		{
			name: "compiles select with single WHERE",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"*"},
				Conditions: []Condition{
					{Column: "active", Operator: "=", Value: true, Type: "and"},
				},
			},
			wantSQL:  "SELECT * FROM `users` WHERE `active` = ?",
			wantArgs: []any{true},
		},
		{
			name: "compiles select with multiple WHERE conditions",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name", "email"},
				Conditions: []Condition{
					{Column: "active", Operator: "=", Value: true, Type: "and"},
					{Column: "age", Operator: ">=", Value: 18, Type: "and"},
					{Column: "role", Operator: "=", Value: "admin", Type: "and"},
				},
			},
			wantSQL:  "SELECT `id`, `name`, `email` FROM `users` WHERE `active` = ? AND `age` >= ? AND `role` = ?",
			wantArgs: []any{true, 18, "admin"},
		},
		{
			name: "compiles select with DISTINCT",
			query: &SelectQuery{
				Table:    "users",
				Columns:  []string{"country"},
				Distinct: true,
			},
			wantSQL:  "SELECT DISTINCT `country` FROM `users`",
			wantArgs: nil,
		},
		{
			name: "compiles select with ORDER BY single column",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"*"},
				Orders: []Order{
					{Column: "created_at", Direction: "DESC"},
				},
			},
			wantSQL:  "SELECT * FROM `users` ORDER BY `created_at` DESC",
			wantArgs: nil,
		},
		{
			name: "compiles select with ORDER BY multiple columns",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"*"},
				Orders: []Order{
					{Column: "last_name", Direction: "ASC"},
					{Column: "first_name", Direction: "ASC"},
				},
			},
			wantSQL:  "SELECT * FROM `users` ORDER BY `last_name` ASC, `first_name` ASC",
			wantArgs: nil,
		},
		{
			name: "compiles select with LIMIT",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"*"},
				Limit:   intPtr(10),
			},
			wantSQL:  "SELECT * FROM `users` LIMIT 10",
			wantArgs: nil,
		},
		{
			name: "compiles select with LIMIT and OFFSET",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"*"},
				Limit:   intPtr(10),
				Offset:  intPtr(20),
			},
			wantSQL:  "SELECT * FROM `users` LIMIT 10 OFFSET 20",
			wantArgs: nil,
		},
		{
			name: "compiles select with GROUP BY",
			query: &SelectQuery{
				Table:   "orders",
				Columns: []string{"user_id", "COUNT(*) as total"},
				Groups:  []string{"user_id"},
			},
			wantSQL:  "SELECT `user_id`, COUNT(*) as total FROM `orders` GROUP BY `user_id`",
			wantArgs: nil,
		},
		{
			name: "compiles select with GROUP BY and HAVING",
			query: &SelectQuery{
				Table:   "orders",
				Columns: []string{"user_id", "SUM(amount) as total"},
				Groups:  []string{"user_id"},
				Having: []Condition{
					{Column: "total", Operator: ">", Value: 1000, Type: "and"},
				},
			},
			wantSQL:  "SELECT `user_id`, SUM(amount) as total FROM `orders` GROUP BY `user_id` HAVING `total` > ?",
			wantArgs: []any{1000},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileSelect(tt.query)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL =\n%q\nwant:\n%q", gotSQL, tt.wantSQL)
			}
			if len(gotArgs) != len(tt.wantArgs) {
				t.Errorf("CompileSelect() args length = %d, want %d", len(gotArgs), len(tt.wantArgs))
			}
			for i := range gotArgs {
				if i < len(tt.wantArgs) && gotArgs[i] != tt.wantArgs[i] {
					t.Errorf("CompileSelect() arg[%d] = %v, want %v", i, gotArgs[i], tt.wantArgs[i])
				}
			}
		})
	}
}

func TestSQLiteGrammar_CompileSelect_JOINQueries(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "compiles INNER JOIN",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"users.id", "users.name", "roles.name"},
				Joins: []Join{
					{Type: "INNER", Table: "roles", On: "users.role_id = roles.id"},
				},
			},
			wantSQL:  "SELECT `users.id`, `users.name`, `roles.name` FROM `users` INNER JOIN `roles` ON users.role_id = roles.id",
			wantArgs: nil,
		},
		{
			name: "compiles LEFT JOIN",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"users.*", "profiles.bio"},
				Joins: []Join{
					{Type: "LEFT", Table: "profiles", On: "users.id = profiles.user_id"},
				},
			},
			wantSQL:  "SELECT `users.*`, `profiles.bio` FROM `users` LEFT JOIN `profiles` ON users.id = profiles.user_id",
			wantArgs: nil,
		},
		{
			name: "compiles RIGHT JOIN",
			query: &SelectQuery{
				Table:   "employees",
				Columns: []string{"*"},
				Joins: []Join{
					{Type: "RIGHT", Table: "departments", On: "employees.dept_id = departments.id"},
				},
			},
			wantSQL:  "SELECT * FROM `employees` RIGHT JOIN `departments` ON employees.dept_id = departments.id",
			wantArgs: nil,
		},
		{
			name: "compiles multiple JOINs",
			query: &SelectQuery{
				Table:   "orders",
				Columns: []string{"orders.id", "users.name", "products.title"},
				Joins: []Join{
					{Type: "INNER", Table: "users", On: "orders.user_id = users.id"},
					{Type: "LEFT", Table: "products", On: "orders.product_id = products.id"},
				},
			},
			wantSQL:  "SELECT `orders.id`, `users.name`, `products.title` FROM `orders` INNER JOIN `users` ON orders.user_id = users.id LEFT JOIN `products` ON orders.product_id = products.id",
			wantArgs: nil,
		},
		{
			name: "compiles JOIN with WHERE",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"users.name", "orders.total"},
				Joins: []Join{
					{Type: "LEFT", Table: "orders", On: "users.id = orders.user_id"},
				},
				Conditions: []Condition{
					{Column: "orders.status", Operator: "=", Value: "completed", Type: "and"},
				},
			},
			wantSQL:  "SELECT `users.name`, `orders.total` FROM `users` LEFT JOIN `orders` ON users.id = orders.user_id WHERE `orders.status` = ?",
			wantArgs: []any{"completed"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileSelect(tt.query)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL =\n%q\nwant:\n%q", gotSQL, tt.wantSQL)
			}
			if len(gotArgs) != len(tt.wantArgs) {
				t.Errorf("CompileSelect() args length = %d, want %d", len(gotArgs), len(tt.wantArgs))
			}
		})
	}
}

// =============================================================================
// Concurrent Access Tests
// =============================================================================

func TestSQLiteDriver_ConcurrentReads(t *testing.T) {
	// Use temp file for concurrent test - in-memory doesn't share across connections
	tmpFile := t.TempDir() + "/concurrent_reads.db"
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{Database: tmpFile, MaxOpenConns: 10, MaxIdleConns: 5}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	// Create and populate table
	driver.CreateTable("products", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INTEGER", Primary: true, AutoIncrement: true},
			{Name: "name", Type: "TEXT"},
			{Name: "price", Type: "REAL"},
		}
	})

	// Insert test data
	for i := 1; i <= 100; i++ {
		driver.ExecContext(context.Background(), "INSERT INTO products (name, price) VALUES (?, ?)", fmt.Sprintf("Product %d", i), float64(i)*1.5)
	}

	tests := []struct {
		name           string
		goroutineCount int
		readsPerGo     int
	}{
		{
			name:           "10 concurrent readers",
			goroutineCount: 10,
			readsPerGo:     10,
		},
		{
			name:           "50 concurrent readers",
			goroutineCount: 50,
			readsPerGo:     5,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wg sync.WaitGroup
			errors := make(chan error, tt.goroutineCount*tt.readsPerGo)

			for i := 0; i < tt.goroutineCount; i++ {
				wg.Add(1)
				go func(workerID int) {
					defer wg.Done()
					for j := 0; j < tt.readsPerGo; j++ {
						productID := (workerID*tt.readsPerGo+j)%100 + 1
						row := driver.QueryRowContext(context.Background(), "SELECT id, name, price FROM products WHERE id = ?", productID)
						var id int
						var name string
						var price float64
						if err := row.Scan(&id, &name, &price); err != nil {
							errors <- fmt.Errorf("worker %d read %d: %v", workerID, j, err)
							continue
						}
						expectedName := fmt.Sprintf("Product %d", productID)
						if name != expectedName {
							errors <- fmt.Errorf("worker %d: got name %q, want %q", workerID, name, expectedName)
						}
					}
				}(i)
			}

			wg.Wait()
			close(errors)

			for err := range errors {
				t.Error(err)
			}
		})
	}
}

func TestSQLiteDriver_ConcurrentWrites(t *testing.T) {
	// Use temp file for concurrent test - in-memory doesn't share across connections
	tmpFile := t.TempDir() + "/concurrent_writes.db"
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{Database: tmpFile, MaxOpenConns: 10, MaxIdleConns: 5}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	// Create table
	driver.CreateTable("counters", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INTEGER", Primary: true},
			{Name: "value", Type: "INTEGER"},
		}
	})

	// Initialize counter
	driver.ExecContext(context.Background(), "INSERT INTO counters (id, value) VALUES (?, ?)", 1, 0)

	tests := []struct {
		name            string
		goroutineCount  int
		incrementsPerGo int
		wantFinalValue  int
	}{
		{
			name:            "10 concurrent incrementers",
			goroutineCount:  10,
			incrementsPerGo: 10,
			wantFinalValue:  100,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset counter
			driver.ExecContext(context.Background(), "UPDATE counters SET value = 0 WHERE id = 1")

			var wg sync.WaitGroup
			var mu sync.Mutex
			errors := make(chan error, tt.goroutineCount)

			for i := 0; i < tt.goroutineCount; i++ {
				wg.Add(1)
				go func(workerID int) {
					defer wg.Done()
					for j := 0; j < tt.incrementsPerGo; j++ {
						// Use mutex for SQLite since it has limited concurrency for writes
						mu.Lock()
						_, err := driver.ExecContext(context.Background(), "UPDATE counters SET value = value + 1 WHERE id = 1")
						mu.Unlock()
						if err != nil {
							errors <- fmt.Errorf("worker %d increment %d: %v", workerID, j, err)
							return
						}
					}
				}(i)
			}

			wg.Wait()
			close(errors)

			for err := range errors {
				t.Error(err)
			}

			// Verify final value
			var finalValue int
			driver.QueryRowContext(context.Background(), "SELECT value FROM counters WHERE id = 1").Scan(&finalValue)
			if finalValue != tt.wantFinalValue {
				t.Errorf("Final counter value = %d, want %d", finalValue, tt.wantFinalValue)
			}
		})
	}
}

func TestSQLiteDriver_ConcurrentReadWrite(t *testing.T) {
	// Use temp file for concurrent test - in-memory doesn't share across connections
	tmpFile := t.TempDir() + "/concurrent_test.db"
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{Database: tmpFile, MaxOpenConns: 10, MaxIdleConns: 5}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	// Create table
	driver.CreateTable("items", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INTEGER", Primary: true, AutoIncrement: true},
			{Name: "name", Type: "TEXT"},
			{Name: "updated_at", Type: "TEXT"},
		}
	})

	// Insert initial data
	for i := 1; i <= 10; i++ {
		driver.ExecContext(context.Background(), "INSERT INTO items (name, updated_at) VALUES (?, ?)", fmt.Sprintf("Item %d", i), "initial")
	}

	tests := []struct {
		name       string
		readers    int
		writers    int
		iterations int
	}{
		{
			name:       "concurrent readers and writers",
			readers:    5,
			writers:    3,
			iterations: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wg sync.WaitGroup
			var mu sync.Mutex
			errors := make(chan error, (tt.readers+tt.writers)*tt.iterations)

			// Start readers
			for i := 0; i < tt.readers; i++ {
				wg.Add(1)
				go func(readerID int) {
					defer wg.Done()
					for j := 0; j < tt.iterations; j++ {
						itemID := j%10 + 1
						row := driver.QueryRowContext(context.Background(), "SELECT name FROM items WHERE id = ?", itemID)
						var name string
						if err := row.Scan(&name); err != nil {
							errors <- fmt.Errorf("reader %d iteration %d: %v", readerID, j, err)
						}
					}
				}(i)
			}

			// Start writers
			for i := 0; i < tt.writers; i++ {
				wg.Add(1)
				go func(writerID int) {
					defer wg.Done()
					for j := 0; j < tt.iterations; j++ {
						itemID := j%10 + 1
						mu.Lock()
						_, err := driver.ExecContext(context.Background(), "UPDATE items SET updated_at = ? WHERE id = ?",
							fmt.Sprintf("writer%d-iter%d", writerID, j), itemID)
						mu.Unlock()
						if err != nil {
							errors <- fmt.Errorf("writer %d iteration %d: %v", writerID, j, err)
						}
					}
				}(i)
			}

			wg.Wait()
			close(errors)

			for err := range errors {
				t.Error(err)
			}

			// Verify data integrity - all items should still exist
			var count int
			driver.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM items").Scan(&count)
			if count != 10 {
				t.Errorf("Item count = %d, want 10 (data integrity violated)", count)
			}
		})
	}
}

func TestSQLiteDriver_ConcurrentTransactions(t *testing.T) {
	// Use temp file for concurrent test - in-memory doesn't share across connections
	tmpFile := t.TempDir() + "/concurrent_txn.db"
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{Database: tmpFile, MaxOpenConns: 10, MaxIdleConns: 5}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	// Create table for balance transfers
	driver.CreateTable("bank_accounts", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INTEGER", Primary: true},
			{Name: "balance", Type: "INTEGER"},
		}
	})

	// Initialize accounts
	driver.ExecContext(context.Background(), "INSERT INTO bank_accounts (id, balance) VALUES (?, ?)", 1, 1000)
	driver.ExecContext(context.Background(), "INSERT INTO bank_accounts (id, balance) VALUES (?, ?)", 2, 1000)

	tests := []struct {
		name             string
		transferCount    int
		wantTotalBalance int
	}{
		{
			name:             "concurrent transfers maintain total balance",
			transferCount:    20,
			wantTotalBalance: 2000, // Total should remain constant
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset balances
			driver.ExecContext(context.Background(), "UPDATE bank_accounts SET balance = 1000 WHERE id IN (1, 2)")

			var wg sync.WaitGroup
			var mu sync.Mutex
			errors := make(chan error, tt.transferCount)

			for i := 0; i < tt.transferCount; i++ {
				wg.Add(1)
				go func(transferID int) {
					defer wg.Done()

					// Alternate transfer direction
					fromID := 1 + (transferID % 2)
					toID := 1 + ((transferID + 1) % 2)
					amount := 10

					mu.Lock()
					tx, err := driver.BeginTx(context.Background(), nil)
					if err != nil {
						mu.Unlock()
						errors <- fmt.Errorf("transfer %d: begin error: %v", transferID, err)
						return
					}

					// Debit from source
					_, err = tx.Exec("UPDATE bank_accounts SET balance = balance - ? WHERE id = ?", amount, fromID)
					if err != nil {
						tx.Rollback()
						mu.Unlock()
						errors <- fmt.Errorf("transfer %d: debit error: %v", transferID, err)
						return
					}

					// Credit to destination
					_, err = tx.Exec("UPDATE bank_accounts SET balance = balance + ? WHERE id = ?", amount, toID)
					if err != nil {
						tx.Rollback()
						mu.Unlock()
						errors <- fmt.Errorf("transfer %d: credit error: %v", transferID, err)
						return
					}

					if err := tx.Commit(); err != nil {
						mu.Unlock()
						errors <- fmt.Errorf("transfer %d: commit error: %v", transferID, err)
						return
					}
					mu.Unlock()
				}(i)
			}

			wg.Wait()
			close(errors)

			for err := range errors {
				t.Error(err)
			}

			// Verify total balance is preserved
			var totalBalance int
			driver.QueryRowContext(context.Background(), "SELECT SUM(balance) FROM bank_accounts").Scan(&totalBalance)
			if totalBalance != tt.wantTotalBalance {
				t.Errorf("Total balance = %d, want %d (money was created or destroyed)", totalBalance, tt.wantTotalBalance)
			}
		})
	}
}

// TestSQLiteGrammar_CompileSelect_InAndBetween covers the IN/NOT IN/BETWEEN/
// NOT BETWEEN operator paths in compileConditions. Without explicit cases the
// default branch emits a single `?` placeholder and binds the entire slice as
// one arg, which produces invalid SQL like `"col" IN ?`.
func TestSQLiteGrammar_CompileSelect_InAndBetween(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "WhereIn expands to parenthesised placeholder list",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Conditions: []Condition{
					{Column: "id", Operator: "IN", Value: []any{1, 2, 3}, Type: "and"},
				},
			},
			wantSQL:  "SELECT `id` FROM `users` WHERE `id` IN (?, ?, ?)",
			wantArgs: []any{1, 2, 3},
		},
		{
			name: "WhereNotIn expands to parenthesised placeholder list",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Conditions: []Condition{
					{Column: "status", Operator: "NOT IN", Value: []any{"a", "b"}, Type: "and"},
				},
			},
			wantSQL:  "SELECT `id` FROM `users` WHERE `status` NOT IN (?, ?)",
			wantArgs: []any{"a", "b"},
		},
		{
			name: "WhereIn with a single element still parenthesises",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Conditions: []Condition{
					{Column: "id", Operator: "IN", Value: []any{42}, Type: "and"},
				},
			},
			wantSQL:  "SELECT `id` FROM `users` WHERE `id` IN (?)",
			wantArgs: []any{42},
		},
		{
			name: "WhereBetween emits ? AND ? with both bounds bound",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Conditions: []Condition{
					{Column: "age", Operator: "BETWEEN", Value: []any{18, 65}, Type: "and"},
				},
			},
			wantSQL:  "SELECT `id` FROM `users` WHERE `age` BETWEEN ? AND ?",
			wantArgs: []any{18, 65},
		},
		{
			name: "WhereNotBetween emits NOT BETWEEN ? AND ? with both bounds bound",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Conditions: []Condition{
					{Column: "age", Operator: "NOT BETWEEN", Value: []any{18, 65}, Type: "and"},
				},
			},
			wantSQL:  "SELECT `id` FROM `users` WHERE `age` NOT BETWEEN ? AND ?",
			wantArgs: []any{18, 65},
		},
		{
			name: "WhereIn with empty slice collapses to 1 = 0 (never matches)",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Conditions: []Condition{
					{Column: "id", Operator: "IN", Value: []any{}, Type: "and"},
				},
			},
			wantSQL:  "SELECT `id` FROM `users` WHERE 1 = 0",
			wantArgs: nil,
		},
		{
			name: "WhereNotIn with empty slice collapses to 1 = 1 (always matches)",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Conditions: []Condition{
					{Column: "id", Operator: "NOT IN", Value: []any{}, Type: "and"},
				},
			},
			wantSQL:  "SELECT `id` FROM `users` WHERE 1 = 1",
			wantArgs: nil,
		},
		{
			name: "IN mixes correctly with other AND-joined conditions",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Conditions: []Condition{
					{Column: "tenant_id", Operator: "=", Value: 7, Type: "and"},
					{Column: "id", Operator: "IN", Value: []any{1, 2}, Type: "and"},
					{Column: "age", Operator: "BETWEEN", Value: []any{18, 65}, Type: "and"},
				},
			},
			wantSQL:  "SELECT `id` FROM `users` WHERE `tenant_id` = ? AND `id` IN (?, ?) AND `age` BETWEEN ? AND ?",
			wantArgs: []any{7, 1, 2, 18, 65},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileSelect(tt.query)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("CompileSelect() args = %#v, want %#v", gotArgs, tt.wantArgs)
			}
		})
	}
}
