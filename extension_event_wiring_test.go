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

// extensionRegisteringProvider registers a dispatcher-aware extension during
// Register, mimicking a third-party package that wants framework events.
type extensionRegisteringProvider struct {
	probe *dispatcherProbe
}

func (p *extensionRegisteringProvider) Register(s *app.Services) error {
	return app.RegisterExtension(s, "ext-event-probe", p.probe)
}

func (p *extensionRegisteringProvider) Boot(_ *app.Services) error       { return nil }
func (p *extensionRegisteringProvider) Shutdown(_ context.Context) error { return nil }

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

// B12 regression (WithProviders path): wireInstanceEvents runs in New()
// before the provider lifecycle, so extensions registered by providers were
// swept against an empty map and never got the dispatcher.
func TestNew_WithProviders_ExtensionReceivesDispatcher(t *testing.T) {
	fake := events.NewFakeDispatcher()
	probe := &dispatcherProbe{}

	a, err := NewTestApp(
		WithFakeEvents(fake),
		WithProviders(&extensionRegisteringProvider{probe: probe}),
	)
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	assertProbeDispatches(t, probe, fake)
}

// B12 regression (chain path): extensions registered by chain providers
// during Bootstrap() must also be wired after their lifecycle completes.
func TestBootstrap_ChainProviderExtensionReceivesDispatcher(t *testing.T) {
	fake := events.NewFakeDispatcher()
	probe := &dispatcherProbe{}

	a, err := NewTestApp(WithFakeEvents(fake))
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	a.Providers(func(r *chain.ProviderRegistry) {
		r.Add(&extensionRegisteringProvider{probe: probe})
	})

	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap() error: %v", err)
	}

	assertProbeDispatches(t, probe, fake)
}

// Under WithoutEvents the dispatcher is nil; the bootstrap-time extension
// sweep must no-op cleanly instead of wiring a nil closure.
func TestBootstrap_WithoutEvents_ExtensionSweepNoOps(t *testing.T) {
	probe := &dispatcherProbe{}

	a, err := NewTestApp(WithoutEvents())
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	a.Providers(func(r *chain.ProviderRegistry) {
		r.Add(&extensionRegisteringProvider{probe: probe})
	})

	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap() error: %v", err)
	}

	if probe.getDispatcher() != nil {
		t.Fatal("probe received a dispatcher despite WithoutEvents")
	}
}
