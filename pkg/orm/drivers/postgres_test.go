package drivers

import (
	"os"
	"strings"
	"testing"
)

func TestPostgresDriver(t *testing.T) {
	// Skip if not in CI or PostgreSQL not available
	if os.Getenv("TEST_POSTGRES") != "true" {
		t.Skip("Skipping PostgreSQL tests (set TEST_POSTGRES=true to run)")
	}

	config := ConnectionConfig{
		Host:     "localhost",
		Port:     "5432",
		Database: "test_db",
		Username: "postgres",
		Password: "postgres",
		SSLMode:  "disable",
	}

	driver := NewPostgresDriver()

	// Test connection
	err := driver.Connect(config)
	if err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	defer driver.Close()

	// Test ping
	if err := driver.Ping(); err != nil {
		t.Errorf("Failed to ping: %v", err)
	}

	// Test create table
	err = driver.CreateTable("test_users", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INT", AutoIncrement: true, Primary: true},
			{Name: "name", Type: "VARCHAR", Size: 255, Nullable: false},
			{Name: "email", Type: "VARCHAR", Size: 255, Unique: true},
			{Name: "active", Type: "BOOLEAN", Default: true},
			{Name: "created_at", Type: "TIMESTAMP"},
		}
	})
	if err != nil {
		t.Errorf("Failed to create table: %v", err)
	}

	// Test table exists
	if !driver.HasTable("test_users") {
		t.Error("Table should exist after creation")
	}

	// Test column exists
	if !driver.HasColumn("test_users", "email") {
		t.Error("Column 'email' should exist")
	}

	// Test insert
	_, err = driver.Exec("INSERT INTO test_users (name, email) VALUES ($1, $2)", "John Doe", "john@example.com")
	if err != nil {
		t.Errorf("Failed to insert: %v", err)
	}

	// Test query
	rows, err := driver.Query("SELECT id, name, email FROM test_users WHERE email = $1", "john@example.com")
	if err != nil {
		t.Errorf("Failed to query: %v", err)
	}
	defer rows.Close()

	if !rows.Next() {
		t.Error("Should have found inserted record")
	}

	var id int
	var name, email string
	err = rows.Scan(&id, &name, &email)
	if err != nil {
		t.Errorf("Failed to scan: %v", err)
	}

	if name != "John Doe" || email != "john@example.com" {
		t.Errorf("Unexpected values: name=%s, email=%s", name, email)
	}

	// Test transaction
	tx, err := driver.Begin()
	if err != nil {
		t.Errorf("Failed to begin transaction: %v", err)
	}

	_, err = tx.Exec("UPDATE test_users SET active = false WHERE email = $1", "john@example.com")
	if err != nil {
		tx.Rollback()
		t.Errorf("Failed to update in transaction: %v", err)
	}

	err = tx.Commit()
	if err != nil {
		t.Errorf("Failed to commit transaction: %v", err)
	}

	// Clean up
	err = driver.DropTable("test_users")
	if err != nil {
		t.Errorf("Failed to drop table: %v", err)
	}
}

func TestPostgresGrammar(t *testing.T) {
	grammar := &PostgresGrammar{}

	t.Run("CompileSelect", func(t *testing.T) {
		query := &SelectQuery{
			Table:   "users",
			Columns: []string{"id", "name", "email"},
			Conditions: []Condition{
				{Column: "active", Operator: "=", Value: true, Type: "and"},
				{Column: "role", Operator: "IN", Value: []any{"admin", "user"}, Type: "and"},
			},
			Orders: []Order{
				{Column: "created_at", Direction: "DESC"},
			},
			Limit:  intPtr(10),
			Offset: intPtr(20),
		}

		sql, args := grammar.CompileSelect(query)

		expectedSQL := `SELECT "id", "name", "email" FROM "users" WHERE "active" = $1 AND "role" IN ($2, $3) ORDER BY "created_at" DESC LIMIT 10 OFFSET 20`
		if sql != expectedSQL {
			t.Errorf("Expected SQL:\n%s\nGot:\n%s", expectedSQL, sql)
		}

		if len(args) != 3 {
			t.Errorf("Expected 3 args, got %d", len(args))
		}
	})

	t.Run("CompileInsert", func(t *testing.T) {
		sql, args := grammar.CompileInsert(
			"users",
			[]string{"name", "email", "active"},
			[][]any{
				{"John", "john@example.com", true},
				{"Jane", "jane@example.com", false},
			},
		)

		expectedSQL := `INSERT INTO "users" ("name", "email", "active") VALUES ($1, $2, $3), ($4, $5, $6) RETURNING id`
		if sql != expectedSQL {
			t.Errorf("Expected SQL:\n%s\nGot:\n%s", expectedSQL, sql)
		}

		if len(args) != 6 {
			t.Errorf("Expected 6 args, got %d", len(args))
		}
	})

	t.Run("CompileUpdate", func(t *testing.T) {
		sql, args := grammar.CompileUpdate(
			"users",
			map[string]any{
				"name":       "Updated Name",
				"updated_at": "NOW()",
			},
			[]Condition{
				{Column: "id", Operator: "=", Value: 1, Type: "and"},
			},
		)

		// Verify SQL contains expected parts (map iteration order varies)
		if !strings.Contains(sql, "UPDATE") || !strings.Contains(sql, "WHERE") {
			t.Errorf("SQL missing expected parts: %s", sql)
		}

		// Note: map iteration order is not guaranteed
		if len(args) != 2 { // name and id (updated_at uses NOW())
			t.Errorf("Expected 2 args, got %d", len(args))
		}
	})

	t.Run("QuoteIdentifier", func(t *testing.T) {
		quoted := grammar.QuoteIdentifier("table_name")
		if quoted != `"table_name"` {
			t.Errorf("Expected quoted identifier to be \"table_name\", got %s", quoted)
		}
	})

	t.Run("Placeholder", func(t *testing.T) {
		placeholder := grammar.Placeholder(5)
		if placeholder != "$5" {
			t.Errorf("Expected placeholder $5, got %s", placeholder)
		}
	})
}

func intPtr(i int) *int {
	return &i
}
