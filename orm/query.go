package orm

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/velocitykode/velocity/orm/drivers"
)

// RawSQL marks a value in an Update or Insert map as raw SQL rather than
// a bound parameter. Values of this type are emitted verbatim into the
// generated statement; every other value, including plain strings that
// happen to look like SQL, is bound as a parameter.
//
// Use [NOW] for the common "set column to the database's current timestamp"
// case. Construct [RawSQL] values directly only when the string is trusted,
// server-generated SQL. Never construct a [RawSQL] value from
// user-controlled input: doing so is a SQL-injection vector by design.
//
// This is a type alias for the driver-level marker so the ORM and its
// grammars see the same underlying type without importing each other.
type RawSQL = drivers.RawSQL

// NOW is a [RawSQL] sentinel for the database's current timestamp. Pass it
// as a value in an Update or Insert map to have the grammar emit a
// database-clock expression rather than bind a literal string.
//
// Contract: DB clock, UTC wall clock. Each grammar pins the emitted SQL to
// UTC (`(NOW() AT TIME ZONE 'UTC')` on PostgreSQL, `UTC_TIMESTAMP()` on
// MySQL, `CURRENT_TIMESTAMP` on SQLite) so the stored value in a naive
// timestamp column is independent of the session timezone. Caveat: into a
// timestamptz column under a hand-set non-UTC session TimeZone the naive
// UTC value is misinterpreted; use an app-side stamp or raw `NOW()` there.
//
// ORM-managed lifecycle columns (created_at/updated_at/deleted_at) do NOT
// use this sentinel; they are stamped app-side with time.Now().UTC().
const NOW = drivers.NOW

// CurrentTimestamp is a [RawSQL] sentinel for the database's
// current-timestamp keyword, the portable counterpart to [NOW]. It carries
// the same contract: DB clock, UTC wall clock (grammars pin the emitted
// SQL to UTC on PostgreSQL and MySQL; SQLite's CURRENT_TIMESTAMP is
// already UTC).
const CurrentTimestamp = drivers.CurrentTimestamp

// validOperators is the allowlist of valid SQL operators
var validOperators = map[string]bool{
	"=": true, "!=": true, "<>": true,
	"<": true, ">": true, "<=": true, ">=": true,
	"LIKE": true, "NOT LIKE": true, "ILIKE": true,
	"IN": true, "NOT IN": true,
	"IS": true, "IS NOT": true,
	"BETWEEN": true, "NOT BETWEEN": true,
	"IS NULL": true, "IS NOT NULL": true,
}

// isValidOperator checks if the operator is in the allowlist (case-insensitive)
func isValidOperator(op string) bool {
	return validOperators[strings.ToUpper(strings.TrimSpace(op))]
}

// Query is a MUTABLE chainable query builder.
//
// Every chain step (Where, OrWhere, OrderBy, Limit, With, …) appends to the
// shared underlying slices on the receiver. Chained calls return the same
// *Query[T], so saving a handle mid-chain and continuing to chain off it
// mutates the handle.
//
// If you need to fork a query, e.g. to build two variants off a shared
// base, call Clone() to obtain an independent copy whose slices are not
// aliased with the original. Without Clone(), appends in one branch leak
// into the other.
//
// Clone() performs a shallow-deep copy: slice backing arrays are duplicated,
// scalar fields are copied by value, and the driver reference is shared
// (drivers are safe to share across goroutines).
//
// Callers who want a fluent-style "always fresh builder" API should wrap
// queries in their own constructor rather than attempt to reuse a single
// *Query[T] across requests.
type Query[T any] struct {
	driver drivers.Driver
	// mgr is the Manager the driver was resolved from (nil for detached
	// builders, e.g. eager-load constructor queries or WhereGroup subs).
	// Execution liveness checks consult it so a query built before
	// Manager.Shutdown and executed after still returns
	// ErrManagerShutdown instead of dereferencing a dead driver.
	mgr           *Manager
	table         string
	conditions    []drivers.Condition
	orders        []drivers.Order
	groups        []string
	having        []drivers.Condition
	joins         []drivers.Join
	limit         *int
	offset        *int
	columns       []string
	rawColumns    []drivers.RawColumn
	distinct      bool
	preloads      []string
	withTrashed   bool
	onlyTrashed   bool
	lockForUpdate bool // For pessimistic locking
	skipLocked    bool // For SKIP LOCKED clause
	hasSoftDelete bool // Whether the model supports soft deletes
	hasUpdatedAt  bool // Whether the model has an UpdatedAt column (skip injection on Update for immutable models)
	withRowHooks  bool // When true, bulk Update/Delete/ForceDelete fan out per-row AfterCommit/AfterRollback hooks (Tier C). Suppresses BulkAfterCommitHook.
	withBulkLock  bool // When true, the pre-SELECT issued by bulk hook capture is compiled with FOR UPDATE. No-op on the atomic ReturningGrammar path.

	// disabledScopes records the global scope names this query opts out
	// of. nil when no opt-outs have been set (no allocation in the
	// common case).
	disabledScopes map[string]bool

	// globalScopesApplied guards applyGlobalScopes against double-apply
	// when one terminal delegates to another (e.g. First -> Get).
	globalScopesApplied bool

	// Deferred error state. Chain builders capture the first validation
	// error here; terminal methods (Get, First, Count, Update, Delete, …)
	// return it before issuing any SQL.
	err error

	// Query state
	lastSQL  string
	lastArgs []any
}

// setErr records the first error hit during query construction. Subsequent
// setters no-op; terminal methods check this before executing.
func (q *Query[T]) setErr(op string, err error) {
	if q.err == nil && err != nil {
		q.err = fmt.Errorf("orm: %s: %w", op, err)
	}
}

// Err returns the first error captured during chain construction, or nil
// if none. Useful for mid-chain inspection; terminal methods also return
// this error ahead of executing.
func (q *Query[T]) Err() error {
	return q.err
}

// newQuery creates a new query builder for type T
func newQuery[T any]() *Query[T] {
	var drv drivers.Driver
	m := Default()
	if m != nil {
		drv = m.DefaultDriver()
	}
	q := &Query[T]{
		driver:        drv,
		mgr:           m,
		table:         getTableName[T](),
		columns:       []string{"*"},
		hasSoftDelete: modelHasSoftDelete[T](),
		hasUpdatedAt:  modelHasUpdatedAt[T](),
	}
	// Remember a reflect-friendly constructor for T so eager-load
	// helpers in relation*.go / morph.go can apply global scopes to T
	// without knowing T at compile time.
	rememberQueryConstructor[T]()
	if q.hasSoftDelete {
		registerSoftDeleteScopeOnce[T]()
	}
	return q
}

// modelHasUpdatedAt reports whether T should auto-stamp UpdatedAt on
// updates. AppendOnly suppresses UpdatedAt injection regardless.
func modelHasUpdatedAt[T any]() bool {
	feats, err := featuresForT[T]()
	if err != nil {
		return false
	}
	if feats.appendOnly {
		return false
	}
	return feats.hasUpdatedAt
}

// modelHasCreatedAt reports whether T manages a created_at column. It is
// false for a model that opted out of timestamps via UsesTimestamps(),
// because featuresFor clears hasCreatedAt in that case. Used by the UUID
// Last() helpers to choose an ordering column that the table actually has.
func modelHasCreatedAt[T any]() bool {
	feats, err := featuresForT[T]()
	if err != nil {
		return false
	}
	return feats.hasCreatedAt
}

// lastOrderColumn returns the column the UUID Last() helpers order by:
// created_at when the model manages timestamps, otherwise id. UUID primary
// keys are non-monotonic, so created_at is the meaningful insertion-order
// proxy; a timestamps-opted-out model has no created_at column, so id is the
// only ordering guaranteed to exist on the table.
func lastOrderColumn[T any]() string {
	if modelHasCreatedAt[T]() {
		return "created_at"
	}
	return "id"
}

// modelHasSoftDelete reports whether T embeds a SoftDeletes trait.
// Routes through the trait fingerprint cache.
func modelHasSoftDelete[T any]() bool {
	feats, err := featuresForT[T]()
	if err != nil {
		return false
	}
	return feats.hasDeletedAt
}

// checkSoftDelete is the reflective counterpart to modelHasSoftDelete.
// Used by relation loaders that have a reflect.Type but no compile-time T.
func checkSoftDelete(t reflect.Type) bool {
	feats, err := featuresFor(t)
	if err != nil {
		return false
	}
	return feats.hasDeletedAt
}

// Clone returns an independent copy of the query. Slice fields are
// duplicated so subsequent chain calls on the clone do not mutate the
// source (and vice versa). The driver reference is shared because
// drivers.Driver implementations are safe for concurrent use.
//
// Use Clone when forking a query into variants:
//
//	base := Model[User]{}.Where("tenant_id = ?", tid)
//	active := base.Clone().Where("active = ?", true)
//	trashed := base.Clone().OnlyTrashed()
//
// Without Clone, the subsequent Where/OnlyTrashed calls would leak into
// the shared base and each other.
func (q *Query[T]) Clone() *Query[T] {
	if q == nil {
		return nil
	}
	clone := &Query[T]{
		driver:              q.driver,
		mgr:                 q.mgr,
		table:               q.table,
		distinct:            q.distinct,
		withTrashed:         q.withTrashed,
		onlyTrashed:         q.onlyTrashed,
		lockForUpdate:       q.lockForUpdate,
		skipLocked:          q.skipLocked,
		hasSoftDelete:       q.hasSoftDelete,
		hasUpdatedAt:        q.hasUpdatedAt,
		withRowHooks:        q.withRowHooks,
		withBulkLock:        q.withBulkLock,
		globalScopesApplied: q.globalScopesApplied,
		err:                 q.err,
		lastSQL:             q.lastSQL,
	}
	if q.disabledScopes != nil {
		clone.disabledScopes = make(map[string]bool, len(q.disabledScopes))
		for k, v := range q.disabledScopes {
			clone.disabledScopes[k] = v
		}
	}
	if q.conditions != nil {
		clone.conditions = append([]drivers.Condition(nil), q.conditions...)
	}
	if q.orders != nil {
		clone.orders = append([]drivers.Order(nil), q.orders...)
	}
	if q.groups != nil {
		clone.groups = append([]string(nil), q.groups...)
	}
	if q.having != nil {
		clone.having = append([]drivers.Condition(nil), q.having...)
	}
	if q.joins != nil {
		clone.joins = append([]drivers.Join(nil), q.joins...)
	}
	if q.columns != nil {
		clone.columns = append([]string(nil), q.columns...)
	}
	if q.rawColumns != nil {
		clone.rawColumns = append([]drivers.RawColumn(nil), q.rawColumns...)
	}
	if q.preloads != nil {
		clone.preloads = append([]string(nil), q.preloads...)
	}
	if q.limit != nil {
		n := *q.limit
		clone.limit = &n
	}
	if q.offset != nil {
		n := *q.offset
		clone.offset = &n
	}
	if q.lastArgs != nil {
		clone.lastArgs = append([]any(nil), q.lastArgs...)
	}
	return clone
}

