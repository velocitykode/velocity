package migrate_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/orm/migrate"
)

// TestDSLGaps_Functional_SQLite executes a table that uses every newly added
// DSL feature against a live SQLite database, proving the generated DDL is
// accepted and that defaults and the CHECK constraint behave at runtime.
func TestDSLGaps_Functional_SQLite(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	migrator := migrate.NewMigrator(db, manager.DriverName())

	err := migrator.CreateTable("widgets", func(tb *migrate.TableBuilder) {
		tb.BigID()
		tb.SmallInteger("priority").Default(1)
		tb.Binary("blob").Nullable()
		tb.TimestampTz("seen_at").Nullable()
		tb.Integer("score").DefaultRaw("10")
		tb.TimestampsTz()
		tb.Check("score_nonneg", "score >= 0")
	})
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	// All expected columns present.
	want := map[string]bool{
		"id": false, "priority": false, "blob": false, "seen_at": false,
		"score": false, "created_at": false, "updated_at": false,
	}
	rows, err := db.Query("PRAGMA table_info(widgets)")
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	for rows.Next() {
		var cid, notNull, pk int
		var name, colType string
		var dflt interface{}
		if err := rows.Scan(&cid, &name, &colType, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		if _, ok := want[name]; ok {
			want[name] = true
		}
	}
	rows.Close()
	for name, found := range want {
		if !found {
			t.Errorf("column %q missing from created table", name)
		}
	}

	// Defaults apply: priority -> 1 (literal), score -> 10 (raw expr).
	// BigID auto-assigns sequential ids.
	if _, err := db.Exec("INSERT INTO widgets DEFAULT VALUES"); err != nil {
		t.Fatalf("insert default row: %v", err)
	}
	if _, err := db.Exec("INSERT INTO widgets DEFAULT VALUES"); err != nil {
		t.Fatalf("insert second row: %v", err)
	}
	var id, priority, score int
	if err := db.QueryRow("SELECT id, priority, score FROM widgets ORDER BY id LIMIT 1").Scan(&id, &priority, &score); err != nil {
		t.Fatalf("select: %v", err)
	}
	if id != 1 {
		t.Errorf("BigID first id = %d, want 1", id)
	}
	if priority != 1 {
		t.Errorf("priority default = %d, want 1", priority)
	}
	if score != 10 {
		t.Errorf("raw default score = %d, want 10", score)
	}
	var maxID int
	if err := db.QueryRow("SELECT MAX(id) FROM widgets").Scan(&maxID); err != nil {
		t.Fatalf("select max id: %v", err)
	}
	if maxID != 2 {
		t.Errorf("BigID second id = %d, want 2", maxID)
	}

	// CHECK is enforced: a negative score is rejected.
	if _, err := db.Exec("INSERT INTO widgets (score) VALUES (-5)"); err == nil {
		t.Error("expected CHECK violation for negative score, got nil")
	}
}

// TestDSLGaps_Check_TablePath verifies that CHECK constraints are wired into
// the ALTER path (Migrator.Table), not just CreateTable.
func TestDSLGaps_Check_TablePath(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	t.Run("postgres emits ADD CONSTRAINT", func(t *testing.T) {
		// Driver override + pretend mode: capture DDL without a live Postgres.
		m := migrate.NewMigrator(manager.DB(), "postgres")
		m.SetPretend(true)
		if err := m.Table("users", func(tb *migrate.TableBuilder) {
			tb.Check("age_pos", "age >= 0")
		}); err != nil {
			t.Fatalf("Table: %v", err)
		}
		joined := strings.Join(m.PretendLog(), "\n")
		want := `ALTER TABLE "users" ADD CONSTRAINT "age_pos" CHECK (age >= 0)`
		if !strings.Contains(joined, want) {
			t.Errorf("missing %q in pretend log:\n%s", want, joined)
		}
	})

	t.Run("invalid check name rejected in Table path", func(t *testing.T) {
		m := migrate.NewMigrator(manager.DB(), "postgres")
		m.SetPretend(true)
		err := m.Table("users", func(tb *migrate.TableBuilder) {
			tb.Check("bad name; DROP", "1=1")
		})
		if err == nil || !strings.Contains(err.Error(), "invalid check constraint name") {
			t.Errorf("expected invalid-name error, got: %v", err)
		}
	})
}

// TestDSLGaps_Check_SQLiteRebuild proves SQLite gains a CHECK via Migrator.Table
// through the table-rebuild path, preserving rows, columns, and explicit
// indexes, while enforcing the new constraint. Uses a temp-file DB so all pool
// connections share one database (a plain :memory: db is per-connection and the
// rebuild pins its own connection).
func TestDSLGaps_Check_SQLiteRebuild(t *testing.T) {
	manager, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: filepath.Join(t.TempDir(), "rebuild.db"),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	m := migrate.NewMigrator(db, manager.DriverName())

	if err := m.CreateTable("members", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.String("name")
		tb.Integer("age")
		tb.Timestamps()
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	// Explicit index that must survive the rebuild.
	if _, err := db.Exec("CREATE INDEX idx_members_name ON members(name)"); err != nil {
		t.Fatalf("create index: %v", err)
	}
	if _, err := db.Exec("INSERT INTO members (name, age) VALUES ('ann', 30), ('bob', 40)"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Add a column AND a CHECK in one call: column add (ALTER) then rebuild,
	// so the rebuild must see the just-added column.
	if err := m.Table("members", func(tb *migrate.TableBuilder) {
		tb.Boolean("active").Default(true)
		tb.Check("age_nonneg", "age >= 0")
	}); err != nil {
		t.Fatalf("Table rebuild: %v", err)
	}

	// Rows preserved, values intact, new column defaulted on existing rows.
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM members").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Errorf("row count = %d, want 2", count)
	}
	var name string
	var age, active int
	if err := db.QueryRow("SELECT name, age, active FROM members ORDER BY id LIMIT 1").Scan(&name, &age, &active); err != nil {
		t.Fatalf("select: %v", err)
	}
	if name != "ann" || age != 30 || active != 1 {
		t.Errorf("row drifted after rebuild: name=%q age=%d active=%d", name, age, active)
	}

	// Explicit index preserved.
	var idx string
	if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='index' AND name='idx_members_name'").Scan(&idx); err != nil {
		t.Errorf("explicit index not preserved across rebuild: %v", err)
	}

	// Temp rebuild table renamed away, not left behind.
	var leftover string
	if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='members_velocity_rebuild'").Scan(&leftover); err == nil {
		t.Errorf("rebuild temp table left behind: %q", leftover)
	}

	// CHECK is enforced.
	if _, err := db.Exec("INSERT INTO members (name, age) VALUES ('neg', -1)"); err == nil {
		t.Error("expected CHECK violation for negative age, got nil")
	}
	if _, err := db.Exec("INSERT INTO members (name, age) VALUES ('pos', 5)"); err != nil {
		t.Errorf("valid insert rejected after rebuild: %v", err)
	}
}

