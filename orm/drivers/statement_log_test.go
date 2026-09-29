package drivers

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"modernc.org/sqlite"

	"github.com/velocitykode/velocity/contract"
	"github.com/velocitykode/velocity/internal/fallbacklog/fallbacklogtest"
	"github.com/velocitykode/velocity/trace"
)

// Values a statement binds or a failing statement's error echoes. No log
// line may carry any of them.
const (
	secretArg = "hunter2-secret-value"
	personArg = "alice@example.com"
)

var registerTestFunctions sync.Once

// registerStatementFunctions installs two SQLite functions on every
// connection opened afterwards: sleep_ms(n) holds the statement for n
// milliseconds and returns n; fail_with(v) fails the statement with an
// error that echoes v, the way database errors echo a bound value.
func registerStatementFunctions(t *testing.T) {
	t.Helper()
	registerTestFunctions.Do(func() {
		sqlite.MustRegisterScalarFunction("sleep_ms", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			ms, _ := args[0].(int64)
			time.Sleep(time.Duration(ms) * time.Millisecond)
			return ms, nil
		})
		sqlite.MustRegisterScalarFunction("fail_with", 1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
			return nil, fmt.Errorf("rejected value %v", args[0])
		})
	})
}

// connectStatementSQLite opens an in-memory SQLite pool with the test
// functions, the given query-log settings and a recording logger.
func connectStatementSQLite(t *testing.T, logQueries bool, slow time.Duration) (Driver, *queryLog) {
	t.Helper()
	registerStatementFunctions(t)
	d := NewSQLiteDriver()
	if err := d.Connect(ConnectionConfig{Database: ":memory:", MaxOpenConns: 1, LogQueries: logQueries, SlowThreshold: slow}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	log := &queryLog{}
	d.(contract.LoggerAware).SetLogger(log)
	return d, log
}

// kv returns the value logged under key, or nil.
func kv(kvs []any, key string) any {
	v, _ := kvOK(kvs, key)
	return v
}

// kvOK returns key's value and whether the pairs carry key at all.
func kvOK(kvs []any, key string) (any, bool) {
	for i := 0; i+1 < len(kvs); i += 2 {
		if kvs[i] == key {
			return kvs[i+1], true
		}
	}
	return nil, false
}

// assertNoValues fails when any entry's message or key-value carries one
// of the bound or echoed values.
func assertNoValues(t *testing.T, entries []queryLogEntry) {
	t.Helper()
	for _, e := range entries {
		line := e.msg + " " + fmt.Sprint(e.kvs...)
		for _, v := range []string{secretArg, personArg, "rejected value"} {
			if strings.Contains(line, v) {
				t.Errorf("%s line carries %q: %s", e.level, v, line)
			}
		}
	}
}

// Every route into an instrumented pool writes one debug line per
// statement with its text and argument count: the pool, a transaction, a
// prepared statement and the raw *sql.DB alike.
func TestStatementLog_EveryRouteWritesOneDebugLine(t *testing.T) {
	ctx := context.Background()
	routes := []struct {
		name  string
		query string
		args  int
		run   func(t *testing.T, d Driver, query string)
	}{
		{name: "pool exec", query: "INSERT INTO people (name) VALUES (?)", args: 1, run: func(t *testing.T, d Driver, q string) {
			if _, err := d.ExecContext(ctx, q, secretArg); err != nil {
				t.Fatalf("exec: %v", err)
			}
		}},
		{name: "pool query", query: "SELECT name FROM people WHERE name = ?", args: 1, run: func(t *testing.T, d Driver, q string) {
			rows, err := d.QueryContext(ctx, q, secretArg)
			if err != nil {
				t.Fatalf("query: %v", err)
			}
			_ = rows.Close()
		}},
		{name: "pool query row", query: "SELECT count(*) FROM people WHERE name <> ?", args: 1, run: func(t *testing.T, d Driver, q string) {
			var n int
			if err := d.QueryRowContext(ctx, q, personArg).Scan(&n); err != nil {
				t.Fatalf("query row: %v", err)
			}
		}},
		{name: "raw pool", query: "UPDATE people SET name = ? WHERE name = ?", args: 2, run: func(t *testing.T, d Driver, q string) {
			if _, err := d.DB().ExecContext(ctx, q, personArg, secretArg); err != nil {
				t.Fatalf("raw exec: %v", err)
			}
		}},
		{name: "transaction exec", query: "DELETE FROM people WHERE name = ?", args: 1, run: func(t *testing.T, d Driver, q string) {
			tx, err := d.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			if _, err := tx.ExecContext(ctx, q, personArg); err != nil {
				t.Fatalf("tx exec: %v", err)
			}
			if err := tx.Commit(); err != nil {
				t.Fatalf("commit: %v", err)
			}
		}},
		{name: "transaction query", query: "SELECT name FROM people WHERE name = ?", args: 1, run: func(t *testing.T, d Driver, q string) {
			tx, err := d.BeginTx(ctx, nil)
			if err != nil {
				t.Fatalf("begin: %v", err)
			}
			defer func() { _ = tx.Rollback() }()
			rows, err := tx.QueryContext(ctx, q, secretArg)
			if err != nil {
				t.Fatalf("tx query: %v", err)
			}
			_ = rows.Close()
		}},
		{name: "prepared exec", query: "INSERT INTO people (name) VALUES (?)", args: 1, run: func(t *testing.T, d Driver, q string) {
			stmt, err := d.DB().PrepareContext(ctx, q)
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			defer func() { _ = stmt.Close() }()
			if _, err := stmt.ExecContext(ctx, secretArg); err != nil {
				t.Fatalf("stmt exec: %v", err)
			}
		}},
		{name: "prepared query", query: "SELECT name FROM people WHERE name = ?", args: 1, run: func(t *testing.T, d Driver, q string) {
			stmt, err := d.DB().PrepareContext(ctx, q)
			if err != nil {
				t.Fatalf("prepare: %v", err)
			}
			defer func() { _ = stmt.Close() }()
			rows, err := stmt.QueryContext(ctx, personArg)
			if err != nil {
				t.Fatalf("stmt query: %v", err)
			}
			_ = rows.Close()
		}},
	}
	for _, tt := range routes {
		t.Run(tt.name, func(t *testing.T) {
			d, log := connectStatementSQLite(t, true, 0)
			if _, err := d.ExecContext(ctx, "CREATE TABLE people (name TEXT)"); err != nil {
				t.Fatalf("create: %v", err)
			}
			before := len(log.all())

			tt.run(t, d, tt.query)

			got := log.all()[before:]
			if len(got) != 1 {
				t.Fatalf("lines = %d, want 1: %+v", len(got), got)
			}
			e := got[0]
			if e.level != "debug" || e.msg != "velocity/orm: query executed" {
				t.Errorf("line = %s %q, want debug %q", e.level, e.msg, "velocity/orm: query executed")
			}
			if kv(e.kvs, "query") != tt.query || kv(e.kvs, "arg_count") != tt.args {
				t.Errorf("kvs = %v, want query %q arg_count %d", e.kvs, tt.query, tt.args)
			}
			if _, ok := kv(e.kvs, "duration_ms").(int64); !ok {
				t.Errorf("duration_ms = %#v, want an int64", kv(e.kvs, "duration_ms"))
			}
			assertNoValues(t, got)
		})
	}
}

// A failed statement writes one debug line naming the statement, and
// neither its argument values nor the error text, which can echo them.
func TestStatementLog_FailedStatementWritesNoValues(t *testing.T) {
	d, log := connectStatementSQLite(t, true, 0)
	ctx := context.Background()

	if _, err := d.ExecContext(ctx, "SELECT fail_with(?)", secretArg); err == nil {
		t.Fatal("statement succeeded, want the fail_with error")
	}
	rows, err := d.QueryContext(ctx, "SELECT fail_with(?)", personArg)
	if err == nil {
		for rows.Next() {
		}
		err = rows.Err()
		_ = rows.Close()
	}
	if err == nil {
		t.Fatal("query succeeded, want the fail_with error")
	}

	got := log.all()
	if len(got) != 2 {
		t.Fatalf("lines = %d, want 2: %+v", len(got), got)
	}
	for _, e := range got {
		if e.level != "debug" || e.msg != "velocity/orm: query failed" {
			t.Errorf("line = %s %q, want debug %q", e.level, e.msg, "velocity/orm: query failed")
		}
		if kv(e.kvs, "query") != "SELECT fail_with(?)" || kv(e.kvs, "arg_count") != 1 {
			t.Errorf("kvs = %v, want the statement and arg_count 1", e.kvs)
		}
	}
	assertNoValues(t, got)
}

// The slow rule: a completed statement slower than the threshold writes
// one warn line (statement, duration, argument count, request ids; no
// values), in place of its debug line; a faster one, a failed one and any
// statement under a zero threshold write none.
func TestSlowStatement_WarnsOnceWithoutValues(t *testing.T) {
	ctx := trace.WithRequestID(trace.WithFullContext(context.Background(), "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7", ""), "req-slow-1")
	tests := []struct {
		name       string
		logQueries bool
		threshold  time.Duration
		query      string
		wantLevel  string
	}{
		{name: "slow", threshold: 100 * time.Millisecond, query: "SELECT sleep_ms(150), ?", wantLevel: "warn"},
		{name: "slow with query log on", logQueries: true, threshold: 100 * time.Millisecond, query: "SELECT sleep_ms(150), ?", wantLevel: "warn"},
		{name: "fast", threshold: 100 * time.Millisecond, query: "SELECT sleep_ms(50), ?"},
		{name: "fast with query log on", logQueries: true, threshold: 100 * time.Millisecond, query: "SELECT sleep_ms(50), ?", wantLevel: "debug"},
		{name: "zero threshold", query: "SELECT sleep_ms(150), ?"},
		{name: "slow failure", threshold: 100 * time.Millisecond, query: "SELECT sleep_ms(150), fail_with(?)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, log := connectStatementSQLite(t, tt.logQueries, tt.threshold)
			rows, err := d.QueryContext(ctx, tt.query, secretArg)
			if err == nil {
				for rows.Next() {
				}
				_ = rows.Close()
			}

			got := log.all()
			if tt.wantLevel == "" {
				if len(got) != 0 {
					t.Fatalf("lines = %+v, want none", got)
				}
				return
			}
			if len(got) != 1 {
				t.Fatalf("lines = %d, want 1: %+v", len(got), got)
			}
			e := got[0]
			if e.level != tt.wantLevel {
				t.Errorf("level = %s, want %s", e.level, tt.wantLevel)
			}
			if tt.wantLevel == "warn" {
				if e.msg != "velocity/orm: slow query" {
					t.Errorf("msg = %q, want %q", e.msg, "velocity/orm: slow query")
				}
				if ms, _ := kv(e.kvs, "duration_ms").(int64); ms < 150 {
					t.Errorf("duration_ms = %v, want at least 150", kv(e.kvs, "duration_ms"))
				}
				for key, want := range map[string]any{
					"query":      tt.query,
					"arg_count":  1,
					"connection": "sqlite",
					"request_id": "req-slow-1",
					"trace_id":   "4bf92f3577b34da6a3ce929d0e0e4736",
					"span_id":    "00f067aa0ba902b7",
				} {
					if got := kv(e.kvs, key); got != want {
						t.Errorf("%s = %#v, want %#v", key, got, want)
					}
				}
			}
			assertNoValues(t, got)
		})
	}
}

