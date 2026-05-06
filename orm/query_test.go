package orm

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/velocitykode/velocity/orm/drivers"
)

// TestUpdate_DoesNotMutateCallerMap is the regression test for a bug where
// Query.Update wrote the updated_at timestamp sentinel directly onto the
// caller's map as a side effect. Update must treat its input as read-only.
func TestUpdate_DoesNotMutateCallerMap(t *testing.T) {
	setupConvenienceTests(t)
	id := seedUser(t, Default(), "Alice", "alice@example.com", 30)

	// Caller's map — we snapshot it before the call and compare afterwards.
	updates := map[string]any{
		"name": "Alice Updated",
		"age":  31,
	}
	originalLen := len(updates)
	originalName := updates["name"]
	originalAge := updates["age"]

	affected, err := Model[TestUser]{}.Where("id = ?", id).Update(updates)
	if err != nil {
		t.Fatalf("Update returned error: %v", err)
	}
	if affected != 1 {
		t.Errorf("expected 1 row affected, got %d", affected)
	}

	if len(updates) != originalLen {
		t.Errorf("Update mutated caller's map: len changed from %d to %d (keys: %v)",
			originalLen, len(updates), keysOf(updates))
	}
	if _, ok := updates["updated_at"]; ok {
		t.Error("Update injected updated_at into caller's map (expected map to be unchanged)")
	}
	if updates["name"] != originalName {
		t.Errorf("Update mutated caller's map: name = %v, want %v", updates["name"], originalName)
	}
	if updates["age"] != originalAge {
		t.Errorf("Update mutated caller's map: age = %v, want %v", updates["age"], originalAge)
	}
}

// TestUpdate_RawSQLMarkerEmitsLiteral asserts that values of type RawSQL
// are emitted verbatim into the generated SQL and are NOT bound as
// parameters. This covers all three dialect grammars; the query.go Update
// path is a thin wrapper over these grammars so testing them directly is
// representative of the full Update pipeline.
func TestUpdate_RawSQLMarkerEmitsLiteral(t *testing.T) {
	tests := []struct {
		name        string
		grammar     drivers.QueryGrammar
		value       RawSQL
		wantLiteral string
	}{
		{
			name:        "mysql NOW()",
			grammar:     &drivers.MySQLGrammar{},
			value:       NOW,
			wantLiteral: "NOW()",
		},
		{
			name:        "postgres NOW()",
			grammar:     &drivers.PostgresGrammar{},
			value:       NOW,
			wantLiteral: "NOW()",
		},
		{
			name:        "sqlite CURRENT_TIMESTAMP",
			grammar:     &drivers.SQLiteGrammar{},
			value:       CurrentTimestamp,
			wantLiteral: "CURRENT_TIMESTAMP",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := tt.grammar.CompileUpdate(
				"users",
				map[string]any{"updated_at": tt.value},
				[]drivers.Condition{
					{Column: "id", Operator: "=", Value: 1, Type: "and"},
				},
			)

			if !strings.Contains(sql, tt.wantLiteral) {
				t.Errorf("SQL missing literal %q, got %q", tt.wantLiteral, sql)
			}

			// The bound-parameter slice must not contain the literal string
			// form of the sentinel — if it did, the grammar bound it as
			// a parameter (the pre-fix bug).
			for i, a := range args {
				if s, ok := a.(string); ok && s == tt.wantLiteral {
					t.Errorf("args[%d] = %q (RawSQL sentinel leaked into bound args)", i, s)
				}
				if r, ok := a.(RawSQL); ok && string(r) == tt.wantLiteral {
					t.Errorf("args[%d] = RawSQL(%q) (RawSQL sentinel leaked into bound args)", i, string(r))
				}
			}
		})
	}
}

