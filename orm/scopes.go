package orm

import (
	"context"
	"reflect"
	"sync"

	"github.com/velocitykode/velocity/orm/drivers"
)

// softDeleteScopeName is the reserved name used for the auto-registered
// soft-delete global scope. Callers may opt out of it by name via
// WithoutGlobalScope(SoftDeleteScopeName).
const softDeleteScopeName = "soft_delete"

// SoftDeleteScopeName is the public name of the auto-registered
// soft-delete scope. Pass it to WithoutGlobalScope to bypass.
const SoftDeleteScopeName = softDeleteScopeName

// scopeApplier is a type-erased scope function stored in the registry.
// It receives the ctx and *Query[T] as `any` and is recovered to its
// concrete generic type by the per-T helper that registered it.
type scopeApplier = func(ctx context.Context, q any)

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
// The scope fn receives the per-call ctx and the *Query[T]; it MUST
// mutate q in place (q.Where(...) etc.). It does not return the query:
// a returned replacement pointer would be silently dropped, which is a
// footgun. This matches Eloquent's addGlobalScope(Scope) shape, where
// Scope::apply returns void. ctx is the same context.Context passed to
// the terminal that triggered the apply, so scopes can read tenant /
// actor / locale values plumbed through ctx.
//
// Re-registering an existing name replaces the prior function. Pass a
// nil fn to remove a scope.
func AddGlobalScope[T any](name string, fn func(ctx context.Context, q *Query[T])) {
	if name == "" {
		return
	}
	t := modelTypeFor[T]()
	if t == nil {
		return
	}
	// Register a fresh-Query[T] constructor so reflect-only callers
	// (eager-load helpers in relation*.go / morph.go) can apply T's
	// global scopes without knowing T at compile time. If T is a
	// soft-delete model, also wire its built-in soft-delete scope so
	// eager-loads of T do not bypass deleted_at IS NULL just because no
	// newQuery[T] has ever been constructed yet.
	rememberQueryConstructor[T]()
	if modelHasSoftDelete[T]() {
		registerSoftDeleteScopeOnce[T]()
	}
	reg := scopeRegistryFor(t)
	reg.mu.Lock()
	defer reg.mu.Unlock()
	if fn == nil {
		delete(reg.entries, name)
		return
	}
	apply := func(ctx context.Context, q any) {
		typed, ok := q.(*Query[T])
		if !ok {
			return
		}
		fn(ctx, typed)
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
// terminal calls another terminal that also invokes apply. ctx is
// forwarded to each scope so callers can read tenant / actor / locale
// values plumbed through it.
func (q *Query[T]) applyGlobalScopes(ctx context.Context) {
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
		entry.apply(ctx, q)
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
//
// The "done" flag is recorded BEFORE invoking AddGlobalScope so the
// reentrancy that AddGlobalScope itself performs (AddGlobalScope calls
// registerSoftDeleteScopeOnce when T is a soft-delete model, to make
// reflect-only eager-load callers see the soft-delete predicate) is
// terminated at the first frame instead of recursing indefinitely.
func registerSoftDeleteScopeOnce[T any]() {
	t := modelTypeFor[T]()
	if t == nil {
		return
	}
	if _, loaded := softDeleteScopeRegistered.LoadOrStore(t, true); loaded {
		return
	}
	AddGlobalScope[T](softDeleteScopeName, func(_ context.Context, q *Query[T]) {
		if q.withTrashed {
			if q.onlyTrashed {
				q.WhereNotNull("deleted_at")
			}
			return
		}
		q.WhereNull("deleted_at")
	})
}

// softDeleteScopeRegistered tracks which model types have already had
// their built-in soft-delete scope auto-registered.
var softDeleteScopeRegistered sync.Map

// scopedQuery is the non-generic surface every *Query[T] satisfies for
// reflect-based scope application. The eager-load helpers in
// relation.go / relation_m2m.go / relation_polymorphic.go / morph.go
// only know the related model type as a reflect.Type, so they cannot
// call applyGlobalScopes directly. They route through
// applyGlobalScopesByType, which constructs a fresh *Query[Related] via
// a per-type constructor (queryConstructorFor), runs every registered
// scope against it, and surfaces the accumulated WHERE conditions back
// to the caller through this interface.
type scopedQuery interface {
	// scopeConditions returns the accumulated WHERE conditions on the
	// underlying *Query[T] after applyGlobalScopes has run.
	scopeConditions() []drivers.Condition
	// applyScopesFromCtx is the entry point for the reflect helper;
	// it invokes applyGlobalScopes with the supplied ctx so each
	// registered scope fn sees the caller's context.
	applyScopesFromCtx(ctx context.Context)
}

// scopeConditions implements scopedQuery. Returns the underlying
// conditions slice directly; callers must not mutate it.
func (q *Query[T]) scopeConditions() []drivers.Condition {
	if q == nil {
		return nil
	}
	return q.conditions
}

// applyScopesFromCtx implements scopedQuery. It is a thin wrapper
// around applyGlobalScopes that routes through the same idempotency
// guard.
func (q *Query[T]) applyScopesFromCtx(ctx context.Context) {
	if q == nil {
		return
	}
	q.applyGlobalScopes(ctx)
}

// queryConstructors maps reflect.Type (the model T) to a func that
// returns a fresh, driver-less *Query[T] as `any`. The constructor is
// registered the first time AddGlobalScope[T] or
// registerSoftDeleteScopeOnce[T] runs for the type, because both call
// paths have a compile-time T from which Query[T] can be instantiated.
// A reflect-only caller (eager-load helpers) calls
// queryConstructorFor(t)() to obtain a typed query without knowing T
// at compile time.
var queryConstructors sync.Map // map[reflect.Type]func() any

// rememberQueryConstructor stores a constructor for *Query[T] keyed by
// modelTypeFor[T](). Idempotent: re-registration on the same type is a
// no-op, so this is safe to call from every code path that has access
// to T (AddGlobalScope, registerSoftDeleteScopeOnce, newQuery).
func rememberQueryConstructor[T any]() {
	t := modelTypeFor[T]()
	if t == nil {
		return
	}
	if _, ok := queryConstructors.Load(t); ok {
		return
	}
	queryConstructors.LoadOrStore(t, func() any {
		// Build a fresh query but skip the Default() driver wiring: the
		// caller (applyGlobalScopesByType) only needs the conditions
		// the scopes append. Driver-dependent state (table name, soft
		// delete metadata) is irrelevant because the eager-load helper
		// has its own table name and only consumes the WHERE clause.
		return &Query[T]{
			table:         getTableName[T](),
			columns:       []string{"*"},
			hasSoftDelete: modelHasSoftDelete[T](),
		}
	})
}

// queryConstructorFor returns the registered fresh-query constructor
// for type t, or nil when no scope (including soft-delete) has been
// registered for t. The nil return is the signal to skip scope
// application for this type entirely (no scopes means no extra
// conditions to inject).
func queryConstructorFor(t reflect.Type) func() any {
	v, ok := queryConstructors.Load(t)
	if !ok {
		return nil
	}
	fn, ok := v.(func() any)
	if !ok {
		return nil
	}
	return fn
}

// applyGlobalScopesByType is the reflect-friendly counterpart to
// (*Query[T]).applyGlobalScopes. It looks up the registered query
// constructor for t, builds a fresh *Query[T], runs every registered
// scope against it, and returns the WHERE conditions the scopes
// appended. Eager-load helpers in relation.go, relation_m2m.go,
// relation_polymorphic.go, and morph.go call this so a hand-rolled
// "SELECT * FROM table WHERE fk IN (...)" query still honours tenant /
// archive / locale / state scopes registered on the related model.
//
// Returns nil conditions (not an error) when no constructor is
// registered for t: that case means no AddGlobalScope[T] or soft-delete
// model has ever been seen for the type, so the scope set is empty by
// construction.
func applyGlobalScopesByType(ctx context.Context, t reflect.Type) []drivers.Condition {
	if t == nil {
		return nil
	}
	ctor := queryConstructorFor(t)
	if ctor == nil {
		return nil
	}
	q, ok := ctor().(scopedQuery)
	if !ok {
		return nil
	}
	q.applyScopesFromCtx(ctx)
	return q.scopeConditions()
}
