package migrate_test

import (
	"strings"
	"testing"

	"github.com/velocitykode/velocity/pkg/orm"
	"github.com/velocitykode/velocity/pkg/orm/migrate"
)

func TestTableBuilder_SoftDeletes(t *testing.T) {
	tests := []struct {
		name   string
		driver string
	}{
		{"sqlite", "sqlite"},
		{"postgres", "postgres"},
		{"mysql", "mysql"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Skip postgres/mysql if not available - just test SQLite for now
			if tt.driver != "sqlite" {
				t.Skip("skipping non-sqlite driver test")
			}

			err := orm.Init("sqlite", map[string]any{
				"database": ":memory:",
			})
			if err != nil {
				t.Fatalf("failed to init ORM: %v", err)
			}
			defer orm.Close()

			migrator := migrate.NewMigrator(orm.DB(), orm.GetDriver())

			// Create table with SoftDeletes
			err = migrator.CreateTable("test_soft_deletes", func(t *migrate.TableBuilder) {
				t.ID()
				t.String("name")
				t.Timestamps()
				t.SoftDeletes()
			})
			if err != nil {
				t.Fatalf("failed to create table: %v", err)
			}

			// Verify deleted_at column exists and is nullable
			db := orm.DB()
			rows, err := db.Query("PRAGMA table_info(test_soft_deletes)")
			if err != nil {
				t.Fatalf("failed to get table info: %v", err)
			}
			defer rows.Close()

			foundDeletedAt := false
			for rows.Next() {
				var cid int
				var name, colType string
				var notNull, pk int
				var dfltValue interface{}

				err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk)
				if err != nil {
					t.Fatalf("failed to scan row: %v", err)
				}

				if name == "deleted_at" {
					foundDeletedAt = true
					// notNull should be 0 (nullable)
					if notNull != 0 {
						t.Errorf("deleted_at should be nullable, got notNull=%d", notNull)
					}
				}
			}

			if !foundDeletedAt {
				t.Error("deleted_at column not found in table")
			}

			// Cleanup
			err = migrator.DropTable("test_soft_deletes")
			if err != nil {
				t.Errorf("failed to drop table: %v", err)
			}
		})
	}
}

func TestTableBuilder_SoftDeletes_SQL(t *testing.T) {
	tests := []struct {
		name     string
		driver   string
		contains string
	}{
		{"sqlite", "sqlite", "deleted_at DATETIME"},
		{"postgres", "postgres", "deleted_at TIMESTAMP"},
		{"mysql", "mysql", "deleted_at TIMESTAMP"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := orm.Init(tt.driver, map[string]any{
				"database": ":memory:",
			})
			if err != nil && tt.driver == "sqlite" {
				t.Fatalf("failed to init ORM: %v", err)
			}
			if err != nil {
				t.Skip("driver not available")
			}
			defer orm.Close()

			migrator := migrate.NewMigrator(orm.DB(), orm.GetDriver())

			// Use CreateTableSQL to get the SQL without executing
			var generatedSQL string
			err = migrator.CreateTable("soft_delete_test", func(tb *migrate.TableBuilder) {
				tb.ID()
				tb.String("name")
				tb.Timestamps()
				tb.SoftDeletes()
				generatedSQL = tb.ToSQL()
			})

			if !strings.Contains(generatedSQL, tt.contains) {
				t.Errorf("expected SQL to contain %q, got:\n%s", tt.contains, generatedSQL)
			}

			// Cleanup
			migrator.DropTable("soft_delete_test")
		})
	}
}

func TestTableBuilder_AllColumns(t *testing.T) {
	err := orm.Init("sqlite", map[string]any{
		"database": ":memory:",
	})
	if err != nil {
		t.Fatalf("failed to init ORM: %v", err)
	}
	defer orm.Close()

	migrator := migrate.NewMigrator(orm.DB(), orm.GetDriver())

	// Create table with all column types
	err = migrator.CreateTable("full_model", func(t *migrate.TableBuilder) {
		t.ID()
		t.String("name")
		t.String("code", 10)
		t.Integer("count")
		t.Boolean("active")
		t.Timestamps()
		t.SoftDeletes()
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	// Verify all columns exist
	db := orm.DB()
	rows, err := db.Query("PRAGMA table_info(full_model)")
	if err != nil {
		t.Fatalf("failed to get table info: %v", err)
	}
	defer rows.Close()

	expectedColumns := map[string]bool{
		"id":         false,
		"name":       false,
		"code":       false,
		"count":      false,
		"active":     false,
		"created_at": false,
		"updated_at": false,
		"deleted_at": false,
	}

	for rows.Next() {
		var cid int
		var name, colType string
		var notNull, pk int
		var dfltValue interface{}

		err := rows.Scan(&cid, &name, &colType, &notNull, &dfltValue, &pk)
		if err != nil {
			t.Fatalf("failed to scan row: %v", err)
		}

		if _, ok := expectedColumns[name]; ok {
			expectedColumns[name] = true
		}
	}

	for col, found := range expectedColumns {
		if !found {
			t.Errorf("expected column %q not found", col)
		}
	}

	migrator.DropTable("full_model")
}
