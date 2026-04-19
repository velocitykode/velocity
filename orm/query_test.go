package orm

import (
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
