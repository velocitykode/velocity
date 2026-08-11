package velocity

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"

	"github.com/velocitykode/velocity/chain"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/exceptions"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/scheduler"
)

// bootstrapTrackingModule extends trackingModule with optional bootstrap interfaces.
type bootstrapTrackingModule struct {
	trackingModule
	routesCalled     bool
	middlewareCalled bool
	eventsCalled     bool
	scheduleCalled   bool
	commandsCalled   bool
}

func (p *bootstrapTrackingModule) Routes(r *chain.Routing) {
	*p.calls = append(*p.calls, p.name+":routes")
	p.routesCalled = true
}

func (p *bootstrapTrackingModule) Middleware(m *chain.MiddlewareStack) {
	*p.calls = append(*p.calls, p.name+":middleware")
	p.middlewareCalled = true
}

func (p *bootstrapTrackingModule) Events(d events.Dispatcher) {
	*p.calls = append(*p.calls, p.name+":events")
	p.eventsCalled = true
}

func (p *bootstrapTrackingModule) Schedule(s scheduler.TaskScheduler) {
	*p.calls = append(*p.calls, p.name+":schedule")
	p.scheduleCalled = true
}

func (p *bootstrapTrackingModule) Commands(r *chain.Commands) {
	*p.calls = append(*p.calls, p.name+":commands")
	p.commandsCalled = true
}

// trackingMW returns middleware that sets a response header.
func trackingMW(key, value string) router.MiddlewareFunc {
	return func(next router.HandlerFunc) router.HandlerFunc {
		return func(c *router.Context) error {
			c.Response.Header().Set(key, value)
			return next(c)
		}
	}
}

// testEvent is a simple event for testing.
type testEvent struct{ name string }

func (e testEvent) Name() string { return e.name }

// testListener records whether it was called.
type testListener struct {
	events.BaseListener
	called bool
}

func (l *testListener) Handle(ctx context.Context, event interface{}) error {
	l.called = true
	return nil
}

func TestBootstrap_FullChain(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	var (
		modulesCalled    bool
		middlewareCalled bool
		routesCalled     bool
		eventsCalled     bool
		scheduleCalled   bool
		commandsCalled   bool
		exceptionsCalled bool
	)

	a.Modules(func(r *chain.ModuleRegistry) {
		modulesCalled = true
	}).Middleware(func(m *chain.MiddlewareStack) {
		middlewareCalled = true
	}).Routes(func(r *chain.Routing) {
		routesCalled = true
	}).Events(func(d events.Dispatcher) {
		eventsCalled = true
	}).Schedule(func(s scheduler.TaskScheduler) {
		scheduleCalled = true
	}).Commands(func(r *chain.Commands) {
		commandsCalled = true
	}).Exceptions(func(h exceptions.ExceptionHandler) {
		exceptionsCalled = true
	})

	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap() error: %v", err)
	}

	if !modulesCalled {
		t.Error("Modules callback not called")
	}
	if !middlewareCalled {
		t.Error("Middleware callback not called")
	}
	if !routesCalled {
		t.Error("Routes callback not called")
	}
	if !eventsCalled {
		t.Error("Events callback not called")
	}
	if !scheduleCalled {
		t.Error("Schedule callback not called")
	}
	if !commandsCalled {
		t.Error("Commands callback not called")
	}
	if !exceptionsCalled {
		t.Error("Exceptions callback not called")
	}
}

func TestBootstrap_ChainOrderIndependent(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	var order []string

	// Register in reverse order
	a.Exceptions(func(h exceptions.ExceptionHandler) {
		order = append(order, "exceptions")
	}).Commands(func(r *chain.Commands) {
		order = append(order, "commands")
	}).Schedule(func(s scheduler.TaskScheduler) {
		order = append(order, "schedule")
	}).Events(func(d events.Dispatcher) {
		order = append(order, "events")
	}).Routes(func(r *chain.Routing) {
		order = append(order, "routes")
	}).Middleware(func(m *chain.MiddlewareStack) {
		order = append(order, "middleware")
	}).Modules(func(r *chain.ModuleRegistry) {
		order = append(order, "providers")
	})

	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap() error: %v", err)
	}

	// Execution order must be fixed regardless of registration order
	want := []string{"providers", "middleware", "routes", "events", "schedule", "commands", "exceptions"}
	if len(order) != len(want) {
		t.Fatalf("got %d calls, want %d: %v", len(order), len(want), order)
	}
	for i, c := range order {
		if c != want[i] {
			t.Errorf("order[%d] = %q, want %q", i, c, want[i])
		}
	}
}

