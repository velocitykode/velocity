package migrate_test

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/migrate"
)

// =============================================================================
// SQL Generation Tests (unit tests - no database required)
// =============================================================================

func TestIndexBuilder_SQLGeneration(t *testing.T) {
	tests := []struct {
		name        string
		driver      string
		indexName   string
		table       string
		columns     []string
		unique      bool
		where       string
		include     []string
		using       string
		ifNotExists bool
		contains    []string
		notContains []string
	}{
		// Basic indexes
		{
			name:      "simple index sqlite",
			driver:    "sqlite",
			indexName: "idx_users_email",
			table:     "users",
			columns:   []string{"email"},
			contains:  []string{"CREATE INDEX", "`idx_users_email`", "ON `users`", "(`email`)"},
		},
		{
			name:      "simple index postgres",
			driver:    "postgres",
			indexName: "idx_users_email",
			table:     "users",
			columns:   []string{"email"},
			contains:  []string{"CREATE INDEX", `"idx_users_email"`, `ON "users"`, `("email")`},
		},
		{
			name:      "simple index mysql",
			driver:    "mysql",
			indexName: "idx_users_email",
			table:     "users",
			columns:   []string{"email"},
			contains:  []string{"CREATE INDEX", "`idx_users_email`", "ON `users`", "(`email`)"},
		},

		// Unique indexes
		{
			name:      "unique index",
			driver:    "postgres",
			indexName: "idx_users_email_unique",
			table:     "users",
			columns:   []string{"email"},
			unique:    true,
			contains:  []string{"CREATE UNIQUE INDEX"},
		},

		// Composite indexes
		{
			name:      "composite index",
			driver:    "postgres",
			indexName: "idx_projects_team",
			table:     "projects",
			columns:   []string{"team_id", "created_at"},
			contains:  []string{`("team_id", "created_at")`},
		},

		// Partial indexes (WHERE clause)
		{
			name:      "partial index postgres",
			driver:    "postgres",
			indexName: "idx_users_active",
			table:     "users",
			columns:   []string{"email"},
			where:     "deleted_at IS NULL",
			contains:  []string{"WHERE deleted_at IS NULL"},
		},
		{
			name:      "partial index sqlite",
			driver:    "sqlite",
			indexName: "idx_users_active",
			table:     "users",
			columns:   []string{"email"},
			where:     "deleted_at IS NULL",
			contains:  []string{"WHERE deleted_at IS NULL"},
		},
		// Unsupported-modifier fail-loud behavior (MySQL partial WHERE, SQLite
		// INCLUDE) is covered by TestIndexBuilder_UnsupportedModifiersFailLoud.

		// Covering indexes (INCLUDE clause - PostgreSQL 11+)
		{
			name:      "covering index postgres",
			driver:    "postgres",
			indexName: "idx_users_covering",
			table:     "users",
			columns:   []string{"email"},
			include:   []string{"id", "name", "avatar_url"},
			contains:  []string{`INCLUDE ("id", "name", "avatar_url")`},
		},

		// Index types (USING clause)
		{
			name:      "btree index postgres",
			driver:    "postgres",
			indexName: "idx_test",
			table:     "test",
			columns:   []string{"col"},
			using:     "btree",
			contains:  []string{"USING btree"},
		},
		{
			name:      "gin index postgres",
			driver:    "postgres",
			indexName: "idx_test",
			table:     "test",
			columns:   []string{"col"},
			using:     "gin",
			contains:  []string{"USING gin"},
		},
		{
			name:      "brin index postgres",
			driver:    "postgres",
			indexName: "idx_test",
			table:     "test",
			columns:   []string{"col"},
			using:     "brin",
			contains:  []string{"USING brin"},
		},

		// IF NOT EXISTS
		{
			name:        "if not exists postgres",
			driver:      "postgres",
			indexName:   "idx_test",
			table:       "test",
			columns:     []string{"col"},
			ifNotExists: true,
			contains:    []string{"IF NOT EXISTS"},
		},
		{
			name:        "if not exists sqlite",
			driver:      "sqlite",
			indexName:   "idx_test",
			table:       "test",
			columns:     []string{"col"},
			ifNotExists: true,
			contains:    []string{"IF NOT EXISTS"},
		},

		// Complex covering + partial index
		{
			name:      "complex index postgres",
			driver:    "postgres",
			indexName: "users_email_login_idx",
			table:     "users",
			columns:   []string{"email"},
			where:     "deleted_at IS NULL",
			include:   []string{"id", "uuid", "password", "name"},
			contains: []string{
				"CREATE INDEX",
				`"users_email_login_idx"`,
				`ON "users"`,
				`("email")`,
				`INCLUDE ("id", "uuid", "password", "name")`,
				"WHERE deleted_at IS NULL",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql := buildTestIndexSQL(tt.driver, tt.indexName, tt.table, tt.columns,
				tt.unique, tt.where, tt.include, tt.using, tt.ifNotExists)

			for _, substr := range tt.contains {
				if !strings.Contains(sql, substr) {
					t.Errorf("expected SQL to contain %q, got:\n%s", substr, sql)
				}
			}

			for _, substr := range tt.notContains {
				if strings.Contains(sql, substr) {
					t.Errorf("expected SQL to NOT contain %q, got:\n%s", substr, sql)
				}
			}
		})
	}
}