// statementRecorder is a StatementObserver that keeps every event.
type statementRecorder struct {
	mu     sync.Mutex
	events []StatementEvent
}

func (r *statementRecorder) Observing() bool { return true }

func (r *statementRecorder) ObserveStatement(ev StatementEvent) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
}

func (r *statementRecorder) all() []StatementEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]StatementEvent(nil), r.events...)
}

// The observer's event says whether the statement was slow by the same
// rule the warn line follows.
func TestSlowStatement_MarksTheStatementEvent(t *testing.T) {
	tests := []struct {
		name      string
		threshold time.Duration
		query     string
		want      bool
	}{
		{name: "slow", threshold: 100 * time.Millisecond, query: "SELECT sleep_ms(150)", want: true},
		{name: "fast", threshold: 100 * time.Millisecond, query: "SELECT sleep_ms(50)"},
		{name: "zero threshold", query: "SELECT sleep_ms(150)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, _ := connectStatementSQLite(t, false, tt.threshold)
			rec := &statementRecorder{}
			d.(StatementObservable).SetStatementObserver(rec)
			if _, err := d.ExecContext(context.Background(), tt.query); err != nil {
				t.Fatalf("exec: %v", err)
			}
			evs := rec.all()
			if len(evs) != 1 {
				t.Fatalf("events = %d, want 1", len(evs))
			}
			if evs[0].Slow != tt.want {
				t.Errorf("Slow = %v, want %v (duration %v)", evs[0].Slow, tt.want, evs[0].Duration)
			}
		})
	}
}

