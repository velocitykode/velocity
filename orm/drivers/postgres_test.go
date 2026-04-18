package drivers

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
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
	_, err = driver.ExecContext(context.Background(), "INSERT INTO test_users (name, email) VALUES ($1, $2)", "John Doe", "john@example.com")
	if err != nil {
		t.Errorf("Failed to insert: %v", err)
	}

	// Test query
	rows, err := driver.QueryContext(context.Background(), "SELECT id, name, email FROM test_users WHERE email = $1", "john@example.com")
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
	tx, err := driver.BeginTx(context.Background(), nil)
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

// intPtr is defined in driver_test.go

// =============================================================================
// SQL Injection Prevention Tests
// =============================================================================

func TestPostgresDriver_SQLInjectionPrevention(t *testing.T) {
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
	if err := driver.Connect(config); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	// Create test table
	driver.DropTable("injection_test")
	err := driver.CreateTable("injection_test", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INT", AutoIncrement: true, Primary: true},
			{Name: "name", Type: "VARCHAR", Size: 255},
			{Name: "email", Type: "VARCHAR", Size: 255},
		}
	})
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	defer driver.DropTable("injection_test")

	tests := []struct {
		name           string
		maliciousInput string
		wantStored     string
	}{
		{
			name:           "prevents DROP TABLE injection",
			maliciousInput: "'; DROP TABLE injection_test; --",
			wantStored:     "'; DROP TABLE injection_test; --",
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
			maliciousInput: "test; DELETE FROM injection_test;",
			wantStored:     "test; DELETE FROM injection_test;",
		},
		{
			name:           "handles backslash escape attempts",
			maliciousInput: `test\'; DROP TABLE injection_test; --`,
			wantStored:     `test\'; DROP TABLE injection_test; --`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Insert malicious input using parameterized query
			row := driver.QueryRowContext(context.Background(), "INSERT INTO injection_test (name, email) VALUES ($1, $2) RETURNING id", tt.maliciousInput, "test@example.com")
			var id int
			if err := row.Scan(&id); err != nil {
				t.Fatalf("Insert failed: %v", err)
			}

			// Verify table still exists
			if !driver.HasTable("injection_test") {
				t.Fatal("Table 'injection_test' was dropped - SQL injection succeeded!")
			}

			// Verify data was stored correctly (escaped, not executed)
			var name string
			queryRow := driver.QueryRowContext(context.Background(), "SELECT name FROM injection_test WHERE id = $1", id)
			if err := queryRow.Scan(&name); err != nil {
				t.Fatalf("Failed to retrieve inserted data: %v", err)
			}

			if name != tt.wantStored {
				t.Errorf("Data not properly stored: got %q, want %q", name, tt.wantStored)
			}

			// Clean up for next test
			driver.ExecContext(context.Background(), "DELETE FROM injection_test WHERE id = $1", id)
		})
	}
}

