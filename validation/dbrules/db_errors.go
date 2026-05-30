package dbrules

import (
	"strings"

	"github.com/velocitykode/velocity/validation"
)

// AsValidationError inspects err for a driver-specific UNIQUE-constraint
// violation and, when one is detected, returns a *validation.ValidationErrors
// keyed by the offending field with the same "has already been taken" message
// the unique: validator rule would have produced.
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
func AsValidationError(err error, fieldRules map[string]string) (*validation.ValidationErrors, bool) {
	if err == nil || len(fieldRules) == 0 {
		return nil, false
	}

	hint, ok := uniqueViolationColumn(err)
	if !ok {
		return nil, false
	}

	fields := selectFields(fieldRules, hint)
	if len(fields) == 0 {
		return nil, false
	}

	ve := &validation.ValidationErrors{
		Errors:       make(map[string][]string, len(fields)),
		RulesByField: make(map[string][]string, len(fields)),
	}
	for _, f := range fields {
		rule := fieldRules[f]
		ve.Errors[f] = append(ve.Errors[f], messageForRule(f, rule))
		ve.RulesByField[f] = append(ve.RulesByField[f], rule)
	}
	return ve, true
}

// uniqueViolationColumn returns (columnHint, true) when err is a
// UNIQUE-constraint violation. columnHint may be the column name
// (Postgres, SQLite) or an index name (MySQL); callers match it against
// field names with selectFields.
//
// Driver-typed matching (*pq.Error, *mysql.MySQLError) is delegated to the
// classifier registry in the core validation package. The orm/postgres and
// orm/mysql leaf packages register typed classifiers from init(), so when an
// app wires one of those drivers (directly or via orm/standard) the typed
// fast path runs and this package needs no SQL-driver import of its own. When
// no classifier recognises err, the function falls back to matching the
// canonical error string each driver emits (which also covers SQLite and
// wrapped / driver-erased errors).
//
// Returns ("", false) for any other error, including other constraint
// violations (FK, NOT NULL, CHECK): a classifier that recognises err as its
// driver's error but not a unique violation returns matched=true so the
// string fallback is skipped and the error is never misread as unique.
func uniqueViolationColumn(err error) (string, bool) {
	// Typed classifiers (registered by orm/postgres, orm/mysql) get first
	// and authoritative say. An empty registry (no driver wired) simply
	// reports matched=false and we drop to the string fallback below.
	if hint, isUnique, matched := validation.ClassifyUniqueViolation(err); matched {
		return hint, isUnique
	}

	// String fallback for wrapped / driver-erased errors, for SQLite, and
	// for Postgres / MySQL when their leaf packages are not imported.
	// Conservative: only the canonical phrases each driver emits.
	//
	// SQLite is intentionally string-only: a typed dependency on
	// mattn/go-sqlite3 would force CGO on every caller. The pure-Go
	// modernc.org/sqlite driver emits the same "UNIQUE constraint failed"
	// message via .Error(), so both are covered by the one branch.
	msg := err.Error()
	switch {
	case strings.Contains(msg, "SQLSTATE 23505") ||
		strings.Contains(msg, "duplicate key value violates unique constraint"):
		// Postgres unique violation. Either form may carry the canonical
		// `... unique constraint "name"` phrase: pgx surfaces it alongside
		// SQLSTATE 23505, and (*pq.Error).Error() returns "pq: " + Message
		// (omitting the SQLSTATE code) when orm/postgres is not imported.
		// Recover the column hint from the quoted constraint name whenever
		// it is present so multi-field callers are not attributed to every
		// candidate. extractPostgresConstraint returns "" when absent.
		return extractPostgresConstraint(msg), true
	case strings.Contains(msg, "Error 1062") || strings.Contains(msg, "ER_DUP_ENTRY"):
		return extractMySQLKeyName(msg), true
	case strings.Contains(msg, "UNIQUE constraint failed"):
		return extractSQLiteColumn(msg), true
	}

	return "", false
}