// operatorRegistry returns the active driver's operator registry, or nil
// when the query is detached from a driver (e.g. WhereGroup sub-builders).
func (q *Query[T]) operatorRegistry() map[string]drivers.OperatorSpec {
	if q.driver == nil {
		return nil
	}
	return q.driver.OperatorRegistry()
}

// Where adds a WHERE condition
func (q *Query[T]) Where(condition string, args ...any) *Query[T] {
	col, op, val, err := parseCondition(condition, args, q.operatorRegistry())
	if err != nil {
		q.setErr("Where", err)
		return q
	}
	spec, err := q.resolveOperator(op, val)
	if err != nil {
		q.setErr("Where", err)
		return q
	}
	if spec == nil {
		if val, err = normalizeMultiValue(op, val); err != nil {
			q.setErr("Where", err)
			return q
		}
	}
	q.conditions = append(q.conditions, drivers.Condition{
		Column:   col,
		Operator: op,
		Value:    val,
		Type:     "and",
		Spec:     spec,
	})
	return q
}

// OrWhere adds an OR WHERE condition
func (q *Query[T]) OrWhere(condition string, args ...any) *Query[T] {
	col, op, val, err := parseCondition(condition, args, q.operatorRegistry())
	if err != nil {
		q.setErr("OrWhere", err)
		return q
	}
	spec, err := q.resolveOperator(op, val)
	if err != nil {
		q.setErr("OrWhere", err)
		return q
	}
	if spec == nil {
		if val, err = normalizeMultiValue(op, val); err != nil {
			q.setErr("OrWhere", err)
			return q
		}
	}
	q.conditions = append(q.conditions, drivers.Condition{
		Column:   col,
		Operator: op,
		Value:    val,
		Type:     "or",
		Spec:     spec,
	})
	return q
}

// WhereGroup adds a parenthesized AND-group of WHERE conditions. The
// closure receives a fresh sub-builder; predicates added to it are
// emitted inside parentheses so AND/OR precedence binds correctly.
//
// Example:
//
//	Where("team_id = ?", t).WhereGroup(func(q *Query[User]) {
//	    q.Where("name ILIKE ?", x).OrWhere("email ILIKE ?", x)
//	})
//
// emits "team_id = ? AND (name ILIKE ? OR email ILIKE ?)" rather than
// the misbinding "team_id = ? AND name ILIKE ? OR email ILIKE ?" that a
// flat chain would produce.
//
// The first error captured by the inner builder propagates to q so
// terminal methods surface it before issuing SQL. Empty groups are
// dropped (no parentheses emitted) to keep the SQL clean.
func (q *Query[T]) WhereGroup(fn func(*Query[T])) *Query[T] {
	return q.appendGroup("and", fn)
}

// OrWhereGroup adds a parenthesized OR-group of WHERE conditions.
// Behaviour mirrors WhereGroup but the group itself is OR'd against
// the surrounding predicates.
func (q *Query[T]) OrWhereGroup(fn func(*Query[T])) *Query[T] {
	return q.appendGroup("or", fn)
}

// appendGroup runs the closure against a fresh sub-builder, captures
// the resulting conditions, and appends them as a single grouped
// Condition. Errors from the sub-builder propagate up; empty groups
// are skipped.
func (q *Query[T]) appendGroup(joinType string, fn func(*Query[T])) *Query[T] {
	if fn == nil {
		return q
	}
	sub := &Query[T]{
		driver:        q.driver,
		mgr:           q.mgr,
		table:         q.table,
		hasSoftDelete: q.hasSoftDelete,
		hasUpdatedAt:  q.hasUpdatedAt,
	}
	fn(sub)
	if sub.err != nil {
		q.setErr("WhereGroup", sub.err)
		return q
	}
	if len(sub.conditions) == 0 {
		return q
	}
	q.conditions = append(q.conditions, drivers.Condition{
		Type:  joinType,
		Group: sub.conditions,
	})
	return q
}

// WhereIn adds a WHERE IN condition
func (q *Query[T]) WhereIn(field string, values []any) *Query[T] {
	if err := validateIdentifier(field); err != nil {
		q.setErr("WhereIn", err)
		return q
	}
	q.conditions = append(q.conditions, drivers.Condition{
		Column:   field,
		Operator: "IN",
		Value:    values,
		Type:     "and",
	})
	return q
}

// WhereNotIn adds a WHERE NOT IN condition
func (q *Query[T]) WhereNotIn(field string, values []any) *Query[T] {
	if err := validateIdentifier(field); err != nil {
		q.setErr("WhereNotIn", err)
		return q
	}
	q.conditions = append(q.conditions, drivers.Condition{
		Column:   field,
		Operator: "NOT IN",
		Value:    values,
		Type:     "and",
	})
	return q
}

// WhereNull adds a WHERE IS NULL condition
func (q *Query[T]) WhereNull(field string) *Query[T] {
	if err := validateIdentifier(field); err != nil {
		q.setErr("WhereNull", err)
		return q
	}
	q.conditions = append(q.conditions, drivers.Condition{
		Column:   field,
		Operator: "IS NULL",
		Value:    nil,
		Type:     "and",
	})
	return q
}

// WhereNotNull adds a WHERE IS NOT NULL condition
func (q *Query[T]) WhereNotNull(field string) *Query[T] {
	if err := validateIdentifier(field); err != nil {
		q.setErr("WhereNotNull", err)
		return q
	}
	q.conditions = append(q.conditions, drivers.Condition{
		Column:   field,
		Operator: "IS NOT NULL",
		Value:    nil,
		Type:     "and",
	})
	return q
}

// WhereBetween adds a WHERE BETWEEN condition
func (q *Query[T]) WhereBetween(field string, start, end any) *Query[T] {
	if err := validateIdentifier(field); err != nil {
		q.setErr("WhereBetween", err)
		return q
	}
	q.conditions = append(q.conditions, drivers.Condition{
		Column:   field,
		Operator: "BETWEEN",
		Value:    []any{start, end},
		Type:     "and",
	})
	return q
}

// parseCondition parses a condition string and args into column, operator, and value.
// Handles formats like:
//   - "col = ?", val         -> col, "=", val
//   - "col", val             -> col, "=", val (default operator)
//   - "col", op, val         -> col, op, val (three-argument convenience form)
//   - "col IS NULL"          -> col, "IS NULL", nil
//   - "col IS NOT NULL"      -> col, "IS NOT NULL", nil
//   - "col NOT LIKE ?", val  -> col, "NOT LIKE", val (multi-word operators)
//
// Anything in the condition string beyond column, operator, and a single
// trailing "?" placeholder is a hard error: inline literals ("age > 18")
// and compound predicates ("a = ? AND b = ?") were previously truncated
// silently, dropping predicates and broadening result sets.
//
// registry is the active driver's operator registry (nil when the query
// is detached from a driver). It only widens which tokens count as an
// operator during parsing; admissibility is still decided by the caller
// via resolveOperator, which has the driver in hand.
func parseCondition(condition string, args []any, registry map[string]drivers.OperatorSpec) (column, operator string, value any, err error) {
	condition = strings.TrimSpace(condition)

	// IS NULL / IS NOT NULL must be the entire condition (case-insensitive).
	// A mid-string match means a compound predicate follows; that falls
	// through to the token parser below and is rejected there.
	upperCond := strings.ToUpper(condition)
	if strings.HasSuffix(upperCond, " IS NOT NULL") {
		if len(args) > 0 {
			return "", "", nil, fmt.Errorf(
				"condition %q takes no bind values but %d argument(s) were given", condition, len(args))
		}
		col := strings.TrimSpace(condition[:len(condition)-len(" IS NOT NULL")])
		if err := validateIdentifier(col); err != nil {
			return "", "", nil, err
		}
		return col, "IS NOT NULL", nil, nil
	}
	if strings.HasSuffix(upperCond, " IS NULL") {
		if len(args) > 0 {
			return "", "", nil, fmt.Errorf(
				"condition %q takes no bind values but %d argument(s) were given", condition, len(args))
		}
		col := strings.TrimSpace(condition[:len(condition)-len(" IS NULL")])
		if err := validateIdentifier(col); err != nil {
			return "", "", nil, err
		}
		return col, "IS NULL", nil, nil
	}

	parts := strings.Fields(condition)
	if len(parts) == 0 {
		return "", "", nil, fmt.Errorf("empty condition")
	}

	if len(parts) == 1 {
		// Bare column: "col", val (implicit =) or the three-argument
		// convenience form "col", op, val.
		if err := validateIdentifier(parts[0]); err != nil {
			return "", "", nil, err
		}
		switch len(args) {
		case 0:
			return parts[0], "=", nil, nil
		case 1:
			return parts[0], "=", args[0], nil
		case 2:
			op, ok := args[0].(string)
			if !ok || !operatorToken(op, registry) {
				return "", "", nil, fmt.Errorf(
					"condition %q with two arguments is the three-argument form (column, operator, value), but %#v is not a SQL operator",
					condition, args[0])
			}
			op = strings.TrimSpace(op)
			if isValidOperator(op) {
				// Grammars special-case multi-value operators (IN, NOT IN,
				// BETWEEN, ...) by exact uppercase match; normalise so a
				// lowercase operator string still hits those cases.
				op = strings.ToUpper(op)
			}
			return parts[0], op, args[1], nil
		default:
			return "", "", nil, fmt.Errorf(
				"condition %q: too many arguments (%d); use the three-argument form (column, operator, value) or chain Where calls",
				condition, len(args))
		}
	}

	column = parts[0]
	if err := validateIdentifier(column); err != nil {
		return "", "", nil, err
	}

	// Longest-match operator: try the two-token form first so NOT LIKE,
	// NOT IN, IS NOT and NOT BETWEEN parse as a single operator.
	operator = parts[1]
	rest := parts[2:]
	if len(parts) >= 3 {
		if twoTok := parts[1] + " " + parts[2]; operatorToken(twoTok, registry) {
			operator = twoTok
			if isValidOperator(twoTok) {
				// Grammars special-case multi-word operators (NOT IN,
				// NOT BETWEEN, ...) by exact uppercase match.
				operator = strings.ToUpper(twoTok)
			}
			rest = parts[3:]
		}
	}

	// After column and operator the only legal remainder is a single "?"
	// placeholder (or nothing).
	if len(rest) > 1 || (len(rest) == 1 && rest[0] != "?") {
		return "", "", nil, fmt.Errorf(
			"unparseable condition %q: a condition is a single \"column operator ?\" predicate; bind values with ? (not inline literals), chain Where/OrWhere or use WhereGroup for compound predicates, and use WhereBetween/WhereIn for multi-value operators",
			condition)
	}

	// Operator admissibility is intentionally deferred to the caller,
	// which has the active driver in hand and can consult its
	// OperatorRegistry before rejecting the operator.

	if len(rest) == 1 {
		if len(args) != 1 {
			return "", "", nil, fmt.Errorf(
				"condition %q has one placeholder but %d argument(s) were given", condition, len(args))
		}
		value = args[0]
	} else {
		if len(args) > 0 {
			return "", "", nil, fmt.Errorf(
				"condition %q has no placeholder but %d argument(s) were given; bind values with ?", condition, len(args))
		}
		// Every operator reaching this path binds a value; the nullary
		// forms (IS NULL / IS NOT NULL) returned from the fast path above,
		// except when unusual whitespace routes them through tokenisation.
		// A dangling operator with no placeholder would otherwise compile
		// to a NULL-bound predicate (e.g. age > NULL).
		if upper := strings.ToUpper(strings.TrimSpace(operator)); upper != "IS NULL" && upper != "IS NOT NULL" {
			return "", "", nil, fmt.Errorf(
				"condition %q: operator %q requires a bound value; add a ? placeholder and argument (use WhereNull/WhereNotNull for null checks)",
				condition, operator)
		}
	}

	return column, operator, value, nil
}