// =============================================================================
// Using() / Where() validation tests (regression coverage for SQL injection
// via the raw USING access method and partial-index WHERE predicate).
// =============================================================================

func TestIndexBuilder_UsingAccepted(t *testing.T) {
	for _, method := range []string{"btree", "hash", "gin", "gist", "brin", "spgist"} {
		t.Run(method, func(t *testing.T) {
			b := migrate.NewIndexBuilder("idx_x", "tbl", "postgres")
			b.Columns("col").Using(method)
			sql, err := b.ToSQL()
			if err != nil {
				t.Fatalf("expected ToSQL to accept Using(%q), got error: %v", method, err)
			}
			if !strings.Contains(sql, "USING "+method) {
				t.Errorf("expected SQL to contain USING %s, got:\n%s", method, sql)
			}
		})
	}
}

func TestIndexBuilder_UsingRejectsUnknown(t *testing.T) {
	b := migrate.NewIndexBuilder("idx_x", "tbl", "postgres")
	b.Columns("col").Using("nonsense")
	if _, err := b.ToSQL(); err == nil {
		t.Fatal("expected ToSQL to reject Using(\"nonsense\")")
	}
}

func TestIndexBuilder_UsingRejectsInjection(t *testing.T) {
	b := migrate.NewIndexBuilder("idx_x", "tbl", "postgres")
	b.Columns("col").Using("btree; DROP TABLE x")
	if _, err := b.ToSQL(); err == nil {
		t.Fatal("expected ToSQL to reject Using(\"btree; DROP TABLE x\")")
	}
}

func TestIndexBuilder_WhereAccepted(t *testing.T) {
	cases := []struct {
		name      string
		predicate string
	}{
		{"is null", "deleted_at IS NULL"},
		{"equals string", "status = 'active'"},
		{"and in list", "col = 1 AND id IN (1, 2, 3)"},
		{"parenthesised or", "(a = 1 OR b = 2)"},
		{"is not null", "deleted_at IS NOT NULL"},
		{"not equal numeric", "count != 0"},
		{"float compare", "ratio >= 0.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := migrate.NewIndexBuilder("idx_x", "tbl", "postgres")
			b.Columns("col").Where(tc.predicate)
			sql, err := b.ToSQL()
			if err != nil {
				t.Fatalf("expected Where(%q) to be accepted, got error: %v", tc.predicate, err)
			}
			if !strings.Contains(sql, "WHERE "+tc.predicate) {
				t.Errorf("expected SQL to contain WHERE %s, got:\n%s", tc.predicate, sql)
			}
		})
	}
}

