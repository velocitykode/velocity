package orm

import (
	"strings"
	"testing"

	"github.com/velocitykode/velocity/orm/drivers"
)

// TestSelect_WhitelistAccepts confirms the projection whitelist admits
// all forms documented in Query[T].Select's godoc: plain identifiers,
// "*", and aggregate-shaped expressions with optional AS alias
// (case-insensitive).
func TestSelect_WhitelistAccepts(t *testing.T) {
	cases := []string{
		"*",
		"id",
		"created_at",
		"users.id",
		"COUNT(*)",
		"SUM(amount)",
		"MIN(price) AS min_price",
		"MAX(price) as max_price",
		"AVG(orders.total) AS avg_total",
		"COUNT(id)",
	}
	for _, c := range cases {
		t.Run(c, func(t *testing.T) {
			q := &Query[TestUser]{
				driver: &nopDriver{grammar: &drivers.SQLiteGrammar{}},
				table:  "test_users",
			}
			q.Select(c)
			if q.Err() != nil {
				t.Fatalf("Select(%q) unexpectedly errored: %v", c, q.Err())
			}
		})
	}
}

// TestSelect_WhitelistRejects locks down the injection vectors called
// out in security audit O-01 (plus a fuller corpus of obvious bypass
// attempts). Each rejected input MUST cause Query[T].Err to be non-nil;
// the deferred-error contract guarantees terminal methods return it
// before issuing SQL.
func TestSelect_WhitelistRejects(t *testing.T) {
	cases := []struct {
		name string
		col  string
	}{
		{"O-01 audit vector", "id),0,(SELECT password FROM admins WHERE 1=1"},
		{"semicolon stacking", "COUNT(*); DROP TABLE users--"},
		{"trailing comment", "id, (SELECT 1)"},
		{"block comment", "col/*comment*/"},
		{"double quote", `col" OR 1=1`},
		{"backtick", "col` OR 1=1"},
		{"single quote", "col' OR 1=1"},
		{"UNION inside aggregate", "COUNT(* FROM admins UNION SELECT password"},
		{"newline injection", "COUNT(*)\n; DROP TABLE users"},
		{"null byte", "COUNT(\x00*)"},
		{"SELECT keyword", "(SELECT password FROM admins)"},
		{"FROM keyword", "id FROM admins"},
		{"sub-select", "(SELECT 1)"},
		{"arithmetic", "1+1"},
		{"unbalanced parens", "COUNT("},
		{"identifier with paren", "name("},
		{"identifier with dash", "user-name"},
		{"identifier with space", "user name"},
		{"AS with paren", "COUNT(*) AS (x)"},
		{"AS with quote", `COUNT(*) AS "x"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &Query[TestUser]{
				driver: &nopDriver{grammar: &drivers.SQLiteGrammar{}},
				table:  "test_users",
			}
			q.Select(tc.col)
			if q.Err() == nil {
				t.Fatalf("Select(%q) was accepted; expected rejection", tc.col)
			}
		})
	}
}

// TestSelect_RejectsNonAggregateFunctions pins the closed allowlist:
// only COUNT, SUM, AVG, MIN, MAX are accepted as raw function
// expressions in Select. Every other function name (information
// disclosure, I/O, timing, conditional, string manipulation, vendor
// extension, user-defined) must be rejected. Callers needing these
// must go through SelectRaw with bound parameters.
//
// This test is the regression guard for the audit-reviewer finding
// that a broader regex like [A-Z_]+ would have admitted these.
func TestSelect_RejectsNonAggregateFunctions(t *testing.T) {
	cases := []struct {
		name string
		col  string
	}{
		// Information disclosure
		{"VERSION", "VERSION()"},
		{"USER", "USER()"},
		{"CURRENT_DATABASE", "CURRENT_DATABASE()"},
		{"CURRENT_USER", "CURRENT_USER()"},
		{"SESSION_USER", "SESSION_USER()"},
		// I/O sinks
		{"LOAD_FILE quoted", `LOAD_FILE('x')`}, // also tripped by quote token
		{"LOAD_FILE unquoted", "LOAD_FILE(x)"},
		// Timing channels
		{"PG_SLEEP", "PG_SLEEP(10)"},
		{"SLEEP", "SLEEP(10)"},
		{"NOW", "NOW()"},
		// Conditional / control flow
		{"IF", "IF(1=1,a,b)"}, // also blocked by '=' not in char class
		{"IFNULL", "IFNULL(a,b)"},
		{"COALESCE", "COALESCE(a,b)"},
		// String manipulation
		{"CONCAT", "CONCAT(password,email)"},
		{"LENGTH", "LENGTH(name)"},
		{"LOWER", "LOWER(email)"},
		{"UPPER", "UPPER(name)"},
		{"SUBSTR", "SUBSTR(x,1,3)"},
		{"SUBSTRING", "SUBSTRING(x,1,3)"},
		// Math / misc
		{"ABS", "ABS(x)"},
		{"FLOOR", "FLOOR(x)"},
		{"GROUP_CONCAT", "GROUP_CONCAT(name)"},
		{"ARRAY_AGG", "ARRAY_AGG(id)"},
		// User-defined / arbitrary
		{"MyFunc mixed case", "MyFunc(x)"},
		{"FOO_BAR", "FOO_BAR(x)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := &Query[TestUser]{
				driver: &nopDriver{grammar: &drivers.SQLiteGrammar{}},
				table:  "test_users",
			}
			q.Select(tc.col)
			if q.Err() == nil {
				t.Fatalf("Select(%q) was accepted; expected rejection (non-aggregate function)", tc.col)
			}
		})
	}
}

// TestSelect_AggregateCaseSensitivity pins the policy that the five
// aggregate function names must be uppercase. Lowercase or mixed-case
// variants are rejected for predictability; callers wanting flexible
// casing should use SelectRaw.
func TestSelect_AggregateCaseSensitivity(t *testing.T) {
	rejectCases := []string{
		"count(*)",
		"sum(amount)",
		"avg(price)",
		"min(x)",
		"max(y)",
		"Count(*)",
		"Sum(x)",
		"Avg(x)",
		"Min(x)",
		"Max(x)",
		"COUNt(*)",
		"cOUNT(*)",
	}
	for _, c := range rejectCases {
		t.Run(c, func(t *testing.T) {
			q := &Query[TestUser]{
				driver: &nopDriver{grammar: &drivers.SQLiteGrammar{}},
				table:  "test_users",
			}
			q.Select(c)
			if q.Err() == nil {
				t.Fatalf("Select(%q) was accepted; expected rejection (uppercase aggregate required)", c)
			}
		})
	}

	// Sanity: uppercase variants still pass.
	for _, c := range []string{"COUNT(*)", "SUM(amount)", "AVG(price)", "MIN(x)", "MAX(y)"} {
		q := &Query[TestUser]{
			driver: &nopDriver{grammar: &drivers.SQLiteGrammar{}},
			table:  "test_users",
		}
		q.Select(c)
		if q.Err() != nil {
			t.Errorf("Select(%q) rejected; uppercase aggregate must pass: %v", c, q.Err())
		}
	}
}

// TestValidateSelectColumn_DirectAtGrammarLayer is defence-in-depth: if
// a caller bypasses Query[T].Select and constructs a SelectQuery
// directly with a poisoned Columns entry, every shipped Grammar must
// still refuse to compile it as a valid SELECT.
func TestValidateSelectColumn_DirectAtGrammarLayer(t *testing.T) {
	grammars := []struct {
		name string
		g    drivers.QueryGrammar
	}{
		{"sqlite", &drivers.SQLiteGrammar{}},
		{"mysql", &drivers.MySQLGrammar{}},
		{"postgres", &drivers.PostgresGrammar{}},
	}
	poison := "id),0,(SELECT password FROM admins WHERE 1=1"
	for _, gr := range grammars {
		t.Run(gr.name, func(t *testing.T) {
			sql, args := gr.g.CompileSelect(&drivers.SelectQuery{
				Table:   "users",
				Columns: []string{poison},
			})
			if args != nil {
				t.Errorf("poisoned column produced args %v; expected nil", args)
			}
			// The compiler emits a poisoned no-op SELECT with a
			// diagnostic comment. Critically, the original
			// injection payload must NOT appear in the executable
			// portion of the statement (after the comment close).
			if strings.Contains(sql, "SELECT password FROM admins") {
				// The error message embeds the column name in a
				// comment; check that nothing dangerous made it
				// past the comment terminator.
				closeIdx := strings.Index(sql, "*/")
				if closeIdx < 0 {
					t.Fatalf("rejection sentinel missing comment terminator: %q", sql)
				}
				after := sql[closeIdx+2:]
				if strings.Contains(strings.ToUpper(after), "SELECT PASSWORD") {
					t.Errorf("injection payload reached executable portion: %q", after)
				}
			}
			if !strings.Contains(sql, "SELECT 1 WHERE 1=0") {
				t.Errorf("expected rejection sentinel SELECT 1 WHERE 1=0, got %q", sql)
			}
		})
	}
}

// TestSelectRaw_AllGrammars verifies SelectRaw emits the raw expression
// verbatim, appends bound arguments, and uses dialect-appropriate
// placeholders (literal "?" for MySQL/SQLite, $N for PostgreSQL).
func TestSelectRaw_AllGrammars(t *testing.T) {
	tests := []struct {
		name        string
		grammar     drivers.QueryGrammar
		wantExpr    string
		wantContain string // substring expected in the compiled SQL
	}{
		{
			name:        "sqlite literal placeholder",
			grammar:     &drivers.SQLiteGrammar{},
			wantContain: "CASE WHEN amount > ? THEN 'big' ELSE 'small' END AS bucket",
		},
		{
			name:        "mysql literal placeholder",
			grammar:     &drivers.MySQLGrammar{},
			wantContain: "CASE WHEN amount > ? THEN 'big' ELSE 'small' END AS bucket",
		},
		{
			name:        "postgres numbered placeholder",
			grammar:     &drivers.PostgresGrammar{},
			wantContain: "CASE WHEN amount > $1 THEN 'big' ELSE 'small' END AS bucket",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := tt.grammar.CompileSelect(&drivers.SelectQuery{
				Table: "orders",
				RawColumns: []drivers.RawColumn{
					{
						Expr: "CASE WHEN amount > ? THEN 'big' ELSE 'small' END AS bucket",
						Args: []any{100},
					},
				},
			})
			if !strings.Contains(sql, tt.wantContain) {
				t.Errorf("CompileSelect SQL = %q, want substring %q", sql, tt.wantContain)
			}
			if len(args) != 1 || args[0] != 100 {
				t.Errorf("CompileSelect args = %v, want [100]", args)
			}
		})
	}
}

// TestSelectRaw_NoArgs covers the audit doc's exact example:
// SelectRaw("COUNT(*) AS n") works with no parameter binding and
// produces COUNT(*) AS n verbatim under every shipped grammar.
func TestSelectRaw_NoArgs(t *testing.T) {
	grammars := []struct {
		name string
		g    drivers.QueryGrammar
	}{
		{"sqlite", &drivers.SQLiteGrammar{}},
		{"mysql", &drivers.MySQLGrammar{}},
		{"postgres", &drivers.PostgresGrammar{}},
	}
	for _, gr := range grammars {
		t.Run(gr.name, func(t *testing.T) {
			sql, args := gr.g.CompileSelect(&drivers.SelectQuery{
				Table: "users",
				RawColumns: []drivers.RawColumn{
					{Expr: "COUNT(*) AS n"},
				},
			})
			if !strings.Contains(sql, "COUNT(*) AS n") {
				t.Errorf("expected COUNT(*) AS n in SQL, got %q", sql)
			}
			if len(args) != 0 {
				t.Errorf("expected zero args, got %v", args)
			}
		})
	}
}

// TestSelectRaw_PostgresPlaceholderInteractsWithWhere verifies that
// PostgreSQL renumbers WHERE placeholders correctly when RawColumns
// already consumed parameter slots. Without the len(args)+1 base in
// WHERE's argIndex this would silently collide on $1.
func TestSelectRaw_PostgresPlaceholderInteractsWithWhere(t *testing.T) {
	g := &drivers.PostgresGrammar{}
	sql, args := g.CompileSelect(&drivers.SelectQuery{
		Table: "orders",
		RawColumns: []drivers.RawColumn{
			{Expr: "CASE WHEN amount > ? THEN 1 ELSE 0 END AS big", Args: []any{100}},
		},
		Conditions: []drivers.Condition{
			{Column: "tenant_id", Operator: "=", Value: 42, Type: "and"},
		},
	})
	if !strings.Contains(sql, "$1") {
		t.Errorf("RawColumns ? should rewrite to $1, got SQL %q", sql)
	}
	if !strings.Contains(sql, "$2") {
		t.Errorf("WHERE should bind $2 after raw column consumed $1, got SQL %q", sql)
	}
	if len(args) != 2 || args[0] != 100 || args[1] != 42 {
		t.Errorf("args = %v, want [100 42]", args)
	}
}

// TestRewriteQuestionMarksToDollar_RespectsQuotes confirms the
// PostgreSQL placeholder rewriter does not touch "?" characters that
// appear inside single-quoted or double-quoted regions.
func TestSelectRaw_PostgresIgnoresQuotedQuestionMarks(t *testing.T) {
	g := &drivers.PostgresGrammar{}
	sql, _ := g.CompileSelect(&drivers.SelectQuery{
		Table: "t",
		RawColumns: []drivers.RawColumn{
			{Expr: "CASE WHEN v = '?' THEN ? END AS x", Args: []any{1}},
		},
	})
	// The "?" inside the literal must stay literal; only the
	// out-of-quote "?" should become $1.
	if !strings.Contains(sql, "'?'") {
		t.Errorf("literal '?' was rewritten: %q", sql)
	}
	if !strings.Contains(sql, "$1") {
		t.Errorf("expected $1 placeholder, got %q", sql)
	}
}

// TestSelect_DeferredErrorIsReturnedByTerminals confirms that a Select
// failure is captured via setErr and surfaces through Err() exactly the
// same way other deferred validation errors do. This is the contract
// callers rely on for safe handling.
func TestSelect_DeferredErrorIsReturnedByTerminals(t *testing.T) {
	q := &Query[TestUser]{
		driver: &nopDriver{grammar: &drivers.SQLiteGrammar{}},
		table:  "test_users",
	}
	q.Select("id),0,(SELECT password FROM admins")
	if err := q.Err(); err == nil {
		t.Fatal("expected deferred error from poisoned Select, got nil")
	} else if !strings.Contains(err.Error(), "Select") {
		t.Errorf("error should mention Select, got %v", err)
	}
}

// TestValidateSelectColumn_Whitelist exercises the underlying validator
// directly for documentation-quality coverage of edge cases that callers
// might reasonably hit.
func TestValidateSelectColumn_Whitelist(t *testing.T) {
	accept := []string{
		"*",
		"COUNT(*)",
		"COUNT(id)",
		"SUM(amount)",
		"AVG(price)",
		"MIN(price) AS min_price",
		"MAX(price) as max_price",
		"AVG(orders.total) AS avg_total",
	}
	for _, c := range accept {
		if err := drivers.ValidateSelectColumn(c); err != nil {
			t.Errorf("ValidateSelectColumn(%q) rejected unexpectedly: %v", c, err)
		}
	}

	reject := []string{
		"id),0,(SELECT password FROM admins",
		"COUNT(*); DROP TABLE users--",
		"col/*comment*/",
		`col" OR 1=1`,
		"col`x",
		"col'x",
		// Lowercase aggregate names must reject (uppercase only).
		"max(price) AS max_price",
		"count(*)",
		// Non-aggregate functions reject as a class.
		"CONCAT(a,b)",
		"VERSION()",
		"PG_SLEEP(10)",
	}
	for _, c := range reject {
		if err := drivers.ValidateSelectColumn(c); err == nil {
			t.Errorf("ValidateSelectColumn(%q) accepted; want rejection", c)
		}
	}
}