// operatorToken reports whether op names a known operator, either in the
// built-in allowlist or in the supplied driver registry. It only decides
// how a condition tokenises; admissibility is resolveOperator's call.
func operatorToken(op string, registry map[string]drivers.OperatorSpec) bool {
	if isValidOperator(op) {
		return true
	}
	op = strings.TrimSpace(op)
	if _, ok := registry[strings.ToUpper(op)]; ok {
		return true
	}
	_, ok := registry[op]
	return ok
}

// resolveOperator decides whether an operator is admissible. Built-in scalar
// operators return (nil, nil). Driver-registered operators return their
// OperatorSpec with cond.Value validated against ParamShape. Unknown
// operators return the existing "invalid SQL operator" error so the
// rejection surface stays unchanged for callers that don't extend it.
func (q *Query[T]) resolveOperator(op string, val any) (*drivers.OperatorSpec, error) {
	if isValidOperator(op) {
		// ILIKE is PostgreSQL-only. A detached builder (nil driver, e.g.
		// WhereGroup sub-builders) cannot know its dialect yet, so it
		// keeps accepting ILIKE; a driver-bound builder rejects it on
		// any other dialect instead of shipping broken SQL.
		if strings.EqualFold(strings.TrimSpace(op), "ILIKE") && q.driver != nil && q.driver.DriverName() != "postgres" {
			return nil, fmt.Errorf("operator ILIKE is PostgreSQL-only; driver %q does not support it (use LIKE)", q.driver.DriverName())
		}
		return nil, nil
	}
	if q.driver == nil {
		return nil, fmt.Errorf("invalid SQL operator: %q", op)
	}
	registry := q.driver.OperatorRegistry()
	if registry == nil {
		return nil, fmt.Errorf("invalid SQL operator: %q", op)
	}
	spec, ok := registry[strings.ToUpper(strings.TrimSpace(op))]
	if !ok {
		spec, ok = registry[strings.TrimSpace(op)]
	}
	if !ok {
		return nil, fmt.Errorf("invalid SQL operator: %q", op)
	}
	if err := validateOperatorValue(&spec, val); err != nil {
		return nil, err
	}
	return &spec, nil
}

// validateOperatorValue rejects a cond.Value that does not match the spec's
// ParamShape. Catches misuse at parse time so a SQL syntax error never
// leaks to execute time.
func validateOperatorValue(spec *drivers.OperatorSpec, val any) error {
	switch spec.ParamShape {
	case drivers.ParamScalar:
		// Any single value is acceptable; nil is rejected because every
		// registered scalar op consumes one bound parameter.
		if val == nil {
			return fmt.Errorf("operator %q requires a non-nil value", spec.Op)
		}
	case drivers.ParamSlice, drivers.ParamArray:
		if _, ok := val.([]any); !ok {
			return fmt.Errorf("operator %q requires []any, got %T", spec.Op, val)
		}
	case drivers.ParamJSON:
		switch val.(type) {
		case string, []byte, json.RawMessage:
		default:
			return fmt.Errorf("operator %q requires JSON (string, []byte, or json.RawMessage), got %T", spec.Op, val)
		}
	}
	return nil
}

// normalizeMultiValue coerces the bound value of a built-in multi-value
// operator (IN, NOT IN, BETWEEN, NOT BETWEEN) to []any. The grammars
// type-assert cond.Value.([]any) when expanding placeholders; a typed
// slice ([]int, []string, ...) fails that assertion and was previously
// indistinguishable from an EMPTY list, so the empty-list constant
// rewrite kicked in: Where("id NOT IN ?", []int{1}) compiled to the
// always-true 1=1 and the IN form to the never-true 1=0. Reflection
// flattens any slice or array kind to []any; non-slice values and
// wrong-arity BETWEEN bounds are build-time errors so a malformed
// predicate never reaches SQL. Driver-registered operators (Spec != nil)
// are excluded: validateOperatorValue already enforces []any for them.
func normalizeMultiValue(op string, val any) (any, error) {
	switch strings.ToUpper(strings.TrimSpace(op)) {
	case "IN", "NOT IN":
		return toAnySlice(op, val)
	case "BETWEEN", "NOT BETWEEN":
		vs, err := toAnySlice(op, val)
		if err != nil {
			return nil, err
		}
		if len(vs) != 2 {
			return nil, fmt.Errorf("operator %s requires exactly two values (start, end), got %d", op, len(vs))
		}
		return vs, nil
	}
	return val, nil
}

// toAnySlice converts a slice or array value of any element type to
// []any. []byte is rejected explicitly: in database terms it is a
// scalar blob, never a list of values, so accepting it would bind one
// placeholder per byte.
func toAnySlice(op string, val any) ([]any, error) {
	if vs, ok := val.([]any); ok {
		return vs, nil
	}
	if _, ok := val.([]byte); ok {
		return nil, fmt.Errorf("operator %s requires a slice of values, got []byte (a scalar blob); bind it with a scalar operator instead", op)
	}
	rv := reflect.ValueOf(val)
	if !rv.IsValid() || (rv.Kind() != reflect.Slice && rv.Kind() != reflect.Array) {
		return nil, fmt.Errorf("operator %s requires a slice of values (e.g. []any{...}), got %T", op, val)
	}
	out := make([]any, rv.Len())
	for i := range out {
		out[i] = rv.Index(i).Interface()
	}
	return out, nil
}

// OrderBy adds an ORDER BY clause
func (q *Query[T]) OrderBy(column, direction string) *Query[T] {
	if err := validateIdentifier(column); err != nil {
		q.setErr("OrderBy", err)
		return q
	}
	dir := strings.ToUpper(strings.TrimSpace(direction))
	if dir != "ASC" && dir != "DESC" {
		dir = "ASC"
	}
	q.orders = append(q.orders, drivers.Order{
		Column:    column,
		Direction: dir,
	})
	return q
}

// OrderByDesc adds an ORDER BY DESC clause
func (q *Query[T]) OrderByDesc(column string) *Query[T] {
	return q.OrderBy(column, "DESC")
}

// GroupBy adds a GROUP BY clause
func (q *Query[T]) GroupBy(columns ...string) *Query[T] {
	for _, col := range columns {
		if err := validateIdentifier(col); err != nil {
			q.setErr("GroupBy", err)
			return q
		}
	}
	q.groups = append(q.groups, columns...)
	return q
}

// Having adds a HAVING condition
func (q *Query[T]) Having(condition string, args ...any) *Query[T] {
	col, op, val, err := parseCondition(condition, args, q.operatorRegistry())
	if err != nil {
		q.setErr("Having", err)
		return q
	}
	if err := validateIdentifier(col); err != nil {
		q.setErr("Having", err)
		return q
	}
	spec, err := q.resolveOperator(op, val)
	if err != nil {
		q.setErr("Having", err)
		return q
	}
	if spec == nil {
		if val, err = normalizeMultiValue(op, val); err != nil {
			q.setErr("Having", err)
			return q
		}
	}
	q.having = append(q.having, drivers.Condition{
		Column:   col,
		Operator: op,
		Value:    val,
		Type:     "and",
		Spec:     spec,
	})
	return q
}

// buildJoinOn safely builds a JOIN ON clause with validated identifiers and operator.
// Returns the built clause and a validation error if either identifier or
// operator is invalid; callers funnel the error into q.err via setErr.
func (q *Query[T]) buildJoinOn(first, operator, second string) (string, error) {
	if err := validateIdentifier(first); err != nil {
		return "", err
	}
	if err := validateIdentifier(second); err != nil {
		return "", err
	}
	if !isValidOperator(operator) {
		return "", fmt.Errorf("invalid JOIN operator: %q", operator)
	}
	// Chain-time driver dereference: a query built after Manager.Shutdown
	// (or with no connection configured) has a nil driver here.
	if err := q.driverLive(); err != nil {
		return "", err
	}
	grammar := q.driver.Grammar()
	return fmt.Sprintf("%s %s %s", grammar.QuoteIdentifier(first), operator, grammar.QuoteIdentifier(second)), nil
}

// Join adds an INNER JOIN
func (q *Query[T]) Join(table, first, operator, second string) *Query[T] {
	if err := validateIdentifier(table); err != nil {
		q.setErr("Join", err)
		return q
	}
	on, err := q.buildJoinOn(first, operator, second)
	if err != nil {
		q.setErr("Join", err)
		return q
	}
	q.joins = append(q.joins, drivers.Join{
		Type:  "INNER",
		Table: table,
		On:    on,
	})
	return q
}

// LeftJoin adds a LEFT JOIN
func (q *Query[T]) LeftJoin(table, first, operator, second string) *Query[T] {
	if err := validateIdentifier(table); err != nil {
		q.setErr("LeftJoin", err)
		return q
	}
	on, err := q.buildJoinOn(first, operator, second)
	if err != nil {
		q.setErr("LeftJoin", err)
		return q
	}
	q.joins = append(q.joins, drivers.Join{
		Type:  "LEFT",
		Table: table,
		On:    on,
	})
	return q
}

// RightJoin adds a RIGHT JOIN
func (q *Query[T]) RightJoin(table, first, operator, second string) *Query[T] {
	if err := validateIdentifier(table); err != nil {
		q.setErr("RightJoin", err)
		return q
	}
	on, err := q.buildJoinOn(first, operator, second)
	if err != nil {
		q.setErr("RightJoin", err)
		return q
	}
	q.joins = append(q.joins, drivers.Join{
		Type:  "RIGHT",
		Table: table,
		On:    on,
	})
	return q
}

