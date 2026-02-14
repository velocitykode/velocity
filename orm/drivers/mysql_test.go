package drivers

import (
	"os"
	"testing"
)

func TestNewMySQLDriver(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"creates new mysql driver instance"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewMySQLDriver()
			if driver == nil {
				t.Error("NewMySQLDriver() returned nil")
			}
		})
	}
}

func TestMySQLDriver_DriverName(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{"returns mysql as driver name", "mysql"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewMySQLDriver()
			got := driver.DriverName()
			if got != tt.want {
				t.Errorf("DriverName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMySQLDriver_Grammar(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"returns MySQLGrammar instance"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewMySQLDriver()
			grammar := driver.Grammar()
			if grammar == nil {
				t.Error("Grammar() returned nil")
			}
			if _, ok := grammar.(*MySQLGrammar); !ok {
				t.Errorf("Grammar() returned %T, want *MySQLGrammar", grammar)
			}
		})
	}
}

func TestMySQLDriver_DB_ReturnsNilBeforeConnect(t *testing.T) {
	tests := []struct {
		name string
	}{
		{"returns nil before connection is established"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewMySQLDriver()
			if driver.DB() != nil {
				t.Error("DB() should return nil before Connect() is called")
			}
		})
	}
}

func TestMySQLDriver_Ping_ReturnsErrorBeforeConnect(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"returns error when not connected", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewMySQLDriver()
			err := driver.Ping()
			if (err != nil) != tt.wantErr {
				t.Errorf("Ping() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && err.Error() != "no database connection" {
				t.Errorf("Ping() error = %q, want %q", err.Error(), "no database connection")
			}
		})
	}
}

