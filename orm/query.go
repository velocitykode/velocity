package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
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

// softDeleteCache caches the result of modelHasSoftDelete per reflect.Type
// to avoid repeated reflection on every newQuery call.
var softDeleteCache sync.Map

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

	// disabledScopes records the global scope names this query opts out
	// of. nil when no opt-outs have been set (no allocation in the
	// common case).
	disabledScopes map[string]bool

	// globalScopesApplied guards applyGlobalScopes against double-apply
	// when one terminal delegates to another (e.g. First -> Get).
	globalScopesApplied bool

	// Context for event propagation
	ctx context.Context

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

// updatedAtCache caches modelHasUpdatedAt results per reflect.Type.
var updatedAtCache sync.Map

// modelHasUpdatedAt reports whether T (or its embedded ORM base type) has
// an UpdatedAt column. ImmutableModel/ImmutableUUIDModel return false; all
// other built-in base types return true. Custom direct UpdatedAt fields
// also return true. Result is cached per type.
func modelHasUpdatedAt[T any]() bool {
	var model T
	t := reflect.TypeOf(model)
	if t == nil {
		return false
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if cached, ok := updatedAtCache.Load(t); ok {
		return cached.(bool)
	}
	result := checkUpdatedAt(t)
	updatedAtCache.Store(t, result)
	return result
}

// checkUpdatedAt walks the immediate fields of t looking for an embedded
// ORM base type or a direct UpdatedAt field.
func checkUpdatedAt(t reflect.Type) bool {
	if t.Kind() != reflect.Struct {
		return false
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		typStr := field.Type.String()

		// Immutable variants explicitly opt out of UpdatedAt.
		if strings.HasPrefix(typStr, "orm.ImmutableModel[") ||
			strings.HasPrefix(typStr, "orm.ImmutableUUIDModel[") {
			return false
		}
		// All other built-in base types include UpdatedAt.
		if strings.HasPrefix(typStr, "orm.Model[") ||
			strings.HasPrefix(typStr, "orm.UUIDModel[") ||
			strings.HasPrefix(typStr, "orm.SoftDeleteModel[") ||
			strings.HasPrefix(typStr, "orm.SoftDeleteUUIDModel[") {
			return true
		}
		// Direct UpdatedAt field on a custom model.
		if field.Name == "UpdatedAt" {
			return true
		}
	}
	return false
}

// modelHasSoftDelete checks if the model type T has a DeletedAt field (supports soft deletes).
// Results are cached per type to avoid repeated reflection on every query.
func modelHasSoftDelete[T any]() bool {
	var model T
	t := reflect.TypeOf(model)

	// Handle pointer types
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Check cache first
	if cached, ok := softDeleteCache.Load(t); ok {
		return cached.(bool)
	}

	// Compute and cache the result
	result := checkSoftDelete(t)
	softDeleteCache.Store(t, result)
	return result
}

// checkSoftDelete performs the actual reflection check for soft-delete support.
func checkSoftDelete(t reflect.Type) bool {
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// Check for embedded SoftDeleteModel or SoftDeleteUUIDModel (which HAVE DeletedAt)
		if strings.HasPrefix(field.Type.String(), "orm.SoftDeleteModel[") ||
			strings.HasPrefix(field.Type.String(), "orm.SoftDeleteUUIDModel[") {
			return true
		}

		// Check for embedded Model or UUIDModel (which do NOT have DeletedAt)
		if strings.HasPrefix(field.Type.String(), "orm.Model[") ||
			strings.HasPrefix(field.Type.String(), "orm.UUIDModel[") {
			return false
		}

		// Direct DeletedAt field check (for custom models)
		if field.Name == "DeletedAt" {
			return true
		}
	}

	return false
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
		globalScopesApplied: q.globalScopesApplied,
		ctx:                 q.ctx,
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

// WithTx binds the query to a *sql.Tx so subsequent reads and writes
// (Get, First, Update, Delete, InsertGetId, Save, Create, CreateMany,
// FirstOrCreate, UpdateOrCreate) execute on the supplied transaction
// instead of the connection pool. Use this inside a Manager.Transaction
// closure so ORM helpers participate in the caller's tx instead of
// escaping it.
//
// The wrapped driver still supplies dialect grammar, driver name, and
// schema introspection; only data-plane ops route through the tx.
// Passing nil leaves the query unchanged so callers can chain WithTx
// unconditionally without nil-guarding at every call site.
//
// Concurrency: *sql.Tx is single-threaded by stdlib contract; the
// returned Query and any handle derived from it (Where, OrderBy, ...)
// must be used from the same goroutine that owns the transaction.
// Fanout inside the Transaction closure must serialize back to one
// goroutine before calling tx-bound helpers.
//
// Re-binding: a second WithTx call on a chain that is already
// tx-bound rebinds against the original pool driver instead of
// stacking another wrapper, so middleware that defensively re-applies
// WithTx is safe.
func (q *Query[T]) WithTx(tx *sql.Tx) *Query[T] {
	if tx == nil {
		return q
	}
	base := q.driver
	if base == nil {
		if m := Default(); m != nil {
			base = m.DefaultDriver()
		}
	}
	if base == nil {
		return q
	}
	// If the chain is already tx-bound (an earlier WithTx call),
	// rebind to the new tx against the original pool driver instead of
	// nesting txDriver wrappers. Stacking would still resolve to the
	// outer tx but leaves the inner Driver methods double-shadowed,
	// which is harder to reason about.
	if outer, ok := base.(*txDriver); ok {
		base = outer.Driver
	}
	q.driver = &txDriver{Driver: base, tx: tx}
	return q
}

// Save persists model through the query's bound driver. Unlike the
// package-level Save, which resolves the driver from a *Manager, this
// method uses q.driver so a tx-bound query (via WithTx) writes inside
// the caller's transaction. Hooks (BeforeCreate/AfterCreate/...) and
// timestamp stamping fire identically to Save.
func (q *Query[T]) Save(model *T) error {
	if q.err != nil {
		return q.err
	}
	if q.driver == nil {
		return errors.New("orm: no database connection")
	}
	return saveWithDriver(q.driver, model)
}

// CreateMany inserts multiple records through the query's bound driver
// so a tx-bound query (via WithTx) writes the entire batch inside the
// caller's transaction. The static-like Model[T].CreateMany helpers
// still resolve the default manager and auto-commit; this method is
// the tx-aware counterpart used by outbox emitters and saga steps.
//
// Iteration is sequential: the first error short-circuits and any
// preceding rows are part of the in-flight tx, so the caller's
// Transaction closure can return the error to roll back the partial
// batch.
func (q *Query[T]) CreateMany(records []T) error {
	if q.err != nil {
		return q.err
	}
	if q.driver == nil {
		return errors.New("orm: no database connection")
	}
	for i := range records {
		if err := saveWithDriver(q.driver, &records[i]); err != nil {
			return err
		}
	}
	return nil
}

// FirstOrCreate routes through the query's bound driver so the
// "find-or-insert" pattern participates in a tx bound via WithTx.
// Mirrors Model[T].FirstOrCreate semantics; arguments and merging
// behavior are identical.
func (q *Query[T]) FirstOrCreate(conditions map[string]any, values map[string]any) (*T, error) {
	if q.err != nil {
		return nil, q.err
	}
	if q.driver == nil {
		return nil, errors.New("orm: no database connection")
	}
	return firstOrCreateWithDriver[T](q.driver, conditions, values)
}

// UpdateOrCreate routes through the query's bound driver so the
// "idempotency-then-write" pattern (the canonical motivating case for
// tx-aware ORM helpers) participates in a tx bound via WithTx. Mirrors
// Model[T].UpdateOrCreate semantics.
func (q *Query[T]) UpdateOrCreate(conditions map[string]any, values map[string]any) (*T, error) {
	if q.err != nil {
		return nil, q.err
	}
	if q.driver == nil {
		return nil, errors.New("orm: no database connection")
	}
	return updateOrCreateWithDriver[T](q.driver, conditions, values)
}

// Create inserts a new record through the query's bound driver,
// mirroring Model[T].Create but routing through q.driver so the write
// participates in a tx bound via WithTx. Accepts a map[string]any for
// fillable assignment or a *T already populated by the caller.
func (q *Query[T]) Create(data any) (*T, error) {
	if q.err != nil {
		return nil, q.err
	}
	if q.driver == nil {
		return nil, errors.New("orm: no database connection")
	}
	switch v := data.(type) {
	case map[string]any:
		model := new(T)
		if err := mapToStruct(v, model); err != nil {
			return nil, err
		}
		if err := saveWithDriver(q.driver, model); err != nil {
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
		if err := saveWithDriver(q.driver, v); err != nil {
			return nil, err
		}
		return v, nil
	default:
		return nil, errors.New("unsupported data type for create")
	}
}

// WithContext sets the context for the query (for event propagation).
func (q *Query[T]) WithContext(ctx context.Context) *Query[T] {
	q.ctx = ctx
	return q
}

// getContext returns the query context, or a background context if none set
func (q *Query[T]) getContext() context.Context {
	if q.ctx != nil {
		return q.ctx
	}
	return context.Background()
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
func (q *Query[T]) applySoftDeleteScope() {
	q.applyGlobalScopes()
}

// Get retrieves all matching records
func (q *Query[T]) Get() ([]T, error) {
	if q.err != nil {
		return nil, q.err
	}
	q.applySoftDeleteScope()

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

	rows, err := q.driver.QueryContext(q.getContext(), sql, args...)

	// Dispatch event regardless of error
	duration := time.Since(start)
	var rowCount int64
	if err == nil {
		// We'll count rows as we scan them
	}

	if err != nil {
		dispatchQueryExecuted(q.getContext(), sql, args, duration, 0, q.driver.DriverName(), 2)
		return nil, err
	}
	defer rows.Close()

	var results []T
	for rows.Next() {
		var model T
		if err := scanIntoStruct(rows, &model); err != nil {
			dispatchQueryExecuted(q.getContext(), sql, args, duration, int64(len(results)), q.driver.DriverName(), 2)
			return nil, err
		}

		// Mark as existing so a subsequent Save routes through UPDATE
		// (with BeforeUpdate/AfterUpdate hooks) instead of re-inserting.
		// The dynamic type of &model is *T (the embedding struct), not
		// *Model[T], so a direct type assertion always fails. Routing
		// through the existenceSetter interface lets method promotion
		// resolve to the embedded base type's setExisting.
		markExisting(&model)

		results = append(results, model)
	}

	rowCount = int64(len(results))
	dispatchQueryExecuted(q.getContext(), sql, args, time.Since(start), rowCount, q.driver.DriverName(), 2)

	// Handle eager loading
	if len(q.preloads) > 0 {
		if err := q.loadRelations(&results); err != nil {
			return nil, err
		}
	}

	return results, nil
}

// First retrieves the first matching record
func (q *Query[T]) First(dest *T) error {
	q.Limit(1)
	results, err := q.Get()
	if err != nil {
		return err
	}
	if len(results) == 0 {
		return ErrRecordNotFound
	}
	*dest = results[0]
	return nil
}

// Find retrieves a record by primary key
func (q *Query[T]) Find(id any, dest *T) error {
	return q.Where("id = ?", id).First(dest)
}

// Count returns the number of matching records
func (q *Query[T]) Count() (int, error) {
	if q.err != nil {
		return 0, q.err
	}
	q.applySoftDeleteScope()
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
	err := q.driver.QueryRowContext(q.getContext(), sql, args...).Scan(&count)
	dispatchQueryExecuted(q.getContext(), sql, args, time.Since(start), 1, q.driver.DriverName(), 2)

	return int(count), err
}

// Exists checks if any records match
func (q *Query[T]) Exists() bool {
	count, _ := q.Count()
	return count > 0
}

// Pluck retrieves values of a single column
func (q *Query[T]) Pluck(column string) ([]any, error) {
	if err := validateIdentifier(column); err != nil {
		q.setErr("Pluck", err)
	}
	if q.err != nil {
		return nil, q.err
	}
	q.applySoftDeleteScope()
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
	rows, err := q.driver.QueryContext(q.getContext(), sql, args...)
	if err != nil {
		dispatchQueryExecuted(q.getContext(), sql, args, time.Since(start), 0, q.driver.DriverName(), 2)
		return nil, err
	}
	defer rows.Close()

	var results []any
	for rows.Next() {
		var value any
		if err := rows.Scan(&value); err != nil {
			dispatchQueryExecuted(q.getContext(), sql, args, time.Since(start), int64(len(results)), q.driver.DriverName(), 2)
			return nil, err
		}
		results = append(results, value)
	}

	dispatchQueryExecuted(q.getContext(), sql, args, time.Since(start), int64(len(results)), q.driver.DriverName(), 2)
	return results, nil
}

// Update updates matching records.
//
// The input map is never mutated: Update copies it internally before
// injecting the updated_at timestamp. Values of type [RawSQL] (including
// the package-level [NOW] sentinel) are emitted verbatim into the
// generated statement; all other values, including plain string values
// that happen to look like SQL, are bound as parameters.
func (q *Query[T]) Update(updates map[string]any) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	if len(updates) == 0 {
		return 0, errors.New("no updates provided")
	}
	// Soft-delete scope: an Update on a soft-deletable model must not touch
	// already-trashed rows unless the caller explicitly opted in via
	// WithTrashed / OnlyTrashed. Delete delegates here for the soft-delete
	// path, so the same predicate also prevents double-stamping deleted_at
	// on already-trashed rows.
	q.applySoftDeleteScope()

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

	sql, args := q.driver.Grammar().CompileUpdate(q.table, copyOfUpdates, q.conditions)
	q.lastSQL = sql
	q.lastArgs = args

	start := time.Now()
	result, err := q.driver.ExecContext(q.getContext(), sql, args...)
	duration := time.Since(start)

	if err != nil {
		dispatchQueryExecuted(q.getContext(), sql, args, duration, 0, q.driver.DriverName(), 2)
		return 0, err
	}

	rowsAffected, _ := result.RowsAffected()
	dispatchQueryExecuted(q.getContext(), sql, args, duration, rowsAffected, q.driver.DriverName(), 2)

	return rowsAffected, nil
}

// InsertGetId inserts a record and returns the ID
func (q *Query[T]) InsertGetId(data map[string]any) (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
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
		result, err := q.driver.ExecContext(q.getContext(), sql, values...)
		duration := time.Since(start)

		if err != nil {
			dispatchQueryExecuted(q.getContext(), sql, values, duration, 0, driverName, 2)
			return 0, err
		}

		lastID, _ := result.LastInsertId()
		dispatchQueryExecuted(q.getContext(), sql, values, duration, 1, driverName, 2)
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
		err := q.driver.QueryRowContext(q.getContext(), sql, values...).Scan(&lastID)
		duration := time.Since(start)

		if err != nil {
			dispatchQueryExecuted(q.getContext(), sql, values, duration, 0, driverName, 2)
			return 0, err
		}

		dispatchQueryExecuted(q.getContext(), sql, values, duration, 1, driverName, 2)
		return lastID, nil
	}
}

// Delete soft deletes matching records (if model supports soft deletes) or hard deletes
func (q *Query[T]) Delete() (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	// Check if model has soft deletes
	if q.hasSoftDelete {
		// Soft delete, use the driver-appropriate RawSQL sentinel so the
		// grammar emits it verbatim (not as a bound parameter).
		return q.Update(map[string]any{
			"deleted_at": currentTimestampSentinel(q.driver.DriverName()),
		})
	}

	// Hard delete for models without soft delete support
	return q.ForceDelete()
}

// ForceDelete permanently deletes matching records
func (q *Query[T]) ForceDelete() (int64, error) {
	if q.err != nil {
		return 0, q.err
	}
	sql, args := q.driver.Grammar().CompileDelete(q.table, q.conditions)

	start := time.Now()
	result, err := q.driver.ExecContext(q.getContext(), sql, args...)
	duration := time.Since(start)

	if err != nil {
		dispatchQueryExecuted(q.getContext(), sql, args, duration, 0, q.driver.DriverName(), 2)
		return 0, err
	}

	rowsAffected, _ := result.RowsAffected()
	dispatchQueryExecuted(q.getContext(), sql, args, duration, rowsAffected, q.driver.DriverName(), 2)

	return rowsAffected, nil
}

// Chunk processes results in chunks
func (q *Query[T]) Chunk(size int, callback func([]T) error) error {
	page := 0
	for {
		q.Limit(size).Offset(page * size)
		results, err := q.Get()
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

func scanIntoStruct(rows *sql.Rows, dest any) error {
	// Get column names
	columns, err := rows.Columns()
	if err != nil {
		return err
	}

	// Create a slice of interface{} to hold the values
	values := make([]interface{}, len(columns))
	valuePtrs := make([]interface{}, len(columns))

	// Get the type and value of the destination struct
	destValue := reflect.ValueOf(dest).Elem()
	destType := destValue.Type()

	// Create a map of column names to field paths (for embedded structs)
	type fieldInfo struct {
		path []int
	}
	fieldMap := make(map[string]fieldInfo)

	var mapFields func(typ reflect.Type, path []int)
	mapFields = func(typ reflect.Type, path []int) {
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			currentPath := append(append([]int{}, path...), i)

			// Skip unexported fields
			if !field.IsExported() {
				continue
			}

			// Check if this is an embedded struct (but not time.Time)
			if field.Anonymous && field.Type.Kind() == reflect.Struct && field.Type.String() != "time.Time" {
				// Recursively map embedded struct fields
				mapFields(field.Type, currentPath)
				continue
			}

			// Skip fields marked with orm:"-" or relation fields
			tag := field.Tag.Get("orm")
			if tag == "-" || strings.Contains(tag, "relation:") {
				continue
			}

			// Many-to-many fields are virtual (loaded via separate pivot
			// query); never scanned from the parent SELECT.
			if extractManyToManyValue(tag) != "" {
				continue
			}

			// Polymorphic morph fields span two columns. Map type_col
			// onto Morph.TypeName and id_col onto Morph.ID so a regular
			// SELECT can populate the discriminator/id pair.
			if pv := extractPolymorphicValue(tag); pv != "" {
				if typeCol, idCol, perr := parsePolymorphicTag(pv); perr == nil {
					morphType := field.Type
					if morphType.Kind() == reflect.Struct {
						if tnf, ok := morphType.FieldByName("TypeName"); ok {
							fieldMap[typeCol] = fieldInfo{path: append(append([]int{}, currentPath...), tnf.Index...)}
						}
						if idf, ok := morphType.FieldByName("ID"); ok {
							fieldMap[idCol] = fieldInfo{path: append(append([]int{}, currentPath...), idf.Index...)}
						}
					}
				}
				continue
			}

			// Get the column name from the struct tag or field name.
			//
			// TODO(orm): Unify with fieldColumnName (orm/model.go). This
			// path diverges in two ways: (1) it re-applies toSnakeCase to
			// the resolved column name, mangling tags like
			// orm:"column:LegacyXYZ" into legacy_x_y_z, and (2) it has a
			// bare `primaryKey` shortcut that the model-side helpers
			// don't share. Reconciling these requires care because
			// scanIntoStruct is on the SELECT row-scan hot path and
			// existing schemas may rely on the snake-case-of-column
			// behavior. Tracked as a follow-up.
			columnName := field.Name
			if tag != "" {
				// Parse the tag to get column name
				parts := strings.Split(tag, ";")
				for _, part := range parts {
					if strings.HasPrefix(part, "column:") {
						columnName = strings.TrimPrefix(part, "column:")
						break
					}
					// Special handling for primaryKey tag
					if part == "primaryKey" {
						columnName = "id"
						break
					}
				}
			}

			// Convert to snake_case if needed
			columnName = toSnakeCase(columnName)
			fieldMap[columnName] = fieldInfo{path: currentPath}
		}
	}

	mapFields(destType, []int{})

	// Map columns to struct fields
	for i, column := range columns {
		if fieldInfo, ok := fieldMap[column]; ok {
			// Navigate to the field using the path
			field := destValue
			for _, index := range fieldInfo.path {
				field = field.Field(index)
			}

			if field.CanSet() {
				valuePtrs[i] = field.Addr().Interface()
			} else {
				valuePtrs[i] = &values[i]
			}
		} else {
			// Column doesn't map to a field, use a dummy value
			valuePtrs[i] = &values[i]
		}
	}

	// Scan the row
	return rows.Scan(valuePtrs...)
}

// toSnakeCase converts a string from CamelCase to snake_case
// Handles consecutive capitals: ProviderID -> provider_id, not provider_i_d
func toSnakeCase(str string) string {
	var result []byte
	for i, r := range str {
		if i > 0 && r >= 'A' && r <= 'Z' {
			// Only add underscore if previous char in ORIGINAL string is lowercase
			prevChar := str[i-1]
			if prevChar >= 'a' && prevChar <= 'z' {
				result = append(result, '_')
			}
		}
		result = append(result, byte(strings.ToLower(string(r))[0]))
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
	sql    string
	args   []any
	ctx    context.Context
}

// NewRawQuery creates a new raw query builder.
//
// WARNING: This method executes raw SQL directly. The caller is responsible for
// preventing SQL injection by using parameterized queries with placeholder arguments.
// Never concatenate user input directly into the sql string.
//
// WARNING: Soft-delete scopes are NOT applied. See the RawQuery type
// documentation for details. Use NewRawQuerySoftDeleteOnly to opt in.
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

// WithContext sets the context for the raw query
func (r *RawQuery[T]) WithContext(ctx context.Context) *RawQuery[T] {
	r.ctx = ctx
	return r
}

// getContext returns the query context, or a background context if none set
func (r *RawQuery[T]) getContext() context.Context {
	if r.ctx != nil {
		return r.ctx
	}
	return context.Background()
}

// First executes the raw query and scans the first result into dest
func (r *RawQuery[T]) First(dest *T) error {
	start := time.Now()
	rows, err := r.driver.QueryContext(r.getContext(), r.sql, r.args...)
	duration := time.Since(start)

	if err != nil {
		dispatchQueryExecuted(r.getContext(), r.sql, r.args, duration, 0, r.driver.DriverName(), 2)
		return err
	}
	defer rows.Close()

	if !rows.Next() {
		dispatchQueryExecuted(r.getContext(), r.sql, r.args, duration, 0, r.driver.DriverName(), 2)
		return ErrRecordNotFound
	}

	if err := scanIntoStruct(rows, dest); err != nil {
		dispatchQueryExecuted(r.getContext(), r.sql, r.args, duration, 0, r.driver.DriverName(), 2)
		return err
	}

	// Mirror Query[T].Get: rows scanned through a raw query are still
	// existing rows from the caller's perspective, so a downstream
	// Save must take the UPDATE path. existenceSetter is a no-op on
	// Immutable* (correct, no UPDATE branch exists for them).
	markExisting(dest)

	dispatchQueryExecuted(r.getContext(), r.sql, r.args, duration, 1, r.driver.DriverName(), 2)
	return nil
}

// Get executes the raw query and returns all matching results
func (r *RawQuery[T]) Get() ([]T, error) {
	start := time.Now()
	rows, err := r.driver.QueryContext(r.getContext(), r.sql, r.args...)
	duration := time.Since(start)

	if err != nil {
		dispatchQueryExecuted(r.getContext(), r.sql, r.args, duration, 0, r.driver.DriverName(), 2)
		return nil, err
	}
	defer rows.Close()

	var results []T
	for rows.Next() {
		var model T
		if err := scanIntoStruct(rows, &model); err != nil {
			dispatchQueryExecuted(r.getContext(), r.sql, r.args, duration, int64(len(results)), r.driver.DriverName(), 2)
			return nil, err
		}
		// Same reasoning as Query[T].Get: mark each scanned row as
		// existing so a subsequent Save updates instead of duplicating.
		markExisting(&model)
		results = append(results, model)
	}

	dispatchQueryExecuted(r.getContext(), r.sql, r.args, duration, int64(len(results)), r.driver.DriverName(), 2)
	return results, nil
}

// Scan executes the raw query and scans into custom destination pointers
// Useful for queries that return scalar values or don't map to structs
func (r *RawQuery[T]) Scan(dest ...any) error {
	start := time.Now()
	err := r.driver.QueryRowContext(r.getContext(), r.sql, r.args...).Scan(dest...)
	duration := time.Since(start)

	rowCount := int64(0)
	if err == nil {
		rowCount = 1
	}
	dispatchQueryExecuted(r.getContext(), r.sql, r.args, duration, rowCount, r.driver.DriverName(), 2)

	return err
}

// Exec executes a raw SQL statement (INSERT, UPDATE, DELETE) and returns affected rows
func (r *RawQuery[T]) Exec() (int64, error) {
	start := time.Now()
	result, err := r.driver.ExecContext(r.getContext(), r.sql, r.args...)
	duration := time.Since(start)

	if err != nil {
		dispatchQueryExecuted(r.getContext(), r.sql, r.args, duration, 0, r.driver.DriverName(), 2)
		return 0, err
	}

	rowsAffected, _ := result.RowsAffected()
	dispatchQueryExecuted(r.getContext(), r.sql, r.args, duration, rowsAffected, r.driver.DriverName(), 2)

	return rowsAffected, nil
}
