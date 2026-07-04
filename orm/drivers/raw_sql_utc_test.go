package drivers

import (
	"strings"
	"testing"
)

// The current-timestamp sentinels carry a fixed contract: DB clock, UTC
// wall clock. Each grammar must pin the emitted SQL accordingly, while any
// other RawSQL value passes through verbatim.
func TestCompileUpdate_UTCPinnedSentinels(t *testing.T) {
	conditions := []Condition{{Column: "id", Operator: "=", Value: 1}}

	tests := []struct {
		name    string
		grammar QueryGrammar
		raw     RawSQL
		want    string
	}{
		{"postgres NOW", &PostgresGrammar{}, NOW, `"ts" = (NOW() AT TIME ZONE 'UTC')`},
		{"postgres CurrentTimestamp", &PostgresGrammar{}, CurrentTimestamp, `"ts" = (NOW() AT TIME ZONE 'UTC')`},
		{"postgres other RawSQL verbatim", &PostgresGrammar{}, RawSQL("ts + interval '1 day'"), `"ts" = ts + interval '1 day'`},
		{"mysql NOW", &MySQLGrammar{}, NOW, "`ts` = UTC_TIMESTAMP()"},
		{"mysql CurrentTimestamp", &MySQLGrammar{}, CurrentTimestamp, "`ts` = UTC_TIMESTAMP()"},
		{"mysql other RawSQL verbatim", &MySQLGrammar{}, RawSQL("ts + INTERVAL 1 DAY"), "`ts` = ts + INTERVAL 1 DAY"},
		{"sqlite NOW mapped", &SQLiteGrammar{}, NOW, "`ts` = CURRENT_TIMESTAMP"},
		{"sqlite CurrentTimestamp", &SQLiteGrammar{}, CurrentTimestamp, "`ts` = CURRENT_TIMESTAMP"},
		{"sqlite other RawSQL verbatim", &SQLiteGrammar{}, RawSQL("ts + 1"), "`ts` = ts + 1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := tt.grammar.CompileUpdate("things", map[string]any{"ts": tt.raw}, conditions)
			if !strings.Contains(sql, tt.want) {
				t.Errorf("CompileUpdate SQL = %q, want it to contain %q", sql, tt.want)
			}
			// Sentinel must be emitted, never bound: only the WHERE arg binds.
			if len(args) != 1 {
				t.Errorf("args = %v, want exactly the WHERE value", args)
			}
		})
	}
}

// Insert maps carry the same sentinel contract as Update maps: RawSQL
// values emit as (UTC-pinned) expressions and are never bound, and
// placeholder numbering skips them so Postgres $N stays consecutive.
func TestCompileInsert_UTCPinnedSentinels(t *testing.T) {
	columns := []string{"name", "ts"}
	values := [][]any{{"a", NOW}}

	tests := []struct {
		name     string
		grammar  QueryGrammar
		wantExpr string
	}{
		{"postgres", &PostgresGrammar{}, "($1, (NOW() AT TIME ZONE 'UTC'))"},
		{"mysql", &MySQLGrammar{}, "(?, UTC_TIMESTAMP())"},
		{"sqlite", &SQLiteGrammar{}, "(?, CURRENT_TIMESTAMP)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql, args := tt.grammar.CompileInsert("things", columns, values)
			if !strings.Contains(sql, tt.wantExpr) {
				t.Errorf("CompileInsert SQL = %q, want it to contain %q", sql, tt.wantExpr)
			}
			if len(args) != 1 || args[0] != "a" {
				t.Errorf("args = %v, want only the bound name value", args)
			}
		})
	}
}

// RawSQLExprFor is the out-of-grammar dispatch used by the ORM's map-based
// INSERT builder; it must agree with the grammars.
func TestRawSQLExprFor(t *testing.T) {
	tests := []struct {
		driver string
		raw    RawSQL
		want   string
	}{
		{"postgres", NOW, "(NOW() AT TIME ZONE 'UTC')"},
		{"mysql", CurrentTimestamp, "UTC_TIMESTAMP()"},
		{"sqlite", NOW, "CURRENT_TIMESTAMP"},
		{"sqlite3", CurrentTimestamp, "CURRENT_TIMESTAMP"},
		{"postgres", RawSQL("x + 1"), "x + 1"},
		{"unknown", NOW, "NOW()"},
	}
	for _, tt := range tests {
		if got := RawSQLExprFor(tt.driver, tt.raw); got != tt.want {
			t.Errorf("RawSQLExprFor(%q, %q) = %q, want %q", tt.driver, tt.raw, got, tt.want)
		}
	}
}