func TestPostgresDriver_SQLInjectionInWhereClause(t *testing.T) {
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
	if err := driver.Connect(config); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	// Create and populate test table
	driver.DropTable("auth_test")
	err := driver.CreateTable("auth_test", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INT", AutoIncrement: true, Primary: true},
			{Name: "username", Type: "VARCHAR", Size: 255},
			{Name: "password", Type: "VARCHAR", Size: 255},
		}
	})
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	defer driver.DropTable("auth_test")

	driver.ExecContext(context.Background(), "INSERT INTO auth_test (username, password) VALUES ($1, $2)", "admin", "secret123")
	driver.ExecContext(context.Background(), "INSERT INTO auth_test (username, password) VALUES ($1, $2)", "user1", "password1")

	tests := []struct {
		name              string
		maliciousUsername string
		wantRowCount      int
	}{
		{
			name:              "OR injection does not bypass authentication",
			maliciousUsername: "admin' OR '1'='1",
			wantRowCount:      0,
		},
		{
			name:              "UNION injection does not work",
			maliciousUsername: "admin' UNION SELECT * FROM auth_test--",
			wantRowCount:      0,
		},
		{
			name:              "comment injection does not work",
			maliciousUsername: "admin'--",
			wantRowCount:      0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rows, err := driver.QueryContext(context.Background(), "SELECT * FROM auth_test WHERE username = $1", tt.maliciousUsername)
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

func TestPostgresDriver_TransactionRollback(t *testing.T) {
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
	if err := driver.Connect(config); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	// Create test table
	driver.DropTable("pg_accounts")
	err := driver.CreateTable("pg_accounts", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INT", AutoIncrement: true, Primary: true},
			{Name: "name", Type: "VARCHAR", Size: 255},
			{Name: "balance", Type: "INT"},
		}
	})
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	defer driver.DropTable("pg_accounts")

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
			driver.ExecContext(context.Background(), "DELETE FROM pg_accounts")

			// Setup initial state
			driver.ExecContext(context.Background(), "INSERT INTO pg_accounts (name, balance) VALUES ($1, $2)", "Initial", tt.setupBalance)

			// Begin transaction
			tx, err := driver.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("Begin() error = %v", err)
			}

			// Insert new row in transaction
			_, err = tx.Exec("INSERT INTO pg_accounts (name, balance) VALUES ($1, $2)", tt.insertName, 50)
			if err != nil {
				tx.Rollback()
				t.Fatalf("tx.Exec() error = %v", err)
			}

			// Update existing row in transaction
			_, err = tx.Exec("UPDATE pg_accounts SET balance = balance + 100 WHERE name = $1", "Initial")
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
			row := driver.QueryRowContext(context.Background(), "SELECT balance FROM pg_accounts WHERE name = $1", "Initial")
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
			row = driver.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM pg_accounts WHERE name = $1", tt.insertName)
			row.Scan(&count)
			rowExists := count > 0

			if rowExists != tt.wantRowExists {
				t.Errorf("Row exists = %v, want %v", rowExists, tt.wantRowExists)
			}
		})
	}
}

