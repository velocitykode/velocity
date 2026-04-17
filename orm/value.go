package orm

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/velocitykode/velocity/orm/drivers"
)

// Value retrieves a single column value from the first matching record.
// Returns ErrNotFound if no record matches.
func (q *Query[T]) Value(column string) (any, error) {
	if err := validateIdentifier(column); err != nil {
		return nil, fmt.Errorf("velocity/orm: value: %w", err)
	}

	q.columns = []string{column}
	one := 1
	q.limit = &one

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

	sqlStr, args := q.driver.Grammar().CompileSelect(selectQuery)

	start := time.Now()
	var result any
	err := q.driver.QueryRowContext(q.getContext(), sqlStr, args...).Scan(&result)
	dispatchQueryExecuted(q.getContext(), sqlStr, args, time.Since(start), 1, q.driver.DriverName(), 2)

	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}

	return result, nil
}
