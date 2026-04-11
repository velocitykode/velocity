package orm

import (
	"database/sql"
	"errors"
	"fmt"
)

// ErrNotFound is returned by *OrFail methods when no record matches the query.
// It wraps sql.ErrNoRows so callers can check with either
// errors.Is(err, ErrNotFound) or errors.Is(err, sql.ErrNoRows).
var ErrNotFound = fmt.Errorf("orm: record not found: %w", sql.ErrNoRows)

// wrapNotFound converts sql.ErrNoRows into ErrNotFound. Other errors pass through.
func wrapNotFound(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

// --- Model[T] (uint ID) ---

// FindOrFail retrieves a record by primary key or returns ErrNotFound.
func (Model[T]) FindOrFail(id any) (T, error) {
	result, err := Model[T]{}.Find(id)
	if err != nil {
		var zero T
		return zero, wrapNotFound(err)
	}
	return *result, nil
}

// FirstOrFail retrieves the first record or returns ErrNotFound.
func (Model[T]) FirstOrFail() (T, error) {
	result, err := Model[T]{}.First()
	if err != nil {
		var zero T
		return zero, wrapNotFound(err)
	}
	return *result, nil
}

// --- UUIDModel[T] (string ID) ---

// FindOrFail retrieves a record by UUID primary key or returns ErrNotFound.
func (UUIDModel[T]) FindOrFail(id string) (T, error) {
	result, err := UUIDModel[T]{}.Find(id)
	if err != nil {
		var zero T
		return zero, wrapNotFound(err)
	}
	return *result, nil
}

// FirstOrFail retrieves the first record or returns ErrNotFound.
func (UUIDModel[T]) FirstOrFail() (T, error) {
	result, err := UUIDModel[T]{}.First()
	if err != nil {
		var zero T
		return zero, wrapNotFound(err)
	}
	return *result, nil
}

// --- SoftDeleteModel[T] (uint ID) ---

// FindOrFail retrieves a record by primary key or returns ErrNotFound.
func (SoftDeleteModel[T]) FindOrFail(id any) (T, error) {
	result, err := SoftDeleteModel[T]{}.Find(id)
	if err != nil {
		var zero T
		return zero, wrapNotFound(err)
	}
	return *result, nil
}

// FirstOrFail retrieves the first record or returns ErrNotFound.
func (SoftDeleteModel[T]) FirstOrFail() (T, error) {
	result, err := SoftDeleteModel[T]{}.First()
	if err != nil {
		var zero T
		return zero, wrapNotFound(err)
	}
	return *result, nil
}

// --- SoftDeleteUUIDModel[T] (string ID) ---

// FindOrFail retrieves a record by UUID primary key or returns ErrNotFound.
func (SoftDeleteUUIDModel[T]) FindOrFail(id string) (T, error) {
	result, err := SoftDeleteUUIDModel[T]{}.Find(id)
	if err != nil {
		var zero T
		return zero, wrapNotFound(err)
	}
	return *result, nil
}

// FirstOrFail retrieves the first record or returns ErrNotFound.
func (SoftDeleteUUIDModel[T]) FirstOrFail() (T, error) {
	result, err := SoftDeleteUUIDModel[T]{}.First()
	if err != nil {
		var zero T
		return zero, wrapNotFound(err)
	}
	return *result, nil
}

// --- Query[T] ---

// FirstOrFail retrieves the first matching record or returns ErrNotFound.
func (q *Query[T]) FirstOrFail(dest *T) error {
	err := q.First(dest)
	return wrapNotFound(err)
}