func TestBootstrap_ModuleLifecycle(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	var calls []string
	pA := &bootstrapTrackingModule{trackingModule: trackingModule{name: "A", calls: &calls}}
	pB := &bootstrapTrackingModule{trackingModule: trackingModule{name: "B", calls: &calls}}

	a.Modules(func(r *chain.ModuleRegistry) {
		r.Add(pA, pB)
	})

	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap() error: %v", err)
	}

	want := []string{
		"A:register", "B:register",
		"A:boot", "B:boot",
		"A:middleware", "B:middleware",
		"A:routes", "B:routes",
		"A:events", "B:events",
		"A:schedule", "B:schedule",
		"A:commands", "B:commands",
	}
	if len(calls) != len(want) {
		t.Fatalf("got %d calls, want %d:\n  got:  %v\n  want: %v", len(calls), len(want), calls, want)
	}
	for i, c := range calls {
		if c != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, c, want[i])
		}
	}

	if !pA.routesCalled || !pA.middlewareCalled || !pA.eventsCalled || !pA.scheduleCalled || !pA.commandsCalled {
		t.Error("provider A missing optional interface calls")
	}
	if !pB.routesCalled || !pB.middlewareCalled || !pB.eventsCalled || !pB.scheduleCalled || !pB.commandsCalled {
		t.Error("provider B missing optional interface calls")
	}
}

func TestBootstrap_ShutdownOrder(t *testing.T) {
	var calls []string
	withA := &trackingModule{name: "withA", calls: &calls}
	withB := &trackingModule{name: "withB", calls: &calls}

	a, err := NewTestApp(WithModules(withA, withB))
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	chainA := &trackingModule{name: "chainA", calls: &calls}
	chainB := &trackingModule{name: "chainB", calls: &calls}

	a.Modules(func(r *chain.ModuleRegistry) {
		r.Add(chainA, chainB)
	})

	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap() error: %v", err)
	}

	// Clear register/boot calls, only track shutdown
	calls = nil

	if err := a.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error: %v", err)
	}

	// Chain modules shut down first (reverse), then WithModules (reverse)
	want := []string{"chainB:shutdown", "chainA:shutdown", "withB:shutdown", "withA:shutdown"}
	if len(calls) != len(want) {
		t.Fatalf("got %d shutdown calls, want %d: %v", len(calls), len(want), calls)
	}
	for i, c := range calls {
		if c != want[i] {
			t.Errorf("shutdown[%d] = %q, want %q", i, c, want[i])
		}
	}
}

func TestBootstrap_RegisterError(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	wantErr := errors.New("register boom")
	var calls []string
	pA := &trackingModule{name: "A", calls: &calls, initErr: wantErr}

	a.Modules(func(r *chain.ModuleRegistry) {
		r.Add(pA)
	})

	err = a.bootstrap()
	if err == nil {
		t.Fatal("expected error from register")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("expected wrapped register error, got: %v", err)
	}
}

func TestBootstrap_BootError(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	wantErr := errors.New("boot boom")
	var calls []string
	pA := &trackingModule{name: "A", calls: &calls, startErr: wantErr}

	a.Modules(func(r *chain.ModuleRegistry) {
		r.Add(pA)
	})

	err = a.bootstrap()
	if err == nil {
		t.Fatal("expected error from boot")
	}
	if !errors.Is(err, wantErr) {
		t.Errorf("expected wrapped boot error, got: %v", err)
	}
}

func TestBootstrap_NilCallbacks(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	var calls []string
	pA := &trackingModule{name: "A", calls: &calls}

	a.Modules(func(r *chain.ModuleRegistry) {
		r.Add(pA)
	})

	// Only Modules set, no other chain methods
	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap() should succeed with nil callbacks: %v", err)
	}

	// Module register + boot should still run
	want := []string{"A:register", "A:boot"}
	if len(calls) != len(want) {
		t.Fatalf("got %d calls, want %d: %v", len(calls), len(want), calls)
	}
	for i, c := range calls {
		if c != want[i] {
			t.Errorf("call[%d] = %q, want %q", i, c, want[i])
		}
	}
}

func TestBootstrap_NoChainMethods(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	// No chain methods at all — backward compat
	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap() should succeed with no chain methods: %v", err)
	}
}

func TestBootstrap_GlobalMiddlewareApplied(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	a.Middleware(func(m *chain.MiddlewareStack) {
		m.Global(trackingMW("X-Global", "yes"))
	}).Routes(func(r *chain.Routing) {
		r.Web(func(rt router.Router) {
			rt.Get("/test", func(c *router.Context) error {
				return c.String(200, "ok")
			})
		})
	})

	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap() error: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/test", nil)
	a.Router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("X-Global"); got != "yes" {
		t.Errorf("X-Global = %q, want %q", got, "yes")
	}
}

