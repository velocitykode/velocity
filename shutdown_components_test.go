package velocity

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/contract"
)

// Marker qualifier types let the same concrete type be registered more than
// once in the component registry (distinct ComponentKey per marker).
type (
	markerA struct{}
	markerB struct{}
)

// shutdownComp is a registry component implementing contract.ShutdownAware. It
// counts calls and records its label into a shared order slice so tests can
// assert reverse-registration teardown order.
type shutdownComp struct {
	label     string
	order     *[]string
	shutdowns atomic.Int32
}

func (c *shutdownComp) Shutdown(_ context.Context) error {
	c.shutdowns.Add(1)
	if c.order != nil {
		*c.order = append(*c.order, c.label)
	}
	return nil
}

var _ contract.ShutdownAware = (*shutdownComp)(nil)

// closableValue is a velocity-unaware third-party value: it exposes a
// Close() error, not the framework's Shutdown(ctx) signature.
type closableValue struct {
	closed   atomic.Int32
	closeErr error
}

func (v *closableValue) Close() error {
	v.closed.Add(1)
	return v.closeErr
}

// closeHook adapts a closableValue to contract.ShutdownAware, capturing the
// ctx it was handed so the test can prove the sweep forwards it.
type closeHook struct {
	value  *closableValue
	gotCtx context.Context
}

func (h *closeHook) Shutdown(ctx context.Context) error {
	h.gotCtx = ctx
	return h.value.Close()
}

var _ contract.ShutdownAware = (*closeHook)(nil)

// panicComp is a misbehaving third-party component whose Shutdown panics.
type panicComp struct {
	shutdowns atomic.Int32
}

func (c *panicComp) Shutdown(_ context.Context) error {
	c.shutdowns.Add(1)
	panic("component shutdown boom")
}

var _ contract.ShutdownAware = (*panicComp)(nil)

// componentRegisteringShutdownProvider registers a ShutdownAware component
// during Register, mimicking an SDK module that binds a closable value.
type componentRegisteringShutdownProvider struct {
	comp *shutdownComp
}

func (p *componentRegisteringShutdownProvider) Register(s *app.Services) error {
	return app.Register(s, p.comp)
}
func (p *componentRegisteringShutdownProvider) Boot(_ *app.Services) error       { return nil }
func (p *componentRegisteringShutdownProvider) Shutdown(_ context.Context) error { return nil }

// (1) Two ShutdownAware components registered A then B are shut down B then A.
func TestShutdown_Components_ReverseRegistrationOrder(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}

	var order []string
	compA := &shutdownComp{label: "A", order: &order}
	compB := &shutdownComp{label: "B", order: &order}
	if err := app.RegisterFor[*shutdownComp, markerA](a.Services, compA); err != nil {
		t.Fatalf("register A: %v", err)
	}
	if err := app.RegisterFor[*shutdownComp, markerB](a.Services, compB); err != nil {
		t.Fatalf("register B: %v", err)
	}

	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	want := []string{"B", "A"}
	if len(order) != len(want) || order[0] != want[0] || order[1] != want[1] {
		t.Fatalf("teardown order %v, want %v (reverse registration order)", order, want)
	}
}

// (2) Component shutdown happens AFTER provider Shutdown and BEFORE the queue
// driver closes.
func TestShutdown_Components_BetweenProvidersAndQueue(t *testing.T) {
	var order []string
	a, err := NewTestApp(WithProviders(&orderRecordingProvider{order: &order}))
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}

	comp := &shutdownComp{label: "component", order: &order}
	if err := app.Register(a.Services, comp); err != nil {
		t.Fatalf("register component: %v", err)
	}

	orig := a.Services.Queue
	t.Cleanup(func() {
		if orig != nil {
			_ = orig.Shutdown(context.Background())
		}
	})
	a.Services.Queue = &orderRecordingQueue{order: &order}

	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	want := []string{"provider", "component", "queue"}
	if len(order) != len(want) {
		t.Fatalf("teardown order %v, want %v", order, want)
	}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("teardown order %v, want %v (components sweep after providers, before queue)", order, want)
		}
	}
}

type ctxKey struct{}

