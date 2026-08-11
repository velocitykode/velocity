package velocity

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/events"
)

// dispatcherProbe records the dispatcher handed to SetEventDispatcher.
// Embedded by the CSRF and extension probes below.
type dispatcherProbe struct {
	mu         sync.Mutex
	dispatcher func(ctx context.Context, event any) error
}

func (p *dispatcherProbe) SetEventDispatcher(fn func(ctx context.Context, event any) error) {
	p.mu.Lock()
	p.dispatcher = fn
	p.mu.Unlock()
}

func (p *dispatcherProbe) getDispatcher() func(ctx context.Context, event any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dispatcher
}

// csrfProbe satisfies contract.CSRFProtector plus EventDispatcherAware so it
// can stand in for a.Services.CSRF in the wireInstanceEvents candidate sweep.
type csrfProbe struct {
	dispatcherProbe
}

func (p *csrfProbe) Middleware(next http.Handler) http.Handler { return next }

// assertProbeDispatches verifies the probe holds a dispatcher and that an
// event sent through it lands in the fake dispatcher.
func assertProbeDispatches(t *testing.T, probe *dispatcherProbe, fake *events.FakeDispatcher) {
	t.Helper()
	dispatch := probe.getDispatcher()
	if dispatch == nil {
		t.Fatal("extension probe never received an event dispatcher")
	}
	if err := dispatch(context.Background(), testEvent{name: "probe.event"}); err != nil {
		t.Fatalf("dispatch through probe: %v", err)
	}
	for _, ev := range fake.GetDispatchedEvents() {
		if te, ok := ev.(testEvent); ok && te.name == "probe.event" {
			return
		}
	}
	t.Fatalf("probe event not recorded by fake dispatcher; got %v", fake.GetDispatchedEvents())
}

// componentProbe is a first-party SDK stand-in: a value that implements
// contract.EventDispatcherAware directly (via the embedded dispatcherProbe) and
// self-registers in the type-keyed component registry.
type componentProbe struct {
	dispatcherProbe
}

// plainValue is a velocity-unaware third-party value: it implements none of the
// framework contract interfaces. It can only be wired through a hook adapter.
type plainValue struct {
	name string
}

// componentRegisteringModule registers a dispatcher-aware value in the
// component registry during Register, mimicking a first-party SDK module.
type componentRegisteringModule struct {
	probe *componentProbe
}

func (p *componentRegisteringModule) Init(s *app.Services) error {
	return app.Register(s, p.probe)
}

func (p *componentRegisteringModule) Start(_ *app.Services) error      { return nil }
func (p *componentRegisteringModule) Shutdown(_ context.Context) error { return nil }

// hookedValueModule registers a velocity-unaware value alongside a
// dispatcher-aware hook adapter, the WithHooks bridging pattern.
type hookedValueModule struct {
	value   *plainValue
	adapter *dispatcherProbe
}

func (p *hookedValueModule) Init(s *app.Services) error {
	return app.Register(s, p.value, app.WithHooks(p.adapter))
}

func (p *hookedValueModule) Start(_ *app.Services) error      { return nil }
func (p *hookedValueModule) Shutdown(_ context.Context) error { return nil }

// (a) A first-party component implementing EventDispatcherAware, registered via
// a WithModules module's Register, receives the dispatcher after New.
func TestNew_WithModules_ComponentReceivesDispatcher(t *testing.T) {
	fake := events.NewFakeDispatcher()
	probe := &componentProbe{}

	a, err := NewTestApp(
		WithFakeEvents(fake),
		WithModules(&componentRegisteringModule{probe: probe}),
	)
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	assertProbeDispatches(t, &probe.dispatcherProbe, fake)
}

// (b) A velocity-unaware value registered with a dispatcher-aware hook adapter:
// the adapter receives the dispatcher, the raw value is untouched and returned
// verbatim by Get.
func TestNew_WithModules_HookAdapterReceivesDispatcher(t *testing.T) {
	fake := events.NewFakeDispatcher()
	value := &plainValue{name: "third-party"}
	adapter := &dispatcherProbe{}

	a, err := NewTestApp(
		WithFakeEvents(fake),
		WithModules(&hookedValueModule{value: value, adapter: adapter}),
	)
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	// The adapter is wired and dispatches through to the app dispatcher.
	assertProbeDispatches(t, adapter, fake)

	// Get returns the RAW registered value, never the hook, and the value is
	// the exact pointer registered (untouched by the sweep).
	got, err := app.Get[*plainValue](a.Services)
	if err != nil {
		t.Fatalf("Get[*plainValue]: %v", err)
	}
	if got != value {
		t.Fatalf("Get returned %p, want raw registered value %p", got, value)
	}
}

// (c) A component registered by a chain module during bootstrap() is wired by
// the bootstrap.go re-sweep that runs after the chain module lifecycle.
func TestBootstrap_ChainModuleComponentReceivesDispatcher(t *testing.T) {
	fake := events.NewFakeDispatcher()
	probe := &componentProbe{}

	a, err := NewTestApp(WithFakeEvents(fake))
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	a.Modules(func(r *chain.ModuleRegistry) {
		r.Add(&componentRegisteringModule{probe: probe})
	})

	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap() error: %v", err)
	}

	assertProbeDispatches(t, &probe.dispatcherProbe, fake)
}

// (d) Re-running wireInstanceEvents is safe: the SetEventDispatcher setter is
// simply called again (no value == hook dedupe), with no panic and no race
// under -race. Registering the same probe as both value and hook exercises the
// double-call path the wiring comment documents as safe.
func TestWireInstanceEvents_ComponentRewireIdempotent(t *testing.T) {
	fake := events.NewFakeDispatcher()
	a, err := NewTestApp(WithFakeEvents(fake))
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	probe := &componentProbe{}
	// The probe is both the registered value AND a hook, so each sweep calls
	// its setter twice. This is exactly the case the wiring code refuses to
	// dedupe (a value == hook comparison would panic for an uncomparable
	// dynamic type), relying instead on SetEventDispatcher being synchronized;
	// the double call must be harmless.
	if err := app.Register(a.Services, probe, app.WithHooks(probe)); err != nil {
		t.Fatalf("Register: %v", err)
	}

	wireInstanceEvents(a)
	wireInstanceEvents(a)

	assertProbeDispatches(t, &probe.dispatcherProbe, fake)
}

// B13 regression: CSRF was missing from the wireInstanceEvents candidate
// slice, so csrf-fired events (e.g. session fallback) were never dispatched.
func TestWireInstanceEvents_CSRFReceivesDispatcher(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	probe := &csrfProbe{}
	a.Services.CSRF = probe

	wireInstanceEvents(a)

	if probe.getDispatcher() == nil {
		t.Fatal("CSRF protector never received an event dispatcher")
	}
}

// Under WithoutEvents the dispatcher is nil; the bootstrap-time component
// sweep must no-op cleanly instead of wiring a nil closure. Ported from the
// removed string-extension SweepNoOps test (the B12 WithModules and chain
// paths it shared a helper with are already covered by the Component
// equivalents above).
func TestBootstrap_WithoutEvents_ComponentSweepNoOps(t *testing.T) {
	probe := &componentProbe{}

	a, err := NewTestApp(WithoutEvents())
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	a.Modules(func(r *chain.ModuleRegistry) {
		r.Add(&componentRegisteringModule{probe: probe})
	})

	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap() error: %v", err)
	}

	if probe.getDispatcher() != nil {
		t.Fatal("probe received a dispatcher despite WithoutEvents")
	}
}
