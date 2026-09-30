package orm

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"

	"github.com/velocitykode/velocity/internal/errchain"
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

// firstOrCreate is the static-helper entry. It resolves the package
// default Manager once, takes both the driver and the hook logger from
// it, and delegates to the driver-bound
// implementation so Query[T].FirstOrCreate (tx-aware) shares the same
// logic. ctx threads through so a tx slot in ctx enrolls the entire
// round trip in the caller's transaction.
func firstOrCreate[T any](ctx context.Context, conditions map[string]any, values map[string]any) (*T, error) {
	m, drv, err := defaultManagerDriver("firstOrCreate")
	if err != nil {
		return nil, err
	}
	if tx, ok := TxFromContext(ctx); ok {
		drv = &txDriver{Driver: drv, tx: tx}
	}
	ctx = withModelHookLogger[T](ctx, m)
	return firstOrCreateWithDriver[T](ctx, m, drv, conditions, values)
}

// firstOrCreateWithDriver finds a row matching conditions and returns
// it; on no-row, it merges conditions+values, persists a new row, and
// returns that. m is the manager drv belongs to (nil for a detached
// builder); the lookup query carries it rather than re-reading the
// package default. drv is used for both the lookup query and the Save so
// callers (notably the tx-aware Query[T].FirstOrCreate) keep the
// entire round trip on a single connection.
func firstOrCreateWithDriver[T any](ctx context.Context, m *Manager, drv drivers.Driver, conditions map[string]any, values map[string]any) (*T, error) {
	for key := range conditions {
		if err := validateIdentifier(key); err != nil {
			return nil, errchain.Errorf("velocity/orm: firstOrCreate: %w", err)
		}
	}
	for key := range values {
		if err := validateIdentifier(key); err != nil {
			return nil, errchain.Errorf("velocity/orm: firstOrCreate: %w", err)
		}
	}
	if err := denyUndeclaredMapKeys[T](conditions, values); err != nil {
		return nil, err
	}

	q := newQueryOn[T](m, drv)
	for field, value := range conditions {
		q = q.Where(field+" = ?", value)
	}

	var found T
	err := q.First(ctx, &found)
	if err == nil {
		return &found, nil
	}
	if !errchain.Is(err, sql.ErrNoRows) {
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
	m, drv, err := defaultManagerDriver("updateOrCreate")
	if err != nil {
		return nil, err
	}
	if tx, ok := TxFromContext(ctx); ok {
		drv = &txDriver{Driver: drv, tx: tx}
	}
	ctx = withModelHookLogger[T](ctx, m)
	return updateOrCreateWithDriver[T](ctx, m, drv, conditions, values)
}

// updateOrCreateWithDriver runs the lookup, update-on-hit / insert-on-miss
// flow against drv. ctx threads through so a tx slot in ctx enrolls the
// entire round trip in the caller's transaction.
func updateOrCreateWithDriver[T any](ctx context.Context, m *Manager, drv drivers.Driver, conditions map[string]any, values map[string]any) (*T, error) {
	for key := range conditions {
		if err := validateIdentifier(key); err != nil {
			return nil, errchain.Errorf("velocity/orm: updateOrCreate: %w", err)
		}
	}
	for key := range values {
		if err := validateIdentifier(key); err != nil {
			return nil, errchain.Errorf("velocity/orm: updateOrCreate: %w", err)
		}
	}
	if err := denyUndeclaredMapKeys[T](conditions, values); err != nil {
		return nil, err
	}

	q := newQueryOn[T](m, drv)
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
	if !errchain.Is(err, sql.ErrNoRows) {
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

// defaultManagerDriver resolves the package default Manager once and
// returns it with its driver, so a static helper writes through and logs
// to the same manager even when the default changes while it runs. The
// error is uniform for the static-helper call sites so the caller-facing
// message identifies which helper failed.
func defaultManagerDriver(op string) (*Manager, drivers.Driver, error) {
	m := Default()
	if m == nil {
		return nil, nil, fmt.Errorf("velocity/orm: %s: no default manager set", op)
	}
	drv, err := m.liveDriver()
	if err != nil {
		return nil, nil, errchain.Errorf("velocity/orm: %s: %w", op, err)
	}
	return m, drv, nil
}

// markExisting sets the IsExisting flag for model via the side-channel
// existence store (see existence.go). The previous typed-receiver
// interface (existenceSetter) is gone with the Existence trait drop;
// the side-channel is now the single source of truth so no type-by-type
// receivers are needed.
func markExisting[T any](model *T) {
	markModelExisting(model)
}

// denyUndeclaredMapKeys enforces deny-by-default mass assignment for the
// FirstOrCreate/UpdateOrCreate helpers. Both take two caller maps -
// conditions and values - and either map can target application columns:
// values is written on every branch, and conditions is merged into the
// insert on the miss branch. Checking the combined key set up front, before
// the lookup query runs, guarantees the hit branch rejects exactly like the
// miss branch instead of returning (FirstOrCreate) or updating
// (UpdateOrCreate) without ever policing conditions.
//
// Matching uses bulk Update's deniedUpdateKeys: both maps end up as SQL
// identifiers (conditions in the lookup WHERE, values in the write),
// where most dialects fold unquoted identifier case, so keys
// are matched case-insensitively against both the SQL column name and the
// snake-cased Go field name. Unknown keys and framework-managed embedded
// columns (id, created_at, ...) pass through. The offending caller keys are
// reported verbatim, in column order, so the error is stable.
func denyUndeclaredMapKeys[T any](conditions, values map[string]any) error {
	var zero T
	if !AccessFor(&zero).implicitDeny {
		return nil
	}
	meta := MetaFor(reflect.TypeOf(zero))
	if meta == nil {
		return nil
	}
	denied := deniedUpdateKeys(mergeConditionsAndValues(conditions, values), meta)
	if len(denied) > 0 {
		return &MassAssignmentError{Model: reflect.TypeOf(zero).String(), Keys: denied}
	}
	return nil
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
