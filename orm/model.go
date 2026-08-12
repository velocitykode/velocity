package orm

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/velocitykode/velocity/orm/drivers"
)

// identifierRegex validates SQL column/field names
var identifierRegex = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.]*$`)

// validateIdentifier checks that a field name is a valid SQL identifier
func validateIdentifier(name string) error {
	if !identifierRegex.MatchString(name) {
		return fmt.Errorf("invalid identifier: %q", name)
	}
	return nil
}

// Model is a convenience composition: integer PK + Timestamps.
// Custom shapes that don't match this exactly should compose traits
// directly (orm.IDInt[T] + orm.Timestamps + ...) rather than embedding
// Model[T] - use orm.IsExisting / orm.Track / orm.IsDirty / orm.HasChanged for
// per-instance state inspection - those operate on the side-channel.
type Model[T any] struct {
	IDInt[T]
	Timestamps
}

// UUIDModel is a convenience composition: UUID PK + Timestamps.
type UUIDModel[T any] struct {
	IDUUID[T]
	Timestamps
}

// SoftDeleteModel is a convenience composition: integer PK + Timestamps + SoftDeletes.
type SoftDeleteModel[T any] struct {
	IDInt[T]
	Timestamps
	SoftDeletes[T]
}

// SoftDeleteUUIDModel is a convenience composition: UUID PK + Timestamps + SoftDeletes.
type SoftDeleteUUIDModel[T any] struct {
	IDUUID[T]
	Timestamps
	SoftDeletes[T]
}

// ImmutableModel is a convenience composition: integer PK + CreatedAtOnly + AppendOnly.
// Save returns ErrImmutableModelUpdate on a row that already exists.
type ImmutableModel[T any] struct {
	IDInt[T]
	CreatedAtOnly
	AppendOnly
}

// ImmutableUUIDModel is a convenience composition: UUID PK + CreatedAtOnly + AppendOnly.
type ImmutableUUIDModel[T any] struct {
	IDUUID[T]
	CreatedAtOnly
	AppendOnly
}

// Package-level generic helpers hold the single canonical implementation of
// each model operation. Every convenience-base variant method
// (Model/UUIDModel/SoftDeleteModel/SoftDeleteUUIDModel and the Immutable
// bases) is a one-line delegation to one of these, so each operation's logic
// is defined exactly once. Go cannot promote a generic method from an
// embedded base (every method needs T), so package-level funcs are the dedup
// vehicle; the variant methods exist only to populate each base's method set.

// modelFind retrieves a record by primary key. id is typed any so both the
// integer-key (id any) and UUID-key (id string) bases share it.
func modelFind[T any](ctx context.Context, id any) (*T, error) {
	var model T
	if err := newQuery[T]().Where("id = ?", id).First(ctx, &model); err != nil {
		return nil, err
	}
	return &model, nil
}

// modelFindBy retrieves a record by a specific field.
func modelFindBy[T any](ctx context.Context, field string, value any) (*T, error) {
	if err := validateIdentifier(field); err != nil {
		return nil, err
	}
	var model T
	if err := newQuery[T]().Where(field+" = ?", value).First(ctx, &model); err != nil {
		return nil, err
	}
	return &model, nil
}

// modelFirst retrieves the first record.
func modelFirst[T any](ctx context.Context) (*T, error) {
	var model T
	if err := newQuery[T]().First(ctx, &model); err != nil {
		return nil, err
	}
	return &model, nil
}

// modelLast retrieves the last record ordered by orderCol descending. Callers
// pass "id" for integer keys and lastOrderColumn[T]() for UUID keys (which
// are non-monotonic and order by created_at when timestamps are managed).
func modelLast[T any](ctx context.Context, orderCol string) (*T, error) {
	var model T
	if err := newQuery[T]().OrderBy(orderCol, "DESC").First(ctx, &model); err != nil {
		return nil, err
	}
	return &model, nil
}

// modelAll retrieves all records.
func modelAll[T any](ctx context.Context) ([]T, error) {
	return newQuery[T]().Get(ctx)
}

// modelWhere starts a query with a WHERE condition.
func modelWhere[T any](condition string, args ...any) *Query[T] {
	return newQuery[T]().Where(condition, args...)
}

// modelWhereIn queries for records where field is in the given values.
func modelWhereIn[T any](field string, values []any) *Query[T] {
	return newQuery[T]().WhereIn(field, values)
}

// modelWhereNull starts a query with a WHERE IS NULL condition.
func modelWhereNull[T any](field string) *Query[T] {
	return newQuery[T]().WhereNull(field)
}

// modelWhereNotNull starts a query with a WHERE IS NOT NULL condition.
func modelWhereNotNull[T any](field string) *Query[T] {
	return newQuery[T]().WhereNotNull(field)
}

// modelOrderBy starts a query with an ORDER BY clause.
func modelOrderBy[T any](column, direction string) *Query[T] {
	return newQuery[T]().OrderBy(column, direction)
}

// modelWith eager loads relationships.
func modelWith[T any](relations ...string) *Query[T] {
	return newQuery[T]().With(relations...)
}

// modelCreate inserts a new record from a map[string]any or a *T. A *T is run
// through applyAssignmentAccessToStruct so mass-assignment protection cannot be
// bypassed by callers who construct the struct manually.
func modelCreate[T any](ctx context.Context, data any) (*T, error) {
	switch v := data.(type) {
	case map[string]any:
		model := new(T)
		if err := mapToStruct(v, model); err != nil {
			return nil, err
		}
		if err := Save(ctx, nil, model); err != nil {
			return nil, err
		}
		return model, nil
	case *T:
		if err := applyAssignmentAccessToStruct(v); err != nil {
			return nil, err
		}
		if err := Save(ctx, nil, v); err != nil {
			return nil, err
		}
		return v, nil
	default:
		return nil, errors.New("unsupported data type for create")
	}
}

// modelCount returns the number of records.
func modelCount[T any](ctx context.Context) (int, error) {
	return newQuery[T]().Count(ctx)
}

// modelExists reports whether any records exist. A failed query returns
// (false, err) rather than silently reporting absence.
func modelExists[T any](ctx context.Context) (bool, error) {
	count, err := modelCount[T](ctx)
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// modelPaginate returns a paginated result for all records.
func modelPaginate[T any](ctx context.Context, page, perPage int) (*PaginatedResult[T], error) {
	return newQuery[T]().Paginate(ctx, page, perPage)
}

// modelPluck retrieves a single column's values.
func modelPluck[T any](ctx context.Context, column string) ([]any, error) {
	return newQuery[T]().Pluck(ctx, column)
}

// modelWhereConditions builds a query whose WHERE clause ANDs an equality
// predicate per conditions entry, validating each field as an identifier.
// Shared by the update/delete terminals below.
func modelWhereConditions[T any](conditions map[string]any) (*Query[T], error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return nil, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query, nil
}

// modelUpdate updates records matching conditions.
func modelUpdate[T any](ctx context.Context, conditions, updates map[string]any) (int64, error) {
	query, err := modelWhereConditions[T](conditions)
	if err != nil {
		return 0, err
	}
	return query.Update(ctx, updates)
}

// modelForceDeleteWhere permanently deletes records matching conditions. Used
// by the non-soft-delete bases' DeleteWhere and the soft-delete bases'
// ForceDeleteWhere.
func modelForceDeleteWhere[T any](ctx context.Context, conditions map[string]any) (int64, error) {
	query, err := modelWhereConditions[T](conditions)
	if err != nil {
		return 0, err
	}
	return query.ForceDelete(ctx)
}

// modelSoftDeleteWhere soft deletes records matching conditions. Used by the
// soft-delete bases' DeleteWhere.
func modelSoftDeleteWhere[T any](ctx context.Context, conditions map[string]any) (int64, error) {
	query, err := modelWhereConditions[T](conditions)
	if err != nil {
		return 0, err
	}
	return query.Delete(ctx)
}

// modelOnlyTrashed retrieves only soft deleted records.
func modelOnlyTrashed[T any]() *Query[T] {
	return newQuery[T]().OnlyTrashed()
}

// modelWithTrashed includes soft deleted records.
func modelWithTrashed[T any]() *Query[T] {
	return newQuery[T]().WithTrashed()
}

// Model[T] static methods

// Find retrieves a record by primary key. Takes ctx as the first
// argument so the read participates in the caller's transaction when
// ctx carries a *sql.Tx.
func (Model[T]) Find(ctx context.Context, id any) (*T, error) {
	return modelFind[T](ctx, id)
}

// FindBy retrieves a record by a specific field. Takes ctx as the first
// argument.
func (Model[T]) FindBy(ctx context.Context, field string, value any) (*T, error) {
	return modelFindBy[T](ctx, field, value)
}

// First retrieves the first record. Takes ctx as the first argument.
func (Model[T]) First(ctx context.Context) (*T, error) {
	return modelFirst[T](ctx)
}

// Last retrieves the last record (by id descending). Takes ctx as the
// first argument.
func (Model[T]) Last(ctx context.Context) (*T, error) {
	return modelLast[T](ctx, "id")
}

// All retrieves all records. Takes ctx as the first argument.
func (Model[T]) All(ctx context.Context) ([]T, error) {
	return modelAll[T](ctx)
}

// Where starts a query with a WHERE condition
func (Model[T]) Where(condition string, args ...any) *Query[T] {
	return modelWhere[T](condition, args...)
}

// WhereIn queries for records where field is in the given values
func (Model[T]) WhereIn(field string, values []any) *Query[T] {
	return modelWhereIn[T](field, values)
}

// WhereNull starts a query with a WHERE IS NULL condition
func (Model[T]) WhereNull(field string) *Query[T] {
	return modelWhereNull[T](field)
}

// WhereNotNull starts a query with a WHERE IS NOT NULL condition
func (Model[T]) WhereNotNull(field string) *Query[T] {
	return modelWhereNotNull[T](field)
}

// OrderBy starts a query with an ORDER BY clause
func (Model[T]) OrderBy(column, direction string) *Query[T] {
	return modelOrderBy[T](column, direction)
}

// With eager loads relationships
func (Model[T]) With(relations ...string) *Query[T] {
	return modelWith[T](relations...)
}

// Create inserts a new record. Takes ctx as the first argument so
// transaction enrollment is mandatory and explicit. Accepts a
// map[string]any or a *T. Resolves the driver from the package default
// Manager.
func (Model[T]) Create(ctx context.Context, data any) (*T, error) {
	return modelCreate[T](ctx, data)
}

// CreateMany inserts multiple records. Takes ctx as the first argument
// so the entire batch participates in the caller's transaction (when
// ctx carries a *sql.Tx) or routes through the pool driver.
func (Model[T]) CreateMany(ctx context.Context, records []T) error {
	return CreateMany[T](ctx, records)
}

// Count returns the number of records. Takes ctx as the first argument.
func (Model[T]) Count(ctx context.Context) (int, error) {
	return modelCount[T](ctx)
}

// Exists checks if any records exist. Takes ctx as the first argument.
// A failed query returns (false, err) rather than silently reporting
// absence.
func (Model[T]) Exists(ctx context.Context) (bool, error) {
	return modelExists[T](ctx)
}

// Paginate returns a paginated result for all records. Takes ctx as
// the first argument.
func (Model[T]) Paginate(ctx context.Context, page, perPage int) (*PaginatedResult[T], error) {
	return modelPaginate[T](ctx, page, perPage)
}

// Raw creates a raw SQL query builder for executing custom queries
// Usage: Model{}.Raw("SELECT * FROM users WHERE id = ?", 1).First(&user)
func (Model[T]) Raw(sql string, args ...any) *RawQuery[T] {
	return NewRawQuery[T](sql, args...)
}

// Pluck retrieves a single column values. Takes ctx as the first
// argument.
func (Model[T]) Pluck(ctx context.Context, column string) ([]any, error) {
	return modelPluck[T](ctx, column)
}

// Update updates records matching conditions. Takes ctx as the first
// argument so transaction enrollment is mandatory and explicit.
func (Model[T]) Update(ctx context.Context, conditions map[string]any, updates map[string]any) (int64, error) {
	return modelUpdate[T](ctx, conditions, updates)
}

// DeleteWhere permanently deletes records matching conditions. Takes
// ctx as the first argument so transaction enrollment is mandatory and
// explicit.
func (Model[T]) DeleteWhere(ctx context.Context, conditions map[string]any) (int64, error) {
	return modelForceDeleteWhere[T](ctx, conditions)
}

// UUIDModel static methods

// Find retrieves a record by UUID primary key. Takes ctx as the first
// argument.
func (UUIDModel[T]) Find(ctx context.Context, id string) (*T, error) {
	return modelFind[T](ctx, id)
}

// FindBy retrieves a record by a specific field. Takes ctx as the first
// argument.
func (UUIDModel[T]) FindBy(ctx context.Context, field string, value any) (*T, error) {
	return modelFindBy[T](ctx, field, value)
}

// First retrieves the first record. Takes ctx as the first argument.
func (UUIDModel[T]) First(ctx context.Context) (*T, error) {
	return modelFirst[T](ctx)
}

// Last retrieves the last record. UUID primary keys are non-monotonic, so
// "last" orders by created_at descending when the model manages timestamps.
// A model that opts out of timestamps (UsesTimestamps()==false) has no
// created_at column, so Last falls back to ordering by id to honor the
// opt-out contract that no read references a timestamp column. Takes ctx as
// the first argument.
func (UUIDModel[T]) Last(ctx context.Context) (*T, error) {
	return modelLast[T](ctx, lastOrderColumn[T]())
}

// All retrieves all records. Takes ctx as the first argument.
func (UUIDModel[T]) All(ctx context.Context) ([]T, error) {
	return modelAll[T](ctx)
}

// Where starts a query with a WHERE condition
func (UUIDModel[T]) Where(condition string, args ...any) *Query[T] {
	return modelWhere[T](condition, args...)
}

// WhereIn queries for records where field is in the given values
func (UUIDModel[T]) WhereIn(field string, values []any) *Query[T] {
	return modelWhereIn[T](field, values)
}

// WhereNull starts a query with a WHERE IS NULL condition
func (UUIDModel[T]) WhereNull(field string) *Query[T] {
	return modelWhereNull[T](field)
}

// WhereNotNull starts a query with a WHERE IS NOT NULL condition
func (UUIDModel[T]) WhereNotNull(field string) *Query[T] {
	return modelWhereNotNull[T](field)
}

// OrderBy starts a query with an ORDER BY clause
func (UUIDModel[T]) OrderBy(column, direction string) *Query[T] {
	return modelOrderBy[T](column, direction)
}

// With eager loads relationships
func (UUIDModel[T]) With(relations ...string) *Query[T] {
	return modelWith[T](relations...)
}

// Create inserts a new record. Takes ctx as the first argument so
// transaction enrollment is mandatory and explicit. Accepts a
// map[string]any or a *T.
func (UUIDModel[T]) Create(ctx context.Context, data any) (*T, error) {
	return modelCreate[T](ctx, data)
}

// CreateMany inserts multiple records. Takes ctx as the first argument
// so the entire batch participates in the caller's transaction.
func (UUIDModel[T]) CreateMany(ctx context.Context, records []T) error {
	return CreateMany[T](ctx, records)
}

// Count returns the number of records. Takes ctx as the first argument.
func (UUIDModel[T]) Count(ctx context.Context) (int, error) {
	return modelCount[T](ctx)
}

// Exists checks if any records exist. Takes ctx as the first argument.
// A failed query returns (false, err) rather than silently reporting
// absence.
func (UUIDModel[T]) Exists(ctx context.Context) (bool, error) {
	return modelExists[T](ctx)
}

// Paginate returns a paginated result for all records. Takes ctx as
// the first argument.
func (UUIDModel[T]) Paginate(ctx context.Context, page, perPage int) (*PaginatedResult[T], error) {
	return modelPaginate[T](ctx, page, perPage)
}

// Raw creates a raw SQL query builder for executing custom queries
// Usage: UUIDModel{}.Raw("SELECT * FROM users WHERE id = ?", 1).First(&user)
func (UUIDModel[T]) Raw(sql string, args ...any) *RawQuery[T] {
	return NewRawQuery[T](sql, args...)
}

// Pluck retrieves a single column values. Takes ctx as the first
// argument.
func (UUIDModel[T]) Pluck(ctx context.Context, column string) ([]any, error) {
	return modelPluck[T](ctx, column)
}

// Update updates records matching conditions. Takes ctx as the first
// argument so transaction enrollment is mandatory and explicit.
func (UUIDModel[T]) Update(ctx context.Context, conditions map[string]any, updates map[string]any) (int64, error) {
	return modelUpdate[T](ctx, conditions, updates)
}

// DeleteWhere permanently deletes records matching conditions. Takes
// ctx as the first argument so transaction enrollment is mandatory and
// explicit.
func (UUIDModel[T]) DeleteWhere(ctx context.Context, conditions map[string]any) (int64, error) {
	return modelForceDeleteWhere[T](ctx, conditions)
}

// SoftDeleteModel static methods

// Find retrieves a record by primary key. Takes ctx as the first
// argument.
func (SoftDeleteModel[T]) Find(ctx context.Context, id any) (*T, error) {
	return modelFind[T](ctx, id)
}

// FindBy retrieves a record by a specific field. Takes ctx as the first
// argument.
func (SoftDeleteModel[T]) FindBy(ctx context.Context, field string, value any) (*T, error) {
	return modelFindBy[T](ctx, field, value)
}

// First retrieves the first record. Takes ctx as the first argument.
func (SoftDeleteModel[T]) First(ctx context.Context) (*T, error) {
	return modelFirst[T](ctx)
}

// Last retrieves the last record (by id descending). Takes ctx as the
// first argument.
func (SoftDeleteModel[T]) Last(ctx context.Context) (*T, error) {
	return modelLast[T](ctx, "id")
}

// All retrieves all records. Takes ctx as the first argument.
func (SoftDeleteModel[T]) All(ctx context.Context) ([]T, error) {
	return modelAll[T](ctx)
}

// Where starts a query with a WHERE condition
func (SoftDeleteModel[T]) Where(condition string, args ...any) *Query[T] {
	return modelWhere[T](condition, args...)
}

// WhereIn queries for records where field is in the given values
func (SoftDeleteModel[T]) WhereIn(field string, values []any) *Query[T] {
	return modelWhereIn[T](field, values)
}

// WhereNull starts a query with a WHERE IS NULL condition
func (SoftDeleteModel[T]) WhereNull(field string) *Query[T] {
	return modelWhereNull[T](field)
}

// WhereNotNull starts a query with a WHERE IS NOT NULL condition
func (SoftDeleteModel[T]) WhereNotNull(field string) *Query[T] {
	return modelWhereNotNull[T](field)
}

// OrderBy starts a query with an ORDER BY clause
func (SoftDeleteModel[T]) OrderBy(column, direction string) *Query[T] {
	return modelOrderBy[T](column, direction)
}

// With eager loads relationships
func (SoftDeleteModel[T]) With(relations ...string) *Query[T] {
	return modelWith[T](relations...)
}

// Create inserts a new record. Takes ctx as the first argument so
// transaction enrollment is mandatory and explicit.
func (SoftDeleteModel[T]) Create(ctx context.Context, data any) (*T, error) {
	return modelCreate[T](ctx, data)
}

// CreateMany inserts multiple records. Takes ctx as the first argument.
func (SoftDeleteModel[T]) CreateMany(ctx context.Context, records []T) error {
	return CreateMany[T](ctx, records)
}

// Count returns the number of records. Takes ctx as the first argument.
func (SoftDeleteModel[T]) Count(ctx context.Context) (int, error) {
	return modelCount[T](ctx)
}

// Exists checks if any records exist. Takes ctx as the first argument.
// A failed query returns (false, err) rather than silently reporting
// absence.
func (SoftDeleteModel[T]) Exists(ctx context.Context) (bool, error) {
	return modelExists[T](ctx)
}

// Paginate returns a paginated result for all records. Takes ctx as
// the first argument.
func (SoftDeleteModel[T]) Paginate(ctx context.Context, page, perPage int) (*PaginatedResult[T], error) {
	return modelPaginate[T](ctx, page, perPage)
}

// Raw creates a raw SQL query builder for executing custom queries
// Usage: SoftDeleteModel{}.Raw("SELECT * FROM users WHERE id = ?", 1).First(&user)
func (SoftDeleteModel[T]) Raw(sql string, args ...any) *RawQuery[T] {
	return NewRawQuery[T](sql, args...)
}

// Pluck retrieves a single column values. Takes ctx as the first
// argument.
func (SoftDeleteModel[T]) Pluck(ctx context.Context, column string) ([]any, error) {
	return modelPluck[T](ctx, column)
}

// Update updates records matching conditions. Takes ctx as the first
// argument so transaction enrollment is mandatory and explicit.
func (SoftDeleteModel[T]) Update(ctx context.Context, conditions map[string]any, updates map[string]any) (int64, error) {
	return modelUpdate[T](ctx, conditions, updates)
}

// DeleteWhere soft deletes records matching conditions. Takes ctx as
// the first argument so transaction enrollment is mandatory and
// explicit.
func (SoftDeleteModel[T]) DeleteWhere(ctx context.Context, conditions map[string]any) (int64, error) {
	return modelSoftDeleteWhere[T](ctx, conditions)
}

// ForceDeleteWhere permanently deletes records matching conditions.
// Takes ctx as the first argument so transaction enrollment is
// mandatory and explicit.
func (SoftDeleteModel[T]) ForceDeleteWhere(ctx context.Context, conditions map[string]any) (int64, error) {
	return modelForceDeleteWhere[T](ctx, conditions)
}

// OnlyTrashed retrieves only soft deleted records
func (SoftDeleteModel[T]) OnlyTrashed() *Query[T] {
	return modelOnlyTrashed[T]()
}

// WithTrashed includes soft deleted records
func (SoftDeleteModel[T]) WithTrashed() *Query[T] {
	return modelWithTrashed[T]()
}

// SoftDeleteUUIDModel static methods

// Find retrieves a record by UUID primary key. Takes ctx as the first
// argument.
func (SoftDeleteUUIDModel[T]) Find(ctx context.Context, id string) (*T, error) {
	return modelFind[T](ctx, id)
}

// FindBy retrieves a record by a specific field. Takes ctx as the first
// argument.
func (SoftDeleteUUIDModel[T]) FindBy(ctx context.Context, field string, value any) (*T, error) {
	return modelFindBy[T](ctx, field, value)
}

// First retrieves the first record. Takes ctx as the first argument.
func (SoftDeleteUUIDModel[T]) First(ctx context.Context) (*T, error) {
	return modelFirst[T](ctx)
}

// Last retrieves the last record. As with UUIDModel, ordering is by
// created_at descending when timestamps are managed, falling back to id when
// the model opted out of timestamps so no read references a missing column.
// Takes ctx as the first argument.
func (SoftDeleteUUIDModel[T]) Last(ctx context.Context) (*T, error) {
	return modelLast[T](ctx, lastOrderColumn[T]())
}

// All retrieves all records. Takes ctx as the first argument.
func (SoftDeleteUUIDModel[T]) All(ctx context.Context) ([]T, error) {
	return modelAll[T](ctx)
}

// Where starts a query with a WHERE condition
func (SoftDeleteUUIDModel[T]) Where(condition string, args ...any) *Query[T] {
	return modelWhere[T](condition, args...)
}

// WhereIn queries for records where field is in the given values
func (SoftDeleteUUIDModel[T]) WhereIn(field string, values []any) *Query[T] {
	return modelWhereIn[T](field, values)
}

// WhereNull starts a query with a WHERE IS NULL condition
func (SoftDeleteUUIDModel[T]) WhereNull(field string) *Query[T] {
	return modelWhereNull[T](field)
}

// WhereNotNull starts a query with a WHERE IS NOT NULL condition
func (SoftDeleteUUIDModel[T]) WhereNotNull(field string) *Query[T] {
	return modelWhereNotNull[T](field)
}

// OrderBy starts a query with an ORDER BY clause
func (SoftDeleteUUIDModel[T]) OrderBy(column, direction string) *Query[T] {
	return modelOrderBy[T](column, direction)
}

// With eager loads relationships
func (SoftDeleteUUIDModel[T]) With(relations ...string) *Query[T] {
	return modelWith[T](relations...)
}

// Create inserts a new record. Takes ctx as the first argument so
// transaction enrollment is mandatory and explicit.
func (SoftDeleteUUIDModel[T]) Create(ctx context.Context, data any) (*T, error) {
	return modelCreate[T](ctx, data)
}

// CreateMany inserts multiple records. Takes ctx as the first argument.
func (SoftDeleteUUIDModel[T]) CreateMany(ctx context.Context, records []T) error {
	return CreateMany[T](ctx, records)
}

// Count returns the number of records. Takes ctx as the first argument.
func (SoftDeleteUUIDModel[T]) Count(ctx context.Context) (int, error) {
	return modelCount[T](ctx)
}

// Exists checks if any records exist. Takes ctx as the first argument.
// A failed query returns (false, err) rather than silently reporting
// absence.
func (SoftDeleteUUIDModel[T]) Exists(ctx context.Context) (bool, error) {
	return modelExists[T](ctx)
}

// Paginate returns a paginated result for all records. Takes ctx as
// the first argument.
func (SoftDeleteUUIDModel[T]) Paginate(ctx context.Context, page, perPage int) (*PaginatedResult[T], error) {
	return modelPaginate[T](ctx, page, perPage)
}

// Raw creates a raw SQL query builder for executing custom queries
// Usage: SoftDeleteUUIDModel{}.Raw("SELECT * FROM users WHERE id = ?", 1).First(&user)
func (SoftDeleteUUIDModel[T]) Raw(sql string, args ...any) *RawQuery[T] {
	return NewRawQuery[T](sql, args...)
}

// Pluck retrieves a single column values. Takes ctx as the first
// argument.
func (SoftDeleteUUIDModel[T]) Pluck(ctx context.Context, column string) ([]any, error) {
	return modelPluck[T](ctx, column)
}

// Update updates records matching conditions. Takes ctx as the first
// argument so transaction enrollment is mandatory and explicit.
func (SoftDeleteUUIDModel[T]) Update(ctx context.Context, conditions map[string]any, updates map[string]any) (int64, error) {
	return modelUpdate[T](ctx, conditions, updates)
}

// DeleteWhere soft deletes records matching conditions. Takes ctx as
// the first argument.
func (SoftDeleteUUIDModel[T]) DeleteWhere(ctx context.Context, conditions map[string]any) (int64, error) {
	return modelSoftDeleteWhere[T](ctx, conditions)
}

// ForceDeleteWhere permanently deletes records matching conditions.
// Takes ctx as the first argument.
func (SoftDeleteUUIDModel[T]) ForceDeleteWhere(ctx context.Context, conditions map[string]any) (int64, error) {
	return modelForceDeleteWhere[T](ctx, conditions)
}

// OnlyTrashed retrieves only soft deleted records
func (SoftDeleteUUIDModel[T]) OnlyTrashed() *Query[T] {
	return modelOnlyTrashed[T]()
}

// WithTrashed includes soft deleted records
func (SoftDeleteUUIDModel[T]) WithTrashed() *Query[T] {
	return modelWithTrashed[T]()
}

// UUIDModel private methods

// Private methods

// Hooks interfaces

type BeforeCreateHook interface {
	BeforeCreate() error
}

type AfterCreateHook interface {
	AfterCreate() error
}

type BeforeUpdateHook interface {
	BeforeUpdate() error
}

type AfterUpdateHook interface {
	AfterUpdate() error
}

type BeforeDeleteHook interface {
	BeforeDelete() error
}

type AfterDeleteHook interface {
	AfterDelete() error
}

// Assignable interface allows models to specify which fields can be
// mass-assigned
type Assignable interface {
	AssignableFields() []string
}

// Protected interface allows models to specify which fields are protected
// from mass-assignment
type Protected interface {
	ProtectedFields() []string
}

// Helper functions

// applyAssignmentAccessToStruct zeros out any protected fields and any
// fields not in the assignable allowlist before the struct is persisted.
// This mirrors the enforcement performed by mapToStruct so Create(*T) and
// Create(map) share the same mass-assignment policy.
//
// Fields managed by the framework itself (ID, timestamps, embedded Model
// bookkeeping) are always left intact - the policy only governs fields the
// application explicitly manages.
//
// Resolves columns and policy through the canonical ModelMeta +
// AssignmentAccess so the protection rules match mapToStruct exactly,
// regardless of which entry point the caller used. Embedded base columns
// are always preserved, even when the model declares an assignable
// allowlist that omits them.
func applyAssignmentAccessToStruct(s any) error {
	policy := PolicyFor(s)
	if policy.implicitDeny {
		// No declared policy. Deny-by-default applies only to map-based
		// writes; a *T the caller constructed field-by-field in code is
		// not attacker-shaped input, so it persists untouched.
		return nil
	}
	if !policy.HasAssignable && !policy.HasProtected {
		return nil
	}

	v := reflect.ValueOf(s)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	meta := MetaForValue(v)
	if meta == nil {
		return nil
	}

	for _, col := range meta.Columns() {
		// Embedded base columns are framework-managed; never blank.
		if col.FromEmbedded {
			continue
		}
		if policy.Allows(col.FieldNameKey) {
			continue
		}
		fv := v.FieldByIndex(col.IndexPath)
		if fv.CanSet() {
			fv.Set(reflect.Zero(fv.Type()))
		}
	}
	return nil
}

// fieldColumnName is the legacy resolver kept as a thin wrapper over
// the canonical reflection resolver so external callers (and any test
// fixtures pinned to the old name) keep compiling. Returns "" for
// orm:"-" or for relation/m2m/morph fields, otherwise honors
// `orm:"column:..."` and falls back to snake_case of the Go field name.
//
// New code should reach the resolver directly via MetaFor(t).ColumnFor(name).
func fieldColumnName(field reflect.StructField) string {
	tag := field.Tag.Get("orm")
	if tag == "-" {
		return ""
	}
	if strings.Contains(tag, "relation:") {
		return ""
	}
	if extractManyToManyValue(tag) != "" {
		return ""
	}
	if extractPolymorphicValue(tag) != "" {
		return ""
	}
	if name := columnNameFromTag(tag); name != "" {
		return name
	}
	return ToSnakeCase(field.Name)
}

// mapToStruct hydrates a model struct from a column-to-value map. Resolves
// every column through the canonical ModelMeta so the read path is
// guaranteed symmetric with the write path (structToMap) and with every
// other reflection callsite. Mass-assignment policy is enforced via
// AssignmentAccess keyed on the snake_case'd Go field name, so attackers
// cannot bypass the policy by submitting the column-tag value instead.
//
// Deny-by-default: a model that declares neither AssignableFields() nor
// ProtectedFields() (and does not opt out via AllowAllColumns) rejects the
// write with a *MassAssignmentError naming the model and the offending
// keys, rather than silently skipping or - worse - writing them. Models
// with a declared policy keep the established semantics: disallowed keys
// are silently skipped.
//
// Embedded base columns (id, created_at, updated_at, deleted_at) bypass
// the assignment-access check by design: they are framework-managed and
// users never key policy on them.
func mapToStruct(m map[string]any, s any) error {
	v := reflect.ValueOf(s)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	meta := MetaForValue(v)
	if meta == nil {
		return nil
	}
	policy := PolicyFor(s)
	if policy.implicitDeny {
		// No declared policy: reject every map key that resolves to an
		// application column. Collected before any write so the caller
		// never observes a partially hydrated model alongside the error.
		// Matching reuses deniedUpdateKeys' case-insensitive column-or-
		// field resolution: an exact-match check alone would treat
		// "IS_ADMIN", or the snake-cased field name of a column-tag
		// aliased field, as having no offending key and proceed.
		if denied := deniedUpdateKeys(m, meta); len(denied) > 0 {
			return &MassAssignmentError{Model: v.Type().String(), Keys: denied}
		}
	}

	for _, col := range meta.Columns() {
		val, ok := m[col.Column]
		if !ok {
			continue
		}
		if !col.FromEmbedded && !policy.Allows(col.FieldNameKey) {
			continue
		}

		fieldValue := v.FieldByIndex(col.IndexPath)
		if !fieldValue.CanSet() {
			continue
		}
		valReflect := reflect.ValueOf(val)
		if !valReflect.IsValid() {
			continue
		}

		// Pointer field receiving a scalar: allocate and set the elem.
		if fieldValue.Kind() == reflect.Ptr && valReflect.Kind() != reflect.Ptr {
			if valReflect.Type().ConvertibleTo(fieldValue.Type().Elem()) {
				ptr := reflect.New(fieldValue.Type().Elem())
				ptr.Elem().Set(valReflect.Convert(fieldValue.Type().Elem()))
				fieldValue.Set(ptr)
			}
			continue
		}
		if valReflect.Type().ConvertibleTo(fieldValue.Type()) {
			fieldValue.Set(valReflect.Convert(fieldValue.Type()))
		}
	}
	return nil
}

// ormTagType extracts the "type:" value from an orm struct tag, splitting on
// ';' so substrings inside other directives don't trigger false matches.
// Returns "" when no type directive is present.
func ormTagType(tag string) string {
	if tag == "" {
		return ""
	}
	for _, part := range strings.Split(tag, ";") {
		if strings.HasPrefix(part, "type:") {
			return strings.TrimPrefix(part, "type:")
		}
	}
	return ""
}

// isJSONColumn reports whether an orm tag declares the column as a JSON or
// JSONB type. Exact match on the type directive, so variants like
// "jsonb_packed" do not qualify.
func isJSONColumn(tag string) bool {
	t := ormTagType(tag)
	return t == "json" || t == "jsonb"
}

// isJSONZeroValue reports whether v should be treated as "absent" for a
// JSON/JSONB column. Empty Go strings are never valid JSON on Postgres/MySQL
// and are silently invalid on SQLite, so the framework omits them so the DB
// default (or NOT NULL constraint) can apply.
//
// Per-kind rules:
//   - string: "" is zero
//   - pointer / interface: nil is zero
//   - []byte: nil OR len == 0 is zero
//   - other slices: only nil is zero (empty slice = explicit user intent)
//   - map: only nil is zero (empty map = explicit user intent, '{}')
func isJSONZeroValue(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.String:
		return v.Len() == 0
	case reflect.Ptr, reflect.Interface:
		return v.IsNil()
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return v.IsNil() || v.Len() == 0
		}
		return v.IsNil()
	case reflect.Map:
		return v.IsNil()
	}
	return false
}

// structToMap converts a model struct into a column-to-value map ready for
// driver insert/update. Embedded ORM base types (Model, UUIDModel and their
// soft-delete variants) are expanded via serializeEmbedded so each branch
// stays focused on a single responsibility.
func structToMap(s any) map[string]any {
	result := make(map[string]any)
	v := reflect.ValueOf(s)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	t := v.Type()

	// Polymorphic morph fields emit a (type_col, id_col) pair sourced
	// from the Morph struct value, not the field itself. ModelMeta
	// excludes them from Columns(), so handle here before the canonical
	// loop. The emission rule is the same in both directions: TypeName
	// when non-empty, ID when not the zero key.
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("orm")
		pv := extractPolymorphicValue(tag)
		if pv == "" {
			continue
		}
		typeCol, idCol, perr := parsePolymorphicTag(pv)
		if perr != nil {
			continue
		}
		fv := v.Field(i)
		if fv.Kind() != reflect.Struct {
			continue
		}
		if tn := fv.FieldByName("TypeName"); tn.IsValid() && tn.Kind() == reflect.String && tn.String() != "" {
			result[typeCol] = tn.String()
		}
		if id := fv.FieldByName("ID"); id.IsValid() && id.CanInterface() {
			idVal := id.Interface()
			if idVal != nil && !isZeroKey(normalizeKey(idVal)) {
				result[idCol] = idVal
			}
		}
	}

	meta := MetaForValue(v)
	if meta == nil {
		return result
	}

	for _, col := range meta.Columns() {
		// Read-only columns (e.g. a SelectDistance score) are hydrated on
		// read but never persisted: skip them on every write path.
		if col.ReadOnly {
			continue
		}

		fv := v.FieldByIndex(col.IndexPath)
		if !fv.IsValid() {
			continue
		}

		// Slice/array gating. Non-byte slices are relation payloads
		// and never persist; byte slices/arrays are scalars (bytea,
		// hashes) unless tagged JSON. A nil byte slice on a non-JSON
		// column is dropped so the DB default applies.
		//
		// A non-byte slice whose type knows how to serialize itself for
		// the driver (driver.Valuer, e.g. orm.Vector) is a scalar DB
		// value, not a relation payload, so it must be emitted: the
		// driver calls Value() at bind time. Relation slices ([]Model)
		// are not Valuers and stay dropped.
		switch fv.Kind() {
		case reflect.Slice, reflect.Array:
			isByteSeq := fv.Type().Elem().Kind() == reflect.Uint8
			_, isValuer := fv.Interface().(driver.Valuer)
			if !col.IsJSON && !isByteSeq && !isValuer {
				continue
			}
			if isByteSeq && !col.IsJSON && fv.Kind() == reflect.Slice && fv.IsNil() {
				continue
			}
		}

		// JSON zero-value: omit so the DB default can apply. "" is
		// never valid JSON on Postgres/MySQL; SQLite would store it
		// as literal text and break downstream json.Unmarshal.
		if col.IsJSON && isJSONZeroValue(fv) {
			continue
		}

		// Auto-managed primary key: skip when not yet assigned so the
		// driver's auto-increment or UUID hook can run on insert. The
		// save path also explicitly delete()s "id" before update, so
		// this rule covers the insert side for both Model[T] (uint)
		// and UUIDModel[T] (string).
		if col.FieldName == "ID" {
			switch fv.Kind() {
			case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
				if fv.Uint() == 0 {
					continue
				}
			case reflect.String:
				if fv.String() == "" {
					continue
				}
			}
		}

		// Pointer fields (notably DeletedAt *time.Time) become NULL
		// when nil; emit only when set so the DB default sticks and
		// soft-delete queries keep working.
		if fv.Kind() == reflect.Ptr && fv.IsNil() {
			continue
		}

		result[col.Column] = fv.Interface()
	}

	return result
}

// Global convenience functions

// Save persists model through manager m. The first argument is a
// context.Context: when ctx carries a *sql.Tx (set by
// Manager.Transaction or WithTxContext) the write enrolls in the
// caller's transaction; otherwise it routes through the manager's
// pool driver. Passing nil for m falls back to the package default
// Manager (SetDefault).
//
// Forgetting ctx is a compile error - the only way to get an unscoped
// auto-commit is to explicitly pass context.Background() or a ctx
// that does not carry a *sql.Tx.
//
// When the model already exists, Save issues an UPDATE targeting the
// row by primary key. The auto-installed soft-delete scope is skipped
// (saving an instance you hold is an explicit by-PK write, so it
// updates a trashed row too, regardless of scope registration order),
// but every other registered global scope (tenant, archive, locale,
// ...) still applies, so a by-PK Save cannot mutate rows outside the
// caller's scope set. Bulk updates via Query.Update respect all
// global scopes including soft-delete.
func Save[T any](ctx context.Context, m *Manager, model *T) error {
	if m == nil {
		m = Default()
	}
	if m == nil {
		return errors.New("orm: no default manager set - call SetDefault or pass a *Manager")
	}
	drv, err := m.liveDriver()
	if err != nil {
		return err
	}
	// Honor a tx slot in ctx so the manager-routed Save and
	// Query[T].Save behave identically: both enroll in the tx when ctx
	// carries one, both route through the pool when it does not.
	if tx, ok := TxFromContext(ctx); ok {
		drv = &txDriver{Driver: drv, tx: tx}
	}
	// Stamp the Manager's TxRecover dispatcher onto ctx so an inline
	// AfterCommit-hook panic surfaces a TxRecover event identical to
	// the in-Transaction path. Without this, a panic in an
	// AfterCommit hook fired through the auto-commit branch would
	// only land on os.Stderr.
	ctx = withTxRecoverDispatcher(ctx, func(ev *TxRecover) { m.dispatchEvent(ctx, ev) })
	return saveWithDriver(ctx, drv, model)
}

// saveWithDriver is the driver-bound persistence entry. It carries the
// reflection + dispatch logic of Save; the public Save resolves the
// driver from a *Manager, while Query.Save / Query.Create reach this
// helper directly using their own (possibly tx-bound) q.driver.
//
// Dispatch routes by trait fingerprint (composition.go), not by
// type-name prefix, so any composition of orm.IDInt[T] / orm.IDUUID[T]
// + Timestamps / CreatedAtOnly + SoftDeletes / AppendOnly + Existence
// participates correctly.
func saveWithDriver[T any](ctx context.Context, drv drivers.Driver, model *T) error {
	v := reflect.ValueOf(model).Elem()
	t := v.Type()

	feats, err := featuresFor(t)
	if err != nil {
		return err
	}
	if !feats.hasPK() {
		return errors.New("orm: model does not embed a primary-key trait (orm.IDInt[T] or orm.IDUUID[T]); compose one of the canonical bases (Model/UUIDModel/SoftDeleteModel/SoftDeleteUUIDModel/ImmutableModel/ImmutableUUIDModel) or embed the trait directly")
	}

	// modelField points at the embedded sub-struct that holds ID and
	// (when present) the Existence trait. With the trait fingerprint we
	// can locate the PK trait directly: walk anonymous embedded fields
	// looking for the one whose first field is the PK sentinel. The
	// outer-struct's reflect.Value.FieldByName resolves promoted fields
	// (ID / IsExisting) recursively, so we just need a reflect.Value
	// rooted at the trait composition wrapper; the simplest correct
	// pick is v itself (the outer struct) - FieldByName will reach
	// through any nesting depth.
	modelField := v

	tableName := deriveTableName(t)

	// Resolve the ID/timestamp fields through the cached ModelMeta IndexPath
	// rather than reflective FieldByName lookups on every Save, but keyed on
	// the exact Go field names the legacy FieldByName path touched - "ID",
	// "CreatedAt", "UpdatedAt" - NOT on the autoCreateTime/autoUpdateTime role
	// flags. Keying on the role flags would stamp/mutate a differently-named
	// tagged field (e.g. `TouchedAt time.Time `orm:"autoUpdateTime"``) that the
	// old path skipped, and could panic if such a field is not time.Time. The
	// cached IndexPath still walks promoted/embedded fields exactly as
	// FieldByName did, so this stays an O(1) lookup with identical reach. The
	// role flag is asserted only as a guard; the FieldByName fallback is off
	// the hot path and only covers a degenerate shape with no flagged PK.
	meta := MetaFor(t)

	var idField reflect.Value
	if pk, ok := meta.ColumnByField("ID"); ok && pk.IsPrimaryKey {
		idField = modelField.FieldByIndex(pk.IndexPath)
	} else {
		idField = modelField.FieldByName("ID")
	}
	isInsert := !isModelExisting(model)

	// AppendOnly: an existing-row Save is rejected outright. The
	// tombstone update path on AppendOnly+SoftDeletes goes through
	// Query.Update(deleted_at=...), not through Save - that path
	// performs its own gating in query.go.
	if feats.appendOnly && !isInsert {
		return ErrImmutableModelUpdate
	}

	// skipTimestamps is set when the model opted out via UsesTimestamps().
	// featuresFor has already forced hasCreatedAt/hasUpdatedAt false; this
	// bool additionally suppresses the in-memory CreatedAt/UpdatedAt field
	// stamping so an opted-out model never carries a spurious timestamp for
	// a column that does not exist.
	skipTimestamps := feats.timestampsOptOut

	var saveErr error
	switch {
	case feats.appendOnly && feats.hasUUIDPK:
		saveErr = saveCore(ctx, drv, model, meta, modelField, idField, tableName, isInsert, skipTimestamps, saveOpts{pk: pkUUID, appendOnly: true})
	case feats.appendOnly:
		saveErr = saveCore(ctx, drv, model, meta, modelField, idField, tableName, isInsert, skipTimestamps, saveOpts{pk: pkInt, appendOnly: true})
	case feats.hasUUIDPK:
		saveErr = saveCore(ctx, drv, model, meta, modelField, idField, tableName, isInsert, skipTimestamps, saveOpts{pk: pkUUID})
	default:
		saveErr = saveCore(ctx, drv, model, meta, modelField, idField, tableName, isInsert, skipTimestamps, saveOpts{pk: pkInt})
	}
	if saveErr == nil {
		// Wire model AfterCommit / AfterRollback hooks against the
		// surrounding TxCallbacks list when there is one; outside a
		// Transaction the AfterCommit hook fires inline because the
		// auto-commit already happened.
		registerModelAfterCommit(ctx, model)
	}
	return saveErr
}

// pkMode selects how saveCore generates and persists the primary key.
type pkMode int

const (
	pkInt  pkMode = iota // auto-increment integer PK: InsertGetId + write the id back
	pkUUID               // UUID PK: generate when unset, plain INSERT, no write-back
)

// saveOpts parameterizes saveCore over the deltas between the model bases:
// the primary-key strategy and whether the base is append-only (immutable).
type saveOpts struct {
	pk         pkMode
	appendOnly bool
}

// saveCore is the single canonical insert/update implementation behind every
// model base. saveWithDriver dispatches into it with the saveOpts that
// describe the base's PK strategy and mutability. Behavior is identical to
// the former per-base saveModel / saveUUIDModel / saveImmutableModel /
// saveImmutableUUIDModel: hook order (BeforeCreate/AfterCreate on insert,
// BeforeUpdate/AfterUpdate on update), timestamp stamping rules,
// skipTimestamps, the by-PK global-scope bypass on update, and existence
// marking are all preserved.
func saveCore[T any](ctx context.Context, drv drivers.Driver, model *T, meta *ModelMeta, modelField, idField reflect.Value, tableName string, isInsert, skipTimestamps bool, opts saveOpts) error {
	// AppendOnly: an existing-row Save is rejected outright. saveWithDriver
	// already gates this before dispatch; the check is repeated here so the
	// invariant travels with the implementation.
	if opts.appendOnly && !isInsert {
		return ErrImmutableModelUpdate
	}

	if isInsert {
		// UUID PK: generate when the caller left it unset.
		if opts.pk == pkUUID && idField.String() == "" {
			idField.SetString(uuid.New().String())
		}

		// Set timestamps: respect caller-set CreatedAt; only stamp when zero.
		// For mutable bases UpdatedAt mirrors CreatedAt on insert; append-only
		// bases (CreatedAtOnly) never stamp UpdatedAt, matching the former
		// saveImmutable* paths even for a custom AppendOnly+Timestamps shape.
		// Each field is gated on validity since compositions may omit either.
		// Skipped entirely when the model opted out of timestamps (the
		// columns are not in the table).
		// Stamping is keyed off the persistent column metadata, so a field
		// named CreatedAt/UpdatedAt that is explicitly non-persistent
		// (tagged orm:"-") is intentionally not stamped: a column the user
		// excluded should not be mutated. (The pre-cache path stamped such
		// shadow fields in-memory via FieldByName even though they were
		// never persisted; this is a deliberate, more-correct divergence.)
		if !skipTimestamps {
			var createdAt time.Time
			if col, ok := meta.ColumnByField("CreatedAt"); ok && col.IsCreatedAt {
				createdAtField := modelField.FieldByIndex(col.IndexPath)
				createdAt = createdAtField.Interface().(time.Time)
				if createdAt.IsZero() {
					// Stamps are UTC so the stored wall clock never
					// depends on the writer's process timezone.
					createdAt = time.Now().UTC()
					createdAtField.Set(reflect.ValueOf(createdAt))
				}
			}
			if !opts.appendOnly {
				if col, ok := meta.ColumnByField("UpdatedAt"); ok && col.IsUpdatedAt {
					updatedAtField := modelField.FieldByIndex(col.IndexPath)
					if updatedAtField.Interface().(time.Time).IsZero() {
						if createdAt.IsZero() {
							createdAt = time.Now().UTC()
						}
						updatedAtField.Set(reflect.ValueOf(createdAt))
					}
				}
			}
		}

		// Call BeforeCreate hook if exists
		if hook, ok := any(model).(BeforeCreateHook); ok {
			if err := hook.BeforeCreate(); err != nil {
				return err
			}
		}

		// Convert to map and insert.
		data := structToMap(model)
		// Integer PK: drop "id" so the driver's auto-increment runs. UUID PK
		// keeps "id" in data: it is already set, so the plain INSERT carries
		// it with no generated-id retrieval.
		if opts.pk == pkInt {
			delete(data, "id")
		}

		query := &Query[T]{
			table:  tableName,
			driver: drv,
		}

		if opts.pk == pkInt {
			lastID, err := query.InsertGetId(ctx, data)
			if err != nil {
				return err
			}
			idField.SetUint(uint64(lastID))
		} else {
			if err := query.insertExec(ctx, data); err != nil {
				return err
			}
		}

		markModelExisting(model)

		// Call AfterCreate hook if exists
		if hook, ok := any(model).(AfterCreateHook); ok {
			if err := hook.AfterCreate(); err != nil {
				return err
			}
		}
		return nil
	}

	// Update existing record. UpdatedAt is optional (CreatedAtOnly without
	// AppendOnly composes here too) and is skipped when the model opted out
	// of timestamps.
	if !skipTimestamps {
		if col, ok := meta.ColumnByField("UpdatedAt"); ok && col.IsUpdatedAt {
			modelField.FieldByIndex(col.IndexPath).Set(reflect.ValueOf(time.Now().UTC()))
		}
	}

	// Call BeforeUpdate hook if exists
	if hook, ok := any(model).(BeforeUpdateHook); ok {
		if err := hook.BeforeUpdate(); err != nil {
			return err
		}
	}

	// Convert to map and update
	data := structToMap(model)
	delete(data, "id") // Remove ID from updates

	// Create query and update. hasUpdatedAt stays false (the default)
	// because the updated_at column is already present in `data` (we set it
	// on modelField above and structToMap emitted it), so we do NOT want
	// Update's grammar-level injection to add a second copy.
	//
	// Scope semantics mirror ForceDelete: skip ONLY the auto-installed
	// soft-delete scope, by name. Save targets the row by primary key on
	// an instance the caller already holds, so deleted_at IS NULL must
	// not filter it (saving a trashed row would otherwise succeed or
	// silently 0-row-update depending on whether any newQuery[T] had run
	// earlier in the process). Every other registered global scope
	// (tenant, archive, locale, ...) still applies via bulkUpdate's
	// applyGlobalScopes, so a by-PK Save cannot mutate rows outside the
	// caller's scope set.
	query := &Query[T]{
		table:  tableName,
		driver: drv,
	}
	query.WithoutGlobalScope(softDeleteScopeName)

	// bulkUpdate, not Update: this map came from structToMap on a
	// caller-constructed *T, so the map-write mass-assignment policy does
	// not apply (same scoping as Create(*T)).
	var idVal any
	if opts.pk == pkInt {
		idVal = idField.Uint()
	} else {
		idVal = idField.String()
	}
	if _, err := query.Where("id = ?", idVal).bulkUpdate(ctx, data, BulkOpUpdate); err != nil {
		return err
	}

	// Call AfterUpdate hook if exists
	if hook, ok := any(model).(AfterUpdateHook); ok {
		if err := hook.AfterUpdate(); err != nil {
			return err
		}
	}
	return nil
}

// CreateMany inserts each record sequentially through the package
// default Manager. Takes ctx as the first argument so the entire batch
// participates in the caller's transaction (when ctx carries a *sql.Tx)
// or routes through the pool driver. The first error short-circuits.
func CreateMany[T any](ctx context.Context, records []T) error {
	for i := range records {
		if err := Save(ctx, nil, &records[i]); err != nil {
			return err
		}
	}
	return nil
}

// Common errors
var (
	ErrRecordNotFound = sql.ErrNoRows
	ErrInvalidSQL     = errors.New("invalid SQL query")
	ErrConnection     = errors.New("database connection error")
	ErrTransaction    = errors.New("transaction error")
)
