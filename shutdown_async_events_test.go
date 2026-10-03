package velocity

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/log"
	"github.com/velocitykode/velocity/mail"
	"github.com/velocitykode/velocity/router"
)

// closingLogger is a levelLogger that records when App.Shutdown closes it.
type closingLogger struct {
	levelLogger
	closed atomic.Bool
}

func (l *closingLogger) Close() error {
	l.closed.Store(true)
	return nil
}

// observeStraggler is a module whose Shutdown, which App.Shutdown runs
// after the router stopped and before the queue, cache, DB and logger
// close, records whether a straggling request finished: the router's run
// is released as ServeHTTP returns, just before the test server's handler
// closes finished, so it waits for that (bounded); a module shut down
// before the straggler was released would see nothing and record false.
type observeStraggler struct {
	release      chan struct{}
	finished     chan struct{}
	finishedSeen atomic.Bool
}

func (m *observeStraggler) Init(*Services) error  { return nil }
func (m *observeStraggler) Start(*Services) error { return nil }
func (m *observeStraggler) Shutdown(context.Context) error {
	select {
	case <-m.release:
	default:
		return nil // shut down before the straggler was released
	}
	select {
	case <-m.finished:
		m.finishedSeen.Store(true)
	case <-time.After(5 * time.Second):
	}
	return nil
}

// TestShutdown_AsyncEventsStragglerPastDeadline asserts App.Shutdown on an
// app using SetAsyncEventDispatcher, with a handler that ignores its
// context and outlives the server's shutdown deadline: Shutdown returns at
// its deadline while the teardown waits for the straggler, which finishes
// normally once released; no later step (the modules, the logger close)
// runs before it, its events all reach the pool (none dropped), and the
// teardown then runs through to the logger close, its last step.
func TestShutdown_AsyncEventsStragglerPastDeadline(t *testing.T) {
	logs := &closingLogger{}
	const driverName = "shutdown-async-events-capture"
	prev := log.Drivers().Override(driverName, func(context.Context, log.LogConfig) (log.Logger, error) {
		return logs, nil
	})
	t.Cleanup(func() { log.Drivers().Override(driverName, prev) })

	mod := &observeStraggler{release: make(chan struct{}), finished: make(chan struct{})}
	a, err := New(WithConfig(Config{
		Env:   "testing",
		Port:  "0",
		Cache: CacheConfig{Driver: "memory", Prefix: "test_cache"},
		Log:   log.LogConfig{Driver: driverName, Config: make(map[string]any)},
		Queue: QueueConfig{Driver: "memory"},
		Mail:  mail.MailConfig{Driver: "log"},
	}), WithModules(mod))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	var (
		evMu      sync.Mutex
		delivered int
	)
	// Shutdown's ctx expires before the pool drains, so a worker can still
	// be delivering when Shutdown returns; each delivery is signalled so
	// the count is read only after both arrived.
	deliveries := make(chan struct{}, 64)
	a.Router.SetAsyncEventDispatcher(func(context.Context, interface{}) error {
		evMu.Lock()
		delivered++
		evMu.Unlock()
		deliveries <- struct{}{}
		return nil
	}, 2, 64)

	entered := make(chan struct{})
	a.Router.Get("/slow", func(c *router.Context) error {
		close(entered)
		<-mod.release // ignores its context: outlives the server drain
		return nil
	})

	var escaped atomic.Value
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(mod.finished)
		defer func() {
			if p := recover(); p != nil {
				escaped.Store(p)
			}
		}()
		a.Router.ServeHTTP(w, r)
	}))
	srv.Config.BaseContext = func(net.Listener) context.Context { return a.shutdownCtx }
	a.server = srv.Config
	srv.Start()
	t.Cleanup(srv.Close)

	go func() {
		resp, err := srv.Client().Get(srv.URL + "/slow")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	<-entered

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		_ = a.Shutdown(ctx)
	}()
	select {
	case <-shutdownDone:
	case <-time.After(10 * time.Second):
		t.Fatal("App.Shutdown never returned")
	}
	if logs.closed.Load() {
		t.Fatal("the teardown closed the logger while an admitted request still ran")
	}
	// Shutdown returned at its deadline while the teardown waits for the
	// straggler; release it, and a second Shutdown waits for that same
	// teardown to end.
	close(mod.release)
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_ = a.Shutdown(context.Background())
	}()
	select {
	case <-finished:
	case <-time.After(10 * time.Second):
		t.Fatal("the teardown never ended")
	}

	if p := escaped.Load(); p != nil {
		t.Fatalf("straggler request panicked: %v", p)
	}
	if !mod.finishedSeen.Load() {
		t.Error("a module shut down before the straggler request finished")
	}
	if got := a.FailedEventCount(); got != 0 {
		t.Errorf("failed event count = %d, want 0 (the pool outlives every admitted request)", got)
	}
	for i := 0; i < 3; i++ {
		select {
		case <-deliveries:
		case <-time.After(5 * time.Second):
			t.Fatalf("the pool delivered %d of the straggler's 3 events within 5s", i)
		}
	}
	evMu.Lock()
	if delivered != 3 {
		t.Errorf("delivered %d events, want 3 (the straggler's RequestStarted, RequestRouted and RequestHandled)", delivered)
	}
	evMu.Unlock()
	if !logs.closed.Load() {
		t.Error("App.Shutdown did not reach the logger close")
	}
}