// Select specifies columns to select.
//
// Each column must be one of:
//
//   - a plain identifier matching ^[a-zA-Z_][a-zA-Z0-9_.]*$ (e.g. "id",
//     "users.email", "created_at"); these are quoted by the dialect's
//     QuoteIdentifier at compile time.
//   - the wildcard "*".
//   - an aggregate expression using EXACTLY ONE of the five SQL
//     standard aggregate functions (uppercase only): COUNT, SUM, AVG,
//     MIN, MAX. Examples: "COUNT(*)", "COUNT(id)", "SUM(amount)",
//     "AVG(price)", "MIN(orders.total) AS min_total", "MAX(price) as
//     max_price". The AS clause is optional and case-insensitive.
//
// All other function names, including but not limited to CONCAT,
// VERSION, CURRENT_DATABASE, PG_SLEEP, USER, LOAD_FILE, NOW, IF,
// LENGTH, LOWER, SUBSTR, and any user-defined function, are rejected
// here. Anything outside the five-aggregate allowlist must be
// expressed through SelectRaw with bound parameters.
//
// The aggregate whitelist also forbids quotes, backticks, semicolons,
// comments (-- and /* */), and the keywords SELECT, UNION, INSERT,
// UPDATE, DELETE, DROP, TRUNCATE, EXEC, EXECUTE, FROM, WHERE, JOIN,
// INTO, VALUES, ALTER, CREATE, GRANT, REVOKE. Anything outside the
// allowlist is captured as a deferred error on the query (terminal
// methods return it) and no SQL is issued.
//
// For arbitrary SQL projections (other functions, window functions,
// CASE expressions, dialect-specific syntax, sub-selects), use
// SelectRaw with bound parameters.
func (q *Query[T]) Select(columns ...string) *Query[T] {
	for _, col := range columns {
		if strings.Contains(col, "(") || col == "*" {
			// Aggregate-or-wildcard path: enforce the projection
			// whitelist. ValidateSelectColumn rejects quotes,
			// comments, dangerous keywords, and any shape outside
			// the COUNT/SUM/MIN/MAX/AVG-style aggregate grammar.
			if err := drivers.ValidateSelectColumn(col); err != nil {
				q.setErr("Select", err)
				return q
			}
			continue
		}
		if err := validateIdentifier(col); err != nil {
			q.setErr("Select", err)
			return q
		}
	}
	q.columns = columns
	return q
}

// SelectRaw appends a trusted raw SQL expression to the projection list
// with bound parameters. The expression is emitted verbatim into the
// SELECT clause; "?" placeholders inside Expr are replaced with the
// dialect's placeholder syntax (literal "?" for MySQL/SQLite, "$N" for
// PostgreSQL) and Args are appended to the parameter list in order.
//
// SelectRaw is the escape hatch for projections that the strict Select
// whitelist cannot express: window functions, CASE expressions, vendor
// extensions, sub-selects. The caller assumes full responsibility for
// the safety of Expr; values that originate from user input MUST flow
// through Args, never through string interpolation into Expr.
//
//	q.SelectRaw("COUNT(*) AS n")
//	q.SelectRaw("CASE WHEN amount > ? THEN 'big' ELSE 'small' END AS bucket", 100)
//	q.SelectRaw("ROW_NUMBER() OVER (PARTITION BY tenant_id ORDER BY id) AS rn")
//
// Multiple SelectRaw calls accumulate; they are emitted after any
// columns configured by Select, in the order they were registered.
func (q *Query[T]) SelectRaw(expr string, args ...any) *Query[T] {
	q.rawColumns = append(q.rawColumns, drivers.RawColumn{
		Expr: expr,
		Args: append([]any(nil), args...),
	})
	return q
}

// Distinct adds DISTINCT to the query
func (q *Query[T]) Distinct() *Query[T] {
	q.distinct = true
	return q
}

// Limit sets the LIMIT
func (q *Query[T]) Limit(n int) *Query[T] {
	q.limit = &n
	return q
}

// Offset sets the OFFSET
func (q *Query[T]) Offset(n int) *Query[T] {
	q.offset = &n
	return q
}

// With eager loads relationships
func (q *Query[T]) With(relations ...string) *Query[T] {
	q.preloads = append(q.preloads, relations...)
	return q
}

// OnlyTrashed queries only soft deleted records
func (q *Query[T]) OnlyTrashed() *Query[T] {
	q.onlyTrashed = true
	q.withTrashed = true
	return q
}

// WithTrashed includes soft deleted records
func (q *Query[T]) WithTrashed() *Query[T] {
	q.withTrashed = true
	return q
}

// LockForUpdate adds FOR UPDATE clause for pessimistic locking
func (q *Query[T]) LockForUpdate() *Query[T] {
	q.lockForUpdate = true
	return q
}

// SkipLocked adds SKIP LOCKED clause to skip locked rows
func (q *Query[T]) SkipLocked() *Query[T] {
	q.skipLocked = true
	return q
}

// WithRowHooks opts the bulk write paths (Update / Delete /
// ForceDelete) into per-row [AfterCommitHook] / [AfterRollbackHook]
// fan-out. The chain runs an extra SELECT before the write to
// hydrate the affected rows, then registers a per-row callback for
// each model exactly as the row-by-row Save path does.
//
// Cost: one extra SELECT plus N model allocations per call. Reserve
// for cases that genuinely need the typed model in the hook (e.g.
// derived-field recompute, per-row validation). For ID-only signal
// (audit log, outbox enqueue, cache invalidation) implement
// [BulkAfterCommitHook] instead, which fires once per statement.
//
// Suppresses BulkAfterCommitHook for the call: if a model implements
// both hooks and WithRowHooks is requested, only per-row hooks fire.
//
// The flag propagates through Clone and through the soft-delete
// Delete -> Update delegation, so q.WithRowHooks().Delete(ctx)
// fans out per-row hooks for soft-deletable models.
func (q *Query[T]) WithRowHooks() *Query[T] {
	q.withRowHooks = true
	return q
}

// WithBulkLock adds FOR UPDATE to the pre-SELECT issued by bulk hook
// capture, so concurrent writers block on the captured rows until
// the surrounding transaction commits or rolls back. Only meaningful
// inside [Manager.Transaction]: outside a transaction the auto-commit
// ends the lock immediately, making the call a no-op for any practical
// purpose.
//
// The flag is a no-op on the atomic ReturningGrammar path (PostgreSQL
// today, plus any adapter that opts in via [drivers.ReturningGrammar])
// because RETURNING captures the affected primary keys atomically with
// the write itself; there is no pre-SELECT to lock.
//
// On SQLite the flag is also a no-op at the storage layer: the SQLite
// grammar accepts the LockForUpdate flag but never emits FOR UPDATE
// because SQLite does not implement row-level locking. The chain
// therefore costs nothing on SQLite but also buys nothing.
//
// Use when exact fidelity between captured ids and committed rows
// matters more than throughput. The trade-off is lock contention
// against concurrent writers that touch the same rows for the
// duration of the transaction.
//
// The flag propagates through [Query.Clone] and through the
// soft-delete Delete -> Update delegation, so
// q.WithBulkLock().Delete(ctx) locks the pre-SELECT for soft-deletable
// models on the pre-SELECT path.
func (q *Query[T]) WithBulkLock() *Query[T] {
	q.withBulkLock = true
	return q
}

// Save persists model through the query's bound driver. Takes ctx as
// the first argument so transaction enrollment is mandatory and
// explicit: passing a ctx returned by Manager.Transaction routes the
// write inside the caller's transaction; passing context.Background()
// uses the pool driver. There is no "forget the ctx and silently
// auto-commit" code path. Hooks (BeforeCreate/AfterCreate/...) and
// timestamp stamping fire identically to the package-level Save.
func (q *Query[T]) Save(ctx context.Context, model *T) error {
	if q.err != nil {
		return q.err
	}
	if err := q.bindTxFromContextValue(ctx); err != nil {
		return err
	}
	return saveWithDriver(ctx, q.driver, model)
}

// CreateMany inserts multiple records through the query's bound
// driver. Takes ctx as the first argument: a ctx returned by
// Manager.Transaction enrolls the entire batch in the caller's
// transaction; context.Background() routes through the pool.
//
// Iteration is sequential: the first error short-circuits and any
// preceding rows are part of the in-flight tx, so the caller's
// Transaction closure can return the error to roll back the partial
// batch.
func (q *Query[T]) CreateMany(ctx context.Context, records []T) error {
	if q.err != nil {
		return q.err
	}
	if err := q.bindTxFromContextValue(ctx); err != nil {
		return err
	}
	for i := range records {
		if err := saveWithDriver(ctx, q.driver, &records[i]); err != nil {
			return err
		}
	}
	return nil
}

// FirstOrCreate runs the find-or-insert pattern through the query's
// bound driver. Takes ctx as the first argument: a ctx returned by
// Manager.Transaction enrolls the lookup and the write in the caller's
// transaction. Mirrors Model[T].FirstOrCreate semantics; arguments and
// merging behavior are identical.
func (q *Query[T]) FirstOrCreate(ctx context.Context, conditions map[string]any, values map[string]any) (*T, error) {
	if q.err != nil {
		return nil, q.err
	}
	if err := q.bindTxFromContextValue(ctx); err != nil {
		return nil, err
	}
	return firstOrCreateWithDriver[T](ctx, q.driver, conditions, values)
}

// UpdateOrCreate runs the idempotency-then-write pattern through the
// query's bound driver. Takes ctx as the first argument: a ctx returned
// by Manager.Transaction enrolls the lookup, the update branch, and the
// insert branch in the caller's transaction.
func (q *Query[T]) UpdateOrCreate(ctx context.Context, conditions map[string]any, values map[string]any) (*T, error) {
	if q.err != nil {
		return nil, q.err
	}
	if err := q.bindTxFromContextValue(ctx); err != nil {
		return nil, err
	}
	return updateOrCreateWithDriver[T](ctx, q.driver, conditions, values)
}

// Create inserts a new record through the query's bound driver. Takes
// ctx as the first argument: a ctx returned by Manager.Transaction
// enrolls the write in the caller's transaction. Accepts a
// map[string]any for assignable assignment or a *T already populated by
// the caller.
func (q *Query[T]) Create(ctx context.Context, data any) (*T, error) {
	if q.err != nil {
		return nil, q.err
	}
	if err := q.bindTxFromContextValue(ctx); err != nil {
		return nil, err
	}
	switch v := data.(type) {
	case map[string]any:
		model := new(T)
		if err := mapToStruct(v, model); err != nil {
			return nil, err
		}
		if err := saveWithDriver(ctx, q.driver, model); err != nil {
			return nil, err
		}
		return model, nil
	case *T:
		// Mirror Model[T].Create: the assignment policy applies to
		// pre-built struct pointers so mass-assignment protection is
		// not bypassed by callers who construct the model manually.
		if err := applyAssignmentAccessToStruct(v); err != nil {
			return nil, err
		}
		if err := saveWithDriver(ctx, q.driver, v); err != nil {
			return nil, err
		}
		return v, nil
	default:
		return nil, errors.New("unsupported data type for create")
	}
}

