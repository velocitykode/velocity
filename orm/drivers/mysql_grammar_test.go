package drivers

import "testing"

func TestMySQLGrammar_CompileHasTable(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name  string
		table string
		want  string
	}{
		{
			name:  "returns SHOW TABLES query",
			table: "users",
			want:  "SHOW TABLES LIKE ?",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.CompileHasTable(tt.table)
			if got != tt.want {
				t.Errorf("CompileHasTable() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMySQLGrammar_CompileHasColumn(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name   string
		table  string
		column string
		want   string
	}{
		{
			name:   "returns information_schema query",
			table:  "users",
			column: "email",
			want: `SELECT COUNT(*) FROM information_schema.columns
		WHERE table_schema = ?
		AND table_name = ?
		AND column_name = ?`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.CompileHasColumn(tt.table, tt.column)
			if got != tt.want {
				t.Errorf("CompileHasColumn() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMySQLGrammar_CompileSelect_WithBetween(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "compiles BETWEEN condition",
			query: &SelectQuery{
				Table:   "orders",
				Columns: []string{"id", "amount"},
				Conditions: []Condition{
					{Column: "amount", Operator: "BETWEEN", Value: []any{100, 500}, Type: "and"},
				},
			},
			wantSQL:  "SELECT `id`, `amount` FROM `orders` WHERE `amount` BETWEEN ? AND ?",
			wantArgs: []any{100, 500},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileSelect(tt.query)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if len(gotArgs) != len(tt.wantArgs) {
				t.Errorf("CompileSelect() args length = %d, want %d", len(gotArgs), len(tt.wantArgs))
			}
		})
	}
}

func TestMySQLGrammar_CompileSelect_WithGroupByAndHaving(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "compiles GROUP BY with HAVING",
			query: &SelectQuery{
				Table:   "orders",
				Columns: []string{"user_id", "COUNT(*) as order_count"},
				Groups:  []string{"user_id"},
				Having: []Condition{
					{Column: "order_count", Operator: ">", Value: 5, Type: "and"},
				},
			},
			wantSQL:  "SELECT `user_id`, COUNT(*) as order_count FROM `orders` GROUP BY `user_id` HAVING `order_count` > ?",
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
			wantSQL:  "SELECT `user_id`, MAX(updated_at) as last_update FROM `orders` GROUP BY `user_id` HAVING `last_update` IS NULL",
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
			wantSQL:  "SELECT `user_id`, MAX(updated_at) as last_update FROM `orders` GROUP BY `user_id` HAVING `last_update` IS NOT NULL",
			wantArgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileSelect(tt.query)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if len(gotArgs) != len(tt.wantArgs) {
				t.Errorf("CompileSelect() args length = %d, want %d", len(gotArgs), len(tt.wantArgs))
			}
			for i := range gotArgs {
				if i >= len(tt.wantArgs) {
					break
				}
				if gotArgs[i] != tt.wantArgs[i] {
					t.Errorf("CompileSelect() args[%d] = %v, want %v", i, gotArgs[i], tt.wantArgs[i])
				}
			}
		})
	}
}

func TestMySQLGrammar_CompileSelect_WithOrCondition(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "compiles OR condition",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name"},
				Conditions: []Condition{
					{Column: "role", Operator: "=", Value: "admin", Type: "and"},
					{Column: "role", Operator: "=", Value: "moderator", Type: "or"},
				},
			},
			wantSQL:  "SELECT `id`, `name` FROM `users` WHERE `role` = ? OR `role` = ?",
			wantArgs: []any{"admin", "moderator"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileSelect(tt.query)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if len(gotArgs) != len(tt.wantArgs) {
				t.Errorf("CompileSelect() args length = %d, want %d", len(gotArgs), len(tt.wantArgs))
			}
		})
	}
}

func TestMySQLGrammar_CompileCreateTable_WithIndexes(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name      string
		tblName   string
		table     *Table
		wantParts []string
	}{
		{
			name:    "table with regular index",
			tblName: "users",
			table: &Table{
				Columns: []Column{
					{Name: "id", Type: "INT", Primary: true, AutoIncrement: true},
					{Name: "email", Type: "VARCHAR", Size: 255},
				},
				Indexes: []Index{
					{Name: "idx_email", Columns: []string{"email"}, Unique: false},
				},
			},
			wantParts: []string{
				"CREATE TABLE `users`",
				"INDEX `idx_email` (`email`)",
			},
		},
		{
			name:    "table with unique index",
			tblName: "users",
			table: &Table{
				Columns: []Column{
					{Name: "id", Type: "INT", Primary: true},
					{Name: "email", Type: "VARCHAR", Size: 255},
				},
				Indexes: []Index{
					{Name: "idx_unique_email", Columns: []string{"email"}, Unique: true},
				},
			},
			wantParts: []string{
				"CREATE TABLE `users`",
				"UNIQUE INDEX `idx_unique_email` (`email`)",
			},
		},
		{
			name:    "table with composite index",
			tblName: "user_roles",
			table: &Table{
				Columns: []Column{
					{Name: "user_id", Type: "INT"},
					{Name: "role_id", Type: "INT"},
				},
				Indexes: []Index{
					{Name: "idx_user_role", Columns: []string{"user_id", "role_id"}, Unique: true},
				},
			},
			wantParts: []string{
				"CREATE TABLE `user_roles`",
				"UNIQUE INDEX `idx_user_role` (`user_id`, `role_id`)",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.CompileCreateTable(tt.tblName, tt.table)
			for _, part := range tt.wantParts {
				if !containsString(got, part) {
					t.Errorf("CompileCreateTable() missing part %q, got %q", part, got)
				}
			}
		})
	}
}

func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsSubstring(s, substr))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
