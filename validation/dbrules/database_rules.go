// Package dbrules holds the DB-backed validation rules (unique, exists) and
// the driver-error mapper (AsValidationError). These are split out of the
// core validation package so that core depends only on contract,
// validation/rules, and the standard library; the orm and SQL-driver
// dependencies live here instead.
//
// The rules return validation.RuleHandler values and are wired into the core
// validator via the CheckWithDB / CheckWithDBW / CheckDataWithDB helpers in
// this package, which delegate to validation.CheckWithRulesW /
// validation.CheckDataWithRules with the unique/exists handlers registered.
//
// # Best-effort uniqueness: ToCToU on the unique: rule
//
// The unique: rule (UniqueRule / UniqueRuleCtx) issues a SELECT COUNT(*)
// to confirm no row already holds the candidate value, then returns to
// the caller, which typically follows up with an INSERT. The window
// between SELECT and INSERT is a classic Time-of-Check to Time-of-Use
// race: two concurrent requests that both pass the SELECT proceed to
// race on the INSERT.
//
// Velocity does NOT lock or serialize the query, by design: uniqueness
// enforcement at scale is the database's job, not the validator's.
//
// To make uniqueness authoritative you MUST add a UNIQUE constraint at
// the database layer (CREATE UNIQUE INDEX, or UNIQUE in the column
// definition). The validator's job is to convert "the value is already
// taken" into a friendly field-level message before the insert; the
// constraint's job is to refuse the race-loser at write time. Without
// the constraint, the unique: rule is advisory and two rows with the
// same value can persist after a race.
//
// When the constraint fires, the INSERT fails with a driver-specific
// error (Postgres SQLSTATE 23505, MySQL errno 1062, SQLite extended
// code 2067 / base code 19). Use AsValidationError in this package to
// map those errors back onto the offending field so the user sees the
// same "has already been taken" message they would have seen if the
// validator had won the race.
package dbrules

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/validation"
)

// identifierRegex validates SQL table/column names. The regex allows dots so
// callers can pass schema-qualified names like "public.users" or compound
// references like "users.email"; validateIdentifier then relies on
// quoteIdentifier to split on each dot and quote the parts individually so
// that "schema.table" becomes `"schema"."table"` (or "`schema`.`table`" on
// MySQL/SQLite) , not `"schema.table"`.
var identifierRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*$`)

func validateIdentifier(name string) error {
	if name == "" || !identifierRegex.MatchString(name) {
		return fmt.Errorf("invalid SQL identifier: %q", name)
	}
	return nil
}

// quoteIdentifier quotes a SQL identifier based on the database driver.
//
// Dotted identifiers such as "schema.table" or "table.column" are split on
// `.` and each segment is quoted independently, producing:
//
//	postgres: "schema"."table"      or "table"."column"
//	mysql:    `schema`.`table`      or `table`.`column`
//	sqlite:   `schema`.`table`      or `table`.`column`
//
// Backticks/double-quotes embedded in an individual segment are escaped by
// doubling them (standard SQL identifier-quoting rules). validateIdentifier
// refuses any character outside [a-zA-Z0-9_.] so escaping here is
// conservative: it exists to close the door on future inputs that might
// relax the allowlist.
func quoteIdentifier(name, driver string) string {
	parts := strings.Split(name, ".")
	for i, p := range parts {
		switch driver {
		case "mysql", "sqlite":
			parts[i] = "`" + strings.ReplaceAll(p, "`", "``") + "`"
		default: // postgres and other ANSI-style quoters
			parts[i] = `"` + strings.ReplaceAll(p, `"`, `""`) + `"`
		}
	}
	return strings.Join(parts, ".")
}

// placeholder returns the correct parameter placeholder for the driver.
// MySQL/SQLite use ?, Postgres uses $1, $2, etc.
func placeholder(driver string, n int) string {
	if driver == "postgres" {
		return "$" + strconv.Itoa(n)
	}
	return "?"
}

// UniqueRule returns a RuleHandler that checks database uniqueness.
//
// Syntax: unique:table,column[,except_id[,id_column]]
//
//	"email": "required|email|unique:users,email"
//	"email": "required|email|unique:users,email,5"         // exclude id=5
//	"email": "required|email|unique:users,email,5,user_id" // custom id column
//
// The returned handler uses context.Background() for the underlying query.
// Callers that want request-scoped cancellation (so a slow query is dropped
// when the client disconnects) should use UniqueRuleCtx and pass the
// request's context.
//
// # Best-effort: a UNIQUE constraint at the DB layer is required for correctness
//
// This rule is ADVISORY, not authoritative. It runs a SELECT COUNT(*) and
// returns; the caller's subsequent INSERT races any other request that
// passed the same SELECT in the same window (ToCToU). Two concurrent
// signups for the same email will both pass and both attempt to insert.
//
// To make uniqueness real you MUST add a UNIQUE constraint at the
// database layer. When the constraint fires on the INSERT, use
// AsValidationError to convert the driver-specific error (Postgres
// SQLSTATE 23505, MySQL errno 1062, SQLite extended code 2067 / base
// code 19) back into a field-level "has already been taken" message
// the user can read. See the package doc for the full rationale.
func UniqueRule(db orm.Database) validation.RuleHandler {
	return UniqueRuleCtx(context.Background(), db)
}

