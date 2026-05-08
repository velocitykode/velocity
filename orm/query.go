package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
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

// NOW is a [RawSQL] sentinel for the database's current-timestamp function
// on MySQL/MariaDB and PostgreSQL. Pass it as a value in an Update or Insert
// map to have the grammar emit `NOW()` verbatim rather than bind the
// literal string `"NOW()"` as a parameter.
//
// For SQLite, which does not expose a NOW() function, use
// [CurrentTimestamp]. The ORM's built-in timestamp injection in Update
// automatically picks the driver-appropriate sentinel.
const NOW RawSQL = "NOW()"

// CurrentTimestamp is a [RawSQL] sentinel for the database's
// current-timestamp keyword. It is supported on all three drivers
// (MySQL, PostgreSQL, SQLite) and is the portable counterpart to [NOW].
const CurrentTimestamp RawSQL = "CURRENT_TIMESTAMP"

// currentTimestampSentinel returns the driver-appropriate [RawSQL] value
// for "set this column to the database's current timestamp". MySQL and
// PostgreSQL use NOW(); SQLite uses CURRENT_TIMESTAMP.
func currentTimestampSentinel(driverName string) RawSQL {
	if driverName == "sqlite" {
		return CurrentTimestamp
	}
	return NOW
}

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
	driver        drivers.Driver
	table         string
	conditions    []drivers.Condition
	orders        []drivers.Order
	groups        []string
	having        []drivers.Condition
	joins         []drivers.Join
	limit         *int
	offset        *int
	columns       []string
	distinct      bool
	preloads      []string
	withTrashed   bool
	onlyTrashed   bool
	lockForUpdate bool // For pessimistic locking
	skipLocked    bool // For SKIP LOCKED clause
	hasSoftDelete bool // Whether the model supports soft deletes
	hasUpdatedAt  bool // Whether the model has an UpdatedAt column (skip injection on Update for immutable models)
	withRowHooks  bool // When true, bulk Update/Delete/ForceDelete fan out per-row AfterCommit/AfterRollback hooks (Tier C). Suppresses BulkAfterCommitHook.

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
	if m := Default(); m != nil {
		drv = m.DefaultDriver()
	}
	q := &Query[T]{
		driver:        drv,
		table:         getTableName[T](),
		columns:       []string{"*"},
		hasSoftDelete: modelHasSoftDelete[T](),
		hasUpdatedAt:  modelHasUpdatedAt[T](),
	}
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
		table:               q.table,
		distinct:            q.distinct,
		withTrashed:         q.withTrashed,
		onlyTrashed:         q.onlyTrashed,
		lockForUpdate:       q.lockForUpdate,
		skipLocked:          q.skipLocked,
		hasSoftDelete:       q.hasSoftDelete,
		hasUpdatedAt:        q.hasUpdatedAt,
		withRowHooks:        q.withRowHooks,
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

// Where adds a WHERE condition
func (q *Query[T]) Where(condition string, args ...any) *Query[T] {
	col, op, val, err := parseCondition(condition, args)
	if err != nil {
		q.setErr("Where", err)
		return q
	}
	q.conditions = append(q.conditions, drivers.Condition{
		Column:   col,
		Operator: op,
		Value:    val,
		Type:     "and",
	})
	return q
}

