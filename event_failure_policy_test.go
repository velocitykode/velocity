package velocity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/app"
	"github.com/velocitykode/velocity/console"
	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/events"
	"github.com/velocitykode/velocity/internal/eventemit"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/orm"
	"github.com/velocitykode/velocity/queue"
	"github.com/velocitykode/velocity/router"
	testsync "github.com/velocitykode/velocity/testing"
)

// failingListener fails on every event it is handed.
type failingListener struct{ name string }

func (l failingListener) Handle(context.Context, interface{}) error {
	return errors.New("listener of " + l.name + " failed")
}
func (failingListener) Async() bool { return false }

// acceptingMailer accepts every message.
type acceptingMailer struct{}

func (acceptingMailer) Send(context.Context, *mail.Message) error { return nil }

// mailManagerModule registers an app-built mail manager as a component in
// Init, so the framework wires the app's dispatcher into it.
type mailManagerModule struct{ m *mail.Manager }

func (p mailManagerModule) Init(s *app.Services) error { return app.Register(s, p.m) }
func (mailManagerModule) Start(*app.Services) error    { return nil }
func (mailManagerModule) Shutdown(context.Context) error {
	return nil
}

// processedJob is a job that succeeds; ran counts its runs.
type processedJob struct {
	ID string `json:"id"`
}

var processedJobRuns atomic.Int32

func (j *processedJob) Handle() error { processedJobRuns.Add(1); return nil }
func (j *processedJob) Failed(error)  {}

// eventFailureWarns counts the warn lines in l naming event under "event".
func eventFailureWarns(l *levelLogger, event string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, e := range l.entries {
		if e.level != "warn" {
			continue
		}
		for i := 0; i+1 < len(e.kvs); i += 2 {
			if k, _ := e.kvs[i].(string); k == "event" && e.kvs[i+1] == event {
				n++
			}
		}
	}
	return n
}

// A listener that fails on a framework event of each kind (a query, a
// cache hit, a sent mail, a processed job, a handled request) produces one
// warn line per event name through the app logger, however often it fails,
// and every failure increments the app's one counter.
func TestEventListenerFailures_OneWarnPerEventName(t *testing.T) {
	queue.RegisterJob(func(data []byte) (*processedJob, error) { return &processedJob{}, nil })

	mailer := mail.NewManager()
	mailer.SetChannel("default", acceptingMailer{})
	a, capture := newLoggerWiringApp(t, func(c *Config) {
		c.DB = DBConfig{Connection: "sqlite", Database: ":memory:"}
	}, WithModules(mailManagerModule{m: mailer}))
	a.Router.Get("/ping", func(c *router.Context) error { return c.String(http.StatusOK, "ok") })
	mgr, ok := a.DB.(*orm.Manager)
	if !ok {
		t.Fatalf("a.DB = %T, want *orm.Manager", a.DB)
	}

	cases := []struct {
		event string
		fire  func(t *testing.T)
	}{
		{"orm.query.completed", func(t *testing.T) {
			if _, err := mgr.Exec(context.Background(), "SELECT 1"); err != nil {
				t.Fatalf("exec: %v", err)
			}
			if err := mgr.FlushQueryEvents(context.Background()); err != nil {
				t.Fatalf("flush: %v", err)
			}
		}},
		{"cache.hit", func(t *testing.T) {
			if err := a.Cache.Put("k", "v", time.Minute); err != nil {
				t.Fatalf("put: %v", err)
			}
			if _, ok := a.Cache.Get("k"); !ok {
				t.Fatal("cache miss, want a hit")
			}
		}},
		{"mail.completed", func(t *testing.T) {
			msg := contract.NewMessage().To("to@example.com").Subject("hi").TextBody("body")
			if err := mailer.Send(context.Background(), "default", msg); err != nil {
				t.Fatalf("send: %v", err)
			}
		}},
		{"queue.job.completed", func(t *testing.T) {
			before := processedJobRuns.Load()
			if err := a.Queue.PushCtx(context.Background(), &processedJob{ID: "j"}, "default"); err != nil {
				t.Fatalf("push: %v", err)
			}
			w := console.NewQueueWorker(a.Queue, queueWorkOptions(a, console.QueueWorkOptions{}))
			w.Start(context.Background())
			testsync.Eventually(t, func() bool { return processedJobRuns.Load() > before }, 5*time.Second, "job processed")
			w.Stop()
		}},
		{"router.request.completed", func(t *testing.T) {
			rec := httptest.NewRecorder()
			a.Router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ping", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
		}},
	}
	for _, tc := range cases {
		a.Services.Events.Listen(tc.event, failingListener{name: tc.event})
	}
	for _, tc := range cases {
		t.Run(tc.event, func(t *testing.T) {
			before := a.FailedEventCount()
			tc.fire(t)
			tc.fire(t)
			if got := eventFailureWarns(capture, tc.event); got != 1 {
				t.Errorf("warn lines naming %s = %d, want 1", tc.event, got)
			}
			if got := a.FailedEventCount() - before; got != 2 {
				t.Errorf("FailedEventCount grew by %d, want 2 (one per failed %s)", got, tc.event)
			}
		})
	}
}

