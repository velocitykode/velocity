package orm

import (
	"reflect"
	"sync"
	"time"
	"unsafe"
	"weak"
)

// existenceStore is the package-level side-channel for per-instance
// model state: the IsExisting bit (drives Save's INSERT-vs-UPDATE
// decision) and an optional Original snapshot used for change tracking
// (orm.Track / orm.IsDirty / orm.HasChanged / orm.IsClean).
//
// The key is the model's address as uintptr; the value is an
// existenceEntry whose alive() closure consults a weak.Pointer to the
// model so a stale entry left behind by a GC'd model is detectable on
// read - closing the address-reuse race that a naive uintptr-keyed
// cache exhibits (model A at addr X is GC'd, model B is allocated at
// addr X, B would inherit A's "existing" bit; weak.Pointer detects
// this).
//
// LIFECYCLE: process-scoped. The reaper goroutine started by startSweep
// runs for the lifetime of the process; it is NOT tied to *Manager and
// is NOT stoppable. Intentional for the framework's one-Manager-per-
// process usage; if a future deployment topology needs multiple
// short-lived Managers, the sweep must be made per-Manager.
var existenceStore sync.Map

// existenceKey is the uintptr address of a model. Used as the sync.Map
// key; equality between two existenceKey values is necessary but not
// sufficient for "same object" - the alive() closure on the entry
// is the source of truth.
type existenceKey = uintptr

// existenceEntry pairs the per-model state (existing bit + optional
// change-tracking snapshot) with a lifetime witness. alive() returns
// true iff the model the entry describes is still reachable.
// Implementations capture a typed weak.Pointer in a closure so the
// entry can outlive the model's GC without leaking false positives.
//
// The "existing" bit is implicit: an entry's mere presence (with
// alive() true) means the row is persisted. Save consults this to
// switch between INSERT and UPDATE.
//
// The Original snapshot is non-nil only after orm.Track has been
// called on the model. It records the field state at track time;
// orm.IsDirty / orm.HasChanged compute deltas lazily by re-snapshot-
// and-compare instead of intercepting field writes (Go has no
// field-set hooks; codegen would be the alternative).
type existenceEntry struct {
	alive    func() bool
	original map[string]any
}

var sweepStarted sync.Once

// startSweep launches the background reaper. Idempotent. Triggered
// lazily on first store.
func startSweep() {
	sweepStarted.Do(func() {
		go func() {
			t := time.NewTicker(30 * time.Second)
			defer t.Stop()
			for range t.C {
				existenceStore.Range(func(k, v any) bool {
					if e, ok := v.(existenceEntry); ok && !e.alive() {
						existenceStore.Delete(k)
					}
					return true
				})
			}
		}()
	})
}

// markModelExisting records that the row is persisted. Idempotent.
func markModelExisting[T any](model *T) {
	if model == nil {
		return
	}
	storeExistenceBitTyped(model)
}

// isModelExisting reports whether the row is persisted. Detects
// stale entries (GC'd predecessor at the same address) via the weak
// pointer's alive() witness.
func isModelExisting[T any](model *T) bool {
	if model == nil {
		return false
	}
	key := pointerKeyTyped(model)
	if key == 0 {
		return false
	}
	cur, ok := existenceStore.Load(key)
	if !ok {
		return false
	}
	e, ok := cur.(existenceEntry)
	if !ok || !e.alive() {
		existenceStore.Delete(key)
		return false
	}
	return true
}

// storeExistenceBitTyped is the typed-pointer entry. The alive() closure
// captures a typed weak.Pointer so the read path can detect address-reuse.
//
// Preserves any prior tracking snapshot at the same key so a
// markExisting-after-Track flow keeps the snapshot intact (a Save that
// re-marks an already-tracked row should not wipe the diff baseline).
func storeExistenceBitTyped[T any](model *T) {
	key := pointerKeyTyped(model)
	if key == 0 {
		return
	}
	w := weak.Make(model)
	var snap map[string]any
	if cur, loaded := existenceStore.Load(key); loaded {
		if e, ok := cur.(existenceEntry); ok && e.alive() {
			snap = e.original
		}
	}
	existenceStore.Store(key, existenceEntry{
		alive:    func() bool { return w.Value() != nil },
		original: snap,
	})
	startSweep()
}