func TestBootstrap_WebMiddlewareOnlyOnWeb(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	a.Middleware(func(m *chain.MiddlewareStack) {
		m.Web(trackingMW("X-Web", "yes"))
	}).Routes(func(r *chain.Routing) {
		r.Web(func(rt router.Router) {
			rt.Get("/web-route", func(c *router.Context) error {
				return c.String(200, "web")
			})
		})
		r.API("/api", func(rt router.Router) {
			rt.Get("/data", func(c *router.Context) error {
				return c.String(200, "api")
			})
		})
	})

	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap() error: %v", err)
	}

	// Web route should have X-Web header
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/web-route", nil)
	a.Router.ServeHTTP(w, req)

	if got := w.Header().Get("X-Web"); got != "yes" {
		t.Errorf("web route: X-Web = %q, want %q", got, "yes")
	}

	// API route should NOT have X-Web header
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/data", nil)
	a.Router.ServeHTTP(w, req)

	if got := w.Header().Get("X-Web"); got != "" {
		t.Errorf("api route: X-Web = %q, want empty", got)
	}
}

func TestBootstrap_APIMiddlewareOnlyOnAPI(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	a.Middleware(func(m *chain.MiddlewareStack) {
		m.API(trackingMW("X-API", "yes"))
	}).Routes(func(r *chain.Routing) {
		r.Web(func(rt router.Router) {
			rt.Get("/web-route", func(c *router.Context) error {
				return c.String(200, "web")
			})
		})
		r.API("/api", func(rt router.Router) {
			rt.Get("/data", func(c *router.Context) error {
				return c.String(200, "api")
			})
		})
	})

	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap() error: %v", err)
	}

	// API route should have X-API header
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/data", nil)
	a.Router.ServeHTTP(w, req)

	if got := w.Header().Get("X-API"); got != "yes" {
		t.Errorf("api route: X-API = %q, want %q", got, "yes")
	}

	// Web route should NOT have X-API header
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/web-route", nil)
	a.Router.ServeHTTP(w, req)

	if got := w.Header().Get("X-API"); got != "" {
		t.Errorf("web route: X-API = %q, want empty", got)
	}
}

func TestBootstrap_EventsRegistered(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	listener := &testListener{}

	a.Events(func(d events.Dispatcher) {
		d.Listen("test.event", listener)
	})

	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap() error: %v", err)
	}

	// Dispatch event and verify listener was called
	if err := a.Services.Events.Dispatch(context.Background(), testEvent{name: "test.event"}); err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}

	if !listener.called {
		t.Error("listener was not called after dispatch")
	}
}

func TestBootstrap_ScheduleRegistered(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	a.Schedule(func(s scheduler.TaskScheduler) {
		s.Call(func() {}).EveryMinute()
	})

	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap() error: %v", err)
	}

	jobs := a.Scheduler.Jobs()
	if len(jobs) != 1 {
		t.Errorf("got %d jobs, want 1", len(jobs))
	}
}

func TestBootstrap_ExceptionsConfigured(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	var handlerRef exceptions.ExceptionHandler

	a.Exceptions(func(h exceptions.ExceptionHandler) {
		handlerRef = h
	})

	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap() error: %v", err)
	}

	if handlerRef == nil {
		t.Fatal("exceptions handler is nil")
	}
	if handlerRef != a.Services.Exceptions {
		t.Error("exceptions handler does not match a.Services.Exceptions")
	}
}

func TestBootstrap_ChainReturnsSameApp(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	got := a.Modules(func(*chain.ModuleRegistry) {})
	if got != a {
		t.Error("Modules() did not return same *App")
	}

	got = a.Middleware(func(*chain.MiddlewareStack) {})
	if got != a {
		t.Error("Middleware() did not return same *App")
	}

	got = a.Routes(func(*chain.Routing) {})
	if got != a {
		t.Error("Routes() did not return same *App")
	}

	got = a.Events(func(events.Dispatcher) {})
	if got != a {
		t.Error("Events() did not return same *App")
	}

	got = a.Schedule(func(scheduler.TaskScheduler) {})
	if got != a {
		t.Error("Schedule() did not return same *App")
	}

	got = a.Commands(func(*chain.Commands) {})
	if got != a {
		t.Error("Commands() did not return same *App")
	}

	got = a.Exceptions(func(exceptions.ExceptionHandler) {})
	if got != a {
		t.Error("Exceptions() did not return same *App")
	}
}

func TestBootstrap_Idempotent(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	callCount := 0
	a.Routes(func(r *chain.Routing) {
		callCount++
	})

	if err := a.bootstrap(); err != nil {
		t.Fatalf("first bootstrap() error: %v", err)
	}
	if err := a.bootstrap(); err != nil {
		t.Fatalf("second bootstrap() error: %v", err)
	}

	if callCount != 1 {
		t.Errorf("callback called %d times, want 1", callCount)
	}
}

