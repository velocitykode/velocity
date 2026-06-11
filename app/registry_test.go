package app_test

import (
	"context"
	"reflect"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/app"
)

// --- fixtures ---

// concreteThing is a velocity-unaware value with a Close method, bridged into
// the registry via an adapter hook in the relevant tests.
type concreteThing struct{ name string }

// greeter is an interface used to exercise interface-keyed registration.
type greeter interface{ Greet() string }

type politeGreeter struct{}

func (politeGreeter) Greet() string { return "hello" }

// qualifier markers
type primaryFoo struct{}
type analyticsFoo struct{}

// eventAware implements contract.EventDispatcherAware.
type eventAware struct {
	mu sync.Mutex
	fn func(ctx context.Context, event any) error
}

func (e *eventAware) SetEventDispatcher(fn func(ctx context.Context, event any) error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fn = fn
}

// shutdownAware implements contract.ShutdownAware.
type shutdownAware struct{ called int }

func (s *shutdownAware) Shutdown(ctx context.Context) error { s.called++; return nil }

// bothAware implements both contract interfaces; stands in for a first-party SDK.
type bothAware struct {
	eventAware
	shutdownAware
}

// --- ComponentKey.String ---

func TestComponentKeyString(t *testing.T) {
	tests := []struct {
		name string
		key  app.ComponentKey
		want string
	}{
		{
			name: "default qualifier omits marker",
			key:  app.ComponentKey{Type: reflect.TypeFor[*concreteThing](), Qualifier: reflect.TypeFor[app.Default]()},
			want: "*app_test.concreteThing",
		},
		{
			name: "nil qualifier omits marker",
			key:  app.ComponentKey{Type: reflect.TypeFor[*concreteThing]()},
			want: "*app_test.concreteThing",
		},
		{
			name: "custom qualifier appended",
			key:  app.ComponentKey{Type: reflect.TypeFor[*concreteThing](), Qualifier: reflect.TypeFor[primaryFoo]()},
			want: "*app_test.concreteThing[qualifier app_test.primaryFoo]",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.key.String(); got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

// --- register / get roundtrip ---

func TestRegisterGetRoundtrip(t *testing.T) {
	s := &app.Services{}

	c := &concreteThing{name: "c"}
	if err := app.Register(s, c); err != nil {
		t.Fatalf("Register concrete: %v", err)
	}
	got, err := app.Get[*concreteThing](s)
	if err != nil {
		t.Fatalf("Get concrete: %v", err)
	}
	if got != c {
		t.Fatalf("Get returned %v, want %v", got, c)
	}

	// interface-typed key: register T=greeter explicitly.
	var g greeter = politeGreeter{}
	if err := app.Register[greeter](s, g); err != nil {
		t.Fatalf("Register interface: %v", err)
	}
	gotG, err := app.Get[greeter](s)
	if err != nil {
		t.Fatalf("Get interface: %v", err)
	}
	if gotG.Greet() != "hello" {
		t.Fatalf("Greet() = %q", gotG.Greet())
	}
}

// Registering a concrete value does NOT make it retrievable by an interface it
// satisfies: matching is exact-type only.
func TestNoDynamicInterfaceSatisfaction(t *testing.T) {
	s := &app.Services{}
	if err := app.Register(s, politeGreeter{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	if _, err := app.Get[greeter](s); err == nil {
		t.Fatal("Get[greeter] should miss a concretely-registered value")
	}
}

// --- duplicate key ---

func TestRegisterDuplicate(t *testing.T) {
	s := &app.Services{}
	if err := app.Register(s, &concreteThing{}); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := app.Register(s, &concreteThing{})
	if err == nil {
		t.Fatal("duplicate Register should error")
	}
	want := "velocity/app: component *app_test.concreteThing already registered"
	if err.Error() != want {
		t.Fatalf("error = %q, want %q", err.Error(), want)
	}
}

// --- qualifier separation ---

func TestQualifierSeparation(t *testing.T) {
	s := &app.Services{}
	a := &concreteThing{name: "primary"}
	b := &concreteThing{name: "analytics"}
	if err := app.RegisterFor[*concreteThing, primaryFoo](s, a); err != nil {
		t.Fatalf("RegisterFor primary: %v", err)
	}
	if err := app.RegisterFor[*concreteThing, analyticsFoo](s, b); err != nil {
		t.Fatalf("RegisterFor analytics: %v", err)
	}

	gotA, err := app.GetFor[*concreteThing, primaryFoo](s)
	if err != nil || gotA != a {
		t.Fatalf("GetFor primary = %v, %v", gotA, err)
	}
	gotB, err := app.GetFor[*concreteThing, analyticsFoo](s)
	if err != nil || gotB != b {
		t.Fatalf("GetFor analytics = %v, %v", gotB, err)
	}

	// default qualifier sees neither qualified instance.
	if _, err := app.Get[*concreteThing](s); err == nil {
		t.Fatal("Get default should not see qualified entries")
	}
}

// --- untyped nil rejection ---

func TestRegisterNilValues(t *testing.T) {
	// untyped nil through an interface T is rejected.
	s := &app.Services{}
	var g greeter // nil interface
	if err := app.Register[greeter](s, g); err == nil {
		t.Fatal("untyped-nil interface Register should error")
	}

	// typed nil pointer is rejected too: it would survive any(v) == nil and
	// later panic the event-wiring sweep (SetEventDispatcher on nil receiver).
	var nilThing *concreteThing
	if err := app.Register(s, nilThing); err == nil {
		t.Fatal("typed-nil pointer Register should error")
	}

	// typed nils of other nilable kinds are rejected the same way.
	var nilFn func() //nolint:staticcheck // nil func literal is the test subject; SA4023 flags it as always-nil
	if err := app.Register(s, nilFn); err == nil {
		t.Fatal("typed-nil func Register should error")
	}
	var nilMap map[string]int
	if err := app.Register(s, nilMap); err == nil {
		t.Fatal("typed-nil map Register should error")
	}

	// non-nil values of nilable kinds still register fine.
	if err := app.Register(s, &concreteThing{name: "ok"}); err != nil {
		t.Fatalf("non-nil pointer Register should succeed: %v", err)
	}
	if err := app.Register(s, map[string]int{"a": 1}); err != nil {
		t.Fatalf("non-nil map Register should succeed: %v", err)
	}

	// nil *Services is an error, not a panic (rule #10).
	if err := app.Register((*app.Services)(nil), &concreteThing{}); err == nil {
		t.Fatal("Register on nil Services should error")
	}
}

func TestNilServicesAccessors(t *testing.T) {
	var s *app.Services

	// Get on nil Services errors instead of panicking.
	if _, err := app.Get[*concreteThing](s); err == nil {
		t.Fatal("Get on nil Services should error")
	}

	// RangeComponents and ListComponents on nil Services are no-ops.
	called := false
	s.RangeComponents(func(app.ComponentKey, any, []any) bool {
		called = true
		return true
	})
	if called {
		t.Fatal("RangeComponents on nil Services should not invoke fn")
	}
	if infos := app.ListComponents(s); len(infos) != 0 {
		t.Fatalf("ListComponents on nil Services = %d entries, want 0", len(infos))
	}
}

// --- error texts: missing vs mismatch ---

func TestGetErrorTexts(t *testing.T) {
	s := &app.Services{}

	_, err := app.Get[*concreteThing](s)
	if err == nil {
		t.Fatal("missing-key Get should error")
	}
	wantMissing := "velocity/app: component *app_test.concreteThing not registered"
	if err.Error() != wantMissing {
		t.Fatalf("missing error = %q, want %q", err.Error(), wantMissing)
	}

	// Mismatch: construct an entry whose stored value does not assert to the
	// requested T but shares the same key. We cannot reach this through
	// Register (the key encodes T), so verify the message format on the
	// not-registered branch only and trust the assertion guard. To exercise
	// the assertion path we register an interface key with a value, then it
	// always asserts; mismatch is structurally unreachable via the public
	// API. This test documents the missing-key wording precisely.
}

// --- WithHooks visible via Range + List facets ---

func TestWithHooksFacets(t *testing.T) {
	s := &app.Services{}

	// velocity-unaware value, lifecycle supplied by a hook adapter.
	thing := &concreteThing{name: "bridged"}
	hook := &bothAware{}
	if err := app.Register(s, thing, app.WithHooks(hook)); err != nil {
		t.Fatalf("Register with hooks: %v", err)
	}

	// Get returns the RAW value, never the hook.
	got, err := app.Get[*concreteThing](s)
	if err != nil || got != thing {
		t.Fatalf("Get = %v, %v", got, err)
	}

	// RangeComponents exposes the hooks.
	var sawHooks int
	s.RangeComponents(func(key app.ComponentKey, v any, hooks []any) bool {
		sawHooks = len(hooks)
		return true
	})
	if sawHooks != 1 {
		t.Fatalf("RangeComponents hooks = %d, want 1", sawHooks)
	}

	infos := app.ListComponents(s)
	if len(infos) != 1 {
		t.Fatalf("ListComponents len = %d, want 1", len(infos))
	}
	info := infos[0]
	if !info.EventAware {
		t.Error("EventAware should be true via hook")
	}
	if !info.ShutdownAware {
		t.Error("ShutdownAware should be true via hook")
	}
	if info.HookCount != 1 {
		t.Errorf("HookCount = %d, want 1", info.HookCount)
	}
}

// Facets driven by the value itself (first-party SDK case), no hooks.
func TestValueFacets(t *testing.T) {
	s := &app.Services{}
	if err := app.Register(s, &bothAware{}); err != nil {
		t.Fatalf("Register: %v", err)
	}
	infos := app.ListComponents(s)
	if len(infos) != 1 {
		t.Fatalf("len = %d", len(infos))
	}
	if !infos[0].EventAware || !infos[0].ShutdownAware {
		t.Fatalf("facets = %+v, want both true", infos[0])
	}
	if infos[0].HookCount != 0 {
		t.Fatalf("HookCount = %d, want 0", infos[0].HookCount)
	}
}

// WithHooks accumulates across multiple options.
func TestWithHooksAccumulate(t *testing.T) {
	s := &app.Services{}
	if err := app.Register(s, &concreteThing{}, app.WithHooks(&eventAware{}), app.WithHooks(&shutdownAware{})); err != nil {
		t.Fatalf("Register: %v", err)
	}
	infos := app.ListComponents(s)
	if infos[0].HookCount != 2 {
		t.Fatalf("HookCount = %d, want 2", infos[0].HookCount)
	}
	if !infos[0].EventAware || !infos[0].ShutdownAware {
		t.Fatalf("facets = %+v", infos[0])
	}
}

// --- RangeComponents snapshot semantics ---

func TestRangeComponentsSnapshot(t *testing.T) {
	s := &app.Services{}
	if err := app.RegisterFor[*concreteThing, primaryFoo](s, &concreteThing{name: "1"}); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Register during iteration must not deadlock, and the new entry must be
	// invisible to the in-flight iteration.
	var seen int
	s.RangeComponents(func(key app.ComponentKey, v any, hooks []any) bool {
		seen++
		if seen == 1 {
			if err := app.RegisterFor[*concreteThing, analyticsFoo](s, &concreteThing{name: "2"}); err != nil {
				t.Errorf("Register during Range: %v", err)
			}
		}
		return true
	})
	if seen != 1 {
		t.Fatalf("iteration saw %d entries, want 1 (snapshot)", seen)
	}

	// A fresh Range now sees both.
	seen = 0
	s.RangeComponents(func(key app.ComponentKey, v any, hooks []any) bool {
		seen++
		return true
	})
	if seen != 2 {
		t.Fatalf("second iteration saw %d, want 2", seen)
	}
}

// Returning false halts iteration.
func TestRangeComponentsHalt(t *testing.T) {
	s := &app.Services{}
	for _, q := range []func() error{
		func() error { return app.RegisterFor[*concreteThing, primaryFoo](s, &concreteThing{}) },
		func() error { return app.RegisterFor[*concreteThing, analyticsFoo](s, &concreteThing{}) },
	} {
		if err := q(); err != nil {
			t.Fatalf("Register: %v", err)
		}
	}
	var seen int
	s.RangeComponents(func(key app.ComponentKey, v any, hooks []any) bool {
		seen++
		return false
	})
	if seen != 1 {
		t.Fatalf("halt: saw %d, want 1", seen)
	}
}

// --- ListComponents order ---

func TestListComponentsOrder(t *testing.T) {
	s := &app.Services{}
	type a struct{}
	type b struct{}
	type c struct{}
	if err := app.Register(s, &a{}); err != nil {
		t.Fatal(err)
	}
	if err := app.Register(s, &b{}); err != nil {
		t.Fatal(err)
	}
	if err := app.Register(s, &c{}); err != nil {
		t.Fatal(err)
	}
	infos := app.ListComponents(s)
	want := []reflect.Type{reflect.TypeFor[*a](), reflect.TypeFor[*b](), reflect.TypeFor[*c]()}
	if len(infos) != len(want) {
		t.Fatalf("len = %d, want %d", len(infos), len(want))
	}
	for i, w := range want {
		if infos[i].Key.Type != w {
			t.Fatalf("order[%d] = %s, want %s", i, infos[i].Key.Type, w)
		}
	}
}

// --- concurrency under -race ---

func TestConcurrentRegisterGetRange(t *testing.T) {
	s := &app.Services{}
	const n = 50
	var wg sync.WaitGroup

	// distinct qualifier per goroutine via index-keyed marker is not possible
	// with types, so register distinct concrete types is also not possible at
	// runtime; instead spread writes across two qualifiers and tolerate
	// duplicate-key errors as a valid concurrent outcome.
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				_ = app.RegisterFor[*concreteThing, primaryFoo](s, &concreteThing{})
			} else {
				_ = app.RegisterFor[*concreteThing, analyticsFoo](s, &concreteThing{})
			}
		}(i)
	}

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, _ = app.GetFor[*concreteThing, primaryFoo](s)
		}()
	}

	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			s.RangeComponents(func(key app.ComponentKey, v any, hooks []any) bool { return true })
			_ = app.ListComponents(s)
		}()
	}

	wg.Wait()
}
