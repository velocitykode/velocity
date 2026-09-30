package queue

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/errchain"
	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/trace"

	sqlite3 "github.com/mattn/go-sqlite3"
)

// hostileCtx is a caller's context whose method on is user code: it runs
// a test's hostile code, then answers as the context it wraps.
// database/sql calls Done on the context it is given, so a driver that
// handed it the caller's context under its lock ran this under the lock.
type hostileCtx struct {
	context.Context
	on   string
	code *hostile.Code
}

func (c hostileCtx) run(method string) {
	if c.on == method {
		c.code.Run()
	}
}

func (c hostileCtx) Done() <-chan struct{} { c.run("Done"); return c.Context.Done() }
func (c hostileCtx) Err() error            { c.run("Err"); return c.Context.Err() }
func (c hostileCtx) Value(key any) any     { c.run("Value"); return c.Context.Value(key) }

// closeUnlessHung closes a test's database unless the test failed: a
// failed test may have left a call hung on the driver's lock holding a
// connection, and closing the database would wait for it for good.
func closeUnlessHung(t *testing.T, cleanup func()) {
	if !t.Failed() {
		cleanup()
	}
}

// TestDatabaseDriver_LockedPaths_CallerContextIsUserCode sweeps every
// driver entry point that runs statements under the driver's lock with a
// caller context whose Done, Err or Value panics, blocks, or calls back
// into the driver. The statements run on a context the driver owns, built before
// the lock is taken: a Done re-entering the driver returns, a blocked one
// holds up no other call on the driver, and a panicking one reaches the
// caller and leaves the driver usable.
func TestDatabaseDriver_LockedPaths_CallerContextIsUserCode(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	type entry struct {
		name string
		// prepare runs with a plain context; call runs with the hostile
		// one.
		prepare func(t *testing.T, d *DatabaseDriver) ReservationToken
		call    func(ctx context.Context, d *DatabaseDriver, token ReservationToken) error
	}
	bg := context.Background()
	push := func(t *testing.T, d *DatabaseDriver) {
		t.Helper()
		if err := d.PushCtx(bg, &TestJob{ID: "1"}, "q"); err != nil {
			t.Fatalf("PushCtx: %v", err)
		}
	}
	reserve := func(t *testing.T, d *DatabaseDriver) ReservationToken {
		t.Helper()
		push(t, d)
		_, token, _, err := d.PopCtxReserved(bg, "q")
		if err != nil || token.IsZero() {
			t.Fatalf("PopCtxReserved: %v, %v", token, err)
		}
		return token
	}
	entries := []entry{
		{"PushIfNotExistsCtx", func(t *testing.T, d *DatabaseDriver) ReservationToken {
			for _, ddl := range JobDedupeMigrationSQL("sqlite") {
				if _, err := d.db.Exec(ddl); err != nil {
					t.Fatalf("job_dedupe schema: %v", err)
				}
			}
			return ReservationToken{}
		}, func(ctx context.Context, d *DatabaseDriver, _ ReservationToken) error {
			return d.PushIfNotExistsCtx(ctx, &TestJob{ID: "1"}, "key", "q")
		}},
		{"PopCtxReserved", func(t *testing.T, d *DatabaseDriver) ReservationToken { push(t, d); return ReservationToken{} },
			func(ctx context.Context, d *DatabaseDriver, _ ReservationToken) error {
				job, token, _, err := d.PopCtxReserved(ctx, "q")
				if err == nil && (job == nil || token.IsZero()) {
					err = errors.New("no job reserved")
				}
				return err
			}},
		{"PopCtx", func(t *testing.T, d *DatabaseDriver) ReservationToken { push(t, d); return ReservationToken{} },
			func(ctx context.Context, d *DatabaseDriver, _ ReservationToken) error {
				job, err := d.PopCtx(ctx, "q")
				if err == nil && job == nil {
					err = errors.New("no job popped")
				}
				return err
			}},
		{"AckCtx", reserve, func(ctx context.Context, d *DatabaseDriver, token ReservationToken) error {
			return d.AckCtx(ctx, token)
		}},
		{"ReleaseCtx", reserve, func(ctx context.Context, d *DatabaseDriver, token ReservationToken) error {
			return d.ReleaseCtx(ctx, token, 0)
		}},
		{"FailReservedCtx", reserve, func(ctx context.Context, d *DatabaseDriver, token ReservationToken) error {
			return d.FailReservedCtx(ctx, token, &TestJob{ID: "1"}, errors.New("job failed"), "q")
		}},
	}
	for _, e := range entries {
		for _, method := range []string{"Done", "Err", "Value"} {
			for _, mode := range hostile.Modes() {
				t.Run(e.name+"/"+method+"/"+mode.String(), func(t *testing.T) {
					d, cleanup := newSQLiteQueueDB(t)
					defer closeUnlessHung(t, cleanup)
					token := e.prepare(t, d)
					var reentered []error
					code := hostile.New(t, mode, func() {
						_, err := d.Size("other")
						reentered = append(reentered, err, d.Clear("other"))
					})
					ctx, cancel := context.WithCancel(bg)
					defer cancel()
					type result struct {
						err      error
						panicked any
					}
					done := make(chan result, 1)
					go func() { //safe-goroutine: the call under test; its result and panic are read below
						var r result
						defer func() {
							r.panicked = recover()
							done <- r
						}()
						r.err = e.call(hostileCtx{ctx, method, code}, d, token)
					}()
					if mode == hostile.Block {
						if !code.AwaitEntered(t) {
							return
						}
						hostile.Within(t, hostile.Deadline, func() {
							if err := d.Clear("other"); err != nil {
								t.Errorf("Clear while Done blocks: %v", err)
							}
						})
						code.Release()
					}
					var r result
					hostile.Within(t, hostile.Deadline, func() { r = <-done })
					if code.Calls() == 0 {
						// The entry point never called this method: nothing
						// hostile ran, and the call must simply succeed.
						if r.err != nil || r.panicked != nil {
							t.Fatalf("%s = %v, panic %v; want success", e.name, r.err, r.panicked)
						}
						return
					}
					switch mode {
					case hostile.Panic:
						if r.panicked != hostile.PanicValue {
							t.Fatalf("panic = %v, want %s's", r.panicked, method)
						}
					default:
						if r.err != nil || r.panicked != nil {
							t.Fatalf("%s = %v, panic %v; want success", e.name, r.err, r.panicked)
						}
					}
					for i, err := range reentered {
						if err != nil {
							t.Errorf("re-entered call %d: %v", i, err)
						}
					}
					// The driver is usable afterwards.
					hostile.Within(t, hostile.Deadline, func() {
						if err := d.Clear("q"); err != nil {
							t.Errorf("a later Clear: %v", err)
						}
						push(t, d)
						if job, err := d.PopCtx(bg, "q"); err != nil || job == nil {
							t.Errorf("a later push and pop = %v, %v", job, err)
						}
					})
				})
			}
		}
	}
}

