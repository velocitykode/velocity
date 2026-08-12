package dbrules

import (
	"github.com/velocitykode/velocity/validation"
	"github.com/velocitykode/velocity/validation/internal/dbcheck"
)

// AsValidationError inspects err for a driver-specific UNIQUE-constraint
// violation and, when one is detected, returns a *validation.ValidationErrors
// keyed by the offending field with the same "has already been taken" message
// the unique validator rule would have produced.
//
// fieldRules is a map of field name to rule name. Pass the rules that
// correspond to columns covered by the UNIQUE constraint(s) you expect to
// encounter on the upcoming INSERT/UPDATE. The value lets the helper pick
// the right message: "unique" produces "The <field> has already been
// taken." Other rule names are accepted and produce a generic
// "The <field> is invalid." so the helper can be reused for future
// constraint types without a breaking change.
//
// Example: convert an INSERT race-loss into a 422 field error.
//
//	if err := db.Insert(user); err != nil {
//	    if verr, ok := dbrules.AsValidationError(err, map[string]string{
//	        "email": "unique",
//	    }); ok {
//	        return ctx.JSON(422, verr)
//	    }
//	    return err
//	}
//
// Field selection precedence on a unique-violation:
//  1. If the driver's error names a column (Postgres pq.Error.Column,
//     SQLite "UNIQUE constraint failed: table.col", MySQL constraint
//     "for key 'idx_name'"), and a field in fieldRules matches by name
//     (or by suffix for index names), that field is selected.
//  2. Otherwise, if fieldRules has exactly one entry, that entry is
//     selected.
//  3. Otherwise, every entry in fieldRules is attributed an error: the
//     driver did not say which column raced, and the caller declared
//     these as the candidates, so all of them are surfaced.
//
// Returns (nil, false) when err is nil, when err is not a UNIQUE
// violation, or when fieldRules is empty. Returns (nil, false) on
// non-unique constraint violations (FK, NOT NULL, CHECK) so callers can
// distinguish "race loss the user can recover from" from "structural
// failure that needs a 500".
//
// The detection and field-attribution logic lives once in
// validation/internal/dbcheck; the typed *pq.Error / *mysql.MySQLError
// fast paths are supplied by the
// classifier registry (validation.ClassifyUniqueViolation), which the
// orm/postgres and orm/mysql leaves populate from init().
func AsValidationError(err error, fieldRules map[string]string) (*validation.ValidationErrors, bool) {
	return dbcheck.AsValidationError(err, fieldRules, validation.ClassifyUniqueViolation)
}

// The unique-violation extraction and field-attribution logic lives once in
// validation/internal/dbcheck. These thin forwarders expose the pieces under
// the historical names this package's tests pin (db_errors_test.go), keeping a
// single implementation behind both surfaces.
func extractMySQLKeyName(msg string) string { return dbcheck.ExtractMySQLKeyName(msg) }
func extractSQLiteColumn(msg string) string { return dbcheck.ExtractSQLiteColumn(msg) }
func selectFields(fieldRules map[string]string, hint string) []string {
	return dbcheck.SelectFields(fieldRules, hint)
}
