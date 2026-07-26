package orm

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stmtCollector records QueryExecuted / QueryFailed events dispatched by the
// driver-level instrumentation.
type stmtCollector struct {
	m        *Manager
	mu       sync.Mutex
	executed []*QueryExecuted
	failed   []*QueryFailed
}

// flush forces delivery of every statement event recorded so far. Statement
// events are handed to a delivery goroutine from inside the driver callback,
// so an assertion made straight after a query would otherwise race it.
func (c *stmtCollector) flush() {
	if c.m != nil {
		_ = c.m.FlushQueryEvents(context.Background())
	}
}

func (c *stmtCollector) dispatch(_ context.Context, ev any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch e := ev.(type) {
	case *QueryExecuted:
		c.executed = append(c.executed, e)
	case *QueryFailed:
		c.failed = append(c.failed, e)
	}
	return nil
}

func (c *stmtCollector) reset() {
	c.flush()
	c.mu.Lock()
	defer c.mu.Unlock()
	c.executed = nil
	c.failed = nil
}

// matching returns every executed event whose SQL contains needle
// (case-insensitive).
func (c *stmtCollector) matching(needle string) []*QueryExecuted {
	c.flush()
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []*QueryExecuted
	for _, e := range c.executed {
		if strings.Contains(strings.ToUpper(e.SQL), strings.ToUpper(needle)) {
			out = append(out, e)
		}
	}
	return out
}

func (c *stmtCollector) failures() []*QueryFailed {
	c.flush()
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*QueryFailed, len(c.failed))
	copy(out, c.failed)
	return out
}

// setupInstrumentedTest returns a manager with the collector wired and the
// test_users table created, with the collector already reset so setup
// statements do not pollute assertions.
func setupInstrumentedTest(t *testing.T) (*Manager, *stmtCollector) {
	t.Helper()
	m := setupConvenienceTests(t)
	c := &stmtCollector{m: m}
	m.SetEventDispatcher(c.dispatch)
	c.reset()
	return m, c
}

// TestInstrumentation_RawSQLDBEmitsEvents covers the path that motivated
// moving instrumentation into the driver: a subsystem holding the raw *sql.DB
// pulled out of the manager, which never touches drivers.Driver or the query
// builder. This was the shape of the old raw-SQL auth provider, whose login query ran on
// every authentication attempt and emitted nothing.
func TestInstrumentation_RawSQLDBEmitsEvents(t *testing.T) {
	m, c := setupInstrumentedTest(t)
	seedUser(t, m, "alice", "a@example.com", 20)
	c.reset()

	db := m.DB()
	var email string
	if err := db.QueryRowContext(context.Background(),
		"SELECT email FROM test_users WHERE name = ?", "alice").Scan(&email); err != nil {
		t.Fatalf("QueryRowContext: %v", err)
	}
	if email != "a@example.com" {
		t.Fatalf("email = %q", email)
	}

	events := c.matching("SELECT email FROM test_users")
	if len(events) != 1 {
		t.Fatalf("want 1 QueryExecuted for the raw pool read, got %d", len(events))
	}
	ev := events[0]
	if ev.Connection != "sqlite" {
		t.Errorf("Connection = %q, want sqlite", ev.Connection)
	}
	if ev.RowsAffected != 1 {
		t.Errorf("RowsAffected = %d, want 1", ev.RowsAffected)
	}
	if ev.Duration <= 0 {
		t.Errorf("Duration = %v, want > 0", ev.Duration)
	}
	if !strings.HasSuffix(ev.File, "_test.go") || ev.Line == 0 {
		t.Errorf("caller frame did not resolve to the caller: file=%q line=%d", ev.File, ev.Line)
	}
	if len(ev.Bindings) != 1 || ev.Bindings[0] != "alice" {
		t.Errorf("Bindings = %#v, want [alice]", ev.Bindings)
	}
}

