package orm

import (
	"context"
	"database/sql"
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

// WithContext returns a *Query[T] bound to ctx. See Model[T].WithContext.
func (ImmutableModel[T]) WithContext(ctx context.Context) *Query[T] {
	return newQuery[T]().WithContext(ctx)
}

// WithTx binds the query chain to tx so Create participates in the
// caller's transaction. Append-only audit-log workflows are the
// motivating case: an audit row hashed against its predecessor must
// land in the same tx as the writes it describes, otherwise the chain
// breaks under concurrent producers.
func (ImmutableModel[T]) WithTx(tx *sql.Tx) *Query[T] {
	return newQuery[T]().WithTx(tx)
}

// Find retrieves a record by primary key.
func (ImmutableModel[T]) Find(id any) (*T, error) {
	var model T
	q := newQuery[T]()
	if err := q.Where("id = ?", id).First(&model); err != nil {
		return nil, err
	}
	return &model, nil
}

// FindBy retrieves a record by a specific field.
func (ImmutableModel[T]) FindBy(field string, value any) (*T, error) {
	if err := validateIdentifier(field); err != nil {
		return nil, err
	}
	var model T
	q := newQuery[T]()
	if err := q.Where(field+" = ?", value).First(&model); err != nil {
		return nil, err
	}
	return &model, nil
}

// First retrieves the first record.
func (ImmutableModel[T]) First() (*T, error) {
	var model T
	q := newQuery[T]()
	if err := q.First(&model); err != nil {
		return nil, err
	}
	return &model, nil
}

// Last retrieves the last record (by id descending).
func (ImmutableModel[T]) Last() (*T, error) {
	var model T
	q := newQuery[T]()
	if err := q.OrderBy("id", "DESC").First(&model); err != nil {
		return nil, err
	}
	return &model, nil
}

// All retrieves all records.
func (ImmutableModel[T]) All() ([]T, error) {
	return newQuery[T]().Get()
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

// Create inserts a new record. Accepts a map[string]any or a *T.
func (ImmutableModel[T]) Create(data any) (*T, error) {
	switch v := data.(type) {
	case map[string]any:
		model := new(T)
		if err := mapToStruct(v, model); err != nil {
			return nil, err
		}
		if err := Save(nil, model); err != nil {
			return nil, err
		}
		return model, nil
	case *T:
		if err := applyFillableToStruct(v); err != nil {
			return nil, err
		}
		if err := Save(nil, v); err != nil {
			return nil, err
		}
		return v, nil
	default:
		return nil, errors.New("unsupported data type for create")
	}
}

// CreateMany inserts multiple records.
func (ImmutableModel[T]) CreateMany(records []T) error {
	for i := range records {
		if err := Save(nil, &records[i]); err != nil {
			return err
		}
	}
	return nil
}

// Count returns the number of records.
func (ImmutableModel[T]) Count() (int, error) {
	return newQuery[T]().Count()
}

// Exists checks if any records exist.
func (ImmutableModel[T]) Exists() bool {
	count, _ := newQuery[T]().Count()
	return count > 0
}

// Paginate returns a paginated result for all records.
func (ImmutableModel[T]) Paginate(page, perPage int) (*PaginatedResult[T], error) {
	return newQuery[T]().Paginate(page, perPage)
}

// Pluck retrieves a single column's values.
func (ImmutableModel[T]) Pluck(column string) ([]any, error) {
	return newQuery[T]().Pluck(column)
}

// Save is decorative on the embedded *ImmutableModel[T] receiver and
// always returns an error. This receiver cannot resolve the parent
// struct, table name, or hooks via reflection; persisting an immutable
// record requires the package-level helper:
//
//	err := orm.Save(manager, &record)
//
// On an already-persisted record, Save returns ErrImmutableModelUpdate
// (immutable models are append-only). On a new record it returns an
// error directing callers to orm.Save. The same trap exists on the
// regular Model[T].Save(); the package-level orm.Save is the one true
// path for both.
func (m *ImmutableModel[T]) Save() error {
	if m.IsExisting {
		return ErrImmutableModelUpdate
	}
	return errors.New("orm: ImmutableModel.Save requires the parent struct; call orm.Save(manager, &record)")
}

// ---------------------------------------------------------------------------
// ImmutableUUIDModel[T] static helpers
// ---------------------------------------------------------------------------

// WithContext returns a *Query[T] bound to ctx.
func (ImmutableUUIDModel[T]) WithContext(ctx context.Context) *Query[T] {
	return newQuery[T]().WithContext(ctx)
}

// WithTx binds the query chain to tx. See ImmutableModel[T].WithTx.
func (ImmutableUUIDModel[T]) WithTx(tx *sql.Tx) *Query[T] {
	return newQuery[T]().WithTx(tx)
}

// Find retrieves a record by UUID primary key.
func (ImmutableUUIDModel[T]) Find(id string) (*T, error) {
	var model T
	q := newQuery[T]()
	if err := q.Where("id = ?", id).First(&model); err != nil {
		return nil, err
	}
	return &model, nil
}

// FindBy retrieves a record by a specific field.
func (ImmutableUUIDModel[T]) FindBy(field string, value any) (*T, error) {
	if err := validateIdentifier(field); err != nil {
		return nil, err
	}
	var model T
	q := newQuery[T]()
	if err := q.Where(field+" = ?", value).First(&model); err != nil {
		return nil, err
	}
	return &model, nil
}

// First retrieves the first record.
func (ImmutableUUIDModel[T]) First() (*T, error) {
	var model T
	q := newQuery[T]()
	if err := q.First(&model); err != nil {
		return nil, err
	}
	return &model, nil
}

// All retrieves all records.
func (ImmutableUUIDModel[T]) All() ([]T, error) {
	return newQuery[T]().Get()
}

// Where starts a query with a WHERE condition.
func (ImmutableUUIDModel[T]) Where(condition string, args ...any) *Query[T] {
	return newQuery[T]().Where(condition, args...)
}

// OrderBy starts a query with an ORDER BY clause.
func (ImmutableUUIDModel[T]) OrderBy(column, direction string) *Query[T] {
	return newQuery[T]().OrderBy(column, direction)
}

// Create inserts a new record. Accepts a map[string]any or a *T.
func (ImmutableUUIDModel[T]) Create(data any) (*T, error) {
	switch v := data.(type) {
	case map[string]any:
		model := new(T)
		if err := mapToStruct(v, model); err != nil {
			return nil, err
		}
		if err := Save(nil, model); err != nil {
			return nil, err
		}
		return model, nil
	case *T:
		if err := applyFillableToStruct(v); err != nil {
			return nil, err
		}
		if err := Save(nil, v); err != nil {
			return nil, err
		}
		return v, nil
	default:
		return nil, errors.New("unsupported data type for create")
	}
}

// CreateMany inserts multiple records.
func (ImmutableUUIDModel[T]) CreateMany(records []T) error {
	for i := range records {
		if err := Save(nil, &records[i]); err != nil {
			return err
		}
	}
	return nil
}

// Count returns the number of records.
func (ImmutableUUIDModel[T]) Count() (int, error) {
	return newQuery[T]().Count()
}

// Save is decorative on the embedded *ImmutableUUIDModel[T] receiver
// and always returns an error. Use orm.Save(manager, &record), the
// package-level helper is the one true path. On a persisted record,
// Save returns ErrImmutableModelUpdate (immutable models are
// append-only). See *ImmutableModel[T].Save for the longer note.
func (m *ImmutableUUIDModel[T]) Save() error {
	if m.IsExisting {
		return ErrImmutableModelUpdate
	}
	return errors.New("orm: ImmutableUUIDModel.Save requires the parent struct; call orm.Save(manager, &record)")
}

// ---------------------------------------------------------------------------
// Save-side wiring: insert-only path for embedded ImmutableModel variants.
// ---------------------------------------------------------------------------

// saveImmutableModel handles auto-increment-id immutable inserts. There
// is no update branch: callers reaching here with isInsert=false get
// ErrImmutableModelUpdate.
func saveImmutableModel[T any](drv drivers.Driver, model *T, modelField, idField, existsField reflect.Value, tableName string, isInsert bool) error {
	if !isInsert {
		return ErrImmutableModelUpdate
	}

	modelField.FieldByName("CreatedAt").Set(reflect.ValueOf(time.Now()))

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
	lastID, err := q.InsertGetId(data)
	if err != nil {
		return err
	}

	idField.SetUint(uint64(lastID))
	existsField.SetBool(true)

	if hook, ok := any(model).(AfterCreateHook); ok {
		if err := hook.AfterCreate(); err != nil {
			return err
		}
	}
	return nil
}

// saveImmutableUUIDModel handles UUID-keyed immutable inserts.
func saveImmutableUUIDModel[T any](drv drivers.Driver, model *T, modelField, idField, existsField reflect.Value, tableName string, isInsert bool) error {
	if !isInsert {
		return ErrImmutableModelUpdate
	}

	if idField.String() == "" {
		idField.SetString(uuid.New().String())
	}
	modelField.FieldByName("CreatedAt").Set(reflect.ValueOf(time.Now()))

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
	if _, err := q.InsertGetId(data); err != nil {
		return err
	}

	existsField.SetBool(true)

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