// OrWhere adds an OR WHERE condition
func (q *Query[T]) OrWhere(condition string, args ...any) *Query[T] {
	col, op, val, err := parseCondition(condition, args)
	if err != nil {
		q.setErr("OrWhere", err)
		return q
	}
	q.conditions = append(q.conditions, drivers.Condition{
		Column:   col,
		Operator: op,
		Value:    val,
		Type:     "or",
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
//   - "col = ?", val        -> col, "=", val
//   - "col", val            -> col, "=", val (default operator)
//   - "col IS NULL"         -> col, "IS NULL", nil
//   - "col IS NOT NULL"     -> col, "IS NOT NULL", nil
//   - "col > ?", val        -> col, ">", val
func parseCondition(condition string, args []any) (column, operator string, value any, err error) {
	condition = strings.TrimSpace(condition)

	// Check for IS NULL / IS NOT NULL patterns (case-insensitive)
	upperCond := strings.ToUpper(condition)
	if idx := strings.Index(upperCond, " IS NOT NULL"); idx != -1 {
		col := strings.TrimSpace(condition[:idx])
		if err := validateIdentifier(col); err != nil {
			return "", "", nil, err
		}
		return col, "IS NOT NULL", nil, nil
	}
	if idx := strings.Index(upperCond, " IS NULL"); idx != -1 {
		col := strings.TrimSpace(condition[:idx])
		if err := validateIdentifier(col); err != nil {
			return "", "", nil, err
		}
		return col, "IS NULL", nil, nil
	}

	// Split into parts: column, operator, rest
	parts := strings.SplitN(condition, " ", 3)

	if len(parts) == 1 {
		// Only column provided, default to "="
		if err := validateIdentifier(parts[0]); err != nil {
			return "", "", nil, err
		}
		if len(args) > 0 {
			return parts[0], "=", args[0], nil
		}
		return parts[0], "=", nil, nil
	}

	// Column and operator provided
	column = parts[0]
	operator = parts[1]

	// Validate column name
	if err := validateIdentifier(column); err != nil {
		return "", "", nil, err
	}

	// Validate operator against allowlist
	if !isValidOperator(operator) {
		return "", "", nil, fmt.Errorf("invalid SQL operator: %q", operator)
	}

	if len(args) > 0 {
		value = args[0]
	}

	return column, operator, value, nil
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
	col, op, val, err := parseCondition(condition, args)
	if err != nil {
		q.setErr("Having", err)
		return q
	}
	if err := validateIdentifier(col); err != nil {
		q.setErr("Having", err)
		return q
	}
	q.having = append(q.having, drivers.Condition{
		Column:   col,
		Operator: op,
		Value:    val,
		Type:     "and",
	})
	return q
}

// buildJoinOn safely builds a JOIN ON clause with validated identifiers and operator.
// Returns the built clause and a validation error if either identifier or
// operator is invalid; callers funnel the error into q.err via setErr.
func (q *Query[T]) buildJoinOn(first, operator, second string) (string, error) {
	if !isValidOperator(operator) {
		return "", fmt.Errorf("invalid JOIN operator: %q", operator)
	}
	grammar := q.driver.Grammar()
	return fmt.Sprintf("%s %s %s", grammar.QuoteIdentifier(first), operator, grammar.QuoteIdentifier(second)), nil
}

// Join adds an INNER JOIN
func (q *Query[T]) Join(table, first, operator, second string) *Query[T] {
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

// Select specifies columns to select
func (q *Query[T]) Select(columns ...string) *Query[T] {
	for _, col := range columns {
		// Skip validation for raw expressions (e.g., "COUNT(*)", "SUM(amount)")
		if strings.Contains(col, "(") {
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
	q.bindTxFromContextValue(ctx)
	if q.driver == nil {
		return errors.New("orm: no database connection")
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
	q.bindTxFromContextValue(ctx)
	if q.driver == nil {
		return errors.New("orm: no database connection")
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
	q.bindTxFromContextValue(ctx)
	if q.driver == nil {
		return nil, errors.New("orm: no database connection")
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
	q.bindTxFromContextValue(ctx)
	if q.driver == nil {
		return nil, errors.New("orm: no database connection")
	}
	return updateOrCreateWithDriver[T](ctx, q.driver, conditions, values)
}

// Create inserts a new record through the query's bound driver. Takes
// ctx as the first argument: a ctx returned by Manager.Transaction
// enrolls the write in the caller's transaction. Accepts a
// map[string]any for fillable assignment or a *T already populated by
// the caller.
func (q *Query[T]) Create(ctx context.Context, data any) (*T, error) {
	if q.err != nil {
		return nil, q.err
	}
	q.bindTxFromContextValue(ctx)
	if q.driver == nil {
		return nil, errors.New("orm: no database connection")
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
		// Mirror Model[T].Create: fillable/guarded gates apply to
		// pre-built struct pointers so mass-assignment protection is
		// not bypassed by callers who construct the model manually.
		if err := applyFillableToStruct(v); err != nil {
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
func (q *Query[T]) bindTxFromContextValue(ctx context.Context) {
	if q == nil {
		return
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
		return
	}
	base := q.driver
	if outer, isTx := base.(*txDriver); isTx {
		if outer.tx == tx {
			return
		}
		base = outer.Driver
	}
	if base == nil {
		if m := Default(); m != nil {
			base = m.DefaultDriver()
		}
	}
	if base == nil {
		return
	}
	q.driver = &txDriver{Driver: base, tx: tx}
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
	q.bindTxFromContextValue(ctx)
	q.applySoftDeleteScope(ctx)

	// Build SELECT query
	selectQuery := &drivers.SelectQuery{
		Table:         q.table,
		Columns:       q.columns,
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

	// Track query timing
	start := time.Now()

	rows, err := q.driver.QueryContext(ctx, sql, args...)

	// Dispatch event regardless of error
	duration := time.Since(start)
	var rowCount int64
	if err == nil {
		// We'll count rows as we scan them
	}

	if err != nil {
		dispatchQueryExecuted(ctx, sql, args, duration, 0, q.driver.DriverName(), 2)
		return nil, err
	}
	defer rows.Close()

	var results []T
	for rows.Next() {
		var model T
		if err := scanIntoStruct(rows, &model); err != nil {
			dispatchQueryExecuted(ctx, sql, args, duration, int64(len(results)), q.driver.DriverName(), 2)
			return nil, err
		}
		results = append(results, model)
	}
	// Side-channel existence is keyed by pointer. Mark each slice
	// element AFTER all appends so the slice's backing array is final
	// (a mid-loop append could grow the slice, invalidating
	// element-address marks taken before the grow).
	for i := range results {
		markExisting(&results[i])
	}

	rowCount = int64(len(results))
	dispatchQueryExecuted(ctx, sql, args, time.Since(start), rowCount, q.driver.DriverName(), 2)

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
	q.bindTxFromContextValue(ctx)
	q.applySoftDeleteScope(ctx)
	q.columns = []string{"COUNT(*) as count"}

	selectQuery := &drivers.SelectQuery{
		Table:      q.table,
		Columns:    q.columns,
		Conditions: q.conditions,
		Joins:      q.joins,
		Distinct:   q.distinct,
	}

	sql, args := q.driver.Grammar().CompileSelect(selectQuery)

	start := time.Now()
	var count int64
	err := q.driver.QueryRowContext(ctx, sql, args...).Scan(&count)
	dispatchQueryExecuted(ctx, sql, args, time.Since(start), 1, q.driver.DriverName(), 2)

	return int(count), err
}

// Exists checks if any records match. Takes ctx as the first argument.
func (q *Query[T]) Exists(ctx context.Context) bool {
	count, _ := q.Count(ctx)
	return count > 0
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
	q.bindTxFromContextValue(ctx)
	q.applySoftDeleteScope(ctx)
	q.Select(column)

	selectQuery := &drivers.SelectQuery{
		Table:      q.table,
		Columns:    q.columns,
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

	start := time.Now()
	rows, err := q.driver.QueryContext(ctx, sql, args...)
	if err != nil {
		dispatchQueryExecuted(ctx, sql, args, time.Since(start), 0, q.driver.DriverName(), 2)
		return nil, err
	}
	defer rows.Close()

	var results []any
	for rows.Next() {
		var value any
		if err := rows.Scan(&value); err != nil {
			dispatchQueryExecuted(ctx, sql, args, time.Since(start), int64(len(results)), q.driver.DriverName(), 2)
			return nil, err
		}
		results = append(results, value)
	}

	dispatchQueryExecuted(ctx, sql, args, time.Since(start), int64(len(results)), q.driver.DriverName(), 2)
	return results, nil
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
	q.bindTxFromContextValue(ctx)
	// Soft-delete scope: an Update on a soft-deletable model must not touch
	// already-trashed rows unless the caller explicitly opted in via
	// WithTrashed / OnlyTrashed. Delete delegates here for the soft-delete
	// path, so the same predicate also prevents double-stamping deleted_at
	// on already-trashed rows.
	q.applyGlobalScopes(ctx)

	// Copy the caller's map before mutation. Update must not have
	// visible side effects on the passed-in map (thread safety, idempotent
	// re-dispatch, and least-surprise for callers).
	copyOfUpdates := make(map[string]any, len(updates)+1)
	for k, v := range updates {
		copyOfUpdates[k] = v
	}

	// Inject the driver-appropriate "current timestamp" sentinel for
	// updated_at. Using the typed [RawSQL] marker (not a raw string)
	// means the grammar emits it verbatim without pattern-matching
	// string contents, closing the SQL-injection vector that the old
	// "NOW()" string sentinel opened.
	//
	// Skip the injection when the model has no UpdatedAt column
	// (ImmutableModel/ImmutableUUIDModel) so the generated UPDATE does
	// not target a non-existent column.
	if q.hasUpdatedAt {
		copyOfUpdates["updated_at"] = currentTimestampSentinel(q.driver.DriverName())
	}

	// Pre-capture for bulk hooks runs BEFORE the write so the affected
	// row set (Tier B IDs / Tier C model snapshots) is captured against
	// the conditions as the caller sees them. Returns a deferred closure
	// that fires after a successful write.
	afterFn, err := q.bulkPrepareHooks(ctx, op)
	if err != nil {
		return 0, err
	}

	sql, args := q.driver.Grammar().CompileUpdate(q.table, copyOfUpdates, q.conditions)
	q.lastSQL = sql
	q.lastArgs = args

	start := time.Now()
	result, err := q.driver.ExecContext(ctx, sql, args...)
	duration := time.Since(start)

	// skip=3 because bulkUpdate is always reached through Update or
	// Delete (one extra frame above bulkUpdate compared to the
	// inline-emitter terminals like Get / Count / ForceDelete).
	if err != nil {
		dispatchQueryExecuted(ctx, sql, args, duration, 0, q.driver.DriverName(), 3)
		return 0, err
	}

	rowsAffected, _ := result.RowsAffected()
	dispatchQueryExecuted(ctx, sql, args, duration, rowsAffected, q.driver.DriverName(), 3)

	if afterFn != nil {
		afterFn()
	}

	return rowsAffected, nil
}

// InsertGetId inserts a record and returns the ID. Takes ctx as the
// first argument so transaction enrollment is mandatory and explicit;
// see Query.Save for the rationale.
func (q *Query[T]) InsertGetId(ctx context.Context, data map[string]any) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	q.bindTxFromContextValue(ctx)
	if len(data) == 0 {
		return 0, errors.New("no data provided for insert")
	}

	// Build the INSERT query
	columns := make([]string, 0, len(data))
	values := make([]any, 0, len(data))
	placeholders := make([]string, 0, len(data))

	i := 1
	for col, val := range data {
		if err := validateIdentifier(col); err != nil {
			return 0, fmt.Errorf("velocity/orm: insertGetId: %w", err)
		}
		columns = append(columns, col)
		values = append(values, val)
		placeholders = append(placeholders, q.driver.Grammar().Placeholder(i))
		i++
	}

	driverName := q.driver.DriverName()

	// Check driver type to determine how to get last insert ID
	if driverName == "sqlite" || driverName == "mysql" {
		// SQLite/MySQL: Use standard INSERT and get last insert ID from result
		sql := fmt.Sprintf(
			"INSERT INTO %s (%s) VALUES (%s)",
			q.table,
			strings.Join(columns, ", "),
			strings.Join(placeholders, ", "),
		)

		start := time.Now()
		result, err := q.driver.ExecContext(ctx, sql, values...)
		duration := time.Since(start)

		if err != nil {
			dispatchQueryExecuted(ctx, sql, values, duration, 0, driverName, 2)
			return 0, err
		}

		lastID, _ := result.LastInsertId()
		dispatchQueryExecuted(ctx, sql, values, duration, 1, driverName, 2)
		return lastID, nil
	} else {
		// PostgreSQL: Use RETURNING id clause
		sql := fmt.Sprintf(
			"INSERT INTO %s (%s) VALUES (%s) RETURNING id",
			q.table,
			strings.Join(columns, ", "),
			strings.Join(placeholders, ", "),
		)

		start := time.Now()
		var lastID int64
		err := q.driver.QueryRowContext(ctx, sql, values...).Scan(&lastID)
		duration := time.Since(start)

		if err != nil {
			dispatchQueryExecuted(ctx, sql, values, duration, 0, driverName, 2)
			return 0, err
		}

		dispatchQueryExecuted(ctx, sql, values, duration, 1, driverName, 2)
		return lastID, nil
	}
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
		// Soft delete, use the driver-appropriate RawSQL sentinel so the
		// grammar emits it verbatim (not as a bound parameter). Route
		// through bulkUpdate (not Update) so BulkAfterCommitHook listeners
		// receive op=BulkOpDelete instead of op=BulkOpUpdate.
		q.bindTxFromContextValue(ctx)
		return q.bulkUpdate(ctx, map[string]any{
			"deleted_at": currentTimestampSentinel(q.driver.DriverName()),
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
func (q *Query[T]) ForceDelete(ctx context.Context) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	q.bindTxFromContextValue(ctx)

	afterFn, err := q.bulkPrepareHooks(ctx, BulkOpForceDelete)
	if err != nil {
		return 0, err
	}

	sql, args := q.driver.Grammar().CompileDelete(q.table, q.conditions)

	start := time.Now()
	result, err := q.driver.ExecContext(ctx, sql, args...)
	duration := time.Since(start)

	if err != nil {
		dispatchQueryExecuted(ctx, sql, args, duration, 0, q.driver.DriverName(), 2)
		return 0, err
	}

	rowsAffected, _ := result.RowsAffected()
	dispatchQueryExecuted(ctx, sql, args, duration, rowsAffected, q.driver.DriverName(), 2)

	if afterFn != nil {
		afterFn()
	}

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
	var model T
	t := reflect.TypeOf(model)

	// Handle pointer types
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Check for TableName method
	v := reflect.ValueOf(model)
	if method := v.MethodByName("TableName"); method.IsValid() {
		result := method.Call(nil)
		if len(result) > 0 {
			if tableName, ok := result[0].Interface().(string); ok {
				return tableName
			}
		}
	}

	// Default to plural lowercase type name
	name := t.Name()
	if name == "" {
		// For generic types, use a default
		return "records"
	}
	return strings.ToLower(name) + "s"
}

// scanIntoStruct hydrates dest from the next row of rows. Resolves columns
// through the canonical ModelMeta so the read path is symmetric with
// structToMap (write) and applyFillableToStruct (policy). Before the
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
		} else {
			valuePtrs[i] = &values[i]
		}
	}

	return rows.Scan(valuePtrs...)
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

// toSnakeCase is the legacy package-private alias kept so internal callers
// in this package don't have to change. New cross-package callers should use
// ToSnakeCase.
func toSnakeCase(str string) string {
	return ToSnakeCase(str)
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
	sql    string
	args   []any
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
	if m := Default(); m != nil {
		drv = m.DefaultDriver()
	}
	return &RawQuery[T]{
		driver: drv,
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
func (r *RawQuery[T]) bindTxFromContextValue(ctx context.Context) {
	if r == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	tx, ok := TxFromContext(ctx)
	if !ok {
		if outer, isTx := r.driver.(*txDriver); isTx {
			r.driver = outer.Driver
		}
		return
	}
	base := r.driver
	if outer, isTx := base.(*txDriver); isTx {
		if outer.tx == tx {
			return
		}
		base = outer.Driver
	}
	if base == nil {
		if m := Default(); m != nil {
			base = m.DefaultDriver()
		}
	}
	if base == nil {
		return
	}
	r.driver = &txDriver{Driver: base, tx: tx}
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
	r.bindTxFromContextValue(ctx)

	start := time.Now()
	rows, err := r.driver.QueryContext(ctx, r.sql, r.args...)
	duration := time.Since(start)

	if err != nil {
		dispatchQueryExecuted(ctx, r.sql, r.args, duration, 0, r.driver.DriverName(), 2)
		return err
	}
	defer rows.Close()

	if !rows.Next() {
		dispatchQueryExecuted(ctx, r.sql, r.args, duration, 0, r.driver.DriverName(), 2)
		return ErrRecordNotFound
	}

	if err := scanIntoStruct(rows, dest); err != nil {
		dispatchQueryExecuted(ctx, r.sql, r.args, duration, 0, r.driver.DriverName(), 2)
		return err
	}

	// Mirror Query[T].Get: rows scanned through a raw query are still
	// existing rows from the caller's perspective, so a downstream
	// Save must take the UPDATE path. existenceSetter is a no-op on
	// Immutable* (correct, no UPDATE branch exists for them).
	markExisting(dest)

	dispatchQueryExecuted(ctx, r.sql, r.args, duration, 1, r.driver.DriverName(), 2)
	return nil
}

// Get executes the raw query and returns all matching results. ctx is
// the first positional argument; same tx-binding semantics as First.
func (r *RawQuery[T]) Get(ctx context.Context) ([]T, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	r.bindTxFromContextValue(ctx)

	start := time.Now()
	rows, err := r.driver.QueryContext(ctx, r.sql, r.args...)
	duration := time.Since(start)

	if err != nil {
		dispatchQueryExecuted(ctx, r.sql, r.args, duration, 0, r.driver.DriverName(), 2)
		return nil, err
	}
	defer rows.Close()

	var results []T
	for rows.Next() {
		var model T
		if err := scanIntoStruct(rows, &model); err != nil {
			dispatchQueryExecuted(ctx, r.sql, r.args, duration, int64(len(results)), r.driver.DriverName(), 2)
			return nil, err
		}
		results = append(results, model)
	}
	// Mark each slice element AFTER all appends so the backing array
	// is final - same reasoning as Query[T].Get above.
	for i := range results {
		markExisting(&results[i])
	}

	dispatchQueryExecuted(ctx, r.sql, r.args, duration, int64(len(results)), r.driver.DriverName(), 2)
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
	r.bindTxFromContextValue(ctx)

	start := time.Now()
	err := r.driver.QueryRowContext(ctx, r.sql, r.args...).Scan(dest...)
	duration := time.Since(start)

	rowCount := int64(0)
	if err == nil {
		rowCount = 1
	}
	dispatchQueryExecuted(ctx, r.sql, r.args, duration, rowCount, r.driver.DriverName(), 2)

	return err
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
	r.bindTxFromContextValue(ctx)

	start := time.Now()
	result, err := r.driver.ExecContext(ctx, r.sql, r.args...)
	duration := time.Since(start)

	if err != nil {
		dispatchQueryExecuted(ctx, r.sql, r.args, duration, 0, r.driver.DriverName(), 2)
		return nil, err
	}

	rowsAffected, _ := result.RowsAffected()
	dispatchQueryExecuted(ctx, r.sql, r.args, duration, rowsAffected, r.driver.DriverName(), 2)

	return result, nil
}