// TestInstrumentation_ManagerRawAndExecEmitEvents covers Manager.Raw and
// Manager.Exec, neither of which dispatched an event before instrumentation
// moved into the driver.
func TestInstrumentation_ManagerRawAndExecEmitEvents(t *testing.T) {
	m, c := setupInstrumentedTest(t)
	seedUser(t, m, "alice", "a@example.com", 20)
	c.reset()

	ctx := context.Background()
	rows, err := m.Raw(ctx, "SELECT name FROM test_users WHERE age > ?", 10)
	if err != nil {
		t.Fatalf("Raw: %v", err)
	}
	count := 0
	for rows.Next() {
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	// The event for a read fires when the result set closes, which is what
	// lets it carry the consumed row count.
	if err := rows.Close(); err != nil {
		t.Fatalf("rows.Close: %v", err)
	}

	raw := c.matching("SELECT name FROM test_users")
	if len(raw) != 1 {
		t.Fatalf("want 1 QueryExecuted for Manager.Raw, got %d", len(raw))
	}
	if raw[0].RowsAffected != int64(count) {
		t.Errorf("Raw RowsAffected = %d, want %d", raw[0].RowsAffected, count)
	}

	if _, err := m.Exec(ctx, "UPDATE test_users SET age = ? WHERE age > ?", 41, 10); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	upd := c.matching("UPDATE test_users")
	if len(upd) != 1 {
		t.Fatalf("want 1 QueryExecuted for Manager.Exec, got %d", len(upd))
	}
	if upd[0].RowsAffected != 1 {
		t.Errorf("Exec RowsAffected = %d, want 1", upd[0].RowsAffected)
	}
}

// TestInstrumentation_TxBranchEmitsEvents covers the Manager.Raw / Manager.Exec
// branches that reach *sql.Tx directly, bypassing drivers.Driver entirely.
func TestInstrumentation_TxBranchEmitsEvents(t *testing.T) {
	m, c := setupInstrumentedTest(t)
	seedUser(t, m, "alice", "a@example.com", 20)
	c.reset()

	err := m.Transaction(context.Background(), func(txCtx context.Context) error {
		if _, err := m.Exec(txCtx, "UPDATE test_users SET age = ? WHERE name = ?", 33, "alice"); err != nil {
			return err
		}
		rows, err := m.Raw(txCtx, "SELECT age FROM test_users WHERE name = ?", "alice")
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var age int
			if err := rows.Scan(&age); err != nil {
				return err
			}
			if age != 33 {
				t.Errorf("tx read saw age = %d, want 33 (uncommitted write)", age)
			}
		}
		return rows.Err()
	})
	if err != nil {
		t.Fatalf("Transaction: %v", err)
	}

	if got := len(c.matching("UPDATE test_users")); got != 1 {
		t.Errorf("want 1 QueryExecuted for the in-tx Exec, got %d", got)
	}
	if got := len(c.matching("SELECT age FROM test_users")); got != 1 {
		t.Errorf("want 1 QueryExecuted for the in-tx Raw, got %d", got)
	}
}

// TestInstrumentation_QueryFailedFires locks in that query.failed reaches the
// dispatcher at all: before instrumentation moved into the driver, QueryFailed
// was declared but never constructed, and failing statements were reported as
// successes with a zero row count.
func TestInstrumentation_QueryFailedFires(t *testing.T) {
	m, c := setupInstrumentedTest(t)

	_, err := m.Exec(context.Background(), "UPDATE no_such_table SET age = 1")
	if err == nil {
		t.Fatal("expected the statement to fail")
	}

	failures := c.failures()
	if len(failures) != 1 {
		t.Fatalf("want 1 QueryFailed, got %d", len(failures))
	}
	f := failures[0]
	if !strings.Contains(f.Query, "no_such_table") {
		t.Errorf("Query = %q, want the failing statement", f.Query)
	}
	if f.Error == "" {
		t.Error("Error is empty")
	}
	if f.Connection != "sqlite" {
		t.Errorf("Connection = %q, want sqlite", f.Connection)
	}
	if f.At.IsZero() {
		t.Error("At is zero")
	}
	if !strings.HasSuffix(f.File, "_test.go") || f.Line == 0 {
		t.Errorf("caller frame did not resolve: file=%q line=%d", f.File, f.Line)
	}
	// A failure must not report success alongside it.
	if got := len(c.matching("no_such_table")); got != 0 {
		t.Errorf("failing statement also emitted %d QueryExecuted events", got)
	}
}