// widgetSynced is an app-defined event: it does not implement
// contract.Event.
type widgetSynced struct{ ID int }

// widgetComponent is an app-built component the framework wires the
// app's dispatcher into; it fires an app-defined event.
type widgetComponent struct {
	mu       sync.Mutex
	dispatch func(ctx context.Context, event any) error
}

func (c *widgetComponent) SetEventDispatcher(fn func(ctx context.Context, event any) error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.dispatch = fn
}

func (c *widgetComponent) sync(id int) error {
	c.mu.Lock()
	fn := c.dispatch
	c.mu.Unlock()
	return fn(context.Background(), &widgetSynced{ID: id})
}

// widgetModule registers the component in Init.
type widgetModule struct{ c *widgetComponent }

func (m widgetModule) Init(s *app.Services) error   { return app.Register(s, m.c) }
func (widgetModule) Start(*app.Services) error      { return nil }
func (widgetModule) Shutdown(context.Context) error { return nil }

// hookRecorder records every call of a failure hook.
type hookRecorder struct {
	mu     sync.Mutex
	errs   []error
	events []any
}

func (h *hookRecorder) hook(err error, event any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.errs = append(h.errs, err)
	h.events = append(h.events, event)
}

func (h *hookRecorder) calls() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events)
}

// The failure hook sees an app-defined event (no contract.Event) that a
// registered component dispatched and a listener failed on, with the
// listener's error; the failure is counted once and logged once under the
// event's Go type.
func TestFailedEventHook_SeesAppDefinedEvents(t *testing.T) {
	rec := &hookRecorder{}
	comp := &widgetComponent{}
	a, capture := newLoggerWiringApp(t, nil, WithFailedEventHook(rec.hook), WithModules(widgetModule{c: comp}))
	errWidget := errors.New("widget listener failed")
	a.Services.Events.Listen(events.OfType[*widgetSynced](), listenerFunc(func(context.Context, any) error { return errWidget }))

	for i := 1; i <= 2; i++ {
		if err := comp.sync(i); !errors.Is(err, errWidget) {
			t.Fatalf("sync %d: err = %v, want the listener's error", i, err)
		}
	}
	if got := a.FailedEventCount(); got != 2 {
		t.Errorf("FailedEventCount = %d, want 2", got)
	}
	if rec.calls() != 2 {
		t.Fatalf("hook calls = %d, want 2", rec.calls())
	}
	rec.mu.Lock()
	ev, ok := rec.events[0].(*widgetSynced)
	gotErr := rec.errs[0]
	rec.mu.Unlock()
	if !ok || ev.ID != 1 {
		t.Errorf("hook event = %#v, want the app's *widgetSynced", ev)
	}
	if !errors.Is(gotErr, errWidget) {
		t.Errorf("hook err = %v, want the listener's error", gotErr)
	}
	if got := eventFailureWarns(capture, "*velocity.widgetSynced"); got != 1 {
		t.Errorf("warn lines naming *velocity.widgetSynced = %d, want 1", got)
	}
}

// A panicking hook is recovered: the app keeps serving, the panic counts
// as one more failure, and the hook is not called for its own panic.
func TestFailedEventHook_PanicIsRecoveredAndCounted(t *testing.T) {
	var calls atomic.Int32
	a, capture := newLoggerWiringApp(t, nil, WithFailedEventHook(func(error, any) {
		calls.Add(1)
		panic("hook broke")
	}))
	a.Services.Events.Listen("router.request.completed", failingListener{name: "request"})
	a.Router.Get("/ping", func(c *router.Context) error { return c.String(http.StatusOK, "ok") })

	for i := 0; i < 2; i++ {
		rec := httptest.NewRecorder()
		a.Router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/ping", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("hook calls = %d, want 2", got)
	}
	if got := a.FailedEventCount(); got != 4 {
		t.Errorf("FailedEventCount = %d, want 4 (2 listener failures + 2 hook panics)", got)
	}
	if got := capture.count("error"); got != 1 {
		t.Errorf("error lines = %d, want 1 (the first hook panic)", got)
	}
}

