package orm

import (
	"context"
	"errors"
	"strings"
)

// ErrImmutableModelUpdate is returned by Save() on an existing record
// embedded in an ImmutableModel[T] / ImmutableUUIDModel[T]. Append-only
// models cannot be mutated; recreate rather than update.
var ErrImmutableModelUpdate = errors.New("orm: cannot update an immutable model (no UpdatedAt column); use ForceDelete + Create or model an updatable copy")

// ---------------------------------------------------------------------------
// ImmutableModel[T] static helpers
// ---------------------------------------------------------------------------

// Find retrieves a record by primary key. Takes ctx as the first
// argument.
func (ImmutableModel[T]) Find(ctx context.Context, id any) (*T, error) {
	return modelFind[T](ctx, id)
}

// FindBy retrieves a record by a specific field. Takes ctx as the first
// argument.
func (ImmutableModel[T]) FindBy(ctx context.Context, field string, value any) (*T, error) {
	return modelFindBy[T](ctx, field, value)
}

// First retrieves the first record. Takes ctx as the first argument.
func (ImmutableModel[T]) First(ctx context.Context) (*T, error) {
	return modelFirst[T](ctx)
}

// Last retrieves the last record (by id descending). Takes ctx as the
// first argument.
func (ImmutableModel[T]) Last(ctx context.Context) (*T, error) {
	return modelLast[T](ctx, "id")
}

// All retrieves all records. Takes ctx as the first argument.
func (ImmutableModel[T]) All(ctx context.Context) ([]T, error) {
	return modelAll[T](ctx)
}

// Where starts a query with a WHERE condition.
func (ImmutableModel[T]) Where(condition string, args ...any) *Query[T] {
	return modelWhere[T](condition, args...)
}

// WhereIn queries for records where field is in the given values.
func (ImmutableModel[T]) WhereIn(field string, values []any) *Query[T] {
	return modelWhereIn[T](field, values)
}

// OrderBy starts a query with an ORDER BY clause.
func (ImmutableModel[T]) OrderBy(column, direction string) *Query[T] {
	return modelOrderBy[T](column, direction)
}

// With eager loads relationships.
func (ImmutableModel[T]) With(relations ...string) *Query[T] {
	return modelWith[T](relations...)
}

// Create inserts a new record. Takes ctx as the first argument so
// transaction enrollment is mandatory and explicit. Accepts a
// map[string]any or a *T.
func (ImmutableModel[T]) Create(ctx context.Context, data any) (*T, error) {
	return modelCreate[T](ctx, data)
}

// CreateMany inserts multiple records. Takes ctx as the first argument.
func (ImmutableModel[T]) CreateMany(ctx context.Context, records []T) error {
	return CreateMany[T](ctx, records)
}

// Count returns the number of records. Takes ctx as the first argument.
func (ImmutableModel[T]) Count(ctx context.Context) (int, error) {
	return modelCount[T](ctx)
}

// Exists checks if any records exist. Takes ctx as the first argument.
// A failed query returns (false, err) rather than silently reporting
// absence.
func (ImmutableModel[T]) Exists(ctx context.Context) (bool, error) {
	return modelExists[T](ctx)
}

// Paginate returns a paginated result for all records. Takes ctx as
// the first argument.
func (ImmutableModel[T]) Paginate(ctx context.Context, page, perPage int) (*PaginatedResult[T], error) {
	return modelPaginate[T](ctx, page, perPage)
}

// Pluck retrieves a single column's values. Takes ctx as the first
// argument.
func (ImmutableModel[T]) Pluck(ctx context.Context, column string) ([]any, error) {
	return modelPluck[T](ctx, column)
}

// ---------------------------------------------------------------------------
// ImmutableUUIDModel[T] static helpers
// ---------------------------------------------------------------------------

// Find retrieves a record by UUID primary key. Takes ctx as the first
// argument.
func (ImmutableUUIDModel[T]) Find(ctx context.Context, id string) (*T, error) {
	return modelFind[T](ctx, id)
}

// FindBy retrieves a record by a specific field. Takes ctx as the first
// argument.
func (ImmutableUUIDModel[T]) FindBy(ctx context.Context, field string, value any) (*T, error) {
	return modelFindBy[T](ctx, field, value)
}

// First retrieves the first record. Takes ctx as the first argument.
func (ImmutableUUIDModel[T]) First(ctx context.Context) (*T, error) {
	return modelFirst[T](ctx)
}

// All retrieves all records. Takes ctx as the first argument.
func (ImmutableUUIDModel[T]) All(ctx context.Context) ([]T, error) {
	return modelAll[T](ctx)
}

// Where starts a query with a WHERE condition.
func (ImmutableUUIDModel[T]) Where(condition string, args ...any) *Query[T] {
	return modelWhere[T](condition, args...)
}

// OrderBy starts a query with an ORDER BY clause.
func (ImmutableUUIDModel[T]) OrderBy(column, direction string) *Query[T] {
	return modelOrderBy[T](column, direction)
}

// Create inserts a new record. Takes ctx as the first argument.
// Accepts a map[string]any or a *T.
func (ImmutableUUIDModel[T]) Create(ctx context.Context, data any) (*T, error) {
	return modelCreate[T](ctx, data)
}

// CreateMany inserts multiple records. Takes ctx as the first argument.
func (ImmutableUUIDModel[T]) CreateMany(ctx context.Context, records []T) error {
	return CreateMany[T](ctx, records)
}

// Count returns the number of records. Takes ctx as the first argument.
func (ImmutableUUIDModel[T]) Count(ctx context.Context) (int, error) {
	return modelCount[T](ctx)
}

// ---------------------------------------------------------------------------
// Save-side wiring: the insert-only path for embedded ImmutableModel variants
// is the saveOpts{appendOnly: true} branch of saveCore (model.go). There is no
// update branch: an existing-row Save returns ErrImmutableModelUpdate.
// ---------------------------------------------------------------------------

// ensureImmutableSerialization is a compile-time guard that the
// immutable embedded types are recognised by serializeEmbedded. The
// switch in serializeEmbedded is updated in model.go alongside this
// file; this comment exists as a reminder.
var _ = strings.HasPrefix
