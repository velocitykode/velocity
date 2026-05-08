package orm

import (
	"context"
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
// Takes ctx as the first argument.
func (Model[T]) FindOrFail(ctx context.Context, id any) (T, error) {
	result, err := Model[T]{}.Find(ctx, id)
	if err != nil {
		var zero T
		return zero, wrapNotFound(err)
	}
	return *result, nil
}

// FirstOrFail retrieves the first record or returns ErrNotFound. Takes
// ctx as the first argument.
func (Model[T]) FirstOrFail(ctx context.Context) (T, error) {
	result, err := Model[T]{}.First(ctx)
	if err != nil {
		var zero T
		return zero, wrapNotFound(err)
	}
	return *result, nil
}

// --- UUIDModel[T] (string ID) ---

// FindOrFail retrieves a record by UUID primary key or returns ErrNotFound.
// Takes ctx as the first argument.
func (UUIDModel[T]) FindOrFail(ctx context.Context, id string) (T, error) {
	result, err := UUIDModel[T]{}.Find(ctx, id)
	if err != nil {
		var zero T
		return zero, wrapNotFound(err)
	}
	return *result, nil
}

// FirstOrFail retrieves the first record or returns ErrNotFound. Takes
// ctx as the first argument.
func (UUIDModel[T]) FirstOrFail(ctx context.Context) (T, error) {
	result, err := UUIDModel[T]{}.First(ctx)
	if err != nil {
		var zero T
		return zero, wrapNotFound(err)
	}
	return *result, nil
}

// --- SoftDeleteModel[T] (uint ID) ---

// FindOrFail retrieves a record by primary key or returns ErrNotFound.
// Takes ctx as the first argument.
func (SoftDeleteModel[T]) FindOrFail(ctx context.Context, id any) (T, error) {
	result, err := SoftDeleteModel[T]{}.Find(ctx, id)
	if err != nil {
		var zero T
		return zero, wrapNotFound(err)
	}
	return *result, nil
}

// FirstOrFail retrieves the first record or returns ErrNotFound. Takes
// ctx as the first argument.
func (SoftDeleteModel[T]) FirstOrFail(ctx context.Context) (T, error) {
	result, err := SoftDeleteModel[T]{}.First(ctx)
	if err != nil {
		var zero T
		return zero, wrapNotFound(err)
	}
	return *result, nil
}

// --- SoftDeleteUUIDModel[T] (string ID) ---

// FindOrFail retrieves a record by UUID primary key or returns ErrNotFound.
// Takes ctx as the first argument.
func (SoftDeleteUUIDModel[T]) FindOrFail(ctx context.Context, id string) (T, error) {
	result, err := SoftDeleteUUIDModel[T]{}.Find(ctx, id)
	if err != nil {
		var zero T
		return zero, wrapNotFound(err)
	}
	return *result, nil
}

// FirstOrFail retrieves the first record or returns ErrNotFound. Takes
// ctx as the first argument.
func (SoftDeleteUUIDModel[T]) FirstOrFail(ctx context.Context) (T, error) {
	result, err := SoftDeleteUUIDModel[T]{}.First(ctx)
	if err != nil {
		var zero T
		return zero, wrapNotFound(err)
	}
	return *result, nil
}

// --- Query[T] ---

// FirstOrFail retrieves the first matching record or returns ErrNotFound.
// Takes ctx as the first argument.
func (q *Query[T]) FirstOrFail(ctx context.Context, dest *T) error {
	err := q.First(ctx, dest)
	return wrapNotFound(err)
}
