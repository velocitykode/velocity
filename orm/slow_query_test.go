package orm

import (
	"context"
	"database/sql/driver"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/velocitykode/velocity/internal/latency"
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

// With a 100ms slow threshold, a statement is Slow exactly when its own
// measured Duration exceeds the threshold, and each Slow statement writes
// one warn line through the manager's logger. A 150ms statement is always
// Slow, in the pool and inside Manager.Transaction; a 50ms one is classified
// by the time it actually took, so a scheduler pause cannot fail the test.
func TestManagerSlowThreshold_WarnsAndMarksQueryExecuted(t *testing.T) {
	registerSleep()
	const threshold = 100 * time.Millisecond
	m, err := NewManager(ManagerConfig{Driver: "sqlite", Database: ":memory:", MaxOpenConns: 1, SlowThreshold: threshold})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	logs := &levelLog{}
	m.SetLogger(logs)

	var (
		mu   sync.Mutex
		seen = map[string]*QueryExecuted{}
	)
	m.SetEventDispatcher(func(_ context.Context, ev any) error {
		if q, ok := ev.(*QueryExecuted); ok {
			mu.Lock()
			seen[q.SQL] = q
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

	mu.Lock()
	defer mu.Unlock()
	slow := 0
	for query, alwaysSlow := range map[string]bool{
		"SELECT sleep_ms(150)": true,
		"SELECT sleep_ms(50)":  false,
		"SELECT sleep_ms(151)": true,
	} {
		q, ok := seen[query]
		if !ok {
			t.Errorf("no QueryExecuted for %q", query)
			continue
		}
		if want := latency.Slow(q.Duration, threshold); q.Slow != want {
			t.Errorf("QueryExecuted{%q}.Slow = %v for Duration %v, want %v", query, q.Slow, q.Duration, want)
		}
		if alwaysSlow && !q.Slow {
			t.Errorf("QueryExecuted{%q}.Slow = false, want true", query)
		}
		if q.Slow {
			slow++
		}
	}
	if got := logs.count("WARN velocity/orm: slow query"); got != slow {
		t.Errorf("slow query warn lines = %d, want one per Slow statement (%d): %v", got, slow, logs.entries)
	}
	if got := len(logs.entries); got != slow {
		t.Errorf("lines = %d, want only the %d warn lines: %v", got, slow, logs.entries)
	}
}