// UniqueRuleCtx is the context-aware variant of UniqueRule. The query runs
// under ctx so request cancellation (client disconnect, timeout middleware)
// aborts the database round-trip instead of letting a slow validation pile
// up goroutines and a SQL connection.
//
// Raw DB errors are deliberately swallowed and replaced with a generic
// "Unable to validate <field>." message: schema names, table existence,
// and query text are server-side details that must not surface to a
// validation error string visible to the client. The underlying error is
// logged via slog.Default() at ERROR level so operators retain a trail.
//
// # Best-effort: see UniqueRule for the ToCToU caveat
//
// The same race window applies here. Add a UNIQUE constraint at the DB
// layer and route INSERT failures through AsValidationError to recover
// the field-level message after a race-loss.
func UniqueRuleCtx(ctx context.Context, db orm.Database) validation.RuleHandler {
	if ctx == nil {
		ctx = context.Background()
	}
	driver := db.DriverName()
	return func(field string, value interface{}, params []string, data map[string]interface{}) error {
		if len(params) < 1 {
			return fmt.Errorf("unique rule requires at least a table name")
		}

		table := params[0]
		column := field
		if len(params) >= 2 && params[1] != "" {
			column = params[1]
		}

		if err := validateIdentifier(table); err != nil {
			return err
		}
		if err := validateIdentifier(column); err != nil {
			return err
		}

		argN := 1
		query := "SELECT COUNT(*) FROM " + quoteIdentifier(table, driver) + " WHERE " + quoteIdentifier(column, driver) + " = " + placeholder(driver, argN)
		args := []interface{}{value}

		if len(params) >= 3 && params[2] != "" {
			idColumn := "id"
			if len(params) >= 4 && params[3] != "" {
				idColumn = params[3]
			}
			if err := validateIdentifier(idColumn); err != nil {
				return err
			}
			argN++
			query += " AND " + quoteIdentifier(idColumn, driver) + " != " + placeholder(driver, argN)
			args = append(args, params[2])
		}

		var count int64
		if err := db.DB().QueryRowContext(ctx, query, args...).Scan(&count); err != nil {
			slog.Default().Error("validation unique rule query failed",
				"field", field,
				"table", table,
				"column", column,
				"driver", driver,
				"err", err.Error(),
			)
			return fmt.Errorf("Unable to validate %s.", field)
		}

		if count > 0 {
			return fmt.Errorf("The %s has already been taken.", field)
		}
		return nil
	}
}

// ExistsRule returns a RuleHandler that checks a value exists in the database.
//
// Syntax: exists:table,column
//
//	"team_id": "required|exists:teams,id"
//
// The returned handler uses context.Background() for the underlying query.
// Use ExistsRuleCtx with the request's context for cancellation propagation.
func ExistsRule(db orm.Database) validation.RuleHandler {
	return ExistsRuleCtx(context.Background(), db)
}

// ExistsRuleCtx is the context-aware variant of ExistsRule. Same semantics
// as UniqueRuleCtx: the query runs under ctx and raw DB errors are
// suppressed in the client-visible message but logged via slog.Default().
func ExistsRuleCtx(ctx context.Context, db orm.Database) validation.RuleHandler {
	if ctx == nil {
		ctx = context.Background()
	}
	driver := db.DriverName()
	return func(field string, value interface{}, params []string, data map[string]interface{}) error {
		if len(params) < 1 {
			return fmt.Errorf("exists rule requires at least a table name")
		}

		table := params[0]
		column := field
		if len(params) >= 2 && params[1] != "" {
			column = params[1]
		}

		if err := validateIdentifier(table); err != nil {
			return err
		}
		if err := validateIdentifier(column); err != nil {
			return err
		}

		query := "SELECT COUNT(*) FROM " + quoteIdentifier(table, driver) + " WHERE " + quoteIdentifier(column, driver) + " = " + placeholder(driver, 1)

		var count int64
		if err := db.DB().QueryRowContext(ctx, query, value).Scan(&count); err != nil {
			slog.Default().Error("validation exists rule query failed",
				"field", field,
				"table", table,
				"column", column,
				"driver", driver,
				"err", err.Error(),
			)
			return fmt.Errorf("Unable to validate %s.", field)
		}

		if count == 0 {
			return fmt.Errorf("The selected %s is invalid.", field)
		}
		return nil
	}
}
