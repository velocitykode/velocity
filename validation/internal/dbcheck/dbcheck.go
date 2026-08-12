// Package dbcheck holds the driver-agnostic core of the DB-backed validation
// rules (unique, exists) and the UNIQUE-constraint error mapper. It is the
// single implementation shared by two surfaces:
//
//   - validation/dbrules: the orm-aware package, which fills the DB-access
//     seam with a typed orm.Database (DriverName(), DB().QueryRowContext).
//   - validation (dbrules_compat.go): the DEPRECATED, orm-free compatibility
//     shims, which fill the same seam structurally via reflection so core
//     takes no orm / database/sql / SQL-driver dependency.
//
// Keeping the SQL assembly, identifier quoting, and error extraction in one
// place means the security-sensitive logic (identifier validation before
// quoting, placeholder-only value binding, dotted-segment quoting) lives once
// and both surfaces stay byte-identical.
//
// # Import constraints
//
// This package is under validation/internal so only validation and its
// subpackages may import it. It depends ONLY on the standard library plus
// contract (which the core validation package already imports), so importing
// it into core adds no orm or SQL-driver dependency. It must NOT import
// validation itself, which would create a cycle; the classifier registry in
// core is reached through the Classifier function parameter the callers pass.
//
// The pure helpers ValidateIdentifier, QuoteIdentifier, Placeholder,
// ExtractMySQLKeyName, ExtractSQLiteColumn and SelectFields are exported so the
// dbrules package can expose them under their historical unexported names for
// its existing differential tests without re-implementing them.
package dbcheck

import (
	"fmt"
	"log/slog"
	"regexp"
	"strconv"
	"strings"

	"github.com/velocitykode/velocity/contract"
)

// CountFunc runs a SELECT COUNT(*) query and returns the scanned count. The
// caller binds the database handle and request context, so this package stays
// free of any database/sql or orm dependency. The raw error is returned
// verbatim so the rule helpers can log it and emit the generic
// "Unable to validate <field>." message.
type CountFunc func(query string, args ...any) (int64, error)

// Classifier matches a database error against a driver-typed UNIQUE-constraint
// violation. It has the same signature as validation.ClassifyUniqueViolation,
// which the callers pass in; this seam keeps dbcheck from importing validation.
type Classifier func(err error) (columnHint string, isUnique bool, matched bool)

