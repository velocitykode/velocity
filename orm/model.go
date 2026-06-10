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

// Static-like methods that return the actual type

// Find retrieves a record by primary key. Takes ctx as the first
// argument so the read participates in the caller's transaction when
// ctx carries a *sql.Tx.
func (Model[T]) Find(ctx context.Context, id any) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.Where("id = ?", id).First(ctx, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// FindBy retrieves a record by a specific field. Takes ctx as the first
// argument.
func (Model[T]) FindBy(ctx context.Context, field string, value any) (*T, error) {
	if err := validateIdentifier(field); err != nil {
		return nil, err
	}
	var model T
	query := newQuery[T]()
	err := query.Where(field+" = ?", value).First(ctx, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// First retrieves the first record. Takes ctx as the first argument.
func (Model[T]) First(ctx context.Context) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.First(ctx, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// Last retrieves the last record (by id descending). Takes ctx as the
// first argument.
func (Model[T]) Last(ctx context.Context) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.OrderBy("id", "DESC").First(ctx, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// All retrieves all records. Takes ctx as the first argument.
func (Model[T]) All(ctx context.Context) ([]T, error) {
	query := newQuery[T]()
	return query.Get(ctx)
}

// Where starts a query with a WHERE condition
func (Model[T]) Where(condition string, args ...any) *Query[T] {
	query := newQuery[T]()
	return query.Where(condition, args...)
}

// WhereIn queries for records where field is in the given values
func (Model[T]) WhereIn(field string, values []any) *Query[T] {
	query := newQuery[T]()
	return query.WhereIn(field, values)
}

// WhereNull starts a query with a WHERE IS NULL condition
func (Model[T]) WhereNull(field string) *Query[T] {
	query := newQuery[T]()
	return query.WhereNull(field)
}

// WhereNotNull starts a query with a WHERE IS NOT NULL condition
func (Model[T]) WhereNotNull(field string) *Query[T] {
	query := newQuery[T]()
	return query.WhereNotNull(field)
}

// OrderBy starts a query with an ORDER BY clause
func (Model[T]) OrderBy(column, direction string) *Query[T] {
	query := newQuery[T]()
	return query.OrderBy(column, direction)
}

// With eager loads relationships
func (Model[T]) With(relations ...string) *Query[T] {
	query := newQuery[T]()
	return query.With(relations...)
}

// Create inserts a new record. Takes ctx as the first argument so
// transaction enrollment is mandatory and explicit. Accepts a
// map[string]any or a *T. Resolves the driver from the package default
// Manager.
func (Model[T]) Create(ctx context.Context, data any) (*T, error) {
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
		// Ensure fillable/guarded gates also apply to pre-constructed
		// model pointers so mass-assignment protection cannot be
		// bypassed by callers who build the struct manually.
		if err := applyFillableToStruct(v); err != nil {
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

// CreateMany inserts multiple records. Takes ctx as the first argument
// so the entire batch participates in the caller's transaction (when
// ctx carries a *sql.Tx) or routes through the pool driver.
func (Model[T]) CreateMany(ctx context.Context, records []T) error {
	for _, record := range records {
		if err := Save(ctx, nil, &record); err != nil {
			return err
		}
	}
	return nil
}

// Count returns the number of records. Takes ctx as the first argument.
func (Model[T]) Count(ctx context.Context) (int, error) {
	query := newQuery[T]()
	return query.Count(ctx)
}

// Exists checks if any records exist. Takes ctx as the first argument.
func (Model[T]) Exists(ctx context.Context) bool {
	count, _ := Model[T]{}.Count(ctx)
	return count > 0
}

// Paginate returns a paginated result for all records. Takes ctx as
// the first argument.
func (Model[T]) Paginate(ctx context.Context, page, perPage int) (*PaginatedResult[T], error) {
	query := newQuery[T]()
	return query.Paginate(ctx, page, perPage)
}

// Raw creates a raw SQL query builder for executing custom queries
// Usage: Model{}.Raw("SELECT * FROM users WHERE id = ?", 1).First(&user)
func (Model[T]) Raw(sql string, args ...any) *RawQuery[T] {
	return NewRawQuery[T](sql, args...)
}

// Pluck retrieves a single column values. Takes ctx as the first
// argument.
func (Model[T]) Pluck(ctx context.Context, column string) ([]any, error) {
	query := newQuery[T]()
	return query.Pluck(ctx, column)
}

// Update updates records matching conditions. Takes ctx as the first
// argument so transaction enrollment is mandatory and explicit.
func (Model[T]) Update(ctx context.Context, conditions map[string]any, updates map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.Update(ctx, updates)
}

// DeleteWhere permanently deletes records matching conditions. Takes
// ctx as the first argument so transaction enrollment is mandatory and
// explicit.
func (Model[T]) DeleteWhere(ctx context.Context, conditions map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.ForceDelete(ctx)
}

// Instance methods

// UUIDModel static methods

// Find retrieves a record by UUID primary key. Takes ctx as the first
// argument.
func (UUIDModel[T]) Find(ctx context.Context, id string) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.Where("id = ?", id).First(ctx, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// FindBy retrieves a record by a specific field. Takes ctx as the first
// argument.
func (UUIDModel[T]) FindBy(ctx context.Context, field string, value any) (*T, error) {
	if err := validateIdentifier(field); err != nil {
		return nil, err
	}
	var model T
	query := newQuery[T]()
	err := query.Where(field+" = ?", value).First(ctx, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// First retrieves the first record. Takes ctx as the first argument.
func (UUIDModel[T]) First(ctx context.Context) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.First(ctx, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// Last retrieves the last record. UUID primary keys are non-monotonic, so
// "last" orders by created_at descending when the model manages timestamps.
// A model that opts out of timestamps (UsesTimestamps()==false) has no
// created_at column, so Last falls back to ordering by id to honor the
// opt-out contract that no read references a timestamp column. Takes ctx as
// the first argument.
func (UUIDModel[T]) Last(ctx context.Context) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.OrderBy(lastOrderColumn[T](), "DESC").First(ctx, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// All retrieves all records. Takes ctx as the first argument.
func (UUIDModel[T]) All(ctx context.Context) ([]T, error) {
	query := newQuery[T]()
	return query.Get(ctx)
}

// Where starts a query with a WHERE condition
func (UUIDModel[T]) Where(condition string, args ...any) *Query[T] {
	query := newQuery[T]()
	return query.Where(condition, args...)
}

// WhereIn queries for records where field is in the given values
func (UUIDModel[T]) WhereIn(field string, values []any) *Query[T] {
	query := newQuery[T]()
	return query.WhereIn(field, values)
}

// WhereNull starts a query with a WHERE IS NULL condition
func (UUIDModel[T]) WhereNull(field string) *Query[T] {
	query := newQuery[T]()
	return query.WhereNull(field)
}

// WhereNotNull starts a query with a WHERE IS NOT NULL condition
func (UUIDModel[T]) WhereNotNull(field string) *Query[T] {
	query := newQuery[T]()
	return query.WhereNotNull(field)
}

// OrderBy starts a query with an ORDER BY clause
func (UUIDModel[T]) OrderBy(column, direction string) *Query[T] {
	query := newQuery[T]()
	return query.OrderBy(column, direction)
}

// With eager loads relationships
func (UUIDModel[T]) With(relations ...string) *Query[T] {
	query := newQuery[T]()
	return query.With(relations...)
}

// Create inserts a new record. Takes ctx as the first argument so
// transaction enrollment is mandatory and explicit. Accepts a
// map[string]any or a *T.
func (UUIDModel[T]) Create(ctx context.Context, data any) (*T, error) {
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
		// Ensure fillable/guarded gates also apply to pre-constructed
		// model pointers so mass-assignment protection cannot be
		// bypassed by callers who build the struct manually.
		if err := applyFillableToStruct(v); err != nil {
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

// CreateMany inserts multiple records. Takes ctx as the first argument
// so the entire batch participates in the caller's transaction.
func (UUIDModel[T]) CreateMany(ctx context.Context, records []T) error {
	for _, record := range records {
		if err := Save(ctx, nil, &record); err != nil {
			return err
		}
	}
	return nil
}

// Count returns the number of records. Takes ctx as the first argument.
func (UUIDModel[T]) Count(ctx context.Context) (int, error) {
	query := newQuery[T]()
	return query.Count(ctx)
}

// Exists checks if any records exist. Takes ctx as the first argument.
func (UUIDModel[T]) Exists(ctx context.Context) bool {
	count, _ := UUIDModel[T]{}.Count(ctx)
	return count > 0
}

// Paginate returns a paginated result for all records. Takes ctx as
// the first argument.
func (UUIDModel[T]) Paginate(ctx context.Context, page, perPage int) (*PaginatedResult[T], error) {
	query := newQuery[T]()
	return query.Paginate(ctx, page, perPage)
}

// Raw creates a raw SQL query builder for executing custom queries
// Usage: UUIDModel{}.Raw("SELECT * FROM users WHERE id = ?", 1).First(&user)
func (UUIDModel[T]) Raw(sql string, args ...any) *RawQuery[T] {
	return NewRawQuery[T](sql, args...)
}

// Pluck retrieves a single column values. Takes ctx as the first
// argument.
func (UUIDModel[T]) Pluck(ctx context.Context, column string) ([]any, error) {
	query := newQuery[T]()
	return query.Pluck(ctx, column)
}

// Update updates records matching conditions. Takes ctx as the first
// argument so transaction enrollment is mandatory and explicit.
func (UUIDModel[T]) Update(ctx context.Context, conditions map[string]any, updates map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.Update(ctx, updates)
}

// DeleteWhere permanently deletes records matching conditions. Takes
// ctx as the first argument so transaction enrollment is mandatory and
// explicit.
func (UUIDModel[T]) DeleteWhere(ctx context.Context, conditions map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.ForceDelete(ctx)
}

// UUIDModel instance methods

// SoftDeleteModel static methods

// Find retrieves a record by primary key. Takes ctx as the first
// argument.
func (SoftDeleteModel[T]) Find(ctx context.Context, id any) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.Where("id = ?", id).First(ctx, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// FindBy retrieves a record by a specific field. Takes ctx as the first
// argument.
func (SoftDeleteModel[T]) FindBy(ctx context.Context, field string, value any) (*T, error) {
	if err := validateIdentifier(field); err != nil {
		return nil, err
	}
	var model T
	query := newQuery[T]()
	err := query.Where(field+" = ?", value).First(ctx, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// First retrieves the first record. Takes ctx as the first argument.
func (SoftDeleteModel[T]) First(ctx context.Context) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.First(ctx, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// Last retrieves the last record (by id descending). Takes ctx as the
// first argument.
func (SoftDeleteModel[T]) Last(ctx context.Context) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.OrderBy("id", "DESC").First(ctx, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// All retrieves all records. Takes ctx as the first argument.
func (SoftDeleteModel[T]) All(ctx context.Context) ([]T, error) {
	query := newQuery[T]()
	return query.Get(ctx)
}

// Where starts a query with a WHERE condition
func (SoftDeleteModel[T]) Where(condition string, args ...any) *Query[T] {
	query := newQuery[T]()
	return query.Where(condition, args...)
}

// WhereIn queries for records where field is in the given values
func (SoftDeleteModel[T]) WhereIn(field string, values []any) *Query[T] {
	query := newQuery[T]()
	return query.WhereIn(field, values)
}

// WhereNull starts a query with a WHERE IS NULL condition
func (SoftDeleteModel[T]) WhereNull(field string) *Query[T] {
	query := newQuery[T]()
	return query.WhereNull(field)
}

// WhereNotNull starts a query with a WHERE IS NOT NULL condition
func (SoftDeleteModel[T]) WhereNotNull(field string) *Query[T] {
	query := newQuery[T]()
	return query.WhereNotNull(field)
}

// OrderBy starts a query with an ORDER BY clause
func (SoftDeleteModel[T]) OrderBy(column, direction string) *Query[T] {
	query := newQuery[T]()
	return query.OrderBy(column, direction)
}

// With eager loads relationships
func (SoftDeleteModel[T]) With(relations ...string) *Query[T] {
	query := newQuery[T]()
	return query.With(relations...)
}

// Create inserts a new record. Takes ctx as the first argument so
// transaction enrollment is mandatory and explicit.
func (SoftDeleteModel[T]) Create(ctx context.Context, data any) (*T, error) {
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
		// Ensure fillable/guarded gates also apply to pre-constructed
		// model pointers so mass-assignment protection cannot be
		// bypassed by callers who build the struct manually.
		if err := applyFillableToStruct(v); err != nil {
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

// CreateMany inserts multiple records. Takes ctx as the first argument.
func (SoftDeleteModel[T]) CreateMany(ctx context.Context, records []T) error {
	for _, record := range records {
		if err := Save(ctx, nil, &record); err != nil {
			return err
		}
	}
	return nil
}

// Count returns the number of records. Takes ctx as the first argument.
func (SoftDeleteModel[T]) Count(ctx context.Context) (int, error) {
	query := newQuery[T]()
	return query.Count(ctx)
}

// Exists checks if any records exist. Takes ctx as the first argument.
func (SoftDeleteModel[T]) Exists(ctx context.Context) bool {
	count, _ := SoftDeleteModel[T]{}.Count(ctx)
	return count > 0
}

// Paginate returns a paginated result for all records. Takes ctx as
// the first argument.
func (SoftDeleteModel[T]) Paginate(ctx context.Context, page, perPage int) (*PaginatedResult[T], error) {
	query := newQuery[T]()
	return query.Paginate(ctx, page, perPage)
}

// Raw creates a raw SQL query builder for executing custom queries
// Usage: SoftDeleteModel{}.Raw("SELECT * FROM users WHERE id = ?", 1).First(&user)
func (SoftDeleteModel[T]) Raw(sql string, args ...any) *RawQuery[T] {
	return NewRawQuery[T](sql, args...)
}

// Pluck retrieves a single column values. Takes ctx as the first
// argument.
func (SoftDeleteModel[T]) Pluck(ctx context.Context, column string) ([]any, error) {
	query := newQuery[T]()
	return query.Pluck(ctx, column)
}

// Update updates records matching conditions. Takes ctx as the first
// argument so transaction enrollment is mandatory and explicit.
func (SoftDeleteModel[T]) Update(ctx context.Context, conditions map[string]any, updates map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.Update(ctx, updates)
}

// DeleteWhere soft deletes records matching conditions. Takes ctx as
// the first argument so transaction enrollment is mandatory and
// explicit.
func (SoftDeleteModel[T]) DeleteWhere(ctx context.Context, conditions map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.Delete(ctx)
}

// ForceDeleteWhere permanently deletes records matching conditions.
// Takes ctx as the first argument so transaction enrollment is
// mandatory and explicit.
func (SoftDeleteModel[T]) ForceDeleteWhere(ctx context.Context, conditions map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.ForceDelete(ctx)
}

// OnlyTrashed retrieves only soft deleted records
func (SoftDeleteModel[T]) OnlyTrashed() *Query[T] {
	query := newQuery[T]()
	return query.OnlyTrashed()
}

// WithTrashed includes soft deleted records
func (SoftDeleteModel[T]) WithTrashed() *Query[T] {
	query := newQuery[T]()
	return query.WithTrashed()
}

// SoftDeleteModel instance methods

// SoftDeleteUUIDModel static methods

// Find retrieves a record by UUID primary key. Takes ctx as the first
// argument.
func (SoftDeleteUUIDModel[T]) Find(ctx context.Context, id string) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.Where("id = ?", id).First(ctx, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// FindBy retrieves a record by a specific field. Takes ctx as the first
// argument.
func (SoftDeleteUUIDModel[T]) FindBy(ctx context.Context, field string, value any) (*T, error) {
	if err := validateIdentifier(field); err != nil {
		return nil, err
	}
	var model T
	query := newQuery[T]()
	err := query.Where(field+" = ?", value).First(ctx, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// First retrieves the first record. Takes ctx as the first argument.
func (SoftDeleteUUIDModel[T]) First(ctx context.Context) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.First(ctx, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// Last retrieves the last record. As with UUIDModel, ordering is by
// created_at descending when timestamps are managed, falling back to id when
// the model opted out of timestamps so no read references a missing column.
// Takes ctx as the first argument.
func (SoftDeleteUUIDModel[T]) Last(ctx context.Context) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.OrderBy(lastOrderColumn[T](), "DESC").First(ctx, &model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// All retrieves all records. Takes ctx as the first argument.
func (SoftDeleteUUIDModel[T]) All(ctx context.Context) ([]T, error) {
	query := newQuery[T]()
	return query.Get(ctx)
}

// Where starts a query with a WHERE condition
func (SoftDeleteUUIDModel[T]) Where(condition string, args ...any) *Query[T] {
	query := newQuery[T]()
	return query.Where(condition, args...)
}

// WhereIn queries for records where field is in the given values
func (SoftDeleteUUIDModel[T]) WhereIn(field string, values []any) *Query[T] {
	query := newQuery[T]()
	return query.WhereIn(field, values)
}

// WhereNull starts a query with a WHERE IS NULL condition
func (SoftDeleteUUIDModel[T]) WhereNull(field string) *Query[T] {
	query := newQuery[T]()
	return query.WhereNull(field)
}

// WhereNotNull starts a query with a WHERE IS NOT NULL condition
func (SoftDeleteUUIDModel[T]) WhereNotNull(field string) *Query[T] {
	query := newQuery[T]()
	return query.WhereNotNull(field)
}

// OrderBy starts a query with an ORDER BY clause
func (SoftDeleteUUIDModel[T]) OrderBy(column, direction string) *Query[T] {
	query := newQuery[T]()
	return query.OrderBy(column, direction)
}

// With eager loads relationships
func (SoftDeleteUUIDModel[T]) With(relations ...string) *Query[T] {
	query := newQuery[T]()
	return query.With(relations...)
}

// Create inserts a new record. Takes ctx as the first argument so
// transaction enrollment is mandatory and explicit.
func (SoftDeleteUUIDModel[T]) Create(ctx context.Context, data any) (*T, error) {
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
		// Ensure fillable/guarded gates also apply to pre-constructed
		// model pointers so mass-assignment protection cannot be
		// bypassed by callers who build the struct manually.
		if err := applyFillableToStruct(v); err != nil {
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

// CreateMany inserts multiple records. Takes ctx as the first argument.
func (SoftDeleteUUIDModel[T]) CreateMany(ctx context.Context, records []T) error {
	for _, record := range records {
		if err := Save(ctx, nil, &record); err != nil {
			return err
		}
	}
	return nil
}

// Count returns the number of records. Takes ctx as the first argument.
func (SoftDeleteUUIDModel[T]) Count(ctx context.Context) (int, error) {
	query := newQuery[T]()
	return query.Count(ctx)
}

// Exists checks if any records exist. Takes ctx as the first argument.
func (SoftDeleteUUIDModel[T]) Exists(ctx context.Context) bool {
	count, _ := SoftDeleteUUIDModel[T]{}.Count(ctx)
	return count > 0
}

// Paginate returns a paginated result for all records. Takes ctx as
// the first argument.
func (SoftDeleteUUIDModel[T]) Paginate(ctx context.Context, page, perPage int) (*PaginatedResult[T], error) {
	query := newQuery[T]()
	return query.Paginate(ctx, page, perPage)
}

// Raw creates a raw SQL query builder for executing custom queries
// Usage: SoftDeleteUUIDModel{}.Raw("SELECT * FROM users WHERE id = ?", 1).First(&user)
func (SoftDeleteUUIDModel[T]) Raw(sql string, args ...any) *RawQuery[T] {
	return NewRawQuery[T](sql, args...)
}

// Pluck retrieves a single column values. Takes ctx as the first
// argument.
func (SoftDeleteUUIDModel[T]) Pluck(ctx context.Context, column string) ([]any, error) {
	query := newQuery[T]()
	return query.Pluck(ctx, column)
}

// Update updates records matching conditions. Takes ctx as the first
// argument so transaction enrollment is mandatory and explicit.
func (SoftDeleteUUIDModel[T]) Update(ctx context.Context, conditions map[string]any, updates map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.Update(ctx, updates)
}

// DeleteWhere soft deletes records matching conditions. Takes ctx as
// the first argument.
func (SoftDeleteUUIDModel[T]) DeleteWhere(ctx context.Context, conditions map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.Delete(ctx)
}

// ForceDeleteWhere permanently deletes records matching conditions.
// Takes ctx as the first argument.
func (SoftDeleteUUIDModel[T]) ForceDeleteWhere(ctx context.Context, conditions map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.ForceDelete(ctx)
}

// OnlyTrashed retrieves only soft deleted records
func (SoftDeleteUUIDModel[T]) OnlyTrashed() *Query[T] {
	query := newQuery[T]()
	return query.OnlyTrashed()
}

// WithTrashed includes soft deleted records
func (SoftDeleteUUIDModel[T]) WithTrashed() *Query[T] {
	query := newQuery[T]()
	return query.WithTrashed()
}

// SoftDeleteUUIDModel instance methods

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

// Fillable interface allows models to specify which fields can be mass-assigned
type Fillable interface {
	Fillable() []string
}

// Guarded interface allows models to specify which fields are protected from mass-assignment
type Guarded interface {
	Guarded() []string
}

// Helper functions

// applyFillableToStruct zeros out any guarded fields and any fields not in
// the Fillable allowlist before the struct is persisted. This mirrors the
// enforcement performed by mapToStruct so Create(*T) and Create(map) share
// the same mass-assignment policy.
//
// Fields protected by the framework itself (ID, timestamps, embedded Model
// bookkeeping) are always left intact - fillable/guarded only governs
// fields the application explicitly manages.
// applyFillableToStruct zeros every field on s that is not allowed by the
// model's Fillable/Guarded policy. Resolves columns and policy through the
// canonical ModelMeta + FillablePolicy so the protection rules match
// mapToStruct exactly, regardless of which entry point the caller used.
//
// Embedded base columns are framework-managed and always preserved, even
// when the model declares a Fillable allowlist that omits them.
func applyFillableToStruct(s any) error {
	policy := PolicyFor(s)
	if policy.implicitDeny {
		// No declared policy. Deny-by-default applies only to map-based
		// writes; a *T the caller constructed field-by-field in code is
		// not attacker-shaped input, so it persists untouched.
		return nil
	}
	if !policy.HasFillable && !policy.HasGuarded {
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
// FillablePolicy keyed on the snake_case'd Go field name, so attackers
// cannot bypass a guard by submitting the column-tag value instead.
//
// Deny-by-default: a model that declares neither Fillable() nor Guarded()
// (and does not opt out via AllowAllColumns) rejects the write with a
// *MassAssignmentError naming the model and the offending keys, rather
// than silently skipping or - worse - writing them. Models with a
// declared policy keep the established semantics: disallowed keys are
// silently skipped.
//
// Embedded base columns (id, created_at, updated_at, deleted_at) bypass
// the Fillable/Guarded check by design: they are framework-managed and
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
func Save[T any](ctx context.Context, m *Manager, model *T) error {
	if m == nil {
		m = Default()
	}
	if m == nil {
		return errors.New("orm: no default manager set - call SetDefault or pass a *Manager")
	}
	drv := m.DefaultDriver()
	if drv == nil {
		return errors.New("orm: no database connection")
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

	tableName := toSnakeCase(t.Name()) + "s"
	if tableNamer, ok := any(model).(interface{ TableName() string }); ok {
		tableName = tableNamer.TableName()
	}

	idField := modelField.FieldByName("ID")
	existsField := modelField.FieldByName("IsExisting")
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
		saveErr = saveImmutableUUIDModel(ctx, drv, model, modelField, idField, existsField, tableName, isInsert, skipTimestamps)
	case feats.appendOnly:
		saveErr = saveImmutableModel(ctx, drv, model, modelField, idField, existsField, tableName, isInsert, skipTimestamps)
	case feats.hasUUIDPK:
		saveErr = saveUUIDModel(ctx, drv, model, modelField, idField, existsField, tableName, isInsert, skipTimestamps)
	default:
		saveErr = saveModel(ctx, drv, model, modelField, idField, existsField, tableName, isInsert, skipTimestamps)
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

// saveModel handles saving for auto-increment ID models
func saveModel[T any](ctx context.Context, drv drivers.Driver, model *T, modelField, idField, existsField reflect.Value, tableName string, isInsert bool, skipTimestamps bool) error {
	if isInsert {
		// Set timestamps: respect caller-set CreatedAt; only stamp when zero.
		// UpdatedAt mirrors CreatedAt on insert for consistency. Both fields
		// are optional (CreatedAtOnly composition has CreatedAt but not
		// UpdatedAt; some custom shapes may have neither) so each is gated
		// on validity. Skipped entirely when the model opted out of
		// timestamps (the columns are not in the table).
		if !skipTimestamps {
			createdAtField := modelField.FieldByName("CreatedAt")
			updatedAtField := modelField.FieldByName("UpdatedAt")
			var createdAt time.Time
			if createdAtField.IsValid() {
				createdAt = createdAtField.Interface().(time.Time)
				if createdAt.IsZero() {
					createdAt = time.Now()
					createdAtField.Set(reflect.ValueOf(createdAt))
				}
			}
			if updatedAtField.IsValid() && updatedAtField.Interface().(time.Time).IsZero() {
				if createdAt.IsZero() {
					createdAt = time.Now()
				}
				updatedAtField.Set(reflect.ValueOf(createdAt))
			}
		}

		// Call BeforeCreate hook if exists
		if hook, ok := any(model).(BeforeCreateHook); ok {
			if err := hook.BeforeCreate(); err != nil {
				return err
			}
		}

		// Convert to map and insert
		data := structToMap(model)
		delete(data, "id") // Remove ID for auto-increment insert

		// Create query and insert
		query := &Query[T]{
			table:  tableName,
			driver: drv,
		}

		lastID, err := query.InsertGetId(ctx, data)
		if err != nil {
			return err
		}

		// Update ID and exists flag
		idField.SetUint(uint64(lastID))
		markModelExisting(model)

		// Call AfterCreate hook if exists
		if hook, ok := any(model).(AfterCreateHook); ok {
			if err := hook.AfterCreate(); err != nil {
				return err
			}
		}
	} else {
		// Update existing record. UpdatedAt is optional (CreatedAtOnly
		// without AppendOnly composes here too) and is skipped when the
		// model opted out of timestamps.
		if !skipTimestamps {
			if updatedAtField := modelField.FieldByName("UpdatedAt"); updatedAtField.IsValid() {
				updatedAtField.Set(reflect.ValueOf(time.Now()))
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

		// Create query and update. hasUpdatedAt=false because the
		// updated_at column is already present in `data` (we set it on
		// modelField above and structToMap emitted it), so we do NOT
		// want Update's grammar-level injection to add a second copy.
		query := &Query[T]{
			table:  tableName,
			driver: drv,
		}

		// bulkUpdate, not Update: this map came from structToMap on a
		// caller-constructed *T, so the map-write mass-assignment policy
		// does not apply (same scoping as Create(*T)).
		_, err := query.Where("id = ?", idField.Uint()).bulkUpdate(ctx, data, BulkOpUpdate)
		if err != nil {
			return err
		}

		// Call AfterUpdate hook if exists
		if hook, ok := any(model).(AfterUpdateHook); ok {
			if err := hook.AfterUpdate(); err != nil {
				return err
			}
		}
	}
	return nil
}

// saveUUIDModel handles saving for UUID primary key models
func saveUUIDModel[T any](ctx context.Context, drv drivers.Driver, model *T, modelField, idField, existsField reflect.Value, tableName string, isInsert bool, skipTimestamps bool) error {
	if isInsert {
		// Generate UUID if not already set
		if idField.String() == "" {
			idField.SetString(uuid.New().String())
		}

		// Set timestamps: respect caller-set CreatedAt; only stamp when zero.
		// UpdatedAt mirrors CreatedAt on insert for consistency. Both fields
		// are optional (custom compositions may omit one or both). Skipped
		// entirely when the model opted out of timestamps.
		if !skipTimestamps {
			createdAtField := modelField.FieldByName("CreatedAt")
			updatedAtField := modelField.FieldByName("UpdatedAt")
			var createdAt time.Time
			if createdAtField.IsValid() {
				createdAt = createdAtField.Interface().(time.Time)
				if createdAt.IsZero() {
					createdAt = time.Now()
					createdAtField.Set(reflect.ValueOf(createdAt))
				}
			}
			if updatedAtField.IsValid() && updatedAtField.Interface().(time.Time).IsZero() {
				if createdAt.IsZero() {
					createdAt = time.Now()
				}
				updatedAtField.Set(reflect.ValueOf(createdAt))
			}
		}

		// Call BeforeCreate hook if exists
		if hook, ok := any(model).(BeforeCreateHook); ok {
			if err := hook.BeforeCreate(); err != nil {
				return err
			}
		}

		// Convert to map and insert (ID is included for UUID models)
		data := structToMap(model)

		// Create query and insert
		query := &Query[T]{
			table:  tableName,
			driver: drv,
		}

		_, err := query.InsertGetId(ctx, data)
		if err != nil {
			return err
		}

		markModelExisting(model)

		// Call AfterCreate hook if exists
		if hook, ok := any(model).(AfterCreateHook); ok {
			if err := hook.AfterCreate(); err != nil {
				return err
			}
		}
	} else {
		// Update existing record. UpdatedAt is optional and is skipped when
		// the model opted out of timestamps.
		if !skipTimestamps {
			if updatedAtField := modelField.FieldByName("UpdatedAt"); updatedAtField.IsValid() {
				updatedAtField.Set(reflect.ValueOf(time.Now()))
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

		// Create query and update
		query := &Query[T]{
			table:  tableName,
			driver: drv,
		}

		// bulkUpdate, not Update: this map came from structToMap on a
		// caller-constructed *T, so the map-write mass-assignment policy
		// does not apply (same scoping as Create(*T)).
		_, err := query.Where("id = ?", idField.String()).bulkUpdate(ctx, data, BulkOpUpdate)
		if err != nil {
			return err
		}

		// Call AfterUpdate hook if exists
		if hook, ok := any(model).(AfterUpdateHook); ok {
			if err := hook.AfterUpdate(); err != nil {
				return err
			}
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
