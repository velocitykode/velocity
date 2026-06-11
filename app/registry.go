package app

import (
	"fmt"
	"reflect"

	"github.com/velocitykode/velocity/contract"
)

// Default is the qualifier marker used for ordinary single-instance
// registrations. Register[T] and Get[T] are shorthands for RegisterFor[T,
// Default] and GetFor[T, Default]. Multi-instance registrations supply their
// own integrator-owned marker types instead (see RegisterFor).
type Default struct{}

// ComponentKey is the identity of a registry entry. It pairs the registered
// type with a qualifier type so the same concrete or interface type can be
// registered more than once under distinct, integrator-owned marker types.
//
// Type is the exact reflect.Type the value was registered under (reflect.TypeFor
// of the registration's T): a concrete type for the common case, or an
// interface type when an SDK opts into interface-keyed registration. There is
// NO dynamic interface satisfaction; see Get.
//
// Qualifier is app.Default for single-instance registrations and the marker
// type for multi-instance ones.
type ComponentKey struct {
	Type      reflect.Type
	Qualifier reflect.Type
}

// String renders the key for error messages and introspection. For a default
// qualifier it is just the type name, e.g. "*ai.Client"; for a multi-instance
// registration it appends the marker, e.g.
// "*foo.Thing[qualifier main.PrimaryFoo]".
func (k ComponentKey) String() string {
	t := "<nil>"
	if k.Type != nil {
		t = k.Type.String()
	}
	if k.Qualifier == nil || k.Qualifier == reflect.TypeFor[Default]() {
		return t
	}
	return fmt.Sprintf("%s[qualifier %s]", t, k.Qualifier.String())
}

// componentEntry is one registered component: its key, the raw registered
// value, and any hook adapters attached via WithHooks.
type componentEntry struct {
	key   ComponentKey
	value any
	hooks []any
}

// RegisterOption customises a registration. The only option today is WithHooks.
type RegisterOption func(*componentEntry)

// WithHooks attaches adapter objects to a registration. Hooks are supplied by
// integrators bridging velocity-unaware libraries: a plain third-party value
// cannot implement framework contract interfaces, so the integrator writes a
// thin adapter and passes it here. Later wiring sweeps the value AND its hooks
// for contract.EventDispatcherAware (event-dispatcher injection) and
// contract.ShutdownAware (reverse-order teardown); Get always returns the raw
// registered value, never a hook. Multiple WithHooks options accumulate.
//
// Ownership rule: the registry owns teardown of the registered value and its
// hooks. A provider that registers a value MUST NOT also close it (or close a
// resource the hook owns) in its own Shutdown; the App.Shutdown registry sweep
// closes anything that implements contract.ShutdownAware exactly once.
func WithHooks(hooks ...any) RegisterOption {
	return func(e *componentEntry) {
		e.hooks = append(e.hooks, hooks...)
	}
}

// Register stores v in the type-keyed component registry under the default
// qualifier. It is shorthand for RegisterFor[T, Default].
//
// The key is reflect.TypeFor[T](): registration and retrieval match on the
// EXACT type, with no dynamic interface satisfaction. The convention is to
// register the concrete type and have the SDK export a From(s) accessor;
// registering an interface type is an explicit opt-in that only Get[SameIface]
// will find. Because a Go type's identity includes its import path, two
// modules can never collide, and a /v2 module path is a distinct type that
// coexists with v1.
//
// Returns an error if v is nil, whether an untyped nil or a typed nil
// pointer/map/chan/func/slice (a nil value would make Get useless and would
// panic the event-wiring sweep), or if the key is already registered (a
// duplicate usually means a provider ran twice). A provider that registers a
// value MUST NOT also close it in its own
// Shutdown: the registry owns reverse-order teardown of registered values.
func Register[T any](s *Services, v T, opts ...RegisterOption) error {
	return RegisterFor[T, Default](s, v, opts...)
}

// RegisterFor stores v under the exact type T qualified by the marker type Q.
// Multi-instance registration is achieved with integrator-owned marker types,
// never strings:
//
//	RegisterFor[*foo.Thing, PrimaryFoo](s, a)
//	RegisterFor[*foo.Thing, AnalyticsFoo](s, b)
//
// reflect.TypeFor[T] handles both interface and concrete T. See Register for
// the exact-match, ownership, and collision semantics; this is the explicit
// form it delegates to.
func RegisterFor[T any, Q any](s *Services, v T, opts ...RegisterOption) error {
	key := ComponentKey{reflect.TypeFor[T](), reflect.TypeFor[Q]()}
	if s == nil {
		return fmt.Errorf("velocity/app: cannot register component %s on nil Services", key)
	}
	if isNilValue(v) {
		return fmt.Errorf("velocity/app: component %s is nil", key)
	}

	entry := componentEntry{
		key:   key,
		value: v,
	}
	for _, opt := range opts {
		opt(&entry)
	}

	s.compMu.Lock()
	defer s.compMu.Unlock()
	if s.componentIdx == nil {
		s.componentIdx = make(map[ComponentKey]int)
	}
	if _, exists := s.componentIdx[entry.key]; exists {
		return fmt.Errorf("velocity/app: component %s already registered", entry.key)
	}
	s.componentIdx[entry.key] = len(s.componentOrder)
	s.componentOrder = append(s.componentOrder, entry)
	return nil
}