func TestPostgresDriver_TransactionRollbackNestedOperations(t *testing.T) {
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
	if err := driver.Connect(config); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	// Create multiple tables
	driver.DropTable("pg_order_items")
	driver.DropTable("pg_orders")
	driver.DropTable("pg_inventory")

	err := driver.CreateTable("pg_orders", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INT", AutoIncrement: true, Primary: true},
			{Name: "customer", Type: "VARCHAR", Size: 255},
			{Name: "total", Type: "INT"},
		}
	})
	if err != nil {
		t.Fatalf("Failed to create pg_orders table: %v", err)
	}
	defer driver.DropTable("pg_orders")

	err = driver.CreateTable("pg_order_items", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INT", AutoIncrement: true, Primary: true},
			{Name: "order_id", Type: "INT"},
			{Name: "product", Type: "VARCHAR", Size: 255},
			{Name: "quantity", Type: "INT"},
		}
	})
	if err != nil {
		t.Fatalf("Failed to create pg_order_items table: %v", err)
	}
	defer driver.DropTable("pg_order_items")

	err = driver.CreateTable("pg_inventory", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INT", AutoIncrement: true, Primary: true},
			{Name: "product", Type: "VARCHAR", Size: 255},
			{Name: "stock", Type: "INT"},
		}
	})
	if err != nil {
		t.Fatalf("Failed to create pg_inventory table: %v", err)
	}
	defer driver.DropTable("pg_inventory")

	// Setup initial inventory
	driver.ExecContext(context.Background(), "INSERT INTO pg_inventory (product, stock) VALUES ($1, $2)", "Widget", 100)
	driver.ExecContext(context.Background(), "INSERT INTO pg_inventory (product, stock) VALUES ($1, $2)", "Gadget", 50)

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
			driver.ExecContext(context.Background(), "DELETE FROM pg_order_items")
			driver.ExecContext(context.Background(), "DELETE FROM pg_orders")
			driver.ExecContext(context.Background(), "UPDATE pg_inventory SET stock = 100 WHERE product = $1", "Widget")
			driver.ExecContext(context.Background(), "UPDATE pg_inventory SET stock = 50 WHERE product = $1", "Gadget")

			// Begin transaction
			tx, err := driver.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatalf("Begin() error = %v", err)
			}

			// Create order
			var orderID int
			err = tx.QueryRow("INSERT INTO pg_orders (customer, total) VALUES ($1, $2) RETURNING id", "John", 150).Scan(&orderID)
			if err != nil {
				tx.Rollback()
				t.Fatalf("Failed to insert order: %v", err)
			}

			// Add order items
			_, err = tx.Exec("INSERT INTO pg_order_items (order_id, product, quantity) VALUES ($1, $2, $3)", orderID, "Widget", 5)
			if err != nil {
				tx.Rollback()
				t.Fatalf("Failed to insert order item: %v", err)
			}

			_, err = tx.Exec("INSERT INTO pg_order_items (order_id, product, quantity) VALUES ($1, $2, $3)", orderID, "Gadget", 3)
			if err != nil {
				tx.Rollback()
				t.Fatalf("Failed to insert order item: %v", err)
			}

			// Update inventory
			_, err = tx.Exec("UPDATE pg_inventory SET stock = stock - 5 WHERE product = $1", "Widget")
			if err != nil {
				tx.Rollback()
				t.Fatalf("Failed to update inventory: %v", err)
			}

			_, err = tx.Exec("UPDATE pg_inventory SET stock = stock - 3 WHERE product = $1", "Gadget")
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
			driver.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM pg_orders").Scan(&orderCount)
			if orderCount != tt.wantOrderCount {
				t.Errorf("Order count = %d, want %d", orderCount, tt.wantOrderCount)
			}

			// Verify item count
			var itemCount int
			driver.QueryRowContext(context.Background(), "SELECT COUNT(*) FROM pg_order_items").Scan(&itemCount)
			if itemCount != tt.wantItemCount {
				t.Errorf("Item count = %d, want %d", itemCount, tt.wantItemCount)
			}

			// Verify inventory
			var widgetStock, gadgetStock int
			driver.QueryRowContext(context.Background(), "SELECT stock FROM pg_inventory WHERE product = $1", "Widget").Scan(&widgetStock)
			driver.QueryRowContext(context.Background(), "SELECT stock FROM pg_inventory WHERE product = $1", "Gadget").Scan(&gadgetStock)

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

