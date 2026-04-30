package migrate_test

import (
	"context"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/migrate"
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

			manager := newTestManager(t)
			defer manager.Shutdown(context.Background())

			db := manager.DB()
			migrator := migrate.NewMigrator(db, manager.DriverName())

			// Create table with SoftDeletes
			err := migrator.CreateTable("test_soft_deletes", func(t *migrate.TableBuilder) {
				t.ID()
				t.String("name")
				t.Timestamps()
				t.SoftDeletes()
			})
			if err != nil {
				t.Fatalf("failed to create table: %v", err)
			}

			// Verify deleted_at column exists and is nullable
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
		{"sqlite", "sqlite", "`deleted_at` DATETIME"},
		{"postgres", "postgres", `"deleted_at" TIMESTAMP`},
		{"mysql", "mysql", "`deleted_at` TIMESTAMP"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, err := orm.NewManager(orm.ManagerConfig{
				Driver:   tt.driver,
				Database: ":memory:",
			})
			if err != nil && tt.driver == "sqlite" {
				t.Fatalf("failed to init ORM: %v", err)
			}
			if err != nil {
				t.Skip("driver not available")
			}
			defer manager.Shutdown(context.Background())

			db := manager.DB()
			migrator := migrate.NewMigrator(db, manager.DriverName())

			// Use CreateTableSQL to get the SQL without executing
			var generatedSQL string
			if err := migrator.CreateTable("soft_delete_test", func(tb *migrate.TableBuilder) {
				tb.ID()
				tb.String("name")
				tb.Timestamps()
				tb.SoftDeletes()
				generatedSQL = tb.ToSQL()
			}); err != nil {
				t.Fatalf("CreateTable failed: %v", err)
			}

			if !strings.Contains(generatedSQL, tt.contains) {
				t.Errorf("expected SQL to contain %q, got:\n%s", tt.contains, generatedSQL)
			}

			// Cleanup
			migrator.DropTable("soft_delete_test")
		})
	}
}

