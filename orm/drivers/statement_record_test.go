package drivers

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/contract"
)

// A scripted database/sql driver whose behaviour each DSN selects, for the
// statement routes SQLite never takes: a connection that declines direct
// execution (driver.ErrSkip, as MySQL does without InterpolateParams) and
// a result set whose next set fails.

// script is what one DSN's connections do.
type script struct {
	// skipDirect makes ExecContext and QueryContext decline with
	// driver.ErrSkip, so database/sql prepares the statement instead.
	skipDirect bool
	// legacyPrepare leaves out ConnPrepareContext, so the connection only
	// has driver.Conn's Prepare.
	legacyPrepare bool
	// prepareErr fails every prepare.
	prepareErr error
	// nextResultSetErr fails the move to a second result set.
	nextResultSetErr error
	// nextErr fails the read after the first row, in place of io.EOF.
	nextErr error
	// closeErr fails every result set's Close.
	closeErr error
	// result, when set, is what every direct exec returns.
	result driver.Result
}

var (
	scriptsMu      sync.Mutex
	scripts        = map[string]*script{}
	registerScript sync.Once
)

const scriptDriverName = "velocity-scripted"

type scriptDriver struct{}

func (scriptDriver) Open(dsn string) (driver.Conn, error) {
	scriptsMu.Lock()
	s := scripts[dsn]
	scriptsMu.Unlock()
	if s == nil {
		return nil, fmt.Errorf("no script for %q", dsn)
	}
	c := &scriptConn{s: s}
	if s.legacyPrepare {
		return c, nil
	}
	return &scriptCtxConn{scriptConn: c}, nil
}

type scriptConn struct{ s *script }

func (c *scriptConn) Prepare(query string) (driver.Stmt, error) {
	if c.s.prepareErr != nil {
		return nil, c.s.prepareErr
	}
	return &scriptStmt{s: c.s}, nil
}

func (c *scriptConn) Close() error              { return nil }
func (c *scriptConn) Begin() (driver.Tx, error) { return nil, errors.New("not supported") }

func (c *scriptConn) ExecContext(context.Context, string, []driver.NamedValue) (driver.Result, error) {
	if c.s.skipDirect {
		return nil, driver.ErrSkip
	}
	if c.s.result != nil {
		return c.s.result, nil
	}
	return driver.RowsAffected(1), nil
}

func (c *scriptConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	if c.s.skipDirect {
		return nil, driver.ErrSkip
	}
	return &scriptRows{s: c.s}, nil
}

type scriptCtxConn struct{ *scriptConn }

func (c *scriptCtxConn) PrepareContext(_ context.Context, query string) (driver.Stmt, error) {
	return c.Prepare(query)
}

type scriptStmt struct{ s *script }

func (s *scriptStmt) Close() error  { return nil }
func (s *scriptStmt) NumInput() int { return -1 }
func (s *scriptStmt) Exec([]driver.Value) (driver.Result, error) {
	return driver.RowsAffected(1), nil
}
func (s *scriptStmt) Query([]driver.Value) (driver.Rows, error) { return &scriptRows{s: s.s}, nil }

// scriptRows yields one row, then reports a second result set whose move
// fails with nextResultSetErr.
type scriptRows struct {
	s    *script
	done bool
}

func (r *scriptRows) Columns() []string { return []string{"n"} }
func (r *scriptRows) Close() error      { return r.s.closeErr }
func (r *scriptRows) Next(dest []driver.Value) error {
	if r.done {
		if r.s.nextErr != nil {
			return r.s.nextErr
		}
		return io.EOF
	}
	r.done = true
	dest[0] = int64(1)
	return nil
}
func (r *scriptRows) HasNextResultSet() bool { return r.s.nextResultSetErr != nil }
func (r *scriptRows) NextResultSet() error {
	if r.s.nextResultSetErr != nil {
		return r.s.nextResultSetErr
	}
	return io.EOF
}

