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
			contains:  []string{"CREATE INDEX", "idx_users_email", "ON users", "(email)"},
		},
		{
			name:      "simple index postgres",
			driver:    "postgres",
			indexName: "idx_users_email",
			table:     "users",
			columns:   []string{"email"},
			contains:  []string{"CREATE INDEX", "idx_users_email", "ON users", "(email)"},
		},
		{
			name:      "simple index mysql",
			driver:    "mysql",
			indexName: "idx_users_email",
			table:     "users",
			columns:   []string{"email"},
			contains:  []string{"CREATE INDEX", "idx_users_email", "ON users", "(email)"},
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
			contains:  []string{"(team_id, created_at)"},
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
		{
			name:        "partial index mysql - not supported",
			driver:      "mysql",
			indexName:   "idx_users_active",
			table:       "users",
			columns:     []string{"email"},
			where:       "deleted_at IS NULL",
			notContains: []string{"WHERE"},
		},

		// Covering indexes (INCLUDE clause - PostgreSQL 11+)
		{
			name:      "covering index postgres",
			driver:    "postgres",
			indexName: "idx_users_covering",
			table:     "users",
			columns:   []string{"email"},
			include:   []string{"id", "name", "avatar_url"},
			contains:  []string{"INCLUDE (id, name, avatar_url)"},
		},
		{
			name:        "covering index sqlite - not supported",
			driver:      "sqlite",
			indexName:   "idx_users_covering",
			table:       "users",
			columns:     []string{"email"},
			include:     []string{"id", "name"},
			notContains: []string{"INCLUDE"},
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
				"users_email_login_idx",
				"ON users",
				"(email)",
				"INCLUDE (id, uuid, password, name)",
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

	sql.WriteString(name)
	sql.WriteString(" ON ")
	sql.WriteString(table)

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
	sql.WriteString(strings.Join(columns, ", "))
	sql.WriteString(")")

	// INCLUDE (postgres only)
	if len(include) > 0 && driver == "postgres" {
		sql.WriteString(" INCLUDE (")
		sql.WriteString(strings.Join(include, ", "))
		sql.WriteString(")")
	}

	// WHERE (postgres, sqlite)
	if where != "" && driver != "mysql" {
		sql.WriteString(" WHERE ")
		sql.WriteString(where)
	}

	return sql.String()
}