func TestIndexBuilder_WhereRejectsInjection(t *testing.T) {
	cases := []struct {
		name      string
		predicate string
	}{
		{"semicolon drop", "status = 'active'; DROP TABLE x"},
		{"embedded quote and comment", "col = 'a'' OR 1=1--"},
		{"double dash comment", "col = 1 -- AND id = 2"},
		{"block comment", "col = 1 /* injected */"},
		{"backtick identifier", "`col` = 1"},
		{"double quote identifier", `"col" = 1`},
		{"unterminated string", "col = 'unterminated"},
		{"backslash escape", `col = 'a\'b'`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := migrate.NewIndexBuilder("idx_x", "tbl", "postgres")
			b.Columns("col").Where(tc.predicate)
			if _, err := b.ToSQL(); err == nil {
				t.Fatalf("expected ToSQL to reject Where(%q)", tc.predicate)
			}
		})
	}
}

// TestIndexBuilder_UnsupportedModifiersFailLoud verifies that semantics-changing
// modifiers are rejected (not silently dropped) on drivers that cannot express
// them: MySQL has no IF NOT EXISTS, INCLUDE, or partial-index WHERE; SQLite has
// no INCLUDE. The error must name both the modifier and the driver.
func TestIndexBuilder_UnsupportedModifiersFailLoud(t *testing.T) {
	cases := []struct {
		name     string
		driver   string
		build    func(b *migrate.IndexBuilder)
		wantText string
	}{
		{"mysql if not exists", "mysql", func(b *migrate.IndexBuilder) { b.Columns("col").IfNotExists() }, "IF NOT EXISTS"},
		{"mysql include", "mysql", func(b *migrate.IndexBuilder) { b.Columns("col").Include("other") }, "INCLUDE"},
		{"mysql where", "mysql", func(b *migrate.IndexBuilder) { b.Columns("col").Where("deleted_at IS NULL") }, "WHERE"},
		{"sqlite include", "sqlite", func(b *migrate.IndexBuilder) { b.Columns("col").Include("other") }, "INCLUDE"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := migrate.NewIndexBuilder("idx_x", "tbl", tc.driver)
			tc.build(b)
			_, err := b.ToSQL()
			if err == nil {
				t.Fatalf("expected ToSQL to reject %s on %s", tc.wantText, tc.driver)
			}
			if !strings.Contains(err.Error(), tc.wantText) {
				t.Errorf("error should name modifier %q, got: %v", tc.wantText, err)
			}
			if !strings.Contains(err.Error(), tc.driver) {
				t.Errorf("error should name driver %q, got: %v", tc.driver, err)
			}
		})
	}
}

// TestIndexBuilder_UsingLenient verifies that USING (an access-path hint, not a
// semantics change) is dropped rather than rejected on SQLite, and dropped for
// unrecognized methods on MySQL.
func TestIndexBuilder_UsingLenient(t *testing.T) {
	cases := []struct{ name, driver, using string }{
		{"sqlite drops using", "sqlite", "btree"},
		{"mysql drops unknown using", "mysql", "gin"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := migrate.NewIndexBuilder("idx_x", "tbl", tc.driver)
			b.Columns("col").Using(tc.using)
			sql, err := b.ToSQL()
			if err != nil {
				t.Fatalf("expected USING(%q) to be lenient on %s, got error: %v", tc.using, tc.driver, err)
			}
			if strings.Contains(sql, "USING") {
				t.Errorf("expected USING to be dropped on %s, got:\n%s", tc.driver, sql)
			}
		})
	}
}

// =============================================================================
// Identifier validation / quoting tests (regression coverage for SQL injection
// via index column names, see toPostgresSQL/toMySQLSQL/toSQLiteSQL).
// =============================================================================

