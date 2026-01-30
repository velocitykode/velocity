package orm

import (
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Model is the generic base model that provides Laravel-style static methods
type Model[T any] struct {
	ID        uint       `orm:"primaryKey;autoIncrement" json:"id"`
	CreatedAt time.Time  `orm:"autoCreateTime" json:"created_at"`
	UpdatedAt time.Time  `orm:"autoUpdateTime" json:"updated_at"`
	DeletedAt *time.Time `orm:"index" json:"deleted_at,omitempty"`

	// Internal fields (not persisted)
	IsExisting bool            `orm:"-" json:"-"`
	Original   map[string]any  `orm:"-" json:"-"`
	Changed    map[string]bool `orm:"-" json:"-"`
}

// UUIDModel is a generic base model with UUID primary key for distributed systems
// and external-facing APIs where sequential IDs pose security risks.
type UUIDModel[T any] struct {
	ID        string     `orm:"primaryKey;type:uuid" json:"id"`
	CreatedAt time.Time  `orm:"autoCreateTime" json:"created_at"`
	UpdatedAt time.Time  `orm:"autoUpdateTime" json:"updated_at"`
	DeletedAt *time.Time `orm:"index" json:"deleted_at,omitempty"`

	// Internal fields (not persisted)
	IsExisting bool            `orm:"-" json:"-"`
	Original   map[string]any  `orm:"-" json:"-"`
	Changed    map[string]bool `orm:"-" json:"-"`
}

// Static-like methods that return the actual type

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

// Last retrieves the last record
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

// Create inserts a new record or multiple records
func (Model[T]) Create(data any) (*T, error) {
	switch v := data.(type) {
	case map[string]any:
		model := new(T)
		if err := mapToStruct(v, model); err != nil {
			return nil, err
		}
		if err := Save(model); err != nil {
			return nil, err
		}
		return model, nil
	case *T:
		if err := Save(v); err != nil {
			return nil, err
		}
		return v, nil
	default:
		return nil, errors.New("unsupported data type for create")
	}
}

// CreateMany inserts multiple records
func (Model[T]) CreateMany(records []T) error {
	for _, record := range records {
		if err := Save(&record); err != nil {
			return err
		}
	}
	return nil
}

// Count returns the number of records
func (Model[T]) Count() (int64, error) {
	query := newQuery[T]()
	return query.Count()
}

// Exists checks if any records exist
func (Model[T]) Exists() bool {
	count, _ := Model[T]{}.Count()
	return count > 0
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
		query = query.Where(field+" = ?", value)
	}
	return query.Update(updates)
}

// DeleteWhere soft deletes records matching conditions
func (Model[T]) DeleteWhere(conditions map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		query = query.Where(field+" = ?", value)
	}
	return query.Delete()
}

// ForceDeleteWhere permanently deletes records matching conditions
func (Model[T]) ForceDeleteWhere(conditions map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		query = query.Where(field+" = ?", value)
	}
	return query.ForceDelete()
}

// OnlyTrashed retrieves only soft deleted records
func (Model[T]) OnlyTrashed() *Query[T] {
	query := newQuery[T]()
	return query.OnlyTrashed()
}

// WithTrashed includes soft deleted records
func (Model[T]) WithTrashed() *Query[T] {
	query := newQuery[T]()
	return query.WithTrashed()
}

// Instance methods

// Save inserts or updates the model
func (m *Model[T]) Save() error {
	if m.IsExisting {
		return m.update()
	}
	return m.insert()
}

// Delete soft deletes the model
func (m *Model[T]) Delete() error {
	if !m.IsExisting {
		return errors.New("cannot delete non-existent model")
	}

	now := time.Now()
	m.DeletedAt = &now
	return m.update()
}

// ForceDelete permanently deletes the model
func (m *Model[T]) ForceDelete() error {
	if !m.IsExisting {
		return errors.New("cannot delete non-existent model")
	}

	query := newQuery[T]()
	_, err := query.Where("id = ?", m.ID).ForceDelete()
	return err
}

// Restore restores a soft deleted model
func (m *Model[T]) Restore() error {
	if !m.IsExisting {
		return errors.New("cannot restore non-existent model")
	}

	m.DeletedAt = nil
	return m.update()
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

// Create inserts a new record or multiple records
func (UUIDModel[T]) Create(data any) (*T, error) {
	switch v := data.(type) {
	case map[string]any:
		model := new(T)
		if err := mapToStruct(v, model); err != nil {
			return nil, err
		}
		if err := Save(model); err != nil {
			return nil, err
		}
		return model, nil
	case *T:
		if err := Save(v); err != nil {
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
		if err := Save(&record); err != nil {
			return err
		}
	}
	return nil
}

// Count returns the number of records
func (UUIDModel[T]) Count() (int64, error) {
	query := newQuery[T]()
	return query.Count()
}

// Exists checks if any records exist
func (UUIDModel[T]) Exists() bool {
	count, _ := UUIDModel[T]{}.Count()
	return count > 0
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
		query = query.Where(field+" = ?", value)
	}
	return query.Update(updates)
}

// DeleteWhere soft deletes records matching conditions
func (UUIDModel[T]) DeleteWhere(conditions map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		query = query.Where(field+" = ?", value)
	}
	return query.Delete()
}