func TestPostgresGrammar_CompileSelect_ComplexQueries(t *testing.T) {
	grammar := &PostgresGrammar{}

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
			wantSQL:  `SELECT "id", "name" FROM "users"`,
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
			wantSQL:  `SELECT * FROM "users" WHERE "active" = $1`,
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
			wantSQL:  `SELECT "id", "name", "email" FROM "users" WHERE "active" = $1 AND "age" >= $2 AND "role" = $3`,
			wantArgs: []any{true, 18, "admin"},
		},
		{
			name: "compiles select with DISTINCT",
			query: &SelectQuery{
				Table:    "users",
				Columns:  []string{"country"},
				Distinct: true,
			},
			wantSQL:  `SELECT DISTINCT "country" FROM "users"`,
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
			wantSQL:  `SELECT * FROM "users" ORDER BY "created_at" DESC`,
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
			wantSQL:  `SELECT * FROM "users" ORDER BY "last_name" ASC, "first_name" ASC`,
			wantArgs: nil,
		},
		{
			name: "compiles select with LIMIT",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"*"},
				Limit:   intPtr(10),
			},
			wantSQL:  `SELECT * FROM "users" LIMIT 10`,
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
			wantSQL:  `SELECT * FROM "users" LIMIT 10 OFFSET 20`,
			wantArgs: nil,
		},
		{
			name: "compiles select with FOR UPDATE",
			query: &SelectQuery{
				Table:         "users",
				Columns:       []string{"*"},
				LockForUpdate: true,
			},
			wantSQL:  `SELECT * FROM "users" FOR UPDATE`,
			wantArgs: nil,
		},
		{
			name: "compiles select with FOR UPDATE SKIP LOCKED",
			query: &SelectQuery{
				Table:         "jobs",
				Columns:       []string{"*"},
				LockForUpdate: true,
				SkipLocked:    true,
				Limit:         intPtr(1),
			},
			wantSQL:  `SELECT * FROM "jobs" LIMIT 1 FOR UPDATE SKIP LOCKED`,
			wantArgs: nil,
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

func TestPostgresGrammar_CompileSelect_JOINQueries(t *testing.T) {
	grammar := &PostgresGrammar{}

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
			wantSQL:  `SELECT "users.id", "users.name", "roles.name" FROM "users" INNER JOIN "roles" ON users.role_id = roles.id`,
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
			wantSQL:  `SELECT "users.*", "profiles.bio" FROM "users" LEFT JOIN "profiles" ON users.id = profiles.user_id`,
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
			wantSQL:  `SELECT "orders.id", "users.name", "products.title" FROM "orders" INNER JOIN "users" ON orders.user_id = users.id LEFT JOIN "products" ON orders.product_id = products.id`,
			wantArgs: nil,
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

func TestPostgresDriver_ConcurrentReads(t *testing.T) {
	if os.Getenv("TEST_POSTGRES") != "true" {
		t.Skip("Skipping PostgreSQL tests (set TEST_POSTGRES=true to run)")
	}

	config := ConnectionConfig{
		Host:         "localhost",
		Port:         "5432",
		Database:     "test_db",
		Username:     "postgres",
		Password:     "postgres",
		SSLMode:      "disable",
		MaxOpenConns: 20,
	}

	driver := NewPostgresDriver()
	if err := driver.Connect(config); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	// Create and populate table
	driver.DropTable("pg_products")
	err := driver.CreateTable("pg_products", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INT", AutoIncrement: true, Primary: true},
			{Name: "name", Type: "VARCHAR", Size: 255},
			{Name: "price", Type: "DECIMAL"},
		}
	})
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	defer driver.DropTable("pg_products")

	// Insert test data
	for i := 1; i <= 100; i++ {
		driver.ExecContext(context.Background(), "INSERT INTO pg_products (name, price) VALUES ($1, $2)", fmt.Sprintf("Product %d", i), float64(i)*1.5)
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
						row := driver.QueryRowContext(context.Background(), "SELECT id, name, price FROM pg_products WHERE id = $1", productID)
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

func TestPostgresDriver_ConcurrentWrites(t *testing.T) {
	if os.Getenv("TEST_POSTGRES") != "true" {
		t.Skip("Skipping PostgreSQL tests (set TEST_POSTGRES=true to run)")
	}

	config := ConnectionConfig{
		Host:         "localhost",
		Port:         "5432",
		Database:     "test_db",
		Username:     "postgres",
		Password:     "postgres",
		SSLMode:      "disable",
		MaxOpenConns: 20,
	}

	driver := NewPostgresDriver()
	if err := driver.Connect(config); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	// Create table
	driver.DropTable("pg_counters")
	err := driver.CreateTable("pg_counters", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INT", Primary: true},
			{Name: "value", Type: "INT"},
		}
	})
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	defer driver.DropTable("pg_counters")

	// Initialize counter
	driver.ExecContext(context.Background(), "INSERT INTO pg_counters (id, value) VALUES ($1, $2)", 1, 0)

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
			driver.ExecContext(context.Background(), "UPDATE pg_counters SET value = 0 WHERE id = 1")

			var wg sync.WaitGroup
			errors := make(chan error, tt.goroutineCount)

			for i := 0; i < tt.goroutineCount; i++ {
				wg.Add(1)
				go func(workerID int) {
					defer wg.Done()
					for j := 0; j < tt.incrementsPerGo; j++ {
						// PostgreSQL handles concurrent writes better than SQLite
						_, err := driver.ExecContext(context.Background(), "UPDATE pg_counters SET value = value + 1 WHERE id = 1")
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
			driver.QueryRowContext(context.Background(), "SELECT value FROM pg_counters WHERE id = 1").Scan(&finalValue)
			if finalValue != tt.wantFinalValue {
				t.Errorf("Final counter value = %d, want %d", finalValue, tt.wantFinalValue)
			}
		})
	}
}

