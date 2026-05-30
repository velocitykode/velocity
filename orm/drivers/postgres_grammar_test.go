package drivers

import (
	"strings"
	"testing"
)

func TestPostgresGrammar(t *testing.T) {
	grammar := &PostgresGrammar{}

	t.Run("CompileSelect", func(t *testing.T) {
		query := &SelectQuery{
			Table:   "users",
			Columns: []string{"id", "name", "email"},
			Conditions: []Condition{
				{Column: "active", Operator: "=", Value: true, Type: "and"},
				{Column: "role", Operator: "IN", Value: []any{"admin", "user"}, Type: "and"},
			},
			Orders: []Order{
				{Column: "created_at", Direction: "DESC"},
			},
			Limit:  intPtr(10),
			Offset: intPtr(20),
		}

		sql, args := grammar.CompileSelect(query)

		expectedSQL := `SELECT "id", "name", "email" FROM "users" WHERE "active" = $1 AND "role" IN ($2, $3) ORDER BY "created_at" DESC LIMIT 10 OFFSET 20`
		if sql != expectedSQL {
			t.Errorf("Expected SQL:\n%s\nGot:\n%s", expectedSQL, sql)
		}

		if len(args) != 3 {
			t.Errorf("Expected 3 args, got %d", len(args))
		}
	})

	t.Run("CompileInsert", func(t *testing.T) {
		sql, args := grammar.CompileInsert(
			"users",
			[]string{"name", "email", "active"},
			[][]any{
				{"John", "john@example.com", true},
				{"Jane", "jane@example.com", false},
			},
		)

		expectedSQL := `INSERT INTO "users" ("name", "email", "active") VALUES ($1, $2, $3), ($4, $5, $6) RETURNING id`
		if sql != expectedSQL {
			t.Errorf("Expected SQL:\n%s\nGot:\n%s", expectedSQL, sql)
		}

		if len(args) != 6 {
			t.Errorf("Expected 6 args, got %d", len(args))
		}
	})

	t.Run("CompileUpdate", func(t *testing.T) {
		sql, args := grammar.CompileUpdate(
			"users",
			map[string]any{
				"name":       "Updated Name",
				"updated_at": RawSQL("NOW()"),
			},
			[]Condition{
				{Column: "id", Operator: "=", Value: 1, Type: "and"},
			},
		)

		// Verify SQL contains expected parts (map iteration order varies)
		if !strings.Contains(sql, "UPDATE") || !strings.Contains(sql, "WHERE") {
			t.Errorf("SQL missing expected parts: %s", sql)
		}

		// Note: map iteration order is not guaranteed.
		// name is bound; updated_at is a RawSQL sentinel (emitted verbatim);
		// id is bound.
		if len(args) != 2 {
			t.Errorf("Expected 2 args, got %d", len(args))
		}
	})

	t.Run("QuoteIdentifier", func(t *testing.T) {
		quoted := grammar.QuoteIdentifier("table_name")
		if quoted != `"table_name"` {
			t.Errorf("Expected quoted identifier to be \"table_name\", got %s", quoted)
		}
	})

	t.Run("Placeholder", func(t *testing.T) {
		placeholder := grammar.Placeholder(5)
		if placeholder != "$5" {
			t.Errorf("Expected placeholder $5, got %s", placeholder)
		}
	})
}

