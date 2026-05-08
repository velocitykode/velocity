package drivers

import (
	"reflect"
	"strings"
	"testing"
)

// =============================================================================
// SQLite Grammar Tests
// =============================================================================

func TestSQLiteGrammar_CompileSelect(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "basic select all columns",
			query: &SelectQuery{
				Table: "users",
			},
			wantSQL:  "SELECT * FROM `users`",
			wantArgs: nil,
		},
		{
			name: "select with specific columns",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name", "email"},
			},
			wantSQL:  "SELECT `id`, `name`, `email` FROM `users`",
			wantArgs: nil,
		},
		{
			name: "select with conditions",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name"},
				Conditions: []Condition{
					{Column: "active", Operator: "=", Value: true, Type: "and"},
					{Column: "role", Operator: "=", Value: "admin", Type: "and"},
				},
			},
			wantSQL:  "SELECT `id`, `name` FROM `users` WHERE `active` = ? AND `role` = ?",
			wantArgs: []any{true, "admin"},
		},
		{
			name: "select distinct",
			query: &SelectQuery{
				Table:    "users",
				Columns:  []string{"role"},
				Distinct: true,
			},
			wantSQL:  "SELECT DISTINCT `role` FROM `users`",
			wantArgs: nil,
		},
		{
			name: "select with IS NULL condition",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name"},
				Conditions: []Condition{
					{Column: "deleted_at", Operator: "IS NULL", Value: nil, Type: "and"},
				},
			},
			wantSQL:  "SELECT `id`, `name` FROM `users` WHERE `deleted_at` IS NULL",
			wantArgs: nil,
		},
		{
			name: "select with JOIN",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"users.id", "posts.title"},
				Joins: []Join{
					{Type: "INNER", Table: "posts", On: "users.id = posts.user_id"},
				},
			},
			wantSQL:  "SELECT `users.id`, `posts.title` FROM `users` INNER JOIN `posts` ON users.id = posts.user_id",
			wantArgs: nil,
		},
		{
			name: "select with ORDER BY",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name"},
				Orders: []Order{
					{Column: "created_at", Direction: "DESC"},
					{Column: "name", Direction: "ASC"},
				},
			},
			wantSQL:  "SELECT `id`, `name` FROM `users` ORDER BY `created_at` DESC, `name` ASC",
			wantArgs: nil,
		},
		{
			name: "select with LIMIT and OFFSET",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name"},
				Limit:   intPtr(10),
				Offset:  intPtr(20),
			},
			wantSQL:  "SELECT `id`, `name` FROM `users` LIMIT 10 OFFSET 20",
			wantArgs: nil,
		},
		{
			name: "complex select with all clauses",
			query: &SelectQuery{
				Table:    "users",
				Columns:  []string{"id", "name", "email"},
				Distinct: true,
				Conditions: []Condition{
					{Column: "active", Operator: "=", Value: true, Type: "and"},
				},
				Orders: []Order{
					{Column: "created_at", Direction: "DESC"},
				},
				Limit:  intPtr(10),
				Offset: intPtr(0),
			},
			wantSQL:  "SELECT DISTINCT `id`, `name`, `email` FROM `users` WHERE `active` = ? ORDER BY `created_at` DESC LIMIT 10 OFFSET 0",
			wantArgs: []any{true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileSelect(tt.query)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("CompileSelect() args = %v, want %v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestSQLiteGrammar_CompileInsert(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name     string
		table    string
		columns  []string
		values   [][]any
		wantSQL  string
		wantArgs []any
	}{
		{
			name:    "single row insert",
			table:   "users",
			columns: []string{"name", "email"},
			values: [][]any{
				{"John", "john@example.com"},
			},
			wantSQL:  "INSERT INTO `users` (`name`, `email`) VALUES (?, ?)",
			wantArgs: []any{"John", "john@example.com"},
		},
		{
			name:    "multi-row insert",
			table:   "users",
			columns: []string{"name", "email", "active"},
			values: [][]any{
				{"John", "john@example.com", true},
				{"Jane", "jane@example.com", false},
			},
			wantSQL:  "INSERT INTO `users` (`name`, `email`, `active`) VALUES (?, ?, ?), (?, ?, ?)",
			wantArgs: []any{"John", "john@example.com", true, "Jane", "jane@example.com", false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileInsert(tt.table, tt.columns, tt.values)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileInsert() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("CompileInsert() args = %v, want %v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestSQLiteGrammar_CompileUpdate(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name       string
		table      string
		values     map[string]any
		conditions []Condition
		wantParts  []string // Parts that must be in the SQL (due to map iteration order)
		wantArgLen int
	}{
		{
			name:  "update without conditions",
			table: "users",
			values: map[string]any{
				"name": "Updated Name",
			},
			conditions: nil,
			wantParts:  []string{"UPDATE `users` SET", "`name` = ?"},
			wantArgLen: 1,
		},
		{
			name:  "update with conditions",
			table: "users",
			values: map[string]any{
				"name": "Updated Name",
			},
			conditions: []Condition{
				{Column: "id", Operator: "=", Value: 1, Type: "and"},
			},
			wantParts:  []string{"UPDATE `users` SET", "`name` = ?", "WHERE `id` = ?"},
			wantArgLen: 2,
		},
		{
			name:  "update with multiple values",
			table: "users",
			values: map[string]any{
				"name":  "Updated Name",
				"email": "updated@example.com",
			},
			conditions: []Condition{
				{Column: "id", Operator: "=", Value: 1, Type: "and"},
			},
			wantParts:  []string{"UPDATE `users` SET", "WHERE `id` = ?"},
			wantArgLen: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileUpdate(tt.table, tt.values, tt.conditions)
			for _, part := range tt.wantParts {
				if !strings.Contains(gotSQL, part) {
					t.Errorf("CompileUpdate() SQL missing part %q, got %q", part, gotSQL)
				}
			}
			if len(gotArgs) != tt.wantArgLen {
				t.Errorf("CompileUpdate() args count = %d, want %d", len(gotArgs), tt.wantArgLen)
			}
		})
	}
}

func TestSQLiteGrammar_CompileDelete(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name       string
		table      string
		conditions []Condition
		wantSQL    string
		wantArgs   []any
	}{
		{
			name:       "delete without conditions",
			table:      "users",
			conditions: nil,
			wantSQL:    "DELETE FROM `users`",
			wantArgs:   nil,
		},
		{
			name:  "delete with conditions",
			table: "users",
			conditions: []Condition{
				{Column: "id", Operator: "=", Value: 1, Type: "and"},
			},
			wantSQL:  "DELETE FROM `users` WHERE `id` = ?",
			wantArgs: []any{1},
		},
		{
			name:  "delete with multiple conditions",
			table: "users",
			conditions: []Condition{
				{Column: "active", Operator: "=", Value: false, Type: "and"},
				{Column: "role", Operator: "=", Value: "guest", Type: "and"},
			},
			wantSQL:  "DELETE FROM `users` WHERE `active` = ? AND `role` = ?",
			wantArgs: []any{false, "guest"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileDelete(tt.table, tt.conditions)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileDelete() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("CompileDelete() args = %v, want %v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestSQLiteGrammar_CompileCreateTable(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name    string
		tblName string
		table   *Table
		want    string
	}{
		{
			name:    "basic table with primary key and autoincrement",
			tblName: "users",
			table: &Table{
				Columns: []Column{
					{Name: "id", Type: "INTEGER", Primary: true, AutoIncrement: true, Nullable: true},
					{Name: "name", Type: "VARCHAR", Nullable: false},
				},
			},
			want: "CREATE TABLE `users` (`id` INTEGER PRIMARY KEY AUTOINCREMENT, `name` TEXT NOT NULL)",
		},
		{
			name:    "table with unique and default",
			tblName: "users",
			table: &Table{
				Columns: []Column{
					{Name: "id", Type: "INTEGER", Primary: true, Nullable: true},
					{Name: "email", Type: "VARCHAR", Unique: true, Nullable: true},
					{Name: "active", Type: "BOOLEAN", Default: true, Nullable: true},
				},
			},
			want: "CREATE TABLE `users` (`id` INTEGER PRIMARY KEY, `email` TEXT UNIQUE, `active` INTEGER DEFAULT true)",
		},
		{
			name:    "table with not null constraint",
			tblName: "posts",
			table: &Table{
				Columns: []Column{
					{Name: "id", Type: "INTEGER", Primary: true, AutoIncrement: true, Nullable: true},
					{Name: "title", Type: "VARCHAR", Nullable: false},
					{Name: "content", Type: "TEXT", Nullable: true},
				},
			},
			want: "CREATE TABLE `posts` (`id` INTEGER PRIMARY KEY AUTOINCREMENT, `title` TEXT NOT NULL, `content` TEXT)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.CompileCreateTable(tt.tblName, tt.table)
			if got != tt.want {
				t.Errorf("CompileCreateTable() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSQLiteGrammar_CompileDropTable(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name  string
		table string
		want  string
	}{
		{
			name:  "drop table",
			table: "users",
			want:  "DROP TABLE IF EXISTS `users`",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.CompileDropTable(tt.table)
			if got != tt.want {
				t.Errorf("CompileDropTable() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSQLiteGrammar_QuoteIdentifier(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple identifier", "users", "`users`"},
		{"identifier with underscore", "user_roles", "`user_roles`"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.QuoteIdentifier(tt.input)
			if got != tt.want {
				t.Errorf("QuoteIdentifier() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSQLiteGrammar_QuoteString(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple string", "hello", "'hello'"},
		{"string with quote", "it's a test", "'it''s a test'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.QuoteString(tt.input)
			if got != tt.want {
				t.Errorf("QuoteString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSQLiteGrammar_Placeholder(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name  string
		index int
		want  string
	}{
		{"first placeholder", 1, "?"},
		{"fifth placeholder", 5, "?"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.Placeholder(tt.index)
			if got != tt.want {
				t.Errorf("Placeholder() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSQLiteGrammar_getSQLiteType(t *testing.T) {
	grammar := &SQLiteGrammar{}

	tests := []struct {
		name string
		typ  string
		want string
	}{
		{"INT to INTEGER", "INT", "INTEGER"},
		{"INTEGER stays INTEGER", "INTEGER", "INTEGER"},
		{"BIGINT to INTEGER", "BIGINT", "INTEGER"},
		{"SMALLINT to INTEGER", "SMALLINT", "INTEGER"},
		{"TINYINT to INTEGER", "TINYINT", "INTEGER"},
		{"DECIMAL to REAL", "DECIMAL", "REAL"},
		{"FLOAT to REAL", "FLOAT", "REAL"},
		{"DOUBLE to REAL", "DOUBLE", "REAL"},
		{"VARCHAR to TEXT", "VARCHAR", "TEXT"},
		{"CHAR to TEXT", "CHAR", "TEXT"},
		{"TEXT stays TEXT", "TEXT", "TEXT"},
		{"BLOB stays BLOB", "BLOB", "BLOB"},
		{"BOOLEAN to INTEGER", "BOOLEAN", "INTEGER"},
		{"BOOL to INTEGER", "BOOL", "INTEGER"},
		{"DATE to TEXT", "DATE", "TEXT"},
		{"DATETIME to TEXT", "DATETIME", "TEXT"},
		{"TIMESTAMP to TEXT", "TIMESTAMP", "TEXT"},
		{"JSON to TEXT", "JSON", "TEXT"},
		{"JSONB to TEXT", "JSONB", "TEXT"},
		{"unknown type passthrough", "CUSTOM", "CUSTOM"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.getSQLiteType(tt.typ)
			if got != tt.want {
				t.Errorf("getSQLiteType(%q) = %q, want %q", tt.typ, got, tt.want)
			}
		})
	}
}

// =============================================================================
// MySQL Grammar Tests
// =============================================================================

func TestMySQLGrammar_CompileSelect(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "basic select all columns",
			query: &SelectQuery{
				Table: "users",
			},
			wantSQL:  "SELECT * FROM `users`",
			wantArgs: nil,
		},
		{
			name: "select with specific columns",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name", "email"},
			},
			wantSQL:  "SELECT `id`, `name`, `email` FROM `users`",
			wantArgs: nil,
		},
		{
			name: "select with conditions",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name"},
				Conditions: []Condition{
					{Column: "active", Operator: "=", Value: true, Type: "and"},
					{Column: "role", Operator: "=", Value: "admin", Type: "and"},
				},
			},
			wantSQL:  "SELECT `id`, `name` FROM `users` WHERE `active` = ? AND `role` = ?",
			wantArgs: []any{true, "admin"},
		},
		{
			name: "select distinct",
			query: &SelectQuery{
				Table:    "users",
				Columns:  []string{"role"},
				Distinct: true,
			},
			wantSQL:  "SELECT DISTINCT `role` FROM `users`",
			wantArgs: nil,
		},
		{
			name: "select with IS NULL condition",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name"},
				Conditions: []Condition{
					{Column: "deleted_at", Operator: "IS NULL", Value: nil, Type: "and"},
				},
			},
			wantSQL:  "SELECT `id`, `name` FROM `users` WHERE `deleted_at` IS NULL",
			wantArgs: nil,
		},
		{
			name: "select with IN condition",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name"},
				Conditions: []Condition{
					{Column: "role", Operator: "IN", Value: []any{"admin", "moderator"}, Type: "and"},
				},
			},
			wantSQL:  "SELECT `id`, `name` FROM `users` WHERE `role` IN (?, ?)",
			wantArgs: []any{"admin", "moderator"},
		},
		{
			name: "select with JOIN",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"*"},
				Joins: []Join{
					{Type: "LEFT", Table: "posts", On: "users.id = posts.user_id"},
				},
			},
			wantSQL:  "SELECT * FROM `users` LEFT JOIN `posts` ON users.id = posts.user_id",
			wantArgs: nil,
		},
		{
			name: "select with ORDER BY",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name"},
				Orders: []Order{
					{Column: "created_at", Direction: "DESC"},
				},
			},
			wantSQL:  "SELECT `id`, `name` FROM `users` ORDER BY `created_at` DESC",
			wantArgs: nil,
		},
		{
			name: "select with LIMIT",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Limit:   intPtr(10),
			},
			wantSQL:  "SELECT `id` FROM `users` LIMIT 10",
			wantArgs: nil,
		},
		{
			name: "select with FOR UPDATE",
			query: &SelectQuery{
				Table:         "users",
				Columns:       []string{"id"},
				LockForUpdate: true,
			},
			wantSQL:  "SELECT `id` FROM `users` FOR UPDATE",
			wantArgs: nil,
		},
		{
			name: "select with FOR UPDATE SKIP LOCKED",
			query: &SelectQuery{
				Table:         "users",
				Columns:       []string{"id"},
				LockForUpdate: true,
				SkipLocked:    true,
			},
			wantSQL:  "SELECT `id` FROM `users` FOR UPDATE SKIP LOCKED",
			wantArgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileSelect(tt.query)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("CompileSelect() args = %v, want %v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestMySQLGrammar_CompileInsert(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name     string
		table    string
		columns  []string
		values   [][]any
		wantSQL  string
		wantArgs []any
	}{
		{
			name:    "single row insert",
			table:   "users",
			columns: []string{"name", "email"},
			values: [][]any{
				{"John", "john@example.com"},
			},
			wantSQL:  "INSERT INTO `users` (`name`, `email`) VALUES (?, ?)",
			wantArgs: []any{"John", "john@example.com"},
		},
		{
			name:    "multi-row insert",
			table:   "users",
			columns: []string{"name", "email"},
			values: [][]any{
				{"John", "john@example.com"},
				{"Jane", "jane@example.com"},
			},
			wantSQL:  "INSERT INTO `users` (`name`, `email`) VALUES (?, ?), (?, ?)",
			wantArgs: []any{"John", "john@example.com", "Jane", "jane@example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileInsert(tt.table, tt.columns, tt.values)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileInsert() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("CompileInsert() args = %v, want %v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestMySQLGrammar_CompileUpdate(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name       string
		table      string
		values     map[string]any
		conditions []Condition
		wantParts  []string
		wantArgLen int
	}{
		{
			name:  "update without conditions",
			table: "users",
			values: map[string]any{
				"name": "Updated Name",
			},
			conditions: nil,
			wantParts:  []string{"UPDATE `users` SET", "`name` = ?"},
			wantArgLen: 1,
		},
		{
			name:  "update with conditions",
			table: "users",
			values: map[string]any{
				"name": "Updated Name",
			},
			conditions: []Condition{
				{Column: "id", Operator: "=", Value: 1, Type: "and"},
			},
			wantParts:  []string{"UPDATE `users` SET", "WHERE `id` = ?"},
			wantArgLen: 2,
		},
		{
			name:  "update with RawSQL NOW() sentinel emits verbatim",
			table: "users",
			values: map[string]any{
				"updated_at": RawSQL("NOW()"),
			},
			conditions: []Condition{
				{Column: "id", Operator: "=", Value: 1, Type: "and"},
			},
			wantParts:  []string{"UPDATE `users` SET", "`updated_at` = NOW()", "WHERE `id` = ?"},
			wantArgLen: 1,
		},
		{
			name:  "update with plain string value equal to NOW() is bound as a parameter",
			table: "users",
			values: map[string]any{
				"comment": "NOW()",
			},
			conditions: []Condition{
				{Column: "id", Operator: "=", Value: 1, Type: "and"},
			},
			wantParts:  []string{"UPDATE `users` SET", "`comment` = ?", "WHERE `id` = ?"},
			wantArgLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileUpdate(tt.table, tt.values, tt.conditions)
			for _, part := range tt.wantParts {
				if !strings.Contains(gotSQL, part) {
					t.Errorf("CompileUpdate() SQL missing part %q, got %q", part, gotSQL)
				}
			}
			if len(gotArgs) != tt.wantArgLen {
				t.Errorf("CompileUpdate() args count = %d, want %d", len(gotArgs), tt.wantArgLen)
			}
		})
	}
}

func TestMySQLGrammar_CompileDelete(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name       string
		table      string
		conditions []Condition
		wantSQL    string
		wantArgs   []any
	}{
		{
			name:       "delete without conditions",
			table:      "users",
			conditions: nil,
			wantSQL:    "DELETE FROM `users`",
			wantArgs:   nil,
		},
		{
			name:  "delete with conditions",
			table: "users",
			conditions: []Condition{
				{Column: "id", Operator: "=", Value: 1, Type: "and"},
			},
			wantSQL:  "DELETE FROM `users` WHERE `id` = ?",
			wantArgs: []any{1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileDelete(tt.table, tt.conditions)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileDelete() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("CompileDelete() args = %v, want %v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestMySQLGrammar_CompileCreateTable(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name      string
		tblName   string
		table     *Table
		wantParts []string
	}{
		{
			name:    "table with primary key and auto_increment",
			tblName: "users",
			table: &Table{
				Columns: []Column{
					{Name: "id", Type: "INT", Primary: true, AutoIncrement: true},
					{Name: "name", Type: "VARCHAR", Size: 255, Nullable: false},
				},
			},
			wantParts: []string{
				"CREATE TABLE `users`",
				"`id` INT AUTO_INCREMENT NOT NULL PRIMARY KEY",
				"`name` VARCHAR(255) NOT NULL",
				"ENGINE=InnoDB DEFAULT CHARSET=utf8mb4",
			},
		},
		{
			name:    "table with unique and default",
			tblName: "users",
			table: &Table{
				Columns: []Column{
					{Name: "id", Type: "INT", Primary: true},
					{Name: "email", Type: "VARCHAR", Size: 255, Unique: true},
					{Name: "active", Type: "BOOLEAN", Default: true},
				},
			},
			wantParts: []string{
				"CREATE TABLE `users`",
				"`id` INT NOT NULL PRIMARY KEY",
				"`email` VARCHAR(255) NOT NULL UNIQUE",
				"`active` TINYINT(1) NOT NULL DEFAULT 1",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.CompileCreateTable(tt.tblName, tt.table)
			for _, part := range tt.wantParts {
				if !strings.Contains(got, part) {
					t.Errorf("CompileCreateTable() missing part %q, got %q", part, got)
				}
			}
		})
	}
}

func TestMySQLGrammar_CompileDropTable(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name  string
		table string
		want  string
	}{
		{
			name:  "drop table",
			table: "users",
			want:  "DROP TABLE IF EXISTS `users`",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.CompileDropTable(tt.table)
			if got != tt.want {
				t.Errorf("CompileDropTable() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMySQLGrammar_QuoteIdentifier(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple identifier", "users", "`users`"},
		{"identifier with underscore", "user_roles", "`user_roles`"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.QuoteIdentifier(tt.input)
			if got != tt.want {
				t.Errorf("QuoteIdentifier() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMySQLGrammar_QuoteString(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple string", "hello", "'hello'"},
		{"string with quote", "it's a test", "'it''s a test'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.QuoteString(tt.input)
			if got != tt.want {
				t.Errorf("QuoteString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMySQLGrammar_Placeholder(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name  string
		index int
		want  string
	}{
		{"first placeholder", 1, "?"},
		{"fifth placeholder", 5, "?"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.Placeholder(tt.index)
			if got != tt.want {
				t.Errorf("Placeholder() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMySQLGrammar_getMySQLType(t *testing.T) {
	grammar := &MySQLGrammar{}

	tests := []struct {
		name   string
		column Column
		want   string
	}{
		{"INT", Column{Type: "INT"}, "INT"},
		{"INTEGER", Column{Type: "INTEGER"}, "INT"},
		{"BIGINT", Column{Type: "BIGINT"}, "BIGINT"},
		{"SMALLINT", Column{Type: "SMALLINT"}, "SMALLINT"},
		{"TINYINT", Column{Type: "TINYINT"}, "TINYINT"},
		{"DECIMAL with size", Column{Type: "DECIMAL", Size: 10}, "DECIMAL(10)"},
		{"DECIMAL without size", Column{Type: "DECIMAL"}, "DECIMAL(10,2)"},
		{"FLOAT", Column{Type: "FLOAT"}, "FLOAT"},
		{"DOUBLE", Column{Type: "DOUBLE"}, "DOUBLE"},
		{"VARCHAR with size", Column{Type: "VARCHAR", Size: 100}, "VARCHAR(100)"},
		{"VARCHAR without size", Column{Type: "VARCHAR"}, "VARCHAR(255)"},
		{"CHAR with size", Column{Type: "CHAR", Size: 10}, "CHAR(10)"},
		{"CHAR without size", Column{Type: "CHAR"}, "CHAR(1)"},
		{"TEXT", Column{Type: "TEXT"}, "TEXT"},
		{"LONGTEXT", Column{Type: "LONGTEXT"}, "LONGTEXT"},
		{"BLOB", Column{Type: "BLOB"}, "BLOB"},
		{"BOOLEAN", Column{Type: "BOOLEAN"}, "TINYINT(1)"},
		{"BOOL", Column{Type: "BOOL"}, "TINYINT(1)"},
		{"DATE", Column{Type: "DATE"}, "DATE"},
		{"TIME", Column{Type: "TIME"}, "TIME"},
		{"DATETIME", Column{Type: "DATETIME"}, "DATETIME"},
		{"TIMESTAMP", Column{Type: "TIMESTAMP"}, "TIMESTAMP"},
		{"JSON", Column{Type: "JSON"}, "JSON"},
		{"UUID", Column{Type: "UUID"}, "CHAR(36)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.getMySQLType(tt.column)
			if got != tt.want {
				t.Errorf("getMySQLType() = %q, want %q", got, tt.want)
			}
		})
	}
}

// =============================================================================
// PostgreSQL Grammar Tests
// =============================================================================

func TestPostgresGrammar_CompileSelect(t *testing.T) {
	grammar := &PostgresGrammar{}

	tests := []struct {
		name     string
		query    *SelectQuery
		wantSQL  string
		wantArgs []any
	}{
		{
			name: "basic select all columns",
			query: &SelectQuery{
				Table: "users",
			},
			wantSQL:  `SELECT * FROM "users"`,
			wantArgs: nil,
		},
		{
			name: "select with specific columns",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name", "email"},
			},
			wantSQL:  `SELECT "id", "name", "email" FROM "users"`,
			wantArgs: nil,
		},
		{
			name: "select with conditions",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name"},
				Conditions: []Condition{
					{Column: "active", Operator: "=", Value: true, Type: "and"},
					{Column: "role", Operator: "=", Value: "admin", Type: "and"},
				},
			},
			wantSQL:  `SELECT "id", "name" FROM "users" WHERE "active" = $1 AND "role" = $2`,
			wantArgs: []any{true, "admin"},
		},
		{
			name: "select distinct",
			query: &SelectQuery{
				Table:    "users",
				Columns:  []string{"role"},
				Distinct: true,
			},
			wantSQL:  `SELECT DISTINCT "role" FROM "users"`,
			wantArgs: nil,
		},
		{
			name: "select with IS NULL condition",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name"},
				Conditions: []Condition{
					{Column: "deleted_at", Operator: "IS NULL", Value: nil, Type: "and"},
				},
			},
			wantSQL:  `SELECT "id", "name" FROM "users" WHERE "deleted_at" IS NULL`,
			wantArgs: nil,
		},
		{
			name: "select with IN condition",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name"},
				Conditions: []Condition{
					{Column: "role", Operator: "IN", Value: []any{"admin", "moderator"}, Type: "and"},
				},
			},
			wantSQL:  `SELECT "id", "name" FROM "users" WHERE "role" IN ($1, $2)`,
			wantArgs: []any{"admin", "moderator"},
		},
		{
			name: "select with JOIN",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"*"},
				Joins: []Join{
					{Type: "LEFT", Table: "posts", On: "users.id = posts.user_id"},
				},
			},
			wantSQL:  `SELECT * FROM "users" LEFT JOIN "posts" ON users.id = posts.user_id`,
			wantArgs: nil,
		},
		{
			name: "select with ORDER BY",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id", "name"},
				Orders: []Order{
					{Column: "created_at", Direction: "DESC"},
				},
			},
			wantSQL:  `SELECT "id", "name" FROM "users" ORDER BY "created_at" DESC`,
			wantArgs: nil,
		},
		{
			name: "select with LIMIT and OFFSET",
			query: &SelectQuery{
				Table:   "users",
				Columns: []string{"id"},
				Limit:   intPtr(10),
				Offset:  intPtr(20),
			},
			wantSQL:  `SELECT "id" FROM "users" LIMIT 10 OFFSET 20`,
			wantArgs: nil,
		},
		{
			name: "select with FOR UPDATE",
			query: &SelectQuery{
				Table:         "users",
				Columns:       []string{"id"},
				LockForUpdate: true,
			},
			wantSQL:  `SELECT "id" FROM "users" FOR UPDATE`,
			wantArgs: nil,
		},
		{
			name: "select with FOR UPDATE SKIP LOCKED",
			query: &SelectQuery{
				Table:         "users",
				Columns:       []string{"id"},
				LockForUpdate: true,
				SkipLocked:    true,
			},
			wantSQL:  `SELECT "id" FROM "users" FOR UPDATE SKIP LOCKED`,
			wantArgs: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileSelect(tt.query)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileSelect() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("CompileSelect() args = %v, want %v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestPostgresGrammar_CompileInsert(t *testing.T) {
	grammar := &PostgresGrammar{}

	tests := []struct {
		name     string
		table    string
		columns  []string
		values   [][]any
		wantSQL  string
		wantArgs []any
	}{
		{
			name:    "single row insert with RETURNING",
			table:   "users",
			columns: []string{"name", "email"},
			values: [][]any{
				{"John", "john@example.com"},
			},
			wantSQL:  `INSERT INTO "users" ("name", "email") VALUES ($1, $2) RETURNING id`,
			wantArgs: []any{"John", "john@example.com"},
		},
		{
			name:    "multi-row insert with RETURNING",
			table:   "users",
			columns: []string{"name", "email"},
			values: [][]any{
				{"John", "john@example.com"},
				{"Jane", "jane@example.com"},
			},
			wantSQL:  `INSERT INTO "users" ("name", "email") VALUES ($1, $2), ($3, $4) RETURNING id`,
			wantArgs: []any{"John", "john@example.com", "Jane", "jane@example.com"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileInsert(tt.table, tt.columns, tt.values)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileInsert() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("CompileInsert() args = %v, want %v", gotArgs, tt.wantArgs)
			}
		})
	}
}

func TestPostgresGrammar_CompileUpdate(t *testing.T) {
	grammar := &PostgresGrammar{}

	tests := []struct {
		name       string
		table      string
		values     map[string]any
		conditions []Condition
		wantParts  []string
		wantArgLen int
	}{
		{
			name:  "update without conditions",
			table: "users",
			values: map[string]any{
				"name": "Updated Name",
			},
			conditions: nil,
			wantParts:  []string{`UPDATE "users" SET`, `"name" = $1`},
			wantArgLen: 1,
		},
		{
			name:  "update with conditions",
			table: "users",
			values: map[string]any{
				"name": "Updated Name",
			},
			conditions: []Condition{
				{Column: "id", Operator: "=", Value: 1, Type: "and"},
			},
			wantParts:  []string{`UPDATE "users" SET`, `WHERE "id" =`},
			wantArgLen: 2,
		},
		{
			name:  "update with RawSQL NOW() sentinel emits verbatim",
			table: "users",
			values: map[string]any{
				"updated_at": RawSQL("NOW()"),
			},
			conditions: []Condition{
				{Column: "id", Operator: "=", Value: 1, Type: "and"},
			},
			wantParts:  []string{`UPDATE "users" SET`, `"updated_at" = NOW()`, `WHERE "id" =`},
			wantArgLen: 1,
		},
		{
			name:  "update with plain string value equal to NOW() is bound as a parameter",
			table: "users",
			values: map[string]any{
				"comment": "NOW()",
			},
			conditions: []Condition{
				{Column: "id", Operator: "=", Value: 1, Type: "and"},
			},
			wantParts:  []string{`UPDATE "users" SET`, `"comment" = $1`, `WHERE "id" = $2`},
			wantArgLen: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileUpdate(tt.table, tt.values, tt.conditions)
			for _, part := range tt.wantParts {
				if !strings.Contains(gotSQL, part) {
					t.Errorf("CompileUpdate() SQL missing part %q, got %q", part, gotSQL)
				}
			}
			if len(gotArgs) != tt.wantArgLen {
				t.Errorf("CompileUpdate() args count = %d, want %d", len(gotArgs), tt.wantArgLen)
			}
		})
	}
}

func TestPostgresGrammar_CompileDelete(t *testing.T) {
	grammar := &PostgresGrammar{}

	tests := []struct {
		name       string
		table      string
		conditions []Condition
		wantSQL    string
		wantArgs   []any
	}{
		{
			name:       "delete without conditions",
			table:      "users",
			conditions: nil,
			wantSQL:    `DELETE FROM "users"`,
			wantArgs:   nil,
		},
		{
			name:  "delete with conditions",
			table: "users",
			conditions: []Condition{
				{Column: "id", Operator: "=", Value: 1, Type: "and"},
			},
			wantSQL:  `DELETE FROM "users" WHERE "id" = $1`,
			wantArgs: []any{1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileDelete(tt.table, tt.conditions)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileDelete() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("CompileDelete() args = %v, want %v", gotArgs, tt.wantArgs)
			}
		})
	}
}

// TestPostgresGrammar_CompileUpdateReturning locks in the SQL surface
// the bulk-hook plan relies on for atomic id capture. The augmenter
// MUST preserve the underlying CompileUpdate output verbatim and append
// RETURNING <quoted-pk> at the tail; any drift breaks the no-race
// guarantee documented on BulkAfterCommitHook.
func TestPostgresGrammar_CompileUpdateReturning(t *testing.T) {
	grammar := &PostgresGrammar{}

	tests := []struct {
		name       string
		table      string
		values     map[string]any
		conditions []Condition
		pkCol      string
		wantParts  []string
		wantSuffix string
		wantArgLen int
	}{
		{
			name:  "update with conditions returns id",
			table: "users",
			values: map[string]any{
				"name": "alice",
			},
			conditions: []Condition{
				{Column: "active", Operator: "=", Value: true, Type: "and"},
			},
			pkCol:      "id",
			wantParts:  []string{`UPDATE "users" SET`, `"name" = $1`, `WHERE "active" = $2`},
			wantSuffix: ` RETURNING "id"`,
			wantArgLen: 2,
		},
		{
			name:  "update without conditions returns id",
			table: "users",
			values: map[string]any{
				"name": "bob",
			},
			conditions: nil,
			pkCol:      "id",
			wantParts:  []string{`UPDATE "users" SET`, `"name" = $1`},
			wantSuffix: ` RETURNING "id"`,
			wantArgLen: 1,
		},
		{
			name:  "RawSQL value passes through with RETURNING appended",
			table: "users",
			values: map[string]any{
				"updated_at": RawSQL("NOW()"),
			},
			conditions: []Condition{
				{Column: "id", Operator: "=", Value: 1, Type: "and"},
			},
			pkCol:      "id",
			wantParts:  []string{`UPDATE "users" SET`, `"updated_at" = NOW()`, `WHERE "id" = $1`},
			wantSuffix: ` RETURNING "id"`,
			wantArgLen: 1,
		},
		{
			name:       "non-default pk column is quoted",
			table:      "events",
			values:     map[string]any{"status": "done"},
			conditions: nil,
			pkCol:      "event_uuid",
			wantParts:  []string{`UPDATE "events" SET`, `"status" = $1`},
			wantSuffix: ` RETURNING "event_uuid"`,
			wantArgLen: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileUpdateReturning(tt.table, tt.values, tt.conditions, tt.pkCol)
			for _, part := range tt.wantParts {
				if !strings.Contains(gotSQL, part) {
					t.Errorf("CompileUpdateReturning() SQL missing part %q, got %q", part, gotSQL)
				}
			}
			if !strings.HasSuffix(gotSQL, tt.wantSuffix) {
				t.Errorf("CompileUpdateReturning() SQL = %q, want suffix %q", gotSQL, tt.wantSuffix)
			}
			if len(gotArgs) != tt.wantArgLen {
				t.Errorf("CompileUpdateReturning() args count = %d, want %d", len(gotArgs), tt.wantArgLen)
			}
		})
	}
}

// TestPostgresGrammar_CompileDeleteReturning is the DELETE counterpart
// to TestPostgresGrammar_CompileUpdateReturning. Same contract: the
// underlying CompileDelete output is preserved verbatim and RETURNING
// <quoted-pk> is appended at the tail.
func TestPostgresGrammar_CompileDeleteReturning(t *testing.T) {
	grammar := &PostgresGrammar{}

	tests := []struct {
		name       string
		table      string
		conditions []Condition
		pkCol      string
		wantSQL    string
		wantArgs   []any
	}{
		{
			name:       "delete without conditions returns id",
			table:      "users",
			conditions: nil,
			pkCol:      "id",
			wantSQL:    `DELETE FROM "users" RETURNING "id"`,
			wantArgs:   nil,
		},
		{
			name:  "delete with conditions returns id",
			table: "users",
			conditions: []Condition{
				{Column: "active", Operator: "=", Value: false, Type: "and"},
			},
			pkCol:    "id",
			wantSQL:  `DELETE FROM "users" WHERE "active" = $1 RETURNING "id"`,
			wantArgs: []any{false},
		},
		{
			name:       "non-default pk column is quoted",
			table:      "events",
			conditions: nil,
			pkCol:      "event_uuid",
			wantSQL:    `DELETE FROM "events" RETURNING "event_uuid"`,
			wantArgs:   nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotSQL, gotArgs := grammar.CompileDeleteReturning(tt.table, tt.conditions, tt.pkCol)
			if gotSQL != tt.wantSQL {
				t.Errorf("CompileDeleteReturning() SQL = %q, want %q", gotSQL, tt.wantSQL)
			}
			if !reflect.DeepEqual(gotArgs, tt.wantArgs) {
				t.Errorf("CompileDeleteReturning() args = %v, want %v", gotArgs, tt.wantArgs)
			}
		})
	}
}

// TestPostgresGrammar_ImplementsReturningGrammar is a compile-time
// guard wrapped in a runtime check: PostgresGrammar must satisfy
// drivers.ReturningGrammar so the ORM's bulk-hook plan can opt in to
// atomic id capture on Postgres.
func TestPostgresGrammar_ImplementsReturningGrammar(t *testing.T) {
	var _ ReturningGrammar = (*PostgresGrammar)(nil)
}

// TestSQLiteGrammar_DoesNotImplementReturningGrammar locks in the
// fallback contract: SQLite's bulk-hook path must use the pre-SELECT
// branch because the grammar does not opt in to RETURNING. Same for
// MySQL. If a future SQLite/MariaDB grammar adds RETURNING support,
// drop the negative assertion and update bulkPrepareHooks accordingly.
func TestSQLiteAndMySQLGrammar_DoNotImplementReturningGrammar(t *testing.T) {
	if _, ok := any(&SQLiteGrammar{}).(ReturningGrammar); ok {
		t.Errorf("SQLiteGrammar must not satisfy ReturningGrammar in v1; bulk-hook fallback path depends on the negative case")
	}
	if _, ok := any(&MySQLGrammar{}).(ReturningGrammar); ok {
		t.Errorf("MySQLGrammar must not satisfy ReturningGrammar in v1; bulk-hook fallback path depends on the negative case")
	}
}

func TestPostgresGrammar_CompileCreateTable(t *testing.T) {
	grammar := &PostgresGrammar{}

	tests := []struct {
		name      string
		tblName   string
		table     *Table
		wantParts []string
	}{
		{
			name:    "table with SERIAL for auto increment",
			tblName: "users",
			table: &Table{
				Columns: []Column{
					{Name: "id", Type: "INT", Primary: true, AutoIncrement: true},
					{Name: "name", Type: "VARCHAR", Size: 255, Nullable: false},
				},
			},
			wantParts: []string{
				`CREATE TABLE "users"`,
				`"id" SERIAL PRIMARY KEY`,
				`"name" VARCHAR(255) NOT NULL`,
			},
		},
		{
			name:    "table with unique and boolean default",
			tblName: "users",
			table: &Table{
				Columns: []Column{
					{Name: "id", Type: "INT", Primary: true},
					{Name: "email", Type: "VARCHAR", Size: 255, Unique: true},
					{Name: "active", Type: "BOOLEAN", Default: true},
				},
			},
			wantParts: []string{
				`CREATE TABLE "users"`,
				`"id" INTEGER PRIMARY KEY NOT NULL`,
				`"email" VARCHAR(255) NOT NULL UNIQUE`,
				`"active" BOOLEAN NOT NULL DEFAULT TRUE`,
			},
		},
		{
			name:    "table with BIGSERIAL",
			tblName: "posts",
			table: &Table{
				Columns: []Column{
					{Name: "id", Type: "BIGINT", Primary: true, AutoIncrement: true},
					{Name: "title", Type: "VARCHAR", Size: 255},
				},
			},
			wantParts: []string{
				`CREATE TABLE "posts"`,
				`"id" BIGSERIAL PRIMARY KEY`,
				`"title" VARCHAR(255)`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.CompileCreateTable(tt.tblName, tt.table)
			for _, part := range tt.wantParts {
				if !strings.Contains(got, part) {
					t.Errorf("CompileCreateTable() missing part %q, got %q", part, got)
				}
			}
		})
	}
}

func TestPostgresGrammar_CompileDropTable(t *testing.T) {
	grammar := &PostgresGrammar{}

	tests := []struct {
		name  string
		table string
		want  string
	}{
		{
			name:  "drop table with CASCADE",
			table: "users",
			want:  `DROP TABLE IF EXISTS "users" CASCADE`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.CompileDropTable(tt.table)
			if got != tt.want {
				t.Errorf("CompileDropTable() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPostgresGrammar_QuoteIdentifier(t *testing.T) {
	grammar := &PostgresGrammar{}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple identifier", "users", `"users"`},
		{"identifier with underscore", "user_roles", `"user_roles"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.QuoteIdentifier(tt.input)
			if got != tt.want {
				t.Errorf("QuoteIdentifier() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPostgresGrammar_QuoteString(t *testing.T) {
	grammar := &PostgresGrammar{}

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"simple string", "hello", "'hello'"},
		{"string with quote", "it's a test", "'it''s a test'"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.QuoteString(tt.input)
			if got != tt.want {
				t.Errorf("QuoteString() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPostgresGrammar_Placeholder(t *testing.T) {
	grammar := &PostgresGrammar{}

	tests := []struct {
		name  string
		index int
		want  string
	}{
		{"first placeholder", 1, "$1"},
		{"fifth placeholder", 5, "$5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.Placeholder(tt.index)
			if got != tt.want {
				t.Errorf("Placeholder() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestPostgresGrammar_getPostgresType(t *testing.T) {
	grammar := &PostgresGrammar{}

	tests := []struct {
		name   string
		column Column
		want   string
	}{
		{"INT", Column{Type: "INT"}, "INTEGER"},
		{"INTEGER", Column{Type: "INTEGER"}, "INTEGER"},
		{"BIGINT", Column{Type: "BIGINT"}, "BIGINT"},
		{"SMALLINT", Column{Type: "SMALLINT"}, "SMALLINT"},
		{"INT with AutoIncrement", Column{Type: "INT", AutoIncrement: true}, "SERIAL"},
		{"BIGINT with AutoIncrement", Column{Type: "BIGINT", AutoIncrement: true}, "BIGSERIAL"},
		{"SMALLINT with AutoIncrement", Column{Type: "SMALLINT", AutoIncrement: true}, "SMALLSERIAL"},
		{"DECIMAL with size", Column{Type: "DECIMAL", Size: 10}, "DECIMAL(10)"},
		{"DECIMAL without size", Column{Type: "DECIMAL"}, "DECIMAL"},
		{"FLOAT", Column{Type: "FLOAT"}, "REAL"},
		{"DOUBLE", Column{Type: "DOUBLE"}, "DOUBLE PRECISION"},
		{"VARCHAR with size", Column{Type: "VARCHAR", Size: 100}, "VARCHAR(100)"},
		{"VARCHAR without size", Column{Type: "VARCHAR"}, "VARCHAR(255)"},
		{"CHAR with size", Column{Type: "CHAR", Size: 10}, "CHAR(10)"},
		{"CHAR without size", Column{Type: "CHAR"}, "CHAR(1)"},
		{"TEXT", Column{Type: "TEXT"}, "TEXT"},
		{"BLOB to BYTEA", Column{Type: "BLOB"}, "BYTEA"},
		{"BOOLEAN", Column{Type: "BOOLEAN"}, "BOOLEAN"},
		{"BOOL", Column{Type: "BOOL"}, "BOOLEAN"},
		{"DATE", Column{Type: "DATE"}, "DATE"},
		{"TIME", Column{Type: "TIME"}, "TIME"},
		{"DATETIME to TIMESTAMP", Column{Type: "DATETIME"}, "TIMESTAMP"},
		{"TIMESTAMP", Column{Type: "TIMESTAMP"}, "TIMESTAMP"},
		{"JSON", Column{Type: "JSON"}, "JSON"},
		{"JSONB", Column{Type: "JSONB"}, "JSONB"},
		{"UUID", Column{Type: "UUID"}, "UUID"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := grammar.getPostgresType(tt.column)
			if got != tt.want {
				t.Errorf("getPostgresType() = %q, want %q", got, tt.want)
			}
		})
	}
}

// =============================================================================
// Cross-Driver Comparison Tests
// =============================================================================

func TestGrammar_PlaceholderDifferences(t *testing.T) {
	tests := []struct {
		name     string
		grammar  QueryGrammar
		index    int
		expected string
	}{
		{"SQLite uses ?", &SQLiteGrammar{}, 1, "?"},
		{"SQLite ignores index", &SQLiteGrammar{}, 5, "?"},
		{"MySQL uses ?", &MySQLGrammar{}, 1, "?"},
		{"MySQL ignores index", &MySQLGrammar{}, 5, "?"},
		{"Postgres uses $N", &PostgresGrammar{}, 1, "$1"},
		{"Postgres increments index", &PostgresGrammar{}, 5, "$5"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.grammar.Placeholder(tt.index)
			if got != tt.expected {
				t.Errorf("Placeholder(%d) = %q, want %q", tt.index, got, tt.expected)
			}
		})
	}
}

func TestGrammar_QuoteIdentifierDifferences(t *testing.T) {
	tests := []struct {
		name     string
		grammar  QueryGrammar
		input    string
		expected string
	}{
		{"SQLite uses backticks", &SQLiteGrammar{}, "users", "`users`"},
		{"MySQL uses backticks", &MySQLGrammar{}, "users", "`users`"},
		{"Postgres uses double quotes", &PostgresGrammar{}, "users", `"users"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.grammar.QuoteIdentifier(tt.input)
			if got != tt.expected {
				t.Errorf("QuoteIdentifier(%q) = %q, want %q", tt.input, got, tt.expected)
			}
		})
	}
}

func TestGrammar_InsertReturningDifference(t *testing.T) {
	table := "users"
	columns := []string{"name"}
	values := [][]any{{"John"}}

	t.Run("Postgres appends RETURNING id", func(t *testing.T) {
		grammar := &PostgresGrammar{}
		sql, _ := grammar.CompileInsert(table, columns, values)
		if !strings.HasSuffix(sql, "RETURNING id") {
			t.Errorf("Postgres CompileInsert() should end with RETURNING id, got %q", sql)
		}
	})

	t.Run("MySQL does not append RETURNING", func(t *testing.T) {
		grammar := &MySQLGrammar{}
		sql, _ := grammar.CompileInsert(table, columns, values)
		if strings.Contains(sql, "RETURNING") {
			t.Errorf("MySQL CompileInsert() should not contain RETURNING, got %q", sql)
		}
	})

	t.Run("SQLite does not append RETURNING", func(t *testing.T) {
		grammar := &SQLiteGrammar{}
		sql, _ := grammar.CompileInsert(table, columns, values)
		if strings.Contains(sql, "RETURNING") {
			t.Errorf("SQLite CompileInsert() should not contain RETURNING, got %q", sql)
		}
	})
}
