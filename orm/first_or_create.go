package orm

import (
	"database/sql"
	"errors"
	"fmt"
)

// --- Model[T] (uint ID) ---

// FirstOrCreate finds the first record matching conditions, or creates one
// by merging conditions and values.
func (Model[T]) FirstOrCreate(conditions map[string]any, values map[string]any) (*T, error) {
	return firstOrCreate[T](conditions, values)
}

// UpdateOrCreate finds the first record matching conditions and updates it
// with values, or creates a new record by merging conditions and values.
func (Model[T]) UpdateOrCreate(conditions map[string]any, values map[string]any) (*T, error) {
	return updateOrCreate[T](conditions, values)
}

// --- UUIDModel[T] (string ID) ---

// FirstOrCreate finds the first record matching conditions, or creates one
// by merging conditions and values.
func (UUIDModel[T]) FirstOrCreate(conditions map[string]any, values map[string]any) (*T, error) {
	return firstOrCreate[T](conditions, values)
}

// UpdateOrCreate finds the first record matching conditions and updates it
// with values, or creates a new record by merging conditions and values.
func (UUIDModel[T]) UpdateOrCreate(conditions map[string]any, values map[string]any) (*T, error) {
	return updateOrCreate[T](conditions, values)
}

// --- SoftDeleteModel[T] (uint ID) ---

// FirstOrCreate finds the first record matching conditions, or creates one
// by merging conditions and values.
func (SoftDeleteModel[T]) FirstOrCreate(conditions map[string]any, values map[string]any) (*T, error) {
	return firstOrCreate[T](conditions, values)
}

// UpdateOrCreate finds the first record matching conditions and updates it
// with values, or creates a new record by merging conditions and values.
func (SoftDeleteModel[T]) UpdateOrCreate(conditions map[string]any, values map[string]any) (*T, error) {
	return updateOrCreate[T](conditions, values)
}

// --- SoftDeleteUUIDModel[T] (string ID) ---

// FirstOrCreate finds the first record matching conditions, or creates one
// by merging conditions and values.
func (SoftDeleteUUIDModel[T]) FirstOrCreate(conditions map[string]any, values map[string]any) (*T, error) {
	return firstOrCreate[T](conditions, values)
}

// UpdateOrCreate finds the first record matching conditions and updates it
// with values, or creates a new record by merging conditions and values.
func (SoftDeleteUUIDModel[T]) UpdateOrCreate(conditions map[string]any, values map[string]any) (*T, error) {
	return updateOrCreate[T](conditions, values)
}

// --- internal helpers ---

// firstOrCreate queries by conditions. If found, returns it. Otherwise merges
// conditions+values, creates a new record via Save, and returns it.
func firstOrCreate[T any](conditions map[string]any, values map[string]any) (*T, error) {
	// Validate all condition keys
	for key := range conditions {
		if err := validateIdentifier(key); err != nil {
			return nil, fmt.Errorf("FirstOrCreate: %w", err)
		}
	}
	for key := range values {
		if err := validateIdentifier(key); err != nil {
			return nil, fmt.Errorf("FirstOrCreate: %w", err)
		}
	}

	// Try to find an existing record
	q := newQuery[T]()
	for field, value := range conditions {
		q = q.Where(field+" = ?", value)
	}

	var found T
	err := q.First(&found)
	if err == nil {
		return &found, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// Not found — merge conditions + values and create
	merged := mergeConditionsAndValues(conditions, values)
	model := new(T)
	if err := mapToStruct(merged, model); err != nil {
		return nil, err
	}
	if err := Save(nil, model); err != nil {
		return nil, err
	}
	return model, nil
}

// updateOrCreate queries by conditions. If found, updates with values and saves.
// If not found, merges conditions+values and creates.
func updateOrCreate[T any](conditions map[string]any, values map[string]any) (*T, error) {
	// Validate all condition keys
	for key := range conditions {
		if err := validateIdentifier(key); err != nil {
			return nil, fmt.Errorf("UpdateOrCreate: %w", err)
		}
	}
	for key := range values {
		if err := validateIdentifier(key); err != nil {
			return nil, fmt.Errorf("UpdateOrCreate: %w", err)
		}
	}

	// Try to find an existing record
	q := newQuery[T]()
	for field, value := range conditions {
		q = q.Where(field+" = ?", value)
	}

	var found T
	err := q.First(&found)
	if err == nil {
		// Found — mark as existing so Save performs UPDATE, not INSERT
		markExisting(&found)

		// Apply values and save
		if err := mapToStruct(values, &found); err != nil {
			return nil, err
		}
		if err := Save(nil, &found); err != nil {
			return nil, err
		}
		return &found, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	// Not found — merge conditions + values and create
	merged := mergeConditionsAndValues(conditions, values)
	model := new(T)
	if err := mapToStruct(merged, model); err != nil {
		return nil, err
	}
	if err := Save(nil, model); err != nil {
		return nil, err
	}
	return model, nil
}

// existenceSetter is implemented by all 4 model base types.
type existenceSetter interface {
	setExisting()
}

func (m *Model[T]) setExisting()               { m.IsExisting = true }
func (m *UUIDModel[T]) setExisting()           { m.IsExisting = true }
func (m *SoftDeleteModel[T]) setExisting()     { m.IsExisting = true }
func (m *SoftDeleteUUIDModel[T]) setExisting() { m.IsExisting = true }

// markExisting sets the IsExisting flag via the existenceSetter interface.
// This avoids fragile reflection-based type string matching.
func markExisting[T any](model *T) {
	if s, ok := any(model).(existenceSetter); ok {
		s.setExisting()
	}
}

// mergeConditionsAndValues creates a new map with conditions as base and values overlaid.
func mergeConditionsAndValues(conditions, values map[string]any) map[string]any {
	merged := make(map[string]any, len(conditions)+len(values))
	for k, v := range conditions {
		merged[k] = v
	}
	for k, v := range values {
		merged[k] = v
	}
	return merged
}
