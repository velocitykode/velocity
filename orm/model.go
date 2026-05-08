package orm

import (
	"context"
	"database/sql"
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

// Model is the generic base model that provides static query methods on the type itself.
// By default, models do NOT have soft deletes. Use SoftDeleteModel for soft delete support.
type Model[T any] struct {
	ID        uint      `orm:"primaryKey;autoIncrement" json:"id"`
	CreatedAt time.Time `orm:"autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `orm:"autoUpdateTime" json:"updated_at"`

	// Internal fields (not persisted)
	IsExisting bool            `orm:"-" json:"-"`
	Original   map[string]any  `orm:"-" json:"-"`
	Changed    map[string]bool `orm:"-" json:"-"`
}

// UUIDModel is a generic base model with UUID primary key for distributed systems
// and external-facing APIs where sequential IDs pose security risks.
// By default, models do NOT have soft deletes. Use SoftDeleteUUIDModel for soft delete support.
type UUIDModel[T any] struct {
	ID        string    `orm:"primaryKey;type:uuid" json:"id"`
	CreatedAt time.Time `orm:"autoCreateTime" json:"created_at"`
	UpdatedAt time.Time `orm:"autoUpdateTime" json:"updated_at"`

	// Internal fields (not persisted)
	IsExisting bool            `orm:"-" json:"-"`
	Original   map[string]any  `orm:"-" json:"-"`
	Changed    map[string]bool `orm:"-" json:"-"`
}

// SoftDeleteModel is a base model WITH soft delete support.
// Use this when you need to keep deleted records (e.g., users, orders, audit trails).
type SoftDeleteModel[T any] struct {
	ID        uint       `orm:"primaryKey;autoIncrement" json:"id"`
	CreatedAt time.Time  `orm:"autoCreateTime" json:"created_at"`
	UpdatedAt time.Time  `orm:"autoUpdateTime" json:"updated_at"`
	DeletedAt *time.Time `orm:"index" json:"deleted_at,omitempty"`

	// Internal fields (not persisted)
	IsExisting bool            `orm:"-" json:"-"`
	Original   map[string]any  `orm:"-" json:"-"`
	Changed    map[string]bool `orm:"-" json:"-"`
}

// SoftDeleteUUIDModel is a UUID primary key model WITH soft delete support.
type SoftDeleteUUIDModel[T any] struct {
	ID        string     `orm:"primaryKey;type:uuid" json:"id"`
	CreatedAt time.Time  `orm:"autoCreateTime" json:"created_at"`
	UpdatedAt time.Time  `orm:"autoUpdateTime" json:"updated_at"`
	DeletedAt *time.Time `orm:"index" json:"deleted_at,omitempty"`

	// Internal fields (not persisted)
	IsExisting bool            `orm:"-" json:"-"`
	Original   map[string]any  `orm:"-" json:"-"`
	Changed    map[string]bool `orm:"-" json:"-"`
}

// ImmutableModel is an append-only base model. It has CreatedAt but no
// UpdatedAt and exposes no Update/Save-as-update path, so embedded
// structs can read and create rows but cannot mutate them. Tables like
// audit_logs that have no `updated_at` column should embed this rather
// than Model[T] (whose Save/Update unconditionally stamp updated_at and
// fail at the driver against missing columns).
//
// The static helpers (Find, FindBy, First, Last, All, Where, ...) and
// the Save() instance method (insert-only) are provided. Update,
// DeleteWhere, and the soft-delete primitives are intentionally omitted.
type ImmutableModel[T any] struct {
	ID        uint      `orm:"primaryKey;autoIncrement" json:"id"`
	CreatedAt time.Time `orm:"autoCreateTime" json:"created_at"`

	// Internal fields (not persisted)
	IsExisting bool `orm:"-" json:"-"`
}

// ImmutableUUIDModel is the UUID-keyed counterpart of ImmutableModel.
type ImmutableUUIDModel[T any] struct {
	ID        string    `orm:"primaryKey;type:uuid" json:"id"`
	CreatedAt time.Time `orm:"autoCreateTime" json:"created_at"`

	// Internal fields (not persisted)
	IsExisting bool `orm:"-" json:"-"`
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

// Save inserts or updates the model. The receiver-style helper cannot
// resolve the embedding parent's reflection metadata, so callers must
// reach the package-level Save(ctx, manager, model) entry point. This
// stub is preserved as a no-op error to surface a clear compile-time
// signal: the form `model.Save()` is no longer supported because it
// would silently auto-commit outside any in-flight transaction.
func (m *Model[T]) Save() error {
	if m.IsExisting {
		return m.update()
	}
	return m.insert()
}

// Delete permanently deletes the model by primary key. Takes ctx as
// the first argument so transaction enrollment is mandatory and
// explicit.
func (m *Model[T]) Delete(ctx context.Context) error {
	if !m.IsExisting {
		return errors.New("cannot delete non-existent model")
	}

	query := newQuery[T]()
	_, err := query.Where("id = ?", m.ID).ForceDelete(ctx)
	return err
}

// Refresh reloads the model from database. Takes ctx as the first
// argument so the read participates in the caller's transaction when
// ctx carries one.
func (m *Model[T]) Refresh(ctx context.Context) error {
	if !m.IsExisting {
		return errors.New("cannot refresh non-existent model")
	}
	fresh, err := Model[T]{}.Find(ctx, m.ID)
	if err != nil {
		return err
	}

	// Copy fresh data to current model
	reflect.ValueOf(m).Elem().Set(reflect.ValueOf(fresh).Elem())
	return nil
}

// HasChanged checks if a field has changed
func (m *Model[T]) HasChanged(field string) bool {
	if m.Changed == nil {
		return false
	}
	return m.Changed[field]
}

// GetChanges returns all changed fields
func (m *Model[T]) GetChanges() map[string]any {
	changes := make(map[string]any)
	if m.Changed == nil {
		return changes
	}

	v := reflect.ValueOf(m).Elem()
	t := v.Type()

	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		if m.Changed[field.Name] {
			changes[field.Name] = v.Field(i).Interface()
		}
	}

	return changes
}

// IsDirty checks if the model has any unsaved changes
func (m *Model[T]) IsDirty() bool {
	return len(m.Changed) > 0
}

// IsClean checks if the model has no unsaved changes
func (m *Model[T]) IsClean() bool {
	return !m.IsDirty()
}

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

// Last retrieves the last record (by created_at descending). Takes ctx
// as the first argument.
func (UUIDModel[T]) Last(ctx context.Context) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.OrderBy("created_at", "DESC").First(ctx, &model)
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

// Save delegates to the package-level Save; the legacy zero-arg form
// is preserved as a no-op shim that returns the canonical error.
func (m *UUIDModel[T]) Save() error {
	if m.IsExisting {
		return m.update()
	}
	return m.insert()
}

// Delete permanently deletes the model by primary key. Takes ctx as
// the first argument so transaction enrollment is mandatory and
// explicit.
func (m *UUIDModel[T]) Delete(ctx context.Context) error {
	if !m.IsExisting {
		return errors.New("cannot delete non-existent model")
	}

	query := newQuery[T]()
	_, err := query.Where("id = ?", m.ID).ForceDelete(ctx)
	return err
}

// Refresh reloads the model from database. Takes ctx as the first
// argument so the read participates in the caller's transaction when
// ctx carries one.
func (m *UUIDModel[T]) Refresh(ctx context.Context) error {
	if !m.IsExisting {
		return errors.New("cannot refresh non-existent model")
	}
	fresh, err := UUIDModel[T]{}.Find(ctx, m.ID)
	if err != nil {
		return err
	}

	// Copy fresh data to current model
	reflect.ValueOf(m).Elem().Set(reflect.ValueOf(fresh).Elem())
	return nil
}

// HasChanged checks if a field has changed
func (m *UUIDModel[T]) HasChanged(field string) bool {
	if m.Changed == nil {
		return false
	}
	return m.Changed[field]
}

// GetChanges returns all changed fields
func (m *UUIDModel[T]) GetChanges() map[string]any {
	changes := make(map[string]any)
	if m.Changed == nil {
		return changes
	}

	v := reflect.ValueOf(m).Elem()
	t := v.Type()

	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		if m.Changed[field.Name] {
			changes[field.Name] = v.Field(i).Interface()
		}
	}

	return changes
}

// IsDirty checks if the model has any unsaved changes
func (m *UUIDModel[T]) IsDirty() bool {
	return len(m.Changed) > 0
}

// IsClean checks if the model has no unsaved changes
func (m *UUIDModel[T]) IsClean() bool {
	return !m.IsDirty()
}

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

// Save (zero-arg) is a legacy stub that returns the canonical
// "use orm.Save" error so callers can grep for the migration.
func (m *SoftDeleteModel[T]) Save() error {
	if m.IsExisting {
		return m.update()
	}
	return m.insert()
}

// Delete soft deletes the model by setting DeletedAt. Takes ctx as
// the first argument so transaction enrollment is mandatory and
// explicit.
func (m *SoftDeleteModel[T]) Delete(ctx context.Context) error {
	if !m.IsExisting {
		return errors.New("cannot delete non-existent model")
	}

	query := newQuery[T]()
	_, err := query.Where("id = ?", m.ID).Delete(ctx)
	if err == nil {
		now := time.Now()
		m.DeletedAt = &now
	}
	return err
}

// ForceDelete permanently deletes the model. Takes ctx as the first
// argument so transaction enrollment is mandatory and explicit.
func (m *SoftDeleteModel[T]) ForceDelete(ctx context.Context) error {
	if !m.IsExisting {
		return errors.New("cannot delete non-existent model")
	}

	query := newQuery[T]()
	_, err := query.Where("id = ?", m.ID).ForceDelete(ctx)
	return err
}

// Restore restores a soft deleted model by clearing DeletedAt. Takes
// ctx as the first argument.
func (m *SoftDeleteModel[T]) Restore(ctx context.Context) error {
	if !m.IsExisting {
		return errors.New("cannot restore non-existent model")
	}

	query := newQuery[T]()
	_, err := query.WithTrashed().Where("id = ?", m.ID).Update(ctx, map[string]any{"deleted_at": nil})
	if err == nil {
		m.DeletedAt = nil
	}
	return err
}

// Refresh reloads the model from database. Takes ctx as the first
// argument so the read participates in the caller's transaction when
// ctx carries one.
func (m *SoftDeleteModel[T]) Refresh(ctx context.Context) error {
	if !m.IsExisting {
		return errors.New("cannot refresh non-existent model")
	}
	fresh, err := SoftDeleteModel[T]{}.Find(ctx, m.ID)
	if err != nil {
		return err
	}

	reflect.ValueOf(m).Elem().Set(reflect.ValueOf(fresh).Elem())
	return nil
}

// HasChanged checks if a field has changed
func (m *SoftDeleteModel[T]) HasChanged(field string) bool {
	if m.Changed == nil {
		return false
	}
	return m.Changed[field]
}

// GetChanges returns all changed fields
func (m *SoftDeleteModel[T]) GetChanges() map[string]any {
	changes := make(map[string]any)
	if m.Changed == nil {
		return changes
	}

	v := reflect.ValueOf(m).Elem()
	t := v.Type()

	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		if m.Changed[field.Name] {
			changes[field.Name] = v.Field(i).Interface()
		}
	}

	return changes
}

// IsDirty checks if the model has any unsaved changes
func (m *SoftDeleteModel[T]) IsDirty() bool {
	return len(m.Changed) > 0
}

// IsClean checks if the model has no unsaved changes
func (m *SoftDeleteModel[T]) IsClean() bool {
	return !m.IsDirty()
}

func (m *SoftDeleteModel[T]) insert() error {
	m.CreatedAt = time.Now()
	m.UpdatedAt = time.Now()
	return errors.New("direct insert on SoftDeleteModel not supported - use orm.Save()")
}

func (m *SoftDeleteModel[T]) update() error {
	m.UpdatedAt = time.Now()
	return errors.New("direct update on SoftDeleteModel not supported - use orm.Save()")
}

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

// Last retrieves the last record (by created_at descending). Takes ctx
// as the first argument.
func (SoftDeleteUUIDModel[T]) Last(ctx context.Context) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.OrderBy("created_at", "DESC").First(ctx, &model)
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

// Save (zero-arg) is a legacy stub that returns the canonical
// "use orm.Save" error so callers can grep for the migration.
func (m *SoftDeleteUUIDModel[T]) Save() error {
	if m.IsExisting {
		return m.update()
	}
	return m.insert()
}

// Delete soft deletes the model. Takes ctx as the first argument.
func (m *SoftDeleteUUIDModel[T]) Delete(ctx context.Context) error {
	if !m.IsExisting {
		return errors.New("cannot delete non-existent model")
	}

	query := newQuery[T]()
	_, err := query.Where("id = ?", m.ID).Delete(ctx)
	if err == nil {
		now := time.Now()
		m.DeletedAt = &now
	}
	return err
}

// ForceDelete permanently deletes the model. Takes ctx as the first
// argument.
func (m *SoftDeleteUUIDModel[T]) ForceDelete(ctx context.Context) error {
	if !m.IsExisting {
		return errors.New("cannot delete non-existent model")
	}

	query := newQuery[T]()
	_, err := query.Where("id = ?", m.ID).ForceDelete(ctx)
	return err
}

// Restore restores a soft deleted model. Takes ctx as the first
// argument.
func (m *SoftDeleteUUIDModel[T]) Restore(ctx context.Context) error {
	if !m.IsExisting {
		return errors.New("cannot restore non-existent model")
	}

	query := newQuery[T]()
	_, err := query.WithTrashed().Where("id = ?", m.ID).Update(ctx, map[string]any{"deleted_at": nil})
	if err == nil {
		m.DeletedAt = nil
	}
	return err
}

// Refresh reloads the model from database. Takes ctx as the first
// argument.
func (m *SoftDeleteUUIDModel[T]) Refresh(ctx context.Context) error {
	if !m.IsExisting {
		return errors.New("cannot refresh non-existent model")
	}
	fresh, err := SoftDeleteUUIDModel[T]{}.Find(ctx, m.ID)
	if err != nil {
		return err
	}

	reflect.ValueOf(m).Elem().Set(reflect.ValueOf(fresh).Elem())
	return nil
}

// HasChanged checks if a field has changed
func (m *SoftDeleteUUIDModel[T]) HasChanged(field string) bool {
	if m.Changed == nil {
		return false
	}
	return m.Changed[field]
}

// GetChanges returns all changed fields
func (m *SoftDeleteUUIDModel[T]) GetChanges() map[string]any {
	changes := make(map[string]any)
	if m.Changed == nil {
		return changes
	}

	v := reflect.ValueOf(m).Elem()
	t := v.Type()

	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		if m.Changed[field.Name] {
			changes[field.Name] = v.Field(i).Interface()
		}
	}

	return changes
}

// IsDirty checks if the model has any unsaved changes
func (m *SoftDeleteUUIDModel[T]) IsDirty() bool {
	return len(m.Changed) > 0
}

// IsClean checks if the model has no unsaved changes
func (m *SoftDeleteUUIDModel[T]) IsClean() bool {
	return !m.IsDirty()
}

func (m *SoftDeleteUUIDModel[T]) insert() error {
	m.CreatedAt = time.Now()
	m.UpdatedAt = time.Now()
	return errors.New("direct insert on SoftDeleteUUIDModel not supported - use orm.Save()")
}

func (m *SoftDeleteUUIDModel[T]) update() error {
	m.UpdatedAt = time.Now()
	return errors.New("direct update on SoftDeleteUUIDModel not supported - use orm.Save()")
}

// UUIDModel private methods

func (m *UUIDModel[T]) insert() error {
	m.CreatedAt = time.Now()
	m.UpdatedAt = time.Now()
	return errors.New("direct insert on UUIDModel not supported - use orm.Save()")
}

func (m *UUIDModel[T]) update() error {
	m.UpdatedAt = time.Now()
	return errors.New("direct update on UUIDModel not supported - use orm.Save()")
}

// Private methods

func (m *Model[T]) insert() error {
	m.CreatedAt = time.Now()
	m.UpdatedAt = time.Now()

	// For embedded generics, we can't directly get the parent struct
	// This method should be overridden or use the global Save function
	return errors.New("direct insert on Model not supported - use the model's Save method or orm.Save()")
}

func (m *Model[T]) update() error {
	m.UpdatedAt = time.Now()

	// For embedded generics, we can't directly get the parent struct
	// This method should be overridden or use the global Save function
	return errors.New("direct update on Model not supported - use the model's Save method or orm.Save()")
}

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
		fv := v.FieldByIndex(col.IndexPath)
		if !fv.IsValid() {
			continue
		}

		// Slice/array gating. Non-byte slices are relation payloads
		// and never persist; byte slices/arrays are scalars (bytea,
		// hashes) unless tagged JSON. A nil byte slice on a non-JSON
		// column is dropped so the DB default applies.
		switch fv.Kind() {
		case reflect.Slice, reflect.Array:
			isByteSeq := fv.Type().Elem().Kind() == reflect.Uint8
			if !col.IsJSON && !isByteSeq {
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
	// Stamp Manager's TxRecover dispatcher onto ctx so an inline
	// AfterCommit-hook panic surfaces a TxRecover event identical to
	// the in-Transaction path. Without this stamping, a panic in an
	// AfterCommit hook fired through the auto-commit branch would
	// only land on os.Stderr.
	ctx = withTxRecoverDispatcher(ctx, func(ev *TxRecover) { m.dispatchEvent(ctx, ev) })
	return saveWithDriver(ctx, drv, model)
}

// saveWithDriver is the driver-bound persistence entry. It carries the
// reflection + dispatch logic of Save; the public Save resolves the
// driver from a *Manager, while Query.Save / Query.Create reach this
// helper directly using their own (possibly tx-bound) q.driver.
func saveWithDriver[T any](ctx context.Context, drv drivers.Driver, model *T) error {
	v := reflect.ValueOf(model).Elem()
	t := v.Type()

	// Find the embedded base model field. Order matters: more-specific
	// types (SoftDeleteUUIDModel, ImmutableUUIDModel) must be checked
	// before the type-prefix match for the simpler variants would also
	// satisfy a substring check (which it doesn't here, but the explicit
	// ordering documents intent).
	var modelField reflect.Value
	var isUUIDModel bool
	var isSoftDeleteModel bool
	var isImmutable bool
	var found bool

	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		typeName := field.Type.String()
		if strings.HasPrefix(typeName, "orm.SoftDeleteUUIDModel[") {
			modelField = v.Field(i)
			isUUIDModel = true
			isSoftDeleteModel = true
			found = true
			break
		}
		if strings.HasPrefix(typeName, "orm.ImmutableUUIDModel[") {
			modelField = v.Field(i)
			isUUIDModel = true
			isImmutable = true
			found = true
			break
		}
		if strings.HasPrefix(typeName, "orm.UUIDModel[") {
			modelField = v.Field(i)
			isUUIDModel = true
			found = true
			break
		}
		if strings.HasPrefix(typeName, "orm.SoftDeleteModel[") {
			modelField = v.Field(i)
			isSoftDeleteModel = true
			found = true
			break
		}
		if strings.HasPrefix(typeName, "orm.ImmutableModel[") {
			modelField = v.Field(i)
			isImmutable = true
			found = true
			break
		}
		if strings.HasPrefix(typeName, "orm.Model[") {
			modelField = v.Field(i)
			found = true
			break
		}
	}

	if !found {
		return errors.New("model does not embed orm.Model, orm.UUIDModel, orm.SoftDeleteModel, orm.SoftDeleteUUIDModel, orm.ImmutableModel, or orm.ImmutableUUIDModel")
	}

	_ = isSoftDeleteModel // Used for future optimizations if needed

	// Get the table name
	tableName := toSnakeCase(t.Name()) + "s" // pluralize
	if tableNamer, ok := any(model).(interface{ TableName() string }); ok {
		tableName = tableNamer.TableName()
	}

	// Check if it's insert or update
	idField := modelField.FieldByName("ID")
	existsField := modelField.FieldByName("IsExisting")
	isInsert := !existsField.Bool()

	var err error
	switch {
	case isImmutable && isUUIDModel:
		err = saveImmutableUUIDModel(ctx, drv, model, modelField, idField, existsField, tableName, isInsert)
	case isImmutable:
		err = saveImmutableModel(ctx, drv, model, modelField, idField, existsField, tableName, isInsert)
	case isUUIDModel:
		err = saveUUIDModel(ctx, drv, model, modelField, idField, existsField, tableName, isInsert)
	default:
		err = saveModel(ctx, drv, model, modelField, idField, existsField, tableName, isInsert)
	}
	if err == nil {
		// Wire model AfterCommit / AfterRollback hooks. Inside a
		// surrounding Manager.Transaction the registration accumulates
		// against the active TxCallbacks list; outside one, AfterCommit
		// fires inline because the auto-commit already happened.
		registerModelAfterCommit(ctx, model)
	}
	return err
}

// saveModel handles saving for auto-increment ID models
func saveModel[T any](ctx context.Context, drv drivers.Driver, model *T, modelField, idField, existsField reflect.Value, tableName string, isInsert bool) error {
	if isInsert {
		// Set timestamps: respect caller-set CreatedAt; only stamp when zero.
		// UpdatedAt mirrors CreatedAt on insert for consistency.
		createdAtField := modelField.FieldByName("CreatedAt")
		updatedAtField := modelField.FieldByName("UpdatedAt")
		createdAt := createdAtField.Interface().(time.Time)
		if createdAt.IsZero() {
			createdAt = time.Now()
			createdAtField.Set(reflect.ValueOf(createdAt))
		}
		if updatedAtField.Interface().(time.Time).IsZero() {
			updatedAtField.Set(reflect.ValueOf(createdAt))
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
		existsField.SetBool(true)

		// Call AfterCreate hook if exists
		if hook, ok := any(model).(AfterCreateHook); ok {
			if err := hook.AfterCreate(); err != nil {
				return err
			}
		}
	} else {
		// Update existing record
		modelField.FieldByName("UpdatedAt").Set(reflect.ValueOf(time.Now()))

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

		_, err := query.Where("id = ?", idField.Uint()).Update(ctx, data)
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
func saveUUIDModel[T any](ctx context.Context, drv drivers.Driver, model *T, modelField, idField, existsField reflect.Value, tableName string, isInsert bool) error {
	if isInsert {
		// Generate UUID if not already set
		if idField.String() == "" {
			idField.SetString(uuid.New().String())
		}

		// Set timestamps: respect caller-set CreatedAt; only stamp when zero.
		// UpdatedAt mirrors CreatedAt on insert for consistency.
		createdAtField := modelField.FieldByName("CreatedAt")
		updatedAtField := modelField.FieldByName("UpdatedAt")
		createdAt := createdAtField.Interface().(time.Time)
		if createdAt.IsZero() {
			createdAt = time.Now()
			createdAtField.Set(reflect.ValueOf(createdAt))
		}
		if updatedAtField.Interface().(time.Time).IsZero() {
			updatedAtField.Set(reflect.ValueOf(createdAt))
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

		existsField.SetBool(true)

		// Call AfterCreate hook if exists
		if hook, ok := any(model).(AfterCreateHook); ok {
			if err := hook.AfterCreate(); err != nil {
				return err
			}
		}
	} else {
		// Update existing record
		modelField.FieldByName("UpdatedAt").Set(reflect.ValueOf(time.Now()))

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

		_, err := query.Where("id = ?", idField.String()).Update(ctx, data)
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
