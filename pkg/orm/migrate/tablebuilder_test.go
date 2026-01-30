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
		t.Text("bio")
		t.Integer("count")
		t.BigInteger("views")
		t.Boolean("active")
		t.Timestamp("verified_at").Nullable()
		t.Date("birth_date").Nullable()
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
		"id":          false,
		"name":        false,
		"code":        false,
		"bio":         false,
		"count":       false,
		"views":       false,
		"active":      false,
		"verified_at": false,
		"birth_date":  false,
		"created_at":  false,
		"updated_at":  false,
		"deleted_at":  false,
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

func TestTableBuilder_NewColumnTypes(t *testing.T) {
	err := orm.Init("sqlite", map[string]any{
		"database": ":memory:",
	})
	if err != nil {
		t.Fatalf("failed to init ORM: %v", err)
	}
	defer orm.Close()

	migrator := migrate.NewMigrator(orm.DB(), orm.GetDriver())

	// Test Text column
	t.Run("Text", func(t *testing.T) {
		err := migrator.CreateTable("text_test", func(tb *migrate.TableBuilder) {
			tb.ID()
			tb.Text("content")
		})
		if err != nil {
			t.Fatalf("failed to create table: %v", err)
		}
		defer migrator.DropTable("text_test")

		// Insert and retrieve text data
		db := orm.DB()
		longText := strings.Repeat("a", 1000)
		_, err = db.Exec("INSERT INTO text_test (content) VALUES (?)", longText)
		if err != nil {
			t.Fatalf("failed to insert: %v", err)
		}

		var retrieved string
		err = db.QueryRow("SELECT content FROM text_test WHERE id = 1").Scan(&retrieved)
		if err != nil {
			t.Fatalf("failed to query: %v", err)
		}
		if retrieved != longText {
			t.Errorf("text content mismatch")
		}
	})

	// Test BigInteger column
	t.Run("BigInteger", func(t *testing.T) {
		err := migrator.CreateTable("bigint_test", func(tb *migrate.TableBuilder) {
			tb.ID()
			tb.BigInteger("big_number")
		})
		if err != nil {
			t.Fatalf("failed to create table: %v", err)
		}
		defer migrator.DropTable("bigint_test")

		db := orm.DB()
		bigNum := int64(9223372036854775807) // Max int64
		_, err = db.Exec("INSERT INTO bigint_test (big_number) VALUES (?)", bigNum)
		if err != nil {
			t.Fatalf("failed to insert: %v", err)
		}

		var retrieved int64
		err = db.QueryRow("SELECT big_number FROM bigint_test WHERE id = 1").Scan(&retrieved)
		if err != nil {
			t.Fatalf("failed to query: %v", err)
		}
		if retrieved != bigNum {
			t.Errorf("expected %d, got %d", bigNum, retrieved)
		}
	})

	// Test Date column
	t.Run("Date", func(t *testing.T) {
		err := migrator.CreateTable("date_test", func(tb *migrate.TableBuilder) {
			tb.ID()
			tb.Date("birth_date").Nullable()
		})
		if err != nil {
			t.Fatalf("failed to create table: %v", err)
		}
		defer migrator.DropTable("date_test")

		db := orm.DB()
		_, err = db.Exec("INSERT INTO date_test (birth_date) VALUES (?)", "2000-01-15")
		if err != nil {
			t.Fatalf("failed to insert: %v", err)
		}

		var retrieved string
		err = db.QueryRow("SELECT birth_date FROM date_test WHERE id = 1").Scan(&retrieved)
		if err != nil {
			t.Fatalf("failed to query: %v", err)
		}
		if !strings.Contains(retrieved, "2000-01-15") {
			t.Errorf("expected date containing 2000-01-15, got %s", retrieved)
		}
	})

	// Test single Timestamp column
	t.Run("Timestamp", func(t *testing.T) {
		err := migrator.CreateTable("ts_test", func(tb *migrate.TableBuilder) {
			tb.ID()
			tb.Timestamp("verified_at").Nullable()
		})
		if err != nil {
			t.Fatalf("failed to create table: %v", err)
		}
		defer migrator.DropTable("ts_test")

		db := orm.DB()
		_, err = db.Exec("INSERT INTO ts_test (verified_at) VALUES (?)", "2024-01-15 10:30:00")
		if err != nil {
			t.Fatalf("failed to insert: %v", err)
		}

		var retrieved string
		err = db.QueryRow("SELECT verified_at FROM ts_test WHERE id = 1").Scan(&retrieved)
		if err != nil {
			t.Fatalf("failed to query: %v", err)
		}
		if !strings.Contains(retrieved, "2024-01-15") {
			t.Errorf("expected timestamp containing 2024-01-15, got %s", retrieved)
		}
	})

	// Test UUID column
	t.Run("UUID", func(t *testing.T) {
		err := migrator.CreateTable("uuid_col_test", func(tb *migrate.TableBuilder) {
			tb.ID()
			tb.UUID("external_id").Unique()
		})
		if err != nil {
			t.Fatalf("failed to create table: %v", err)
		}
		defer migrator.DropTable("uuid_col_test")

		db := orm.DB()
		testUUID := "550e8400-e29b-41d4-a716-446655440000"
		_, err = db.Exec("INSERT INTO uuid_col_test (external_id) VALUES (?)", testUUID)
		if err != nil {
			t.Fatalf("failed to insert: %v", err)
		}

		var retrieved string
		err = db.QueryRow("SELECT external_id FROM uuid_col_test WHERE id = 1").Scan(&retrieved)
		if err != nil {
			t.Fatalf("failed to query: %v", err)
		}
		if retrieved != testUUID {
			t.Errorf("expected %s, got %s", testUUID, retrieved)
		}
	})

	// Test UUIDPrimary column
	t.Run("UUIDPrimary", func(t *testing.T) {
		err := migrator.CreateTable("uuid_pk_test", func(tb *migrate.TableBuilder) {
			tb.UUIDPrimary()
			tb.String("name")
		})
		if err != nil {
			t.Fatalf("failed to create table: %v", err)
		}
		defer migrator.DropTable("uuid_pk_test")

		db := orm.DB()
		testUUID := "a0eebc99-9c0b-4ef8-bb6d-6bb9bd380a11"
		_, err = db.Exec("INSERT INTO uuid_pk_test (id, name) VALUES (?, ?)", testUUID, "Test")
		if err != nil {
			t.Fatalf("failed to insert: %v", err)
		}

		var retrievedID, retrievedName string
		err = db.QueryRow("SELECT id, name FROM uuid_pk_test WHERE id = ?", testUUID).Scan(&retrievedID, &retrievedName)
		if err != nil {
			t.Fatalf("failed to query: %v", err)
		}
		if retrievedID != testUUID {
			t.Errorf("expected ID %s, got %s", testUUID, retrievedID)
		}
		if retrievedName != "Test" {
			t.Errorf("expected name Test, got %s", retrievedName)
		}
	})
}
