package dbrules

import (
	"context"
	"net/http"

	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/validation"
)

// dbHandlers builds the rule-name -> handler map registered on the core
// validator for DB-backed checks. ctx is threaded into unique/exists so a
// slow query is dropped when the client disconnects or a timeout fires,
// instead of piling up goroutines + connections on the request hot path.
//
// Returns nil when db is nil so the core engine registers no DB rules and a
// rules set referencing unique:/exists: simply has no handler for them
// (matching the previous "db == nil skips registration" behavior).
func dbHandlers(ctx context.Context, db orm.Database) map[string]validation.RuleHandler {
	if db == nil {
		return nil
	}
	return map[string]validation.RuleHandler{
		"unique": UniqueRuleCtx(ctx, db),
		"exists": ExistsRuleCtx(ctx, db),
	}
}

// CheckWithDB validates request data with database rules (unique, exists)
// available. The request's context is threaded into the database rules so
// unique/exists queries are cancelled when the client disconnects or a
// timeout fires; this prevents slow-query goroutine pile-up on the request
// hot path.
//
// Prefer CheckWithDBW(w, r, ...) when a *http.ResponseWriter is available.
func CheckWithDB(r *http.Request, rules validation.Rules, db orm.Database, messages ...validation.Messages) *validation.Result {
	return CheckWithDBW(nil, r, rules, db, messages...)
}

// CheckWithDBW is CheckWithDB plus a *http.ResponseWriter for MaxBytesReader
// wiring. See validation.CheckW for body-size handling.
func CheckWithDBW(w http.ResponseWriter, r *http.Request, rules validation.Rules, db orm.Database, messages ...validation.Messages) *validation.Result {
	return validation.CheckWithRulesW(w, r, rules, dbHandlers(r.Context(), db), messages...)
}

// CheckDataWithDB validates a data map with database rules available.
func CheckDataWithDB(data map[string]interface{}, rules validation.Rules, db orm.Database, messages ...validation.Messages) *validation.Result {
	return CheckDataWithDBCtx(context.Background(), data, rules, db, messages...)
}

// CheckDataWithDBCtx is like CheckDataWithDB but uses the caller-supplied
// context for unique/exists query cancellation. Use this in non-HTTP code
// paths (workers, jobs) that still need to validate against the DB.
func CheckDataWithDBCtx(ctx context.Context, data map[string]interface{}, rules validation.Rules, db orm.Database, messages ...validation.Messages) *validation.Result {
	if ctx == nil {
		ctx = context.Background()
	}
	return validation.CheckDataWithRules(data, rules, dbHandlers(ctx, db), messages...)
}