// TestUpdate_StringValueNOW_IsBoundParameter is the SQL-injection regression
// test for the old string-sentinel bug. A caller passing the literal
// string "NOW()" as a column value (e.g. a user comment that happens to
// equal that text) must be bound as a parameter, not promoted to raw SQL.
func TestUpdate_StringValueNOW_IsBoundParameter(t *testing.T) {
	tests := []struct {
		name        string
		grammar     drivers.QueryGrammar
		wantPart    string // driver-specific placeholder fragment
		wantArgsLen int
	}{
		{
			name:        "mysql binds string",
			grammar:     &drivers.MySQLGrammar{},
			wantPart:    "`comment` = ?",
			wantArgsLen: 2, // comment + id
		},
		{
			name:        "postgres binds string",
			grammar:     &drivers.PostgresGrammar{},
			wantPart:    `"comment" = $1`,
			wantArgsLen: 2,
		},
		{
			name:        "sqlite binds string",
			grammar:     &drivers.SQLiteGrammar{},
			wantPart:    "`comment` = ?",
			wantArgsLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := tt.grammar.CompileUpdate(
				"users",
				map[string]any{"comment": "NOW()"}, // plain string that looks like SQL
				[]drivers.Condition{
					{Column: "id", Operator: "=", Value: 1, Type: "and"},
				},
			)

			if !strings.Contains(sql, tt.wantPart) {
				t.Errorf("SQL missing placeholder fragment %q, got %q", tt.wantPart, sql)
			}
			if strings.Contains(sql, "`comment` = NOW()") || strings.Contains(sql, `"comment" = NOW()`) {
				t.Errorf("SQL promoted string value to raw SQL (SQL-injection vector): %q", sql)
			}
			if len(args) != tt.wantArgsLen {
				t.Errorf("args count = %d, want %d; args = %v", len(args), tt.wantArgsLen, args)
			}

			// The literal string "NOW()" must appear in the bound args
			// slice — that's the whole point of the fix.
			found := false
			for _, a := range args {
				if s, ok := a.(string); ok && s == "NOW()" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("bound args do not contain the string \"NOW()\"; args = %v", args)
			}
		})
	}
}

// keysOf returns the keys of a map as a sorted-deterministic-ish slice
// (for error messages only; iteration order is not asserted).
func keysOf(m map[string]any) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestPluck_HonorsDistinct is the regression test for the bug where
// Query.Pluck silently dropped the Distinct flag when forwarding to the
// grammar, causing facet/lookup queries to return duplicates instead of
// distinct values.
//
// We assert at three layers:
//   - end-to-end: Distinct().Pluck(col) returns deduped values
//   - SQL emission: the captured SQL contains "DISTINCT"
//   - all three grammars: SelectQuery{Distinct: true} emits SELECT DISTINCT
//
// The grammar layer is dialect-specific and shipped on every driver, so we
// table-test all three rather than rely on the SQLite path alone.
func TestPluck_HonorsDistinct(t *testing.T) {
	setupConvenienceTests(t)
	m := Default()

	// Seed with duplicate names: pluck of "name" with Distinct should
	// dedupe to the unique set.
	seedUser(t, m, "Alice", "alice1@example.com", 30)
	seedUser(t, m, "Alice", "alice2@example.com", 31)
	seedUser(t, m, "Bob", "bob@example.com", 25)

	// Without Distinct: 3 rows.
	all, err := Model[TestUser]{}.Pluck("name")
	if err != nil {
		t.Fatalf("Pluck without Distinct returned error: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("non-distinct pluck: got %d rows, want 3 (seeded values)", len(all))
	}

	// With Distinct: 2 rows.
	q := newQuery[TestUser]()
	q.Distinct()
	got, err := q.Pluck("name")
	if err != nil {
		t.Fatalf("Distinct().Pluck returned error: %v", err)
	}
	if len(got) != 2 {
		t.Errorf("Distinct().Pluck returned %d rows, want 2 (Alice, Bob); rows=%v", len(got), got)
	}

	// Verify the emitted SQL contained DISTINCT, the grammar-level proof
	// that the Distinct flag actually flowed through Pluck's SelectQuery.
	sql, _ := q.ToSQL()
	if !strings.Contains(strings.ToUpper(sql), "SELECT DISTINCT") {
		t.Errorf("Pluck SQL missing DISTINCT keyword: %q", sql)
	}
}

// TestWhereGroup_ParenthesizedSubgroup is the regression test for the
// missing nested-predicate primitive: chains of Where.OrWhere with a flat
// Conditions slice mis-bound when AND/OR mixed (e.g. "team_id=? AND name
// ILIKE ? OR email ILIKE ?" instead of the intended grouped form).
//
// We assert SQL emission for all three grammars as well as end-to-end
// behaviour against SQLite, since the wiring lives in both the query
// builder and every driver's grammar.
func TestWhereGroup_ParenthesizedSubgroup(t *testing.T) {
	tests := []struct {
		name        string
		grammar     drivers.QueryGrammar
		wantContain string
	}{
		{
			name:        "sqlite",
			grammar:     &drivers.SQLiteGrammar{},
			wantContain: "WHERE `team_id` = ? AND (`name` ILIKE ? OR `email` ILIKE ?)",
		},
		{
			name:        "mysql",
			grammar:     &drivers.MySQLGrammar{},
			wantContain: "WHERE `team_id` = ? AND (`name` ILIKE ? OR `email` ILIKE ?)",
		},
		{
			name:        "postgres",
			grammar:     &drivers.PostgresGrammar{},
			wantContain: `WHERE "team_id" = $1 AND ("name" ILIKE $2 OR "email" ILIKE $3)`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			conds := []drivers.Condition{
				{Column: "team_id", Operator: "=", Value: 7, Type: "and"},
				{
					Type: "and",
					Group: []drivers.Condition{
						{Column: "name", Operator: "ILIKE", Value: "%foo%", Type: "and"},
						{Column: "email", Operator: "ILIKE", Value: "%foo%", Type: "or"},
					},
				},
			}
			sql, args := tt.grammar.CompileSelect(&drivers.SelectQuery{
				Table:      "users",
				Conditions: conds,
			})
			if !strings.Contains(sql, tt.wantContain) {
				t.Errorf("SQL missing expected fragment %q\nfull SQL: %q", tt.wantContain, sql)
			}
			if len(args) != 3 {
				t.Errorf("args count = %d, want 3; args=%v", len(args), args)
			}
		})
	}
}

