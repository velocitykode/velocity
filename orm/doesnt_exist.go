package orm

// --- Query[T] ---

// DoesntExist returns true if no records match the query conditions.
func (q *Query[T]) DoesntExist() bool {
	return !q.Exists()
}

// --- Model[T] ---

// DoesntExist returns true if no records exist for this model type.
func (Model[T]) DoesntExist() bool {
	return !Model[T]{}.Exists()
}

// --- UUIDModel[T] ---

// DoesntExist returns true if no records exist for this model type.
func (UUIDModel[T]) DoesntExist() bool {
	return !UUIDModel[T]{}.Exists()
}

// --- SoftDeleteModel[T] ---

// DoesntExist returns true if no records exist for this model type.
func (SoftDeleteModel[T]) DoesntExist() bool {
	return !SoftDeleteModel[T]{}.Exists()
}

// --- SoftDeleteUUIDModel[T] ---

// DoesntExist returns true if no records exist for this model type.
func (SoftDeleteUUIDModel[T]) DoesntExist() bool {
	return !SoftDeleteUUIDModel[T]{}.Exists()
}
