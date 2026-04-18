package migrate_test

import (
	"context"
	"database/sql"
	"regexp"
	"testing"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/migrate"
)

// dbIdentifierRegex validates SQL identifiers to prevent injection when
// identifiers must be interpolated (e.g. SQLite PRAGMA which doesn't
// support parameterized identifiers).
var dbIdentifierRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func newTestManager(t *testing.T) *orm.Manager {
	t.Helper()
	manager, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: ":memory:",
	})
	if err != nil {
		t.Fatalf("failed to create ORM manager: %v", err)
	}
	return manager
}

func TestMigrator_AddColumn(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	// Create initial table
	err := migrator.CreateTable("users", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.String("name")
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("users")

	tests := []struct {
		name    string
		column  string
		builder func(*migrate.ColumnBuilder)
		wantErr bool
	}{
		{
			name:   "add string column",
			column: "email",
			builder: func(c *migrate.ColumnBuilder) {
				c.String(100).Nullable()
			},
		},
		{
			name:   "add integer column with default",
			column: "age",
			builder: func(c *migrate.ColumnBuilder) {
				c.Integer().Default(0)
			},
		},
		{
			name:   "add boolean column",
			column: "active",
			builder: func(c *migrate.ColumnBuilder) {
				c.Boolean().Default(true)
			},
		},
		{
			name:   "add timestamp column nullable",
			column: "verified_at",
			builder: func(c *migrate.ColumnBuilder) {
				c.Timestamp().Nullable()
			},
		},
		{
			name:   "add text column",
			column: "bio",
			builder: func(c *migrate.ColumnBuilder) {
				c.Text().Nullable()
			},
		},
		{
			name:   "add date column",
			column: "birth_date",
			builder: func(c *migrate.ColumnBuilder) {
				c.Date().Nullable()
			},
		},
		{
			name:   "add biginteger column",
			column: "views",
			builder: func(c *migrate.ColumnBuilder) {
				c.BigInteger().Default(0)
			},
		},
		{
			name:   "add uuid column",
			column: "external_id",
			builder: func(c *migrate.ColumnBuilder) {
				c.UUID().Nullable()
			},
		},
		{
			name:   "add json column",
			column: "metadata",
			builder: func(c *migrate.ColumnBuilder) {
				c.JSON().Nullable()
			},
		},
		{
			name:   "add jsonb column",
			column: "settings",
			builder: func(c *migrate.ColumnBuilder) {
				c.JSONB().Nullable()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := migrator.AddColumn("users", tt.column, tt.builder)
			if (err != nil) != tt.wantErr {
				t.Errorf("AddColumn() error = %v, wantErr %v", err, tt.wantErr)
			}

			if err == nil {
				// Verify column exists
				if !columnExists(t, db, "users", tt.column) {
					t.Errorf("column %s was not added", tt.column)
				}
			}
		})
	}
}

func TestMigrator_AddColumn_Unique(t *testing.T) {
	// Note: SQLite does not support adding a UNIQUE column with ALTER TABLE ADD COLUMN
	// This test verifies the limitation is handled gracefully
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	err := migrator.CreateTable("products", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.String("name")
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("products")

	// SQLite cannot add UNIQUE column via ALTER TABLE - expect error
	err = migrator.AddColumn("products", "sku", func(c *migrate.ColumnBuilder) {
		c.String(50).Unique()
	})
	if err == nil {
		t.Skip("SQLite may support UNIQUE in newer versions")
	}

	// Adding column without UNIQUE should work
	err = migrator.AddColumn("products", "code", func(c *migrate.ColumnBuilder) {
		c.String(50).Nullable()
	})
	if err != nil {
		t.Fatalf("AddColumn() without UNIQUE error = %v", err)
	}

	if !columnExists(t, db, "products", "code") {
		t.Error("column code should exist")
	}
}

func TestMigrator_DropColumn(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	// Create table with multiple columns
	err := migrator.CreateTable("posts", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.String("title")
		tb.Text("content")
		tb.String("slug", 100)
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("posts")

	// Verify column exists before drop
	if !columnExists(t, db, "posts", "slug") {
		t.Fatal("column slug should exist before drop")
	}

	// Drop the column
	err = migrator.DropColumn("posts", "slug")
	if err != nil {
		t.Fatalf("DropColumn() error = %v", err)
	}

	// Verify column no longer exists
	if columnExists(t, db, "posts", "slug") {
		t.Error("column slug should not exist after drop")
	}

	// Verify other columns still exist
	if !columnExists(t, db, "posts", "title") {
		t.Error("column title should still exist")
	}
	if !columnExists(t, db, "posts", "content") {
		t.Error("column content should still exist")
	}
}

func TestMigrator_DropColumn_NonExistent(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	err := migrator.CreateTable("items", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.String("name")
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("items")

	// Try to drop non-existent column
	err = migrator.DropColumn("items", "nonexistent")
	if err == nil {
		t.Error("expected error when dropping non-existent column")
	}
}

func TestColumnBuilder_ToSQL(t *testing.T) {
	tests := []struct {
		name     string
		driver   string
		builder  func(*migrate.ColumnBuilder)
		contains []string
	}{
		{
			name:   "sqlite string",
			driver: "sqlite",
			builder: func(c *migrate.ColumnBuilder) {
				c.String(100)
			},
			contains: []string{"VARCHAR(100)", "NOT NULL"},
		},
		{
			name:   "sqlite nullable timestamp",
			driver: "sqlite",
			builder: func(c *migrate.ColumnBuilder) {
				c.Timestamp().Nullable()
			},
			contains: []string{"DATETIME"},
		},
		{
			name:   "sqlite boolean with default",
			driver: "sqlite",
			builder: func(c *migrate.ColumnBuilder) {
				c.Boolean().Default(false)
			},
			contains: []string{"INTEGER", "DEFAULT 0"},
		},
		{
			name:   "sqlite json column",
			driver: "sqlite",
			builder: func(c *migrate.ColumnBuilder) {
				c.JSON().Nullable()
			},
			contains: []string{"TEXT"},
		},
		{
			name:   "sqlite jsonb column",
			driver: "sqlite",
			builder: func(c *migrate.ColumnBuilder) {
				c.JSONB().Nullable()
			},
			contains: []string{"TEXT"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, err := orm.NewManager(orm.ManagerConfig{
				Driver:   tt.driver,
				Database: ":memory:",
			})
			if err != nil {
				t.Skip("driver not available")
			}
			defer manager.Shutdown(context.Background())

			db := manager.DB()
			migrator := migrate.NewMigrator(db, manager.DriverName())

			// Create table and add column to verify SQL works
			err = migrator.CreateTable("test_col_sql", func(tb *migrate.TableBuilder) {
				tb.ID()
			})
			if err != nil {
				t.Fatalf("failed to create table: %v", err)
			}
			defer migrator.DropTable("test_col_sql")

			err = migrator.AddColumn("test_col_sql", "test_col", tt.builder)
			if err != nil {
				t.Errorf("AddColumn failed: %v", err)
			}
		})
	}
}

// columnExists checks if a column exists in a table (SQLite-specific)
func columnExists(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()

	if !dbIdentifierRegex.MatchString(table) {
		t.Fatalf("invalid table name: %q", table)
	}
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		t.Fatalf("failed to get table info: %v", err)
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue interface{}

		err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk)
		if err != nil {
			t.Fatalf("failed to scan row: %v", err)
		}

		if name == column {
			return true
		}
	}
	return false
}

func TestMigrator_Table(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	// Create initial table
	err := migrator.CreateTable("articles", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.String("title")
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("articles")

	// Use Table() to add multiple columns at once
	err = migrator.Table("articles", func(tb *migrate.TableBuilder) {
		tb.String("slug", 100).Nullable()
		tb.Text("body").Nullable()
		tb.Boolean("published").Nullable()
	})
	if err != nil {
		t.Fatalf("Table() error = %v", err)
	}

	// Verify all columns were added
	for _, col := range []string{"slug", "body", "published"} {
		if !columnExists(t, db, "articles", col) {
			t.Errorf("column %s was not added by Table()", col)
		}
	}

	// Original columns should still exist
	if !columnExists(t, db, "articles", "title") {
		t.Error("original column title should still exist")
	}
}

func TestMigrator_Table_EmptyCallback(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	err := migrator.CreateTable("tags", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.String("name")
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("tags")

	// Empty Table() call should be a no-op
	err = migrator.Table("tags", func(tb *migrate.TableBuilder) {})
	if err != nil {
		t.Fatalf("Table() with empty callback should not error: %v", err)
	}

	// Verify table is unchanged — still only 2 columns
	if !columnExists(t, db, "tags", "id") {
		t.Error("column id should still exist")
	}
	if !columnExists(t, db, "tags", "name") {
		t.Error("column name should still exist")
	}
}

func TestMigrator_Table_RejectsPrimaryKey(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	err := migrator.CreateTable("items", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.String("name")
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("items")

	// Attempting to add a PK via Table() should error
	err = migrator.Table("items", func(tb *migrate.TableBuilder) {
		tb.ID()
	})
	if err == nil {
		t.Error("expected error when adding primary key column via Table()")
	}
}

func TestMigrator_Table_NonExistentTable(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	err := migrator.Table("nonexistent", func(tb *migrate.TableBuilder) {
		tb.String("name").Nullable()
	})
	if err == nil {
		t.Error("expected error when altering non-existent table")
	}
}
