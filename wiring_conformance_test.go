package velocity

// Event-wiring conformance tests. Event wiring has regressed several
// independent ways (B3 bus never autowired, B12 provider-registered
// extensions swept against an empty map, B13 CSRF missing from the
// candidate slice, B47 dispatcher wiring drift); these tests pin the
// contract so the next drift fails CI instead of silently dropping events:
//
//   - Part A sweeps app.Services by reflection and requires every
//     dispatcher-aware field to appear in eventWiringCandidates.
//   - Part B pins the extension wiring path end to end.
//   - Part C pins the bus autowire path end to end (B3+B12 jointly).

import (
	"context"
	"reflect"
	"testing"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/bus"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/events"
)

// The probes below stand in for Services fields that are nil under the test
// config so the Part A sweep exercises every candidate slot. Each embeds its
// contract interface for method-set satisfaction and defines
// SetEventDispatcher at depth 0: contract.Database and contract.Notifier
// declare SetEventDispatcher themselves, so promoting it from an embedded
// dispatcherProbe would be ambiguous and the probe would satisfy neither
// interface.

type dbWiringProbe struct {
	contract.Database
	probe dispatcherProbe
}

func (p *dbWiringProbe) SetEventDispatcher(fn func(ctx context.Context, event any) error) {
	p.probe.SetEventDispatcher(fn)
}

type viewWiringProbe struct {
	contract.ViewEngine
	probe dispatcherProbe
}

func (p *viewWiringProbe) SetEventDispatcher(fn func(ctx context.Context, event any) error) {
	p.probe.SetEventDispatcher(fn)
}

type authWiringProbe struct {
	contract.AuthManager
	probe dispatcherProbe
}

func (p *authWiringProbe) SetEventDispatcher(fn func(ctx context.Context, event any) error) {
	p.probe.SetEventDispatcher(fn)
}

type cryptoWiringProbe struct {
	contract.Encryptor
	probe dispatcherProbe
}

func (p *cryptoWiringProbe) SetEventDispatcher(fn func(ctx context.Context, event any) error) {
	p.probe.SetEventDispatcher(fn)
}

type notifierWiringProbe struct {
	contract.Notifier
	probe dispatcherProbe
}

func (p *notifierWiringProbe) SetEventDispatcher(fn func(ctx context.Context, event any) error) {
	p.probe.SetEventDispatcher(fn)
}

// Part A: every exported app.Services field whose value implements
// contract.EventDispatcherAware must be offered the dispatcher by
// wireInstanceEvents, i.e. appear (pointer-identical) in
// eventWiringCandidates. A new Services field that fires events but is
// missing from the candidate slice fails here by name. The sweep gates on
// the static field type, not just the runtime value: any interface field
// left nil after probe assignment fails outright, so a new field cannot
// dodge the check by being nil under the test config.
func TestConformance_ServicesEventWiringCandidates(t *testing.T) {
	fake := events.NewFakeDispatcher()
	a, err := NewTestApp(WithFakeEvents(fake))
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	// Fill the slots the test config leaves nil with dispatcher-aware
	// probes, and restore the originals before the deferred Shutdown runs:
	// the probes' embedded contract interfaces are nil, so any lifecycle
	// call against them (e.g. Database.Shutdown) would panic.
	type slot struct {
		get func() any
		set func(any)
	}
	slots := []slot{
		{func() any { return a.Services.DB }, func(v any) {
			if v == nil {
				a.Services.DB = nil
			} else {
				a.Services.DB = v.(contract.Database)
			}
		}},
		{func() any { return a.Services.View }, func(v any) {
			if v == nil {
				a.Services.View = nil
			} else {
				a.Services.View = v.(contract.ViewEngine)
			}
		}},
		{func() any { return a.Services.CSRF }, func(v any) {
			if v == nil {
				a.Services.CSRF = nil
			} else {
				a.Services.CSRF = v.(contract.CSRFProtector)
			}
		}},
		{func() any { return a.Services.Auth }, func(v any) {
			if v == nil {
				a.Services.Auth = nil
			} else {
				a.Services.Auth = v.(contract.AuthManager)
			}
		}},
		{func() any { return a.Services.Crypto }, func(v any) {
			if v == nil {
				a.Services.Crypto = nil
			} else {
				a.Services.Crypto = v.(contract.Encryptor)
			}
		}},
		{func() any { return a.Services.Notification }, func(v any) {
			if v == nil {
				a.Services.Notification = nil
			} else {
				a.Services.Notification = v.(contract.Notifier)
			}
		}},
	}
	probes := []any{
		&dbWiringProbe{}, &viewWiringProbe{}, &csrfProbe{},
		&authWiringProbe{}, &cryptoWiringProbe{}, &notifierWiringProbe{},
	}
	originals := make([]any, len(slots))
	for i, s := range slots {
		originals[i] = s.get()
		if originals[i] == nil {
			s.set(probes[i])
		}
	}
	defer func() {
		for i, s := range slots {
			s.set(originals[i])
		}
	}()

	// Candidates must be collected AFTER probe assignment so the slice
	// reflects the probed field values.
	candidates := eventWiringCandidates(a)
	for i, c := range candidates {
		if c == nil {
			t.Fatalf("eventWiringCandidates()[%d] is nil even after probe assignment; the sweep cannot exercise that slot - extend the probe set", i)
		}
	}
	inCandidates := func(v any) bool {
		for _, c := range candidates {
			if c == v {
				return true
			}
		}
		return false
	}

	awareType := reflect.TypeOf((*contract.EventDispatcherAware)(nil)).Elem()
	sv := reflect.ValueOf(a.Services).Elem()
	st := sv.Type()
	for i := 0; i < st.NumField(); i++ {
		field := st.Field(i)
		// Unexported fields (extMu) can never hold wireable services.
		if !field.IsExported() {
			continue
		}
		// Services.Events IS the dispatcher: it is the wiring source,
		// never a wiring target.
		if field.Name == "Events" {
			continue
		}
		// A field whose static type is neither an interface nor an
		// implementation of the contract (the Extensions map) can never
		// hold a dispatcher-aware value. Every interface field stays in
		// scope regardless of its declared method set, because a concrete
		// implementation may opt into the contract at runtime.
		if field.Type.Kind() != reflect.Interface && !field.Type.Implements(awareType) {
			continue
		}
		fv := sv.Field(i)
		if fv.Kind() == reflect.Interface && fv.IsNil() {
			// Silently skipping a nil field would leave its wiring
			// unverified, so a future Services field that is nil under
			// NewTestApp could miss eventWiringCandidates without
			// failing here. Force the probe set to keep up with the
			// struct instead.
			t.Errorf("app.Services.%s (%s) is still nil after probe assignment, so the sweep cannot verify its event wiring; add a dispatcher-aware probe for it in the slot list above", field.Name, field.Type)
			continue
		}
		val := fv.Interface()
		if _, ok := val.(contract.EventDispatcherAware); !ok {
			continue
		}
		// The Router is wired directly in wireInstanceEvents because it
		// lives on App, not Services; RedirectAllowlist aliases the same
		// router instance, so exclude by pointer identity rather than by
		// field name.
		if val == any(a.Router) {
			continue
		}
		if !inCandidates(val) {
			t.Errorf("app.Services.%s (%T) implements contract.EventDispatcherAware but is missing from eventWiringCandidates in bootstrap.go; add it so wireInstanceEvents offers it the dispatcher", field.Name, val)
		}
	}
}