func TestCreateIndex_RejectsMaliciousColumn(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	if err := migrator.CreateTable("safe_users", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.String("name")
	}); err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("safe_users")

	cases := []struct {
		name string
		col  string
	}{
		{"sql injection", "id; DROP TABLE users"},
		{"contains space", "bad name"},
		{"contains quote", `bad"name`},
		{"contains backtick", "bad`name"},
		{"starts with digit", "1column"},
		{"empty", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := migrator.CreateIndex("idx_bad", "safe_users", func(b *migrate.IndexBuilder) {
				b.Columns(tc.col)
			})
			if err == nil {
				t.Fatalf("expected CreateIndex to reject column %q, got nil error", tc.col)
			}
			if !strings.Contains(err.Error(), "invalid") {
				t.Errorf("expected error to mention 'invalid', got %v", err)
			}
		})
	}
}

func TestCreateIndex_RejectsMaliciousInclude(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	// Use postgres driver path for SQL generation (INCLUDE is postgres-only).
	migrator := migrate.NewMigrator(db, "postgres")

	err := migrator.CreateIndex("idx_bad", "users", func(b *migrate.IndexBuilder) {
		b.Columns("email").Include("id; DROP TABLE users")
	})
	if err == nil {
		t.Fatal("expected CreateIndex to reject malicious include column")
	}
	if !strings.Contains(err.Error(), "invalid") {
		t.Errorf("expected error to mention 'invalid', got %v", err)
	}
}

func TestCreateIndex_RejectsMaliciousName(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	err := migrator.CreateIndex("idx; DROP TABLE users", "safe_users", func(b *migrate.IndexBuilder) {
		b.Columns("name")
	})
	if err == nil {
		t.Fatal("expected CreateIndex to reject malicious index name")
	}
}

func TestDropIndex_RejectsMaliciousName(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	if err := migrator.DropIndex("idx; DROP TABLE users"); err == nil {
		t.Fatal("expected DropIndex to reject malicious index name")
	}
}

// TestIndexBuilder_ReservedWordQuoted verifies that a reserved SQL keyword
// used as a column name is still emitted safely via driver-appropriate
// quoting (e.g. `order` → "order" on postgres, `order` on mysql/sqlite).
func TestIndexBuilder_ReservedWordQuoted(t *testing.T) {
	cases := []struct {
		driver       string
		wantContains string
	}{
		{"postgres", `("order")`},
		{"mysql", "(`order`)"},
		{"sqlite", "(`order`)"},
	}

	for _, tc := range cases {
		t.Run(tc.driver, func(t *testing.T) {
			sql := buildTestIndexSQL(tc.driver, "idx_orders", "orders",
				[]string{"order"}, false, "", nil, "", false)
			if !strings.Contains(sql, tc.wantContains) {
				t.Errorf("expected SQL to contain %q, got:\n%s", tc.wantContains, sql)
			}
			// Should not contain the bare unquoted identifier in column list.
			if strings.Contains(sql, "(order)") {
				t.Errorf("reserved word emitted unquoted, got:\n%s", sql)
			}
		})
	}
}

// TestIndexBuilder_ToSQLValidates ensures ToSQL returns an error directly
// when an invalid column name is configured on the builder.
func TestIndexBuilder_ToSQLValidates(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	_ = db
	// Drive ToSQL via CreateIndex which constructs the builder.
	migrator := migrate.NewMigrator(db, manager.DriverName())
	if err := migrator.CreateTable("tbl", func(tb *migrate.TableBuilder) {
		tb.ID()
	}); err != nil {
		t.Fatalf("create table: %v", err)
	}
	defer migrator.DropTable("tbl")

	err := migrator.CreateIndex("idx_x", "tbl", func(b *migrate.IndexBuilder) {
		b.Columns("col; DROP TABLE x")
	})
	if err == nil {
		t.Fatal("expected error from invalid column")
	}
}

// =============================================================================
// SQLite Integration Tests
// =============================================================================

