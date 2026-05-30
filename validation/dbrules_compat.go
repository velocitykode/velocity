package validation

// This file restores the DB-backed validation symbols that used to live in
// the core validation package as DEPRECATED compatibility shims. The real
// implementation now lives in the orm-aware validation/dbrules subpackage so
// that core stays free of orm / database/sql / SQL-driver dependencies (see
// the CHANGELOG entry "DB-backed validation API moved out of validation").
//
// To keep that decoupling intact, the shims here reach the *sql.DB behind an
// orm.Database STRUCTURALLY, via reflection, instead of importing orm. The db
// argument is typed `any` rather than `orm.Database`; an `orm.Database`
// value satisfies it, so existing call sites such as
//
//	validation.CheckWithDB(r, rules, db)
//	validation.UniqueRule(db)
//
// continue to compile and behave as before. New code should prefer the
// dbrules subpackage:
//
//	dbrules.CheckWithDB(r, rules, db)
//	dbrules.UniqueRule(db)
//
// Behavior note: AsValidationError here matches UNIQUE-constraint violations
// by error-string only (the canonical phrases each driver emits) because the
// typed driver checks (pq.Error, mysql.MySQLError) would pull driver imports
// into core. dbrules.AsValidationError keeps the typed fast paths; prefer it
// when you have the dependency anyway.

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// anyType is the reflect.Type for interface{}, used to materialize a typed
// zero value when a nil query argument must be passed through reflection
// (reflect.ValueOf(nil) is an invalid Value and would panic in Call).
var anyType = reflect.TypeOf((*any)(nil)).Elem()

// dbIsNil reports whether db is a nil interface or a typed-nil pointer/etc.
// A `var db orm.Database = nil` passed into an `any` parameter is a non-nil
// interface wrapping a nil value, so a plain `db == nil` check is not enough.
func dbIsNil(db any) bool {
	if db == nil {
		return true
	}
	v := reflect.ValueOf(db)
	switch v.Kind() {
	case reflect.Ptr, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func, reflect.Chan:
		return v.IsNil()
	}
	return false
}

// dbDriverName calls db.DriverName() structurally. Returns "" when db does
// not expose the method, which makes quoteIdentifier/placeholder fall back to
// ANSI-style quoting and `?` placeholders.
func dbDriverName(db any) string {
	m := reflect.ValueOf(db).MethodByName("DriverName")
	if !m.IsValid() {
		return ""
	}
	out := m.Call(nil)
	if len(out) == 1 && out[0].Kind() == reflect.String {
		return out[0].String()
	}
	return ""
}

// dbQueryCount runs `query` with `args` against the *sql.DB behind db via
// db.DB().QueryRowContext(ctx, ...).Scan(&count), entirely through reflection
// so this package takes no database/sql or orm dependency. The error is
// returned verbatim so callers can log it and emit the generic
// "Unable to validate <field>." message the dbrules implementation uses.
func dbQueryCount(ctx context.Context, db any, query string, args ...any) (int64, error) {
	dbMethod := reflect.ValueOf(db).MethodByName("DB")
	if !dbMethod.IsValid() {
		return 0, fmt.Errorf("validation: db value does not expose DB() *sql.DB")
	}
	sqlDB := dbMethod.Call(nil)[0]
	qrc := sqlDB.MethodByName("QueryRowContext")
	if !qrc.IsValid() {
		return 0, fmt.Errorf("validation: db.DB() does not expose QueryRowContext")
	}

	callArgs := make([]reflect.Value, 0, len(args)+2)
	callArgs = append(callArgs, reflect.ValueOf(ctx), reflect.ValueOf(query))
	for _, a := range args {
		if a == nil {
			callArgs = append(callArgs, reflect.Zero(anyType))
			continue
		}
		callArgs = append(callArgs, reflect.ValueOf(a))
	}
	row := qrc.Call(callArgs)[0]

	scan := row.MethodByName("Scan")
	if !scan.IsValid() {
		return 0, fmt.Errorf("validation: QueryRowContext result does not expose Scan")
	}
	var count int64
	scanOut := scan.Call([]reflect.Value{reflect.ValueOf(&count)})
	if err, _ := scanOut[0].Interface().(error); err != nil {
		return 0, err
	}
	return count, nil
}

// dbHandlers builds the rule-name -> handler map for DB-backed checks,
// mirroring dbrules.dbHandlers. Returns nil when db is nil so referencing
// unique:/exists: in a rules set simply finds no handler.
func dbHandlers(ctx context.Context, db any) map[string]RuleHandler {
	if dbIsNil(db) {
		return nil
	}
	return map[string]RuleHandler{
		"unique": UniqueRuleCtx(ctx, db),
		"exists": ExistsRuleCtx(ctx, db),
	}
}

// CheckWithDB validates request data with database rules (unique, exists)
// available.
//
// Deprecated: use github.com/velocitykode/velocity/validation/dbrules.CheckWithDB.
// This shim is retained for source compatibility and reaches the database
// structurally via reflection so core takes no orm dependency.
func CheckWithDB(r *http.Request, rules Rules, db any, messages ...Messages) *Result {
	return CheckWithDBW(nil, r, rules, db, messages...)
}

