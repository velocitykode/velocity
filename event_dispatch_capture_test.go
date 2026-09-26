package velocity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/cache"
	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/console"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/scheduler"
)

// dispatchLoopModule starts, in Start, a goroutine that keeps firing events
// through a framework service until Shutdown, the shape of a module that
// launches a worker or poller. fire is resolved against the services in
// Start and called once per loop iteration. After each fire the loop offers
// a tick on fired (non-blocking), so a module can wait for dispatches.
type dispatchLoopModule struct {
	fire  func(s *app.Services) func()
	fired chan struct{}
	stop  chan struct{}
	done  chan struct{}
	once  sync.Once
}

func newDispatchLoopModule(fire func(s *app.Services) func()) *dispatchLoopModule {
	return &dispatchLoopModule{
		fire:  fire,
		fired: make(chan struct{}),
		stop:  make(chan struct{}),
		done:  make(chan struct{}),
	}
}

func (m *dispatchLoopModule) Init(_ *app.Services) error { return nil }

func (m *dispatchLoopModule) Start(s *app.Services) error {
	fire := m.fire(s)
	go func() {
		defer close(m.done)
		for {
			select {
			case <-m.stop:
				return
			default:
			}
			fire()
			select {
			case m.fired <- struct{}{}:
			default:
			}
			time.Sleep(50 * time.Microsecond)
		}
	}()
	return nil
}

// halt stops the dispatch loop and waits for it to exit. Safe to call more
// than once (the test calls it before Shutdown runs the module's own).
func (m *dispatchLoopModule) halt() {
	m.once.Do(func() { close(m.stop) })
	<-m.done
}

func (m *dispatchLoopModule) Shutdown(_ context.Context) error {
	m.halt()
	return nil
}

// eventsSwapModule replaces Services.Events in Start with to. With a
// dispatch loop to wait on, it waits for the loop to dispatch, writes, then
// waits for two more dispatches before returning, so at least one whole
// dispatch runs after the write and before New re-wires the services: the
// window in which a call-time read of Services.Events races the write.
type eventsSwapModule struct {
	fired <-chan struct{}
	to    contract.Dispatcher
}

func (m *eventsSwapModule) Init(_ *app.Services) error { return nil }

func (m *eventsSwapModule) Start(s *app.Services) error {
	if m.fired != nil {
		<-m.fired
	}
	s.Events = m.to
	if m.fired != nil {
		<-m.fired
		<-m.fired
	}
	return nil
}

func (m *eventsSwapModule) Shutdown(_ context.Context) error { return nil }

// eventsSwapEventModule is a chain EventModule that replaces Services.Events
// from its Events callback.
type eventsSwapEventModule struct {
	services *app.Services
	to       contract.Dispatcher
}

func (m *eventsSwapEventModule) Init(s *app.Services) error {
	m.services = s
	return nil
}

func (m *eventsSwapEventModule) Start(_ *app.Services) error      { return nil }
func (m *eventsSwapEventModule) Shutdown(_ context.Context) error { return nil }
func (m *eventsSwapEventModule) Events(_ events.Dispatcher)       { m.services.Events = m.to }

// countEvents returns how many events of type T fake recorded.
func countEvents[T any](fake *events.FakeDispatcher) int {
	n := 0
	for _, ev := range fake.GetDispatchedEvents() {
		if _, ok := ev.(T); ok {
			n++
		}
	}
	return n
}

// waitForEvent polls fake until it records an event of type T or the
// deadline passes.
func waitForEvent[T any](t *testing.T, fake *events.FakeDispatcher, what string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if countEvents[T](fake) > 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("%s never reached the dispatcher the module swapped in", what)
}