// (3) A hook adapter bridging a Close()-only value runs with the passed ctx
// and its error is aggregated, not dropped.
func TestShutdown_Components_HookCtxAndErrorAggregated(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}

	closeErr := errors.New("close failed")
	value := &closableValue{closeErr: closeErr}
	hook := &closeHook{value: value}
	if err := app.Register(a.Services, value, app.WithHooks(hook)); err != nil {
		t.Fatalf("register value: %v", err)
	}

	ctx := context.WithValue(context.Background(), ctxKey{}, "sentinel")
	shutdownErr := a.Shutdown(ctx)

	if got := value.closed.Load(); got != 1 {
		t.Errorf("value.Close called %d times via hook, want 1", got)
	}
	if hook.gotCtx == nil || hook.gotCtx.Value(ctxKey{}) != "sentinel" {
		t.Errorf("hook Shutdown did not receive the ctx passed to App.Shutdown: %v", hook.gotCtx)
	}
	if !errors.Is(shutdownErr, closeErr) {
		t.Errorf("hook Close error not aggregated into Shutdown result: %v", shutdownErr)
	}
}

// (4) The same instance registered under two keys is shut down once.
func TestShutdown_Components_SameInstanceTwoKeysOnce(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}

	comp := &shutdownComp{label: "shared"}
	if err := app.Register(a.Services, comp); err != nil {
		t.Fatalf("register default: %v", err)
	}
	if err := app.RegisterFor[*shutdownComp, markerB](a.Services, comp); err != nil {
		t.Fatalf("register markerB: %v", err)
	}

	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if got := comp.shutdowns.Load(); got != 1 {
		t.Errorf("shared instance Shutdown called %d times, want 1 (exactly-once across keys)", got)
	}
}

// (5) A panicking component Shutdown is converted to an error and the
// remaining components still shut down.
func TestShutdown_Components_PanicConvertedRemainingRun(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}

	good1 := &shutdownComp{label: "good1"}
	panicker := &panicComp{}
	good2 := &shutdownComp{label: "good2"}

	// Registration order good1, panicker, good2 -> reverse teardown is
	// good2, panicker (panics), good1. good1 running proves the panic did
	// not abort the sweep.
	if err := app.RegisterFor[*shutdownComp, markerA](a.Services, good1); err != nil {
		t.Fatalf("register good1: %v", err)
	}
	if err := app.Register(a.Services, panicker); err != nil {
		t.Fatalf("register panicker: %v", err)
	}
	if err := app.RegisterFor[*shutdownComp, markerB](a.Services, good2); err != nil {
		t.Fatalf("register good2: %v", err)
	}

	shutdownErr := a.Shutdown(context.Background())
	if shutdownErr == nil {
		t.Fatal("expected Shutdown to aggregate the converted panic error")
	}
	if !strings.Contains(shutdownErr.Error(), "boom") {
		t.Errorf("Shutdown error %q does not carry the converted panic", shutdownErr)
	}
	if got := panicker.shutdowns.Load(); got != 1 {
		t.Errorf("panicker Shutdown called %d times, want 1", got)
	}
	if got := good1.shutdowns.Load(); got != 1 {
		t.Errorf("good1 Shutdown called %d times, want 1 (panic must not abort the sweep)", got)
	}
	if got := good2.shutdowns.Load(); got != 1 {
		t.Errorf("good2 Shutdown called %d times, want 1", got)
	}
}

// (6) A New() failure after the provider phase unwinds registered components:
// a value an earlier provider registered is torn down on the failure path.
func TestNew_ProviderFailure_UnwindsComponents(t *testing.T) {
	comp := &shutdownComp{label: "registered-before-failure"}
	regProvider := &componentRegisteringShutdownProvider{comp: comp}
	failsRegister := &shutdownRecorder{registerErr: errors.New("register kaboom")}

	_, err := NewTestApp(WithProviders(regProvider, failsRegister))
	if err == nil {
		t.Fatal("expected register failure to propagate from New()")
	}
	if !errors.Is(err, failsRegister.registerErr) {
		t.Fatalf("expected wrapped register error, got: %v", err)
	}

	if got := comp.shutdowns.Load(); got != 1 {
		t.Errorf("registered component Shutdown called %d times on New failure, want 1", got)
	}
}
