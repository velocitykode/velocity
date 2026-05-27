package validation

import (
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/lib/pq"
	"github.com/mattn/go-sqlite3"
)

// TestAsValidationError_PostgresUniqueViolation pins the Postgres SQLSTATE
// 23505 path. pq.Error.Code is checked first, then Column is preferred when
// PostgreSQL provided it. The returned *ValidationErrors must carry the
// "has already been taken" message keyed by the matching field and the
// "unique" rule entry in RulesByField so downstream test helpers
// (validation/testing.AssertErrorRule) keep working.
func TestAsValidationError_PostgresUniqueViolation(t *testing.T) {
	err := &pq.Error{
		Code:    "23505",
		Column:  "email",
		Message: `duplicate key value violates unique constraint "users_email_key"`,
	}

	verr, ok := AsValidationError(err, map[string]string{"email": "unique"})
	if !ok {
		t.Fatalf("expected unique violation to be detected; got ok=false")
	}
	if verr == nil {
		t.Fatalf("expected non-nil ValidationErrors")
	}
	if !verr.HasError("email") {
		t.Fatalf("expected email error; got %v", verr.All())
	}
	if got := verr.First("email"); got != "The email has already been taken." {
		t.Errorf("unexpected message: %q", got)
	}
	if !verr.HasRule("email", "unique") {
		t.Errorf("expected unique rule recorded; got rules %v", verr.RulesFor("email"))
	}
}

// TestAsValidationError_PostgresUniqueViolation_ConstraintFallback covers
// the common case where pq.Error.Column is empty but the constraint name
// embeds the column. The fallback uses Constraint and selectFields's
// suffix / underscore matching to attribute "users_email_key" back to
// the "email" field.
func TestAsValidationError_PostgresUniqueViolation_ConstraintFallback(t *testing.T) {
	err := &pq.Error{
		Code:       "23505",
		Constraint: "users_email_key",
		Message:    `duplicate key value violates unique constraint "users_email_key"`,
	}

	verr, ok := AsValidationError(err, map[string]string{"email": "unique"})
	if !ok || verr == nil {
		t.Fatalf("expected unique violation detection; ok=%v verr=%v", ok, verr)
	}
	if !verr.HasError("email") {
		t.Fatalf("expected email error from constraint suffix match; got %v", verr.All())
	}
}

// TestAsValidationError_MySQLUniqueViolation pins the MySQL ER_DUP_ENTRY
// path (errno 1062). The key name in the message ("users_email_unique") is
// matched to the "email" field via suffix containment.
func TestAsValidationError_MySQLUniqueViolation(t *testing.T) {
	err := &mysql.MySQLError{
		Number:  1062,
		Message: "Duplicate entry 'a@b.com' for key 'users_email_unique'",
	}

	verr, ok := AsValidationError(err, map[string]string{"email": "unique"})
	if !ok || verr == nil {
		t.Fatalf("expected unique violation detection; ok=%v verr=%v", ok, verr)
	}
	if got := verr.First("email"); got != "The email has already been taken." {
		t.Errorf("unexpected message: %q", got)
	}
}

// TestAsValidationError_MySQL_ER_DUP_ENTRY_WITH_KEY_NAME pins errno 1586,
// the variant returned for partitioned tables.
func TestAsValidationError_MySQL_ER_DUP_ENTRY_WITH_KEY_NAME(t *testing.T) {
	err := &mysql.MySQLError{
		Number:  1586,
		Message: "Duplicate entry 'a@b.com' for key 'users.uniq_email'",
	}

	verr, ok := AsValidationError(err, map[string]string{"email": "unique"})
	if !ok || verr == nil {
		t.Fatalf("expected detection; ok=%v", ok)
	}
	if !verr.HasError("email") {
		t.Errorf("expected email error; got %v", verr.All())
	}
}

// TestAsValidationError_SQLiteUniqueViolation pins the SQLite
// SQLITE_CONSTRAINT_UNIQUE (extended code 2067) path. The message embeds
// the column as "users.email"; extractSQLiteColumn pulls "email" out.
func TestAsValidationError_SQLiteUniqueViolation(t *testing.T) {
	err := sqlite3.Error{
		Code:         sqlite3.ErrConstraint,
		ExtendedCode: sqlite3.ErrConstraintUnique,
	}

	verr, ok := AsValidationError(err, map[string]string{"email": "unique"})
	if !ok || verr == nil {
		t.Fatalf("expected detection; ok=%v", ok)
	}
	// No column hint in this artificial error (sqlite3.Error.Error()
	// without a sqlite-supplied err string returns the generic
	// constraint message), so the single-field branch attributes to
	// "email".
	if !verr.HasError("email") {
		t.Errorf("expected email error from single-field fallback; got %v", verr.All())
	}
}