// CheckWithDBW is CheckWithDB plus a *http.ResponseWriter for MaxBytesReader
// wiring. See CheckW for body-size handling.
//
// Deprecated: use github.com/velocitykode/velocity/validation/dbrules.CheckWithDBW.
func CheckWithDBW(w http.ResponseWriter, r *http.Request, rules Rules, db any, messages ...Messages) *Result {
	return CheckWithRulesW(w, r, rules, dbHandlers(r.Context(), db), messages...)
}

// CheckDataWithDB validates a data map with database rules available.
//
// Deprecated: use github.com/velocitykode/velocity/validation/dbrules.CheckDataWithDB.
func CheckDataWithDB(data map[string]interface{}, rules Rules, db any, messages ...Messages) *Result {
	return CheckDataWithDBCtx(context.Background(), data, rules, db, messages...)
}

// CheckDataWithDBCtx is like CheckDataWithDB but uses the caller-supplied
// context for unique/exists query cancellation.
//
// Deprecated: use github.com/velocitykode/velocity/validation/dbrules.CheckDataWithDBCtx.
func CheckDataWithDBCtx(ctx context.Context, data map[string]interface{}, rules Rules, db any, messages ...Messages) *Result {
	if ctx == nil {
		ctx = context.Background()
	}
	return CheckDataWithRules(data, rules, dbHandlers(ctx, db), messages...)
}