// VEL-139: a service dispatching from a goroutine an earlier module's Start
// launched must not race a later module's Start replacing Services.Events.
// The dispatch closure is bound to the dispatcher it was wired with, so the
// only shared state is the service's own (synchronized) dispatcher slot. On
// a call-time read of Services.Events, -race reports the loop's read
// against the swap module's write. Once New re-wires after the WithModules
// lifecycle, the loop's later dispatches reach the swapped-in dispatcher.
func TestNew_ModuleStartSwapsEventsWhileServiceDispatches(t *testing.T) {
	tests := []struct {
		name string
		fire func(s *app.Services) func()
		// reached waits until the swapped-in dispatcher recorded an
		// event from the loop.
		reached func(t *testing.T, fake *events.FakeDispatcher)
	}{
		{
			name: "queue push (job.queued)",
			fire: func(s *app.Services) func() {
				return func() {
					_ = s.Queue.PushCtx(context.Background(), &fakeQueueJob{name: "loop"})
				}
			},
			reached: func(t *testing.T, fake *events.FakeDispatcher) {
				waitForEvent[*queue.JobQueued](t, fake, "job.queued")
			},
		},
		{
			name: "cache write",
			fire: func(s *app.Services) func() {
				return func() { _ = s.Cache.Put("vel139", 1, time.Minute) }
			},
			reached: func(t *testing.T, fake *events.FakeDispatcher) {
				waitForEvent[*cache.CacheWritten](t, fake, "cache written")
			},
		},
		{
			name: "batch global hook",
			fire: func(s *app.Services) func() {
				return func() {
					_, _ = queue.NewBatch(&fakeQueueJob{name: "batched"}).Dispatch(context.Background(), s.Queue)
				}
			},
			reached: func(t *testing.T, fake *events.FakeDispatcher) {
				waitForEvent[*queue.BatchCreated](t, fake, "batch created")
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := events.NewFakeDispatcher()
			after := events.NewFakeDispatcher()
			loop := newDispatchLoopModule(tt.fire)

			a, err := NewTestApp(
				WithFakeEvents(before),
				WithModules(loop, &eventsSwapModule{fired: loop.fired, to: after}),
			)
			if err != nil {
				t.Fatalf("NewTestApp() error: %v", err)
			}
			// Deferred first, so it runs last: the batch row leaves
			// unfinished batches and callback entries in the process-wide
			// repository, which neither Shutdown nor pruning removes.
			defer queue.ResetDefaultBatchRepositoryForTest()
			defer a.Shutdown(context.Background())
			defer loop.halt()

			tt.reached(t, after)
		})
	}
}

// VEL-139: the worker options `queue work` builds carry a dispatcher bound
// when they are built, so a later write to Services.Events does not race
// the worker's dispatches (and does not redirect them: the options are
// built after bootstrap, when the dispatcher is final).
func TestQueueWorkOptions_DispatcherCapturedWhenBuilt(t *testing.T) {
	built := events.NewFakeDispatcher()
	a, err := NewTestApp(WithFakeEvents(built))
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())

	opts := queueWorkOptions(a, console.QueueWorkOptions{})
	if opts.Dispatcher == nil {
		t.Fatal("queueWorkOptions attached no dispatcher for an app with events")
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = opts.Dispatcher(context.Background(), testEvent{name: "worker.event"})
		}
	}()
	a.Services.Events = events.NewFakeDispatcher()
	wg.Wait()

	if got := countEvents[testEvent](built); got != 200 {
		t.Fatalf("dispatcher the options were built with recorded %d worker events, want 200", got)
	}
}