func TestPostgresGrammar_CompileSelect_ComplexQueries(t *testing.T) {
	grammar := &PostgresGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "compiles simple select",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name"},
			},
			wantSQL:  `SELECT "id", "name" FROM "users"`,
			wantArgs: nil,
		},
		{
			name: "compiles select with single WHERE",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"*"},
				Conditions: []Condition{
					{Column: "active", Operator: "=", Value: true, Type: "and"},
				},
			},
			wantSQL:  `SELECT * FROM "users" WHERE "active" = $1`,
			wantArgs: []any{true},
		},
		{
			name: "compiles select with multiple WHERE conditions",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name", "email"},
				Conditions: []Condition{
					{Column: "active", Operator: "=", Value: true, Type: "and"},
					{Column: "age", Operator: ">=", Value: 18, Type: "and"},
					{Column: "role", Operator: "=", Value: "admin", Type: "and"},
				},
			},
			wantSQL:  `SELECT "id", "name", "email" FROM "users" WHERE "active" = $1 AND "age" >= $2 AND "role" = $3`,
			wantArgs: []any{true, 18, "admin"},
		},
		{
			name: "compiles select with DISTINCT",
			query: &SelectQuery{
				Table:    "users",
				Columns:  []string{"country"},
				Distinct: true,
			},
			wantSQL:  `SELECT DISTINCT "country" FROM "users"`,
			wantArgs: nil,
		},
		{
			name: "compiles select with ORDER BY single column",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"*"},
				Orders: []Order{
					{Column: "created_at", Direction: "DESC"},
				},
			},
			wantSQL:  `SELECT * FROM "users" ORDER BY "created_at" DESC`,
			wantArgs: nil,
		},
		{
			name: "compiles select with ORDER BY multiple columns",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"*"},
				Orders: []Order{
					{Column: "last_name", Direction: "ASC"},
					{Column: "first_name", Direction: "ASC"},
				},
			},
			wantSQL:  `SELECT * FROM "users" ORDER BY "last_name" ASC, "first_name" ASC`,
			wantArgs: nil,
		},
		{
			name: "compiles select with LIMIT",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"*"},
				Limit:   intPtr(10),
			},
			wantSQL:  `SELECT * FROM "users" LIMIT 10`,
			wantArgs: nil,
		},
		{
			name: "compiles select with LIMIT and OFFSET",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"*"},
				Limit:   intPtr(10),
				Offset:  intPtr(20),
			},
			wantSQL:  `SELECT * FROM "users" LIMIT 10 OFFSET 20`,
			wantArgs: nil,
		},
		{
			name: "compiles select with FOR UPDATE",
			query: &SelectQuery{
				Table:         "users",
				Columns:       []string{"*"},
				LockForUpdate: true,
			},
			wantSQL:  `SELECT * FROM "users" FOR UPDATE`,
			wantArgs: nil,
		},
		{
			name: "compiles select with FOR UPDATE SKIP LOCKED",
			query: &SelectQuery{
				Table:         "jobs",
				Columns:       []string{"*"},
				LockForUpdate: true,
				SkipLocked:    true,
				Limit:         intPtr(1),
			},
			wantSQL:  `SELECT * FROM "jobs" LIMIT 1 FOR UPDATE SKIP LOCKED`,
			wantArgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileSelect(tt.query)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL =\n%q\nwant:\n%q", gotSQL, tt.wantSQL)
			}
			if len(gotArgs) != len(tt.wantArgs) {
				t.Errorf("CompileSelect() args length = %d, want %d", len(gotArgs), len(tt.wantArgs))
			}
			for i := range gotArgs {
				if i < len(tt.wantArgs) && gotArgs[i] != tt.wantArgs[i] {
					t.Errorf("CompileSelect() arg[%d] = %v, want %v", i, gotArgs[i], tt.wantArgs[i])
				}
			}
		})
	}
}

