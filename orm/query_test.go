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

	// Caller's map, we snapshot it before the call and compare afterwards.
	updates := map[string]any{
		"name": "Alice Updated",
		"age":  31,
	}
	originalLen := len(updates)
	originalName := updates["name"]
	originalAge := updates["age"]

	affected, err := Model[TestUser]{}.Where("id = ?", id).Update(context.Background(), updates)
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
// are emitted into the generated SQL (with the well-known current-timestamp
// sentinels pinned to their UTC form) and are NOT bound as parameters. This
// covers all three dialect grammars; the query.go Update path is a thin
// wrapper over these grammars so testing them directly is representative of
// the full Update pipeline.
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
			wantLiteral: "UTC_TIMESTAMP()",
		},
		{
			name:        "postgres NOW()",
			grammar:     &drivers.PostgresGrammar{},
			value:       NOW,
			wantLiteral: "(NOW() AT TIME ZONE 'UTC')",
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
			// form of the sentinel, if it did, the grammar bound it as
			// a parameter (the pre-fix bug).
			for i, a := range args {
				if s, ok := a.(string); ok && s == string(tt.value) {
					t.Errorf("args[%d] = %q (RawSQL sentinel leaked into bound args)", i, s)
				}
				if r, ok := a.(RawSQL); ok && r == tt.value {
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
			// slice, that's the whole point of the fix.
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
	all, err := Model[TestUser]{}.Pluck(context.Background(), "name")
	if err != nil {
		t.Fatalf("Pluck without Distinct returned error: %v", err)
	}
	if len(all) != 3 {
		t.Errorf("non-distinct pluck: got %d rows, want 3 (seeded values)", len(all))
	}

	// With Distinct: 2 rows.
	q := newQuery[TestUser]()
	q.Distinct()
	got, err := q.Pluck(context.Background(), "name")
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
	got, err := q.Get(context.Background())
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
func (d *nopDriver) OperatorRegistry() map[string]drivers.OperatorSpec        { return nil }

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

// stubRegistryDriver wraps nopDriver with a custom OperatorRegistry so
// query-side tests can exercise resolveOperator without needing the full
// postgres surface.
type stubRegistryDriver struct {
	nopDriver
	registry map[string]drivers.OperatorSpec
}

func (d *stubRegistryDriver) OperatorRegistry() map[string]drivers.OperatorSpec {
	return d.registry
}

// TestWhere_UnregisteredOperatorRejected confirms an operator absent from
// both the built-in scalar allowlist and the active driver's registry
// surfaces the existing "invalid SQL operator" error unchanged.
func TestWhere_UnregisteredOperatorRejected(t *testing.T) {
	q := Model[TestUser]{}.Where("id ## ?", 5)
	if q.err == nil {
		t.Fatal("expected error for unregistered operator '##'")
	}
	if !strings.Contains(q.err.Error(), "invalid SQL operator") {
		t.Errorf("error: got %q, want containing 'invalid SQL operator'", q.err.Error())
	}
}

// withStubRegistryDriver swaps Default to a Manager backed by a stub driver
// so test code can exercise the public chain (Model[T]{}.Where(...)) without
// reaching into Query's struct internals. Returns the restore closure.
func withStubRegistryDriver(t *testing.T, registry map[string]drivers.OperatorSpec) func() {
	t.Helper()
	d := &stubRegistryDriver{
		nopDriver: nopDriver{grammar: &drivers.PostgresGrammar{}},
		registry:  registry,
	}
	m := &Manager{defaultDriver: d, defaultName: "stub", connections: map[string]drivers.Driver{}}
	prev := Default()
	SetDefault(m)
	return func() { SetDefault(prev) }
}

// TestWhere_RegisteredOperatorRoundTrips confirms a driver-registered
// operator flows through resolveOperator, lands a non-nil Spec on the
// Condition, and survives parseCondition without rejection. Goes through
// the public Model[T]{}.Where chain so a future construction-time
// validation on Query would still catch this test.
func TestWhere_RegisteredOperatorRoundTrips(t *testing.T) {
	defer withStubRegistryDriver(t, map[string]drivers.OperatorSpec{
		"@>": {Op: "@>", Arity: 1, ParamShape: drivers.ParamJSON, Template: "{{lhs}} @> {{rhs}}::jsonb"},
	})()

	q := Model[TestUser]{}.Where("processes @> ?", `{"key":"value"}`)
	if q.err != nil {
		t.Fatalf("Where rejected registered operator: %v", q.err)
	}
	if len(q.conditions) != 1 {
		t.Fatalf("conditions: got %d, want 1", len(q.conditions))
	}
	cond := q.conditions[0]
	if cond.Spec == nil {
		t.Fatal("Condition.Spec is nil; resolveOperator did not stash registry hit")
	}
	if cond.Spec.Op != "@>" {
		t.Errorf("Spec.Op: got %q, want %q", cond.Spec.Op, "@>")
	}
}

// TestWhere_RegisteredOperatorParamShapeMismatch confirms cond.Value is
// validated against the spec's ParamShape at parse time, not at execute
// time. A JSONB op given a struct value must reject up front.
func TestWhere_RegisteredOperatorParamShapeMismatch(t *testing.T) {
	defer withStubRegistryDriver(t, map[string]drivers.OperatorSpec{
		"@>": {Op: "@>", Arity: 1, ParamShape: drivers.ParamJSON, Template: "{{lhs}} @> {{rhs}}::jsonb"},
	})()

	q := Model[TestUser]{}.Where("processes @> ?", struct{ X int }{X: 1})
	if q.err == nil {
		t.Fatal("expected error for ParamJSON op given struct value")
	}
	if !strings.Contains(q.err.Error(), "JSON") {
		t.Errorf("error: got %q, want containing 'JSON'", q.err.Error())
	}
}

// namedDriver overrides nopDriver's DriverName so dialect-gated paths
// (e.g. the ILIKE gate) can be tested per driver.
type namedDriver struct {
	nopDriver
	name string
}

func (d *namedDriver) DriverName() string { return d.name }

// newDetachedQuery builds a driver-bound Query without a database
// connection for parse/compile-level Where tests.
func newDetachedQuery(grammar drivers.QueryGrammar) *Query[TestUser] {
	return &Query[TestUser]{
		driver:  &nopDriver{grammar: grammar},
		table:   "test_users",
		columns: []string{"*"},
	}
}

// TestWhere_ThreeArgForm_RegressionB6 covers the three-argument form
// Where(column, operator, value). Before the B6 fix,
// this parsed as column="age", operator="=", value=">" — the operator
// string was bound as the value and 18 was silently dropped.
func TestWhere_ThreeArgForm_RegressionB6(t *testing.T) {
	q := newDetachedQuery(&drivers.SQLiteGrammar{})
	q.Where("age", ">", 18)
	if q.Err() != nil {
		t.Fatalf("three-argument Where returned error: %v", q.Err())
	}
	if len(q.conditions) != 1 {
		t.Fatalf("conditions: got %d, want 1", len(q.conditions))
	}
	cond := q.conditions[0]
	if cond.Column != "age" || cond.Operator != ">" || cond.Value != 18 {
		t.Errorf("condition = {%q %q %v}, want {age > 18}", cond.Column, cond.Operator, cond.Value)
	}

	sql, args := (&drivers.SQLiteGrammar{}).CompileSelect(&drivers.SelectQuery{
		Table:      "test_users",
		Conditions: q.conditions,
	})
	if !strings.Contains(sql, "`age` > ?") {
		t.Errorf("SQL missing \"`age` > ?\"; got %q", sql)
	}
	if len(args) != 1 || args[0] != 18 {
		t.Errorf("args = %v, want [18]", args)
	}
}

// TestWhere_ThreeArgForm_RegistryOperator confirms the three-argument
// form also recognises driver-registered operators, not just the
// built-in allowlist.
func TestWhere_ThreeArgForm_RegistryOperator(t *testing.T) {
	defer withStubRegistryDriver(t, map[string]drivers.OperatorSpec{
		"@>": {Op: "@>", Arity: 1, ParamShape: drivers.ParamJSON, Template: "{{lhs}} @> {{rhs}}::jsonb"},
	})()

	q := Model[TestUser]{}.Where("processes", "@>", `{"key":"value"}`)
	if q.err != nil {
		t.Fatalf("three-argument Where with registry operator errored: %v", q.err)
	}
	if len(q.conditions) != 1 || q.conditions[0].Operator != "@>" {
		t.Fatalf("conditions = %+v, want one @> condition", q.conditions)
	}
	if q.conditions[0].Spec == nil {
		t.Error("Condition.Spec is nil; registry operator did not resolve")
	}
}

// TestWhere_ThreeArgNonOperatorRejected_RegressionB6: a bare column with
// two extra arguments whose first is not an operator string is a hard
// error. Before the fix args[1] was silently dropped and args[0] bound
// as the value.
func TestWhere_ThreeArgNonOperatorRejected_RegressionB6(t *testing.T) {
	for _, args := range [][]any{
		{1, 2},        // first arg not a string
		{"bogus", 18}, // first arg not an operator
	} {
		q := newDetachedQuery(&drivers.SQLiteGrammar{})
		q.Where("age", args...)
		if q.Err() == nil {
			t.Errorf("Where(\"age\", %v...): expected error, got nil", args)
			continue
		}
		if !strings.Contains(q.Err().Error(), "three-argument form") {
			t.Errorf("error %q does not mention the three-argument form", q.Err().Error())
		}
	}
}

// TestWhere_BareColumnTooManyArgs_RegressionB6: more than two extra
// arguments on a bare column is a hard error (previously all but the
// first were silently dropped).
func TestWhere_BareColumnTooManyArgs_RegressionB6(t *testing.T) {
	q := newDetachedQuery(&drivers.SQLiteGrammar{})
	q.Where("age", ">", 18, 19)
	if q.Err() == nil {
		t.Fatal("expected error for bare column with three extra args")
	}
	if !strings.Contains(q.Err().Error(), "too many arguments") {
		t.Errorf("error %q does not mention too many arguments", q.Err().Error())
	}
}

// TestWhere_CompoundConditionRejected_RegressionB6 is the headline B6
// regression: "a = ? AND b = ?" used to silently drop everything after
// the first predicate (and its bound arg), broadening the result set.
func TestWhere_CompoundConditionRejected_RegressionB6(t *testing.T) {
	q := newDetachedQuery(&drivers.SQLiteGrammar{})
	q.Where("a = ? AND b = ?", 1, 2)
	if q.Err() == nil {
		t.Fatal("expected error for compound condition string")
	}
	if !strings.Contains(q.Err().Error(), "WhereGroup") {
		t.Errorf("error %q should point the caller at WhereGroup/chained Where", q.Err().Error())
	}
	if len(q.conditions) != 0 {
		t.Errorf("rejected condition still appended: %+v", q.conditions)
	}
}

// TestOrWhere_CompoundConditionRejected_RegressionB6 keeps OrWhere
// symmetric with Where; both share parseCondition.
func TestOrWhere_CompoundConditionRejected_RegressionB6(t *testing.T) {
	q := newDetachedQuery(&drivers.SQLiteGrammar{})
	q.Where("a = ?", 1).OrWhere("b = ? OR c = ?", 2, 3)
	if q.Err() == nil {
		t.Fatal("expected error for compound OrWhere condition string")
	}
}

// TestHaving_CompoundConditionRejected_RegressionB6 keeps Having
// symmetric with Where; both share parseCondition.
func TestHaving_CompoundConditionRejected_RegressionB6(t *testing.T) {
	q := newDetachedQuery(&drivers.SQLiteGrammar{})
	q.GroupBy("age").Having("age > ? AND id > ?", 18, 5)
	if q.Err() == nil {
		t.Fatal("expected error for compound Having condition string")
	}
}

// TestWhere_InlineLiteralRejected_RegressionB6: "age > 18" used to parse
// as operator ">" with the literal discarded, emitting "age > NULL".
func TestWhere_InlineLiteralRejected_RegressionB6(t *testing.T) {
	q := newDetachedQuery(&drivers.SQLiteGrammar{})
	q.Where("age > 18")
	if q.Err() == nil {
		t.Fatal("expected error for inline literal in condition string")
	}
	if !strings.Contains(q.Err().Error(), "bind values with ?") {
		t.Errorf("error %q should tell the caller to bind via ? placeholders", q.Err().Error())
	}
}

// TestWhere_IsNullCompoundRejected_RegressionB6: an IS NULL form followed
// by more predicate used to match the IS NULL fast path and silently drop
// the remainder.
func TestWhere_IsNullCompoundRejected_RegressionB6(t *testing.T) {
	q := newDetachedQuery(&drivers.SQLiteGrammar{})
	q.Where("deleted_at IS NULL OR id = ?", 1)
	if q.Err() == nil {
		t.Fatal("expected error for compound condition after IS NULL")
	}
	if len(q.conditions) != 0 {
		t.Errorf("rejected condition still appended: %+v", q.conditions)
	}
}

// TestWhere_NullPredicateExtraArgsRejected: IS NULL / IS NOT NULL take no
// bind values; extra Go arguments used to be silently dropped.
func TestWhere_NullPredicateExtraArgsRejected(t *testing.T) {
	for _, cond := range []string{"deleted_at IS NULL", "deleted_at IS NOT NULL"} {
		q := newDetachedQuery(&drivers.SQLiteGrammar{})
		q.Where(cond, 1)
		if q.Err() == nil {
			t.Errorf("Where(%q, 1): expected error for extra argument", cond)
		}
		if len(q.conditions) != 0 {
			t.Errorf("Where(%q, 1): rejected condition still appended: %+v", cond, q.conditions)
		}
	}
}

// TestWhere_PlaceholderArityRejected: a single-placeholder predicate takes
// exactly one bind value; extra arguments used to be silently dropped and a
// missing argument silently bound NULL.
func TestWhere_PlaceholderArityRejected(t *testing.T) {
	t.Run("too many args", func(t *testing.T) {
		q := newDetachedQuery(&drivers.SQLiteGrammar{})
		q.Where("age > ?", 18, 19)
		if q.Err() == nil {
			t.Fatal("expected error for extra argument with single placeholder")
		}
		if len(q.conditions) != 0 {
			t.Errorf("rejected condition still appended: %+v", q.conditions)
		}
	})
	t.Run("missing arg", func(t *testing.T) {
		q := newDetachedQuery(&drivers.SQLiteGrammar{})
		q.Where("age > ?")
		if q.Err() == nil {
			t.Fatal("expected error for placeholder with no argument")
		}
	})
	t.Run("no placeholder with arg", func(t *testing.T) {
		q := newDetachedQuery(&drivers.SQLiteGrammar{})
		q.Where("age >", 18)
		if q.Err() == nil {
			t.Fatal("expected error for argument without placeholder")
		}
	})
}

// TestWhere_NotLike_RegressionB6: multi-word operators used to be
// unparseable — "name NOT LIKE ?" split into operator "NOT" and failed
// operator validation despite NOT LIKE being in the allowlist.
func TestWhere_NotLike_RegressionB6(t *testing.T) {
	for _, cond := range []string{"name NOT LIKE ?", "name not like ?"} {
		q := newDetachedQuery(&drivers.SQLiteGrammar{})
		q.Where(cond, "%foo%")
		if q.Err() != nil {
			t.Errorf("Where(%q) errored: %v", cond, q.Err())
			continue
		}
		if got := q.conditions[0].Operator; got != "NOT LIKE" {
			t.Errorf("Where(%q) operator = %q, want NOT LIKE", cond, got)
			continue
		}
		sql, args := (&drivers.SQLiteGrammar{}).CompileSelect(&drivers.SelectQuery{
			Table:      "test_users",
			Conditions: q.conditions,
		})
		if !strings.Contains(sql, "`name` NOT LIKE ?") {
			t.Errorf("SQL missing NOT LIKE predicate; got %q", sql)
		}
		if len(args) != 1 || args[0] != "%foo%" {
			t.Errorf("args = %v, want [%%foo%%]", args)
		}
	}
}

// TestWhere_NotIn_MultiWordOperator confirms the longest-match operator
// path keeps the exact uppercase form grammars special-case for slice
// expansion.
func TestWhere_NotIn_MultiWordOperator(t *testing.T) {
	q := newDetachedQuery(&drivers.SQLiteGrammar{})
	q.Where("id not in ?", []any{1, 2, 3})
	if q.Err() != nil {
		t.Fatalf("Where with NOT IN errored: %v", q.Err())
	}
	sql, args := (&drivers.SQLiteGrammar{}).CompileSelect(&drivers.SelectQuery{
		Table:      "test_users",
		Conditions: q.conditions,
	})
	if !strings.Contains(sql, "`id` NOT IN (?, ?, ?)") {
		t.Errorf("SQL missing expanded NOT IN; got %q", sql)
	}
	if len(args) != 3 {
		t.Errorf("args = %v, want 3 expanded values", args)
	}
}

// TestWhere_ILIKE_DriverGated_RegressionB6: ILIKE is PostgreSQL-only.
// Driver-bound builders on other dialects must reject it instead of
// shipping SQL that fails at execute time; detached builders (nil
// driver) keep accepting it because the dialect is not yet known.
func TestWhere_ILIKE_DriverGated_RegressionB6(t *testing.T) {
	gated := func(name string, grammar drivers.QueryGrammar) *Query[TestUser] {
		return &Query[TestUser]{
			driver:  &namedDriver{nopDriver: nopDriver{grammar: grammar}, name: name},
			table:   "test_users",
			columns: []string{"*"},
		}
	}

	for _, name := range []string{"sqlite", "mysql"} {
		q := gated(name, &drivers.SQLiteGrammar{})
		q.Where("name ILIKE ?", "%foo%")
		if q.Err() == nil {
			t.Errorf("driver %q: expected ILIKE rejection, got nil error", name)
		} else if !strings.Contains(q.Err().Error(), "PostgreSQL-only") {
			t.Errorf("driver %q: error %q should say ILIKE is PostgreSQL-only", name, q.Err().Error())
		}
	}

	pg := gated("postgres", &drivers.PostgresGrammar{})
	pg.Where("name ILIKE ?", "%foo%")
	if pg.Err() != nil {
		t.Errorf("postgres: ILIKE rejected: %v", pg.Err())
	}

	detached := &Query[TestUser]{table: "test_users", columns: []string{"*"}}
	detached.Where("name ILIKE ?", "%foo%")
	if detached.Err() != nil {
		t.Errorf("nil driver: ILIKE rejected: %v", detached.Err())
	}
}