// bindTxFromContextValue is the single binding chokepoint used by
// every read and write terminal on Query[T]. Each terminal funnels
// through this helper using its explicit ctx argument so:
//
//   - A ctx carrying a *sql.Tx (typically from Manager.Transaction)
//     enrolls the call in the caller's transaction.
//   - A plain ctx (e.g. context.Background()) routes the call through
//     the pool driver. If the chain was previously bound to a tx, the
//     wrapper is unwrapped so the explicit opt-out path is honored.
//
// Forgetting to pass ctx to a terminal is a compile error because ctx
// is a required positional argument; there is no silent
// auto-commit-outside-tx code path and no out-of-band chain ctx.
//
// Re-binding rules:
//   - q.driver already wraps the same tx: no-op.
//   - q.driver wraps a different tx: rebind against the original
//     pool driver instead of stacking another wrapper, so a nested
//     Transaction whose ctx carries a different tx switches cleanly.
//   - q.driver is the pool driver and ctx has a tx: wrap it.
//
// Concurrency: q.driver is mutated in place. *sql.Tx is single-threaded
// by stdlib contract, so callers who fan out a tx-bound query across
// goroutines were already broken; this helper does not change that.
//
// Liveness: after binding, the helper validates the driver via
// driverLive and returns ErrManagerShutdown / ErrNoConnection so no
// terminal can reach a nil (or post-Shutdown) driver dereference.
// Every caller must check the returned error before executing.
func (q *Query[T]) bindTxFromContextValue(ctx context.Context) error {
	if q == nil {
		return ErrNoConnection
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tx, ok := TxFromContext(ctx)
	if !ok {
		// No tx in ctx: unwrap any prior tx binding so the call hits
		// the pool driver. This implements the explicit opt-out
		// pattern: pass a non-tx ctx (the original ctx captured before
		// Manager.Transaction) and the call escapes the auto-enrolled
		// tx.
		if outer, isTx := q.driver.(*txDriver); isTx {
			q.driver = outer.Driver
		}
		return q.driverLive()
	}
	base := q.driver
	if outer, isTx := base.(*txDriver); isTx {
		if outer.tx == tx {
			return q.driverLive()
		}
		base = outer.Driver
	}
	if base == nil {
		if m := Default(); m != nil {
			base = m.DefaultDriver()
		}
	}
	if base != nil {
		q.driver = &txDriver{Driver: base, tx: tx}
	}
	return q.driverLive()
}

// driverLive reports whether the query can execute against its driver.
// The manager closed check runs first (and wins): after Shutdown the
// driver may be nil OR a stale non-nil pointer to a closed pool, and
// "shut down" is the actionable diagnosis either way. One atomic load
// on the healthy path; no locks.
func (q *Query[T]) driverLive() error {
	if q.mgr != nil && q.mgr.closed.Load() {
		return ErrManagerShutdown
	}
	if q.driver == nil {
		return ErrNoConnection
	}
	return nil
}

// Execution methods

// applySoftDeleteScope is a thin wrapper that delegates to
// applyGlobalScopes. The soft-delete predicate is now itself a
// registered global scope (auto-installed by newQuery for soft-delete
// models); this wrapper is preserved as a single entry point so the
// existing terminal call sites stay unchanged.
//
// Idempotency: applyGlobalScopes guards against double-apply, so an
// outer terminal that delegates to an inner terminal (Exists -> Count,
// First -> Get) is safe to call this method.
//
// Tx auto-enrollment: every read and write terminal calls
// bindTxFromContextValue with its explicit ctx argument before this
// helper, so the driver in scope already reflects ctx by the time
// global scopes apply. ctx is forwarded to each scope so consumer-set
// scopes can read request-scoped values from it.
func (q *Query[T]) applySoftDeleteScope(ctx context.Context) {
	q.applyGlobalScopes(ctx)
}

// Get retrieves all matching records. Takes ctx as the first argument
// so reads participate in the caller's transaction when ctx carries a
// *sql.Tx, mirroring the explicit-ctx contract of every write
// terminal. context.Background() routes through the pool driver.
func (q *Query[T]) Get(ctx context.Context) ([]T, error) {
	if q.err != nil {
		return nil, q.err
	}
	if err := q.bindTxFromContextValue(ctx); err != nil {
		return nil, err
	}
	q.applySoftDeleteScope(ctx)
	// A scope predicate that fails validation (invalid identifier,
	// unknown operator, driver-registered operator with bad value) sets
	// q.err during apply. Surface it before issuing the SELECT so a
	// broken scope cannot silently drop its predicate from the query.
	if q.err != nil {
		return nil, q.err
	}

	// Build SELECT query
	selectQuery := &drivers.SelectQuery{
		Table:         q.table,
		Columns:       q.columns,
		RawColumns:    q.rawColumns,
		Conditions:    q.conditions,
		Orders:        q.orders,
		Groups:        q.groups,
		Having:        q.having,
		Joins:         q.joins,
		Limit:         q.limit,
		Offset:        q.offset,
		Distinct:      q.distinct,
		LockForUpdate: q.lockForUpdate,
		SkipLocked:    q.skipLocked,
	}

	sql, args := q.driver.Grammar().CompileSelect(selectQuery)
	q.lastSQL = sql
	q.lastArgs = args

	rows, err := q.driver.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []T
	for rows.Next() {
		var model T
		if err := scanIntoStruct(rows, &model); err != nil {
			return nil, err
		}
		results = append(results, model)
	}
	// A driver error mid-iteration surfaces via rows.Err(), not via
	// rows.Next()/Scan. Without this check a truncated result set would
	// be returned as success.
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Side-channel existence is keyed by pointer. Mark each slice
	// element AFTER all appends so the slice's backing array is final
	// (a mid-loop append could grow the slice, invalidating
	// element-address marks taken before the grow).
	for i := range results {
		markExisting(&results[i])
	}

	// Handle eager loading
	if len(q.preloads) > 0 {
		if err := q.loadRelations(ctx, &results); err != nil {
			return nil, err
		}
	}

	return results, nil
}

// First retrieves the first matching record. Takes ctx as the first
// argument so reads participate in the caller's transaction when ctx
// carries a *sql.Tx.
func (q *Query[T]) First(ctx context.Context, dest *T) error {
	q.Limit(1)
	results, err := q.Get(ctx)
	if err != nil {
		return err
	}
	if len(results) == 0 {
		return ErrRecordNotFound
	}
	*dest = results[0]
	// Side-channel existence is keyed by pointer; the value-copy above
	// loses the per-instance mark from results[0]. Re-mark dest so the
	// caller's pointer participates in the existence store.
	markExisting(dest)
	return nil
}

// Find retrieves a record by primary key. Takes ctx as the first
// argument so reads participate in the caller's transaction when ctx
// carries a *sql.Tx.
func (q *Query[T]) Find(ctx context.Context, id any, dest *T) error {
	return q.Where("id = ?", id).First(ctx, dest)
}

// Count returns the number of matching records. Takes ctx as the first
// argument.
func (q *Query[T]) Count(ctx context.Context) (int, error) {
	if q.err != nil {
		return 0, q.err
	}
	if err := q.bindTxFromContextValue(ctx); err != nil {
		return 0, err
	}
	q.applySoftDeleteScope(ctx)
	// A scope predicate that fails validation sets q.err during apply.
	// Surface it before issuing SQL so a broken scope cannot silently
	// drop its predicate.
	if q.err != nil {
		return 0, q.err
	}
	var sql string
	var args []any
	if len(q.groups) > 0 || q.distinct {
		// GROUP BY collapses rows after aggregation, so a flat COUNT(*)
		// returns one count per group instead of the number of groups;
		// DISTINCT applied to a COUNT(*) projection is a no-op. Both
		// need the row set materialized first: compile the full inner
		// select and count its rows through a derived table. The wrapper
		// text is constant; everything user-supplied is validated and
		// compiled by the grammar inside the inner select.
		inner := &drivers.SelectQuery{
			Table:      q.table,
			Columns:    q.columns,
			RawColumns: q.rawColumns,
			Conditions: q.conditions,
			Joins:      q.joins,
			Groups:     q.groups,
			Having:     q.having,
			Distinct:   q.distinct,
		}
		// SELECT * with GROUP BY is rejected by PostgreSQL (every
		// projected column must appear in GROUP BY); project the
		// grouping columns instead, which every dialect accepts and
		// which preserves the row count exactly.
		wildcard := len(q.columns) == 0 || (len(q.columns) == 1 && q.columns[0] == "*")
		if len(q.groups) > 0 && len(q.rawColumns) == 0 && wildcard {
			inner.Columns = q.groups
		}
		innerSQL, innerArgs := q.driver.Grammar().CompileSelect(inner)
		sql = "SELECT COUNT(*) AS count FROM (" + innerSQL + ") AS count_sub"
		args = innerArgs
	} else {
		// COUNT(*) is a framework-generated projection: emit it through
		// the trusted RawColumns path so the user-facing Columns whitelist
		// stays strict for untrusted input.
		selectQuery := &drivers.SelectQuery{
			Table:      q.table,
			RawColumns: []drivers.RawColumn{{Expr: "COUNT(*) AS count"}},
			Conditions: q.conditions,
			Joins:      q.joins,
		}
		sql, args = q.driver.Grammar().CompileSelect(selectQuery)
	}

	var count int64
	err := q.driver.QueryRowContext(ctx, sql, args...).Scan(&count)

	return int(count), err
}

// Exists checks if any records match. Takes ctx as the first argument.
// A failed query returns (false, err) rather than silently reporting
// absence.
func (q *Query[T]) Exists(ctx context.Context) (bool, error) {
	count, err := q.Count(ctx)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// Pluck retrieves values of a single column. Takes ctx as the first
// argument.
func (q *Query[T]) Pluck(ctx context.Context, column string) ([]any, error) {
	if err := validateIdentifier(column); err != nil {
		q.setErr("Pluck", err)
	}
	if q.err != nil {
		return nil, q.err
	}
	if err := q.bindTxFromContextValue(ctx); err != nil {
		return nil, err
	}
	q.applySoftDeleteScope(ctx)
	// A scope predicate that fails validation sets q.err during apply.
	// Surface it before issuing SQL so a broken scope cannot silently
	// drop its predicate.
	if q.err != nil {
		return nil, q.err
	}
	q.Select(column)

	selectQuery := &drivers.SelectQuery{
		Table:      q.table,
		Columns:    q.columns,
		RawColumns: q.rawColumns,
		Conditions: q.conditions,
		Orders:     q.orders,
		Joins:      q.joins,
		Limit:      q.limit,
		Offset:     q.offset,
		Distinct:   q.distinct,
	}

	sql, args := q.driver.Grammar().CompileSelect(selectQuery)
	q.lastSQL = sql
	q.lastArgs = args

	rows, err := q.driver.QueryContext(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []any
	for rows.Next() {
		var value any
		if err := rows.Scan(&value); err != nil {
			return nil, err
		}
		results = append(results, rebaseAnyTimeUTC(value))
	}
	// A driver error mid-iteration surfaces via rows.Err(), not via
	// rows.Next()/Scan.
	if err := rows.Err(); err != nil {
		return nil, err
	}

	return results, nil
}

// deniedUpdateKeys returns the map keys that resolve to an application
// (non-embedded) column of meta. Matching is case-insensitive against
// both the SQL column name and the snake-cased Go field name: an
// exact-match check alone would let "IS_ADMIN" or an aliased column name
// slip past the implicit-deny gate (into compiled SQL on the bulk Update
// path, or past mapToStruct's preflight on the Create(map) path). Keys
// are returned in column order so the resulting *MassAssignmentError is
// stable.
func deniedUpdateKeys(updates map[string]any, meta *ModelMeta) []string {
	if meta == nil {
		return nil
	}
	var denied []string
	for _, col := range meta.Columns() {
		if col.FromEmbedded {
			continue
		}
		for key := range updates {
			lower := strings.ToLower(key)
			if lower == strings.ToLower(col.Column) || lower == strings.ToLower(col.FieldNameKey) {
				denied = append(denied, key)
			}
		}
	}
	return denied
}

// Update updates matching records. Takes ctx as the first argument so
// transaction enrollment is mandatory and explicit: passing a ctx
// returned by Manager.Transaction enrolls the UPDATE in the caller's
// transaction; context.Background() routes through the pool.
//
// The input map is never mutated: Update copies it internally before
// injecting the updated_at timestamp. Values of type [RawSQL] (including
// the package-level [NOW] sentinel) are emitted verbatim into the
// generated statement; all other values, including plain string values
// that happen to look like SQL, are bound as parameters.
//
// Hook semantics: this is a bulk path. Per-row [AfterCommitHook] and
// [AfterRollbackHook] do NOT fire. Two opt-ins:
//   - Implement [BulkAfterCommitHook] for one event per bulk statement
//     with the affected primary-key set.
//   - Chain [Query.WithRowHooks] to fan out per-row hooks at the cost of
//     an extra SELECT and N model allocations.
func (q *Query[T]) Update(ctx context.Context, updates map[string]any) (int64, error) {
	// Mass-assignment policy: a map passed to Update is attacker-shaped
	// input exactly like a map passed to Create, so a model with no
	// declared Assignable/Protected policy (and no AllowAllColumns opt-in)
	// rejects any key that resolves to an application column with
	// *MassAssignmentError before SQL compilation. Matching is
	// case-insensitive and covers both the SQL column name and the
	// snake-cased field name, so neither a casing variant nor a column
	// alias slips into the compiled SQL. Framework-managed embedded
	// columns bypass policy by design. Models with a declared policy or
	// AllowAllColumns keep their established Update semantics unchanged.
	//
	// The check lives here on the public entry point, not in bulkUpdate:
	// internal struct-based writes (Save's update branch, which builds
	// its map via structToMap from a caller-constructed *T) and the
	// soft-delete branch of Delete call bulkUpdate directly and are not
	// policed, mirroring the Create(*T) scoping.
	var zero T
	if PolicyFor(&zero).implicitDeny {
		if denied := deniedUpdateKeys(updates, MetaForValue(reflect.ValueOf(zero))); len(denied) > 0 {
			return 0, &MassAssignmentError{Model: reflect.TypeOf(zero).String(), Keys: denied}
		}
	}
	return q.bulkUpdate(ctx, updates, BulkOpUpdate)
}

// bulkUpdate is the shared implementation for the public Update entry
// point and the soft-delete branch of Delete. op identifies the caller
// so BulkAfterCommitHook listeners receive the correct BulkOp value
// (BulkOpUpdate from Update; BulkOpDelete from Delete on a soft-delete
// model).
func (q *Query[T]) bulkUpdate(ctx context.Context, updates map[string]any, op BulkOp) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	if len(updates) == 0 {
		return 0, errors.New("no updates provided")
	}
	if err := q.bindTxFromContextValue(ctx); err != nil {
		return 0, err
	}
	// Soft-delete scope: an Update on a soft-deletable model must not touch
	// already-trashed rows unless the caller explicitly opted in via
	// WithTrashed / OnlyTrashed. Delete delegates here for the soft-delete
	// path, so the same predicate also prevents double-stamping deleted_at
	// on already-trashed rows.
	q.applyGlobalScopes(ctx)
	// A scope predicate that fails validation (invalid identifier,
	// unknown operator, driver-registered operator with bad value) sets
	// q.err during apply. Surface it before issuing the UPDATE so a
	// broken tenant scope cannot silently mutate rows outside its
	// intended set.
	if q.err != nil {
		return 0, q.err
	}

	// Copy the caller's map before mutation. Update must not have
	// visible side effects on the passed-in map (thread safety, idempotent
	// re-dispatch, and least-surprise for callers).
	copyOfUpdates := make(map[string]any, len(updates)+1)
	for k, v := range updates {
		copyOfUpdates[k] = v
	}

	// Drop read_only columns (e.g. a SelectDistance score) so a map-based
	// Update cannot target a column the model marks read-only. This mirrors
	// structToMap's write-path skip, keeping the "never emitted on write"
	// contract true for the map API as well.
	var zero T
	stripReadOnlyKeys(copyOfUpdates, MetaForValue(reflect.ValueOf(zero)))
	if len(copyOfUpdates) == 0 {
		return 0, errors.New("no updatable columns provided (all keys are read-only)")
	}

	// Stamp updated_at app-side in UTC, the same clock the struct Save
	// path uses, so every ORM-managed lifecycle column carries one
	// invariant: app clock, UTC wall clock, independent of the writer's
	// process timezone and the database session timezone.
	//
	// Skip the stamp when the model has no UpdatedAt column
	// (ImmutableModel/ImmutableUUIDModel) so the generated UPDATE does
	// not target a non-existent column.
	if q.hasUpdatedAt {
		copyOfUpdates["updated_at"] = time.Now().UTC()
	}

	// Resolve the bulk hook plan. On Postgres + Tier B the plan asks
	// us to capture ids atomically via RETURNING (no pre-SELECT, no
	// race window); otherwise the plan has either pre-captured the
	// ids/rows or has no hook work at all.
	plan, err := q.bulkPrepareHooks(ctx, op)
	if err != nil {
		return 0, err
	}

	grammar := q.driver.Grammar()

	var (
		sqlStr  string
		args    []any
		retIDs  []any
		retRows *sql.Rows
	)

	if plan.useReturning() {
		// Postgres atomic capture path. CompileUpdateReturning is only
		// reachable when the grammar already type-asserts to
		// drivers.ReturningGrammar (verified inside bulkPrepareHooks).
		rg := grammar.(drivers.ReturningGrammar)
		sqlStr, args = rg.CompileUpdateReturning(q.table, copyOfUpdates, q.conditions, plan.ReturningPK)
	} else {
		sqlStr, args = grammar.CompileUpdate(q.table, copyOfUpdates, q.conditions)
	}
	q.lastSQL = sqlStr
	q.lastArgs = args

	var (
		rowsAffected int64
		execErr      error
	)
	if plan.useReturning() {
		retRows, execErr = q.driver.QueryContext(ctx, sqlStr, args...)
		if execErr == nil {
			retIDs, execErr = scanReturnedIDs(retRows)
			rowsAffected = int64(len(retIDs))
		}
	} else {
		var result sql.Result
		result, execErr = q.driver.ExecContext(ctx, sqlStr, args...)
		if execErr == nil {
			rowsAffected, _ = result.RowsAffected()
		}
	}
	if execErr != nil {
		return 0, execErr
	}

	plan.invoke(retIDs)

	return rowsAffected, nil
}

// compileInsertSQL builds a single-row INSERT through the driver
// grammar: the table and column identifiers are grammar-quoted and the
// columns are emitted in sorted order so the generated SQL is
// deterministic regardless of map iteration order. Typed [RawSQL] values
// (e.g. orm.NOW) emit as UTC-pinned SQL expressions instead of binding,
// matching the Update-map behavior; placeholder numbering counts bound
// values only so Postgres $N stays consecutive.
func (q *Query[T]) compileInsertSQL(data map[string]any) (string, []any, error) {
	columns := make([]string, 0, len(data))
	for col := range data {
		if err := validateIdentifier(col); err != nil {
			return "", nil, err
		}
		columns = append(columns, col)
	}
	sort.Strings(columns)

	grammar := q.driver.Grammar()
	driverName := q.driver.DriverName()
	quoted := make([]string, len(columns))
	placeholders := make([]string, len(columns))
	values := make([]any, 0, len(columns))
	argIndex := 1
	for i, col := range columns {
		quoted[i] = grammar.QuoteIdentifier(col)
		if raw, ok := data[col].(RawSQL); ok {
			placeholders[i] = drivers.RawSQLExprFor(driverName, raw)
			continue
		}
		placeholders[i] = grammar.Placeholder(argIndex)
		values = append(values, data[col])
		argIndex++
	}

	sqlStr := fmt.Sprintf(
		"INSERT INTO %s (%s) VALUES (%s)",
		grammar.QuoteIdentifier(q.table),
		strings.Join(quoted, ", "),
		strings.Join(placeholders, ", "),
	)
	return sqlStr, values, nil
}

// InsertGetId inserts a record and returns the ID. Takes ctx as the
// first argument so transaction enrollment is mandatory and explicit;
// see Query.Save for the rationale.
func (q *Query[T]) InsertGetId(ctx context.Context, data map[string]any) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	if err := q.bindTxFromContextValue(ctx); err != nil {
		return 0, err
	}
	if len(data) == 0 {
		return 0, errors.New("no data provided for insert")
	}

	sqlStr, values, err := q.compileInsertSQL(data)
	if err != nil {
		return 0, fmt.Errorf("velocity/orm: insertGetId: %w", err)
	}

	driverName := q.driver.DriverName()

	// Check driver type to determine how to get last insert ID
	if driverName == "sqlite" || driverName == "mysql" {
		// SQLite/MySQL: Use standard INSERT and get last insert ID from result
		result, err := q.driver.ExecContext(ctx, sqlStr, values...)
		if err != nil {
			return 0, err
		}

		lastID, _ := result.LastInsertId()
		return lastID, nil
	}

	// PostgreSQL: append a grammar-quoted RETURNING id clause and scan it
	sqlStr += " RETURNING " + q.driver.Grammar().QuoteIdentifier("id")

	var lastID int64
	if err := q.driver.QueryRowContext(ctx, sqlStr, values...).Scan(&lastID); err != nil {
		return 0, err
	}

	return lastID, nil
}

// insertExec runs a grammar-compiled INSERT via ExecContext with no
// RETURNING clause or last-insert-id handling. Used by save paths whose
// primary key is set by the caller (UUID models), where scanning a
// database-generated integer id would be wrong.
func (q *Query[T]) insertExec(ctx context.Context, data map[string]any) error {
	if q.err != nil {
		return q.err
	}
	if err := q.bindTxFromContextValue(ctx); err != nil {
		return err
	}
	if len(data) == 0 {
		return errors.New("no data provided for insert")
	}

	sqlStr, values, err := q.compileInsertSQL(data)
	if err != nil {
		return fmt.Errorf("velocity/orm: insert: %w", err)
	}

	if _, err := q.driver.ExecContext(ctx, sqlStr, values...); err != nil {
		return err
	}
	return nil
}

// Delete soft deletes matching records (if model supports soft
// deletes) or hard deletes. Takes ctx as the first argument so
// transaction enrollment is mandatory and explicit.
//
// Hook semantics: this is a bulk path. Per-row [AfterCommitHook] and
// [AfterRollbackHook] do NOT fire. Use [BulkAfterCommitHook] (one event
// with affected IDs and op=BulkOpDelete) or chain [Query.WithRowHooks]
// to fan out per-row hooks.
func (q *Query[T]) Delete(ctx context.Context) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	// Check if model has soft deletes
	if q.hasSoftDelete {
		// Soft delete stamps deleted_at app-side in UTC, matching every
		// other ORM-managed lifecycle column (one clock: app, UTC). Route
		// through bulkUpdate (not Update) so BulkAfterCommitHook listeners
		// receive op=BulkOpDelete instead of op=BulkOpUpdate.
		if err := q.bindTxFromContextValue(ctx); err != nil {
			return 0, err
		}
		return q.bulkUpdate(ctx, map[string]any{
			"deleted_at": time.Now().UTC(),
		}, BulkOpDelete)
	}

	// Hard delete for models without soft delete support
	return q.ForceDelete(ctx)
}

