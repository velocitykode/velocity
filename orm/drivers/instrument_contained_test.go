package drivers

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
	"github.com/velocitykode/velocity/internal/ownctx"
)

// panickingResult is a driver result whose RowsAffected is hostile.
type panickingResult struct{}

func (panickingResult) LastInsertId() (int64, error) { return 0, nil }
func (panickingResult) RowsAffected() (int64, error) { panic(hostile.PanicValue) }

// countLines counts the log lines with msg.
func countLines(log *queryLog, msg string) int {
	n := 0
	for _, l := range log.all() {
		if l.msg == msg {
			n++
		}
	}
	return n
}

// The driver's RowsAffected is called by the instrumentation on the
// statement's behalf: its panic is contained, reported once, and the
// statement, which ran, returns its result and records zero rows.
func TestResultRows_DriverPanicContainedAndReportedOnce(t *testing.T) {
	db, _, rec, log := openScripted(t, &script{result: panickingResult{}})
	res, err := db.ExecContext(context.Background(), "UPDATE t SET a = 1")
	if err != nil || res == nil {
		t.Fatalf("ExecContext = %v, %v; want the statement's result", res, err)
	}
	evs := rec.all()
	if len(evs) != 1 || evs[0].Err != nil || evs[0].RowsAffected != 0 {
		t.Fatalf("events = %+v, want one completed statement with zero rows", evs)
	}
	if n := countLines(log, "velocity/orm: driver result RowsAffected panicked"); n != 1 {
		t.Errorf("RowsAffected panic reports = %d, want 1", n)
	}
	if n := countLines(log, "velocity/orm: query executed"); n != 1 {
		t.Errorf("query lines = %d, want 1", n)
	}
}

// hostileObserver runs code in the method on names, then records.
type hostileObserver struct {
	statementRecorder
	on        string
	code      *hostile.Code
	observing atomic.Int64
}

func (o *hostileObserver) Observing() bool {
	o.observing.Add(1)
	if o.on == "Observing" {
		o.code.Run()
	}
	return true
}

func (o *hostileObserver) ObserveStatement(ev StatementEvent) {
	if o.on == "ObserveStatement" {
		o.code.Run()
	}
	o.statementRecorder.ObserveStatement(ev)
}

// A panicking Observing counts as not observing: the statement runs and
// is logged, no event is recorded, and the panic is reported once per
// statement. A panicking ObserveStatement is reported once too.
func TestObserverMethods_PanicContainedAndReportedOnce(t *testing.T) {
	for _, on := range []string{"Observing", "ObserveStatement"} {
		t.Run(on, func(t *testing.T) {
			db, binding, _, log := openScripted(t, &script{})
			obs := &hostileObserver{on: on, code: hostile.New(t, hostile.Panic, nil)}
			binding.set(obs)
			const statements = 3
			for i := 0; i < statements; i++ {
				if _, err := db.ExecContext(context.Background(), "UPDATE t SET a = 1"); err != nil {
					t.Fatalf("ExecContext: %v", err)
				}
			}
			if got := countLines(log, "velocity/orm: statement observer panicked"); got != statements || obs.code.Calls() != statements {
				t.Errorf("panic reports = %d for %d panics, want one per panic (%d)", got, obs.code.Calls(), statements)
			}
			if n := countLines(log, "velocity/orm: query executed"); n != statements {
				t.Errorf("query lines = %d, want %d", n, statements)
			}
			if on == "Observing" && len(obs.all()) != 0 {
				t.Errorf("events = %+v, want none: a panicking Observing is not observing", obs.all())
			}
		})
	}
}

