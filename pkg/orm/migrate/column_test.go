package migrate_test

import (
	"testing"

	"github.com/velocitykode/velocity/pkg/orm"
	"github.com/velocitykode/velocity/pkg/orm/migrate"
)

func TestMigrator_AddColumn(t *testing.T) {
	err := orm.Init("sqlite", map[string]any{
		"database": ":memory:",
	})
	if err != nil {
		t.Fatalf("failed to init ORM: %v", err)
	}
	defer orm.Close()

	migrator := migrate.NewMigrator(orm.DB(), orm.GetDriver())

	// Create initial table
	err = migrator.CreateTable("users", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.String("name")
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("users")

	tests := []struct {
		name     string
		column   string
		builder  func(*migrate.ColumnBuilder)
		wantErr  bool
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
				if !columnExists(t, "users", tt.column) {
					t.Errorf("column %s was not added", tt.column)
				}
			}
		})
	}
}

func TestMigrator_AddColumn_Unique(t *testing.T) {
	// Note: SQLite does not support adding a UNIQUE column with ALTER TABLE ADD COLUMN
	// This test verifies the limitation is handled gracefully
	err := orm.Init("sqlite", map[string]any{
		"database": ":memory:",
	})
	if err != nil {
		t.Fatalf("failed to init ORM: %v", err)
	}
	defer orm.Close()

	migrator := migrate.NewMigrator(orm.DB(), orm.GetDriver())

	err = migrator.CreateTable("products", func(tb *migrate.TableBuilder) {
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

	if !columnExists(t, "products", "code") {
		t.Error("column code should exist")
	}
}

func TestMigrator_DropColumn(t *testing.T) {
	err := orm.Init("sqlite", map[string]any{
		"database": ":memory:",
	})
	if err != nil {
		t.Fatalf("failed to init ORM: %v", err)
	}
	defer orm.Close()

	migrator := migrate.NewMigrator(orm.DB(), orm.GetDriver())

	// Create table with multiple columns
	err = migrator.CreateTable("posts", func(tb *migrate.TableBuilder) {
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
	if !columnExists(t, "posts", "slug") {
		t.Fatal("column slug should exist before drop")
	}

	// Drop the column
	err = migrator.DropColumn("posts", "slug")
	if err != nil {
		t.Fatalf("DropColumn() error = %v", err)
	}

	// Verify column no longer exists
	if columnExists(t, "posts", "slug") {
		t.Error("column slug should not exist after drop")
	}

	// Verify other columns still exist
	if !columnExists(t, "posts", "title") {
		t.Error("column title should still exist")
	}
	if !columnExists(t, "posts", "content") {
		t.Error("column content should still exist")
	}
}

func TestMigrator_DropColumn_NonExistent(t *testing.T) {
	err := orm.Init("sqlite", map[string]any{
		"database": ":memory:",
	})
	if err != nil {
		t.Fatalf("failed to init ORM: %v", err)
	}
	defer orm.Close()

	migrator := migrate.NewMigrator(orm.DB(), orm.GetDriver())

	err = migrator.CreateTable("items", func(tb *migrate.TableBuilder) {
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
			err := orm.Init(tt.driver, map[string]any{
				"database": ":memory:",
			})
			if err != nil {
				t.Skip("driver not available")
			}
			defer orm.Close()

			migrator := migrate.NewMigrator(orm.DB(), orm.GetDriver())

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
func columnExists(t *testing.T, table, column string) bool {
	t.Helper()

	rows, err := orm.DB().Query("PRAGMA table_info(" + table + ")")
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
