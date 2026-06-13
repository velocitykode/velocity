package mysql

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/drivers"
)

func TestSchemaIntrospectionMySQLIntegration(t *testing.T) {
	if os.Getenv("TEST_MYSQL") != "true" {
		t.Skip("Skipping MySQL introspection integration test (set TEST_MYSQL=true to run)")
	}

	ctx := context.Background()
	m, err := orm.NewManager(mysqlIntrospectionConfig())
	if err != nil {
		t.Fatalf("NewManager(mysql) error = %v", err)
	}
	defer m.Shutdown(ctx)

	const table = "velocity_introspection_mysql"
	_, _ = m.Exec(ctx, `DROP TABLE IF EXISTS velocity_introspection_mysql`)
	_, err = m.Exec(ctx, `CREATE TABLE velocity_introspection_mysql (
		id INT AUTO_INCREMENT PRIMARY KEY,
		email VARCHAR(255) NOT NULL,
		status VARCHAR(32) DEFAULT 'active',
		notes TEXT NULL
	)`)
	if err != nil {
		t.Fatalf("create table error = %v", err)
	}
	defer m.Exec(ctx, `DROP TABLE IF EXISTS velocity_introspection_mysql`)

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
	if !strings.Contains(strings.ToLower(status.DataType), "varchar") {
		t.Fatalf("status DataType = %q, want varchar-like MySQL type", status.DataType)
	}
	if status.Default == nil || *status.Default != "active" {
		t.Fatalf("status Default = %v, want active", status.Default)
	}

	if _, err := m.DescribeTable(ctx, table+"_missing"); err == nil {
		t.Fatal("DescribeTable(missing) error = nil, want missing table error")
	}
}

func mysqlIntrospectionConfig() orm.ManagerConfig {
	return orm.ManagerConfig{
		Driver:   "mysql",
		Host:     envOr("MYSQL_HOST", "localhost"),
		Port:     envOr("MYSQL_PORT", "3306"),
		Database: envOr("MYSQL_DB", "test_db"),
		Username: envOr("MYSQL_USER", "root"),
		Password: envOr("MYSQL_PASSWORD", "root"),
		TLS:      envOr("MYSQL_TLS", "false"),
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