// VEL-139: a dispatcher swapped in at each lifecycle point that can replace
// Services.Events is what dispatches after that point reach, on every path
// the wiring hands the closure to: an aware service (queue driver), a
// registry component, the batch global hook, and the `queue work` options.
func TestEventDispatcherSwap_ReachedAfterEachRewirePoint(t *testing.T) {
	tests := []struct {
		name string
		// setup builds the app with original as its dispatcher and
		// arranges for to to replace it at the lifecycle point under test.
		setup func(t *testing.T, original, to *events.FakeDispatcher, probe *componentProbe) *App
	}{
		{
			name: "WithModules Start",
			setup: func(t *testing.T, original, to *events.FakeDispatcher, probe *componentProbe) *App {
				a, err := NewTestApp(
					WithFakeEvents(original),
					WithModules(&componentRegisteringModule{probe: probe}, &eventsSwapModule{to: to}),
				)
				if err != nil {
					t.Fatalf("NewTestApp() error: %v", err)
				}
				return a
			},
		},
		{
			name: "chain module Start",
			setup: func(t *testing.T, original, to *events.FakeDispatcher, probe *componentProbe) *App {
				a := newTestAppWith(t, original)
				a.Modules(func(r *chain.ModuleRegistry) {
					r.Add(&componentRegisteringModule{probe: probe}, &eventsSwapModule{to: to})
				})
				return a
			},
		},
		{
			name: "EventModule Events",
			setup: func(t *testing.T, original, to *events.FakeDispatcher, probe *componentProbe) *App {
				a := newTestAppWith(t, original)
				a.Modules(func(r *chain.ModuleRegistry) {
					r.Add(&componentRegisteringModule{probe: probe}, &eventsSwapEventModule{to: to})
				})
				return a
			},
		},
		{
			name: "Events callback",
			setup: func(t *testing.T, original, to *events.FakeDispatcher, probe *componentProbe) *App {
				a := newTestAppWith(t, original)
				a.Modules(func(r *chain.ModuleRegistry) {
					r.Add(&componentRegisteringModule{probe: probe})
				})
				a.Events(func(events.Dispatcher) { a.Services.Events = to })
				return a
			},
		},
		{
			name: "Schedule callback",
			setup: func(t *testing.T, original, to *events.FakeDispatcher, probe *componentProbe) *App {
				a := newTestAppWith(t, original)
				a.Modules(func(r *chain.ModuleRegistry) {
					r.Add(&componentRegisteringModule{probe: probe})
				})
				a.Schedule(func(scheduler.TaskScheduler) { a.Services.Events = to })
				return a
			},
		},
		{
			name: "Errors callback",
			setup: func(t *testing.T, original, to *events.FakeDispatcher, probe *componentProbe) *App {
				a := newTestAppWith(t, original)
				a.Modules(func(r *chain.ModuleRegistry) {
					r.Add(&componentRegisteringModule{probe: probe})
				})
				a.Errors(func(contract.ErrorHandler) { a.Services.Events = to })
				return a
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			original := events.NewFakeDispatcher()
			to := events.NewFakeDispatcher()
			probe := &componentProbe{}

			a := tt.setup(t, original, to, probe)
			defer queue.ResetDefaultBatchRepositoryForTest()
			defer a.Shutdown(context.Background())
			if err := a.Bootstrap(); err != nil {
				t.Fatalf("Bootstrap() error: %v", err)
			}
			original.ClearEvents()

			ctx := context.Background()
			if err := a.Queue.PushCtx(ctx, &fakeQueueJob{name: "after-swap"}); err != nil {
				t.Fatalf("PushCtx: %v", err)
			}
			dispatch := probe.getDispatcher()
			if dispatch == nil {
				t.Fatal("registry component never received an event dispatcher")
			}
			if err := dispatch(ctx, testEvent{name: "component.event"}); err != nil {
				t.Fatalf("component dispatch: %v", err)
			}
			if _, err := queue.NewBatch(&fakeQueueJob{name: "batched"}).Dispatch(ctx, a.Queue); err != nil {
				t.Fatalf("batch Dispatch: %v", err)
			}
			opts := queueWorkOptions(a, console.QueueWorkOptions{})
			if opts.Dispatcher == nil {
				t.Fatal("queueWorkOptions attached no dispatcher")
			}
			if err := opts.Dispatcher(ctx, testEvent{name: "worker.event"}); err != nil {
				t.Fatalf("worker dispatch: %v", err)
			}

			if n := countEvents[*queue.JobQueued](to); n == 0 {
				t.Error("queue push: job.queued did not reach the swapped-in dispatcher")
			}
			if n := countEvents[*queue.BatchCreated](to); n == 0 {
				t.Error("batch global hook: batch created did not reach the swapped-in dispatcher")
			}
			for _, name := range []string{"component.event", "worker.event"} {
				if !hasTestEvent(to, name) {
					t.Errorf("%s did not reach the swapped-in dispatcher", name)
				}
			}
			if got := original.GetDispatchedEvents(); len(got) != 0 {
				t.Errorf("replaced dispatcher still received %d events after the swap: %v", len(got), got)
			}
		})
	}
}

