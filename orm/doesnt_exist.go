package orm

import "context"

// --- Query[T] ---

// DoesntExist returns true if no records match the query conditions.
// Takes ctx as the first argument.
func (q *Query[T]) DoesntExist(ctx context.Context) bool {
	return !q.Exists(ctx)
}

// --- Model[T] ---

// DoesntExist returns true if no records exist for this model type.
// Takes ctx as the first argument.
func (Model[T]) DoesntExist(ctx context.Context) bool {
	return !Model[T]{}.Exists(ctx)
}

// --- UUIDModel[T] ---

// DoesntExist returns true if no records exist for this model type.
// Takes ctx as the first argument.
func (UUIDModel[T]) DoesntExist(ctx context.Context) bool {
	return !UUIDModel[T]{}.Exists(ctx)
}

// --- SoftDeleteModel[T] ---

// DoesntExist returns true if no records exist for this model type.
// Takes ctx as the first argument.
func (SoftDeleteModel[T]) DoesntExist(ctx context.Context) bool {
	return !SoftDeleteModel[T]{}.Exists(ctx)
}

// --- SoftDeleteUUIDModel[T] ---

// DoesntExist returns true if no records exist for this model type.
// Takes ctx as the first argument.
func (SoftDeleteUUIDModel[T]) DoesntExist(ctx context.Context) bool {
	return !SoftDeleteUUIDModel[T]{}.Exists(ctx)
}
