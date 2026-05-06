package orm

import (
	"reflect"
	"sync"
)

// softDeleteScopeName is the reserved name used for the auto-registered
// soft-delete global scope. Callers may opt out of it by name via
// WithoutGlobalScope(SoftDeleteScopeName).
const softDeleteScopeName = "soft_delete"

// SoftDeleteScopeName is the public name of the auto-registered
// soft-delete scope. Pass it to WithoutGlobalScope to bypass.
const SoftDeleteScopeName = softDeleteScopeName

// scopeApplier is a type-erased scope function stored in the registry.
// It receives the *Query[T] as `any` and is recovered to its concrete
// generic type by the per-T helper that registered it.
type scopeApplier = func(q any)

// scopeEntry holds a single named scope plus the order in which it
// was registered. Order is preserved so callers see deterministic
// predicate ordering across runs.
type scopeEntry struct {
	name  string
	apply scopeApplier
	order int
}

// scopeRegistry is the per-model-type set of named global scopes.
// All access is guarded by mu so concurrent AddGlobalScope and query
// execution are safe.
type scopeRegistry struct {
	mu      sync.RWMutex
	entries map[string]*scopeEntry
	next    int
}

// globalScopeRegistry maps reflect.Type (the model T) to its
// *scopeRegistry. sync.Map handles concurrent registry creation; the
// inner *scopeRegistry handles concurrent entry mutation.
var globalScopeRegistry sync.Map

// scopeRegistryFor returns the registry for type t, creating an empty
// one on first use.
func scopeRegistryFor(t reflect.Type) *scopeRegistry {
	if existing, ok := globalScopeRegistry.Load(t); ok {
		return existing.(*scopeRegistry)
	}
	created := &scopeRegistry{entries: make(map[string]*scopeEntry)}
	actual, _ := globalScopeRegistry.LoadOrStore(t, created)
	return actual.(*scopeRegistry)
}

// modelTypeFor returns the reflect.Type for the model type T,
// dereferencing pointer types so that Model[Foo] and *Model[Foo]
// share a registry.
func modelTypeFor[T any]() reflect.Type {
	var zero T
	t := reflect.TypeOf(zero)
	if t == nil {
		return nil
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	return t
}

// AddGlobalScope registers a named global scope for model type T.
// The scope runs on every read, count, update and delete unless the
// caller opts out via WithoutGlobalScope(name) or WithoutGlobalScopes().
//
// The scope fn MUST mutate q in place (q.Where(...) etc.). It does not
// return the query: a returned replacement pointer would be silently
// dropped, which is a footgun. This matches Eloquent's
// addGlobalScope(Scope) shape, where Scope::apply returns void.
//
// Re-registering an existing name replaces the prior function. Pass a
// nil fn to remove a scope.
func AddGlobalScope[T any](name string, fn func(*Query[T])) {
	if name == "" {
		return
	}
	t := modelTypeFor[T]()
	if t == nil {
		return
	}
	reg := scopeRegistryFor(t)
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if fn == nil {
		delete(reg.entries, name)
		return
	}
	apply := func(q any) {
		typed, ok := q.(*Query[T])
		if !ok {
			return
		}
		fn(typed)
	}
	// Replace the entry rather than mutate it so concurrent readers
	// holding a snapshot of *scopeEntry pointers see immutable values.
	if existing, ok := reg.entries[name]; ok {
		reg.entries[name] = &scopeEntry{name: name, apply: apply, order: existing.order}
		return
	}
	reg.next++
	reg.entries[name] = &scopeEntry{name: name, apply: apply, order: reg.next}
}

// RemoveGlobalScope removes a previously registered scope for type T.
// No-op if the scope is not present. Primarily intended for tests.
func RemoveGlobalScope[T any](name string) {
	t := modelTypeFor[T]()
	if t == nil {
		return
	}
	reg := scopeRegistryFor(t)
	reg.mu.Lock()
	defer reg.mu.Unlock()
	delete(reg.entries, name)
}

// snapshotScopes returns the entries of the registry sorted by
// registration order. Callers iterate the snapshot without holding
// the registry lock.
func (r *scopeRegistry) snapshotScopes() []*scopeEntry {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if len(r.entries) == 0 {
		return nil
	}
	out := make([]*scopeEntry, 0, len(r.entries))
	for _, e := range r.entries {
		out = append(out, e)
	}
	// Insertion sort: registries are tiny (typically <5 scopes).
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1].order > out[j].order; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// applyGlobalScopes runs every registered scope for type T against q,
// skipping scopes whose name appears in q.disabledScopes. Idempotent
// per query: a Query[T] applies its scopes at most once, even if a
// terminal calls another terminal that also invokes apply.
func (q *Query[T]) applyGlobalScopes() {
	if q == nil || q.globalScopesApplied {
		return
	}
	q.globalScopesApplied = true

	t := modelTypeFor[T]()
	if t == nil {
		return
	}
	reg := scopeRegistryFor(t)
	for _, entry := range reg.snapshotScopes() {
		if q.disabledScopes[entry.name] {
			continue
		}
		entry.apply(q)
	}
}

// WithoutGlobalScope marks the named global scope as skipped for this
// query. Returns the same *Query[T] so it composes inline with the
// other chain methods.
func (q *Query[T]) WithoutGlobalScope(name string) *Query[T] {
	if q == nil || name == "" {
		return q
	}
	if q.disabledScopes == nil {
		q.disabledScopes = make(map[string]bool)
	}
	q.disabledScopes[name] = true
	return q
}

// WithoutGlobalScopes marks every registered global scope for type T
// as skipped for this query.
func (q *Query[T]) WithoutGlobalScopes() *Query[T] {
	if q == nil {
		return q
	}
	t := modelTypeFor[T]()
	if t == nil {
		return q
	}
	if q.disabledScopes == nil {
		q.disabledScopes = make(map[string]bool)
	}
	reg := scopeRegistryFor(t)
	for _, entry := range reg.snapshotScopes() {
		q.disabledScopes[entry.name] = true
	}
	return q
}

// registerSoftDeleteScopeOnce auto-registers the built-in soft-delete
// scope for type T the first time newQuery sees a soft-delete model.
// The scope reads q.withTrashed / q.onlyTrashed at apply time so the
// existing WithTrashed() / OnlyTrashed() chain methods keep working.
func registerSoftDeleteScopeOnce[T any]() {
	t := modelTypeFor[T]()
	if t == nil {
		return
	}
	if _, done := softDeleteScopeRegistered.Load(t); done {
		return
	}
	AddGlobalScope[T](softDeleteScopeName, func(q *Query[T]) {
		if q.withTrashed {
			if q.onlyTrashed {
				q.WhereNotNull("deleted_at")
			}
			return
		}
		q.WhereNull("deleted_at")
	})
	softDeleteScopeRegistered.Store(t, true)
}

// softDeleteScopeRegistered tracks which model types have already had
// their built-in soft-delete scope auto-registered.
var softDeleteScopeRegistered sync.Map
