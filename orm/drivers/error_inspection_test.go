package drivers

import (
	"context"
	"database/sql/driver"
	"io"
	"testing"

	"github.com/velocitykode/velocity/internal/hostile"
)

// skipPanics is a driver error whose Is method panics when asked about
// driver.ErrSkip, the check only the instrumentation makes: database/sql
// itself asks only about driver.ErrBadConn and compares io.EOF by
// identity, so the panic reaches nothing but the wrapper's own check.
type skipPanics struct{}

func (skipPanics) Error() string { return "driver failure" }
func (skipPanics) Is(target error) bool {
	if target == driver.ErrSkip {
		panic("driver error's Is panicked")
	}
	return false
}

// selfLoop unwraps to itself: a chain with a cycle.
type selfLoop struct{}

func (e *selfLoop) Error() string { return "loop" }
func (e *selfLoop) Unwrap() error { return e }

// A result set that fails mid-read is closed by database/sql from the
// caller's rows.Next; the failure's control-error check runs inside that
// Close. A driver error whose Is panics there is contained: the statement
// is recorded as failed, the read returns the error, and the connection
// goes back to the pool, so the next statement on a one-connection pool
// runs.
func TestRowsFailure_PanickingIsDoesNotEscapeOrLeakTheConnection(t *testing.T) {
	db, _, rec, _ := openScripted(t, &script{nextErr: skipPanics{}})
	db.SetMaxOpenConns(1)
	const query = "SELECT n FROM t"
	p := hostile.Within(t, hostile.Deadline, func() {
		rows, err := db.QueryContext(context.Background(), query)
		if err != nil {
			t.Errorf("QueryContext: %v", err)
			return
		}
		for rows.Next() {
		}
		if err := rows.Err(); err != (skipPanics{}) {
			t.Errorf("rows.Err() = %v, want the driver's error", err)
		}
	})
	if p != nil {
		t.Fatalf("the read panicked: %v", p)
	}
	evs := rec.all()
	if len(evs) != 1 || evs[0].Err != (skipPanics{}) || evs[0].SQL != query {
		t.Fatalf("events = %+v, want one failure carrying the driver's error", evs)
	}
	hostile.Within(t, hostile.Deadline, func() {
		if _, err := db.ExecContext(context.Background(), "UPDATE t SET n = 1"); err != nil {
			t.Errorf("the next statement failed: %v", err)
		}
	})
}

// database/sql closes a result set whose context ended on a goroutine of
// its own. A driver Close error whose Is panics in the control-error check
// is contained there: the process survives and the statement is recorded
// as failed.
func TestRowsCleanup_PanickingIsIsContainedOnTheCleanupGoroutine(t *testing.T) {
	hostile.Isolated(t, func() {
		db, _, rec, _ := openScripted(t, &script{closeErr: skipPanics{}})
		ctx, cancel := context.WithCancel(context.Background())
		rows, err := db.QueryContext(ctx, "SELECT n FROM t")
		if err != nil {
			t.Fatalf("QueryContext: %v", err)
		}
		t.Cleanup(func() { _ = rows.Close() })
		cancel()
		hostile.Eventually(t, hostile.Deadline, "the cleanup goroutine recorded the statement", func() bool {
			return len(rec.all()) == 1
		})
		if ev := rec.all()[0]; ev.Err != (skipPanics{}) {
			t.Fatalf("event error = %v, want the driver's Close error", ev.Err)
		}
	})
}

// The control-error check ends on a chain that loops back on itself.
func TestIsControlErr_CyclicChainEnds(t *testing.T) {
	var got bool
	hostile.Within(t, hostile.Deadline, func() { got = isControlErr(&selfLoop{}) })
	if got {
		t.Fatal("a cyclic chain is a control error")
	}
	for _, err := range []error{driver.ErrSkip, driver.ErrBadConn, io.ErrUnexpectedEOF} {
		want := err != io.ErrUnexpectedEOF
		if got := isControlErr(err); got != want {
			t.Errorf("isControlErr(%v) = %v, want %v", err, got, want)
		}
	}
}
