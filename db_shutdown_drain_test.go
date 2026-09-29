package velocity

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/velocitykode/velocity/orm"
)

// An App whose Shutdown ctx ends before the ORM has delivered its queued
// statement events reports that, and still tears everything down: the
// error comes back from Shutdown, nothing panics, and the database is
// closed.
func TestAppShutdown_ReportsAnUnfinishedQueryEventDrain(t *testing.T) {
	a, _ := newLoggerWiringApp(t, func(c *Config) {
		c.DB = DBConfig{Connection: "sqlite", Database: ":memory:"}
	})
	gate := make(chan struct{})
	var once, first sync.Once
	release := func() { once.Do(func() { close(gate) }) }
	defer release()
	entered := make(chan struct{})
	a.Services.Events.Listen("orm.query.completed", listenerFunc(func(context.Context, any) error {
		first.Do(func() { close(entered) })
		<-gate
		return nil
	}))
	for i := 0; i < 3; i++ {
		if _, err := a.DB.Exec(context.Background(), "SELECT 1"); err != nil {
			t.Fatalf("exec: %v", err)
		}
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("no statement event reached the listener")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.Shutdown(ctx) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("App.Shutdown did not return")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("App.Shutdown = %v, want the unfinished drain's context.DeadlineExceeded", err)
	}
	if err := a.DB.Ping(); !errors.Is(err, orm.ErrManagerShutdown) {
		t.Errorf("after App.Shutdown: DB.Ping = %v, want orm.ErrManagerShutdown (teardown went on)", err)
	}
}
