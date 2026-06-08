package orm

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/velocitykode/velocity/orm/drivers"
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
	var model T
	q := newQuery[T]()
	if err := q.Where("id = ?", id).First(ctx, &model); err != nil {
		return nil, err
	}
	return &model, nil
}

// FindBy retrieves a record by a specific field. Takes ctx as the first
// argument.
func (ImmutableModel[T]) FindBy(ctx context.Context, field string, value any) (*T, error) {
	if err := validateIdentifier(field); err != nil {
		return nil, err
	}
	var model T
	q := newQuery[T]()
	if err := q.Where(field+" = ?", value).First(ctx, &model); err != nil {
		return nil, err
	}
	return &model, nil
}

// First retrieves the first record. Takes ctx as the first argument.
func (ImmutableModel[T]) First(ctx context.Context) (*T, error) {
	var model T
	q := newQuery[T]()
	if err := q.First(ctx, &model); err != nil {
		return nil, err
	}
	return &model, nil
}

// Last retrieves the last record (by id descending). Takes ctx as the
// first argument.
func (ImmutableModel[T]) Last(ctx context.Context) (*T, error) {
	var model T
	q := newQuery[T]()
	if err := q.OrderBy("id", "DESC").First(ctx, &model); err != nil {
		return nil, err
	}
	return &model, nil
}

// All retrieves all records. Takes ctx as the first argument.
func (ImmutableModel[T]) All(ctx context.Context) ([]T, error) {
	return newQuery[T]().Get(ctx)
}

// Where starts a query with a WHERE condition.
func (ImmutableModel[T]) Where(condition string, args ...any) *Query[T] {
	return newQuery[T]().Where(condition, args...)
}

// WhereIn queries for records where field is in the given values.
func (ImmutableModel[T]) WhereIn(field string, values []any) *Query[T] {
	return newQuery[T]().WhereIn(field, values)
}

// OrderBy starts a query with an ORDER BY clause.
func (ImmutableModel[T]) OrderBy(column, direction string) *Query[T] {
	return newQuery[T]().OrderBy(column, direction)
}

// With eager loads relationships.
func (ImmutableModel[T]) With(relations ...string) *Query[T] {
	return newQuery[T]().With(relations...)
}