func TestBootstrap_BackwardCompat_RouterUse(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	// Old-style: register directly on Router
	a.Router.Use(trackingMW("X-Old", "yes"))
	a.Router.Get("/old-route", func(c *router.Context) error {
		return c.String(200, "old")
	})

	// No chain methods — backward compat
	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap() error: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/old-route", nil)
	a.Router.ServeHTTP(w, req)

	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("X-Old"); got != "yes" {
		t.Errorf("X-Old = %q, want %q", got, "yes")
	}
}

func TestBootstrap_BackwardCompat_MixedOldNew(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	// Old-style direct route
	a.Router.Get("/old", func(c *router.Context) error {
		return c.String(200, "old")
	})

	// New-style chain route
	a.Routes(func(r *chain.Routing) {
		r.Web(func(rt router.Router) {
			rt.Get("/new", func(c *router.Context) error {
				return c.String(200, "new")
			})
		})
	})

	if err := a.bootstrap(); err != nil {
		t.Fatalf("bootstrap() error: %v", err)
	}

	// Old route should work
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/old", nil)
	a.Router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("/old status = %d, want 200", w.Code)
	}

	// New route should also work
	w = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/new", nil)
	a.Router.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Errorf("/new status = %d, want 200", w.Code)
	}
}

// TestBootstrap_FailureIsSticky proves that a failed bootstrap latches its
// error: the second Bootstrap() call must return the SAME non-nil error
// instead of nil (re-running a partially-completed bootstrap would
// double-register middleware and routes, so sticky-error is the only safe
// semantics).
func TestBootstrap_FailureIsSticky(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	wantErr := errors.New("register boom")
	var calls []string
	a.Modules(func(r *chain.ModuleRegistry) {
		r.Add(&trackingModule{name: "A", calls: &calls, initErr: wantErr})
	})

	first := a.Bootstrap()
	if !errors.Is(first, wantErr) {
		t.Fatalf("first Bootstrap() = %v, want wrapped %v", first, wantErr)
	}

	second := a.Bootstrap()
	if second == nil {
		t.Fatal("second Bootstrap() = nil, want the sticky error from the first call")
	}
	if !errors.Is(second, wantErr) {
		t.Fatalf("second Bootstrap() = %v, want wrapped %v", second, wantErr)
	}
	if first.Error() != second.Error() {
		t.Errorf("second Bootstrap() error %q differs from first %q", second, first)
	}

	// The module must not have been re-registered by the second call.
	registers := 0
	for _, c := range calls {
		if c == "A:register" {
			registers++
		}
	}
	if registers != 1 {
		t.Errorf("Register called %d times, want 1 (failed bootstrap must not re-run)", registers)
	}
}

// TestRunCmd_AfterFailedBootstrap_NoPanic proves the run-command path fails
// cleanly after a failed bootstrap. Before the sticky-error latch, the second
// bootstrap() returned nil, runCmd.run proceeded with a.commands == nil, and
// a.commands.Get panicked with a nil dereference.
func TestRunCmd_AfterFailedBootstrap_NoPanic(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp() error: %v", err)
	}

	wantErr := errors.New("register boom")
	var calls []string
	a.Modules(func(r *chain.ModuleRegistry) {
		r.Add(&trackingModule{name: "A", calls: &calls, initErr: wantErr})
	})

	if err := a.Bootstrap(); !errors.Is(err, wantErr) {
		t.Fatalf("Bootstrap() = %v, want wrapped %v", err, wantErr)
	}

	runErr := runCmd{}.run(a, []string{"nothing"})
	if runErr == nil {
		t.Fatal("runCmd.run after failed bootstrap = nil, want error")
	}
	if !errors.Is(runErr, wantErr) {
		t.Errorf("runCmd.run = %v, want the sticky bootstrap error %v", runErr, wantErr)
	}
}

// TestBootstrap_WithoutEvents_SkipsEventCallbacks proves that under
// WithoutEvents (nil dispatcher) bootstrap skips the event registration
// callbacks instead of invoking them with a nil dispatcher, which would
// panic on the first d.Listen call inside consumer code.
func TestBootstrap_WithoutEvents_SkipsEventCallbacks(t *testing.T) {
	a, err := NewTestApp(WithoutEvents())
	if err != nil {
		t.Fatalf("NewTestApp(WithoutEvents()) error: %v", err)
	}

	eventsCalled := false
	a.Events(func(d events.Dispatcher) {
		eventsCalled = true
		d.Listen("x", &testListener{})
	})

	if err := a.Bootstrap(); err != nil {
		t.Fatalf("Bootstrap() error: %v", err)
	}
	if eventsCalled {
		t.Error("events callback invoked despite WithoutEvents; would panic on nil dispatcher")
	}
}