// identifierRegex validates SQL table/column names. The regex allows dots so
// callers can pass schema-qualified names like "public.users" or compound
// references like "users.email"; ValidateIdentifier then relies on
// QuoteIdentifier to split on each dot and quote the parts individually so
// that "schema.table" becomes `"schema"."table"` (or "`schema`.`table`" on
// MySQL/SQLite) , not `"schema.table"`.
var identifierRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*$`)

// ValidateIdentifier rejects any table/column/index name that is not a bare or
// dotted SQL identifier, closing the door on injection before the name is
// interpolated into a query by QuoteIdentifier.
func ValidateIdentifier(name string) error {
	if name == "" || !identifierRegex.MatchString(name) {
		return fmt.Errorf("invalid SQL identifier: %q", name)
	}
	return nil
}

// QuoteIdentifier quotes a SQL identifier based on the database driver.
//
// Dotted identifiers such as "schema.table" or "table.column" are split on
// `.` and each segment is quoted independently, producing:
//
//	postgres: "schema"."table"      or "table"."column"
//	mysql:    `schema`.`table`      or `table`.`column`
//	sqlite:   `schema`.`table`      or `table`.`column`
//
// Backticks/double-quotes embedded in an individual segment are escaped by
// doubling them (standard SQL identifier-quoting rules). ValidateIdentifier
// refuses any character outside [a-zA-Z0-9_.] so escaping here is
// conservative: it exists to close the door on future inputs that might
// relax the allowlist.
func QuoteIdentifier(name, driver string) string {
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

// Placeholder returns the correct parameter placeholder for the driver.
// MySQL/SQLite use ?, Postgres uses $1, $2, etc.
func Placeholder(driver string, n int) string {
	if driver == "postgres" {
		return "$" + strconv.Itoa(n)
	}
	return "?"
}

// UniqueRule returns a RuleHandler that checks database uniqueness against the
// supplied driver and count seam.
//
// Parameters, in order: table, column, except id, id column.
//
// Raw DB errors are deliberately swallowed and replaced with a generic
// "Unable to validate <field>." message: schema names, table existence, and
// query text are server-side details that must not surface to a client-visible
// validation error string. The underlying error is logged via slog.Default()
// at ERROR level so operators retain a trail.
func UniqueRule(driver string, count CountFunc) contract.RuleHandler {
	return func(field string, value interface{}, params []string, data map[string]interface{}) error {
		if len(params) < 1 {
			return fmt.Errorf("unique rule requires at least a table name")
		}

		table := params[0]
		column := field
		if len(params) >= 2 && params[1] != "" {
			column = params[1]
		}

		if err := ValidateIdentifier(table); err != nil {
			return err
		}
		if err := ValidateIdentifier(column); err != nil {
			return err
		}

		argN := 1
		query := "SELECT COUNT(*) FROM " + QuoteIdentifier(table, driver) + " WHERE " + QuoteIdentifier(column, driver) + " = " + Placeholder(driver, argN)
		args := []interface{}{value}

		if len(params) >= 3 && params[2] != "" {
			idColumn := "id"
			if len(params) >= 4 && params[3] != "" {
				idColumn = params[3]
			}
			if err := ValidateIdentifier(idColumn); err != nil {
				return err
			}
			argN++
			query += " AND " + QuoteIdentifier(idColumn, driver) + " != " + Placeholder(driver, argN)
			args = append(args, params[2])
		}

		n, err := count(query, args...)
		if err != nil {
			slog.Default().Error("validation unique rule query failed",
				"field", field,
				"table", table,
				"column", column,
				"driver", driver,
				"err", err.Error(),
			)
			return fmt.Errorf("Unable to validate %s.", field)
		}

		if n > 0 {
			return fmt.Errorf("The %s has already been taken.", field)
		}
		return nil
	}
}

// ExistsRule returns a RuleHandler that checks a value exists in the database
// against the supplied driver and count seam.
//
// Parameters, in order: table, column.
//
// Same error handling as UniqueRule: the query runs through the count seam and
// raw DB errors are suppressed in the client-visible message but logged via
// slog.Default().
func ExistsRule(driver string, count CountFunc) contract.RuleHandler {
	return func(field string, value interface{}, params []string, data map[string]interface{}) error {
		if len(params) < 1 {
			return fmt.Errorf("exists rule requires at least a table name")
		}

		table := params[0]
		column := field
		if len(params) >= 2 && params[1] != "" {
			column = params[1]
		}

		if err := ValidateIdentifier(table); err != nil {
			return err
		}
		if err := ValidateIdentifier(column); err != nil {
			return err
		}

		query := "SELECT COUNT(*) FROM " + QuoteIdentifier(table, driver) + " WHERE " + QuoteIdentifier(column, driver) + " = " + Placeholder(driver, 1)

		n, err := count(query, value)
		if err != nil {
			slog.Default().Error("validation exists rule query failed",
				"field", field,
				"table", table,
				"column", column,
				"driver", driver,
				"err", err.Error(),
			)
			return fmt.Errorf("Unable to validate %s.", field)
		}

		if n == 0 {
			return fmt.Errorf("The selected %s is invalid.", field)
		}
		return nil
	}
}

// AsValidationError inspects err for a UNIQUE-constraint violation and, when
// one is detected, returns a *contract.ValidationErrors keyed by the offending
// field with the same "has already been taken" message the unique rule emits.
// classify is the typed-classifier seam (validation.ClassifyUniqueViolation);
// when it does not match, the generic per-driver error-string fallback runs.
//
// Returns (nil, false) when err is nil, when err is not a UNIQUE violation, or
// when fieldRules is empty.
func AsValidationError(err error, fieldRules map[string]string, classify Classifier) (*contract.ValidationErrors, bool) {
	if err == nil || len(fieldRules) == 0 {
		return nil, false
	}

	hint, ok := uniqueViolationColumn(err, classify)
	if !ok {
		return nil, false
	}

	fields := SelectFields(fieldRules, hint)
	if len(fields) == 0 {
		return nil, false
	}

	ve := &contract.ValidationErrors{
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
// UNIQUE-constraint violation. columnHint may be the column name (Postgres,
// SQLite) or an index name (MySQL); callers match it with SelectFields.
//
// Driver-typed matching (*pq.Error, *mysql.MySQLError) is delegated to the
// classifier seam. The orm/postgres and orm/mysql leaf packages register typed
// classifiers from init(), so when an app wires one of those drivers (directly
// or via orm/standard) the typed fast path runs and neither surface needs a
// SQL-driver import of its own. When no classifier recognises err, the function
// falls back to matching the canonical error string each driver emits (which
// also covers SQLite and wrapped / driver-erased errors).
//
// Returns ("", false) for any other error, including other constraint
// violations (FK, NOT NULL, CHECK): a classifier that recognises err as its
// driver's error but not a unique violation returns matched=true so the string
// fallback is skipped and the error is never misread as unique.
func uniqueViolationColumn(err error, classify Classifier) (string, bool) {
	// Typed classifiers (registered by orm/postgres, orm/mysql) get first and
	// authoritative say. An empty registry (no driver wired) reports
	// matched=false and we drop to the string fallback below.
	if classify != nil {
		if hint, isUnique, matched := classify(err); matched {
			return hint, isUnique
		}
	}

	// String fallback for wrapped / driver-erased errors, for SQLite, and for
	// Postgres / MySQL when their leaf packages are not imported. Conservative:
	// only the canonical phrases each driver emits.
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
		// Recover the column hint from the quoted constraint name whenever it
		// is present so multi-field callers are not attributed to every
		// candidate. extractPostgresConstraint returns "" when absent.
		return extractPostgresConstraint(msg), true
	case strings.Contains(msg, "Error 1062") || strings.Contains(msg, "ER_DUP_ENTRY"):
		return ExtractMySQLKeyName(msg), true
	case strings.Contains(msg, "UNIQUE constraint failed"):
		return ExtractSQLiteColumn(msg), true
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

// ExtractMySQLKeyName pulls the index name out of a MySQL ER_DUP_ENTRY message
// of the form `Duplicate entry 'val' for key 'idx_name'`. Returns "" when the
// pattern is not found; callers must tolerate an empty hint and fall back to
// the single-rule branch.
func ExtractMySQLKeyName(msg string) string {
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

// ExtractSQLiteColumn pulls the column hint out of a SQLite UNIQUE constraint
// message of the form `UNIQUE constraint failed: table.col` or
// `UNIQUE constraint failed: table.col1, table.col2`. Returns the first column
// for multi-column constraints; callers that need finer disambiguation can
// fall through to the all-fields branch by passing every candidate in
// fieldRules.
func ExtractSQLiteColumn(msg string) string {
	const marker = "UNIQUE constraint failed: "
	i := strings.Index(msg, marker)
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(msg[i+len(marker):])
	// First comma separates columns when the constraint covers more than one;
	// first dot separates table.column. We return the column half of the first
	// pair.
	if comma := strings.Index(rest, ","); comma >= 0 {
		rest = rest[:comma]
	}
	if dot := strings.LastIndex(rest, "."); dot >= 0 {
		return strings.TrimSpace(rest[dot+1:])
	}
	return strings.TrimSpace(rest)
}

// SelectFields picks the field(s) from fieldRules to attribute the violation
// to. hint is the column / index name extracted from the driver error; it may
// be empty.
//
// Match priority:
//  1. Exact field name == hint.
//  2. Hint contains field name as a token (suffix or underscore-delimited), so
//     an index named "users_email_unique" or "uniq_users_email" attributes to
//     "email".
//  3. Exactly one field in fieldRules: attribute to it.
//  4. Multi-field fall-through: attribute to all entries so the user sees
//     something rather than a 500.
func SelectFields(fieldRules map[string]string, hint string) []string {
	if hint != "" {
		// Pass 1: exact match.
		if _, ok := fieldRules[hint]; ok {
			return []string{hint}
		}
		// Pass 2: token / suffix containment. Lowercased for case-insensitive
		// matching against typical MySQL index names like "users_Email_UNIQUE".
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

	// Multi-field fall-through. Sort would be nicer but map iteration order is
	// fine for an error response; the caller is going to render all of them
	// anyway.
	out := make([]string, 0, len(fieldRules))
	for f := range fieldRules {
		out = append(out, f)
	}
	return out
}

// messageForRule returns the user-facing message for a given rule name,
// matching what the built-in rule emits when it fails in the validator.
// Unknown rules fall back to a generic "The <field> is invalid." so the helper
// stays useful for non-unique constraints the framework gains later (CHECK,
// EXCLUDE) without a breaking signature change.
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