// TestQuery_WhereGroup_EndToEnd asserts the query-builder API ships the
// grouped predicate through to SQL and (against SQLite) returns the
// correct row set.
func TestQuery_WhereGroup_EndToEnd(t *testing.T) {
	setupConvenienceTests(t)
	m := Default()

	// Two teams, three users; team A owns Alice (matches "Al"), team B
	// owns Alex (matches "Al"). The grouped predicate restricts the
	// "Al" search to team A only, so only Alice should match.
	if _, err := m.DB().Exec("ALTER TABLE test_users ADD COLUMN team_id INTEGER"); err != nil {
		t.Fatalf("alter table: %v", err)
	}
	insert := func(name, email string, teamID int) {
		if _, err := m.DB().Exec(
			"INSERT INTO test_users (name, email, age, is_active, team_id, created_at, updated_at) VALUES (?, ?, ?, ?, ?, datetime('now'), datetime('now'))",
			name, email, 30, true, teamID,
		); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	insert("Alice", "alice@a.example", 1)
	insert("Bob", "bob@a.example", 1)
	insert("Alex", "alex@b.example", 2)

	// Builder: WHERE team_id = 1 AND (name LIKE '%Al%' OR email LIKE '%Al%')
	q := newQuery[TestUser]()
	q.Where("team_id = ?", 1).WhereGroup(func(sub *Query[TestUser]) {
		sub.Where("name LIKE ?", "%Al%").OrWhere("email LIKE ?", "%Al%")
	})
	got, err := q.Get()
	if err != nil {
		t.Fatalf("Get returned error: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d rows, want 1 (Alice in team 1); rows=%+v", len(got), got)
	}
	if got[0].Name != "Alice" {
		t.Errorf("got user %q, want Alice", got[0].Name)
	}

	// Verify the captured SQL has the parens.
	sql, _ := q.ToSQL()
	if !strings.Contains(sql, "(") || !strings.Contains(sql, ")") {
		t.Errorf("WhereGroup did not emit parentheses; SQL=%q", sql)
	}
}

// TestWhereGroup_OrWhereGroup verifies OrWhereGroup OR-joins the group to
// the prior conditions (vs WhereGroup which AND-joins).
func TestWhereGroup_OrWhereGroup(t *testing.T) {
	grammar := &drivers.SQLiteGrammar{}
	conds := []drivers.Condition{
		{Column: "active", Operator: "=", Value: true, Type: "and"},
		{
			Type: "or",
			Group: []drivers.Condition{
				{Column: "role", Operator: "=", Value: "admin", Type: "and"},
				{Column: "role", Operator: "=", Value: "owner", Type: "or"},
			},
		},
	}
	sql, _ := grammar.CompileSelect(&drivers.SelectQuery{
		Table:      "users",
		Conditions: conds,
	})
	want := "WHERE `active` = ? OR (`role` = ? OR `role` = ?)"
	if !strings.Contains(sql, want) {
		t.Errorf("SQL missing %q; got %q", want, sql)
	}
}

// TestWhereGroup_NestedGroups exercises the recursive grouping path: a
// group inside a group. Postgres positional placeholders are the
// strictest test because each leaf must allocate sequential $N values
// across nested groups.
func TestWhereGroup_NestedGroups(t *testing.T) {
	grammar := &drivers.PostgresGrammar{}
	conds := []drivers.Condition{
		{Column: "tenant_id", Operator: "=", Value: 1, Type: "and"},
		{
			Type: "and",
			Group: []drivers.Condition{
				{Column: "active", Operator: "=", Value: true, Type: "and"},
				{
					Type: "or",
					Group: []drivers.Condition{
						{Column: "role", Operator: "=", Value: "admin", Type: "and"},
						{Column: "role", Operator: "=", Value: "owner", Type: "or"},
					},
				},
			},
		},
	}
	sql, args := grammar.CompileSelect(&drivers.SelectQuery{
		Table:      "users",
		Conditions: conds,
	})
	want := `WHERE "tenant_id" = $1 AND ("active" = $2 OR ("role" = $3 OR "role" = $4))`
	if !strings.Contains(sql, want) {
		t.Errorf("SQL missing %q; got %q", want, sql)
	}
	if len(args) != 4 {
		t.Errorf("args count = %d, want 4; args=%v", len(args), args)
	}
}

// TestWhereGroup_EmptyGroupNoOp guarantees an empty closure does not
// emit empty parentheses (which would be a SQL syntax error).
func TestWhereGroup_EmptyGroupNoOp(t *testing.T) {
	q := &Query[TestUser]{
		driver:  &nopDriver{grammar: &drivers.SQLiteGrammar{}},
		table:   "test_users",
		columns: []string{"*"},
	}
	q.Where("active = ?", true).WhereGroup(func(sub *Query[TestUser]) {
		// no-op closure
	})
	if got := len(q.conditions); got != 1 {
		t.Errorf("empty WhereGroup leaked a condition: got %d, want 1", got)
	}
}

// TestWhereGroup_NilClosureNoOp guarantees a nil closure does not panic
// or alter the condition list.
func TestWhereGroup_NilClosureNoOp(t *testing.T) {
	q := &Query[TestUser]{
		driver:  &nopDriver{grammar: &drivers.SQLiteGrammar{}},
		table:   "test_users",
		columns: []string{"*"},
	}
	q.Where("active = ?", true).WhereGroup(nil)
	if got := len(q.conditions); got != 1 {
		t.Errorf("nil WhereGroup mutated conditions: got %d, want 1", got)
	}
}

// TestWhereGroup_ErrorPropagates asserts a validation error inside a
// WhereGroup closure surfaces on the parent query (and via terminal
// methods).
func TestWhereGroup_ErrorPropagates(t *testing.T) {
	q := &Query[TestUser]{
		driver:  &nopDriver{grammar: &drivers.SQLiteGrammar{}},
		table:   "test_users",
		columns: []string{"*"},
	}
	q.WhereGroup(func(sub *Query[TestUser]) {
		// Invalid identifier; parseCondition should record an error.
		sub.Where("1 INVALID-OP ?", 1)
	})
	if q.Err() == nil {
		t.Error("expected error to propagate from WhereGroup closure")
	}
}

// nopDriver is a minimal Driver implementation for grammar-only tests
// that do not need a real database connection.
type nopDriver struct {
	grammar drivers.QueryGrammar
}

func (d *nopDriver) Connect(drivers.ConnectionConfig) error { return nil }
func (d *nopDriver) Close() error                           { return nil }
func (d *nopDriver) Ping() error                            { return nil }
func (d *nopDriver) DB() *sql.DB                            { return nil }
func (d *nopDriver) QueryContext(context.Context, string, ...any) (*sql.Rows, error) {
	return nil, nil
}
func (d *nopDriver) QueryRowContext(context.Context, string, ...any) *sql.Row {
	return nil
}
func (d *nopDriver) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	return nil, nil
}
func (d *nopDriver) BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error) { return nil, nil }
func (d *nopDriver) CreateTable(string, func(*drivers.Table)) error           { return nil }
func (d *nopDriver) DropTable(string) error                                   { return nil }
func (d *nopDriver) HasTable(string) bool                                     { return false }
func (d *nopDriver) HasColumn(string, string) bool                            { return false }
func (d *nopDriver) Grammar() drivers.QueryGrammar                            { return d.grammar }
func (d *nopDriver) DriverName() string                                       { return "sqlite" }

// TestPluck_DistinctEmitsSQL_AllDrivers verifies SelectQuery.Distinct flows
// to SELECT DISTINCT on each shipped grammar. This is a grammar-level guard
// so the regression cannot recur on a non-SQLite driver.
func TestPluck_DistinctEmitsSQL_AllDrivers(t *testing.T) {
	tests := []struct {
		name    string
		grammar drivers.QueryGrammar
	}{
		{"sqlite", &drivers.SQLiteGrammar{}},
		{"mysql", &drivers.MySQLGrammar{}},
		{"postgres", &drivers.PostgresGrammar{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, _ := tt.grammar.CompileSelect(&drivers.SelectQuery{
				Table:    "users",
				Columns:  []string{"role"},
				Distinct: true,
			})
			if !strings.Contains(strings.ToUpper(sql), "SELECT DISTINCT") {
				t.Errorf("%s: CompileSelect with Distinct=true did not emit SELECT DISTINCT, got %q",
					tt.name, sql)
			}
		})
	}
}