// hostileTextError is an error whose Error method is user code.
type hostileTextError struct{ code *hostile.Code }

func (e hostileTextError) Error() string {
	e.code.Run()
	return "hostile failure"
}

// TestDatabaseDriver_FailReserved_ErrorTextIsUserCode records a failed
// reservation whose error's Error method panics, blocks, or calls back into
// the driver. The text is read, contained, before the worker-path lock: a
// re-entrant Error returns, a blocked one holds up no other call on the
// driver, and a panicking one is recorded with errchain.Unreadable.
func TestDatabaseDriver_FailReserved_ErrorTextIsUserCode(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)
	bg := context.Background()
	for _, mode := range hostile.Modes() {
		t.Run(mode.String(), func(t *testing.T) {
			d, cleanup := newSQLiteQueueDB(t)
			defer closeUnlessHung(t, cleanup)
			if err := d.PushCtx(bg, &TestJob{ID: "1"}, "q"); err != nil {
				t.Fatalf("PushCtx: %v", err)
			}
			_, token, _, err := d.PopCtxReserved(bg, "q")
			if err != nil || token.IsZero() {
				t.Fatalf("PopCtxReserved: %v, %v", token, err)
			}
			var reentered error
			code := hostile.New(t, mode, func() { reentered = d.Clear("other") })
			type result struct {
				err      error
				panicked any
			}
			done := make(chan result, 1)
			go func() { //safe-goroutine: the call under test; its result and panic are read below
				var r result
				defer func() {
					r.panicked = recover()
					done <- r
				}()
				r.err = d.FailReservedCtx(bg, token, &TestJob{ID: "1"}, hostileTextError{code}, "q")
			}()
			if mode == hostile.Block {
				if !code.AwaitEntered(t) {
					return
				}
				hostile.Within(t, hostile.Deadline, func() {
					if err := d.Clear("other"); err != nil {
						t.Errorf("Clear while Error blocks: %v", err)
					}
				})
				code.Release()
			}
			var r result
			hostile.Within(t, hostile.Deadline, func() { r = <-done })
			if code.Calls() == 0 {
				t.Fatal("the error's Error never ran")
			}
			if r.err != nil || r.panicked != nil || reentered != nil {
				t.Fatalf("FailReservedCtx = %v, panic %v (re-entered: %v)", r.err, r.panicked, reentered)
			}
			var exception string
			if err := d.db.QueryRow("SELECT exception FROM failed_jobs").Scan(&exception); err != nil {
				t.Fatalf("read failed_jobs: %v", err)
			}
			want := "hostile failure"
			if mode == hostile.Panic {
				want = errchain.Unreadable
			}
			if exception != want {
				t.Fatalf("recorded exception = %q, want %q", exception, want)
			}
		})
	}
}