func TestPostgresDriver_ConcurrentTransactions(t *testing.T) {
	if os.Getenv("TEST_POSTGRES") != "true" {
		t.Skip("Skipping PostgreSQL tests (set TEST_POSTGRES=true to run)")
	}

	config := ConnectionConfig{
		Host:         "localhost",
		Port:         "5432",
		Database:     "test_db",
		Username:     "postgres",
		Password:     "postgres",
		SSLMode:      "disable",
		MaxOpenConns: 30,
	}

	driver := NewPostgresDriver()
	if err := driver.Connect(config); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	// Create table for balance transfers
	driver.DropTable("pg_bank_accounts")
	err := driver.CreateTable("pg_bank_accounts", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INT", Primary: true},
			{Name: "balance", Type: "INT"},
		}
	})
	if err != nil {
		t.Fatalf("Failed to create table: %v", err)
	}
	defer driver.DropTable("pg_bank_accounts")

	// Initialize accounts
	driver.ExecContext(context.Background(), "INSERT INTO pg_bank_accounts (id, balance) VALUES ($1, $2)", 1, 1000)
	driver.ExecContext(context.Background(), "INSERT INTO pg_bank_accounts (id, balance) VALUES ($1, $2)", 2, 1000)

	tests := []struct {
		name             string
		transferCount    int
		wantTotalBalance int
	}{
		{
			name:             "concurrent transfers maintain total balance",
			transferCount:    20,
			wantTotalBalance: 2000,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset balances
			driver.ExecContext(context.Background(), "UPDATE pg_bank_accounts SET balance = 1000 WHERE id IN (1, 2)")

			var wg sync.WaitGroup
			errors := make(chan error, tt.transferCount)

			for i := 0; i < tt.transferCount; i++ {
				wg.Add(1)
				go func(transferID int) {
					defer wg.Done()

					// Alternate transfer direction
					fromID := 1 + (transferID % 2)
					toID := 1 + ((transferID + 1) % 2)
					amount := 10

					tx, err := driver.BeginTx(context.Background(), nil)
					if err != nil {
						errors <- fmt.Errorf("transfer %d: begin error: %v", transferID, err)
						return
					}

					// Debit from source
					_, err = tx.Exec("UPDATE pg_bank_accounts SET balance = balance - $1 WHERE id = $2", amount, fromID)
					if err != nil {
						tx.Rollback()
						errors <- fmt.Errorf("transfer %d: debit error: %v", transferID, err)
						return
					}

					// Credit to destination
					_, err = tx.Exec("UPDATE pg_bank_accounts SET balance = balance + $1 WHERE id = $2", amount, toID)
					if err != nil {
						tx.Rollback()
						errors <- fmt.Errorf("transfer %d: credit error: %v", transferID, err)
						return
					}

					if err := tx.Commit(); err != nil {
						errors <- fmt.Errorf("transfer %d: commit error: %v", transferID, err)
						return
					}
				}(i)
			}

			wg.Wait()
			close(errors)

			for err := range errors {
				t.Error(err)
			}

			// Verify total balance is preserved
			var totalBalance int
			driver.QueryRowContext(context.Background(), "SELECT SUM(balance) FROM pg_bank_accounts").Scan(&totalBalance)
			if totalBalance != tt.wantTotalBalance {
				t.Errorf("Total balance = %d, want %d (money was created or destroyed)", totalBalance, tt.wantTotalBalance)
			}
		})
	}
}