// Without a logger (never set, or reset with nil) a statement goes to the
// framework's standalone fallback logger: it drops the debug line and
// writes a slow statement's warn line to standard error, without values.
func TestStatementLog_WithoutLoggerUsesTheFallback(t *testing.T) {
	for _, reset := range []bool{false, true} {
		t.Run(fmt.Sprintf("reset=%v", reset), func(t *testing.T) {
			registerStatementFunctions(t)
			fallback := fallbacklogtest.Capture(t)
			d := NewSQLiteDriver()
			if err := d.Connect(ConnectionConfig{Database: ":memory:", MaxOpenConns: 1, LogQueries: true, SlowThreshold: 100 * time.Millisecond}); err != nil {
				t.Fatalf("Connect: %v", err)
			}
			t.Cleanup(func() { _ = d.Close() })
			if reset {
				d.(contract.LoggerAware).SetLogger(&queryLog{})
				d.(contract.LoggerAware).SetLogger(nil)
			}
			ctx := context.Background()
			if _, err := d.ExecContext(ctx, "SELECT ?", secretArg); err != nil {
				t.Fatalf("fast: %v", err)
			}
			if _, err := d.ExecContext(ctx, "SELECT sleep_ms(150), ?", secretArg); err != nil {
				t.Fatalf("slow: %v", err)
			}
			out := fallback.String()
			if n := strings.Count(out, "\n"); n != 1 || !strings.Contains(out, "WARN velocity/orm: slow query") {
				t.Errorf("fallback = %q, want exactly the slow query's warn line", out)
			}
			if strings.Contains(out, secretArg) {
				t.Errorf("fallback line carries the bound value: %q", out)
			}
		})
	}
}

