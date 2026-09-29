package orm

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/orm/drivers"
)

// panickingLoggerManager returns a single-connection sqlite manager with
// the statement log on, holding a table bg with two rows, whose query
// logger panics on every statement line from then on. Every statement is
// slow, so each line is a warning, which the fallback logger writes.
func panickingLoggerManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(ManagerConfig{Driver: "sqlite", Database: ":memory:", LogQueries: true, SlowThreshold: time.Nanosecond, Logger: &levelLog{}})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	db := m.DB()
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(context.Background(), "CREATE TABLE bg (id INTEGER)"); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := db.ExecContext(context.Background(), "INSERT INTO bg (id) VALUES (1), (2)"); err != nil {
		t.Fatalf("insert: %v", err)
	}
	m.SetLogger(panickingQueryLogger{})
	return m
}

// A query logger that panics on a statement's line does not escape the
// driver callback: the statement's result reaches the caller, closing a
// result set returns the connection to the pool (a second statement on a
// one-connection pool runs), and the line lands on the fallback logger.
func TestStatementLog_PanickingLoggerIsContained(t *testing.T) {
	fallback := fallbacklogtest.Capture(t)
	m := panickingLoggerManager(t)
	db := m.DB()
	ctx := context.Background()
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("a statement's log line panicked out of database/sql: %v", p)
		}
	}()
	rows, err := db.QueryContext(ctx, "SELECT id FROM bg")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for rows.Next() {
	}
	_ = rows.Close()
	if in := db.Stats().InUse; in != 0 {
		t.Fatalf("connections in use after Close = %d, want 0: the release was skipped", in)
	}
	done := make(chan error, 1)
	go func() {
		_, err := db.ExecContext(ctx, "INSERT INTO bg (id) VALUES (3)")
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("second statement: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("second statement waited for the connection the panicking close never released")
	}
	if !strings.Contains(fallback.String(), "WARN velocity/orm: slow query") {
		t.Errorf("fallback output %q lacks the contained statement line", fallback.String())
	}
}

// A result set whose context ends while it is open is closed by
// database/sql on a goroutine of its own. A query logger that panics on
// that close's line is contained there too: the process survives, and the
// connection returns to the pool.
func TestStatementLog_PanickingLoggerOnBackgroundClose(t *testing.T) {
	fallbacklogtest.Capture(t)
	m := panickingLoggerManager(t)
	db := m.DB()
	ctx, cancel := context.WithCancel(context.Background())
	rows, err := db.QueryContext(ctx, "SELECT id FROM bg")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	cancel() // database/sql's awaitDone closes rows on its own goroutine
	deadline := time.Now().Add(3 * time.Second)
	for db.Stats().InUse != 0 {
		if time.Now().After(deadline) {
			t.Fatal("the background close did not return the connection")
		}
		time.Sleep(5 * time.Millisecond)
	}
	_ = rows.Close()
	if _, err := db.ExecContext(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("statement after the background close: %v", err)
	}
}

// panickingObserver wants every statement and panics on each.
type panickingObserver struct{}

func (panickingObserver) Observing() bool                         { return true }
func (panickingObserver) ObserveStatement(drivers.StatementEvent) { panic("observer boom") }

// A statement observer that panics is contained the same way: the
// statement succeeds, its line is still written, and the panic is reported
// through the query logger.
func TestStatementObserver_PanickingObserverIsContained(t *testing.T) {
	logs := &levelLog{}
	m, err := NewManager(ManagerConfig{Driver: "sqlite", Database: ":memory:", LogQueries: true, Logger: logs})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	m.DefaultDriver().(drivers.StatementObservable).SetStatementObserver(panickingObserver{})
	defer func() {
		if p := recover(); p != nil {
			t.Fatalf("an observer panic escaped the statement: %v", p)
		}
	}()
	before := logs.count("DEBUG velocity/orm: query executed")
	if _, err := m.DB().ExecContext(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if got := logs.count("DEBUG velocity/orm: query executed") - before; got != 1 {
		t.Errorf("statement lines = %d, want 1", got)
	}
	if got := logs.count("ERROR velocity/orm: statement observer panicked"); got != 1 {
		t.Errorf("observer panic lines = %d, want 1", got)
	}
}
