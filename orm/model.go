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

// WithContext returns a *Query[T] bound to ctx so static-like helpers
// chained off it (Where, OrderBy, First, Get, Count, Pluck, ...) propagate
// the context to the driver. This is the chain entry point for callers
// who want to attach a request/transaction context to the Find/All/etc.
// helpers exposed on Model[T] (which are context-blind by themselves).
//
// Example:
//
//	var u User
//	if err := orm.Model[User]{}.WithContext(ctx).Where("id = ?", id).First(&u); err != nil {
//	    return err
//	}
//
// Returns *Query[T] rather than Model[T] so the rest of the chain has the
// full builder API (Where, GroupBy, Limit, ...) available.
func (Model[T]) WithContext(ctx context.Context) *Query[T] {
	return newQuery[T]().WithContext(ctx)
}

// Find retrieves a record by primary key
func (Model[T]) Find(id any) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.Where("id = ?", id).First(&model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// FindBy retrieves a record by a specific field
func (Model[T]) FindBy(field string, value any) (*T, error) {
	if err := validateIdentifier(field); err != nil {
		return nil, err
	}
	var model T
	query := newQuery[T]()
	err := query.Where(field+" = ?", value).First(&model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// First retrieves the first record
func (Model[T]) First() (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.First(&model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// Last retrieves the last record (by id descending)
func (Model[T]) Last() (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.OrderBy("id", "DESC").First(&model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// All retrieves all records
func (Model[T]) All() ([]T, error) {
	query := newQuery[T]()
	return query.Get()
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

// Create inserts a new record.
// Accepts a map[string]any or a *T. Requires a *Manager - use orm.Save(manager, model) directly.
func (Model[T]) Create(data any) (*T, error) {
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
		// Ensure fillable/guarded gates also apply to pre-constructed
		// model pointers so mass-assignment protection cannot be
		// bypassed by callers who build the struct manually.
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
// Requires a *Manager - use orm.Save(manager, model) directly.
func (Model[T]) CreateMany(records []T) error {
	for _, record := range records {
		if err := Save(nil, &record); err != nil {
			return err
		}
	}
	return nil
}

// Count returns the number of records
func (Model[T]) Count() (int, error) {
	query := newQuery[T]()
	return query.Count()
}

// Exists checks if any records exist
func (Model[T]) Exists() bool {
	count, _ := Model[T]{}.Count()
	return count > 0
}

// Paginate returns a paginated result for all records
func (Model[T]) Paginate(page, perPage int) (*PaginatedResult[T], error) {
	query := newQuery[T]()
	return query.Paginate(page, perPage)
}

// Raw creates a raw SQL query builder for executing custom queries
// Usage: Model{}.Raw("SELECT * FROM users WHERE id = ?", 1).First(&user)
func (Model[T]) Raw(sql string, args ...any) *RawQuery[T] {
	return NewRawQuery[T](sql, args...)
}

// Pluck retrieves a single column values
func (Model[T]) Pluck(column string) ([]any, error) {
	query := newQuery[T]()
	return query.Pluck(column)
}

// Update updates records matching conditions
func (Model[T]) Update(conditions map[string]any, updates map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.Update(updates)
}

// DeleteWhere permanently deletes records matching conditions
func (Model[T]) DeleteWhere(conditions map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.ForceDelete()
}

// Instance methods

// Save inserts or updates the model
func (m *Model[T]) Save() error {
	if m.IsExisting {
		return m.update()
	}
	return m.insert()
}

// Delete permanently deletes the model
func (m *Model[T]) Delete() error {
	if !m.IsExisting {
		return errors.New("cannot delete non-existent model")
	}

	query := newQuery[T]()
	_, err := query.Where("id = ?", m.ID).ForceDelete()
	return err
}

// Refresh reloads the model from database
func (m *Model[T]) Refresh() error {
	if !m.IsExisting {
		return errors.New("cannot refresh non-existent model")
	}

	fresh, err := Model[T]{}.Find(m.ID)
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

// WithContext returns a *Query[T] bound to ctx so static-like helpers
// chained off it propagate context to the driver. See Model[T].WithContext.
func (UUIDModel[T]) WithContext(ctx context.Context) *Query[T] {
	return newQuery[T]().WithContext(ctx)
}

// Find retrieves a record by UUID primary key
func (UUIDModel[T]) Find(id string) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.Where("id = ?", id).First(&model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// FindBy retrieves a record by a specific field
func (UUIDModel[T]) FindBy(field string, value any) (*T, error) {
	if err := validateIdentifier(field); err != nil {
		return nil, err
	}
	var model T
	query := newQuery[T]()
	err := query.Where(field+" = ?", value).First(&model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// First retrieves the first record
func (UUIDModel[T]) First() (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.First(&model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// Last retrieves the last record (by created_at descending)
func (UUIDModel[T]) Last() (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.OrderBy("created_at", "DESC").First(&model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// All retrieves all records
func (UUIDModel[T]) All() ([]T, error) {
	query := newQuery[T]()
	return query.Get()
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

// Create inserts a new record.
// Accepts a map[string]any or a *T.
func (UUIDModel[T]) Create(data any) (*T, error) {
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
		// Ensure fillable/guarded gates also apply to pre-constructed
		// model pointers so mass-assignment protection cannot be
		// bypassed by callers who build the struct manually.
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

// CreateMany inserts multiple records
func (UUIDModel[T]) CreateMany(records []T) error {
	for _, record := range records {
		if err := Save(nil, &record); err != nil {
			return err
		}
	}
	return nil
}

// Count returns the number of records
func (UUIDModel[T]) Count() (int, error) {
	query := newQuery[T]()
	return query.Count()
}

// Exists checks if any records exist
func (UUIDModel[T]) Exists() bool {
	count, _ := UUIDModel[T]{}.Count()
	return count > 0
}

// Paginate returns a paginated result for all records
func (UUIDModel[T]) Paginate(page, perPage int) (*PaginatedResult[T], error) {
	query := newQuery[T]()
	return query.Paginate(page, perPage)
}

// Raw creates a raw SQL query builder for executing custom queries
// Usage: UUIDModel{}.Raw("SELECT * FROM users WHERE id = ?", 1).First(&user)
func (UUIDModel[T]) Raw(sql string, args ...any) *RawQuery[T] {
	return NewRawQuery[T](sql, args...)
}

// Pluck retrieves a single column values
func (UUIDModel[T]) Pluck(column string) ([]any, error) {
	query := newQuery[T]()
	return query.Pluck(column)
}

// Update updates records matching conditions
func (UUIDModel[T]) Update(conditions map[string]any, updates map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.Update(updates)
}

// DeleteWhere permanently deletes records matching conditions
func (UUIDModel[T]) DeleteWhere(conditions map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.ForceDelete()
}

// UUIDModel instance methods

// Save inserts or updates the model
func (m *UUIDModel[T]) Save() error {
	if m.IsExisting {
		return m.update()
	}
	return m.insert()
}

// Delete permanently deletes the model
func (m *UUIDModel[T]) Delete() error {
	if !m.IsExisting {
		return errors.New("cannot delete non-existent model")
	}

	query := newQuery[T]()
	_, err := query.Where("id = ?", m.ID).ForceDelete()
	return err
}

// Refresh reloads the model from database
func (m *UUIDModel[T]) Refresh() error {
	if !m.IsExisting {
		return errors.New("cannot refresh non-existent model")
	}

	fresh, err := UUIDModel[T]{}.Find(m.ID)
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

// WithContext returns a *Query[T] bound to ctx so static-like helpers
// chained off it propagate context to the driver. See Model[T].WithContext.
func (SoftDeleteModel[T]) WithContext(ctx context.Context) *Query[T] {
	return newQuery[T]().WithContext(ctx)
}

// Find retrieves a record by primary key
func (SoftDeleteModel[T]) Find(id any) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.Where("id = ?", id).First(&model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// FindBy retrieves a record by a specific field
func (SoftDeleteModel[T]) FindBy(field string, value any) (*T, error) {
	if err := validateIdentifier(field); err != nil {
		return nil, err
	}
	var model T
	query := newQuery[T]()
	err := query.Where(field+" = ?", value).First(&model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// First retrieves the first record
func (SoftDeleteModel[T]) First() (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.First(&model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// Last retrieves the last record (by id descending)
func (SoftDeleteModel[T]) Last() (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.OrderBy("id", "DESC").First(&model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// All retrieves all records
func (SoftDeleteModel[T]) All() ([]T, error) {
	query := newQuery[T]()
	return query.Get()
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

// Create inserts a new record
func (SoftDeleteModel[T]) Create(data any) (*T, error) {
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
		// Ensure fillable/guarded gates also apply to pre-constructed
		// model pointers so mass-assignment protection cannot be
		// bypassed by callers who build the struct manually.
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

// CreateMany inserts multiple records
func (SoftDeleteModel[T]) CreateMany(records []T) error {
	for _, record := range records {
		if err := Save(nil, &record); err != nil {
			return err
		}
	}
	return nil
}

// Count returns the number of records
func (SoftDeleteModel[T]) Count() (int, error) {
	query := newQuery[T]()
	return query.Count()
}

// Exists checks if any records exist
func (SoftDeleteModel[T]) Exists() bool {
	count, _ := SoftDeleteModel[T]{}.Count()
	return count > 0
}

// Paginate returns a paginated result for all records
func (SoftDeleteModel[T]) Paginate(page, perPage int) (*PaginatedResult[T], error) {
	query := newQuery[T]()
	return query.Paginate(page, perPage)
}

// Raw creates a raw SQL query builder for executing custom queries
// Usage: SoftDeleteModel{}.Raw("SELECT * FROM users WHERE id = ?", 1).First(&user)
func (SoftDeleteModel[T]) Raw(sql string, args ...any) *RawQuery[T] {
	return NewRawQuery[T](sql, args...)
}

// Pluck retrieves a single column values
func (SoftDeleteModel[T]) Pluck(column string) ([]any, error) {
	query := newQuery[T]()
	return query.Pluck(column)
}

// Update updates records matching conditions
func (SoftDeleteModel[T]) Update(conditions map[string]any, updates map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.Update(updates)
}

// DeleteWhere soft deletes records matching conditions
func (SoftDeleteModel[T]) DeleteWhere(conditions map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.Delete()
}

// ForceDeleteWhere permanently deletes records matching conditions
func (SoftDeleteModel[T]) ForceDeleteWhere(conditions map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.ForceDelete()
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

// Save inserts or updates the model
func (m *SoftDeleteModel[T]) Save() error {
	if m.IsExisting {
		return m.update()
	}
	return m.insert()
}

// Delete soft deletes the model
func (m *SoftDeleteModel[T]) Delete() error {
	if !m.IsExisting {
		return errors.New("cannot delete non-existent model")
	}

	now := time.Now()
	m.DeletedAt = &now
	return m.update()
}

// ForceDelete permanently deletes the model
func (m *SoftDeleteModel[T]) ForceDelete() error {
	if !m.IsExisting {
		return errors.New("cannot delete non-existent model")
	}

	query := newQuery[T]()
	_, err := query.Where("id = ?", m.ID).ForceDelete()
	return err
}

// Restore restores a soft deleted model
func (m *SoftDeleteModel[T]) Restore() error {
	if !m.IsExisting {
		return errors.New("cannot restore non-existent model")
	}

	m.DeletedAt = nil
	return m.update()
}

// Refresh reloads the model from database
func (m *SoftDeleteModel[T]) Refresh() error {
	if !m.IsExisting {
		return errors.New("cannot refresh non-existent model")
	}

	fresh, err := SoftDeleteModel[T]{}.Find(m.ID)
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

// WithContext returns a *Query[T] bound to ctx so static-like helpers
// chained off it propagate context to the driver. See Model[T].WithContext.
func (SoftDeleteUUIDModel[T]) WithContext(ctx context.Context) *Query[T] {
	return newQuery[T]().WithContext(ctx)
}

// Find retrieves a record by UUID primary key
func (SoftDeleteUUIDModel[T]) Find(id string) (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.Where("id = ?", id).First(&model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// FindBy retrieves a record by a specific field
func (SoftDeleteUUIDModel[T]) FindBy(field string, value any) (*T, error) {
	if err := validateIdentifier(field); err != nil {
		return nil, err
	}
	var model T
	query := newQuery[T]()
	err := query.Where(field+" = ?", value).First(&model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// First retrieves the first record
func (SoftDeleteUUIDModel[T]) First() (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.First(&model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// Last retrieves the last record (by created_at descending)
func (SoftDeleteUUIDModel[T]) Last() (*T, error) {
	var model T
	query := newQuery[T]()
	err := query.OrderBy("created_at", "DESC").First(&model)
	if err != nil {
		return nil, err
	}
	return &model, nil
}

// All retrieves all records
func (SoftDeleteUUIDModel[T]) All() ([]T, error) {
	query := newQuery[T]()
	return query.Get()
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

// Create inserts a new record
func (SoftDeleteUUIDModel[T]) Create(data any) (*T, error) {
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
		// Ensure fillable/guarded gates also apply to pre-constructed
		// model pointers so mass-assignment protection cannot be
		// bypassed by callers who build the struct manually.
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

// CreateMany inserts multiple records
func (SoftDeleteUUIDModel[T]) CreateMany(records []T) error {
	for _, record := range records {
		if err := Save(nil, &record); err != nil {
			return err
		}
	}
	return nil
}

// Count returns the number of records
func (SoftDeleteUUIDModel[T]) Count() (int, error) {
	query := newQuery[T]()
	return query.Count()
}

// Exists checks if any records exist
func (SoftDeleteUUIDModel[T]) Exists() bool {
	count, _ := SoftDeleteUUIDModel[T]{}.Count()
	return count > 0
}

// Paginate returns a paginated result for all records
func (SoftDeleteUUIDModel[T]) Paginate(page, perPage int) (*PaginatedResult[T], error) {
	query := newQuery[T]()
	return query.Paginate(page, perPage)
}

// Raw creates a raw SQL query builder for executing custom queries
// Usage: SoftDeleteUUIDModel{}.Raw("SELECT * FROM users WHERE id = ?", 1).First(&user)
func (SoftDeleteUUIDModel[T]) Raw(sql string, args ...any) *RawQuery[T] {
	return NewRawQuery[T](sql, args...)
}

// Pluck retrieves a single column values
func (SoftDeleteUUIDModel[T]) Pluck(column string) ([]any, error) {
	query := newQuery[T]()
	return query.Pluck(column)
}

// Update updates records matching conditions
func (SoftDeleteUUIDModel[T]) Update(conditions map[string]any, updates map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.Update(updates)
}

// DeleteWhere soft deletes records matching conditions
func (SoftDeleteUUIDModel[T]) DeleteWhere(conditions map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.Delete()
}

// ForceDeleteWhere permanently deletes records matching conditions
func (SoftDeleteUUIDModel[T]) ForceDeleteWhere(conditions map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		if err := validateIdentifier(field); err != nil {
			return 0, err
		}
		query = query.Where(field+" = ?", value)
	}
	return query.ForceDelete()
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

// Save inserts or updates the model
func (m *SoftDeleteUUIDModel[T]) Save() error {
	if m.IsExisting {
		return m.update()
	}
	return m.insert()
}

// Delete soft deletes the model
func (m *SoftDeleteUUIDModel[T]) Delete() error {
	if !m.IsExisting {
		return errors.New("cannot delete non-existent model")
	}

	now := time.Now()
	m.DeletedAt = &now
	return m.update()
}

// ForceDelete permanently deletes the model
func (m *SoftDeleteUUIDModel[T]) ForceDelete() error {
	if !m.IsExisting {
		return errors.New("cannot delete non-existent model")
	}

	query := newQuery[T]()
	_, err := query.Where("id = ?", m.ID).ForceDelete()
	return err
}

// Restore restores a soft deleted model
func (m *SoftDeleteUUIDModel[T]) Restore() error {
	if !m.IsExisting {
		return errors.New("cannot restore non-existent model")
	}

	m.DeletedAt = nil
	return m.update()
}

// Refresh reloads the model from database
func (m *SoftDeleteUUIDModel[T]) Refresh() error {
	if !m.IsExisting {
		return errors.New("cannot refresh non-existent model")
	}

	fresh, err := SoftDeleteUUIDModel[T]{}.Find(m.ID)
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
func applyFillableToStruct(s any) error {
	fillable, hasFillable := anyFillableList(s)
	guarded, hasGuarded := anyGuardedList(s)
	if !hasFillable && !hasGuarded {
		return nil
	}

	v := reflect.ValueOf(s)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return nil
	}
	t := v.Type()

	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		// Skip embedded ORM base models; their fields are framework-
		// managed and must never be blanked by mass-assignment policy.
		typStr := field.Type.String()
		if field.Anonymous && (strings.HasPrefix(typStr, "orm.Model[") ||
			strings.HasPrefix(typStr, "orm.UUIDModel[") ||
			strings.HasPrefix(typStr, "orm.SoftDeleteModel[") ||
			strings.HasPrefix(typStr, "orm.SoftDeleteUUIDModel[")) {
			continue
		}
		// Skip ORM-internal bookkeeping fields that are tagged orm:"-".
		if tag := field.Tag.Get("orm"); tag == "-" ||
			strings.Contains(tag, "relation:") ||
			extractManyToManyValue(tag) != "" ||
			extractPolymorphicValue(tag) != "" {
			continue
		}

		name := toSnakeCase(field.Name)

		var reject bool
		if hasFillable && !fillable[name] {
			reject = true
		}
		if hasGuarded && guarded[name] {
			reject = true
		}

		if reject {
			fv := v.Field(i)
			if fv.CanSet() {
				fv.Set(reflect.Zero(fv.Type()))
			}
		}
	}
	return nil
}

func anyFillableList(s any) (map[string]bool, bool) {
	if f, ok := s.(Fillable); ok {
		set := make(map[string]bool, len(f.Fillable()))
		for _, name := range f.Fillable() {
			set[name] = true
		}
		return set, true
	}
	return nil, false
}

func anyGuardedList(s any) (map[string]bool, bool) {
	if g, ok := s.(Guarded); ok {
		set := make(map[string]bool, len(g.Guarded()))
		for _, name := range g.Guarded() {
			set[name] = true
		}
		return set, true
	}
	return nil, false
}

func mapToStruct(m map[string]any, s any) error {
	v := reflect.ValueOf(s)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	t := v.Type()

	// Check for Fillable/Guarded interfaces
	var fillableSet map[string]bool
	var guardedSet map[string]bool

	if f, ok := s.(Fillable); ok {
		fillableSet = make(map[string]bool)
		for _, field := range f.Fillable() {
			fillableSet[field] = true
		}
	}
	if g, ok := s.(Guarded); ok {
		guardedSet = make(map[string]bool)
		for _, field := range g.Guarded() {
			guardedSet[field] = true
		}
	}

	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		fieldName := toSnakeCase(field.Name)

		// Check if field value exists in map
		if val, ok := m[fieldName]; ok {
			// Mass assignment protection
			if fillableSet != nil && !fillableSet[fieldName] {
				continue
			}
			if guardedSet != nil && guardedSet[fieldName] {
				continue
			}

			fieldValue := v.Field(i)
			if fieldValue.CanSet() {
				valReflect := reflect.ValueOf(val)

				// Handle pointer fields: if field is *T and val is T, create pointer
				if fieldValue.Kind() == reflect.Ptr && valReflect.Kind() != reflect.Ptr {
					if valReflect.Type().ConvertibleTo(fieldValue.Type().Elem()) {
						ptr := reflect.New(fieldValue.Type().Elem())
						ptr.Elem().Set(valReflect.Convert(fieldValue.Type().Elem()))
						fieldValue.Set(ptr)
					}
				} else if valReflect.Type().ConvertibleTo(fieldValue.Type()) {
					fieldValue.Set(valReflect.Convert(fieldValue.Type()))
				}
			}
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

	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("orm")

		// Skip fields marked with "-" or relations
		if tag == "-" || strings.Contains(tag, "relation:") {
			continue
		}

		// Many-to-many fields are virtual; never persisted from the parent.
		if extractManyToManyValue(tag) != "" {
			continue
		}

		// Polymorphic morph fields write the (type_col, id_col) pair
		// derived from the embedded Morph value rather than serializing
		// the struct itself.
		if pv := extractPolymorphicValue(tag); pv != "" {
			if typeCol, idCol, perr := parsePolymorphicTag(pv); perr == nil {
				fv := v.Field(i)
				if fv.Kind() == reflect.Struct {
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
			}
			continue
		}

		isJSON := isJSONColumn(tag)

		// Skip slice/array fields (usually relations) unless the field is
		// JSON-tagged. JSON columns may legitimately be backed by []byte,
		// []any, or map types and need to flow through to the driver.
		kind := v.Field(i).Kind()
		if (kind == reflect.Slice || kind == reflect.Array) && !isJSON {
			continue
		}

		// Delegate embedded ORM base fields to a single helper so the
		// main loop stays a straight pass over the declared columns.
		if serializeEmbedded(field, v.Field(i), result) {
			continue
		}

		// Get column name from tag or use field name
		columnName := field.Name
		if tag != "" {
			parts := strings.Split(tag, ";")
			for _, part := range parts {
				if strings.HasPrefix(part, "column:") {
					columnName = strings.TrimPrefix(part, "column:")
					break
				}
			}
		}

		// Convert field name to snake_case if no column specified
		if columnName == field.Name {
			columnName = toSnakeCase(field.Name)
		}

		// JSON/JSONB columns: omit zero values so DB defaults can apply.
		// Empty string is never valid JSON on Postgres/MySQL; SQLite would
		// store "" as literal text and break downstream json.Unmarshal.
		if isJSON && isJSONZeroValue(v.Field(i)) {
			continue
		}

		value := v.Field(i).Interface()

		// Skip zero values for certain types
		if field.Name == "ID" {
			// For uint ID (Model[T]), skip if 0
			if v.Field(i).Kind() == reflect.Uint && v.Field(i).Uint() == 0 {
				continue
			}
			// For string ID (UUIDModel[T]), skip if empty
			if v.Field(i).Kind() == reflect.String && v.Field(i).String() == "" {
				continue
			}
		}

		result[columnName] = value
	}

	return result
}

// serializeEmbedded writes the columns contributed by an embedded ORM base
// type into result and reports whether the field was handled. Returning
// false signals the caller to fall through to the regular scalar-field
// branch.
//
// The serialization rules mirror the original inline switch:
//   - orm.Model[T]: writes created_at and updated_at.
//   - orm.UUIDModel[T]: writes id (when non-empty), created_at, updated_at.
//   - orm.SoftDeleteModel[T]: writes created_at, updated_at, deleted_at.
//   - orm.SoftDeleteUUIDModel[T]: writes id, created_at, updated_at,
//     deleted_at.
func serializeEmbedded(field reflect.StructField, value reflect.Value, result map[string]any) bool {
	typeName := field.Type.String()

	switch {
	case strings.HasPrefix(typeName, "orm.Model["):
		result["created_at"] = value.FieldByName("CreatedAt").Interface()
		result["updated_at"] = value.FieldByName("UpdatedAt").Interface()
		return true

	case strings.HasPrefix(typeName, "orm.UUIDModel["):
		if id := value.FieldByName("ID").String(); id != "" {
			result["id"] = id
		}
		result["created_at"] = value.FieldByName("CreatedAt").Interface()
		result["updated_at"] = value.FieldByName("UpdatedAt").Interface()
		return true

	case strings.HasPrefix(typeName, "orm.SoftDeleteModel["):
		result["created_at"] = value.FieldByName("CreatedAt").Interface()
		result["updated_at"] = value.FieldByName("UpdatedAt").Interface()
		if deletedAt := value.FieldByName("DeletedAt"); !deletedAt.IsZero() && !deletedAt.IsNil() {
			result["deleted_at"] = deletedAt.Interface()
		}
		return true

	case strings.HasPrefix(typeName, "orm.SoftDeleteUUIDModel["):
		if id := value.FieldByName("ID").String(); id != "" {
			result["id"] = id
		}
		result["created_at"] = value.FieldByName("CreatedAt").Interface()
		result["updated_at"] = value.FieldByName("UpdatedAt").Interface()
		if deletedAt := value.FieldByName("DeletedAt"); !deletedAt.IsZero() && !deletedAt.IsNil() {
			result["deleted_at"] = deletedAt.Interface()
		}
		return true

	case strings.HasPrefix(typeName, "orm.ImmutableModel["):
		// Append-only: no updated_at column.
		result["created_at"] = value.FieldByName("CreatedAt").Interface()
		return true

	case strings.HasPrefix(typeName, "orm.ImmutableUUIDModel["):
		if id := value.FieldByName("ID").String(); id != "" {
			result["id"] = id
		}
		result["created_at"] = value.FieldByName("CreatedAt").Interface()
		return true
	}

	return false
}

// Global convenience functions

func Save[T any](m *Manager, model *T) error {
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

	if isImmutable {
		if isUUIDModel {
			return saveImmutableUUIDModel(drv, model, modelField, idField, existsField, tableName, isInsert)
		}
		return saveImmutableModel(drv, model, modelField, idField, existsField, tableName, isInsert)
	}
	if isUUIDModel {
		return saveUUIDModel(drv, model, modelField, idField, existsField, tableName, isInsert)
	}
	return saveModel(drv, model, modelField, idField, existsField, tableName, isInsert)
}

// saveModel handles saving for auto-increment ID models
func saveModel[T any](drv drivers.Driver, model *T, modelField, idField, existsField reflect.Value, tableName string, isInsert bool) error {
	if isInsert {
		// Set timestamps
		modelField.FieldByName("CreatedAt").Set(reflect.ValueOf(time.Now()))
		modelField.FieldByName("UpdatedAt").Set(reflect.ValueOf(time.Now()))

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

		lastID, err := query.InsertGetId(data)
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

		// Create query and update
		query := &Query[T]{
			table:  tableName,
			driver: drv,
		}

		_, err := query.Where("id = ?", idField.Uint()).Update(data)
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
func saveUUIDModel[T any](drv drivers.Driver, model *T, modelField, idField, existsField reflect.Value, tableName string, isInsert bool) error {
	if isInsert {
		// Generate UUID if not already set
		if idField.String() == "" {
			idField.SetString(uuid.New().String())
		}

		// Set timestamps
		modelField.FieldByName("CreatedAt").Set(reflect.ValueOf(time.Now()))
		modelField.FieldByName("UpdatedAt").Set(reflect.ValueOf(time.Now()))

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

		_, err := query.InsertGetId(data)
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

		_, err := query.Where("id = ?", idField.String()).Update(data)
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

func CreateMany[T any](records []T) error {
	for i := range records {
		if err := Save(nil, &records[i]); err != nil {
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
