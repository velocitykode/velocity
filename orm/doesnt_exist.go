package orm

import "context"

// --- Query[T] ---

// DoesntExist returns true if no records match the query conditions.
// Takes ctx as the first argument. A failed query returns (false, err)
// rather than silently reporting presence.
func (q *Query[T]) DoesntExist(ctx context.Context) (bool, error) {
	exists, err := q.Exists(ctx)
	if err != nil {
		return false, err
	}
	return !exists, nil
}

// --- Model[T] ---

// DoesntExist returns true if no records exist for this model type.
// Takes ctx as the first argument. A failed query returns (false, err).
func (Model[T]) DoesntExist(ctx context.Context) (bool, error) {
	exists, err := Model[T]{}.Exists(ctx)
	if err != nil {
		return false, err
	}
	return !exists, nil
}

// --- UUIDModel[T] ---

// DoesntExist returns true if no records exist for this model type.
// Takes ctx as the first argument. A failed query returns (false, err).
func (UUIDModel[T]) DoesntExist(ctx context.Context) (bool, error) {
	exists, err := UUIDModel[T]{}.Exists(ctx)
	if err != nil {
		return false, err
	}
	return !exists, nil
}

// --- SoftDeleteModel[T] ---

// DoesntExist returns true if no records exist for this model type.
// Takes ctx as the first argument. A failed query returns (false, err).
func (SoftDeleteModel[T]) DoesntExist(ctx context.Context) (bool, error) {
	exists, err := SoftDeleteModel[T]{}.Exists(ctx)
	if err != nil {
		return false, err
	}
	return !exists, nil
}

// --- SoftDeleteUUIDModel[T] ---

// DoesntExist returns true if no records exist for this model type.
// Takes ctx as the first argument. A failed query returns (false, err).
func (SoftDeleteUUIDModel[T]) DoesntExist(ctx context.Context) (bool, error) {
	exists, err := SoftDeleteUUIDModel[T]{}.Exists(ctx)
	if err != nil {
		return false, err
	}
	return !exists, nil
}
