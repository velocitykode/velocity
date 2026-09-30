package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/velocitykode/velocity/internal/hostile"
	ormdrivers "github.com/velocitykode/velocity/orm/drivers"
)

// lockProbeObserver is a statement observer whose methods are user code:
// each runs the Code the test arms, after noting whether the driver's
// lock was held when it was called.
type lockProbeObserver struct {
	d *DatabaseDriver
	// on names the method that runs the armed code: "Observing" or
	// "ObserveStatement".
	on   string
	code atomic.Pointer[hostile.Code]
	// underLock counts the calls made while d.mu was held.
	underLock atomic.Int64
}

func (o *lockProbeObserver) probe(method string) {
	if o.d.mu.TryLock() {
		o.d.mu.Unlock()
	} else {
		o.underLock.Add(1)
	}
	if o.on == method {
		o.code.Load().Run()
	}
}

func (o *lockProbeObserver) Observing() bool { o.probe("Observing"); return true }

func (o *lockProbeObserver) ObserveStatement(ormdrivers.StatementEvent) {
	o.probe("ObserveStatement")
}

// newObservedQueueDB is newSQLiteQueueDB over a pool the ORM's drivers
// instrument, its statements reported to the observer observer builds for
// the driver, and the pool's query log.
func newObservedQueueDB(t *testing.T, observer func(d *DatabaseDriver) ormdrivers.StatementObserver) (*DatabaseDriver, *hostile.Logger, func()) {
	t.Helper()
	log := hostile.NewLogger(nil)
	base := &ormdrivers.BaseDriver{Config: ormdrivers.ConnectionConfig{Logger: log}}
	d, cleanup := newSQLiteQueueDBOpenedBy(t, func(dsn string) (*sql.DB, error) {
		return base.OpenInstrumented("sqlite3", "sqlite", dsn)
	})
	base.SetStatementObserver(observer(d))
	return d, log, cleanup
}

// reenteringObserver calls back into the driver from every report: it
// sizes the queue, which takes no lock, and every eighth report clears
// another queue, which takes d.mu. A report run under d.mu by the same
// goroutine deadlocks on the Clear.
type reenteringObserver struct {
	d       *DatabaseDriver
	reports atomic.Int64
	// armed is set once the test's own setup statements, which run
	// outside the lock, have run.
	armed atomic.Bool
}

func (o *reenteringObserver) Observing() bool { return o.armed.Load() }

func (o *reenteringObserver) ObserveStatement(ev ormdrivers.StatementEvent) {
	// Size's own count is not re-entered from: Size runs no statement
	// under the lock, so its report runs inside the statement, where a
	// call back into a one-connection pool waits for good (the
	// StatementObserver contract). Every other statement here is one of
	// the lock-held paths', whose report must run off the lock and after
	// the statement: run inline under d.mu, the Clear below deadlocks.
	if strings.Contains(ev.SQL, "COUNT(*)") {
		return
	}
	if o.reports.Add(1)%8 == 0 {
		_ = o.d.Clear("other")
		return
	}
	_, _ = o.d.Size("other")
}