// A statement run on a holding context (ownctx.Hold) asks the observer
// nothing and reports nothing until the Held is released: then the
// observer is asked whether it is observing, receives the event, and the
// line is written, on the releasing goroutine. After the release, a
// statement on the same context reports as it runs.
func TestHeldStatement_ReportsAtRelease(t *testing.T) {
	db, binding, _, log := openScripted(t, &script{})
	obs := &hostileObserver{}
	binding.set(obs)
	ctx, held := ownctx.Hold(context.Background())

	if _, err := db.ExecContext(ctx, "UPDATE t SET a = ?", 1); err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	var n int64
	if err := db.QueryRowContext(ctx, "SELECT n FROM t").Scan(&n); err != nil {
		t.Fatalf("QueryRowContext: %v", err)
	}
	if obs.observing.Load() != 0 || len(obs.all()) != 0 || len(log.all()) != 0 {
		t.Fatalf("before Release: Observing called %d time(s), events %+v, lines %+v; want none",
			obs.observing.Load(), obs.all(), log.all())
	}

	held.Release()
	evs := obs.all()
	if len(evs) != 2 || evs[0].SQL != "UPDATE t SET a = ?" || evs[1].SQL != "SELECT n FROM t" {
		t.Fatalf("after Release: events %+v, want the exec then the query", evs)
	}
	if len(evs[0].Args) != 1 || evs[1].RowsAffected != 1 {
		t.Errorf("events carry args %v and rows %d, want 1 arg and 1 row", evs[0].Args, evs[1].RowsAffected)
	}
	if got := obs.observing.Load(); got != 2 {
		t.Errorf("Observing called %d time(s) at Release, want 2", got)
	}
	if n := countLines(log, "velocity/orm: query executed"); n != 2 {
		t.Errorf("query lines = %d, want 2", n)
	}

	if _, err := db.ExecContext(ctx, "UPDATE t SET a = 2"); err != nil {
		t.Fatalf("ExecContext after Release: %v", err)
	}
	if len(obs.all()) != 3 {
		t.Errorf("a statement after Release reported %d event(s) in all, want 3", len(obs.all()))
	}
}

// An observer that stops observing before the Held is released gets no
// event, and the bound values are dropped; the line is still written.
func TestHeldStatement_ObserverAskedAtRelease(t *testing.T) {
	db, binding, _, log := openScripted(t, &script{})
	obs := &togglingObserver{}
	obs.on.Store(true)
	binding.set(obs)
	ctx, held := ownctx.Hold(context.Background())
	if _, err := db.ExecContext(ctx, "UPDATE t SET a = ?", "secret"); err != nil {
		t.Fatalf("ExecContext: %v", err)
	}
	obs.on.Store(false)
	held.Release()
	if len(obs.all()) != 0 {
		t.Errorf("events = %+v, want none: the observer stopped observing", obs.all())
	}
	if n := countLines(log, "velocity/orm: query executed"); n != 1 {
		t.Errorf("query lines = %d, want 1", n)
	}
}

type togglingObserver struct {
	statementRecorder
	on atomic.Bool
}

func (o *togglingObserver) Observing() bool { return o.on.Load() }

// Statements on a holding context race its Release from many goroutines:
// every statement is reported exactly once, before or at the release
// (run with -race -cpu 1,2).
func TestHeldStatement_ConcurrentRelease(t *testing.T) {
	db, binding, _, _ := openScripted(t, &script{})
	obs := &hostileObserver{}
	binding.set(obs)
	for round := 0; round < 20; round++ {
		before := len(obs.all())
		ctx, held := ownctx.Hold(context.Background())
		var wg sync.WaitGroup
		const workers, each = 4, 5
		for w := 0; w < workers; w++ {
			wg.Add(1)
			go func() { //safe-goroutine: a test worker joined by wg
				defer wg.Done()
				for i := 0; i < each; i++ {
					if _, err := db.ExecContext(ctx, "UPDATE t SET a = 1"); err != nil {
						t.Errorf("ExecContext: %v", err)
					}
				}
			}()
		}
		wg.Add(1)
		go func() { //safe-goroutine: a test worker joined by wg
			defer wg.Done()
			held.Release()
		}()
		wg.Wait()
		held.Release()
		if got := len(obs.all()) - before; got != workers*each {
			t.Fatalf("round %d: %d events, want %d", round, got, workers*each)
		}
	}
}