// TestInstrumentation_FailedReadCarriesNoBindings locks in that a failed
// statement never records bound values.
func TestInstrumentation_FailedReadCarriesNoBindings(t *testing.T) {
	m, c := setupInstrumentedTest(t)

	_, err := m.Raw(context.Background(), "SELECT * FROM no_such_table WHERE name = ?", "secret-value")
	if err == nil {
		t.Fatal("expected the read to fail")
	}
	failures := c.failures()
	if len(failures) != 1 {
		t.Fatalf("want 1 QueryFailed, got %d", len(failures))
	}
	// QueryFailed carries no Bindings field at all; assert the value did
	// not leak into the statement text either.
	if strings.Contains(failures[0].Query, "secret-value") {
		t.Errorf("bound value leaked into QueryFailed.Query: %q", failures[0].Query)
	}
}

// TestInstrumentation_OneEventPerBuilderStatement guards against the double
// emission the call-site sweep exists to prevent: the builder no longer
// dispatches, so each statement must produce exactly one event from the driver
// layer.
func TestInstrumentation_OneEventPerBuilderStatement(t *testing.T) {
	m, c := setupInstrumentedTest(t)
	seedUser(t, m, "alice", "a@example.com", 20)
	seedUser(t, m, "bob", "b@example.com", 30)
	c.reset()

	users, err := Model[TestUser]{}.Where("age > ?", 0).Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(users) != 2 {
		t.Fatalf("got %d users, want 2", len(users))
	}

	events := c.matching("SELECT")
	if len(events) != 1 {
		t.Fatalf("want exactly 1 QueryExecuted for one builder SELECT, got %d", len(events))
	}
	if events[0].RowsAffected != 2 {
		t.Errorf("RowsAffected = %d, want 2", events[0].RowsAffected)
	}
}