// storeExistenceBitFromAny is the reflective-entry counterpart.
//
// Invariant: addr must be a pointer to the START of a heap allocation
// (i.e. produced by &someStruct or reflect.Value.Addr().Interface() on
// an addressable struct). Passing an interior pointer breaks
// weak.Make's lifetime tracking. Current callers honor the invariant.
func storeExistenceBitFromAny(addr any) {
	v := reflect.ValueOf(addr)
	if v.Kind() != reflect.Ptr || v.IsNil() {
		return
	}
	if v.Elem().Kind() != reflect.Struct {
		return
	}
	key := existenceKey(v.Pointer())
	if key == 0 {
		return
	}
	bp := (*byte)(unsafe.Pointer(v.Pointer()))
	w := weak.Make(bp)
	var snap map[string]any
	if cur, loaded := existenceStore.Load(key); loaded {
		if e, ok := cur.(existenceEntry); ok && e.alive() {
			snap = e.original
		}
	}
	existenceStore.Store(key, existenceEntry{
		alive:    func() bool { return w.Value() != nil },
		original: snap,
	})
	startSweep()
}

// pointerKeyTyped returns the address of the struct pointed to by model.
// Returns 0 (sentinel) for any input that doesn't fit the "non-nil
// pointer" contract.
func pointerKeyTyped[T any](model *T) existenceKey {
	if model == nil {
		return 0
	}
	return existenceKey(uintptr(unsafe.Pointer(model)))
}

// ============================================================================
// Public Track API
// ============================================================================

// Track captures a snapshot of the model's persistable fields so
// IsDirty / HasChanged / IsClean can report deltas later. Tracking is
// per-call opt-in: callers who don't need change inspection pay zero
// (the snapshot is the only meaningful cost of the per-row tracker).
//
// The model must already be persisted (Save'd or loaded from a query)
// for tracking to be meaningful; calling Track on an unpersisted
// in-memory struct is a no-op.
//
// Implementation: snapshot is captured eagerly via reflection over the
// trait fingerprint's column list. Subsequent Track calls on the same
// pointer overwrite the snapshot (re-baseline after a successful Save,
// for example).
func Track[T any](model *T) {
	if model == nil {
		return
	}
	key := pointerKeyTyped(model)
	if key == 0 {
		return
	}
	snap := captureSnapshot(model)
	if snap == nil {
		return
	}
	w := weak.Make(model)
	existenceStore.Store(key, existenceEntry{
		alive:    func() bool { return w.Value() != nil },
		original: snap,
	})
	startSweep()
}

// IsDirty reports whether any tracked field has changed since the
// model was last Track'd. Returns false when tracking is not active
// (Track was never called for this pointer).
func IsDirty[T any](model *T) bool {
	if model == nil {
		return false
	}
	key := pointerKeyTyped(model)
	cur, ok := existenceStore.Load(key)
	if !ok {
		return false
	}
	e, ok := cur.(existenceEntry)
	if !ok || !e.alive() || e.original == nil {
		return false
	}
	now := captureSnapshot(model)
	for k, v := range e.original {
		if !reflect.DeepEqual(now[k], v) {
			return true
		}
	}
	return false
}

// IsClean reports the inverse of IsDirty.
func IsClean[T any](model *T) bool {
	return !IsDirty(model)
}

// HasChanged reports whether the named field has changed since
// tracking started. Returns false when tracking is inactive or when
// the field is not part of the column set.
func HasChanged[T any](model *T, field string) bool {
	if model == nil {
		return false
	}
	key := pointerKeyTyped(model)
	cur, ok := existenceStore.Load(key)
	if !ok {
		return false
	}
	e, ok := cur.(existenceEntry)
	if !ok || !e.alive() || e.original == nil {
		return false
	}
	now := captureSnapshot(model)
	return !reflect.DeepEqual(now[field], e.original[field])
}

// MarkClean re-baselines tracking so subsequent IsDirty / HasChanged
// calls compare against the current state rather than the original
// snapshot. Useful after a successful Save when the caller wants to
// continue tracking.
func MarkClean[T any](model *T) {
	if model == nil {
		return
	}
	Track(model)
}

// IsExisting is the public form of isModelExisting. Reports whether
// the row is persisted (and the side-channel entry is still alive).
// Useful in branching code that wants to check existence without
// triggering a Save.
func IsExisting[T any](model *T) bool {
	return isModelExisting(model)
}

// captureSnapshot reads the persistable column values from model into
// a fresh map. Returns nil when the type has no ModelMeta (e.g. plain
// data with no orm traits). Used by Track / IsDirty / HasChanged.
func captureSnapshot[T any](model *T) map[string]any {
	v := reflect.ValueOf(model).Elem()
	meta := MetaForValue(v)
	if meta == nil {
		return nil
	}
	cols := meta.Columns()
	if len(cols) == 0 {
		return nil
	}
	snap := make(map[string]any, len(cols))
	for _, col := range cols {
		fv := v.FieldByIndex(col.IndexPath)
		if fv.IsValid() && fv.CanInterface() {
			snap[col.FieldName] = fv.Interface()
		}
	}
	return snap
}