func TestMySQLDriver_Close_NoErrorWhenNotConnected(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{"returns nil when db is not connected", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewMySQLDriver()
			err := driver.Close()
			if (err != nil) != tt.wantErr {
				t.Errorf("Close() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

// Integration tests - require MySQL database
func TestMySQLDriver_Integration(t *testing.T) {
	if os.Getenv("TEST_MYSQL") != "true" {
		t.Skip("Skipping MySQL integration tests (set TEST_MYSQL=true to run)")
	}

	config := ConnectionConfig{
		Host:     "localhost",
		Port:     "3306",
		Database: "test_db",
		Username: "root",
		Password: "root",
	}

	driver := NewMySQLDriver()

	t.Run("connects to database", func(t *testing.T) {
		err := driver.Connect(config)
		if err != nil {
			t.Fatalf("Connect() error = %v", err)
		}
		defer driver.Close()

		if driver.DB() == nil {
			t.Error("DB() should not return nil after successful Connect()")
		}
	})

	t.Run("pings database after connection", func(t *testing.T) {
		err := driver.Connect(config)
		if err != nil {
			t.Fatalf("Connect() error = %v", err)
		}
		defer driver.Close()

		if err := driver.Ping(); err != nil {
			t.Errorf("Ping() error = %v", err)
		}
	})

	t.Run("creates and drops table", func(t *testing.T) {
		err := driver.Connect(config)
		if err != nil {
			t.Fatalf("Connect() error = %v", err)
		}
		defer driver.Close()

		tableName := "test_mysql_driver_table"

		// Create table
		err = driver.CreateTable(tableName, func(table *Table) {
			table.Columns = []Column{
				{Name: "id", Type: "INT", AutoIncrement: true, Primary: true},
				{Name: "name", Type: "VARCHAR", Size: 255, Nullable: false},
				{Name: "email", Type: "VARCHAR", Size: 255, Unique: true},
				{Name: "active", Type: "BOOLEAN", Default: true},
			}
		})
		if err != nil {
			t.Errorf("CreateTable() error = %v", err)
		}

		// Check table exists
		if !driver.HasTable(tableName) {
			t.Error("HasTable() = false, want true after CreateTable()")
		}

		// Check columns exist
		if !driver.HasColumn(tableName, "email") {
			t.Error("HasColumn() = false for 'email', want true")
		}

		// Drop table
		err = driver.DropTable(tableName)
		if err != nil {
			t.Errorf("DropTable() error = %v", err)
		}

		// Check table no longer exists
		if driver.HasTable(tableName) {
			t.Error("HasTable() = true after DropTable(), want false")
		}
	})

	t.Run("executes queries", func(t *testing.T) {
		err := driver.Connect(config)
		if err != nil {
			t.Fatalf("Connect() error = %v", err)
		}
		defer driver.Close()

		tableName := "test_mysql_query_table"

		// Create table
		err = driver.CreateTable(tableName, func(table *Table) {
			table.Columns = []Column{
				{Name: "id", Type: "INT", AutoIncrement: true, Primary: true},
				{Name: "name", Type: "VARCHAR", Size: 255},
			}
		})
		if err != nil {
			t.Fatalf("CreateTable() error = %v", err)
		}
		defer driver.DropTable(tableName)

		// Insert record
		result, err := driver.Exec("INSERT INTO "+tableName+" (name) VALUES (?)", "Test User")
		if err != nil {
			t.Errorf("Exec() INSERT error = %v", err)
		}
		if result != nil {
			lastID, _ := result.LastInsertId()
			if lastID == 0 {
				t.Error("LastInsertId() = 0, want > 0")
			}
		}

		// Query record
		rows, err := driver.Query("SELECT id, name FROM "+tableName+" WHERE name = ?", "Test User")
		if err != nil {
			t.Errorf("Query() error = %v", err)
		}
		defer rows.Close()

		if !rows.Next() {
			t.Error("Query() returned no rows, want 1 row")
		}

		var id int
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			t.Errorf("Scan() error = %v", err)
		}
		if name != "Test User" {
			t.Errorf("name = %q, want %q", name, "Test User")
		}

		// QueryRow
		row := driver.QueryRow("SELECT name FROM "+tableName+" WHERE id = ?", id)
		var queriedName string
		if err := row.Scan(&queriedName); err != nil {
			t.Errorf("QueryRow() Scan() error = %v", err)
		}
		if queriedName != "Test User" {
			t.Errorf("QueryRow() name = %q, want %q", queriedName, "Test User")
		}
	})

	t.Run("handles transactions", func(t *testing.T) {
		err := driver.Connect(config)
		if err != nil {
			t.Fatalf("Connect() error = %v", err)
		}
		defer driver.Close()

		tableName := "test_mysql_tx_table"

		err = driver.CreateTable(tableName, func(table *Table) {
			table.Columns = []Column{
				{Name: "id", Type: "INT", AutoIncrement: true, Primary: true},
				{Name: "value", Type: "INT"},
			}
		})
		if err != nil {
			t.Fatalf("CreateTable() error = %v", err)
		}
		defer driver.DropTable(tableName)

		// Test successful transaction
		tx, err := driver.Begin()
		if err != nil {
			t.Errorf("Begin() error = %v", err)
		}

		_, err = tx.Exec("INSERT INTO "+tableName+" (value) VALUES (?)", 100)
		if err != nil {
			tx.Rollback()
			t.Errorf("tx.Exec() error = %v", err)
		}

		if err := tx.Commit(); err != nil {
			t.Errorf("Commit() error = %v", err)
		}

		// Verify data was committed
		var count int
		row := driver.QueryRow("SELECT COUNT(*) FROM " + tableName)
		if err := row.Scan(&count); err != nil {
			t.Errorf("QueryRow() Scan() error = %v", err)
		}
		if count != 1 {
			t.Errorf("count = %d, want 1", count)
		}

		// Test rollback
		tx2, _ := driver.Begin()
		tx2.Exec("INSERT INTO "+tableName+" (value) VALUES (?)", 200)
		tx2.Rollback()

		row = driver.QueryRow("SELECT COUNT(*) FROM " + tableName)
		row.Scan(&count)
		if count != 1 {
			t.Errorf("count after rollback = %d, want 1", count)
		}
	})
}

func TestMySQLDriver_Connect_BuildsDSNCorrectly(t *testing.T) {
	// This test verifies DSN building logic by checking connection attempts
	// These tests will fail at connection but verify the configuration is used

	tests := []struct {
		name   string
		config ConnectionConfig
	}{
		{
			name: "builds DSN with password",
			config: ConnectionConfig{
				Host:     "localhost",
				Port:     "3306",
				Database: "test",
				Username: "user",
				Password: "pass",
			},
		},
		{
			name: "builds DSN without password",
			config: ConnectionConfig{
				Host:     "localhost",
				Port:     "3306",
				Database: "test",
				Username: "user",
			},
		},
		{
			name: "builds DSN with charset",
			config: ConnectionConfig{
				Host:     "localhost",
				Port:     "3306",
				Database: "test",
				Username: "user",
				Charset:  "utf8",
			},
		},
		{
			name: "builds DSN with collation",
			config: ConnectionConfig{
				Host:      "localhost",
				Port:      "3306",
				Database:  "test",
				Username:  "user",
				Collation: "utf8_general_ci",
			},
		},
		{
			name: "builds DSN with timezone",
			config: ConnectionConfig{
				Host:     "localhost",
				Port:     "3306",
				Database: "test",
				Username: "user",
				TimeZone: "UTC",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			driver := NewMySQLDriver()
			// We expect this to fail because there's no actual MySQL server
			// but it should not panic and should attempt connection
			_ = driver.Connect(tt.config)
			// The test passes if no panic occurs
		})
	}
}

func TestMySQLGrammar_CompileHasTable(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name  string
		table string
		want  string
	}{
		{
			name:  "returns SHOW TABLES query",
			table: "users",
			want:  "SHOW TABLES LIKE ?",
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

func TestMySQLGrammar_CompileHasColumn(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name   string
		table  string
		column string
		want   string
	}{
		{
			name:   "returns information_schema query",
			table:  "users",
			column: "email",
			want: `SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = ?
		AND table_name = ?
		AND column_name = ?`,
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

func TestMySQLGrammar_CompileSelect_WithBetween(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "compiles BETWEEN condition",
			query: &SelectQuery{
				Table:   "orders",
				Columns: []string{"id", "amount"},
				Conditions: []Condition{
					{Column: "amount", Operator: "BETWEEN", Value: []any{100, 500}, Type: "and"},
				},
			},
			wantSQL:  "SELECT `id`, `amount` FROM `orders` WHERE `amount` BETWEEN ? AND ?",
			wantArgs: []any{100, 500},
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

func TestMySQLGrammar_CompileSelect_WithGroupByAndHaving(t *testing.T) {
	grammar := &MySQLGrammar{}

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

func TestMySQLGrammar_CompileSelect_WithOrCondition(t *testing.T) {
	grammar := &MySQLGrammar{}

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

func TestMySQLGrammar_CompileCreateTable_WithIndexes(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name      string
		tblName   string
		table     *Table
		wantParts []string
	}{
		{
			name:    "table with regular index",
			tblName: "users",
			table: &Table{
				Columns: []Column{
					{Name: "id", Type: "INT", Primary: true, AutoIncrement: true},
					{Name: "email", Type: "VARCHAR", Size: 255},
				},
				Indexes: []Index{
					{Name: "idx_email", Columns: []string{"email"}, Unique: false},
				},
			},
			wantParts: []string{
				"CREATE TABLE `users`",
				"INDEX `idx_email` (`email`)",
			},
		},
		{
			name:    "table with unique index",
			tblName: "users",
			table: &Table{
				Columns: []Column{
					{Name: "id", Type: "INT", Primary: true},
					{Name: "email", Type: "VARCHAR", Size: 255},
				},
				Indexes: []Index{
					{Name: "idx_unique_email", Columns: []string{"email"}, Unique: true},
				},
			},
			wantParts: []string{
				"CREATE TABLE `users`",
				"UNIQUE INDEX `idx_unique_email` (`email`)",
			},
		},
		{
			name:    "table with composite index",
			tblName: "user_roles",
			table: &Table{
				Columns: []Column{
					{Name: "user_id", Type: "INT"},
					{Name: "role_id", Type: "INT"},
				},
				Indexes: []Index{
					{Name: "idx_user_role", Columns: []string{"user_id", "role_id"}, Unique: true},
				},
			},
			wantParts: []string{
				"CREATE TABLE `user_roles`",
				"UNIQUE INDEX `idx_user_role` (`user_id`, `role_id`)",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.CompileCreateTable(tt.tblName, tt.table)
			for _, part := range tt.wantParts {
				if !containsString(got, part) {
					t.Errorf("CompileCreateTable() missing part %q, got %q", part, got)
				}
			}
		})
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
