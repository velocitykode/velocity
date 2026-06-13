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
// The driver-agnostic core of the rules and the error mapper live in
// validation/internal/dbcheck and are shared with the deprecated reflection
// shims in the core validation package, so both surfaces stay byte-identical.
// This package keeps ONLY the typed orm.Database seam (DriverName / DB().
// QueryRowContext) and the public signatures, which fill that shared core's
// seam and delegate to it.
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

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/validation"
	"github.com/velocitykode/velocity/validation/internal/dbcheck"
)

// The SQL assembly, identifier quoting, and placeholder logic live once in
// validation/internal/dbcheck. These thin forwarders expose them under the
// historical names this package's tests pin (database_rules_test.go,
// database_drivers_test.go), keeping a single implementation behind both.
func validateIdentifier(name string) error       { return dbcheck.ValidateIdentifier(name) }
func quoteIdentifier(name, driver string) string { return dbcheck.QuoteIdentifier(name, driver) }
func placeholder(driver string, n int) string    { return dbcheck.Placeholder(driver, n) }

// countQuery binds the typed orm.Database seam and a context into a
// dbcheck.CountFunc, so the shared rule helpers run their SELECT COUNT(*)
// through db.DB().QueryRowContext without dbcheck importing database/sql.
func countQuery(ctx context.Context, db orm.Database) dbcheck.CountFunc {
	return func(query string, args ...any) (int64, error) {
		var n int64
		if err := db.DB().QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			return 0, err
		}
		return n, nil
	}
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
	return dbcheck.UniqueRule(db.DriverName(), countQuery(ctx, db))
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
	return dbcheck.ExistsRule(db.DriverName(), countQuery(ctx, db))
}