func TestTableBuilder_AllColumns(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	// Create table with all column types
	err := migrator.CreateTable("full_model", func(t *migrate.TableBuilder) {
		t.ID()
		t.String("name")
		t.String("code", 10)
		t.Text("bio")
		t.Integer("count")
		t.BigInteger("views")
		t.Boolean("active")
		t.JSON("metadata")
		t.JSONB("settings").Nullable()
		t.Timestamp("verified_at").Nullable()
		t.Date("birth_date").Nullable()
		t.Timestamps()
		t.SoftDeletes()
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	// Verify all columns exist
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
		"metadata":    false,
		"settings":    false,
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
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

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

func TestTableBuilder_IP(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	var generatedSQL string
	err := migrator.CreateTable("servers", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.IP("ip_address").Nullable()
		tb.IP("private_ip").Nullable()
		generatedSQL = tb.ToSQL()
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("servers")

	// IP should be VARCHAR(45) to support IPv6
	if !strings.Contains(generatedSQL, "`ip_address` VARCHAR(45)") {
		t.Errorf("expected VARCHAR(45) for IP column, got:\n%s", generatedSQL)
	}

	// Test storing IPv4 and IPv6 addresses
	_, err = db.Exec("INSERT INTO servers (ip_address, private_ip) VALUES (?, ?)", "192.168.1.1", "2001:0db8:85a3:0000:0000:8a2e:0370:7334")
	if err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	var ipv4, ipv6 string
	err = db.QueryRow("SELECT ip_address, private_ip FROM servers WHERE id = 1").Scan(&ipv4, &ipv6)
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	if ipv4 != "192.168.1.1" {
		t.Errorf("expected 192.168.1.1, got %s", ipv4)
	}
	if ipv6 != "2001:0db8:85a3:0000:0000:8a2e:0370:7334" {
		t.Errorf("expected IPv6 address, got %s", ipv6)
	}
}

func TestTableBuilder_Decimal(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	var generatedSQL string
	err := migrator.CreateTable("metrics", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.Decimal("cpu_percent", 5, 2)
		tb.Decimal("load_avg", 6, 2)
		generatedSQL = tb.ToSQL()
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("metrics")

	// Decimal should be NUMERIC(precision, scale)
	if !strings.Contains(generatedSQL, "`cpu_percent` NUMERIC(5,2)") {
		t.Errorf("expected NUMERIC(5,2) for decimal column, got:\n%s", generatedSQL)
	}

	// Test storing decimal values
	_, err = db.Exec("INSERT INTO metrics (cpu_percent, load_avg) VALUES (?, ?)", 75.55, 123.45)
	if err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	var cpu, load float64
	err = db.QueryRow("SELECT cpu_percent, load_avg FROM metrics WHERE id = 1").Scan(&cpu, &load)
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	if cpu != 75.55 {
		t.Errorf("expected 75.55, got %f", cpu)
	}
}

func TestTableBuilder_JSON(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	var generatedSQL string
	err := migrator.CreateTable("json_test", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.JSON("metadata")
		tb.JSONB("settings").Nullable()
		generatedSQL = tb.ToSQL()
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("json_test")

	// SQLite maps JSON/JSONB to TEXT
	if !strings.Contains(generatedSQL, "`metadata` TEXT") {
		t.Errorf("expected TEXT for JSON column in SQLite, got:\n%s", generatedSQL)
	}
	if !strings.Contains(generatedSQL, "`settings` TEXT") {
		t.Errorf("expected TEXT for JSONB column in SQLite, got:\n%s", generatedSQL)
	}

	// Test storing and retrieving JSON data
	jsonData := `{"key":"value","nested":{"arr":[1,2,3]}}`
	_, err = db.Exec("INSERT INTO json_test (metadata, settings) VALUES (?, ?)", jsonData, nil)
	if err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	var retrieved string
	err = db.QueryRow("SELECT metadata FROM json_test WHERE id = 1").Scan(&retrieved)
	if err != nil {
		t.Fatalf("failed to query: %v", err)
	}
	if retrieved != jsonData {
		t.Errorf("expected %s, got %s", jsonData, retrieved)
	}
}

func TestTableBuilder_JSON_SQL(t *testing.T) {
	tests := []struct {
		name          string
		driver        string
		jsonContains  string
		jsonbContains string
	}{
		{"sqlite", "sqlite", "`metadata` TEXT", "`settings` TEXT"},
		{"postgres", "postgres", `"metadata" JSON`, `"settings" JSONB`},
		{"mysql", "mysql", "`metadata` JSON", "`settings` JSON"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, err := orm.NewManager(orm.ManagerConfig{
				Driver:   tt.driver,
				Database: ":memory:",
			})
			if err != nil && tt.driver == "sqlite" {
				t.Fatalf("failed to init ORM: %v", err)
			}
			if err != nil {
				t.Skip("driver not available")
			}
			defer manager.Shutdown(context.Background())

			db := manager.DB()
			migrator := migrate.NewMigrator(db, manager.DriverName())

			var generatedSQL string
			if err := migrator.CreateTable("json_sql_test", func(tb *migrate.TableBuilder) {
				tb.ID()
				tb.JSON("metadata")
				tb.JSONB("settings").Nullable()
				generatedSQL = tb.ToSQL()
			}); err != nil {
				t.Fatalf("CreateTable failed: %v", err)
			}

			if !strings.Contains(generatedSQL, tt.jsonContains) {
				t.Errorf("expected SQL to contain %q, got:\n%s", tt.jsonContains, generatedSQL)
			}
			if !strings.Contains(generatedSQL, tt.jsonbContains) {
				t.Errorf("expected SQL to contain %q, got:\n%s", tt.jsonbContains, generatedSQL)
			}

			migrator.DropTable("json_sql_test")
		})
	}
}

func TestTableBuilder_CompositePrimaryKey(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	var generatedSQL string
	err := migrator.CreateTable("server_ssh_keys", func(tb *migrate.TableBuilder) {
		tb.Integer("server_id")
		tb.Integer("ssh_key_id")
		tb.Timestamps()
		tb.PrimaryKey("server_id", "ssh_key_id")
		generatedSQL = tb.ToSQL()
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("server_ssh_keys")

	// Should have composite primary key
	if !strings.Contains(generatedSQL, "PRIMARY KEY (`server_id`, `ssh_key_id`)") {
		t.Errorf("expected composite PRIMARY KEY, got:\n%s", generatedSQL)
	}

	// Test inserting with composite key
	_, err = db.Exec("INSERT INTO server_ssh_keys (server_id, ssh_key_id) VALUES (?, ?)", 1, 1)
	if err != nil {
		t.Fatalf("failed to insert: %v", err)
	}
	_, err = db.Exec("INSERT INTO server_ssh_keys (server_id, ssh_key_id) VALUES (?, ?)", 1, 2)
	if err != nil {
		t.Fatalf("failed to insert second row: %v", err)
	}

	// Should fail on duplicate composite key
	_, err = db.Exec("INSERT INTO server_ssh_keys (server_id, ssh_key_id) VALUES (?, ?)", 1, 1)
	if err == nil {
		t.Error("expected error on duplicate composite key, got none")
	}
}

func TestTableBuilder_Primary(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	var generatedSQL string
	err := migrator.CreateTable("user_two_factor", func(tb *migrate.TableBuilder) {
		tb.Integer("user_id").Primary()
		tb.String("secret")
		generatedSQL = tb.ToSQL()
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("user_two_factor")

	// Should have PRIMARY KEY on user_id
	if !strings.Contains(generatedSQL, "`user_id` INTEGER PRIMARY KEY") {
		t.Errorf("expected PRIMARY KEY on user_id, got:\n%s", generatedSQL)
	}

	// Test inserting with primary key
	_, err = db.Exec("INSERT INTO user_two_factor (user_id, secret) VALUES (?, ?)", 1, "TOTP123")
	if err != nil {
		t.Fatalf("failed to insert: %v", err)
	}

	// Should fail on duplicate primary key
	_, err = db.Exec("INSERT INTO user_two_factor (user_id, secret) VALUES (?, ?)", 1, "TOTP456")
	if err == nil {
		t.Error("expected error on duplicate primary key, got none")
	}
}

func TestTableBuilder_Decimal_SQL(t *testing.T) {
	tests := []struct {
		name     string
		driver   string
		contains string
	}{
		{"sqlite", "sqlite", "`cpu` NUMERIC(5,2)"},
		{"postgres", "postgres", `"cpu" NUMERIC(5,2)`},
		{"mysql", "mysql", "`cpu` DECIMAL(5,2)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			manager, err := orm.NewManager(orm.ManagerConfig{
				Driver:   tt.driver,
				Database: ":memory:",
			})
			if err != nil && tt.driver == "sqlite" {
				t.Fatalf("failed to init ORM: %v", err)
			}
			if err != nil {
				t.Skip("driver not available")
			}
			defer manager.Shutdown(context.Background())

			db := manager.DB()
			migrator := migrate.NewMigrator(db, manager.DriverName())

			var generatedSQL string
			if err := migrator.CreateTable("decimal_test", func(tb *migrate.TableBuilder) {
				tb.ID()
				tb.Decimal("cpu", 5, 2)
				generatedSQL = tb.ToSQL()
			}); err != nil {
				t.Fatalf("CreateTable failed: %v", err)
			}

			if !strings.Contains(generatedSQL, tt.contains) {
				t.Errorf("expected SQL to contain %q, got:\n%s", tt.contains, generatedSQL)
			}

			migrator.DropTable("decimal_test")
		})
	}
}
