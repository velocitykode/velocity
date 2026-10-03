package velocity

// Event-wiring conformance tests. Event wiring has regressed several
// independent ways (B3 bus never autowired, B12 module-registered
// components swept against an empty registry, B13 CSRF missing from the
// candidate slice, B47 dispatcher wiring drift); these tests pin the
// contract so the next drift fails CI instead of silently dropping events:
//
//   - Part A sweeps app.Services by reflection and requires every
//     dispatcher-aware field to appear in eventWiringCandidates.
//   - Part C pins the bus autowire path end to end (B3+B12 jointly).
//   - Part D pins the component wiring path end to end.

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"go.uber.org/goleak"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/bus"
	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/internal/teardown"
	"github.com/velocitykode/velocity/scheduler"
	"github.com/velocitykode/velocity/view"
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
		// Unexported fields (compMu, the registry maps) can never hold
		// wireable services reachable through eventWiringCandidates.
		if !field.IsExported() {
			continue
		}
		// Services.Events IS the dispatcher: it is the wiring source,
		// never a wiring target.
		if field.Name == "Events" {
			continue
		}
		// A field whose static type is neither an interface nor an
		// implementation of the contract (e.g. the CookiePolicy
		// struct) can never hold a dispatcher-aware value. Every interface
		// field stays in scope regardless of its declared method set,
		// because a concrete implementation may opt into the contract at
		// runtime.
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

// conformanceComponentModule registers a dispatcher-aware value in the
// type-keyed component registry during Register, the way a first-party SDK
// module opts into framework events.
type conformanceComponentModule struct {
	probe *componentProbe
}

func (p *conformanceComponentModule) Init(s *app.Services) error {
	return app.Register(s, p.probe)
}

func (p *conformanceComponentModule) Start(_ *app.Services) error      { return nil }
func (p *conformanceComponentModule) Shutdown(_ context.Context) error { return nil }

// Part D: canonical pin of the component wiring contract. A dispatcher-aware
// value registered in the type-keyed registry by a module must receive the
// dispatcher, and a dispatch through it must land in the app dispatcher. This
// guards future SDKs that self-register via app.Register against a wiring
// regression in wireComponentEvents.
func TestConformance_ComponentReceivesDispatcher(t *testing.T) {
	fake := events.NewFakeDispatcher()
	probe := &componentProbe{}

	a, err := NewTestApp(
		WithFakeEvents(fake),
		WithModules(&conformanceComponentModule{probe: probe}),
	)
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	assertProbeDispatches(t, &probe.dispatcherProbe, fake)
}

// busRegisteringModule registers an application bus in the type-keyed
// component registry via the typed app.Register API.
type busRegisteringModule struct {
	bus *bus.Bus
}

func (p *busRegisteringModule) Init(s *app.Services) error {
	return app.Register[*bus.Bus](s, p.bus)
}

func (p *busRegisteringModule) Start(_ *app.Services) error      { return nil }
func (p *busRegisteringModule) Shutdown(_ context.Context) error { return nil }

type conformanceCommand struct {
	ID int
}