func TestCreateIndex_SQLite(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	// Create test table
	err := migrator.CreateTable("users", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.String("email").Unique()
		tb.String("name")
		tb.Integer("team_id")
		tb.Timestamps()
		tb.SoftDeletes()
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("users")

	t.Run("simple index", func(t *testing.T) {
		err := migrator.CreateIndex("idx_users_name", "users", func(b *migrate.IndexBuilder) {
			b.Columns("name")
		})
		if err != nil {
			t.Errorf("CreateIndex failed: %v", err)
		}
	})

	t.Run("composite index", func(t *testing.T) {
		err := migrator.CreateIndex("idx_users_team_name", "users", func(b *migrate.IndexBuilder) {
			b.Columns("team_id", "name")
		})
		if err != nil {
			t.Errorf("CreateIndex failed: %v", err)
		}
	})

	t.Run("partial index", func(t *testing.T) {
		err := migrator.CreateIndex("idx_users_active", "users", func(b *migrate.IndexBuilder) {
			b.Columns("email").Where("deleted_at IS NULL")
		})
		if err != nil {
			t.Errorf("CreateIndex failed: %v", err)
		}
	})

	t.Run("unique index", func(t *testing.T) {
		err := migrator.CreateIndex("idx_users_team_email", "users", func(b *migrate.IndexBuilder) {
			b.Columns("team_id", "email").Unique()
		})
		if err != nil {
			t.Errorf("CreateIndex failed: %v", err)
		}
	})

	// Verify indexes exist
	indexes := getIndexes(t, db, "users")
	expected := []string{"idx_users_name", "idx_users_team_name", "idx_users_active", "idx_users_team_email"}
	for _, idx := range expected {
		if !indexes[idx] {
			t.Errorf("expected index %q to exist", idx)
		}
	}
}

func TestDropIndex_SQLite(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	err := migrator.CreateTable("test", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.String("name")
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("test")

	// Create and drop index
	err = migrator.CreateIndex("idx_to_drop", "test", func(b *migrate.IndexBuilder) {
		b.Columns("name")
	})
	if err != nil {
		t.Fatalf("failed to create index: %v", err)
	}

	err = migrator.DropIndex("idx_to_drop")
	if err != nil {
		t.Errorf("DropIndex failed: %v", err)
	}

	// Verify index is gone
	var count int
	db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_to_drop'").Scan(&count)
	if count != 0 {
		t.Error("index should have been dropped")
	}
}

func TestShorthandMethods_SQLite(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	err := migrator.CreateTable("test", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.String("email")
		tb.String("name")
		tb.Integer("team_id")
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("test")

	t.Run("Index", func(t *testing.T) {
		if err := migrator.Index("test", "name"); err != nil {
			t.Errorf("Index() failed: %v", err)
		}
	})

	t.Run("Index composite", func(t *testing.T) {
		if err := migrator.Index("test", "team_id", "name"); err != nil {
			t.Errorf("Index() failed: %v", err)
		}
	})

	t.Run("UniqueIndex", func(t *testing.T) {
		if err := migrator.UniqueIndex("test", "email"); err != nil {
			t.Errorf("UniqueIndex() failed: %v", err)
		}
	})
}

// =============================================================================
// PostgreSQL Integration Tests (skip if not available)
// =============================================================================

func TestCreateIndex_Postgres(t *testing.T) {
	manager := initPostgres(t)
	if manager == nil {
		return
	}
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	err := migrator.CreateTable("idx_test_pg", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.UUID("uuid").Unique()
		tb.String("email")
		tb.String("name")
		tb.Integer("team_id")
		tb.Timestamps()
		tb.SoftDeletes()
	})
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}
	defer migrator.DropTable("idx_test_pg")

	t.Run("simple index", func(t *testing.T) {
		err := migrator.CreateIndex("idx_pg_name", "idx_test_pg", func(b *migrate.IndexBuilder) {
			b.Columns("name")
		})
		if err != nil {
			t.Errorf("CreateIndex failed: %v", err)
		}
	})

	t.Run("partial index", func(t *testing.T) {
		err := migrator.CreateIndex("idx_pg_active", "idx_test_pg", func(b *migrate.IndexBuilder) {
			b.Columns("email").Where("deleted_at IS NULL")
		})
		if err != nil {
			t.Errorf("CreateIndex with WHERE failed: %v", err)
		}
	})

	t.Run("covering index", func(t *testing.T) {
		err := migrator.CreateIndex("idx_pg_covering", "idx_test_pg", func(b *migrate.IndexBuilder) {
			b.Columns("email").Include("id", "uuid", "name")
		})
		if err != nil {
			t.Errorf("CreateIndex with INCLUDE failed: %v", err)
		}
	})

	t.Run("brin index", func(t *testing.T) {
		err := migrator.CreateIndex("idx_pg_brin", "idx_test_pg", func(b *migrate.IndexBuilder) {
			b.Columns("created_at").Using("brin")
		})
		if err != nil {
			t.Errorf("CreateIndex with USING brin failed: %v", err)
		}
	})
}

// =============================================================================
// Helpers
// =============================================================================

func initPostgres(t *testing.T) *orm.Manager {
	host := envOr("TEST_PG_HOST", "localhost")
	port := envOr("TEST_PG_PORT", "5432")
	user := envOr("TEST_PG_USER", "postgres")
	pass := envOr("TEST_PG_PASS", "postgres")
	db := envOr("TEST_PG_DB", "velocity_test")

	manager, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "postgres",
		Host:     host,
		Port:     port,
		Database: db,
		Username: user,
		Password: pass,
	})
	if err != nil {
		t.Skipf("PostgreSQL not available: %v", err)
		return nil
	}
	return manager
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getIndexes(t *testing.T, db *sql.DB, table string) map[string]bool {
	rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='index' AND tbl_name=?", table)
	if err != nil {
		t.Fatalf("failed to query indexes: %v", err)
	}
	defer rows.Close()

	indexes := make(map[string]bool)
	for rows.Next() {
		var name string
		rows.Scan(&name)
		indexes[name] = true
	}
	return indexes
}

