package drivers

import (
	"strings"
	"testing"
)

// TestPostgresGrammar_CompileCreateTable_NoInlineIndex pins B32: PostgreSQL has
// no inline "INDEX name (cols)" clause inside CREATE TABLE (that is MySQL
// syntax), so the postgres grammar must not emit one even when the table
// carries indexes. The indexes are produced separately by CompileCreateIndexes.
func TestPostgresGrammar_CompileCreateTable_NoInlineIndex(t *testing.T) {
	grammar := &PostgresGrammar{}
	table := &Table{
		Columns: []Column{
			{Name: "id", Type: "INTEGER", Primary: true},
			{Name: "email", Type: "VARCHAR", Size: 255},
		},
		Indexes: []Index{
			{Name: "idx_email", Columns: []string{"email"}},
		},
	}
	got := grammar.CompileCreateTable("users", table)
	if strings.Contains(got, "INDEX") {
		t.Errorf("postgres CompileCreateTable must not emit an inline INDEX clause: %q", got)
	}
}

// TestPostgresGrammar_CompileCreateIndexes verifies the postgres grammar emits a
// standalone CREATE [UNIQUE] INDEX statement per index, with every identifier
// quoted.
func TestPostgresGrammar_CompileCreateIndexes(t *testing.T) {
	grammar := &PostgresGrammar{}
	table := &Table{
		Indexes: []Index{
			{Name: "idx_email", Columns: []string{"email"}},
			{Name: "idx_name_team", Columns: []string{"name", "team"}, Unique: true},
		},
	}
	stmts := grammar.CompileCreateIndexes("users", table)
	if len(stmts) != 2 {
		t.Fatalf("got %d statements, want 2: %v", len(stmts), stmts)
	}
	if stmts[0] != `CREATE INDEX "idx_email" ON "users" ("email")` {
		t.Errorf("stmt[0] = %q", stmts[0])
	}
	if stmts[1] != `CREATE UNIQUE INDEX "idx_name_team" ON "users" ("name", "team")` {
		t.Errorf("stmt[1] = %q", stmts[1])
	}
}

// TestPostgresGrammar_CompileCreateIndexes_Empty returns nil when no indexes.
func TestPostgresGrammar_CompileCreateIndexes_Empty(t *testing.T) {
	if got := (&PostgresGrammar{}).CompileCreateIndexes("users", &Table{}); got != nil {
		t.Errorf("want nil for no indexes, got %v", got)
	}
}

// TestSQLiteGrammar_CompileCreateIndexes verifies the sqlite grammar now emits
// per-index CREATE INDEX statements. Before B32, SQLiteGrammar.CompileCreateTable
// silently dropped table.Indexes entirely; CreateTableWith now runs these.
func TestSQLiteGrammar_CompileCreateIndexes(t *testing.T) {
	grammar := &SQLiteGrammar{}
	table := &Table{
		Indexes: []Index{
			{Name: "idx_email", Columns: []string{"email"}},
			{Name: "idx_name_team", Columns: []string{"name", "team"}, Unique: true},
		},
	}
	stmts := grammar.CompileCreateIndexes("users", table)
	if len(stmts) != 2 {
		t.Fatalf("got %d statements, want 2: %v", len(stmts), stmts)
	}
	if stmts[0] != "CREATE INDEX `idx_email` ON `users` (`email`)" {
		t.Errorf("stmt[0] = %q", stmts[0])
	}
	if stmts[1] != "CREATE UNIQUE INDEX `idx_name_team` ON `users` (`name`, `team`)" {
		t.Errorf("stmt[1] = %q", stmts[1])
	}
}

// TestSQLiteGrammar_CompileCreateIndexes_Empty returns nil when no indexes.
func TestSQLiteGrammar_CompileCreateIndexes_Empty(t *testing.T) {
	if got := (&SQLiteGrammar{}).CompileCreateIndexes("users", &Table{}); got != nil {
		t.Errorf("want nil for no indexes, got %v", got)
	}
}

// TestCreateTableWith_CreatesIndexes_SQLite exercises the full BaseDriver path
// on a live in-memory SQLite database: CreateTableWith must execute the table
// statement and then the per-index CREATE INDEX statements, so the declared
// index actually exists (the silent-drop bug fixed by B32).
func TestCreateTableWith_CreatesIndexes_SQLite(t *testing.T) {
	driver := NewSQLiteDriver()
	if err := driver.Connect(ConnectionConfig{Database: ":memory:", MaxIdleConns: 1}); err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer driver.Close()

	err := driver.CreateTable("users", func(table *Table) {
		table.Columns = []Column{
			{Name: "id", Type: "INTEGER", Primary: true},
			{Name: "email", Type: "VARCHAR", Size: 255},
		}
		table.Indexes = []Index{
			{Name: "idx_users_email", Columns: []string{"email"}, Unique: true},
		}
	})
	if err != nil {
		t.Fatalf("CreateTable: %v", err)
	}

	var name string
	row := driver.DB().QueryRow(
		"SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?",
		"idx_users_email",
	)
	if err := row.Scan(&name); err != nil {
		t.Fatalf("index idx_users_email not found after CreateTable: %v", err)
	}
	if name != "idx_users_email" {
		t.Errorf("got index %q, want idx_users_email", name)
	}
}
