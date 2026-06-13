package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/drivers"
)

func TestSchemaIntrospectionPostgresIntegration(t *testing.T) {
	if os.Getenv("TEST_POSTGRES") != "true" {
		t.Skip("Skipping PostgreSQL introspection integration test (set TEST_POSTGRES=true to run)")
	}

	ctx := context.Background()
	m, err := orm.NewManager(postgresIntrospectionConfig())
	if err != nil {
		t.Fatalf("NewManager(postgres) error = %v", err)
	}
	defer m.Shutdown(ctx)

	const table = "velocity_introspection_pg"
	_, _ = m.Exec(ctx, `DROP TABLE IF EXISTS velocity_introspection_pg`)
	_, err = m.Exec(ctx, `CREATE TABLE velocity_introspection_pg (
		id SERIAL PRIMARY KEY,
		email VARCHAR(255) NOT NULL,
		status TEXT DEFAULT 'active',
		notes TEXT
	)`)
	if err != nil {
		t.Fatalf("create table error = %v", err)
	}
	defer m.Exec(ctx, `DROP TABLE IF EXISTS velocity_introspection_pg`)

	tables, err := m.ListTables(ctx)
	if err != nil {
		t.Fatalf("ListTables() error = %v", err)
	}
	if !containsString(tables, table) {
		t.Fatalf("ListTables() = %#v, want %q", tables, table)
	}

	columns, err := m.DescribeTable(ctx, table)
	if err != nil {
		t.Fatalf("DescribeTable(%q) error = %v", table, err)
	}
	byName := columnsByName(columns)

	id := byName["id"]
	if !id.PrimaryKey || id.Nullable {
		t.Fatalf("id column = %#v, want primary key and not nullable", id)
	}
	email := byName["email"]
	if email.Nullable || email.Default != nil {
		t.Fatalf("email column = %#v, want not nullable with no default", email)
	}
	status := byName["status"]
	if !status.Nullable {
		t.Fatalf("status column = %#v, want nullable", status)
	}
	if !strings.Contains(strings.ToLower(status.DataType), "text") {
		t.Fatalf("status DataType = %q, want text-like PostgreSQL type", status.DataType)
	}
	if status.Default == nil || !strings.Contains(*status.Default, "active") {
		t.Fatalf("status Default = %v, want active default expression", status.Default)
	}

	if _, err := m.DescribeTable(ctx, table+"_missing"); err == nil {
		t.Fatal("DescribeTable(missing) error = nil, want missing table error")
	}
}

func postgresIntrospectionConfig() orm.ManagerConfig {
	return orm.ManagerConfig{
		Driver:   "postgres",
		Host:     envOr("POSTGRES_HOST", "localhost"),
		Port:     envOr("POSTGRES_PORT", "5432"),
		Database: envOr("POSTGRES_DB", "test_db"),
		Username: envOr("POSTGRES_USER", "postgres"),
		Password: envOr("POSTGRES_PASSWORD", "postgres"),
		SSLMode:  envOr("POSTGRES_SSLMODE", "disable"),
	}
}

func columnsByName(columns []drivers.ColumnSchema) map[string]drivers.ColumnSchema {
	byName := make(map[string]drivers.ColumnSchema, len(columns))
	for _, col := range columns {
		byName[col.Name] = col
	}
	return byName
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func containsString(values []string, needle string) bool {
	for _, value := range values {
		if value == needle {
			return true
		}
	}
	return false
}