// Part C: bus end to end (pins B3+B12 jointly). A bus registered in the
// type-keyed component registry must be autowired into the app dispatcher,
// so command lifecycle events from Dispatch reach it without any manual
// SetEventDispatcher call.
func TestConformance_BusCommandEventsReachDispatcher(t *testing.T) {
	fake := events.NewFakeDispatcher()
	b := bus.New()

	a, err := NewTestApp(
		WithFakeEvents(fake),
		WithModules(&busRegisteringModule{bus: b}),
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

// Part E: every owned Services field follows a replacement. A module that
// replaces a field has the framework's consumers re-bound to the
// replacement and the instance it displaced shut down once; the instance
// a field holds at Shutdown is closed once.

// retireProbe counts the Shutdown calls of one replacement and exposes the
// instance it decorates through Unwrap, so the decorated instance is kept.
// The final replacement forwards its Shutdown to the decorated instance,
// which it owns.
type retireProbe struct {
	inner   any
	closes  atomic.Int32
	forward bool
}

func (p *retireProbe) shutdown(ctx context.Context) error {
	p.closes.Add(1)
	if p.forward {
		return teardown.Close(ctx, p.inner)
	}
	return nil
}

type logProbe struct {
	contract.Logger
	rp *retireProbe
}

func (p *logProbe) Shutdown(ctx context.Context) error { return p.rp.shutdown(ctx) }
func (p *logProbe) Unwrap() contract.Logger            { return p.Logger }

type errorsProbe struct {
	contract.ErrorHandler
	rp *retireProbe
}

func (p *errorsProbe) Shutdown(ctx context.Context) error { return p.rp.shutdown(ctx) }
func (p *errorsProbe) Unwrap() contract.ErrorHandler      { return p.ErrorHandler }

type cryptoProbe struct {
	contract.Encryptor
	rp *retireProbe
}

func (p *cryptoProbe) Shutdown(ctx context.Context) error { return p.rp.shutdown(ctx) }
func (p *cryptoProbe) Unwrap() contract.Encryptor         { return p.Encryptor }

type dbProbe struct {
	contract.Database
	rp *retireProbe
}

func (p *dbProbe) Shutdown(ctx context.Context) error { return p.rp.shutdown(ctx) }
func (p *dbProbe) Unwrap() contract.Database          { return p.Database }
func (p *dbProbe) SetEventDispatcher(fn func(ctx context.Context, event any) error) {
	p.Database.SetEventDispatcher(fn)
}

type authProbe struct {
	contract.AuthManager
	rp *retireProbe
}

func (p *authProbe) Shutdown(ctx context.Context) error { return p.rp.shutdown(ctx) }
func (p *authProbe) Unwrap() contract.AuthManager       { return p.AuthManager }

type csrfSwapProbe struct {
	contract.CSRFProtector
	rp *retireProbe
}

func (p *csrfSwapProbe) Shutdown(ctx context.Context) error { return p.rp.shutdown(ctx) }
func (p *csrfSwapProbe) Unwrap() contract.CSRFProtector     { return p.CSRFProtector }

type viewProbe struct {
	contract.ViewEngine
	rp *retireProbe
}

func (p *viewProbe) Shutdown(ctx context.Context) error { return p.rp.shutdown(ctx) }
func (p *viewProbe) Unwrap() contract.ViewEngine        { return p.ViewEngine }

type cacheProbe struct {
	contract.CacheManager
	rp *retireProbe
}

func (p *cacheProbe) Shutdown(ctx context.Context) error { return p.rp.shutdown(ctx) }
func (p *cacheProbe) Unwrap() contract.CacheManager      { return p.CacheManager }

type eventsProbe struct {
	contract.Dispatcher
	rp *retireProbe
}

func (p *eventsProbe) Shutdown(ctx context.Context) error { return p.rp.shutdown(ctx) }
func (p *eventsProbe) Unwrap() contract.Dispatcher        { return p.Dispatcher }

type queueProbe struct {
	contract.QueueDriver
	rp *retireProbe
}

func (p *queueProbe) Shutdown(ctx context.Context) error { return p.rp.shutdown(ctx) }
func (p *queueProbe) Unwrap() contract.QueueDriver       { return p.QueueDriver }

type storageProbe struct {
	contract.StorageManager
	rp *retireProbe
}

func (p *storageProbe) Shutdown(ctx context.Context) error { return p.rp.shutdown(ctx) }
func (p *storageProbe) Unwrap() contract.StorageManager    { return p.StorageManager }

type schedulerProbe struct {
	scheduler.TaskScheduler
	rp *retireProbe
}

func (p *schedulerProbe) Shutdown(ctx context.Context) error { return p.rp.shutdown(ctx) }
func (p *schedulerProbe) Unwrap() scheduler.TaskScheduler    { return p.TaskScheduler }

type mailProbe struct {
	contract.Mailer
	rp *retireProbe
}

func (p *mailProbe) Shutdown(ctx context.Context) error { return p.rp.shutdown(ctx) }
func (p *mailProbe) Unwrap() contract.Mailer            { return p.Mailer }

type notifierProbe struct {
	contract.Notifier
	rp *retireProbe
}

func (p *notifierProbe) Shutdown(ctx context.Context) error { return p.rp.shutdown(ctx) }
func (p *notifierProbe) Unwrap() contract.Notifier          { return p.Notifier }
func (p *notifierProbe) SetEventDispatcher(fn func(ctx context.Context, event any) error) {
	p.Notifier.SetEventDispatcher(fn)
}

// wrapOwned replaces every owned field of s with a probe decorating what
// it holds, or, given prev, what prev's probes decorate, and returns the
// probes in ownedFields order.
func wrapOwned(s *app.Services, prev *[ownedFieldCount]*retireProbe, forward bool) [ownedFieldCount]*retireProbe {
	if prev != nil {
		unwrapOwned(s, prev)
	}
	var rps [ownedFieldCount]*retireProbe
	rp := func(i int, inner any) *retireProbe {
		rps[i] = &retireProbe{inner: inner, forward: forward}
		return rps[i]
	}
	s.Log = &logProbe{s.Log, rp(fieldLog, s.Log)}
	s.Errors = &errorsProbe{s.Errors, rp(fieldErrors, s.Errors)}
	s.Crypto = &cryptoProbe{s.Crypto, rp(fieldCrypto, s.Crypto)}
	s.DB = &dbProbe{s.DB, rp(fieldDB, s.DB)}
	s.Auth = &authProbe{s.Auth, rp(fieldAuth, s.Auth)}
	s.CSRF = &csrfSwapProbe{s.CSRF, rp(fieldCSRF, s.CSRF)}
	s.View = &viewProbe{s.View, rp(fieldView, s.View)}
	s.Cache = &cacheProbe{s.Cache, rp(fieldCache, s.Cache)}
	s.Events = &eventsProbe{s.Events, rp(fieldEvents, s.Events)}
	s.Queue = &queueProbe{s.Queue, rp(fieldQueue, s.Queue)}
	s.Storage = &storageProbe{s.Storage, rp(fieldStorage, s.Storage)}
	s.Scheduler = &schedulerProbe{s.Scheduler, rp(fieldScheduler, s.Scheduler)}
	s.Mail = &mailProbe{s.Mail, rp(fieldMail, s.Mail)}
	s.Notification = &notifierProbe{s.Notification, rp(fieldNotification, s.Notification)}
	return rps
}

// unwrapOwned puts back in s what prev's probes decorate.
func unwrapOwned(s *app.Services, prev *[ownedFieldCount]*retireProbe) {
	s.Log = prev[fieldLog].inner.(contract.Logger)
	s.Errors = prev[fieldErrors].inner.(contract.ErrorHandler)
	s.Crypto = prev[fieldCrypto].inner.(contract.Encryptor)
	s.DB = prev[fieldDB].inner.(contract.Database)
	s.Auth = prev[fieldAuth].inner.(contract.AuthManager)
	s.CSRF = prev[fieldCSRF].inner.(contract.CSRFProtector)
	s.View = prev[fieldView].inner.(contract.ViewEngine)
	s.Cache = prev[fieldCache].inner.(contract.CacheManager)
	s.Events = prev[fieldEvents].inner.(contract.Dispatcher)
	s.Queue = prev[fieldQueue].inner.(contract.QueueDriver)
	s.Storage = prev[fieldStorage].inner.(contract.StorageManager)
	s.Scheduler = prev[fieldScheduler].inner.(scheduler.TaskScheduler)
	s.Mail = prev[fieldMail].inner.(contract.Mailer)
	s.Notification = prev[fieldNotification].inner.(contract.Notifier)
}

// ownedClosedAtShutdown marks the owned fields App.Shutdown closes the
// final instance of; Auth, Crypto, Events and Errors hold none it closes.
var ownedClosedAtShutdown = [ownedFieldCount]bool{
	fieldLog: true, fieldDB: true, fieldCSRF: true, fieldView: true,
	fieldCache: true, fieldQueue: true, fieldStorage: true, fieldScheduler: true,
	fieldMail: true, fieldNotification: true,
}

// conformanceSwapConfig is the test config with every owned field built:
// a database, an APP_KEY and a view engine.
func conformanceSwapConfig() Config {
	cfg := testAppConfig()
	cfg.DB = DBConfig{Connection: "sqlite", Database: ":memory:"}
	cfg.Crypto.Key = strings.Repeat("k", 32)
	cfg.Key = strings.Repeat("k", 32)
	cfg.View = view.Config{RootTemplate: inertiaTestTemplate, Version: "v1"}
	return cfg
}

func TestConformance_OwnedFieldsFollowAReplacement(t *testing.T) {
	defer goleak.VerifyNone(t, goleak.IgnoreCurrent())

	var first, second [ownedFieldCount]*retireProbe
	a, err := New(WithConfig(conformanceSwapConfig()), WithModules(swapIn(func(s *app.Services) {
		first = wrapOwned(s, nil, false)
	})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i, v := range ownedFields(a) {
		if v == nil {
			t.Fatalf("Services.%s is nil: the swap cannot exercise it", ownedFieldNames[i])
		}
	}
	a.Modules(func(r *chain.ModuleRegistry) {
		r.Add(swapIn(func(s *app.Services) { second = wrapOwned(s, &first, true) }))
	})
	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap: %v", err)
	}

	// Consumers bound through a cell observe the second replacement.
	if got := a.cryptoCell.load(); got != contract.Encryptor(a.Services.Crypto) {
		t.Errorf("encryptor cell holds %T, want the replacement", got)
	}
	if got := a.mailCell.load(); got != contract.Mailer(a.Services.Mail) {
		t.Errorf("mailer cell holds %T, want the replacement", got)
	}

	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	for i := range ownedFieldCount {
		if got := first[i].closes.Load(); got != 1 {
			t.Errorf("Services.%s: displaced replacement shut down %d times, want 1", ownedFieldNames[i], got)
		}
		want := int32(0)
		if ownedClosedAtShutdown[i] {
			want = 1
		}
		if got := second[i].closes.Load(); got != want {
			t.Errorf("Services.%s: final replacement shut down %d times, want %d", ownedFieldNames[i], got, want)
		}
	}
}

// TestConformance_OwnedFieldsAreEnumerated pins the owned field list to
// app.Services: a service field added there without a row in ownedFields
// (and so without rebind following it) fails here by name.
func TestConformance_OwnedFieldsAreEnumerated(t *testing.T) {
	notOwned := map[string]bool{
		"Validator": true, "RedirectAllowlist": true, "CookiePolicy": true, "FlashBag": true,
	}
	var got []string
	st := reflect.TypeOf(app.Services{})
	for i := range st.NumField() {
		f := st.Field(i)
		if f.IsExported() && !notOwned[f.Name] {
			got = append(got, f.Name)
		}
	}
	if !slices.Equal(got, ownedFieldNames[:]) {
		t.Fatalf("owned Services fields = %v, ownedFieldNames = %v: add the new field to ownedFields and its consumers to boundConsumers", got, ownedFieldNames)
	}
}