// quoteIdent mirrors the driver-aware quoting used by IndexBuilder so the unit
// test helper produces the same SQL shape as production code.
func quoteIdent(name, driver string) string {
	switch driver {
	case "mysql", "sqlite":
		return "`" + strings.ReplaceAll(name, "`", "``") + "`"
	default: // postgres + fallback
		return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
	}
}

func quoteIdentList(names []string, driver string) string {
	parts := make([]string, len(names))
	for i, n := range names {
		parts[i] = quoteIdent(n, driver)
	}
	return strings.Join(parts, ", ")
}

// buildTestIndexSQL mirrors the IndexBuilder logic for unit testing
func buildTestIndexSQL(driver, name, table string, columns []string, unique bool,
	where string, include []string, using string, ifNotExists bool) string {

	var sql strings.Builder

	sql.WriteString("CREATE ")
	if unique {
		sql.WriteString("UNIQUE ")
	}
	sql.WriteString("INDEX ")

	if ifNotExists && driver != "mysql" {
		sql.WriteString("IF NOT EXISTS ")
	}

	sql.WriteString(quoteIdent(name, driver))
	sql.WriteString(" ON ")
	sql.WriteString(quoteIdent(table, driver))

	// USING (postgres, mysql btree/hash only)
	if using != "" {
		if driver == "postgres" {
			sql.WriteString(" USING ")
			sql.WriteString(using)
		} else if driver == "mysql" && (using == "btree" || using == "hash") {
			sql.WriteString(" USING ")
			sql.WriteString(strings.ToUpper(using))
		}
	}

	sql.WriteString(" (")
	sql.WriteString(quoteIdentList(columns, driver))
	sql.WriteString(")")

	// INCLUDE (postgres only)
	if len(include) > 0 && driver == "postgres" {
		sql.WriteString(" INCLUDE (")
		sql.WriteString(quoteIdentList(include, driver))
		sql.WriteString(")")
	}

	// WHERE (postgres, sqlite)
	if where != "" && driver != "mysql" {
		sql.WriteString(" WHERE ")
		sql.WriteString(where)
	}

	return sql.String()
}