// TestDSLGaps_Check_SQLiteRebuild_AtomicRollback proves the whole Table() call
// is atomic on SQLite: when a CHECK is violated by existing rows, the rebuild
// fails AND the column added in the same call is rolled back (not left behind),
// and the original table is untouched.
func TestDSLGaps_Check_SQLiteRebuild_AtomicRollback(t *testing.T) {
	manager, err := orm.NewManager(orm.ManagerConfig{
		Driver:   "sqlite",
		Database: filepath.Join(t.TempDir(), "atomic.db"),
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer manager.Shutdown(context.Background())

	db := manager.DB()
	m := migrate.NewMigrator(db, manager.DriverName())

	if err := m.CreateTable("items", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.Integer("qty")
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}
	// Seed a row that will violate the CHECK we are about to add.
	if _, err := db.Exec("INSERT INTO items (qty) VALUES (-5)"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// One call adds a column AND a CHECK that existing data violates.
	err = m.Table("items", func(tb *migrate.TableBuilder) {
		tb.Boolean("flag").Default(true)
		tb.Check("qty_nonneg", "qty >= 0")
	})
	if err == nil {
		t.Fatal("expected rebuild to fail on violating row, got nil")
	}

	// Column must NOT have been added (whole op rolled back).
	rows, err := db.Query("PRAGMA table_info(items)")
	if err != nil {
		t.Fatalf("table_info: %v", err)
	}
	hasFlag := false
	for rows.Next() {
		var cid, notNull, pk int
		var cname, ctype string
		var dflt interface{}
		if err := rows.Scan(&cid, &cname, &ctype, &notNull, &dflt, &pk); err != nil {
			rows.Close()
			t.Fatalf("scan: %v", err)
		}
		if cname == "flag" {
			hasFlag = true
		}
	}
	rows.Close()
	if hasFlag {
		t.Error("column 'flag' was left behind after failed rebuild (not atomic)")
	}

	// Original row intact.
	var qty int
	if err := db.QueryRow("SELECT qty FROM items").Scan(&qty); err != nil {
		t.Fatalf("select after rollback: %v", err)
	}
	if qty != -5 {
		t.Errorf("qty = %d, want -5 (unchanged)", qty)
	}

	// No temp table left behind.
	var leftover string
	if err := db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name='items_velocity_rebuild'").Scan(&leftover); err == nil {
		t.Errorf("rebuild temp table left behind: %q", leftover)
	}
}

// TestDSLGaps_AddColumn_TimestampManagedDefault covers the full Migrator.AddColumn
// path (not just ColumnBuilder.ToSQL): a non-nullable timestamp added on
// postgres must carry the managed default so it backfills existing rows.
func TestDSLGaps_AddColumn_TimestampManagedDefault(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	m := migrate.NewMigrator(manager.DB(), "postgres")
	m.SetPretend(true)
	if err := m.AddColumn("events", "ts", func(cb *migrate.ColumnBuilder) {
		cb.TimestampTz()
	}); err != nil {
		t.Fatalf("AddColumn: %v", err)
	}
	joined := strings.Join(m.PretendLog(), "\n")
	want := `ALTER TABLE "events" ADD COLUMN "ts" TIMESTAMPTZ DEFAULT NOW() NOT NULL`
	if !strings.Contains(joined, want) {
		t.Errorf("missing %q in:\n%s", want, joined)
	}
}

// TestDSLGaps_AddColumn_SQLiteTimestamp documents SQLite's real, row-count
// dependent ADD COLUMN behavior (verified against the driver): a non-null
// timestamp add succeeds on an empty table, and SQLite returns a clear native
// error on a populated one. This is a general SQLite property of adding any
// non-null column without a constant default, not specific to timestamps.
func TestDSLGaps_AddColumn_SQLiteTimestamp(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	m := migrate.NewMigrator(manager.DB(), manager.DriverName())
	if err := m.CreateTable("evts", func(tb *migrate.TableBuilder) {
		tb.ID()
	}); err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	// Empty table: a non-null timestamp add succeeds.
	if err := m.AddColumn("evts", "ts", func(cb *migrate.ColumnBuilder) {
		cb.TimestampTz()
	}); err != nil {
		t.Fatalf("add non-null timestamp to empty table should succeed: %v", err)
	}

	// Populated table: SQLite refuses with a clear native error. (ts was added
	// NOT NULL above, so a value must be supplied for the seed row.)
	if _, err := manager.DB().Exec("INSERT INTO evts (ts) VALUES ('2020-01-01 00:00:00')"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	err := m.AddColumn("evts", "ts2", func(cb *migrate.ColumnBuilder) {
		cb.TimestampTz()
	})
	if err == nil {
		t.Error("expected SQLite error adding non-null timestamp to populated table, got nil")
	}

	// Nullable add always works.
	if err := m.AddColumn("evts", "ts3", func(cb *migrate.ColumnBuilder) {
		cb.TimestampTz().Nullable()
	}); err != nil {
		t.Errorf("nullable timestamp add should succeed: %v", err)
	}
}

func TestDSLGaps_CheckNameValidation(t *testing.T) {
	manager := newTestManager(t)
	defer manager.Shutdown(context.Background())

	migrator := migrate.NewMigrator(manager.DB(), manager.DriverName())

	err := migrator.CreateTable("bad_check", func(tb *migrate.TableBuilder) {
		tb.ID()
		tb.Integer("x")
		tb.Check("x; DROP TABLE users", "x > 0")
	})
	if err == nil {
		t.Fatal("expected error for invalid check constraint name")
	}
	if !strings.Contains(err.Error(), "invalid check constraint name") {
		t.Errorf("unexpected error: %v", err)
	}
}
