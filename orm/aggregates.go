package orm

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/velocitykode/velocity/orm/drivers"
)

// Sum returns the sum of a column for matching records.
// Returns 0 for empty result sets.
func (q *Query[T]) Sum(column string) (float64, error) {
	return q.aggregate("SUM", column)
}

// Avg returns the average of a column for matching records.
// Returns 0 for empty result sets.
func (q *Query[T]) Avg(column string) (float64, error) {
	return q.aggregate("AVG", column)
}

// Min returns the minimum value of a column for matching records.
// Returns 0 for empty result sets.
func (q *Query[T]) Min(column string) (float64, error) {
	return q.aggregate("MIN", column)
}

// Max returns the maximum value of a column for matching records.
// Returns 0 for empty result sets.
func (q *Query[T]) Max(column string) (float64, error) {
	return q.aggregate("MAX", column)
}

// aggregate executes an aggregate function on a column and returns the result.
func (q *Query[T]) aggregate(fn, column string) (float64, error) {
	if err := validateIdentifier(column); err != nil {
		return 0, fmt.Errorf("%s: %w", fn, err)
	}

	q.columns = []string{fmt.Sprintf("%s(%s) as agg", fn, q.driver.Grammar().QuoteIdentifier(column))}

	selectQuery := &drivers.SelectQuery{
		Table:      q.table,
		Columns:    q.columns,
		Conditions: q.conditions,
		Joins:      q.joins,
		Distinct:   q.distinct,
	}

	sqlStr, args := q.driver.Grammar().CompileSelect(selectQuery)

	start := time.Now()
	var result sql.NullFloat64
	err := q.driver.QueryRowContext(q.getContext(), sqlStr, args...).Scan(&result)
	dispatchQueryExecuted(q.getContext(), sqlStr, args, time.Since(start), 1, q.driver.DriverName(), 2)

	if err != nil {
		return 0, err
	}

	if result.Valid {
		return result.Float64, nil
	}
	return 0, nil
}
