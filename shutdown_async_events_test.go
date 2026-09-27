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

// releaseOnShutdown is a module whose Shutdown, which App.Shutdown runs
// after the event dispatcher stopped and before the queue, cache, DB and
// logger close, releases a straggling handler and waits for its request
// to finish, so the request's events are dispatched in the middle of
// App.Shutdown.
type releaseOnShutdown struct {
	release  chan struct{}
	finished chan struct{}
	waited   atomic.Bool
}

func (m *releaseOnShutdown) Init(*Services) error  { return nil }
func (m *releaseOnShutdown) Start(*Services) error { return nil }
func (m *releaseOnShutdown) Shutdown(context.Context) error {
	close(m.release)
	select {
	case <-m.finished:
		m.waited.Store(true)
	case <-time.After(5 * time.Second):
	}
	return nil
}

// TestShutdown_AsyncEventsStragglerPastDeadline asserts App.Shutdown on an
// app using SetAsyncEventDispatcher, with a handler that ignores its
// context and outlives the server's shutdown deadline: the straggler's
// request finishes normally when released after the event pool stopped
// (its RequestHandled is a counted drop, not a send on a closed channel),
// and Shutdown runs through to the logger close, its last step.
func TestShutdown_AsyncEventsStragglerPastDeadline(t *testing.T) {
	logs := &closingLogger{}
	const driverName = "shutdown-async-events-capture"
	prev := log.Drivers().Override(driverName, func(context.Context, log.LogConfig) (log.Logger, error) {
		return logs, nil
	})
	t.Cleanup(func() { log.Drivers().Override(driverName, prev) })

	mod := &releaseOnShutdown{release: make(chan struct{}), finished: make(chan struct{})}
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
	a.Router.SetAsyncEventDispatcher(func(context.Context, interface{}) error {
		evMu.Lock()
		delivered++
		evMu.Unlock()
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

	if p := escaped.Load(); p != nil {
		t.Fatalf("straggler request panicked after the event pool stopped: %v", p)
	}
	if !mod.waited.Load() {
		t.Error("straggler request did not finish while App.Shutdown was running")
	}
	if got := a.Router.DroppedEventCount(); got != 1 {
		t.Errorf("DroppedEventCount = %d, want 1 (the straggler's late RequestHandled)", got)
	}
	evMu.Lock()
	if delivered != 2 {
		t.Errorf("delivered %d events, want 2 (the straggler's RequestStarted and RequestRouted, queued before the stop)", delivered)
	}
	evMu.Unlock()
	if !logs.closed.Load() {
		t.Error("App.Shutdown did not reach the logger close")
	}
}
