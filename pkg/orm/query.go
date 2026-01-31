package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/velocitykode/velocity/pkg/orm/drivers"
)

// Query represents a chainable query builder with generics
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

	// Context for event propagation
	ctx context.Context

	// Query state
	lastSQL  string
	lastArgs []any
}

// newQuery creates a new query builder for type T
func newQuery[T any]() *Query[T] {
	q := &Query[T]{
		driver:        getCurrentDriver(),
		table:         getTableName[T](),
		columns:       []string{"*"},
		hasSoftDelete: modelHasSoftDelete[T](),
	}
	return q
}

// modelHasSoftDelete checks if the model type T has a DeletedAt field (supports soft deletes)
func modelHasSoftDelete[T any]() bool {
	var model T
	t := reflect.TypeOf(model)

	// Handle pointer types
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Check all fields including embedded structs
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

// Where adds a WHERE condition
func (q *Query[T]) Where(condition string, args ...any) *Query[T] {
	parts := strings.SplitN(condition, " ", 3)
	if len(parts) < 2 {
		parts = append(parts, "=")
	}

	q.conditions = append(q.conditions, drivers.Condition{
		Column:   parts[0],
		Operator: parts[1],
		Value:    args[0],
		Type:     "and",
	})
	return q
}

// OrWhere adds an OR WHERE condition
func (q *Query[T]) OrWhere(condition string, args ...any) *Query[T] {
	parts := strings.SplitN(condition, " ", 3)
	if len(parts) < 2 {
		parts = append(parts, "=")
	}

	q.conditions = append(q.conditions, drivers.Condition{
		Column:   parts[0],
		Operator: parts[1],
		Value:    args[0],
		Type:     "or",
	})
	return q
}

// WhereIn adds a WHERE IN condition
func (q *Query[T]) WhereIn(field string, values []any) *Query[T] {
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
	q.conditions = append(q.conditions, drivers.Condition{
		Column:   field,
		Operator: "BETWEEN",
		Value:    []any{start, end},
		Type:     "and",
	})
	return q
}

// OrderBy adds an ORDER BY clause
func (q *Query[T]) OrderBy(column, direction string) *Query[T] {
	if direction == "" {
		direction = "ASC"
	}
	q.orders = append(q.orders, drivers.Order{
		Column:    column,
		Direction: strings.ToUpper(direction),
	})
	return q
}

// OrderByDesc adds an ORDER BY DESC clause
func (q *Query[T]) OrderByDesc(column string) *Query[T] {
	return q.OrderBy(column, "DESC")
}

// GroupBy adds a GROUP BY clause
func (q *Query[T]) GroupBy(columns ...string) *Query[T] {
	q.groups = append(q.groups, columns...)
	return q
}

// Having adds a HAVING condition
func (q *Query[T]) Having(condition string, args ...any) *Query[T] {
	parts := strings.SplitN(condition, " ", 3)
	if len(parts) < 2 {
		parts = append(parts, "=")
	}

	q.having = append(q.having, drivers.Condition{
		Column:   parts[0],
		Operator: parts[1],
		Value:    args[0],
		Type:     "and",
	})
	return q
}

// Join adds an INNER JOIN
func (q *Query[T]) Join(table, first, operator, second string) *Query[T] {
	q.joins = append(q.joins, drivers.Join{
		Type:  "INNER",
		Table: table,
		On:    fmt.Sprintf("%s %s %s", first, operator, second),
	})
	return q
}

// LeftJoin adds a LEFT JOIN
func (q *Query[T]) LeftJoin(table, first, operator, second string) *Query[T] {
	q.joins = append(q.joins, drivers.Join{
		Type:  "LEFT",
		Table: table,
		On:    fmt.Sprintf("%s %s %s", first, operator, second),
	})
	return q
}

// RightJoin adds a RIGHT JOIN
func (q *Query[T]) RightJoin(table, first, operator, second string) *Query[T] {
	q.joins = append(q.joins, drivers.Join{
		Type:  "RIGHT",
		Table: table,
		On:    fmt.Sprintf("%s %s %s", first, operator, second),
	})
	return q
}

// Select specifies columns to select
func (q *Query[T]) Select(columns ...string) *Query[T] {
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

// WithContext sets the context for the query (for event propagation)
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

// Get retrieves all matching records
func (q *Query[T]) Get() ([]T, error) {
	// Apply soft delete filtering only if model supports soft deletes
	if q.hasSoftDelete {
		if !q.withTrashed {
			q.WhereNull("deleted_at")
		} else if q.onlyTrashed {
			q.WhereNotNull("deleted_at")
		}
	}

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

	rows, err := q.driver.Query(sql, args...)

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

		// Mark as existing
		if m, ok := any(&model).(*Model[T]); ok {
			m.IsExisting = true
		}

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
func (q *Query[T]) Count() (int64, error) {
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
	err := q.driver.QueryRow(sql, args...).Scan(&count)
	dispatchQueryExecuted(q.getContext(), sql, args, time.Since(start), 1, q.driver.DriverName(), 2)

	return count, err
}

// Exists checks if any records match
func (q *Query[T]) Exists() bool {
	count, _ := q.Count()
	return count > 0
}

// Pluck retrieves values of a single column
func (q *Query[T]) Pluck(column string) ([]any, error) {
	q.Select(column)

	selectQuery := &drivers.SelectQuery{
		Table:      q.table,
		Columns:    q.columns,
		Conditions: q.conditions,
		Orders:     q.orders,
		Joins:      q.joins,
		Limit:      q.limit,
		Offset:     q.offset,
	}

	sql, args := q.driver.Grammar().CompileSelect(selectQuery)

	start := time.Now()
	rows, err := q.driver.Query(sql, args...)
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

// Update updates matching records
func (q *Query[T]) Update(updates map[string]any) (int64, error) {
	if len(updates) == 0 {
		return 0, errors.New("no updates provided")
	}

	// Add updated_at if model has it
	updates["updated_at"] = "NOW()"

	sql, args := q.driver.Grammar().CompileUpdate(q.table, updates, q.conditions)

	start := time.Now()
	result, err := q.driver.Exec(sql, args...)
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
	if len(data) == 0 {
		return 0, errors.New("no data provided for insert")
	}

	// Build the INSERT query
	columns := make([]string, 0, len(data))
	values := make([]any, 0, len(data))
	placeholders := make([]string, 0, len(data))

	i := 1
	for col, val := range data {
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
		result, err := q.driver.Exec(sql, values...)
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
		err := q.driver.QueryRow(sql, values...).Scan(&lastID)
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
	// Check if model has soft deletes
	if q.hasSoftDelete {
		// Soft delete
		return q.Update(map[string]any{
			"deleted_at": "NOW()",
		})
	}

	// Hard delete for models without soft delete support
	return q.ForceDelete()
}

// ForceDelete permanently deletes matching records
func (q *Query[T]) ForceDelete() (int64, error) {
	sql, args := q.driver.Grammar().CompileDelete(q.table, q.conditions)

	start := time.Now()
	result, err := q.driver.Exec(sql, args...)
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

func (q *Query[T]) loadRelations(models *[]T) error {
	// This would handle eager loading of relationships
	// Implementation depends on relationship definitions
	return nil
}

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

			// Skip fields marked with orm:"-"
			tag := field.Tag.Get("orm")
			if tag == "-" {
				continue
			}

			// Get the column name from the struct tag or field name
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