// TestAsValidationError_SQLiteUniqueViolation_BaseCodeWithMessage pins the
// older SQLite path where extended codes are not surfaced and the only
// signal is the message text "UNIQUE constraint failed: users.email".
// The string-fallback branch must still pick this up.
func TestAsValidationError_SQLiteUniqueViolation_BaseCodeWithMessage(t *testing.T) {
	err := errors.New("UNIQUE constraint failed: users.email")
	verr, ok := AsValidationError(err, map[string]string{"email": "unique"})
	if !ok || verr == nil {
		t.Fatalf("expected detection via string fallback; ok=%v", ok)
	}
	if !verr.HasError("email") {
		t.Errorf("expected email error; got %v", verr.All())
	}
}

// TestAsValidationError_NonUniqueErrors_BoolFalse covers structural
// failures that must NOT be converted into validation errors: FK
// violation, NOT NULL, CHECK, generic SQL syntax error, and a plain
// stdlib error. The helper must return (nil, false) so the caller
// can surface a 500 instead.
func TestAsValidationError_NonUniqueErrors_BoolFalse(t *testing.T) {
	cases := []struct {
		name string
		err  error
	}{
		{
			name: "pq foreign key violation 23503",
			err: &pq.Error{
				Code:    "23503",
				Message: "insert or update on table \"posts\" violates foreign key constraint",
			},
		},
		{
			name: "pq not null violation 23502",
			err:  &pq.Error{Code: "23502"},
		},
		{
			name: "pq check violation 23514",
			err:  &pq.Error{Code: "23514"},
		},
		{
			name: "mysql foreign key 1452",
			err:  &mysql.MySQLError{Number: 1452, Message: "cannot add child row"},
		},
		{
			name: "mysql syntax 1064",
			err:  &mysql.MySQLError{Number: 1064, Message: "syntax error"},
		},
		{
			name: "sqlite FK constraint",
			err: sqlite3.Error{
				Code:         sqlite3.ErrConstraint,
				ExtendedCode: sqlite3.ErrConstraintForeignKey,
			},
		},
		{
			name: "plain error",
			err:  errors.New("connection refused"),
		},
		{
			name: "wrapped plain error",
			err:  fmt.Errorf("query failed: %w", errors.New("EOF")),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verr, ok := AsValidationError(tc.err, map[string]string{"email": "unique"})
			if ok {
				t.Fatalf("expected ok=false for %T %v; got verr=%v", tc.err, tc.err, verr)
			}
			if verr != nil {
				t.Fatalf("expected nil ValidationErrors; got %v", verr)
			}
		})
	}
}

// TestAsValidationError_EmptyFieldRules_BoolFalse pins the contract that
// "no candidates declared" yields ok=false even on a real unique
// violation. Callers must be explicit about which fields a constraint
// covers; the helper must not invent attribution.
func TestAsValidationError_EmptyFieldRules_BoolFalse(t *testing.T) {
	err := &pq.Error{Code: "23505", Column: "email"}

	if verr, ok := AsValidationError(err, nil); ok || verr != nil {
		t.Fatalf("nil map: expected ok=false verr=nil; got ok=%v verr=%v", ok, verr)
	}
	if verr, ok := AsValidationError(err, map[string]string{}); ok || verr != nil {
		t.Fatalf("empty map: expected ok=false verr=nil; got ok=%v verr=%v", ok, verr)
	}
}

// TestAsValidationError_NilError_BoolFalse pins the nil-input guard so
// callers can pass a freshly-checked err without an extra nil branch.
func TestAsValidationError_NilError_BoolFalse(t *testing.T) {
	if verr, ok := AsValidationError(nil, map[string]string{"email": "unique"}); ok || verr != nil {
		t.Fatalf("expected ok=false verr=nil for nil err; got ok=%v verr=%v", ok, verr)
	}
}

// TestAsValidationError_WrappedDriverError pins that errors.As traversal
// works through fmt.Errorf("%w"). Real ORM code wraps driver errors with
// context (operation name, table name) before returning; the helper must
// still detect the wrapped pq.Error.
func TestAsValidationError_WrappedDriverError(t *testing.T) {
	inner := &pq.Error{Code: "23505", Column: "email"}
	wrapped := fmt.Errorf("orm.Insert(users): %w", inner)

	verr, ok := AsValidationError(wrapped, map[string]string{"email": "unique"})
	if !ok || verr == nil {
		t.Fatalf("expected detection through wrap; ok=%v", ok)
	}
	if !verr.HasError("email") {
		t.Errorf("expected email error; got %v", verr.All())
	}
}

