package orm

import (
	"context"
	"database/sql/driver"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"
)

var registerSleepOnce sync.Once

// registerSleep installs sleep_ms(n) on every SQLite connection opened
// afterwards: it holds the statement for n milliseconds.
func registerSleep() {
	registerSleepOnce.Do(func() {
		sqlite.MustRegisterScalarFunction("sleep_ms", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			ms, _ := args[0].(int64)
			time.Sleep(time.Duration(ms) * time.Millisecond)
			return ms, nil
		})
	})
}

// With a 100ms slow threshold, a 150ms statement writes one warn line
// through the manager's logger and its QueryExecuted says Slow; a 50ms one
// writes nothing and is not Slow. Inside Manager.Transaction too.
func TestManagerSlowThreshold_WarnsAndMarksQueryExecuted(t *testing.T) {
	registerSleep()
	m, err := NewManager(ManagerConfig{Driver: "sqlite", Database: ":memory:", MaxOpenConns: 1, SlowThreshold: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	logs := &levelLog{}
	m.SetLogger(logs)

	var (
		mu   sync.Mutex
		seen = map[string]bool{}
	)
	m.SetEventDispatcher(func(_ context.Context, ev any) error {
		if q, ok := ev.(*QueryExecuted); ok {
			mu.Lock()
			seen[q.SQL] = q.Slow
			mu.Unlock()
		}
		return nil
	})

	ctx := context.Background()
	if _, err := m.Exec(ctx, "SELECT sleep_ms(150)"); err != nil {
		t.Fatalf("slow: %v", err)
	}
	if _, err := m.Exec(ctx, "SELECT sleep_ms(50)"); err != nil {
		t.Fatalf("fast: %v", err)
	}
	if err := m.Transaction(ctx, func(ctx context.Context) error {
		_, err := m.Exec(ctx, "SELECT sleep_ms(151)")
		return err
	}); err != nil {
		t.Fatalf("transaction: %v", err)
	}
	if err := m.FlushQueryEvents(ctx); err != nil {
		t.Fatalf("FlushQueryEvents: %v", err)
	}

	if got := logs.count("WARN velocity/orm: slow query"); got != 2 {
		t.Errorf("slow query warn lines = %d, want 2 (pool and transaction): %v", got, logs.entries)
	}
	if got := len(logs.entries); got != 2 {
		t.Errorf("lines = %d, want only the two warn lines: %v", got, logs.entries)
	}
	mu.Lock()
	defer mu.Unlock()
	for query, want := range map[string]bool{
		"SELECT sleep_ms(150)": true,
		"SELECT sleep_ms(50)":  false,
		"SELECT sleep_ms(151)": true,
	} {
		slow, ok := seen[query]
		if !ok {
			t.Errorf("no QueryExecuted for %q", query)
			continue
		}
		if slow != want {
			t.Errorf("QueryExecuted{%q}.Slow = %v, want %v", query, slow, want)
		}
	}
}