// openScripted opens an instrumented pool over s with an observer and the
// query log on, returning both recorders.
func openScripted(t testing.TB, s *script) (*sql.DB, *observerBinding, *statementRecorder, *queryLog) {
	t.Helper()
	registerScript.Do(func() { sql.Register(scriptDriverName, scriptDriver{}) })
	dsn := t.Name()
	scriptsMu.Lock()
	scripts[dsn] = s
	scriptsMu.Unlock()
	t.Cleanup(func() {
		scriptsMu.Lock()
		delete(scripts, dsn)
		scriptsMu.Unlock()
	})
	db, binding, err := openInstrumented(scriptDriverName, "scripted", dsn)
	if err != nil {
		t.Fatalf("openInstrumented: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	rec := &statementRecorder{}
	binding.set(rec)
	log := &queryLog{}
	var holder atomic.Value
	holder.Store(queryLoggerHolder{Logger: log})
	binding.logQueries = true
	binding.logger = &holder
	return db, binding, rec, log
}

// assertOneFailure fails unless exactly one failed statement was recorded
// for query, with want as its error, and one "query failed" line carrying
// argCount, or no arg_count at all when argCount is -1.
func assertOneFailure(t *testing.T, rec *statementRecorder, log *queryLog, query string, want error, argCount int) {
	t.Helper()
	evs := rec.all()
	if len(evs) != 1 {
		t.Fatalf("events = %+v, want 1", evs)
	}
	if ev := evs[0]; !errors.Is(ev.Err, want) || ev.SQL != query || ev.Args != nil {
		t.Errorf("event = {SQL:%q Err:%v Args:%v}, want {SQL:%q Err:%v Args:nil}", ev.SQL, ev.Err, ev.Args, query, want)
	}
	lines := log.all()
	if len(lines) != 1 || lines[0].msg != "velocity/orm: query failed" {
		t.Fatalf("lines = %+v, want one %q line", lines, "velocity/orm: query failed")
	}
	if got, ok := kvOK(lines[0].kvs, "arg_count"); argCount < 0 && ok {
		t.Errorf("arg_count = %v, want none: the prepare's operation is unknown", got)
	} else if argCount >= 0 && got != argCount {
		t.Errorf("arg_count = %v, want %d", got, argCount)
	}
	if got := kv(lines[0].kvs, "query"); got != query {
		t.Errorf("query = %v, want %q", got, query)
	}
}

// A connection that declines direct execution (driver.ErrSkip) sends
// database/sql down the prepared path; a prepare that then fails is the
// statement's failure and is recorded once, through the one statement
// exit. Its line leaves arg_count out: the prepare cannot be tied to the
// operation it serves (see TestPrepareFailure_TakesNoArgCountFromAnotherOperation).
func TestPrepareFailureAfterSkip_IsRecordedOnce(t *testing.T) {
	prepErr := errors.New("Error 1146 (42S02): Table 'app.missing' doesn't exist")
	const query = "SELECT n FROM missing WHERE a = ? AND b = ?"
	for _, legacy := range []bool{false, true} {
		for _, route := range []string{"exec", "query"} {
			t.Run(fmt.Sprintf("%s/legacy prepare %t", route, legacy), func(t *testing.T) {
				db, _, rec, log := openScripted(t, &script{skipDirect: true, legacyPrepare: legacy, prepareErr: prepErr})
				var err error
				if route == "exec" {
					_, err = db.ExecContext(context.Background(), query, "hunter2", 7)
				} else {
					var rows *sql.Rows
					rows, err = db.QueryContext(context.Background(), query, "hunter2", 7)
					if rows != nil {
						_ = rows.Close()
					}
				}
				if !errors.Is(err, prepErr) {
					t.Fatalf("%s error = %v, want the prepare failure", route, err)
				}
				assertOneFailure(t, rec, log, query, prepErr, -1)
			})
		}
	}
}

// An explicit prepare that fails is recorded once too, with no arg_count; a prepare that succeeds is silent until the statement runs.
func TestExplicitPrepare_FailureRecordedSuccessSilent(t *testing.T) {
	prepErr := errors.New("syntax error at or near \"SELEC\"")
	t.Run("failure", func(t *testing.T) {
		db, _, rec, log := openScripted(t, &script{prepareErr: prepErr})
		if _, err := db.PrepareContext(context.Background(), "SELEC 1"); !errors.Is(err, prepErr) {
			t.Fatalf("prepare error = %v, want the prepare failure", err)
		}
		assertOneFailure(t, rec, log, "SELEC 1", prepErr, -1)
	})
	t.Run("success", func(t *testing.T) {
		db, _, rec, log := openScripted(t, &script{})
		stmt, err := db.PrepareContext(context.Background(), "SELECT 1")
		if err != nil {
			t.Fatalf("prepare: %v", err)
		}
		defer stmt.Close()
		if evs, lines := rec.all(), log.all(); len(evs) != 0 || len(lines) != 0 {
			t.Errorf("after a successful prepare: events %+v, lines %+v, want none", evs, lines)
		}
	})
}

// The non-context Prepare on the wrapped connection records its failure
// the same way.
func TestInstrumentedConnPrepare_FailureRecordedOnce(t *testing.T) {
	prepErr := errors.New("prepare refused")
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy prepare %t", legacy), func(t *testing.T) {
			_, binding, rec, log := openScripted(t, &script{legacyPrepare: legacy, prepareErr: prepErr})
			inner, err := scriptDriver{}.Open(t.Name())
			if err != nil {
				t.Fatalf("open: %v", err)
			}
			c := &instrumentedConn{inner: inner, binding: binding}
			if _, err := c.Prepare("SELECT 1"); !errors.Is(err, prepErr) {
				t.Fatalf("Prepare error = %v, want the prepare failure", err)
			}
			assertOneFailure(t, rec, log, "SELECT 1", prepErr, -1)
		})
	}
}