// TestDatabaseDriver_LockedPaths_StatementObserverIsUserCode sweeps every
// driver entry point that runs statements under the driver's lock, over an
// instrumented pool whose statement observer's Observing or
// ObserveStatement panics, blocks, or calls back into the driver. The
// statements run on a holding context (ownctx.Hold), so the observer runs
// after the lock is released: it never sees the lock held, a re-entrant
// call returns, a blocked one holds up no other call on the driver, and a
// panic is contained, reported once, and leaves the call's result alone.
func TestDatabaseDriver_LockedPaths_StatementObserverIsUserCode(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)

	type entry struct {
		name    string
		prepare func(t *testing.T, d *DatabaseDriver) ReservationToken
		call    func(d *DatabaseDriver, token ReservationToken) error
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
		}, func(d *DatabaseDriver, _ ReservationToken) error {
			return d.PushIfNotExistsCtx(bg, &TestJob{ID: "1"}, "key", "q")
		}},
		{"PopCtxReserved", func(t *testing.T, d *DatabaseDriver) ReservationToken { push(t, d); return ReservationToken{} },
			func(d *DatabaseDriver, _ ReservationToken) error {
				job, token, _, err := d.PopCtxReserved(bg, "q")
				if err == nil && (job == nil || token.IsZero()) {
					err = errors.New("no job reserved")
				}
				return err
			}},
		{"AckCtx", reserve, func(d *DatabaseDriver, token ReservationToken) error {
			return d.AckCtx(bg, token)
		}},
		{"ReleaseCtx", reserve, func(d *DatabaseDriver, token ReservationToken) error {
			return d.ReleaseCtx(bg, token, 0)
		}},
		{"FailReservedCtx", reserve, func(d *DatabaseDriver, token ReservationToken) error {
			return d.FailReservedCtx(bg, token, &TestJob{ID: "1"}, errors.New("job failed"), "q")
		}},
		{"Clear", func(t *testing.T, d *DatabaseDriver) ReservationToken { push(t, d); return ReservationToken{} },
			func(d *DatabaseDriver, _ ReservationToken) error { return d.Clear("q") }},
	}
	for _, e := range entries {
		for _, method := range []string{"Observing", "ObserveStatement"} {
			for _, mode := range hostile.Modes() {
				t.Run(e.name+"/"+method+"/"+mode.String(), func(t *testing.T) {
					var obs *lockProbeObserver
					d, log, cleanup := newObservedQueueDB(t, func(d *DatabaseDriver) ormdrivers.StatementObserver {
						obs = &lockProbeObserver{d: d, on: method}
						return obs
					})
					defer closeUnlessHung(t, cleanup)
					token := e.prepare(t, d)
					var reentered []error
					code := hostile.New(t, mode, func() {
						_, err := d.Size("other")
						reentered = append(reentered, err, d.Clear("other"))
					})
					obs.underLock.Store(0)
					obs.code.Store(code)
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
						r.err = e.call(d, token)
					}()
					if mode == hostile.Block {
						if !code.AwaitEntered(t) {
							return
						}
						// The observer blocks the call's goroutine, not the
						// driver: another call takes the lock and returns.
						code.Disarm()
						hostile.Within(t, hostile.Deadline, func() {
							if err := d.Clear("other"); err != nil {
								t.Errorf("Clear while the observer blocks: %v", err)
							}
						})
						code.Release()
					}
					var r result
					hostile.Within(t, hostile.Deadline, func() { r = <-done })
					obs.code.Store(nil)
					if code.Calls() == 0 {
						t.Fatalf("%s ran no statement the observer saw", e.name)
					}
					if r.err != nil || r.panicked != nil {
						t.Fatalf("%s = %v, panic %v; want success", e.name, r.err, r.panicked)
					}
					if n := obs.underLock.Load(); n != 0 {
						t.Errorf("the observer ran %d time(s) with the driver's lock held", n)
					}
					if mode == hostile.Panic {
						// One report per panic: every call of the armed
						// method panicked once.
						if got, want := log.Count(hostile.Error, "velocity/orm: statement observer panicked"), code.Calls(); got != want {
							t.Errorf("observer panic reports = %d, want %d (one per panic)", got, want)
						}
					}
					for i, err := range reentered {
						if err != nil {
							t.Errorf("re-entered call %d: %v", i, err)
						}
					}
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

// TestDatabaseDriver_StatementObserver_ConcurrentWorkers runs pushes, pops,
// and acks on many goroutines over an instrumented pool whose observer
// calls back into the driver from every statement (reenteringObserver).
// With the observer run off the lock, the workers finish (run with -race
// -cpu 1,2).
func TestDatabaseDriver_StatementObserver_ConcurrentWorkers(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)
	var obs *reenteringObserver
	d, _, cleanup := newObservedQueueDB(t, func(d *DatabaseDriver) ormdrivers.StatementObserver {
		obs = &reenteringObserver{d: d}
		return obs
	})
	defer closeUnlessHung(t, cleanup)

	for _, ddl := range JobDedupeMigrationSQL("sqlite") {
		if _, err := d.db.Exec(ddl); err != nil {
			t.Fatalf("job_dedupe schema: %v", err)
		}
	}
	obs.armed.Store(true)
	bg := context.Background()
	const workers, rounds = 4, 20
	var wg sync.WaitGroup
	errs := make(chan error, workers*rounds)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() { //safe-goroutine: a worker; its errors are read below
			defer wg.Done()
			for i := 0; i < rounds; i++ {
				if err := d.PushIfNotExistsCtx(bg, &TestJob{ID: "1"}, fmt.Sprintf("w%d-%d", w, i), "q"); err != nil {
					errs <- err
					return
				}
				_, token, _, err := d.PopCtxReserved(bg, "q")
				if err != nil {
					errs <- err
					return
				}
				if !token.IsZero() {
					if err := d.AckCtx(bg, token); err != nil && !errors.Is(err, ErrLeaseLost) {
						errs <- err
						return
					}
				}
			}
		}()
	}
	hostile.Within(t, hostile.Deadline, wg.Wait)
	close(errs)
	for err := range errs {
		t.Errorf("worker: %v", err)
	}
	if obs.reports.Load() == 0 {
		t.Fatal("the observer saw no statement")
	}
}

// recordingProbe records each statement report's SQL and whether d.mu was
// held when it ran.
type recordingProbe struct {
	d         *DatabaseDriver
	mu        sync.Mutex
	sqls      []string
	underLock int
}

func (p *recordingProbe) Observing() bool { return true }

func (p *recordingProbe) ObserveStatement(ev ormdrivers.StatementEvent) {
	locked := !p.d.mu.TryLock()
	if !locked {
		p.d.mu.Unlock()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.sqls = append(p.sqls, ev.SQL)
	if locked {
		p.underLock++
	}
}

func (p *recordingProbe) count(fragment string) (n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, s := range p.sqls {
		if strings.Contains(s, fragment) {
			n++
		}
	}
	return n
}

// A panic inside the lock-held section still delivers the reports of the
// statements that ran before it: the Held is released on a defer, which
// runs after the lock's deferred unlock, so each statement's event and log
// line land once, off the lock, while the panic unwinds to the caller.
func TestDatabaseDriver_PanicUnderLock_StillDeliversHeldReports(t *testing.T) {
	saveAndRestoreSigningState(t)
	SetSigningKey(nil)
	log := hostile.NewLogger(nil)
	base := &ormdrivers.BaseDriver{Config: ormdrivers.ConnectionConfig{Logger: log, LogQueries: true}}
	d, cleanup := newSQLiteQueueDBOpenedBy(t, func(dsn string) (*sql.DB, error) {
		return base.OpenInstrumented("sqlite3", "sqlite", dsn)
	})
	defer closeUnlessHung(t, cleanup)
	now := time.Now().UTC()
	if _, err := d.db.Exec(
		`INSERT INTO jobs (queue, payload, attempts, scheduled_at, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?)`,
		"q", "not-json{{", 0, now.Add(-time.Second), now, now,
	); err != nil {
		t.Fatalf("insert poison row: %v", err)
	}
	probe := &recordingProbe{d: d}
	base.SetStatementObserver(probe)
	// The quarantine's commit hook runs under the worker-path lock, after
	// the quarantine's delete and insert ran.
	restore := setPopQuarantineCommitHookForTest(func() { panic("component panic under the lock") })
	t.Cleanup(restore)

	p := hostile.Within(t, hostile.Deadline, func() { _, _, _, _ = d.PopCtxReserved(context.Background(), "q") })
	if p != "component panic under the lock" {
		t.Fatalf("PopCtxReserved panic = %v, want the hook's", p)
	}
	if got := probe.count("INSERT INTO failed_jobs"); got != 1 {
		t.Errorf("failed_jobs insert reported %d time(s), want 1", got)
	}
	if got := probe.count("DELETE FROM jobs"); got != 1 {
		t.Errorf("quarantine delete reported %d time(s), want 1", got)
	}
	if probe.underLock != 0 {
		t.Errorf("%d report(s) ran with d.mu held", probe.underLock)
	}
	lines := 0
	for _, l := range log.Lines() {
		for i := 0; i+1 < len(l.KVs); i += 2 {
			if l.KVs[i] == "query" && strings.Contains(l.KVs[i+1].(string), "INSERT INTO failed_jobs") {
				lines++
			}
		}
	}
	if lines != 1 {
		t.Errorf("failed_jobs insert logged %d line(s), want 1", lines)
	}
	// The driver is usable: the lock was released.
	hostile.Within(t, hostile.Deadline, func() {
		if err := d.Clear("q"); err != nil {
			t.Errorf("a later Clear: %v", err)
		}
	})
}