// WithFailedEventHook(nil) installs no hook; failures are still counted
// and logged.
func TestWithFailedEventHook_Nil(t *testing.T) {
	a, capture := newLoggerWiringApp(t, nil, WithFailedEventHook(nil))
	a.Services.Events.Listen("router.request.completed", failingListener{name: "request"})
	a.Router.Get("/ping", func(c *router.Context) error { return c.String(http.StatusOK, "ok") })
	a.Router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ping", nil))
	if got := a.FailedEventCount(); got != 1 {
		t.Errorf("FailedEventCount = %d, want 1", got)
	}
	if got := eventFailureWarns(capture, "router.request.completed"); got != 1 {
		t.Errorf("warn lines = %d, want 1", got)
	}
}

// Under the router's async pool a listener failure is recorded once: the
// app's dispatch function records it and marks it, and the pool's hand-off
// to the router's policy does not record it again.
func TestFailedEventCount_AsyncRouterPoolCountsOnce(t *testing.T) {
	rec := &hookRecorder{}
	a, _ := newLoggerWiringApp(t, nil, WithFailedEventHook(rec.hook))
	a.Services.Events.Listen("router.request.completed", failingListener{name: "request"})
	a.Router.Get("/ping", func(c *router.Context) error { return c.String(http.StatusOK, "ok") })
	a.Router.SetAsyncEventDispatcher(func(context.Context, any) error { return nil }, 2, 64)
	if err := a.Bootstrap(); err != nil { // binds the pool to the app's dispatch function
		t.Fatalf("Bootstrap: %v", err)
	}

	const requests = 5
	for i := 0; i < requests; i++ {
		a.Router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ping", nil))
	}
	if err := a.Router.ShutdownEventDispatcher(context.Background()); err != nil {
		t.Fatalf("ShutdownEventDispatcher: %v", err)
	}
	if got := a.FailedEventCount(); got != requests {
		t.Errorf("FailedEventCount = %d, want %d (one per failed event)", got, requests)
	}
	if got := rec.calls(); got != requests {
		t.Errorf("hook calls = %d, want %d", got, requests)
	}
}