// ForceDeleteWhere permanently deletes records matching conditions
func (UUIDModel[T]) ForceDeleteWhere(conditions map[string]any) (int64, error) {
	query := newQuery[T]()
	for field, value := range conditions {
		query = query.Where(field+" = ?", value)
	}
	return query.ForceDelete()
}

// OnlyTrashed retrieves only soft deleted records
func (UUIDModel[T]) OnlyTrashed() *Query[T] {
	query := newQuery[T]()
	return query.OnlyTrashed()
}

// WithTrashed includes soft deleted records
func (UUIDModel[T]) WithTrashed() *Query[T] {
	query := newQuery[T]()
	return query.WithTrashed()
}

// UUIDModel instance methods

// Save inserts or updates the model
func (m *UUIDModel[T]) Save() error {
	if m.IsExisting {
		return m.update()
	}
	return m.insert()
}

// Delete soft deletes the model
func (m *UUIDModel[T]) Delete() error {
	if !m.IsExisting {
		return errors.New("cannot delete non-existent model")
	}

	now := time.Now()
	m.DeletedAt = &now
	return m.update()
}

// ForceDelete permanently deletes the model
func (m *UUIDModel[T]) ForceDelete() error {
	if !m.IsExisting {
		return errors.New("cannot delete non-existent model")
	}

	query := newQuery[T]()
	_, err := query.Where("id = ?", m.ID).ForceDelete()
	return err
}

// Restore restores a soft deleted model
func (m *UUIDModel[T]) Restore() error {
	if !m.IsExisting {
		return errors.New("cannot restore non-existent model")
	}

	m.DeletedAt = nil
	return m.update()
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

// Helper functions

func mapToStruct(m map[string]any, s any) error {
	v := reflect.ValueOf(s)
	if v.Kind() == reflect.Ptr {
		v = v.Elem()
	}
	t := v.Type()

	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		fieldName := toSnakeCase(field.Name)

		// Check if field value exists in map
		if val, ok := m[fieldName]; ok {
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

		// Skip slice/array fields (usually relations)
		if v.Field(i).Kind() == reflect.Slice || v.Field(i).Kind() == reflect.Array {
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

		// Handle Model[T] embedded field specially
		if strings.HasPrefix(field.Type.String(), "orm.Model[") {
			// Extract the base Model fields
			modelValue := v.Field(i)
			result["created_at"] = modelValue.FieldByName("CreatedAt").Interface()
			result["updated_at"] = modelValue.FieldByName("UpdatedAt").Interface()
			if deletedAt := modelValue.FieldByName("DeletedAt"); !deletedAt.IsZero() && !deletedAt.IsNil() {
				result["deleted_at"] = deletedAt.Interface()
			}
			continue
		}

		// Handle UUIDModel[T] embedded field specially
		if strings.HasPrefix(field.Type.String(), "orm.UUIDModel[") {
			// Extract the base UUIDModel fields
			modelValue := v.Field(i)
			// Include ID for UUID models (it's set before insert)
			if idVal := modelValue.FieldByName("ID").String(); idVal != "" {
				result["id"] = idVal
			}
			result["created_at"] = modelValue.FieldByName("CreatedAt").Interface()
			result["updated_at"] = modelValue.FieldByName("UpdatedAt").Interface()
			if deletedAt := modelValue.FieldByName("DeletedAt"); !deletedAt.IsZero() && !deletedAt.IsNil() {
				result["deleted_at"] = deletedAt.Interface()
			}
			continue
		}

		result[columnName] = value
	}

	return result
}

// Global convenience functions

func Save[T any](model *T) error {
	v := reflect.ValueOf(model).Elem()
	t := v.Type()

	// Find the embedded Model or UUIDModel field
	var modelField reflect.Value
	var isUUIDModel bool
	var found bool

	for i := 0; i < v.NumField(); i++ {
		field := t.Field(i)
		if strings.HasPrefix(field.Type.String(), "orm.UUIDModel[") {
			modelField = v.Field(i)
			isUUIDModel = true
			found = true
			break
		}
		if strings.HasPrefix(field.Type.String(), "orm.Model[") {
			modelField = v.Field(i)
			found = true
			break
		}
	}

	if !found {
		return errors.New("model does not embed orm.Model or orm.UUIDModel")
	}

	// Get the table name
	tableName := toSnakeCase(t.Name()) + "s" // pluralize
	if tableNamer, ok := any(model).(interface{ TableName() string }); ok {
		tableName = tableNamer.TableName()
	}

	// Check if it's insert or update
	idField := modelField.FieldByName("ID")
	existsField := modelField.FieldByName("IsExisting")
	isInsert := !existsField.Bool()

	if isUUIDModel {
		return saveUUIDModel(model, modelField, idField, existsField, tableName, isInsert)
	}
	return saveModel(model, modelField, idField, existsField, tableName, isInsert)
}

// saveModel handles saving for auto-increment ID models
func saveModel[T any](model *T, modelField, idField, existsField reflect.Value, tableName string, isInsert bool) error {
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
			driver: getCurrentDriver(),
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
			driver: getCurrentDriver(),
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
func saveUUIDModel[T any](model *T, modelField, idField, existsField reflect.Value, tableName string, isInsert bool) error {
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
			driver: getCurrentDriver(),
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
			driver: getCurrentDriver(),
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
		if err := Save(&records[i]); err != nil {
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