// ForceDelete permanently deletes matching records. Takes ctx as the
// first argument so transaction enrollment is mandatory and explicit.
//
// Hook semantics: this is a bulk path. Per-row [AfterCommitHook] and
// [AfterRollbackHook] do NOT fire. Use [BulkAfterCommitHook] (one event
// with affected IDs and op=BulkOpForceDelete) or chain
// [Query.WithRowHooks] to fan out per-row hooks.
//
// Scope semantics: ForceDelete honours every registered global scope on T
// EXCEPT the auto-installed soft-delete predicate. Filtering by
// deleted_at IS NULL would defeat the entire purpose of ForceDelete on a
// SoftDeleteModel (the caller is explicitly trying to drop trashed rows
// alongside live ones). All other scopes (tenant, archive, locale,
// state) still apply, so a multi-tenant ForceDelete cannot leak across
// tenant boundaries. Opt out of additional scopes with
// [Query.WithoutGlobalScope] / [Query.WithoutGlobalScopes] if needed.
func (q *Query[T]) ForceDelete(ctx context.Context) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	if err := q.bindTxFromContextValue(ctx); err != nil {
		return 0, err
	}
	// Apply every registered global scope EXCEPT soft-delete so user
	// scopes (tenant, archive, locale, ...) still constrain the rows we
	// drop. The soft-delete scope must be skipped because ForceDelete
	// is the documented way to bypass it.
	q.WithoutGlobalScope(softDeleteScopeName)
	q.applyGlobalScopes(ctx)
	// A scope predicate that fails validation (invalid identifier,
	// unknown operator, driver-registered operator with bad value) sets
	// q.err during apply. Surface it before issuing the DELETE so a
	// broken tenant scope cannot silently drop rows outside its
	// intended set.
	if q.err != nil {
		return 0, q.err
	}

	plan, err := q.bulkPrepareHooks(ctx, BulkOpForceDelete)
	if err != nil {
		return 0, err
	}

	grammar := q.driver.Grammar()

	var (
		sqlStr  string
		args    []any
		retIDs  []any
		retRows *sql.Rows
	)

	if plan.useReturning() {
		rg := grammar.(drivers.ReturningGrammar)
		sqlStr, args = rg.CompileDeleteReturning(q.table, q.conditions, plan.ReturningPK)
	} else {
		sqlStr, args = grammar.CompileDelete(q.table, q.conditions)
	}

	var (
		rowsAffected int64
		execErr      error
	)
	if plan.useReturning() {
		retRows, execErr = q.driver.QueryContext(ctx, sqlStr, args...)
		if execErr == nil {
			retIDs, execErr = scanReturnedIDs(retRows)
			rowsAffected = int64(len(retIDs))
		}
	} else {
		var result sql.Result
		result, execErr = q.driver.ExecContext(ctx, sqlStr, args...)
		if execErr == nil {
			rowsAffected, _ = result.RowsAffected()
		}
	}
	if execErr != nil {
		return 0, execErr
	}

	plan.invoke(retIDs)

	return rowsAffected, nil
}