// conformanceExtensionProvider registers a dispatcher-aware extension during
// Register, the way a third-party package opts into framework events.
type conformanceExtensionProvider struct {
	probe *dispatcherProbe
}

func (p *conformanceExtensionProvider) Register(s *app.Services) error {
	return app.RegisterExtension(s, "conformance-event-probe", p.probe)
}

func (p *conformanceExtensionProvider) Boot(_ *app.Services) error       { return nil }
func (p *conformanceExtensionProvider) Shutdown(_ context.Context) error { return nil }

// Part B: canonical pin of the extension wiring contract (B12). A
// dispatcher-aware extension registered by a provider must receive the
// dispatcher, and a dispatch through it must land in the app dispatcher.
// Overlaps the B12 regression tests in extension_event_wiring_test.go by
// design; this is the contract pin, those are the bug regressions.
func TestConformance_ExtensionReceivesDispatcher(t *testing.T) {
	fake := events.NewFakeDispatcher()
	probe := &dispatcherProbe{}

	a, err := NewTestApp(
		WithFakeEvents(fake),
		WithProviders(&conformanceExtensionProvider{probe: probe}),
	)
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	assertProbeDispatches(t, probe, fake)
}

// busRegisteringProvider registers an application bus under the
// conventional "bus" extension key.
type busRegisteringProvider struct {
	bus *bus.Bus
}

func (p *busRegisteringProvider) Register(s *app.Services) error {
	return app.RegisterExtension(s, "bus", p.bus)
}

func (p *busRegisteringProvider) Boot(_ *app.Services) error       { return nil }
func (p *busRegisteringProvider) Shutdown(_ context.Context) error { return nil }

type conformanceCommand struct {
	ID int
}

// Part C: bus end to end (pins B3+B12 jointly). A bus registered as the
// "bus" extension must be autowired into the app dispatcher, so command
// lifecycle events from Dispatch reach it without any manual
// SetEventDispatcher call.
func TestConformance_BusCommandEventsReachDispatcher(t *testing.T) {
	fake := events.NewFakeDispatcher()
	b := bus.New()

	a, err := NewTestApp(
		WithFakeEvents(fake),
		WithProviders(&busRegisteringProvider{bus: b}),
	)
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	bus.Register(b, func(cmd conformanceCommand) error { return nil })
	if err := b.Dispatch(conformanceCommand{ID: 1}); err != nil {
		t.Fatalf("bus.Dispatch() error: %v", err)
	}

	if err := fake.AssertDispatched(&bus.CommandDispatching{}, nil); err != nil {
		t.Errorf("bus.CommandDispatching never reached the app dispatcher: %v", err)
	}
	if err := fake.AssertDispatched(&bus.CommandCompleted{}, nil); err != nil {
		t.Errorf("bus.CommandCompleted never reached the app dispatcher: %v", err)
	}
}