// VEL-139: a module that sets Services.Events to nil after the dispatcher
// was wired turns dispatch into a no-op, as WithoutEvents does, instead of
// leaving the replaced dispatcher wired.
func TestEventDispatcherSwap_ToNilClearsWiring(t *testing.T) {
	original := events.NewFakeDispatcher()
	probe := &componentProbe{}

	a, err := NewTestApp(
		WithFakeEvents(original),
		WithModules(&componentRegisteringModule{probe: probe}, &eventsSwapModule{to: nil}),
	)
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	defer a.Shutdown(context.Background())
	original.ClearEvents()

	if err := a.Queue.PushCtx(context.Background(), &fakeQueueJob{name: "after-nil"}); err != nil {
		t.Fatalf("PushCtx: %v", err)
	}
	if d := probe.getDispatcher(); d != nil {
		t.Error("registry component kept a dispatcher after Services.Events was set to nil")
	}
	if opts := queueWorkOptions(a, console.QueueWorkOptions{}); opts.Dispatcher != nil {
		t.Error("queueWorkOptions attached a dispatcher after Services.Events was set to nil")
	}
	if got := original.GetDispatchedEvents(); len(got) != 0 {
		t.Errorf("replaced dispatcher still received %d events: %v", len(got), got)
	}
}

func newTestAppWith(t *testing.T, d *events.FakeDispatcher) *App {
	t.Helper()
	a, err := NewTestApp(WithFakeEvents(d))
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}
	return a
}

func hasTestEvent(fake *events.FakeDispatcher, name string) bool {
	for _, ev := range fake.GetDispatchedEvents() {
		if te, ok := ev.(testEvent); ok && te.name == name {
			return true
		}
	}
	return false
}

// requestStartedBlocker is a dispatcher whose Dispatch blocks on release
// for router.RequestStarted and records every event. Blocking only that
// event keeps the other services' synchronous dispatches flowing while it
// shows whether the router delivers on the request goroutine (sync) or on
// its worker pool (async).
type requestStartedBlocker struct {
	*events.FakeDispatcher
	release chan struct{}
	started chan struct{}
	once    sync.Once
}

func newRequestStartedBlocker() *requestStartedBlocker {
	return &requestStartedBlocker{
		FakeDispatcher: events.NewFakeDispatcher(),
		release:        make(chan struct{}),
		started:        make(chan struct{}),
	}
}

func (b *requestStartedBlocker) Dispatch(ctx context.Context, event interface{}) error {
	err := b.FakeDispatcher.Dispatch(ctx, event)
	if _, ok := event.(*router.RequestStarted); ok {
		b.once.Do(func() { close(b.started) })
		<-b.release
	}
	return err
}