// Get retrieves the component registered under type T and the default
// qualifier. It is shorthand for GetFor[T, Default].
//
// Lookup is by EXACT type: Get[SomeIface] only finds an entry registered with
// T=SomeIface, never a concrete value that merely satisfies SomeIface. Returns
// the raw registered value (no wrapper). Returns an error if the key is not
// registered, or if the stored value does not assert to T (which cannot happen
// for values registered through Register, but is checked rather than panicked;
// rule #10).
func Get[T any](s *Services) (T, error) {
	return GetFor[T, Default](s)
}

// GetFor retrieves the component registered under the exact type T qualified by
// marker type Q. See Get for the exact-match and no-panic semantics.
func GetFor[T any, Q any](s *Services) (T, error) {
	var zero T
	key := ComponentKey{reflect.TypeFor[T](), reflect.TypeFor[Q]()}

	if s == nil {
		return zero, fmt.Errorf("velocity/app: component %s not registered (nil Services)", key)
	}

	s.compMu.RLock()
	idx, ok := s.componentIdx[key]
	var v any
	if ok {
		v = s.componentOrder[idx].value
	}
	s.compMu.RUnlock()

	if !ok {
		return zero, fmt.Errorf("velocity/app: component %s not registered", key)
	}
	typed, ok := v.(T)
	if !ok {
		return zero, fmt.Errorf("velocity/app: component %s is %T, not %T", key, v, zero)
	}
	return typed, nil
}

// RangeComponents calls fn for every registered component. fn is invoked
// OUTSIDE the compMu critical section: componentOrder is snapshotted under
// RLock, the lock is released, and fn iterates the snapshot. This means fn is
// free to call Register or any other Services method without deadlocking, and a
// slow fn cannot block concurrent Register writes. Components added after the
// snapshot is taken will not be visible to this iteration; call Range again to
// see them.
//
// Returns false from fn to halt iteration early.
//
// The framework uses this from bootstrap.wireInstanceEvents to push the
// instance event dispatcher into every registered value (and hook) that
// implements contract.EventDispatcherAware, and from App.Shutdown to sweep
// values and hooks implementing contract.ShutdownAware.
func (s *Services) RangeComponents(fn func(key ComponentKey, v any, hooks []any) bool) {
	if s == nil {
		return
	}
	s.compMu.RLock()
	snapshot := make([]componentEntry, len(s.componentOrder))
	copy(snapshot, s.componentOrder)
	for i := range snapshot {
		snapshot[i].hooks = append([]any(nil), s.componentOrder[i].hooks...)
	}
	s.compMu.RUnlock()
	for _, e := range snapshot {
		if !fn(e.key, e.value, e.hooks) {
			return
		}
	}
}

// ComponentInfo is an introspection record for one registered component. The
// EventAware / ShutdownAware facets report whether the value OR any of its
// hooks satisfies the corresponding contract interface, which is exactly the
// condition the event-wiring and shutdown sweeps act on.
type ComponentInfo struct {
	Key           ComponentKey
	EventAware    bool
	ShutdownAware bool
	HookCount     int
}

// ListComponents returns an introspection record per registered component in
// registration order. The facets are computed by asserting the value and each
// hook against contract.EventDispatcherAware and contract.ShutdownAware.
func ListComponents(s *Services) []ComponentInfo {
	var infos []ComponentInfo
	s.RangeComponents(func(key ComponentKey, v any, hooks []any) bool {
		info := ComponentInfo{Key: key, HookCount: len(hooks)}
		info.EventAware = isEventAware(v)
		info.ShutdownAware = isShutdownAware(v)
		for _, h := range hooks {
			if !info.EventAware && isEventAware(h) {
				info.EventAware = true
			}
			if !info.ShutdownAware && isShutdownAware(h) {
				info.ShutdownAware = true
			}
		}
		infos = append(infos, info)
		return true
	})
	return infos
}

func isEventAware(v any) bool {
	_, ok := v.(contract.EventDispatcherAware)
	return ok
}

func isShutdownAware(v any) bool {
	_, ok := v.(contract.ShutdownAware)
	return ok
}

// isNilValue reports whether v is an untyped nil or a typed nil of a nilable
// kind (pointer, map, chan, func, slice, interface). A typed-nil registration
// must be rejected up front: it would survive a plain any(v) == nil check, and
// the event-wiring sweep would later call SetEventDispatcher on the nil
// receiver and panic during bootstrap.
func isNilValue(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Chan, reflect.Func, reflect.Slice, reflect.Interface:
		return rv.IsNil()
	default:
		return false
	}
}
