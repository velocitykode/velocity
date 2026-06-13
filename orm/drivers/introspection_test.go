package drivers

import (
	"strings"
	"testing"
)

func TestValidateSchemaIdentifier(t *testing.T) {
	tests := []struct {
		name    string
		wantErr bool
	}{
		{name: "users"},
		{name: "user_roles"},
		{name: "_migrations"},
		{name: "public.users", wantErr: true},
		{name: "users; DROP TABLE users", wantErr: true},
		{name: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateSchemaIdentifier(tt.name)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateSchemaIdentifier(%q) error = %v, wantErr %v", tt.name, err, tt.wantErr)
			}
		})
	}
}

func TestIntrospectionGrammar_CompileListTables(t *testing.T) {
	tests := []struct {
		name    string
		grammar IntrospectionGrammar
		want    string
	}{
		{
			name:    "postgres",
			grammar: &PostgresGrammar{},
			want:    "information_schema.tables AS t",
		},
		{
			name:    "mysql",
			grammar: &MySQLGrammar{},
			want:    "information_schema.tables AS t",
		},
		{
			name:    "sqlite",
			grammar: &SQLiteGrammar{},
			want:    "sqlite_master",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.grammar.CompileListTables()
			if !strings.Contains(got, tt.want) {
				t.Fatalf("CompileListTables() = %q, want it to contain %q", got, tt.want)
			}
			if !strings.Contains(strings.ToUpper(got), "ORDER BY") {
				t.Fatalf("CompileListTables() = %q, want deterministic ordering", got)
			}
		})
	}
}

func TestIntrospectionGrammar_CompileDescribeTable(t *testing.T) {
	t.Run("postgres qualifies column names across joins", func(t *testing.T) {
		got := (&PostgresGrammar{}).CompileDescribeTable("users")
		for _, want := range []string{"c.column_name", "pk.column_name", "c.ordinal_position"} {
			if !strings.Contains(got, want) {
				t.Fatalf("CompileDescribeTable() = %q, want it to contain %q", got, want)
			}
		}
		if strings.Contains(got, "SELECT\n\t\t\tcolumn_name") {
			t.Fatalf("CompileDescribeTable() selected an unqualified column_name: %q", got)
		}
	})

	t.Run("mysql returns key metadata", func(t *testing.T) {
		got := (&MySQLGrammar{}).CompileDescribeTable("users")
		for _, want := range []string{"c.column_name", "c.column_type", "c.column_key"} {
			if !strings.Contains(got, want) {
				t.Fatalf("CompileDescribeTable() = %q, want it to contain %q", got, want)
			}
		}
	})

	t.Run("sqlite quotes table name", func(t *testing.T) {
		got := (&SQLiteGrammar{}).CompileDescribeTable("users")
		want := "PRAGMA table_info(`users`)"
		if got != want {
			t.Fatalf("CompileDescribeTable() = %q, want %q", got, want)
		}
	})
}