// Chunk processes results in chunks. Takes ctx as the first argument.
func (q *Query[T]) Chunk(ctx context.Context, size int, callback func([]T) error) error {
	page := 0
	for {
		q.Limit(size).Offset(page * size)
		results, err := q.Get(ctx)
		if err != nil {
			return err
		}

		if len(results) == 0 {
			break
		}

		if err := callback(results); err != nil {
			return err
		}

		if len(results) < size {
			break
		}

		page++
	}
	return nil
}

// ToSQL returns the SQL and arguments that would be executed
func (q *Query[T]) ToSQL() (string, []any) {
	return q.lastSQL, q.lastArgs
}

// Helper methods

// loadRelations is defined in relation.go

// Helper functions

func getTableName[T any]() string {
	if name := deriveTableName(modelTypeFor[T]()); name != "" {
		return name
	}
	// Anonymous/unnamed generic instantiation: no type name to pluralize.
	return "records"
}

// scanIntoStruct hydrates dest from the next row of rows. Resolves columns
// through the canonical ModelMeta so the read path is symmetric with
// structToMap (write) and applyAssignmentAccessToStruct (policy). Before the
// resolver landed, this path independently parsed tags AND re-applied
// toSnakeCase to the resolved column name, which mangled `column:LegacyXYZ`
// into legacy_x_y_z and silently broke read-back of column-tagged fields
// (the corresponding write path honored the tag verbatim).
//
// Polymorphic morph fields are not registered as columns in ModelMeta
// because they span a (type, id) pair on a single Morph value. They are
// resolved in a small pre-pass here so a SELECT * can populate the pair.
//
// Columns the model doesn't declare are scanned into a throwaway slot so
// the driver doesn't error on extra columns from joins or wildcards.
func scanIntoStruct(rows *sql.Rows, dest any) error {
	columns, err := rows.Columns()
	if err != nil {
		return err
	}

	values := make([]any, len(columns))
	valuePtrs := make([]any, len(columns))

	destValue := reflect.ValueOf(dest).Elem()
	if destValue.Kind() != reflect.Struct {
		// Non-struct destination: send everything to discard slots so
		// the driver doesn't panic. Callers that need scalar scans use
		// RawQuery.Scan, not scanIntoStruct, but the guard keeps the
		// helper honest.
		for i := range columns {
			valuePtrs[i] = &values[i]
		}
		return rows.Scan(valuePtrs...)
	}

	meta := MetaForValue(destValue)

	// Polymorphic morph fields contribute two columns (type, id) sourced
	// from a single Morph struct field. ModelMeta excludes them, so
	// build a small (column -> path) override map from the top-level
	// struct fields. Morph values are conventionally declared at the
	// outer model level, not promoted through embedded bases.
	morphPaths := map[string][]int{}
	destType := destValue.Type()
	for i := 0; i < destType.NumField(); i++ {
		field := destType.Field(i)
		tag := field.Tag.Get("orm")
		pv := extractPolymorphicValue(tag)
		if pv == "" {
			continue
		}
		typeCol, idCol, perr := parsePolymorphicTag(pv)
		if perr != nil {
			continue
		}
		morphType := field.Type
		if morphType.Kind() != reflect.Struct {
			continue
		}
		if tnf, ok := morphType.FieldByName("TypeName"); ok {
			morphPaths[typeCol] = append(append([]int{}, i), tnf.Index...)
		}
		if idf, ok := morphType.FieldByName("ID"); ok {
			morphPaths[idCol] = append(append([]int{}, i), idf.Index...)
		}
	}

	// Time-typed destinations are remembered so they can be rebased to
	// UTC after Scan. Drivers surface naive timestamps in inconsistent
	// locations (lib/pq uses FixedZone("", 0), modernc sqlite preserves
	// whatever offset was stored), so round-trips are only stable across
	// hosts if scanned times uniformly carry time.UTC.
	var timeFields []reflect.Value

	for i, column := range columns {
		var path []int
		if mp, ok := morphPaths[column]; ok {
			path = mp
		} else if meta != nil {
			if col, ok := meta.ColumnByName(column); ok {
				path = col.IndexPath
			}
		}
		if path == nil {
			valuePtrs[i] = &values[i]
			continue
		}
		field := destValue.FieldByIndex(path)
		if field.CanSet() {
			valuePtrs[i] = field.Addr().Interface()
			if ft := field.Type(); ft == timeType || (ft.Kind() == reflect.Ptr && ft.Elem() == timeType) {
				timeFields = append(timeFields, field)
			}
		} else {
			valuePtrs[i] = &values[i]
		}
	}

	if err := rows.Scan(valuePtrs...); err != nil {
		return err
	}
	rebaseScannedTimesUTC(timeFields)
	return nil
}

// timeType is the reflect.Type of time.Time, used to recognize time-typed
// scan destinations.
var timeType = reflect.TypeOf(time.Time{})

// rebaseScannedTimesUTC sets every scanned time field to the same instant
// in time.UTC (read side of the storage contract: instants are stored UTC;
// zones are presentation). Fields are freshly hydrated by rows.Scan, so
// mutating them in place is safe.
func rebaseScannedTimesUTC(fields []reflect.Value) {
	for _, f := range fields {
		if f.Kind() == reflect.Ptr {
			if f.IsNil() {
				continue
			}
			f = f.Elem()
		}
		t := f.Interface().(time.Time)
		if t.Location() != time.UTC {
			f.Set(reflect.ValueOf(t.In(time.UTC)))
		}
	}
}