// With no observer and neither query logging nor a slow threshold, a
// statement is not timed and nothing is recorded or logged.
func TestStatementLog_OffWritesNothing(t *testing.T) {
	d, log := connectStatementSQLite(t, false, 0)
	if _, err := d.ExecContext(context.Background(), "SELECT sleep_ms(1)"); err != nil {
		t.Fatalf("exec: %v", err)
	}
	if got := log.all(); len(got) != 0 {
		t.Errorf("lines = %+v, want none", got)
	}
}

// SetLogger may swap the logger while many goroutines run slow and fast
// statements through every route. Run under -race.
func TestStatementLog_SetLoggerWhileLoggingIsSafe(t *testing.T) {
	registerStatementFunctions(t)
	d := NewSQLiteDriver()
	if err := d.Connect(ConnectionConfig{Database: ":memory:", MaxOpenConns: 4, LogQueries: true, SlowThreshold: time.Millisecond}); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	aware := d.(contract.LoggerAware)
	ctx := context.Background()

	var wg sync.WaitGroup
	errs := make(chan error, 64)
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				query := "SELECT sleep_ms(?)"
				if _, err := d.ExecContext(ctx, query, int64(i%3)); err != nil {
					errs <- err
					return
				}
				if g%2 == 0 {
					tx, err := d.BeginTx(ctx, nil)
					if err != nil {
						errs <- err
						return
					}
					if _, err := tx.ExecContext(ctx, "SELECT ?", i); err != nil {
						errs <- err
					}
					_ = tx.Rollback()
				}
			}
		}(g)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			if i%3 == 0 {
				aware.SetLogger(nil)
				continue
			}
			aware.SetLogger(&queryLog{})
		}
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, context.Canceled) {
			t.Errorf("statement: %v", err)
		}
	}
}

// Edge inputs: SetLogger on a zero BaseDriver and after Close, a statement
// after Close, and a binding with no owning driver (its lines go to the
// fallback) never panic, and a closed pool writes no line.
func TestStatementLog_EdgeInputs(t *testing.T) {
	var zero BaseDriver
	zero.SetLogger(nil)
	zero.SetLogger(&queryLog{})

	d, log := connectStatementSQLite(t, true, time.Millisecond)
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	d.(contract.LoggerAware).SetLogger(log)
	if _, err := d.ExecContext(context.Background(), "SELECT 1"); err == nil {
		t.Error("statement after Close succeeded")
	}
	if got := log.all(); len(got) != 0 {
		t.Errorf("lines after Close = %+v, want none", got)
	}

	fallback := fallbacklogtest.Capture(t)
	ownerless := &observerBinding{name: "sqlite", slowThreshold: time.Millisecond}
	ownerless.record(nil, StatementEvent{Context: context.Background(), Connection: "sqlite", SQL: "SELECT ?", Duration: 2 * time.Millisecond}, 1)
	ownerless.record(nil, StatementEvent{Context: context.Background(), Connection: "sqlite", SQL: "SELECT ?", Duration: time.Millisecond}, 1)
	ownerless.record(nil, StatementEvent{Context: nil, Connection: "sqlite", SQL: "SELECT ?", Duration: time.Hour, Err: driver.ErrSkip}, 1)
	out := fallback.String()
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, "WARN velocity/orm: slow query") || !strings.Contains(out, "duration_ms=2") {
		t.Errorf("fallback = %q, want one slow query line for the 2ms statement only", out)
	}
}