// Control-flow sentinels from a prepare are not failures: database/sql
// retries driver.ErrBadConn on a fresh connection, so no record is made.
func TestPrepareBadConn_IsNotRecorded(t *testing.T) {
	db, _, rec, log := openScripted(t, &script{skipDirect: true, prepareErr: driver.ErrBadConn})
	_, _ = db.ExecContext(context.Background(), "SELECT 1")
	if evs, lines := rec.all(), log.all(); len(evs) != 0 || len(lines) != 0 {
		t.Errorf("events %+v, lines %+v, want none", evs, lines)
	}
}

// A result set whose move to its next set fails is a failed statement,
// even when closing it then succeeds.
func TestNextResultSetFailure_IsRecordedAsAFailure(t *testing.T) {
	nrsErr := errors.New("Error 1644 (45000): raised after the first result set")
	const query = "CALL two_sets(?)"
	db, _, rec, log := openScripted(t, &script{nextResultSetErr: nrsErr})
	rows, err := db.QueryContext(context.Background(), query, "hunter2")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for rows.Next() {
	}
	if rows.NextResultSet() {
		t.Fatal("NextResultSet = true, want false")
	}
	if !errors.Is(rows.Err(), nrsErr) {
		t.Fatalf("rows.Err = %v, want the next-result-set failure", rows.Err())
	}
	_ = rows.Close()
	assertOneFailure(t, rec, log, query, nrsErr, 1)
}

// Running out of result sets (io.EOF) is not a failure.
func TestNextResultSetEOF_IsASuccess(t *testing.T) {
	db, _, rec, _ := openScripted(t, &script{})
	rows, err := db.QueryContext(context.Background(), "SELECT n")
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	for rows.Next() {
	}
	_ = rows.NextResultSet()
	_ = rows.Close()
	evs := rec.all()
	if len(evs) != 1 || evs[0].Err != nil || evs[0].RowsAffected != 1 {
		t.Errorf("events = %+v, want one completed statement with 1 row", evs)
	}
}

// ConnectionConfig.Logger is the query logger from the first statement the
// driver runs while it connects; nil keeps a logger installed earlier.
func TestConnectionConfigLogger_TakesTheConnectStatements(t *testing.T) {
	t.Run("set", func(t *testing.T) {
		log := &queryLog{}
		d := NewSQLiteDriver()
		if err := d.Connect(ConnectionConfig{Database: ":memory:", LogQueries: true, Logger: log}); err != nil {
			t.Fatalf("Connect: %v", err)
		}
		t.Cleanup(func() { _ = d.Close() })
		var pragmas int
		for _, e := range log.all() {
			if q, _ := kv(e.kvs, "query").(string); strings.HasPrefix(q, "PRAGMA") {
				pragmas++
			}
		}
		if pragmas != 2 {
			t.Errorf("PRAGMA lines = %d, want 2: %+v", pragmas, log.all())
		}
	})
	t.Run("nil keeps an earlier logger", func(t *testing.T) {
		log := &queryLog{}
		d := NewSQLiteDriver()
		d.(contract.LoggerAware).SetLogger(log)
		if err := d.Connect(ConnectionConfig{Database: ":memory:", LogQueries: true}); err != nil {
			t.Fatalf("Connect: %v", err)
		}
		t.Cleanup(func() { _ = d.Close() })
		if len(log.all()) != 2 {
			t.Errorf("lines = %+v, want the two PRAGMA lines", log.all())
		}
	})
}

// A prepare cannot be tied to the operation it serves: database/sql runs a
// declined statement's fallback prepare under a second hold of the
// connection, and another operation on the same *sql.Conn can run its own
// prepare of the same query in between (sql.go execDC, then
// ctxDriverPrepare). So a failed prepare's line leaves arg_count out rather
// than borrow the count of whichever statement the connection last
// declined.
func TestPrepareFailure_TakesNoArgCountFromAnotherOperation(t *testing.T) {
	prepErr := errors.New("prepare refused")
	const query = "SELECT n FROM t WHERE a = ? AND b = ?"
	_, binding, rec, log := openScripted(t, &script{skipDirect: true, prepareErr: prepErr})
	inner, err := scriptDriver{}.Open(t.Name())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	c := &instrumentedConn{inner: inner, binding: binding}
	args := []driver.NamedValue{{Ordinal: 1, Value: "hunter2"}, {Ordinal: 2, Value: int64(7)}}
	// Operation A: declined with two arguments; its prepare has not run.
	if _, err := c.ExecContext(context.Background(), query, args); err != driver.ErrSkip {
		t.Fatalf("A exec error = %v, want driver.ErrSkip", err)
	}
	// Operation B: an explicit prepare of the same query, which fails.
	if _, err := c.PrepareContext(context.Background(), query); !errors.Is(err, prepErr) {
		t.Fatalf("B prepare error = %v, want the prepare failure", err)
	}
	assertOneFailure(t, rec, log, query, prepErr, -1)
}
