package orm

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/velocitykode/velocity/orm/drivers"
)

// --- Model[T] (uint ID) ---

// FirstOrCreate finds the first record matching conditions, or creates one
// by merging conditions and values. Takes ctx as the first argument so
// transaction enrollment is mandatory and explicit.
func (Model[T]) FirstOrCreate(ctx context.Context, conditions map[string]any, values map[string]any) (*T, error) {
	return firstOrCreate[T](ctx, conditions, values)
}

// UpdateOrCreate finds the first record matching conditions and updates it
// with values, or creates a new record by merging conditions and values.
// Takes ctx as the first argument.
func (Model[T]) UpdateOrCreate(ctx context.Context, conditions map[string]any, values map[string]any) (*T, error) {
	return updateOrCreate[T](ctx, conditions, values)
}

// --- UUIDModel[T] (string ID) ---

// FirstOrCreate finds the first record matching conditions, or creates one
// by merging conditions and values. Takes ctx as the first argument.
func (UUIDModel[T]) FirstOrCreate(ctx context.Context, conditions map[string]any, values map[string]any) (*T, error) {
	return firstOrCreate[T](ctx, conditions, values)
}

// UpdateOrCreate finds the first record matching conditions and updates it
// with values, or creates a new record by merging conditions and values.
// Takes ctx as the first argument.
func (UUIDModel[T]) UpdateOrCreate(ctx context.Context, conditions map[string]any, values map[string]any) (*T, error) {
	return updateOrCreate[T](ctx, conditions, values)
}

// --- SoftDeleteModel[T] (uint ID) ---

// FirstOrCreate finds the first record matching conditions, or creates one
// by merging conditions and values. Takes ctx as the first argument.
func (SoftDeleteModel[T]) FirstOrCreate(ctx context.Context, conditions map[string]any, values map[string]any) (*T, error) {
	return firstOrCreate[T](ctx, conditions, values)
}

// UpdateOrCreate finds the first record matching conditions and updates it
// with values, or creates a new record by merging conditions and values.
// Takes ctx as the first argument.
func (SoftDeleteModel[T]) UpdateOrCreate(ctx context.Context, conditions map[string]any, values map[string]any) (*T, error) {
	return updateOrCreate[T](ctx, conditions, values)
}

// --- SoftDeleteUUIDModel[T] (string ID) ---

// FirstOrCreate finds the first record matching conditions, or creates one
// by merging conditions and values. Takes ctx as the first argument.
func (SoftDeleteUUIDModel[T]) FirstOrCreate(ctx context.Context, conditions map[string]any, values map[string]any) (*T, error) {
	return firstOrCreate[T](ctx, conditions, values)
}

// UpdateOrCreate finds the first record matching conditions and updates it
// with values, or creates a new record by merging conditions and values.
// Takes ctx as the first argument.
func (SoftDeleteUUIDModel[T]) UpdateOrCreate(ctx context.Context, conditions map[string]any, values map[string]any) (*T, error) {
	return updateOrCreate[T](ctx, conditions, values)
}

// --- internal helpers ---

// firstOrCreate is the static-helper entry. It resolves the driver
// from the package default Manager and delegates to the driver-bound
// implementation so Query[T].FirstOrCreate (tx-aware) shares the same
// logic. ctx threads through so a tx slot in ctx enrolls the entire
// round trip in the caller's transaction.
func firstOrCreate[T any](ctx context.Context, conditions map[string]any, values map[string]any) (*T, error) {
	drv, err := defaultDriverOrErr("firstOrCreate")
	if err != nil {
		return nil, err
	}
	if tx, ok := TxFromContext(ctx); ok {
		drv = &txDriver{Driver: drv, tx: tx}
	}
	return firstOrCreateWithDriver[T](ctx, drv, conditions, values)
}