// TestDatabaseDriver_LockedPaths_CallerCancellationStillBounds cancels, or
// lets expire, the caller's context while a pop waits under the worker
// lock for the pool's one connection, held by the test: the context the
// driver owns follows the caller's, so the pop returns the caller's error.
func TestDatabaseDriver_LockedPaths_CallerCancellationStillBounds(t *testing.T) {
	for _, tc := range []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want error
	}{
		{"cancel", func() (context.Context, context.CancelFunc) { return context.WithCancel(context.Background()) }, context.Canceled},
		{"deadline", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 50*time.Millisecond)
		}, context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, cleanup := newSQLiteQueueDB(t)
			defer cleanup()
			held, err := d.db.Conn(context.Background())
			if err != nil {
				t.Fatalf("Conn: %v", err)
			}
			defer held.Close()
			waits := d.db.Stats().WaitCount
			ctx, cancel := tc.ctx()
			defer cancel()
			var popErr error
			var wg sync.WaitGroup
			wg.Go(func() { _, _, _, popErr = d.PopCtxReserved(ctx, "q") })
			hostile.Eventually(t, hostile.Deadline, "the pop waiting for the connection", func() bool {
				return d.db.Stats().WaitCount > waits
			})
			if tc.want == context.Canceled {
				cancel()
			}
			hostile.Within(t, hostile.Deadline, wg.Wait)
			if !errors.Is(popErr, tc.want) {
				t.Fatalf("PopCtxReserved = %v, want %v", popErr, tc.want)
			}
		})
	}
}

// recordingDriver wraps the sqlite driver and records the trace id each
// ExecContext is handed: what a statement observer under the driver's
// lock sees.
type recordingDriver struct {
	inner driver.Driver
	mu    sync.Mutex
	seen  []string
}

func (r *recordingDriver) Open(name string) (driver.Conn, error) {
	c, err := r.inner.Open(name)
	if err != nil {
		return nil, err
	}
	return recordingConn{c, r}, nil
}

type recordingConn struct {
	driver.Conn
	r *recordingDriver
}

func (c recordingConn) ExecContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Result, error) {
	c.r.mu.Lock()
	c.r.seen = append(c.r.seen, trace.GetTraceID(ctx))
	c.r.mu.Unlock()
	return c.Conn.(driver.ExecerContext).ExecContext(ctx, query, args)
}

func (c recordingConn) BeginTx(ctx context.Context, opts driver.TxOptions) (driver.Tx, error) {
	return c.Conn.(driver.ConnBeginTx).BeginTx(ctx, opts)
}

func (c recordingConn) QueryContext(ctx context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	return c.Conn.(driver.QueryerContext).QueryContext(ctx, query, args)
}

// recordingDrivers numbers the registered recording drivers: database/sql
// refuses a name twice, and -count runs the test again.
var recordingDrivers atomic.Int64

// TestDatabaseDriver_Ack_CarriesTheJobsTraceIDs acks with the job's trace
// context: the statement runs on the context the driver owns, which
// carries the caller's trace id to the database driver under the lock.
func TestDatabaseDriver_Ack_CarriesTheJobsTraceIDs(t *testing.T) {
	rec := &recordingDriver{inner: &sqlite3.SQLiteDriver{}}
	name := fmt.Sprintf("sqlite3-recording-%d", recordingDrivers.Add(1))
	sql.Register(name, rec)
	db, err := sql.Open(name, "file:"+t.TempDir()+"/q.db?_busy_timeout=5000")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	for _, ddl := range []string{
		`CREATE TABLE jobs (id INTEGER PRIMARY KEY AUTOINCREMENT, queue TEXT NOT NULL, payload TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0, scheduled_at DATETIME NOT NULL, reserved_at DATETIME, reserved_by TEXT,
			failed_at DATETIME, failed_reason TEXT, created_at DATETIME NOT NULL, updated_at DATETIME NOT NULL)`,
	} {
		if _, err := db.Exec(ddl); err != nil {
			t.Fatalf("schema: %v", err)
		}
	}
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)
	d := NewDatabaseDriver(db, "sqlite")
	bg := context.Background()
	if err := d.PushCtx(bg, &TestJob{ID: "1"}, "q"); err != nil {
		t.Fatalf("PushCtx: %v", err)
	}
	_, token, _, err := d.PopCtxReserved(bg, "q")
	if err != nil || token.IsZero() {
		t.Fatalf("PopCtxReserved: %v, %v", token, err)
	}
	rec.mu.Lock()
	rec.seen = nil
	rec.mu.Unlock()
	ctx, cancel := context.WithCancel(trace.WithFullContext(bg, "trace-ack", "span-ack", "parent-ack"))
	defer cancel()
	if err := d.AckCtx(ctx, token); err != nil {
		t.Fatalf("AckCtx: %v", err)
	}
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if len(rec.seen) != 1 || rec.seen[0] != "trace-ack" {
		t.Fatalf("trace ids the database driver saw = %q, want [trace-ack]", rec.seen)
	}
}