func TestPostgresGrammar_CompileSelect_JOINQueries(t *testing.T) {
	grammar := &PostgresGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "compiles INNER JOIN",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"users.id", "users.name", "roles.name"},
				Joins: []Join{
					{Type: "INNER", Table: "roles", On: "users.role_id = roles.id"},
				},
			},
			wantSQL:  `SELECT "users.id", "users.name", "roles.name" FROM "users" INNER JOIN "roles" ON users.role_id = roles.id`,
			wantArgs: nil,
		},
		{
			name: "compiles LEFT JOIN",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"users.*", "profiles.bio"},
				Joins: []Join{
					{Type: "LEFT", Table: "profiles", On: "users.id = profiles.user_id"},
				},
			},
			wantSQL:  `SELECT "users.*", "profiles.bio" FROM "users" LEFT JOIN "profiles" ON users.id = profiles.user_id`,
			wantArgs: nil,
		},
		{
			name: "compiles multiple JOINs",
			query: &SelectQuery{
				Table:   "orders",
				Columns: []string{"orders.id", "users.name", "products.title"},
				Joins: []Join{
					{Type: "INNER", Table: "users", On: "orders.user_id = users.id"},
					{Type: "LEFT", Table: "products", On: "orders.product_id = products.id"},
				},
			},
			wantSQL:  `SELECT "orders.id", "users.name", "products.title" FROM "orders" INNER JOIN "users" ON orders.user_id = users.id LEFT JOIN "products" ON orders.product_id = products.id`,
			wantArgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileSelect(tt.query)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL =\n%q\nwant:\n%q", gotSQL, tt.wantSQL)
			}
			if len(gotArgs) != len(tt.wantArgs) {
				t.Errorf("CompileSelect() args length = %d, want %d", len(gotArgs), len(tt.wantArgs))
			}
		})
	}
}

func TestPostgresGrammar_CompileSelect_WithGroupByAndHaving(t *testing.T) {
	grammar := &PostgresGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "compiles GROUP BY with HAVING comparison",
			query: &SelectQuery{
				Table:   "orders",
				Columns: []string{"user_id", "COUNT(*) as order_count"},
				Groups:  []string{"user_id"},
				Having: []Condition{
					{Column: "order_count", Operator: ">", Value: 5, Type: "and"},
				},
			},
			wantSQL:  `SELECT "user_id", COUNT(*) as order_count FROM "orders" GROUP BY "user_id" HAVING "order_count" > $1`,
			wantArgs: []any{5},
		},
		{
			name: "compiles HAVING with IS NULL (no placeholder, no args)",
			query: &SelectQuery{
				Table:   "orders",
				Columns: []string{"user_id", "MAX(updated_at) as last_update"},
				Groups:  []string{"user_id"},
				Having: []Condition{
					{Column: "last_update", Operator: "IS NULL", Type: "and"},
				},
			},
			wantSQL:  `SELECT "user_id", MAX(updated_at) as last_update FROM "orders" GROUP BY "user_id" HAVING "last_update" IS NULL`,
			wantArgs: nil,
		},
		{
			name: "compiles HAVING with IS NOT NULL (no placeholder, no args)",
			query: &SelectQuery{
				Table:   "orders",
				Columns: []string{"user_id", "MAX(updated_at) as last_update"},
				Groups:  []string{"user_id"},
				Having: []Condition{
					{Column: "last_update", Operator: "IS NOT NULL", Type: "and"},
				},
			},
			wantSQL:  `SELECT "user_id", MAX(updated_at) as last_update FROM "orders" GROUP BY "user_id" HAVING "last_update" IS NOT NULL`,
			wantArgs: nil,
		},
		{
			name: "compiles HAVING mixing IS NULL with comparison renumbers placeholders correctly",
			query: &SelectQuery{
				Table:   "orders",
				Columns: []string{"user_id", "COUNT(*) as order_count"},
				Groups:  []string{"user_id"},
				Having: []Condition{
					{Column: "last_update", Operator: "IS NOT NULL", Type: "and"},
					{Column: "order_count", Operator: ">", Value: 5, Type: "and"},
				},
			},
			wantSQL:  `SELECT "user_id", COUNT(*) as order_count FROM "orders" GROUP BY "user_id" HAVING "last_update" IS NOT NULL AND "order_count" > $1`,
			wantArgs: []any{5},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileSelect(tt.query)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL =\n%q\nwant:\n%q", gotSQL, tt.wantSQL)
			}
			if len(gotArgs) != len(tt.wantArgs) {
				t.Errorf("CompileSelect() args length = %d, want %d", len(gotArgs), len(tt.wantArgs))
			}
			for i := range gotArgs {
				if i < len(tt.wantArgs) && gotArgs[i] != tt.wantArgs[i] {
					t.Errorf("CompileSelect() arg[%d] = %v, want %v", i, gotArgs[i], tt.wantArgs[i])
				}
			}
		})
	}
}