// extractPostgresConstraint pulls the constraint name out of pq's canonical
// unique-violation message, which embeds it in double quotes, e.g.
// `duplicate key value violates unique constraint "users_email_key"`.
// Returns "" when no quoted name is present.
func extractPostgresConstraint(msg string) string {
	const marker = "unique constraint "
	i := strings.Index(msg, marker)
	if i < 0 {
		return ""
	}
	rest := msg[i+len(marker):]
	open := strings.Index(rest, `"`)
	if open < 0 {
		return ""
	}
	rest = rest[open+1:]
	closeIdx := strings.Index(rest, `"`)
	if closeIdx < 0 {
		return ""
	}
	return rest[:closeIdx]
}

// extractMySQLKeyName pulls the index name out of a MySQL ER_DUP_ENTRY
// message of the form `Duplicate entry 'val' for key 'idx_name'`.
// Returns "" when the pattern is not found; callers must tolerate an
// empty hint and fall back to the single-rule branch.
func extractMySQLKeyName(msg string) string {
	const marker = "for key '"
	i := strings.Index(msg, marker)
	if i < 0 {
		return ""
	}
	rest := msg[i+len(marker):]
	j := strings.Index(rest, "'")
	if j < 0 {
		return ""
	}
	return rest[:j]
}

// extractSQLiteColumn pulls the column hint out of a SQLite UNIQUE
// constraint message of the form `UNIQUE constraint failed: table.col`
// or `UNIQUE constraint failed: table.col1, table.col2`. Returns the
// first column for multi-column constraints; callers that need finer
// disambiguation can fall through to the all-fields branch by passing
// every candidate in fieldRules.
func extractSQLiteColumn(msg string) string {
	const marker = "UNIQUE constraint failed: "
	i := strings.Index(msg, marker)
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(msg[i+len(marker):])
	// First comma separates columns when the constraint covers more
	// than one; first dot separates table.column. We return the column
	// half of the first pair.
	if comma := strings.Index(rest, ","); comma >= 0 {
		rest = rest[:comma]
	}
	if dot := strings.LastIndex(rest, "."); dot >= 0 {
		return strings.TrimSpace(rest[dot+1:])
	}
	return strings.TrimSpace(rest)
}

// selectFields picks the field(s) from fieldRules to attribute the
// violation to. hint is the column / index name extracted from the
// driver error; it may be empty.
//
// Match priority:
//  1. Exact field name == hint.
//  2. Hint contains field name as a token (suffix or underscore-delimited),
//     so an index named "users_email_unique" or "uniq_users_email"
//     attributes to "email".
//  3. Exactly one field in fieldRules: attribute to it.
//  4. Multi-field fall-through: attribute to all entries so the user
//     sees something rather than a 500.
func selectFields(fieldRules map[string]string, hint string) []string {
	if hint != "" {
		// Pass 1: exact match.
		if _, ok := fieldRules[hint]; ok {
			return []string{hint}
		}
		// Pass 2: token / suffix containment. Lowercased for case-
		// insensitive matching against typical MySQL index names like
		// "users_Email_UNIQUE".
		lowerHint := strings.ToLower(hint)
		for f := range fieldRules {
			lf := strings.ToLower(f)
			if lf == "" {
				continue
			}
			if strings.HasSuffix(lowerHint, "_"+lf) ||
				strings.HasSuffix(lowerHint, "."+lf) ||
				strings.Contains(lowerHint, "_"+lf+"_") ||
				lowerHint == lf {
				return []string{f}
			}
		}
	}

	if len(fieldRules) == 1 {
		for f := range fieldRules {
			return []string{f}
		}
	}

	// Multi-field fall-through. Sort would be nicer but map iteration
	// order is fine for an error response; the caller is going to
	// render all of them anyway.
	out := make([]string, 0, len(fieldRules))
	for f := range fieldRules {
		out = append(out, f)
	}
	return out
}

// messageForRule returns the user-facing message for a given rule name,
// matching what the built-in rule emits when it fails in the validator.
// Unknown rules fall back to a generic "The <field> is invalid." so the
// helper stays useful for non-unique constraints the framework gains
// later (CHECK, EXCLUDE) without a breaking signature change.
func messageForRule(field, rule string) string {
	switch rule {
	case "unique":
		return "The " + field + " has already been taken."
	case "exists":
		return "The selected " + field + " is invalid."
	default:
		return "The " + field + " is invalid."
	}
}