// dbIdentifierRegex validates SQL table/column names; dots allowed for
// schema-qualified names quoted segment-by-segment by dbQuoteIdentifier.
var dbIdentifierRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*(\.[a-zA-Z_][a-zA-Z0-9_]*)*$`)

func dbValidateIdentifier(name string) error {
	if name == "" || !dbIdentifierRegex.MatchString(name) {
		return fmt.Errorf("invalid SQL identifier: %q", name)
	}
	return nil
}

// dbQuoteIdentifier quotes a (possibly dotted) SQL identifier per driver.
func dbQuoteIdentifier(name, driver string) string {
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

// dbPlaceholder returns the parameter placeholder for the driver.
func dbPlaceholder(driver string, n int) string {
	if driver == "postgres" {
		return "$" + strconv.Itoa(n)
	}
	return "?"
}

// UniqueRule returns a RuleHandler that checks database uniqueness.
//
// Syntax: unique:table,column[,except_id[,id_column]]
//
// Deprecated: use github.com/velocitykode/velocity/validation/dbrules.UniqueRule.
func UniqueRule(db any) RuleHandler {
	return UniqueRuleCtx(context.Background(), db)
}

// UniqueRuleCtx is the context-aware variant of UniqueRule. The query runs
// under ctx so request cancellation aborts the round-trip. Raw DB errors are
// swallowed and replaced with a generic "Unable to validate <field>."
// message, with the underlying error logged via slog.Default().
//
// Deprecated: use github.com/velocitykode/velocity/validation/dbrules.UniqueRuleCtx.
func UniqueRuleCtx(ctx context.Context, db any) RuleHandler {
	if ctx == nil {
		ctx = context.Background()
	}
	driver := dbDriverName(db)
	return func(field string, value interface{}, params []string, data map[string]interface{}) error {
		if len(params) < 1 {
			return fmt.Errorf("unique rule requires at least a table name")
		}

		table := params[0]
		column := field
		if len(params) >= 2 && params[1] != "" {
			column = params[1]
		}

		if err := dbValidateIdentifier(table); err != nil {
			return err
		}
		if err := dbValidateIdentifier(column); err != nil {
			return err
		}

		argN := 1
		query := "SELECT COUNT(*) FROM " + dbQuoteIdentifier(table, driver) + " WHERE " + dbQuoteIdentifier(column, driver) + " = " + dbPlaceholder(driver, argN)
		args := []interface{}{value}

		if len(params) >= 3 && params[2] != "" {
			idColumn := "id"
			if len(params) >= 4 && params[3] != "" {
				idColumn = params[3]
			}
			if err := dbValidateIdentifier(idColumn); err != nil {
				return err
			}
			argN++
			query += " AND " + dbQuoteIdentifier(idColumn, driver) + " != " + dbPlaceholder(driver, argN)
			args = append(args, params[2])
		}

		count, err := dbQueryCount(ctx, db, query, args...)
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
// Deprecated: use github.com/velocitykode/velocity/validation/dbrules.ExistsRule.
func ExistsRule(db any) RuleHandler {
	return ExistsRuleCtx(context.Background(), db)
}

// ExistsRuleCtx is the context-aware variant of ExistsRule.
//
// Deprecated: use github.com/velocitykode/velocity/validation/dbrules.ExistsRuleCtx.
func ExistsRuleCtx(ctx context.Context, db any) RuleHandler {
	if ctx == nil {
		ctx = context.Background()
	}
	driver := dbDriverName(db)
	return func(field string, value interface{}, params []string, data map[string]interface{}) error {
		if len(params) < 1 {
			return fmt.Errorf("exists rule requires at least a table name")
		}

		table := params[0]
		column := field
		if len(params) >= 2 && params[1] != "" {
			column = params[1]
		}

		if err := dbValidateIdentifier(table); err != nil {
			return err
		}
		if err := dbValidateIdentifier(column); err != nil {
			return err
		}

		query := "SELECT COUNT(*) FROM " + dbQuoteIdentifier(table, driver) + " WHERE " + dbQuoteIdentifier(column, driver) + " = " + dbPlaceholder(driver, 1)

		count, err := dbQueryCount(ctx, db, query, value)
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

		if count == 0 {
			return fmt.Errorf("The selected %s is invalid.", field)
		}
		return nil
	}
}

// AsValidationError inspects err for a UNIQUE-constraint violation and, when
// detected, returns a *ValidationErrors keyed by the offending field.
//
// Deprecated: use github.com/velocitykode/velocity/validation/dbrules.AsValidationError,
// which adds typed pq.Error / mysql.MySQLError matching. This shim matches by
// error-string only so core takes no SQL-driver dependency.
func AsValidationError(err error, fieldRules map[string]string) (*ValidationErrors, bool) {
	if err == nil || len(fieldRules) == 0 {
		return nil, false
	}

	hint, ok := dbUniqueViolationColumn(err)
	if !ok {
		return nil, false
	}

	fields := dbSelectFields(fieldRules, hint)
	if len(fields) == 0 {
		return nil, false
	}

	ve := &ValidationErrors{
		Errors:       make(map[string][]string, len(fields)),
		RulesByField: make(map[string][]string, len(fields)),
	}
	for _, f := range fields {
		rule := fieldRules[f]
		ve.Errors[f] = append(ve.Errors[f], dbMessageForRule(f, rule))
		ve.RulesByField[f] = append(ve.RulesByField[f], rule)
	}
	return ve, true
}

// dbUniqueViolationColumn returns (columnHint, true) for a UNIQUE-constraint
// violation. Typed classifiers registered by the orm/postgres and orm/mysql
// leaves (via the classifier registry) get first say, so this shim gains the
// same typed *pq.Error / *mysql.MySQLError matching dbrules has when those
// drivers are wired, without core importing any SQL driver. When no classifier
// matches, it falls back to the canonical phrase each driver emits. The hint
// may be empty (Postgres) or a column/index name (MySQL/SQLite).
func dbUniqueViolationColumn(err error) (string, bool) {
	if hint, isUnique, matched := ClassifyUniqueViolation(err); matched {
		return hint, isUnique
	}

	msg := err.Error()
	switch {
	case strings.Contains(msg, "SQLSTATE 23505") ||
		strings.Contains(msg, "duplicate key value violates unique constraint"):
		// Postgres unique violation. Either form may carry the canonical
		// `... unique constraint "name"` phrase: pgx surfaces it alongside
		// SQLSTATE 23505, and (*pq.Error).Error() returns "pq: " + Message
		// (omitting the SQLSTATE code) when orm/postgres is not imported.
		// Recover the column hint from the quoted constraint name whenever
		// present so multi-field callers are not attributed to every
		// candidate. dbExtractPostgresConstraint returns "" when absent.
		return dbExtractPostgresConstraint(msg), true
	case strings.Contains(msg, "Error 1062") || strings.Contains(msg, "ER_DUP_ENTRY"):
		return dbExtractMySQLKeyName(msg), true
	case strings.Contains(msg, "UNIQUE constraint failed"):
		return dbExtractSQLiteColumn(msg), true
	}
	return "", false
}

// dbExtractPostgresConstraint pulls the constraint name out of pq's canonical
// unique-violation message, which embeds it in double quotes, e.g.
// `duplicate key value violates unique constraint "users_email_key"`.
// Returns "" when no quoted name is present.
func dbExtractPostgresConstraint(msg string) string {
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
	close := strings.Index(rest, `"`)
	if close < 0 {
		return ""
	}
	return rest[:close]
}

func dbExtractMySQLKeyName(msg string) string {
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

func dbExtractSQLiteColumn(msg string) string {
	const marker = "UNIQUE constraint failed: "
	i := strings.Index(msg, marker)
	if i < 0 {
		return ""
	}
	rest := strings.TrimSpace(msg[i+len(marker):])
	if comma := strings.Index(rest, ","); comma >= 0 {
		rest = rest[:comma]
	}
	if dot := strings.LastIndex(rest, "."); dot >= 0 {
		return strings.TrimSpace(rest[dot+1:])
	}
	return strings.TrimSpace(rest)
}

func dbSelectFields(fieldRules map[string]string, hint string) []string {
	if hint != "" {
		if _, ok := fieldRules[hint]; ok {
			return []string{hint}
		}
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

	out := make([]string, 0, len(fieldRules))
	for f := range fieldRules {
		out = append(out, f)
	}
	return out
}

func dbMessageForRule(field, rule string) string {
	switch rule {
	case "unique":
		return "The " + field + " has already been taken."
	case "exists":
		return "The selected " + field + " is invalid."
	default:
		return "The " + field + " is invalid."
	}
}