// firstOrCreateWithDriver finds a row matching conditions and returns
// it; on no-row, it merges conditions+values, persists a new row, and
// returns that. drv is used for both the lookup query and the Save so
// callers (notably the tx-aware Query[T].FirstOrCreate) keep the
// entire round trip on a single connection.
func firstOrCreateWithDriver[T any](ctx context.Context, drv drivers.Driver, conditions map[string]any, values map[string]any) (*T, error) {
	for key := range conditions {
		if err := validateIdentifier(key); err != nil {
			return nil, fmt.Errorf("velocity/orm: firstOrCreate: %w", err)
		}
	}
	for key := range values {
		if err := validateIdentifier(key); err != nil {
			return nil, fmt.Errorf("velocity/orm: firstOrCreate: %w", err)
		}
	}

	q := newQuery[T]()
	q.driver = drv
	for field, value := range conditions {
		q = q.Where(field+" = ?", value)
	}

	var found T
	err := q.First(ctx, &found)
	if err == nil {
		return &found, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	merged := mergeConditionsAndValues(conditions, values)
	model := new(T)
	if err := mapToStruct(merged, model); err != nil {
		return nil, err
	}
	if err := saveWithDriver(ctx, drv, model); err != nil {
		return nil, err
	}
	return model, nil
}

// updateOrCreate is the static-helper entry. See firstOrCreate.
func updateOrCreate[T any](ctx context.Context, conditions map[string]any, values map[string]any) (*T, error) {
	drv, err := defaultDriverOrErr("updateOrCreate")
	if err != nil {
		return nil, err
	}
	if tx, ok := TxFromContext(ctx); ok {
		drv = &txDriver{Driver: drv, tx: tx}
	}
	return updateOrCreateWithDriver[T](ctx, drv, conditions, values)
}

// updateOrCreateWithDriver runs the lookup, update-on-hit / insert-on-miss
// flow against drv. ctx threads through so a tx slot in ctx enrolls the
// entire round trip in the caller's transaction.
func updateOrCreateWithDriver[T any](ctx context.Context, drv drivers.Driver, conditions map[string]any, values map[string]any) (*T, error) {
	for key := range conditions {
		if err := validateIdentifier(key); err != nil {
			return nil, fmt.Errorf("velocity/orm: updateOrCreate: %w", err)
		}
	}
	for key := range values {
		if err := validateIdentifier(key); err != nil {
			return nil, fmt.Errorf("velocity/orm: updateOrCreate: %w", err)
		}
	}

	q := newQuery[T]()
	q.driver = drv
	for field, value := range conditions {
		q = q.Where(field+" = ?", value)
	}

	var found T
	err := q.First(ctx, &found)
	if err == nil {
		// Belt-and-suspenders: Query.First already marks IsExisting on
		// scan, but a redundant call is idempotent and survives any
		// future refactor of the read path that forgets to mark.
		markExisting(&found)
		if err := mapToStruct(values, &found); err != nil {
			return nil, err
		}
		if err := saveWithDriver(ctx, drv, &found); err != nil {
			return nil, err
		}
		return &found, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}

	merged := mergeConditionsAndValues(conditions, values)
	model := new(T)
	if err := mapToStruct(merged, model); err != nil {
		return nil, err
	}
	if err := saveWithDriver(ctx, drv, model); err != nil {
		return nil, err
	}
	return model, nil
}

// defaultDriverOrErr resolves the package default Manager's driver,
// returning a uniform error for the static-helper call sites so the
// caller-facing message identifies which helper failed.
func defaultDriverOrErr(op string) (drivers.Driver, error) {
	m := Default()
	if m == nil {
		return nil, fmt.Errorf("velocity/orm: %s: no default manager set", op)
	}
	drv := m.DefaultDriver()
	if drv == nil {
		return nil, fmt.Errorf("velocity/orm: %s: no database connection", op)
	}
	return drv, nil
}

// existenceSetter is implemented by every base model type. Immutable
// variants opt in deliberately: leaving them unmarked would let a
// read-then-Save round trip silently re-INSERT the row (auto-inc PK)
// or fail with a raw DB unique-key error (UUID PK), instead of the
// loud, intended ErrImmutableModelUpdate.
type existenceSetter interface {
	setExisting()
}

func (m *Model[T]) setExisting()               { m.IsExisting = true }
func (m *UUIDModel[T]) setExisting()           { m.IsExisting = true }
func (m *SoftDeleteModel[T]) setExisting()     { m.IsExisting = true }
func (m *SoftDeleteUUIDModel[T]) setExisting() { m.IsExisting = true }
func (m *ImmutableModel[T]) setExisting()      { m.IsExisting = true }
func (m *ImmutableUUIDModel[T]) setExisting()  { m.IsExisting = true }

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