// VEL-139: an async router delivery mode the app configured in a
// bootstrap callback survives the later re-wires (a request returns while
// the listener is blocked), and, per the lifecycle policy, the router's
// events reach the app's current Services.Events after bootstrap: the
// dispatcher swapped in later, or, with no swap, the app dispatcher in
// place of the separate sink the callback configured.
func TestBootstrap_RouterAsyncDeliverySurvivesRewire(t *testing.T) {
	tests := []struct {
		name string
		// inEvents configures async delivery from the Events callback
		// instead of the Routes callback. swap targets the app
		// dispatcher and replaces Services.Events in the Errors callback;
		// otherwise the callback targets a separate sink and the app
		// dispatcher never changes.
		inEvents bool
		swap     bool
	}{
		{name: "configured in Routes, dispatcher swapped", swap: true},
		{name: "configured in Events, dispatcher swapped", inEvents: true, swap: true},
		{name: "configured in Routes with a separate sink, no swap"},
		{name: "configured in Events with a separate sink, no swap", inEvents: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// want is the app dispatcher after bootstrap and must get the
			// router's events; configured is the target the callback
			// gave the pool and must get none after bootstrap.
			want := newRequestStartedBlocker()
			configured := events.NewFakeDispatcher()
			a := newTestAppWith(t, configured)
			if tt.swap {
				a.Errors(func(contract.ErrorHandler) { a.Services.Events = want })
			} else {
				a.Services.Events = want
			}

			var released bool
			release := func() {
				if !released {
					released = true
					close(want.release)
				}
			}
			served := make(chan struct{})
			serving := false
			defer a.Shutdown(context.Background())
			// Release the listener and join the request before Shutdown,
			// which stops the router's worker pool, also on a Fatal path.
			defer func() {
				release()
				if serving {
					<-served
				}
			}()

			configure := func(r *router.VelocityRouterV2) {
				r.SetAsyncEventDispatcher(configured.Dispatch, 1, 16)
			}
			a.Routes(func(r *chain.Routing) {
				if !tt.inEvents {
					configure(r.Router())
				}
				r.Router().Get("/ping", func(c *router.Context) error { return nil })
			})
			if tt.inEvents {
				a.Events(func(events.Dispatcher) { configure(a.Router) })
			}

			if err := a.Bootstrap(); err != nil {
				t.Fatalf("Bootstrap() error: %v", err)
			}

			serving = true
			go func() {
				defer close(served)
				a.Router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ping", nil))
			}()

			select {
			case <-want.started:
			case <-time.After(5 * time.Second):
				t.Fatal("request.started never reached the app's current dispatcher")
			}
			select {
			case <-served:
			case <-time.After(5 * time.Second):
				t.Fatal("request blocked on the listener: router delivery is no longer async")
			}
			release()

			if n := countEvents[*router.RequestStarted](configured); n != 0 {
				t.Errorf("target the callback configured received %d request.started after bootstrap", n)
			}
		})
	}
}

// secondComponentProbe is a second aware component type: the registry is
// keyed by type, so a test registering two probes needs two types.
type secondComponentProbe struct {
	dispatcherProbe
}

// VEL-139: with Services.Events unchanged, a consumer added or replaced in
// a Routes or Events callback (a registry component, a hook adapter, an
// aware service instance) is wired by the sweeps that follow them.
func TestBootstrap_ConsumersAddedInCallbacksAreWired(t *testing.T) {
	fake := events.NewFakeDispatcher()
	a := newTestAppWith(t, fake)
	defer a.Shutdown(context.Background())

	inRoutes := &componentProbe{}
	inEvents := &secondComponentProbe{}
	adapter := &dispatcherProbe{}
	csrfSwap := &csrfProbe{}
	a.Routes(func(*chain.Routing) {
		if err := app.Register(a.Services, inRoutes); err != nil {
			t.Errorf("Register in Routes: %v", err)
		}
	})
	a.Events(func(events.Dispatcher) {
		if err := app.Register(a.Services, inEvents); err != nil {
			t.Errorf("Register in Events: %v", err)
		}
		if err := app.Register(a.Services, &plainValue{name: "hooked"}, app.WithHooks(adapter)); err != nil {
			t.Errorf("Register with hooks in Events: %v", err)
		}
		// The replaced instance is no longer a.CSRF, so App.Shutdown
		// will not stop its store's cleanup goroutine; stop it here.
		if sd, ok := a.Services.CSRF.(contract.ShutdownAware); ok {
			t.Cleanup(func() { _ = sd.Shutdown(context.Background()) })
		}
		a.Services.CSRF = csrfSwap
	})
	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap() error: %v", err)
	}

	for name, probe := range map[string]*dispatcherProbe{
		"component registered in Routes":    &inRoutes.dispatcherProbe,
		"component registered in Events":    &inEvents.dispatcherProbe,
		"hook adapter registered in Events": adapter,
		"CSRF instance replaced in Events":  &csrfSwap.dispatcherProbe,
	} {
		t.Run(name, func(t *testing.T) {
			fake.ClearEvents()
			assertProbeDispatches(t, probe, fake)
		})
	}
}

