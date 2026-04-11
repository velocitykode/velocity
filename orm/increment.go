package orm

import (
	"fmt"
	"strings"
	"time"
)

// --- Model[T] ---

// Increment atomically increments a column by amount (default 1) for all records of this type.
func (Model[T]) Increment(column string, amount ...int) error {
	return newQuery[T]().Increment(column, amount...)
}

// Decrement atomically decrements a column by amount (default 1) for all records of this type.
func (Model[T]) Decrement(column string, amount ...int) error {
	return newQuery[T]().Decrement(column, amount...)
}

// --- UUIDModel[T] ---

// Increment atomically increments a column by amount (default 1) for all records of this type.
func (UUIDModel[T]) Increment(column string, amount ...int) error {
	return newQuery[T]().Increment(column, amount...)
}

// Decrement atomically decrements a column by amount (default 1) for all records of this type.
func (UUIDModel[T]) Decrement(column string, amount ...int) error {
	return newQuery[T]().Decrement(column, amount...)
}

// --- SoftDeleteModel[T] ---

// Increment atomically increments a column by amount (default 1) for all records of this type.
func (SoftDeleteModel[T]) Increment(column string, amount ...int) error {
	return newQuery[T]().Increment(column, amount...)
}

// Decrement atomically decrements a column by amount (default 1) for all records of this type.
func (SoftDeleteModel[T]) Decrement(column string, amount ...int) error {
	return newQuery[T]().Decrement(column, amount...)
}

// --- SoftDeleteUUIDModel[T] ---

// Increment atomically increments a column by amount (default 1) for all records of this type.
func (SoftDeleteUUIDModel[T]) Increment(column string, amount ...int) error {
	return newQuery[T]().Increment(column, amount...)
}

// Decrement atomically decrements a column by amount (default 1) for all records of this type.
func (SoftDeleteUUIDModel[T]) Decrement(column string, amount ...int) error {
	return newQuery[T]().Decrement(column, amount...)
}

// --- Query[T] ---

// Increment atomically increments a column by amount (default 1) for matching records.
func (q *Query[T]) Increment(column string, amount ...int) error {
	return q.incrementOrDecrement(column, "+", amount...)
}

// Decrement atomically decrements a column by amount (default 1) for matching records.
func (q *Query[T]) Decrement(column string, amount ...int) error {
	return q.incrementOrDecrement(column, "-", amount...)
}

// incrementOrDecrement builds and executes an UPDATE SET col = col +/- ? query.
//
// It delegates the WHERE clause to the grammar's CompileDelete (which compiles
// conditions identically to CompileUpdate) to avoid hand-rolling placeholder
// logic that differs per driver (e.g. ? vs $N).
func (q *Query[T]) incrementOrDecrement(column, op string, amount ...int) error {
	if err := validateIdentifier(column); err != nil {
		return fmt.Errorf("Increment/Decrement: %w", err)
	}

	amt := 1
	if len(amount) > 0 {
		amt = amount[0]
	}

	grammar := q.driver.Grammar()
	quotedCol := grammar.QuoteIdentifier(column)
	quotedTable := grammar.QuoteIdentifier(q.table)

	// Use CompileDelete to get a grammar-correct WHERE clause with proper
	// placeholder numbering and quoting. The result is:
	//   "DELETE FROM table WHERE ..."  with args for conditions.
	// We strip everything before " WHERE " and re-number placeholders to
	// account for the leading amount arg.
	deleteSQL, condArgs := grammar.CompileDelete(q.table, q.conditions)

	// Build the SET clause: UPDATE table SET col = col +/- <placeholder>
	var sqlBuilder strings.Builder
	sqlBuilder.WriteString("UPDATE ")
	sqlBuilder.WriteString(quotedTable)
	sqlBuilder.WriteString(" SET ")
	sqlBuilder.WriteString(quotedCol)
	sqlBuilder.WriteString(" = ")
	sqlBuilder.WriteString(quotedCol)
	sqlBuilder.WriteString(" ")
	sqlBuilder.WriteString(op)
	sqlBuilder.WriteString(" ")
	sqlBuilder.WriteString(grammar.Placeholder(1))

	// Extract and append the WHERE clause from the compiled DELETE.
	if idx := strings.Index(deleteSQL, " WHERE "); idx >= 0 {
		whereClause := deleteSQL[idx:]
		// For Postgres ($N placeholders), re-number: $1 → $2, $2 → $3, etc.
		// since the amount parameter occupies $1.
		if q.driver.DriverName() == "postgres" {
			for i := len(condArgs); i >= 1; i-- {
				whereClause = strings.ReplaceAll(whereClause,
					fmt.Sprintf("$%d", i),
					fmt.Sprintf("$%d", i+1))
			}
		}
		sqlBuilder.WriteString(whereClause)
	}

	args := make([]any, 0, 1+len(condArgs))
	args = append(args, amt)
	args = append(args, condArgs...)

	sqlStr := sqlBuilder.String()

	start := time.Now()
	_, err := q.driver.Exec(sqlStr, args...)
	dispatchQueryExecuted(q.getContext(), sqlStr, args, time.Since(start), 0, q.driver.DriverName(), 2)

	return err
}