// ToSnakeCase converts a CamelCase / acronym-cased identifier to snake_case.
//
// BREAKING: as of this version, consecutive uppercase letters are split at
// the acronym->word boundary, and digit->upper transitions also insert an
// underscore. Previously these collapsed:
//
//	SSHKey       -> ssh_key       (was sshkey)
//	URLPath      -> url_path      (was urlpath)
//	OAuthID      -> o_auth_id     (was oauthid)
//	Field1Name   -> field1_name   (was field1name)
//
// Apps with acronym-named or digit-bearing model types that relied on the
// previous mapping for auto-derived table/column names must either override
// TableName() on the model to pin the legacy name, or run a migration to
// rename the table/column to the new convention.
//
// Exported so other packages (e.g. console scaffolders) can share a single
// canonical implementation, keeping migration table names aligned with the
// runtime ORM's column/table inference.
//
// Handles acronym->word boundaries so consecutive capitals split correctly:
//   - ProviderID      -> provider_id
//   - SSHKeyID        -> ssh_key_id
//   - URLPath         -> url_path
//   - HTTPSConnection -> https_connection
//
// Also splits at digit->upper boundaries so embedded numbers don't fuse the
// next word onto the digit run:
//   - Field1Name      -> field1_name
//   - OAuth2Token     -> o_auth2_token
//   - Zone1AConfig    -> zone1_a_config
//
// An underscore is inserted before an uppercase letter when ANY of:
//  1. the previous char is lowercase (camelCase boundary), OR
//  2. the previous char is uppercase AND the next char is lowercase
//     (acronym->word boundary, e.g. the "K" in "SSHKey"), OR
//  3. the previous char is a digit (digit->word boundary).
//
// Non-ASCII runes are lowercased via strings.ToLower and emitted as their
// full UTF-8 byte sequence (no truncation to a single byte).
func ToSnakeCase(str string) string {
	var result []byte
	runes := []rune(str)
	for i, r := range runes {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := runes[i-1]
			prevLower := prev >= 'a' && prev <= 'z'
			prevUpper := prev >= 'A' && prev <= 'Z'
			prevDigit := prev >= '0' && prev <= '9'
			nextLower := i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z'
			if prevLower || (prevUpper && nextLower) || prevDigit {
				result = append(result, '_')
			}
		}
		// Append the full lowercased UTF-8 sequence rather than just the
		// first byte. A multi-byte rune's lowercase form may itself be
		// multi-byte (e.g. some Unicode case folds), and truncating to
		// byte 0 corrupts the output.
		result = append(result, []byte(strings.ToLower(string(r)))...)
	}
	return string(result)
}

// RawQuery represents a raw SQL query that can be executed with First() or Get()
//
// RawQuery SOFT-DELETE BEHAVIOR:
//
// RawQuery does NOT apply the deleted_at IS NULL scope that the fluent
// Query builder adds automatically for soft-delete models. The supplied SQL
// is sent to the driver verbatim. If you pass a plain "SELECT * FROM users"
// against a SoftDeleteModel[User] table, the result will include trashed
// rows. Callers who expect scope-aware behavior should either:
//
//  1. Add "WHERE deleted_at IS NULL" to the query explicitly, or
//  2. Use the fluent Query[T] builder via Model[T]{}.Where(...) instead, or
//  3. Use NewRawQuerySoftDeleteOnly which rewrites the SQL to enforce the
//     deleted_at IS NULL predicate.
//
// This bypass is intentional, raw SQL is an escape hatch. Surfacing the
// scope silently would make it impossible to query trashed records via raw
// SQL, which is a legitimate use case (e.g. admin dashboards).
type RawQuery[T any] struct {
	driver drivers.Driver
	// mgr mirrors Query.mgr: the Manager the driver was resolved from,
	// consulted by execution liveness checks. See Query.mgr.
	mgr  *Manager
	sql  string
	args []any
}

// NewRawQuery creates a new raw query builder.
//
// WARNING: This method executes raw SQL directly. The caller is responsible for
// preventing SQL injection by using parameterized queries with placeholder arguments.
// Never concatenate user input directly into the sql string.
//
// WARNING: Soft-delete scopes are NOT applied. See the RawQuery type
// documentation for details. Use NewRawQuerySoftDeleteOnly to opt in.
//
// Tx enrollment: every terminal (First, Get, Scan, Exec) takes ctx as its
// first positional argument and funnels through bindTxFromContextValue,
// so a ctx carrying a *sql.Tx (e.g. from Manager.Transaction) routes the
// raw statement through that tx automatically. Pass a non-tx ctx
// (typically the original ctx captured before Manager.Transaction) to
// the terminal to opt out and execute against the pool driver instead.
func NewRawQuery[T any](sql string, args ...any) *RawQuery[T] {
	var drv drivers.Driver
	m := Default()
	if m != nil {
		drv = m.DefaultDriver()
	}
	return &RawQuery[T]{
		driver: drv,
		mgr:    m,
		sql:    sql,
		args:   args,
	}
}

// NewRawQuerySoftDeleteOnly creates a raw query that enforces the
// deleted_at IS NULL predicate the fluent Query builder applies for
// soft-delete models. The SQL is wrapped in an outer SELECT so we can
// append the scope without attempting to parse or rewrite the caller's
// query. For models that do not support soft deletes, this is a no-op
// and the underlying SQL is executed as-is.
//
// Only the soft-delete scope is enforced. User-registered global scopes
// via orm.AddGlobalScope are NOT applied. Raw SQL cannot have arbitrary
// predicates appended generically. If your model has any other global
// scope (multi-tenant, region, archive, etc.), prefer the fluent
// Query[T] builder so all scopes run, or extend the SQL by hand to
// include every required predicate. Calling this against a model with
// non-soft-delete scopes registered is a cross-tenant leak waiting to
// happen, hence the explicit name.
func NewRawQuerySoftDeleteOnly[T any](sql string, args ...any) *RawQuery[T] {
	if modelHasSoftDelete[T]() {
		// Wrap the user-supplied query in an outer SELECT. Using a
		// subquery avoids any attempt to splice WHERE clauses into the
		// original statement, which would be brittle and unsafe.
		sql = "SELECT * FROM (" + sql + ") AS __orm_scoped WHERE deleted_at IS NULL"
	}
	return NewRawQuery[T](sql, args...)
}

// bindTxFromContextValue mirrors Query[T].bindTxFromContextValue for the
// raw-SQL escape hatch. Each RawQuery terminal funnels through this
// helper using its explicit ctx argument so:
//
//   - A ctx carrying a *sql.Tx (typically from Manager.Transaction)
//     enrolls the raw statement in the caller's transaction.
//   - A plain ctx (e.g. context.Background() or the caller's pre-tx ctx)
//     routes the call through the pool driver. If a prior terminal had
//     bound the chain to a tx, the wrapper is unwrapped so the explicit
//     opt-out path is honored.
//
// Re-binding rules match Query[T]: same tx is a no-op, a different tx
// rebinds against the original pool driver instead of stacking another
// wrapper, and a pool driver with a tx in ctx gets wrapped.
//
// Concurrency: r.driver is mutated in place. *sql.Tx is single-threaded
// by stdlib contract, so callers who fan out a tx-bound raw query across
// goroutines were already broken; this helper does not change that.
//
// Liveness: mirrors Query.bindTxFromContextValue. After binding, the
// driver is validated and ErrManagerShutdown / ErrNoConnection is
// returned so terminals never dereference a nil or post-Shutdown driver.
func (r *RawQuery[T]) bindTxFromContextValue(ctx context.Context) error {
	if r == nil {
		return ErrNoConnection
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tx, ok := TxFromContext(ctx)
	if !ok {
		if outer, isTx := r.driver.(*txDriver); isTx {
			r.driver = outer.Driver
		}
		return r.driverLive()
	}
	base := r.driver
	if outer, isTx := base.(*txDriver); isTx {
		if outer.tx == tx {
			return r.driverLive()
		}
		base = outer.Driver
	}
	if base == nil {
		if m := Default(); m != nil {
			base = m.DefaultDriver()
		}
	}
	if base != nil {
		r.driver = &txDriver{Driver: base, tx: tx}
	}
	return r.driverLive()
}

// driverLive mirrors Query.driverLive for the raw-SQL escape hatch.
func (r *RawQuery[T]) driverLive() error {
	if r.mgr != nil && r.mgr.closed.Load() {
		return ErrManagerShutdown
	}
	if r.driver == nil {
		return ErrNoConnection
	}
	return nil
}

// First executes the raw query and scans the first result into dest.
// ctx is the first positional argument; pass a tx-bound ctx (e.g. the
// one received inside Manager.Transaction's closure) to enroll the
// statement in that tx, or a plain ctx to opt out and route through the
// pool driver.
func (r *RawQuery[T]) First(ctx context.Context, dest *T) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := r.bindTxFromContextValue(ctx); err != nil {
		return err
	}

	rows, err := r.driver.QueryContext(ctx, r.sql, r.args...)
	if err != nil {
		return err
	}
	defer rows.Close()

	if !rows.Next() {
		return ErrRecordNotFound
	}

	if err := scanIntoStruct(rows, dest); err != nil {
		return err
	}

	// Mirror Query[T].Get: rows scanned through a raw query are still
	// existing rows from the caller's perspective, so a downstream
	// Save must take the UPDATE path. existenceSetter is a no-op on
	// Immutable* (correct, no UPDATE branch exists for them).
	markExisting(dest)

	return nil
}

// Get executes the raw query and returns all matching results. ctx is
// the first positional argument; same tx-binding semantics as First.
func (r *RawQuery[T]) Get(ctx context.Context) ([]T, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := r.bindTxFromContextValue(ctx); err != nil {
		return nil, err
	}

	rows, err := r.driver.QueryContext(ctx, r.sql, r.args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []T
	for rows.Next() {
		var model T
		if err := scanIntoStruct(rows, &model); err != nil {
			return nil, err
		}
		results = append(results, model)
	}
	// A driver error mid-iteration surfaces via rows.Err(), not via
	// rows.Next()/Scan.
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// Mark each slice element AFTER all appends so the backing array
	// is final - same reasoning as Query[T].Get above.
	for i := range results {
		markExisting(&results[i])
	}

	return results, nil
}

// Scan executes the raw query and scans into custom destination pointers.
// Useful for queries that return scalar values or don't map to structs.
// ctx is the first positional argument; same tx-binding semantics as
// First.
func (r *RawQuery[T]) Scan(ctx context.Context, dest ...any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := r.bindTxFromContextValue(ctx); err != nil {
		return err
	}

	return r.driver.QueryRowContext(ctx, r.sql, r.args...).Scan(dest...)
}

// Exec executes a raw SQL statement (INSERT, UPDATE, DELETE) and returns
// the underlying sql.Result so callers can inspect both RowsAffected and
// LastInsertId via the standard database/sql helpers. ctx is the first
// positional argument; pass a tx-bound ctx to enroll the write in the
// caller's transaction (so a rollback inside Manager.Transaction undoes
// the raw write), or a non-tx ctx to execute against the pool driver and
// escape the surrounding tx.
//
// Use result.RowsAffected() for the affected row count and
// result.LastInsertId() for the inserted primary key (driver-dependent;
// some drivers return -1 when unavailable, which is why the bare int64
// flavor of this method was removed).
func (r *RawQuery[T]) Exec(ctx context.Context) (sql.Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := r.bindTxFromContextValue(ctx); err != nil {
		return nil, err
	}

	result, err := r.driver.ExecContext(ctx, r.sql, r.args...)
	if err != nil {
		return nil, err
	}

	return result, nil
}
