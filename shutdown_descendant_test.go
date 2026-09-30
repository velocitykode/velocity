package velocity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/router"
	"github.com/velocitykode/velocity/scheduler"
)

// An App.Shutdown called from a router event listener runs on a worker the
// teardown's event-dispatcher drain waits for: it returns an error wrapping
// contract.ErrStopFromOwnWork at once instead of waiting on itself until
// its ctx, starts nothing, and a Shutdown from outside then completes.
func TestShutdown_FromARouterListenerDoesNotWaitOnItself(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	queue := &queueCloseProbe{QueueDriver: a.Queue}
	a.Queue = queue
	inner := make(chan error, 1)
	var once sync.Once
	a.Router.SetAsyncEventDispatcher(func(context.Context, interface{}) error {
		once.Do(func() {
			ctx, cancel := context.WithTimeout(context.Background(), hostile.Deadline)
			defer cancel()
			inner <- a.Shutdown(ctx)
		})
		return nil
	}, 1, 8)
	a.Router.Get("/", func(c *router.Context) error { return c.String(http.StatusOK, "ok") })
	a.Router.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))

	var got error
	hostile.Within(t, hostile.Deadline, func() { got = <-inner })
	if !errors.Is(got, contract.ErrStopFromOwnWork) {
		t.Fatalf("Shutdown from a listener = %v, want an error wrapping contract.ErrStopFromOwnWork", got)
	}
	// Refused, it changed nothing: the teardown did not start.
	a.teardown.mu.Lock()
	started := a.teardown.run != nil
	a.teardown.mu.Unlock()
	if started || queue.shutdowns.Load() != 0 {
		t.Fatalf("a refused Shutdown started the teardown (queue closes %d)", queue.shutdowns.Load())
	}
	hostile.Within(t, hostile.Deadline, func() { err = a.Shutdown(context.Background()) })
	if err != nil {
		t.Fatalf("Shutdown from outside = %v", err)
	}
}

// shutdownLineLogger calls fn from the scheduler's "Scheduler shutting
// down" line, which the scheduler's stop writes on a goroutine of its own.
type shutdownLineLogger struct {
	contract.Logger
	once sync.Once
	fn   func()
}

func (l *shutdownLineLogger) Info(msg string, kvs ...any) {
	if msg == "Scheduler shutting down" {
		l.once.Do(l.fn)
	}
}

func (l *shutdownLineLogger) With(...any) contract.Logger { return l }

// The same from the scheduler's own stop line, written while the teardown
// waits for the scheduler.
func TestShutdown_FromTheSchedulersStopLineDoesNotWaitOnItself(t *testing.T) {
	a, err := NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	s, ok := a.Scheduler.(*scheduler.Scheduler)
	if !ok {
		t.Fatalf("Scheduler is %T", a.Scheduler)
	}
	inner := make(chan error, 1)
	s.SetLogger(&shutdownLineLogger{Logger: a.Log, fn: func() {
		ctx, cancel := context.WithTimeout(context.Background(), hostile.Deadline)
		defer cancel()
		inner <- a.Shutdown(ctx)
	}})
	ticked := make(chan struct{})
	var tick sync.Once
	s.Named("descendant.tick", func() { tick.Do(func() { close(ticked) }) }).Cron("* * * * *")
	ran := make(chan error, 1)
	go func() { ran <- s.Run(context.Background()) }()
	<-ticked

	hostile.Within(t, hostile.Deadline, func() { err = a.Shutdown(context.Background()) })
	if err != nil {
		t.Fatalf("Shutdown = %v", err)
	}
	var got error
	hostile.Within(t, hostile.Deadline, func() { got = <-inner })
	if !errors.Is(got, contract.ErrStopFromOwnWork) {
		t.Fatalf("Shutdown from the scheduler's stop line = %v, want an error wrapping contract.ErrStopFromOwnWork", got)
	}
	<-ran
}
