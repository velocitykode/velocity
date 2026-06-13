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
// The SQL assembly, identifier quoting, and unique-violation extraction are
// shared with the dbrules subpackage through validation/internal/dbcheck, so
// both surfaces stay byte-identical. This file keeps ONLY the reflection seam
// (dbIsNil / dbDriverName / dbQueryCount) and the deprecated public signatures,
// which fill that shared core's seam and delegate to it.
//
// Behavior note: AsValidationError here matches UNIQUE-constraint violations
// by error-string only (the canonical phrases each driver emits) unless an
// orm driver leaf has registered a typed classifier, because pulling the typed
// pq.Error / mysql.MySQLError checks in directly would force driver imports
// into core. dbrules.AsValidationError shares the identical code path; prefer
// it when you already depend on the driver.

import (
	"context"
	"fmt"
	"net/http"
	"reflect"

	"github.com/velocitykode/velocity/validation/internal/dbcheck"
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
// not expose the method, which makes the shared identifier quoting fall back
// to ANSI-style quoting and `?` placeholders.
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
// returned verbatim so the shared rule helpers can log it and emit the generic
// "Unable to validate <field>." message.
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

// dbCount binds the reflection seam and a context into a dbcheck.CountFunc so
// the shared rule helpers can run a count query without touching reflection.
func dbCount(ctx context.Context, db any) dbcheck.CountFunc {
	return func(query string, args ...any) (int64, error) {
		return dbQueryCount(ctx, db, query, args...)
	}
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
	return dbcheck.UniqueRule(dbDriverName(db), dbCount(ctx, db))
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
	return dbcheck.ExistsRule(dbDriverName(db), dbCount(ctx, db))
}

// AsValidationError inspects err for a UNIQUE-constraint violation and, when
// detected, returns a *ValidationErrors keyed by the offending field.
//
// Deprecated: use github.com/velocitykode/velocity/validation/dbrules.AsValidationError.
// Both share the identical code path via validation/internal/dbcheck; the typed
// pq.Error / mysql.MySQLError fast paths run only when an orm driver leaf has
// registered a classifier, so core takes no SQL-driver dependency.
func AsValidationError(err error, fieldRules map[string]string) (*ValidationErrors, bool) {
	return dbcheck.AsValidationError(err, fieldRules, ClassifyUniqueViolation)
}