// VEL-139: a Routes callback that sets Services.Events to nil clears the
// dispatch closures at the event registration boundary, so an event a
// later Schedule callback fires reaches nothing, as with WithoutEvents.
func TestBootstrap_RoutesSetsEventsNilClearsBeforeLaterCallbacks(t *testing.T) {
	original := events.NewFakeDispatcher()
	a := newTestAppWith(t, original)
	defer a.Shutdown(context.Background())

	a.Routes(func(*chain.Routing) { a.Services.Events = nil })
	var pushErr error
	a.Schedule(func(scheduler.TaskScheduler) {
		original.ClearEvents()
		pushErr = a.Queue.PushCtx(context.Background(), &fakeQueueJob{name: "after-nil"})
	})
	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap() error: %v", err)
	}
	if pushErr != nil {
		t.Fatalf("PushCtx: %v", pushErr)
	}
	if got := original.GetDispatchedEvents(); len(got) != 0 {
		t.Errorf("replaced dispatcher received %d events after Routes set Services.Events to nil: %v", len(got), got)
	}
}

// VEL-139: a dispatcher the Events callback swapped in is re-wired
// before the later bootstrap callbacks run, so an event a Schedule callback
// fires (here a queue push) already reaches it.
func TestBootstrap_EventsSwapReachedByLaterCallbacks(t *testing.T) {
	original := events.NewFakeDispatcher()
	to := events.NewFakeDispatcher()
	a := newTestAppWith(t, original)
	defer a.Shutdown(context.Background())

	a.Events(func(events.Dispatcher) { a.Services.Events = to })
	var pushErr error
	a.Schedule(func(scheduler.TaskScheduler) {
		pushErr = a.Queue.PushCtx(context.Background(), &fakeQueueJob{name: "from-schedule"})
	})
	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap() error: %v", err)
	}
	if pushErr != nil {
		t.Fatalf("PushCtx: %v", pushErr)
	}
	if n := countEvents[*queue.JobQueued](to); n != 1 {
		t.Errorf("swapped-in dispatcher recorded %d job.queued from the Schedule callback, want 1", n)
	}
	if n := countEvents[*queue.JobQueued](original); n != 0 {
		t.Errorf("replaced dispatcher recorded %d job.queued from the Schedule callback, want 0", n)
	}
}

// eventsNilModule sets Services.Events to nil in its Start.
type eventsNilModule struct{}

func (eventsNilModule) Init(_ *app.Services) error       { return nil }
func (eventsNilModule) Start(s *app.Services) error      { s.Events = nil; return nil }
func (eventsNilModule) Shutdown(_ context.Context) error { return nil }

// VEL-139: once a dispatcher has been wired, clearing is sticky. A chain
// module sets Services.Events to nil (the post-Start sweep clears every
// consumer); a Routes callback then introduces consumers still bound to
// the old dispatcher (an async router dispatcher and a component
// configured with it). The later boundaries clear those too, instead of
// treating the app as one with events disabled from the start.
func TestBootstrap_ClearingStaysStickyForConsumersAddedLater(t *testing.T) {
	old := events.NewFakeDispatcher()
	a := newTestAppWith(t, old)
	defer a.Shutdown(context.Background())

	probe := &componentProbe{}
	a.Modules(func(r *chain.ModuleRegistry) { r.Add(eventsNilModule{}) })
	a.Routes(func(r *chain.Routing) {
		r.Router().SetAsyncEventDispatcher(old.Dispatch, 1, 16)
		probe.SetEventDispatcher(old.Dispatch)
		if err := app.Register(a.Services, probe); err != nil {
			t.Errorf("Register in Routes: %v", err)
		}
		r.Router().Get("/ping", func(c *router.Context) error { return nil })
	})
	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap() error: %v", err)
	}
	old.ClearEvents()

	if d := probe.getDispatcher(); d != nil {
		t.Error("component configured with the old dispatcher after the clearing sweep kept it")
	}
	a.Router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ping", nil))
	// Drain the router's pool so any event it would deliver has landed.
	if err := a.Router.ShutdownEventDispatcher(context.Background()); err != nil {
		t.Fatalf("ShutdownEventDispatcher: %v", err)
	}
	if n := countEvents[*router.RequestStarted](old); n != 0 {
		t.Errorf("old dispatcher received %d request.started through the router pool configured after clearing", n)
	}
}
