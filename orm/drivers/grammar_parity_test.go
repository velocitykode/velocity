package drivers

import (
	"reflect"
	"testing"
)

// These tests lock the cross-grammar parity fixes (B28): empty IN/NOT IN
// collapse to constant booleans in every dialect (matching SQLite, which
// shipped the behaviour first), NOT BETWEEN binds both bounds, and HAVING
// compiles through the same condition machinery as WHERE so IN lists
// expand to placeholder lists instead of binding a slice as one scalar.

func TestMySQLGrammar_CompileSelect_EmptyInAndNotBetween(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "WhereIn with empty slice collapses to 1 = 0 (never matches)",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Conditions: []Condition{
					{Column: "id", Operator: "IN", Value: []any{}, Type: "and"},
				},
			},
			wantSQL:  "SELECT `id` FROM `users` WHERE 1 = 0",
			wantArgs: nil,
		},
		{
			name: "WhereNotIn with empty slice collapses to 1 = 1 (always matches)",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Conditions: []Condition{
					{Column: "id", Operator: "NOT IN", Value: []any{}, Type: "and"},
				},
			},
			wantSQL:  "SELECT `id` FROM `users` WHERE 1 = 1",
			wantArgs: nil,
		},
		{
			name: "WhereNotBetween emits NOT BETWEEN ? AND ? with both bounds bound",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Conditions: []Condition{
					{Column: "age", Operator: "NOT BETWEEN", Value: []any{18, 65}, Type: "and"},
				},
			},
			wantSQL:  "SELECT `id` FROM `users` WHERE `age` NOT BETWEEN ? AND ?",
			wantArgs: []any{18, 65},
		},
		{
			name: "empty IN mixes correctly with other AND-joined conditions",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Conditions: []Condition{
					{Column: "tenant_id", Operator: "=", Value: 7, Type: "and"},
					{Column: "id", Operator: "IN", Value: []any{}, Type: "and"},
				},
			},
			wantSQL:  "SELECT `id` FROM `users` WHERE `tenant_id` = ? AND 1 = 0",
			wantArgs: []any{7},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileSelect(tt.query)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("CompileSelect() args = %#v, want %#v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestPostgresGrammar_CompileSelect_EmptyInAndNotBetween(t *testing.T) {
	grammar := &PostgresGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "WhereIn with empty slice collapses to 1 = 0 (never matches)",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Conditions: []Condition{
					{Column: "id", Operator: "IN", Value: []any{}, Type: "and"},
				},
			},
			wantSQL:  `SELECT "id" FROM "users" WHERE 1 = 0`,
			wantArgs: nil,
		},
		{
			name: "WhereNotIn with empty slice collapses to 1 = 1 (always matches)",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Conditions: []Condition{
					{Column: "id", Operator: "NOT IN", Value: []any{}, Type: "and"},
				},
			},
			wantSQL:  `SELECT "id" FROM "users" WHERE 1 = 1`,
			wantArgs: nil,
		},
		{
			name: "WhereNotBetween emits NOT BETWEEN $1 AND $2 with both bounds bound",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Conditions: []Condition{
					{Column: "age", Operator: "NOT BETWEEN", Value: []any{18, 65}, Type: "and"},
				},
			},
			wantSQL:  `SELECT "id" FROM "users" WHERE "age" NOT BETWEEN $1 AND $2`,
			wantArgs: []any{18, 65},
		},
		{
			name: "empty IN does not consume a placeholder number",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Conditions: []Condition{
					{Column: "tenant_id", Operator: "=", Value: 7, Type: "and"},
					{Column: "id", Operator: "IN", Value: []any{}, Type: "and"},
					{Column: "age", Operator: ">", Value: 21, Type: "and"},
				},
			},
			wantSQL:  `SELECT "id" FROM "users" WHERE "tenant_id" = $1 AND 1 = 0 AND "age" > $2`,
			wantArgs: []any{7, 21},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileSelect(tt.query)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("CompileSelect() args = %#v, want %#v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestGrammars_CompileSelect_HavingInList(t *testing.T) {
	// One HAVING IN query compiled by each grammar; before the dedupe the
	// hand-rolled HAVING blocks bound the []any as a single scalar.
	query := func() *SelectQuery {
		return &SelectQuery{
			Table:   "orders",
			Columns: []string{"user_id", "COUNT(*) as order_count"},
			Conditions: []Condition{
				{Column: "status", Operator: "=", Value: "active", Type: "and"},
			},
			Groups: []string{"user_id"},
			Having: []Condition{
				{Column: "user_id", Operator: "IN", Value: []any{1, 2, 3}, Type: "and"},
			},
		}
	}

	tests := []struct {
		name     string
		grammar  QueryGrammar
		wantSQL  string
		wantArgs []any
	}{
		{
			name:     "sqlite",
			grammar:  &SQLiteGrammar{},
			wantSQL:  "SELECT `user_id`, COUNT(*) as order_count FROM `orders` WHERE `status` = ? GROUP BY `user_id` HAVING `user_id` IN (?, ?, ?)",
			wantArgs: []any{"active", 1, 2, 3},
		},
		{
			name:     "mysql",
			grammar:  &MySQLGrammar{},
			wantSQL:  "SELECT `user_id`, COUNT(*) as order_count FROM `orders` WHERE `status` = ? GROUP BY `user_id` HAVING `user_id` IN (?, ?, ?)",
			wantArgs: []any{"active", 1, 2, 3},
		},
		{
			name:     "postgres",
			grammar:  &PostgresGrammar{},
			wantSQL:  `SELECT "user_id", COUNT(*) as order_count FROM "orders" WHERE "status" = $1 GROUP BY "user_id" HAVING "user_id" IN ($2, $3, $4)`,
			wantArgs: []any{"active", 1, 2, 3},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := tt.grammar.CompileSelect(query())
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("CompileSelect() args = %#v, want %#v", gotArgs, tt.wantArgs)
			}
		})
	}
}