// TestAsValidationError_MultiFieldFallback covers the case where the
// driver gave no usable column hint and the caller declared multiple
// candidates. The helper attributes the error to ALL declared fields
// so the response is informative, even if slightly noisy. The
// alternative (silently dropping the error) is worse: the user sees
// a 500 with no field info.
func TestAsValidationError_MultiFieldFallback(t *testing.T) {
	// pq.Error with empty Column and Constraint produces an empty hint.
	err := &pq.Error{Code: "23505"}

	verr, ok := AsValidationError(err, map[string]string{
		"email":    "unique",
		"username": "unique",
	})
	if !ok || verr == nil {
		t.Fatalf("expected detection; ok=%v", ok)
	}
	if !verr.HasError("email") || !verr.HasError("username") {
		t.Errorf("expected both fields to carry errors; got %v", verr.All())
	}
}

// TestAsValidationError_UnknownRuleFallsBackToGenericMessage exercises the
// non-unique-rule branch of messageForRule. The helper accepts arbitrary
// rule names so the surface stays useful when callers want to surface,
// say, a CHECK constraint as a structured validation error.
func TestAsValidationError_UnknownRuleFallsBackToGenericMessage(t *testing.T) {
	err := &pq.Error{Code: "23505", Column: "amount"}
	verr, ok := AsValidationError(err, map[string]string{"amount": "positive"})
	if !ok || verr == nil {
		t.Fatalf("expected detection; ok=%v", ok)
	}
	if got := verr.First("amount"); got != "The amount is invalid." {
		t.Errorf("unexpected generic message: %q", got)
	}
}

// TestExtractMySQLKeyName covers the key-name parser in isolation. The
// MySQL message format is stable across versions but the surrounding
// text varies, so we pin a handful of shapes.
func TestExtractMySQLKeyName(t *testing.T) {
	cases := []struct {
		msg  string
		want string
	}{
		{"Duplicate entry 'a@b.com' for key 'users_email_unique'", "users_email_unique"},
		{"Duplicate entry 'x' for key 'PRIMARY'", "PRIMARY"},
		{"Duplicate entry '1' for key 'users.uniq_email'", "users.uniq_email"},
		{"some other message", ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			if got := extractMySQLKeyName(tc.msg); got != tc.want {
				t.Errorf("extractMySQLKeyName(%q) = %q; want %q", tc.msg, got, tc.want)
			}
		})
	}
}

// TestExtractSQLiteColumn covers the column parser in isolation.
func TestExtractSQLiteColumn(t *testing.T) {
	cases := []struct {
		msg  string
		want string
	}{
		{"UNIQUE constraint failed: users.email", "email"},
		{"UNIQUE constraint failed: users.email, users.tenant_id", "email"},
		{"UNIQUE constraint failed: email", "email"},
		{"NOT NULL constraint failed: users.email", ""},
		{"", ""},
	}
	for _, tc := range cases {
		t.Run(tc.msg, func(t *testing.T) {
			if got := extractSQLiteColumn(tc.msg); got != tc.want {
				t.Errorf("extractSQLiteColumn(%q) = %q; want %q", tc.msg, got, tc.want)
			}
		})
	}
}

// TestSelectFields_MatchPriority covers the field-selection precedence
// in selectFields: exact match wins over suffix wins over single-entry
// fallback wins over multi-entry fall-through.
func TestSelectFields_MatchPriority(t *testing.T) {
	t.Run("exact match wins", func(t *testing.T) {
		got := selectFields(map[string]string{"email": "unique", "name": "unique"}, "email")
		if len(got) != 1 || got[0] != "email" {
			t.Errorf("got %v; want [email]", got)
		}
	})
	t.Run("suffix match for index name", func(t *testing.T) {
		got := selectFields(map[string]string{"email": "unique"}, "users_email_unique")
		if len(got) != 1 || got[0] != "email" {
			t.Errorf("got %v; want [email]", got)
		}
	})
	t.Run("dotted table.column suffix", func(t *testing.T) {
		got := selectFields(map[string]string{"email": "unique"}, "users.email")
		if len(got) != 1 || got[0] != "email" {
			t.Errorf("got %v; want [email]", got)
		}
	})
	t.Run("case-insensitive suffix", func(t *testing.T) {
		got := selectFields(map[string]string{"email": "unique"}, "users_Email_UNIQUE")
		if len(got) != 1 || got[0] != "email" {
			t.Errorf("got %v; want [email]", got)
		}
	})
	t.Run("no hint single entry attributes to it", func(t *testing.T) {
		got := selectFields(map[string]string{"email": "unique"}, "")
		if len(got) != 1 || got[0] != "email" {
			t.Errorf("got %v; want [email]", got)
		}
	})
	t.Run("no hint multi entry attributes to all", func(t *testing.T) {
		got := selectFields(map[string]string{"email": "unique", "name": "unique"}, "")
		if len(got) != 2 {
			t.Errorf("got %v; want both entries", got)
		}
	})
}