// Create inserts a new record. Takes ctx as the first argument so
// transaction enrollment is mandatory and explicit. Accepts a
// map[string]any or a *T.
func (ImmutableModel[T]) Create(ctx context.Context, data any) (*T, error) {
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
func (ImmutableModel[T]) CreateMany(ctx context.Context, records []T) error {
	for i := range records {
		if err := Save(ctx, nil, &records[i]); err != nil {
			return err
		}
	}
	return nil
}

// Count returns the number of records. Takes ctx as the first argument.
func (ImmutableModel[T]) Count(ctx context.Context) (int, error) {
	return newQuery[T]().Count(ctx)
}

// Exists checks if any records exist. Takes ctx as the first argument.
func (ImmutableModel[T]) Exists(ctx context.Context) bool {
	count, _ := newQuery[T]().Count(ctx)
	return count > 0
}

// Paginate returns a paginated result for all records. Takes ctx as
// the first argument.
func (ImmutableModel[T]) Paginate(ctx context.Context, page, perPage int) (*PaginatedResult[T], error) {
	return newQuery[T]().Paginate(ctx, page, perPage)
}

// Pluck retrieves a single column's values. Takes ctx as the first
// argument.
func (ImmutableModel[T]) Pluck(ctx context.Context, column string) ([]any, error) {
	return newQuery[T]().Pluck(ctx, column)
}

// ---------------------------------------------------------------------------
// ImmutableUUIDModel[T] static helpers
// ---------------------------------------------------------------------------

// Find retrieves a record by UUID primary key. Takes ctx as the first
// argument.
func (ImmutableUUIDModel[T]) Find(ctx context.Context, id string) (*T, error) {
	var model T
	q := newQuery[T]()
	if err := q.Where("id = ?", id).First(ctx, &model); err != nil {
		return nil, err
	}
	return &model, nil
}

// FindBy retrieves a record by a specific field. Takes ctx as the first
// argument.
func (ImmutableUUIDModel[T]) FindBy(ctx context.Context, field string, value any) (*T, error) {
	if err := validateIdentifier(field); err != nil {
		return nil, err
	}
	var model T
	q := newQuery[T]()
	if err := q.Where(field+" = ?", value).First(ctx, &model); err != nil {
		return nil, err
	}
	return &model, nil
}

// First retrieves the first record. Takes ctx as the first argument.
func (ImmutableUUIDModel[T]) First(ctx context.Context) (*T, error) {
	var model T
	q := newQuery[T]()
	if err := q.First(ctx, &model); err != nil {
		return nil, err
	}
	return &model, nil
}

// All retrieves all records. Takes ctx as the first argument.
func (ImmutableUUIDModel[T]) All(ctx context.Context) ([]T, error) {
	return newQuery[T]().Get(ctx)
}

// Where starts a query with a WHERE condition.
func (ImmutableUUIDModel[T]) Where(condition string, args ...any) *Query[T] {
	return newQuery[T]().Where(condition, args...)
}

// OrderBy starts a query with an ORDER BY clause.
func (ImmutableUUIDModel[T]) OrderBy(column, direction string) *Query[T] {
	return newQuery[T]().OrderBy(column, direction)
}

// Create inserts a new record. Takes ctx as the first argument.
// Accepts a map[string]any or a *T.
func (ImmutableUUIDModel[T]) Create(ctx context.Context, data any) (*T, error) {
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
func (ImmutableUUIDModel[T]) CreateMany(ctx context.Context, records []T) error {
	for i := range records {
		if err := Save(ctx, nil, &records[i]); err != nil {
			return err
		}
	}
	return nil
}

// Count returns the number of records. Takes ctx as the first argument.
func (ImmutableUUIDModel[T]) Count(ctx context.Context) (int, error) {
	return newQuery[T]().Count(ctx)
}

// ---------------------------------------------------------------------------
// Save-side wiring: insert-only path for embedded ImmutableModel variants.
// ---------------------------------------------------------------------------

// saveImmutableModel handles auto-increment-id immutable inserts. There
// is no update branch: callers reaching here with isInsert=false get
// ErrImmutableModelUpdate.
func saveImmutableModel[T any](ctx context.Context, drv drivers.Driver, model *T, modelField, idField, existsField reflect.Value, tableName string, isInsert bool, skipTimestamps bool) error {
	if !isInsert {
		return ErrImmutableModelUpdate
	}

	// Respect caller-set CreatedAt; only stamp when zero. The field is
	// optional (AppendOnly without CreatedAtOnly is a valid composition,
	// e.g. a model that wants no auto-managed timestamp at all) so each
	// access is gated on validity. Skipped when the model opted out of
	// timestamps.
	if !skipTimestamps {
		if createdAtField := modelField.FieldByName("CreatedAt"); createdAtField.IsValid() {
			if createdAtField.Interface().(time.Time).IsZero() {
				createdAtField.Set(reflect.ValueOf(time.Now()))
			}
		}
	}

	if hook, ok := any(model).(BeforeCreateHook); ok {
		if err := hook.BeforeCreate(); err != nil {
			return err
		}
	}

	data := structToMap(model)
	delete(data, "id")

	q := &Query[T]{
		table:        tableName,
		driver:       drv,
		hasUpdatedAt: false,
	}
	lastID, err := q.InsertGetId(ctx, data)
	if err != nil {
		return err
	}

	idField.SetUint(uint64(lastID))
	markModelExisting(model)

	if hook, ok := any(model).(AfterCreateHook); ok {
		if err := hook.AfterCreate(); err != nil {
			return err
		}
	}
	return nil
}

// saveImmutableUUIDModel handles UUID-keyed immutable inserts.
func saveImmutableUUIDModel[T any](ctx context.Context, drv drivers.Driver, model *T, modelField, idField, existsField reflect.Value, tableName string, isInsert bool, skipTimestamps bool) error {
	if !isInsert {
		return ErrImmutableModelUpdate
	}

	if idField.String() == "" {
		idField.SetString(uuid.New().String())
	}
	// Respect caller-set CreatedAt; only stamp when zero. Optional field
	// per the trait composition rules (see saveImmutableModel). Skipped
	// when the model opted out of timestamps.
	if !skipTimestamps {
		if createdAtField := modelField.FieldByName("CreatedAt"); createdAtField.IsValid() {
			if createdAtField.Interface().(time.Time).IsZero() {
				createdAtField.Set(reflect.ValueOf(time.Now()))
			}
		}
	}

	if hook, ok := any(model).(BeforeCreateHook); ok {
		if err := hook.BeforeCreate(); err != nil {
			return err
		}
	}

	data := structToMap(model)

	q := &Query[T]{
		table:        tableName,
		driver:       drv,
		hasUpdatedAt: false,
	}
	if _, err := q.InsertGetId(ctx, data); err != nil {
		return err
	}

	markModelExisting(model)

	if hook, ok := any(model).(AfterCreateHook); ok {
		if err := hook.AfterCreate(); err != nil {
			return err
		}
	}
	return nil
}

// ensureImmutableSerialization is a compile-time guard that the
// immutable embedded types are recognised by serializeEmbedded. The
// switch in serializeEmbedded is updated in model.go alongside this
// file; this comment exists as a reminder.
var _ = strings.HasPrefix