// A listener failure of a detached delivery (no-queue DispatchAsync and
// DispatchAfter, a debouncing or coalescing dispatcher's later delivery),
// which no caller receives, is counted and handed to the hook once per
// delivery, and reported to the error handler once, as the dispatcher's
// AsyncFailed. A listener that fails on that AsyncFailed makes its
// delivery one more failure, counted, hooked and reported once.
func TestFailedEventCount_DetachedListenerFailureCountedOnce(t *testing.T) {
	widgetFails := func(a *App) {
		a.Services.Events.Listen(events.OfType[*widgetSynced](), listenerFunc(func(context.Context, any) error {
			return errors.New("detached listener failed")
		}))
	}
	cacheHitFails := func(a *App) {
		a.Services.Events.Listen("cache.hit", failingListener{name: "cache"})
	}
	cacheHit := func(t *testing.T, a *App) {
		if err := a.Cache.Put("k", "v", time.Minute); err != nil {
			t.Fatalf("put: %v", err)
		}
		if _, ok := a.Cache.Get("k"); !ok {
			t.Fatal("cache miss, want a hit")
		}
	}
	cases := []struct {
		name       string
		dispatcher contract.Dispatcher // installed as Services.Events when set
		listen     func(a *App)
		fire       func(t *testing.T, a *App)
		asyncFails bool // a listener of AsyncFailed fails as well
		wantEvent  string
		want       int
	}{
		{name: "DispatchAsync", listen: widgetFails, want: 1, wantEvent: "*velocity.widgetSynced",
			fire: func(t *testing.T, a *App) {
				if err := a.Services.Events.DispatchAsync(context.Background(), &widgetSynced{ID: 1}); err != nil {
					t.Fatalf("DispatchAsync: %v", err)
				}
			}},
		{name: "DispatchAfter", listen: widgetFails, want: 1, wantEvent: "*velocity.widgetSynced",
			fire: func(t *testing.T, a *App) {
				if err := a.Services.Events.DispatchAfter(context.Background(), &widgetSynced{ID: 1}, time.Millisecond); err != nil {
					t.Fatalf("DispatchAfter: %v", err)
				}
			}},
		{name: "debounced framework event", dispatcher: events.NewDebouncingDispatcher(5 * time.Millisecond),
			listen: cacheHitFails, fire: cacheHit, want: 1, wantEvent: "cache.hit"},
		{name: "coalesced framework event", dispatcher: events.NewCoalescingDispatcher(5 * time.Millisecond),
			listen: cacheHitFails, fire: cacheHit, want: 1, wantEvent: "cache.hit"},
		{name: "AsyncFailed listener fails too", listen: widgetFails, asyncFails: true, want: 2, wantEvent: "*velocity.widgetSynced",
			fire: func(t *testing.T, a *App) {
				if err := a.Services.Events.DispatchAsync(context.Background(), &widgetSynced{ID: 1}); err != nil {
					t.Fatalf("DispatchAsync: %v", err)
				}
			}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := &hookRecorder{}
			opts := []Option{WithFailedEventHook(rec.hook)}
			if tc.dispatcher != nil {
				opts = append(opts, WithModules(swapEventsModule{d: tc.dispatcher}))
			}
			a, _ := newLoggerWiringApp(t, nil, opts...)
			reports := &recordingReporter{}
			a.Services.Errors.AddReporter(reports)
			tc.listen(a)
			if tc.asyncFails {
				a.Services.Events.Listen(events.OfType[*events.AsyncFailed](), listenerFunc(func(context.Context, any) error {
					return errors.New("AsyncFailed listener failed")
				}))
			}
			before, beforeCalls := a.FailedEventCount(), rec.calls()

			tc.fire(t, a)
			want := uint64(tc.want)
			// Wait for the detached delivery, then for nothing more to arrive.
			for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
				if a.FailedEventCount()-before >= want && rec.calls()-beforeCalls >= tc.want && reports.count() >= tc.want {
					break
				}
			}
			time.Sleep(50 * time.Millisecond)
			if got := a.FailedEventCount() - before; got != want {
				t.Errorf("FailedEventCount grew by %d, want %d", got, want)
			}
			if got := rec.calls() - beforeCalls; got != tc.want {
				t.Errorf("hook calls grew by %d, want %d", got, tc.want)
			}
			if got := reports.count(); got != tc.want {
				t.Errorf("error handler reports = %d, want %d", got, tc.want)
			}
			rec.mu.Lock()
			names := map[string]int{}
			for _, ev := range rec.events[beforeCalls:] {
				names[eventemit.EventName(ev)]++
			}
			rec.mu.Unlock()
			if names[tc.wantEvent] != 1 {
				t.Errorf("hook events = %v, want %s once", names, tc.wantEvent)
			}
			if tc.asyncFails && names["events.listener.failed"] != 1 {
				t.Errorf("hook events = %v, want the AsyncFailed delivery once", names)
			}
		})
	}
}

// The app's failure line for a request event carries that request's
// request_id, trace_id and span_id, like every request-time line.
func TestEventFailureLine_CarriesTheRequestIDs(t *testing.T) {
	a, capture := newLoggerWiringApp(t, nil)
	a.Services.Events.Listen("router.request.completed", failingListener{name: "request"})
	var reqID string
	a.Router.Get("/ping", func(c *router.Context) error {
		reqID = router.GetRequestID(c.Request)
		return c.String(http.StatusOK, "ok")
	})
	a.Router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ping", nil))

	capture.mu.Lock()
	defer capture.mu.Unlock()
	found := false
	for _, e := range capture.entries {
		if e.level != "warn" {
			continue
		}
		fields := map[string]any{}
		for i := 0; i+1 < len(e.kvs); i += 2 {
			if k, ok := e.kvs[i].(string); ok {
				fields[k] = e.kvs[i+1]
			}
		}
		if fields["event"] != "router.request.completed" {
			continue
		}
		found = true
		if reqID == "" || fields["request_id"] != reqID {
			t.Errorf("request_id = %v, want the request's %q", fields["request_id"], reqID)
		}
		for _, k := range []string{"trace_id", "span_id"} {
			if s, _ := fields[k].(string); s == "" {
				t.Errorf("%s missing from the failure line: %v", k, e.kvs)
			}
		}
	}
	if !found {
		t.Fatal("no failure line naming router.request.completed")
	}
}