// TestInstrumentation_RepeatedRowsCloseEmitsOnce covers the emit-once guard:
// database/sql closes a result set on exhaustion, and an explicit Close (or a
// deferred one) closes it again.
func TestInstrumentation_RepeatedRowsCloseEmitsOnce(t *testing.T) {
	m, c := setupInstrumentedTest(t)
	seedUser(t, m, "alice", "a@example.com", 20)
	c.reset()

	rows, err := m.Raw(context.Background(), "SELECT name FROM test_users")
	if err != nil {
		t.Fatalf("Raw: %v", err)
	}
	for rows.Next() { // drains to exhaustion, which closes the set
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	// Close twice more on top of the implicit close.
	_ = rows.Close()
	_ = rows.Close()

	if got := len(c.matching("SELECT name FROM test_users")); got != 1 {
		t.Fatalf("want exactly 1 QueryExecuted across repeated closes, got %d", got)
	}
}

// TestInstrumentation_NoDispatcherEmitsNothing covers the fast path: with no
// dispatcher wired the observer reports it is not observing, and statements run
// without being recorded.
func TestInstrumentation_NoDispatcherEmitsNothing(t *testing.T) {
	m, c := setupInstrumentedTest(t)
	m.SetEventDispatcher(nil)

	if _, err := m.Exec(context.Background(),
		"INSERT INTO test_users (name, email, age) VALUES (?, ?, ?)", "carol", "c@example.com", 40); err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if got := len(c.matching("INSERT")); got != 0 {
		t.Errorf("events dispatched with no dispatcher wired: %d", got)
	}
}

// newSingleConnManager returns a manager whose pool holds exactly one
// connection, with a table to query. MaxOpenConns=1 is what turns "a listener
// runs while the connection is still checked out" from a latent hazard into a
// hang.
func newSingleConnManager(t *testing.T) *Manager {
	t.Helper()
	m, err := NewManager(ManagerConfig{
		Driver:       "sqlite",
		Database:     ":memory:",
		MaxOpenConns: 1,
		MaxIdleConns: 1,
	})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
	if _, err := m.Exec(context.Background(),
		`CREATE TABLE items (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT)`); err != nil {
		t.Fatalf("create table: %v", err)
	}
	if _, err := m.Exec(context.Background(), `INSERT INTO items (name) VALUES (?)`, "one"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	return m
}

// TestInstrumentation_ListenerQueryingSamePoolDoesNotDeadlock is the
// regression test for observing statements from inside a database/sql driver
// callback.
//
// The callback holds the connection's lock and runs before the connection
// returns to the pool. A listener that queries the same pool from there waits
// for a connection that cannot be freed until the listener returns; with
// MaxOpenConns=1 that is a permanent hang, and the query that triggered the
// event never completes. Delivering events off the callback is what makes this
// safe.
func TestInstrumentation_ListenerQueryingSamePoolDoesNotDeadlock(t *testing.T) {
	m := newSingleConnManager(t)

	var (
		reentered   atomic.Bool
		nestedErr   = make(chan error, 1)
		listenerHit = make(chan struct{}, 8)
	)
	m.SetEventDispatcher(func(_ context.Context, ev any) error {
		q, ok := ev.(*QueryExecuted)
		if !ok {
			return nil
		}
		listenerHit <- struct{}{}
		// Only the first listener invocation re-enters, otherwise the
		// nested query's own event would recurse forever.
		if !strings.Contains(q.SQL, "SELECT name FROM items") || !reentered.CompareAndSwap(false, true) {
			return nil
		}
		var name string
		nestedErr <- m.DB().QueryRowContext(context.Background(),
			"SELECT name FROM items LIMIT 1").Scan(&name)
		return nil
	})

	// The query itself must not block. If observation ever moves back
	// inside the driver callback this send never happens and the test
	// fails on the timeout rather than hanging the suite.
	done := make(chan error, 1)
	go func() {
		var name string
		done <- m.DB().QueryRowContext(context.Background(),
			"SELECT name FROM items WHERE id = ?", 1).Scan(&name)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("outer query: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("outer query blocked: statement observation is holding the connection")
	}

	select {
	case err := <-nestedErr:
		if err != nil {
			t.Fatalf("listener query on the same pool failed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("listener query on the same pool never completed")
	}
}

// TestInstrumentation_EventsBindToOwningManager covers the routing fix: a
// manager reports through its own dispatcher without any SetDefault call, and
// two managers never cross-dispatch.
func TestInstrumentation_EventsBindToOwningManager(t *testing.T) {
	// No SetDefault anywhere in this test. Deliberately: resolving a
	// process-wide default at dispatch time meant a directly constructed
	// manager emitted nothing, and statements from one manager could
	// dispatch through another's listener.
	prev := Default()
	ResetDefault()
	t.Cleanup(func() { SetDefault(prev) })

	newM := func(table string) (*Manager, *stmtCollector) {
		m, err := NewManager(ManagerConfig{Driver: "sqlite", Database: ":memory:"})
		if err != nil {
			t.Fatalf("NewManager: %v", err)
		}
		t.Cleanup(func() { _ = m.Shutdown(context.Background()) })
		if _, err := m.Exec(context.Background(),
			"CREATE TABLE "+table+" (id INTEGER PRIMARY KEY AUTOINCREMENT)"); err != nil {
			t.Fatalf("create %s: %v", table, err)
		}
		c := &stmtCollector{m: m}
		m.SetEventDispatcher(c.dispatch)
		c.reset()
		return m, c
	}

	a, ca := newM("alpha")
	b, cb := newM("beta")

	if _, err := a.Exec(context.Background(), "INSERT INTO alpha DEFAULT VALUES"); err != nil {
		t.Fatalf("insert alpha: %v", err)
	}
	if _, err := b.Exec(context.Background(), "INSERT INTO beta DEFAULT VALUES"); err != nil {
		t.Fatalf("insert beta: %v", err)
	}

	// Each manager saw its own statement without SetDefault being involved.
	if got := len(ca.matching("INSERT INTO alpha")); got != 1 {
		t.Errorf("manager A: want 1 event for its own statement, got %d", got)
	}
	if got := len(cb.matching("INSERT INTO beta")); got != 1 {
		t.Errorf("manager B: want 1 event for its own statement, got %d", got)
	}
	// And neither saw the other's.
	if got := len(ca.matching("INSERT INTO beta")); got != 0 {
		t.Errorf("manager A received %d events belonging to manager B", got)
	}
	if got := len(cb.matching("INSERT INTO alpha")); got != 0 {
		t.Errorf("manager B received %d events belonging to manager A", got)
	}
}

// TestInstrumentation_ConcurrentDispatcherToggleKeepsFlagConsistent covers the
// lost-update hazard in SetEventDispatcher.
//
// The observer consults hasDispatcher on the hot path to decide whether to
// record a statement, so that flag must never disagree with the dispatcher it
// stands for. Updating it outside the lock allowed a call installing a
// dispatcher to be overtaken by one clearing it and then stamp the flag back
// to true, leaving statements recorded against a manager with no dispatcher
// and silently discarded at delivery.
//
// Both writes happen under the write lock, so any reader holding the read lock
// must see them agree. That is what this asserts, from a goroutine reading
// concurrently with the togglers: checking only after the togglers finish
// would leave a window of a few nanoseconds and miss the bug.
func TestInstrumentation_ConcurrentDispatcherToggleKeepsFlagConsistent(t *testing.T) {
	m, err := NewManager(ManagerConfig{Driver: "sqlite", Database: ":memory:"})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Shutdown(context.Background()) })

	dispatch := func(context.Context, any) error { return nil }

	var (
		stop       atomic.Bool
		violations atomic.Int64
		checkers   sync.WaitGroup
	)
	for i := 0; i < 4; i++ {
		checkers.Add(1)
		go func() {
			defer checkers.Done()
			for !stop.Load() {
				m.mu.RLock()
				installed := m.eventDispatcher != nil
				flag := m.hasDispatcher.Load()
				m.mu.RUnlock()
				if flag != installed {
					violations.Add(1)
				}
				runtime.Gosched()
			}
		}()
	}

	var togglers sync.WaitGroup
	for round := 0; round < 500; round++ {
		for i := 0; i < 8; i++ {
			togglers.Add(1)
			go func(i int) {
				defer togglers.Done()
				if i%2 == 0 {
					m.SetEventDispatcher(dispatch)
					return
				}
				m.SetEventDispatcher(nil)
			}(i)
		}
		togglers.Wait()
	}
	stop.Store(true)
	checkers.Wait()

	if got := violations.Load(); got != 0 {
		t.Fatalf("observed %d moments where hasDispatcher disagreed with the installed dispatcher; "+
			"statements would be recorded and then discarded", got)
	}

	m.mu.RLock()
	installed := m.eventDispatcher != nil
	m.mu.RUnlock()
	if flag := m.hasDispatcher.Load(); flag != installed {
		t.Fatalf("final state: hasDispatcher=%t but a dispatcher is installed=%t", flag, installed)
	}
}

// TestInstrumentation_DispatcherToggledBackOnStillDelivers guards the other
// side of the toggle: turning telemetry off and on again must leave a working
// pump rather than a flag pointing at nothing.
func TestInstrumentation_DispatcherToggledBackOnStillDelivers(t *testing.T) {
	m, c := setupInstrumentedTest(t)

	m.SetEventDispatcher(nil)
	if _, err := m.Exec(context.Background(),
		"INSERT INTO test_users (name, email, age) VALUES (?, ?, ?)", "off", "off@example.com", 1); err != nil {
		t.Fatalf("Exec while disabled: %v", err)
	}

	m.SetEventDispatcher(c.dispatch)
	if _, err := m.Exec(context.Background(),
		"INSERT INTO test_users (name, email, age) VALUES (?, ?, ?)", "on", "on@example.com", 2); err != nil {
		t.Fatalf("Exec after re-enabling: %v", err)
	}

	events := c.matching("INSERT INTO test_users")
	if len(events) != 1 {
		t.Fatalf("want exactly the statement run while enabled, got %d events", len(events))
	}
	if len(events[0].Bindings) == 0 || events[0].Bindings[0] != "on" {
		t.Errorf("delivered the wrong statement: bindings=%#v", events[0].Bindings)
	}
}
