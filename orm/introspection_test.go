package orm

import (
	"context"
	"testing"
)

func TestManagerSchemaIntrospectionSQLite(t *testing.T) {
	ctx := context.Background()
	m, err := NewManager(ManagerConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer m.Shutdown(ctx)

	stmts := []string{
		`CREATE TABLE users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT NOT NULL,
			status TEXT DEFAULT 'active',
			notes TEXT
		)`,
		`CREATE VIEW user_emails AS SELECT email FROM users`,
	}
	for _, stmt := range stmts {
		if _, err := m.Exec(ctx, stmt); err != nil {
			t.Fatalf("Exec(%q) error = %v", stmt, err)
		}
	}

	tables, err := m.ListTables(ctx)
	if err != nil {
		t.Fatalf("ListTables() error = %v", err)
	}
	if len(tables) != 1 || tables[0] != "users" {
		t.Fatalf("ListTables() = %#v, want [users]", tables)
	}

	columns, err := m.DescribeTable(ctx, "users")
	if err != nil {
		t.Fatalf("DescribeTable() error = %v", err)
	}
	if len(columns) != 4 {
		t.Fatalf("DescribeTable() returned %d columns, want 4: %#v", len(columns), columns)
	}

	if columns[0].Name != "id" || !columns[0].PrimaryKey || columns[0].Nullable {
		t.Fatalf("id column = %#v, want primary key and not nullable", columns[0])
	}
	if columns[1].Name != "email" || columns[1].Nullable || columns[1].Default != nil {
		t.Fatalf("email column = %#v, want not nullable with no default", columns[1])
	}
	if columns[2].Name != "status" || !columns[2].Nullable || columns[2].Default == nil || *columns[2].Default != "'active'" {
		t.Fatalf("status column = %#v, want nullable with default 'active'", columns[2])
	}
	if columns[3].Name != "notes" || !columns[3].Nullable {
		t.Fatalf("notes column = %#v, want nullable", columns[3])
	}
}

func TestManagerSchemaIntrospectionRejectsUnsafeTableName(t *testing.T) {
	ctx := context.Background()
	m, err := NewManager(ManagerConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer m.Shutdown(ctx)

	if _, err := m.DescribeTable(ctx, "users; DROP TABLE users"); err == nil {
		t.Fatal("DescribeTable() error = nil, want invalid identifier error")
	}
}

func TestManagerSchemaIntrospectionMissingTable(t *testing.T) {
	ctx := context.Background()
	m, err := NewManager(ManagerConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	defer m.Shutdown(ctx)

	if _, err := m.DescribeTable(ctx, "missing"); err == nil {
		t.Fatal("DescribeTable() error = nil, want missing table error")
	}
}
