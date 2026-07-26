package orm

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/velocitykode/velocity/orm/drivers"
)

// Sum returns the sum of a column for matching records. Takes ctx as
// the first argument so reads participate in the caller's transaction
// when ctx carries a *sql.Tx. Returns 0 for empty result sets.
func (q *Query[T]) Sum(ctx context.Context, column string) (float64, error) {
	return q.aggregate(ctx, "SUM", column)
}

// Avg returns the average of a column for matching records. Takes ctx
// as the first argument. Returns 0 for empty result sets.
func (q *Query[T]) Avg(ctx context.Context, column string) (float64, error) {
	return q.aggregate(ctx, "AVG", column)
}

// Min returns the minimum value of a column for matching records.
// Takes ctx as the first argument. Returns 0 for empty result sets.
func (q *Query[T]) Min(ctx context.Context, column string) (float64, error) {
	return q.aggregate(ctx, "MIN", column)
}

// Max returns the maximum value of a column for matching records.
// Takes ctx as the first argument. Returns 0 for empty result sets.
func (q *Query[T]) Max(ctx context.Context, column string) (float64, error) {
	return q.aggregate(ctx, "MAX", column)
}

// aggregate executes an aggregate function on a column and returns the result.
//
// Scope semantics: aggregate honours every registered global scope on T
// (tenant, archive, soft-delete, ...). A Sum/Avg/Min/Max over a
// SoftDeleteModel does NOT include trashed rows by default, and a
// multi-tenant aggregate cannot leak the other tenant's totals. Opt out
// per-query with [Query.WithoutGlobalScope].
func (q *Query[T]) aggregate(ctx context.Context, fn, column string) (float64, error) {
	if err := validateIdentifier(column); err != nil {
		return 0, fmt.Errorf("%s: %w", fn, err)
	}
	if q.err != nil {
		return 0, q.err
	}
	if err := q.bindTxFromContextValue(ctx); err != nil {
		return 0, err
	}
	q.applyGlobalScopes(ctx)
	// A scope predicate that fails validation (invalid identifier,
	// unknown operator, driver-registered operator with bad value) sets
	// q.err during apply. Surface it before issuing SQL so a broken
	// scope cannot silently drop its predicate.
	if q.err != nil {
		return 0, q.err
	}

	// Framework-built aggregate projection: emit through the trusted
	// RawColumns path so the user-facing Columns whitelist (which
	// forbids quotes and backticks) does not need to admit
	// QuoteIdentifier output.
	rawExpr := fmt.Sprintf("%s(%s) AS agg", fn, q.driver.Grammar().QuoteIdentifier(column))

	selectQuery := &drivers.SelectQuery{
		Table:      q.table,
		RawColumns: []drivers.RawColumn{{Expr: rawExpr}},
		Conditions: q.conditions,
		Joins:      q.joins,
		Distinct:   q.distinct,
	}

	sqlStr, args := q.driver.Grammar().CompileSelect(selectQuery)

	var result sql.NullFloat64
	err := q.driver.QueryRowContext(ctx, sqlStr, args...).Scan(&result)
	if err != nil {
		return 0, err
	}

	if result.Valid {
		return result.Float64, nil
	}
	return 0, nil
}
